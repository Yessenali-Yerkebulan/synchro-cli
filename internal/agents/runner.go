package agents

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/synchro/synchro/internal/llm"
	"github.com/synchro/synchro/internal/model"
	"github.com/synchro/synchro/internal/repo"
	"github.com/synchro/synchro/internal/store"
)

// Engine executes agents against providers. It owns no global state, so the
// CLI, the REPL and the tests can each hold their own.
type Engine struct {
	Store    *store.Store
	Searcher Searcher
	HTTP     *http.Client

	// OnDelta receives streamed text as it arrives.
	OnDelta func(text string)
	// OnStage fires when a pipeline stage starts or finishes, for progress UI.
	OnStage func(stage Stage, phase string)
	// AutoCommit overrides the workspace setting when non-nil.
	AutoCommit *bool
}

// Stage is one step of a multi-agent pipeline.
type Stage struct {
	Key   string
	Label string
	Agent *model.Agent
	Input string
	// Note explains why a stage has no agent, when Agent is nil.
	Note string
}

// Phase values passed to OnStage.
const (
	PhaseStart  = "start"
	PhaseFinish = "finish"
	PhaseFail   = "fail"
)

// Config is the effective runtime configuration.
func (e *Engine) Config() store.Config { return e.Store.Config() }

// ResolveAPIKey finds a provider key: the stored credential first, then the
// conventional environment variable. This is the only place keys are read.
func (e *Engine) ResolveAPIKey(provider string) string {
	provider = strings.ToLower(provider)
	if c, ok := e.Store.Credential(provider); ok && strings.TrimSpace(c.APIKey) != "" {
		return strings.TrimSpace(c.APIKey)
	}
	if p, ok := llm.Lookup(provider); ok && p.KeyEnv != "" {
		if v := strings.TrimSpace(os.Getenv(p.KeyEnv)); v != "" {
			return v
		}
	}
	return ""
}

// baseURL resolves a provider's endpoint, honouring a user override so a
// self-hosted proxy or a different-region endpoint can be pointed at without
// code changes.
func (e *Engine) baseURL(provider string) string {
	provider = strings.ToLower(provider)
	if provider == "ollama" {
		if u := strings.TrimSpace(e.Config().OllamaURL); u != "" {
			return u
		}
	}
	// SYNCHRO_<PROVIDER>_URL, e.g. SYNCHRO_OPENROUTER_URL.
	if v := strings.TrimSpace(os.Getenv("SYNCHRO_" + strings.ToUpper(provider) + "_URL")); v != "" {
		return v
	}
	if p, ok := llm.Lookup(provider); ok {
		return p.BaseURL
	}
	return ""
}

// BuildPrompt assembles the user turn. Ported from
// synchro/app/tasks/agent_tasks.py:_build_prompt.
func BuildPrompt(title, description, searchContext, extraInstructions string) string {
	var b strings.Builder
	b.WriteString(searchContext)
	fmt.Fprintf(&b, "Task: %s\n", title)
	if description != "" {
		fmt.Fprintf(&b, "Description: %s\n", description)
	}
	b.WriteString("\nPlease analyze and complete this task. Provide a detailed response.")
	b.WriteString(extraInstructions)
	return b.String()
}

