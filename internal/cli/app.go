// Package cli implements the command surface and the interactive shell.
package cli

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/synchro/synchro/internal/agents"
	"github.com/synchro/synchro/internal/llm"
	"github.com/synchro/synchro/internal/model"
	"github.com/synchro/synchro/internal/store"
	"github.com/synchro/synchro/internal/ui"
	"golang.org/x/term"
)

// App is the shared runtime for every command: the store, the printer, and the
// resolved "where am I" context (workspace → team → agent → project).
type App struct {
	Store *store.Store
	P     *ui.Printer
	Ctx   context.Context

	// Flags, filled by the root command's persistent flags.
	FlagWorkspace string
	FlagTeam      string
	FlagAgent     string
	FlagProject   string
}

// NewApp opens the store and builds the printer.
func NewApp(dir string) (*App, error) {
	s, err := store.Open(dir)
	if err != nil {
		return nil, err
	}
	a := &App{Store: s, Ctx: context.Background(), P: ui.New(s.Config().Color)}
	return a, nil
}

// NewAppWithPrinter is NewApp with an injected printer, for tests.
func NewAppWithPrinter(dir string, p *ui.Printer) (*App, error) {
	s, err := store.Open(dir)
	if err != nil {
		return nil, err
	}
	return &App{Store: s, Ctx: context.Background(), P: p}, nil
}

// SetContext replaces the cancellation context.
func (a *App) SetContext(ctx context.Context) { a.Ctx = ctx }

// ---------------------------------------------------------------------------
// context resolution
// ---------------------------------------------------------------------------

// Scope is the resolved working set for a command.
type Scope struct {
	Workspace *model.Workspace
	Team      *model.Team
	Agent     *model.Agent
	Project   *model.Project
}

// Empty reports whether nothing at all was resolved.
func (s Scope) Empty() bool {
	return s.Workspace == nil && s.Team == nil && s.Agent == nil && s.Project == nil
}

// RequireWorkspace resolves the active workspace or explains how to create one.
func (a *App) RequireWorkspace() (*model.Workspace, error) {
	w, err := a.resolveWorkspace()
	if err != nil {
		return nil, fmt.Errorf("no workspace selected. Create one with:  synchro ws new <name>")
	}
	return w, nil
}

// RequireTeam resolves the active team.
func (a *App) RequireTeam() (*model.Team, error) {
	if _, err := a.RequireWorkspace(); err != nil {
		return nil, err
	}
	t, err := a.resolveTeam()
	if err != nil {
		return nil, fmt.Errorf("no team selected in this workspace. Create one with:  synchro team new <name>  (or  synchro team from <template>)")
	}
	return t, nil
}

// RequireAgent resolves the active agent.
func (a *App) RequireAgent() (*model.Agent, error) {
	if _, err := a.RequireTeam(); err != nil {
		return nil, err
	}
	ag, err := a.resolveAgent()
	if err != nil {
		return nil, fmt.Errorf("no agent selected. Pick one with:  synchro agent use <name-or-number>")
	}
	return ag, nil
}

// resolveWorkspace picks a workspace from, in order: the --workspace flag, the
// remembered active one, or the only workspace if there is exactly one.
func (a *App) resolveWorkspace() (*model.Workspace, error) {
	if a.FlagWorkspace != "" {
		return a.Store.Workspace(a.FlagWorkspace)
	}
	cfg := a.Store.Config()
	if cfg.ActiveWorkspaceID != "" {
		if w, err := a.Store.Workspace(cfg.ActiveWorkspaceID); err == nil {
			return w, nil
		}
	}
	all := a.Store.Workspaces()
	if len(all) == 1 {
		return &all[0], nil
	}
	if len(all) == 0 {
		return nil, fmt.Errorf("no workspaces yet")
	}
	return nil, fmt.Errorf("several workspaces exist; choose one with --workspace")
}

func (a *App) resolveTeam() (*model.Team, error) {
	ws, err := a.resolveWorkspace()
	if err != nil {
		return nil, err
	}
	if a.FlagTeam != "" {
		return a.Store.Team(a.FlagTeam, ws.ID)
	}
	if cfg := a.Store.Config(); cfg.ActiveTeamID != "" {
		if t, err := a.Store.Team(cfg.ActiveTeamID, ws.ID); err == nil {
			return t, nil
		}
	}
	teams := a.Store.Teams(ws.ID)
	if len(teams) == 1 {
		return &teams[0], nil
	}
	if len(teams) == 0 {
		return nil, fmt.Errorf("no teams in this workspace")
	}
	return nil, fmt.Errorf("several teams exist; choose one with --team")
}

