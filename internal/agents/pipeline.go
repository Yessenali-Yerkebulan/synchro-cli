package agents

import (
	"context"
	"fmt"
	"strings"

	"github.com/synchro/synchro-cli/internal/llm"
	"github.com/synchro/synchro-cli/internal/model"
)

// StageResult is the outcome of one pipeline stage.
type StageResult struct {
	Stage  Stage
	Task   *model.Task
	Result *model.TaskResult
	Err    error
	// Skipped is set when the team had no agent for the stage's role.
	Skipped bool
	Note    string
}

// PipelineMode is a named sequence of roles.
type PipelineMode struct {
	ID          string
	Title       string
	Description string
	Roles       []model.Role
}

// PipelineModes are the ready-made chains. Every one of them is satisfiable by
// a team created from the matching template.
var PipelineModes = []PipelineMode{
	{
		ID:    "idea",
		Title: "Idea → shipped MVP",
		Description: "Research the market, write a spec, implement it, then review the result. " +
			"The full product loop, the one the web app was built around.",
		Roles: []model.Role{model.RoleResearcher, model.RoleProductManager, model.RoleDeveloper, model.RoleQA},
	},
	{
		ID:          "spec",
		Title:       "Idea → buildable spec",
		Description: "Turn a rough idea into a spec a developer could start from immediately.",
		Roles:       []model.Role{model.RoleProductManager},
	},
	{
		ID:          "build",
		Title:       "Spec → working code",
		Description: "Implement the description as a real codebase in the project repo.",
		Roles:       []model.Role{model.RoleDeveloper},
	},
	{
		ID:          "fullstack",
		Title:       "Full-stack build",
		Description: "Backend first, then the frontend built against the backend that was just written.",
		Roles:       []model.Role{model.RoleDeveloper, model.RoleDeveloper},
	},
	{
		ID:          "review",
		Title:       "Spec → build → review",
		Description: "Implement, then have a QA agent find the real problems before anything ships.",
		Roles:       []model.Role{model.RoleDeveloper, model.RoleQA},
	},
	{
		ID:          "research",
		Title:       "Research the question",
		Description: "Grounded market research with sources.",
		Roles:       []model.Role{model.RoleResearcher},
	},
	{
		ID:          "critique",
		Title:       "Draft → critique",
		Description: "Produce work, then have a skeptic attack the weakest assumption in it.",
		Roles:       []model.Role{model.RoleMarketer, model.RoleCritic},
	},
}

// PipelineModeByID finds a named mode, tolerating the obvious aliases.
func PipelineModeByID(id string) (PipelineMode, bool) {
	id = strings.ToLower(strings.TrimSpace(id))
	aliases := map[string]string{
		"mvp": "idea", "full": "idea", "idea-to-mvp": "idea",
		"dev": "build", "code": "build",
		"qa": "review", "check": "review",
		"research": "research", "market": "research",
		"critic": "critique", "critique": "critique",
	}
	if a, ok := aliases[id]; ok {
		id = a
	}
	for _, m := range PipelineModes {
		if m.ID == id {
			return m, true
		}
	}
	return PipelineMode{}, false
}

// BuildStages pairs each role in the chain with a concrete agent from the team.
// A role with no matching agent is reported rather than silently dropped, so
// the user learns their team cannot run the mode they asked for.
//
// A chain that needs the same role twice (fullstack) uses two different agents
// when the team has them, and reuses the only one when it does not.
func BuildStages(teamAgents []model.Agent, mode PipelineMode, description string) []Stage {
	used := map[string]bool{}
	var stages []Stage
	for i, role := range mode.Roles {
		var picked *model.Agent
		for j := range teamAgents {
			a := &teamAgents[j]
			if a.Role == role && a.Status != model.StatusInactive && !used[a.ID] {
				picked = a
				break
			}
		}
		if picked == nil {
			// Fall back to any unused agent holding this role, even if inactive.
			for j := range teamAgents {
				if teamAgents[j].Role == role {
					picked = &teamAgents[j]
					break
				}
			}
		}
		if picked == nil {
			stages = append(stages, Stage{Key: fmt.Sprintf("stage%d", i+1), Label: string(role), Note: fmt.Sprintf("no agent with role %s in this team", role)})
			continue
		}
		used[picked.ID] = true
		stages = append(stages, Stage{
			Key:   fmt.Sprintf("stage%d", i+1),
			Label: stageLabel(i, mode, picked),
			Agent: picked,
			Input: description,
		})
	}
	return stages
}

func stageLabel(i int, mode PipelineMode, a *model.Agent) string {
	// Two DEVELOPER stages in a row mean backend then frontend; say so
	// explicitly so the second one does not look like a repeat.
	if mode.ID == "fullstack" {
		if i == 0 {
			return a.Name + " · backend"
		}
		return a.Name + " · frontend"
	}
	return a.Name
}

// RunPipeline executes stages in order, feeding each stage's output into the
// next one's context. It stops at the first hard failure and returns everything
// completed so far, so the caller can show partial progress.
func (e *Engine) RunPipeline(ctx context.Context, project *model.Project, stages []Stage) []StageResult {
	results := make([]StageResult, 0, len(stages))
	carried := ""

	for _, stage := range stages {
		if stage.Agent == nil {
			results = append(results, StageResult{Stage: stage, Skipped: true, Note: stage.Note})
			continue
		}
		if err := ctx.Err(); err != nil {
			results = append(results, StageResult{Stage: stage, Skipped: true, Note: "cancelled"})
			return results
		}

		description := carried
		if description == "" {
			description = stage.Input
		} else {
			// Keep the original ask in view; earlier output alone is not enough
			// context once several stages deep.
			description = fmt.Sprintf("Original request:\n%s\n\nPrevious step (%s) produced:\n%s", stage.Input, stage.Label, truncate(carried, 8000))
		}

		task := &model.Task{
			ProjectID:       project.ID,
			AssignedAgentID: stage.Agent.ID,
			Title:           stage.Label,
			Description:     description,
			Status:          model.StatusRunning,
		}
		if err := e.Store.CreateTask(task); err != nil {
			results = append(results, StageResult{Stage: stage, Err: err})
			return results
		}

		if e.OnStage != nil {
			e.OnStage(stage, PhaseStart)
		}

		res, err := e.RunTask(ctx, task, stage.Agent)
		if err != nil {
			task.Status = model.StatusFailed
			task.ErrorMessage = err.Error()
			_ = e.Store.UpdateTask(task)
			if e.OnStage != nil {
				e.OnStage(stage, PhaseFail)
			}
			results = append(results, StageResult{Stage: stage, Task: task, Err: err})
			return results
		}

		task.Status = model.StatusCompleted
		task.Result = res
		_ = e.Store.UpdateTask(task)
		_ = e.Store.AddSpent(project.ID, llmCost(stage.Agent, res))

		if e.OnStage != nil {
			e.OnStage(stage, PhaseFinish)
		}
		results = append(results, StageResult{Stage: stage, Task: task, Result: res})
		carried = res.Output
	}
	return results
}

func llmCost(a *model.Agent, r *model.TaskResult) float64 {
	if r == nil {
		return 0
	}
	provider := r.Provider
	if provider == "" {
		provider = a.Provider
	}
	m := r.Model
	if m == "" {
		m = a.Model
	}
	return llm.EstimateCostUSD(provider, m, r.TokensUsed)
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "\n... (truncated)"
}