// RunTask executes one task with one agent, persists the outcome, and returns
// the result. It never returns an error for a provider failure that was
// successfully recorded: the caller gets the result and can inspect Status.
func (e *Engine) RunTask(ctx context.Context, task *model.Task, agent *model.Agent) (*model.TaskResult, error) {
	if agent == nil {
		return nil, errors.New("no agent assigned to task")
	}

	cfg := e.Config()
	provider := strings.ToLower(agent.Provider)
	model_ := agent.Model
	if model_ == "" {
		model_ = cfg.Model
	}

	temp := agent.Temperature
	if temp == 0 {
		temp = cfg.Temperature
	}
	if temp == 0 {
		temp = 0.7
	}

	// Ground RESEARCHER agents in live results. Best-effort: a search failure
	// degrades the answer, it does not fail the task.
	var searchResults []SearchResult
	if agent.Role == model.RoleResearcher && cfg.Search {
		searchResults = Search(ctx, e.Searcher, searchQuery(task), 5)
	}
	searchContext := FormatSearchContext(searchResults)

	// DEVELOPER agents are asked to lay their answer out as named files.
	extra := ""
	if agent.Role == model.RoleDeveloper {
		extra = DeveloperFileInstructions
	}

	start := time.Now()
	res, err := llm.Call(ctx, llm.Request{
		Provider:    provider,
		Model:       model_,
		APIKey:      e.ResolveAPIKey(provider),
		System:      agent.SystemPrompt,
		Prompt:      BuildPrompt(task.Title, task.Description, searchContext, extra),
		Temperature: temp,
		MaxTokens:   cfg.MaxTokens,
		Stream:      cfg.Stream,
		OnDelta:     e.OnDelta,
		BaseURL:     e.baseURL(provider),
		HTTPClient:  e.HTTP,
		Timeout:     time.Duration(cfg.RequestTimeout) * time.Second,
	})
	if err != nil {
		return nil, err
	}

	result := &model.TaskResult{
		Output:          res.Output,
		Provider:        res.Provider,
		Model:           res.Model,
		TokensUsed:      res.TokensUsed,
		TokensEstimated: res.TokensEstimated,
		DurationSeconds: res.Duration.Seconds(),
	}
	for _, r := range searchResults {
		result.Sources = append(result.Sources, model.Source{Title: r.Title, URL: r.URL})
	}

	// A DEVELOPER answer in FILE: format is split into a summary plus files.
	if agent.Role == model.RoleDeveloper {
		summary, files := ParseCodeFiles(res.Output)
		if len(files) > 0 {
			result.Output = summary
			if result.Output == "" {
				result.Output = fmt.Sprintf("Generated %d file(s).", len(files))
			}
			for _, f := range files {
				result.Files = append(result.Files, model.CodeFile{Path: f.Path, Content: f.Content})
			}
			info, unchanged := e.maybeCommit(task, agent, result.Files)
			if info != nil {
				result.Repo = info
			}
			result.RepoUnchanged = unchanged
		}
	}

	_ = start
	return result, nil
}

// maybeCommit writes a DEVELOPER agent's files into the project repo when
// auto-commit is on, and returns the commit info. Failures are swallowed: the
// files on the result remain the source of truth, and a git problem should not
// turn a successful run into a failed task.
func (e *Engine) maybeCommit(task *model.Task, agent *model.Agent, files []model.CodeFile) (*model.RepoInfo, bool) {
	enabled := false
	if e.AutoCommit != nil {
		enabled = *e.AutoCommit
	} else if team, err := e.Store.Team(agent.TeamID, ""); err == nil {
		if ws, err := e.Store.Workspace(team.WorkspaceID); err == nil {
			enabled = ws.AutoCommitAgentCode
		}
	}
	if !enabled {
		return nil, false
	}
	info, err := repo.WriteFiles(e.Store.ReposDir(), task.ProjectID, files, task.Title)
	if err != nil {
		return nil, false
	}
	if info == nil {
		// Auto-commit is on but the files came back identical, so there was
		// nothing to commit. Say so rather than implying a commit is pending.
		return nil, true
	}
	return &model.RepoInfo{Commit: info.Commit, Path: info.Path}, false
}

// CommitFiles commits a task's previously generated files on demand, which is
// the CLI equivalent of the web app's "Approve & Commit" button.
func (e *Engine) CommitFiles(taskID string) (*model.RepoInfo, error) {
	task, err := e.Store.Task(taskID)
	if err != nil {
		return nil, err
	}
	if task.Result == nil || len(task.Result.Files) == 0 {
		return nil, errors.New("this task has no generated files to commit")
	}
	info, err := repo.WriteFiles(e.Store.ReposDir(), task.ProjectID, task.Result.Files, task.Title)
	if err != nil {
		return nil, err
	}
	if info == nil {
		return nil, errors.New("nothing to commit (files were unchanged)")
	}
	task.Result.Repo = &model.RepoInfo{Commit: info.Commit, Path: info.Path}
	_ = e.Store.UpdateTask(task)
	return task.Result.Repo, nil
}

// searchQuery builds the query a RESEARCHER agent grounds itself on.
func searchQuery(task *model.Task) string {
	q := strings.TrimSpace(task.Title)
	if d := strings.TrimSpace(task.Description); d != "" {
		q += " " + firstSentence(d)
	}
	return q
}

func firstSentence(s string) string {
	for i, r := range s {
		if r == '.' || r == '\n' {
			return s[:i]
		}
	}
	if len(s) > 200 {
		return s[:200]
	}
	return s
}