func (a *App) resolveAgent() (*model.Agent, error) {
	team, err := a.resolveTeam()
	if err != nil {
		return nil, err
	}
	if a.FlagAgent != "" {
		return a.Store.Agent(a.FlagAgent, team.ID)
	}
	if cfg := a.Store.Config(); cfg.ActiveAgentID != "" {
		if ag, err := a.Store.Agent(cfg.ActiveAgentID, team.ID); err == nil {
			return ag, nil
		}
	}
	agents := a.Store.Agents(team.ID)
	if len(agents) == 1 {
		return &agents[0], nil
	}
	if len(agents) == 0 {
		return nil, fmt.Errorf("this team has no agents")
	}
	return nil, fmt.Errorf("several agents exist; choose one with --agent")
}

// resolveProject returns the active project, or nil when none is selected.
// A project is optional: many operations work fine without one.
func (a *App) resolveProject() (*model.Project, error) {
	ws, err := a.resolveWorkspace()
	if err != nil {
		return nil, err
	}
	if a.FlagProject != "" {
		return a.Store.Project(a.FlagProject, ws.ID)
	}
	if cfg := a.Store.Config(); cfg.ActiveProjectID != "" {
		if p, err := a.Store.Project(cfg.ActiveProjectID, ws.ID); err == nil {
			return p, nil
		}
	}
	projects := a.Store.Projects(ws.ID)
	if len(projects) == 1 {
		return &projects[0], nil
	}
	return nil, nil
}

// Resolve returns everything that could be resolved, without failing. The
// interactive shell uses it to draw a context bar.
func (a *App) Resolve() Scope {
	var s Scope
	s.Workspace, _ = a.resolveWorkspace()
	s.Team, _ = a.resolveTeam()
	s.Agent, _ = a.resolveAgent()
	s.Project, _ = a.resolveProject()
	return s
}

// SetActive remembers a context selection for the next invocation.
func (a *App) SetActive(workspace, team, agent, project string) error {
	return a.Store.SetConfig(func(c *store.Config) {
		if workspace != "" {
			c.ActiveWorkspaceID = workspace
		}
		if team != "" {
			c.ActiveTeamID = team
		}
		if agent != "" {
			c.ActiveAgentID = agent
		}
		if project != "" {
			c.ActiveProjectID = project
		}
	})
}

// ClearActive forgets a remembered selection.
func (a *App) ClearActive(what string) error {
	return a.Store.SetConfig(func(c *store.Config) {
		switch what {
		case "workspace":
			c.ActiveWorkspaceID = ""
			c.ActiveTeamID = ""
			c.ActiveAgentID = ""
		case "team":
			c.ActiveTeamID = ""
			c.ActiveAgentID = ""
		case "agent":
			c.ActiveAgentID = ""
		case "project":
			c.ActiveProjectID = ""
		}
	})
}

// ---------------------------------------------------------------------------
// engine
// ---------------------------------------------------------------------------

// NewEngine builds an engine wired to this app's store and search settings.
func (a *App) NewEngine() *agents.Engine {
	cfg := a.Store.Config()
	return &agents.Engine{
		Store: a.Store,
		Searcher: agents.NewSearcher(agents.SearchOptions{
			TavilyKey: lookupTavilyKey(),
			Timeout:   time.Duration(cfg.SearchTimeout) * time.Second,
		}),
	}
}

func lookupTavilyKey() string {
	if v := strings.TrimSpace(os.Getenv("TAVILY_API_KEY")); v != "" {
		return v
	}
	return ""
}

// ---------------------------------------------------------------------------
// shared rendering
// ---------------------------------------------------------------------------

// RunOutcome summarises one completed agent run.
type RunOutcome struct {
	Task   *model.Task
	Result *model.TaskResult
	Err    error
}

// ExecuteTask runs a task with live output: a header, streamed markdown, and a
// footer reporting tokens, duration, cost and any committed files.
//
// This is the single implementation behind `synchro run`, `synchro task run`,
// `synchro pipeline` and the REPL, so all four look and behave the same.
func (a *App) ExecuteTask(task *model.Task, agent *model.Agent, label string) (*model.TaskResult, error) {
	return a.executeTask(task, agent, label, true)
}

// ExecuteTaskQuiet runs a task without echoing its answer. It exists for
// callers that render the result themselves, such as the delivery report:
// streaming it and then printing the finished document would show the same
// text twice.
func (a *App) ExecuteTaskQuiet(task *model.Task, agent *model.Agent, label string) (*model.TaskResult, error) {
	return a.executeTask(task, agent, label, false)
}

func (a *App) executeTask(task *model.Task, agent *model.Agent, label string, live bool) (*model.TaskResult, error) {
	e := a.NewEngine()

	// live reports whether to echo the answer as it arrives. Even when it is
	// off the spinner still shows that work is happening.
	var stream *ui.Stream
	if live {
		stream = a.P.NewStream(false)
		e.OnDelta = func(text string) { _, _ = stream.Write([]byte(text)) }
	}

	a.P.Printf("\n%s %s\n", a.P.Bold("▸"), a.P.Bold(label))
	a.P.Printf("  %s\n", a.P.Gray(fmt.Sprintf("%s · %s/%s", agent.Name, agent.Provider, agent.Model)))

	spin := a.P.StartSpinner("thinking…")
	res, err := e.RunTask(a.Ctx, task, agent)
	spin.Stop()
	if stream != nil {
		stream.Flush()
	}

	if err != nil {
		task.Status = model.StatusFailed
		task.ErrorMessage = err.Error()
		_ = a.Store.UpdateTask(task)
		return nil, err
	}

	task.Status = model.StatusCompleted
	task.Result = res
	_ = a.Store.UpdateTask(task)
	if res.TokensUsed > 0 {
		_ = a.Store.AddSpent(task.ProjectID, llm.EstimateCostUSD(res.Provider, res.Model, res.TokensUsed))
	}

	if live {
		a.printRunFooter(res)
	} else {
		a.P.Printf("\n%s %s\n", a.P.Gray("·"), a.P.Gray(fmt.Sprintf("%s tokens · %.1fs", humanInt(res.TokensUsed), res.DurationSeconds)))
	}
	return res, nil
}

// printRunFooter reports what a run cost and produced.
func (a *App) printRunFooter(res *model.TaskResult) {
	bits := []string{
		fmt.Sprintf("%s tokens", humanInt(res.TokensUsed)),
		fmt.Sprintf("%.1fs", res.DurationSeconds),
	}
	if res.TokensEstimated {
		bits = append(bits, "est.")
	}
	cost := llm.EstimateCostUSD(res.Provider, res.Model, res.TokensUsed)
	if cost == 0 {
		bits = append(bits, a.P.Green("free"))
	} else {
		bits = append(bits, fmt.Sprintf("~%s", money(cost)))
	}
	a.P.Printf("\n%s %s\n", a.P.Gray("·"), a.P.Gray(strings.Join(bits, " · ")))

	if len(res.Sources) > 0 {
		a.P.Printf("%s %s\n", a.P.Gray("·"), a.P.Gray(fmt.Sprintf("%d source(s) cited", len(res.Sources))))
		for _, s := range res.Sources {
			a.P.Printf("    %s %s\n", a.P.Gray("-"), a.P.Gray(s.URL))
		}
	}
	if n := len(res.Files); n > 0 {
		a.P.Printf("%s %s\n", a.P.Gray("·"), a.P.Bold(fmt.Sprintf("%d file(s) generated", n)))
		for _, f := range res.Files {
			a.P.Printf("    %s %s\n", a.P.Gray("-"), f.Path)
		}
		switch {
		case res.Repo != nil:
			a.P.Success("committed as %s  →  %s", res.Repo.Commit, a.P.Dim(res.Repo.Path))
		case res.RepoUnchanged:
			a.P.Printf("%s %s\n", a.P.Gray("·"), "files were identical to the last commit, nothing to record")
		default:
			a.P.Hint("commit them with:  synchro commit <task-id>")
		}
	}
}

// humanInt formats an int with thousands separators.
func humanInt(n int) string {
	s := fmt.Sprintf("%d", n)
	if len(s) <= 3 {
		return s
	}
	var b strings.Builder
	pre := len(s) % 3
	if pre > 0 {
		b.WriteString(s[:pre])
	}
	for i := pre; i < len(s); i += 3 {
		if b.Len() > 0 {
			b.WriteString(",")
		}
		b.WriteString(s[i : i+3])
	}
	return b.String()
}

// money formats a dollar amount, dropping cents when they are zero so free
// things read as "free" rather than "$0.00".
func money(v float64) string {
	if v == 0 {
		return "$0"
	}
	if v < 0.01 {
		return "<$0.01"
	}
	return fmt.Sprintf("$%.2f", v)
}

// llmCostOf is the local bookkeeping cost of a call. It never gates anything.
func llmCostOf(provider, modelName string, tokens int) float64 {
	return llm.EstimateCostUSD(provider, modelName, tokens)
}

func nowUTC() time.Time { return time.Now().UTC() }

// writeFile saves generated text, creating parent directories as needed.
func writeFile(path, content string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return os.WriteFile(path, []byte(content), 0o644)
}

func isTerminal(fd int) bool { return term.IsTerminal(fd) }

func readPassword(fd int) ([]byte, error) { return term.ReadPassword(fd) }
