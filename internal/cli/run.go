package cli

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/spf13/cobra"
	"github.com/synchro/synchro-cli/internal/agents"
	"github.com/synchro/synchro-cli/internal/model"
	"github.com/synchro/synchro-cli/internal/ui"
)

func newRunCmd(st *rootState) *cobra.Command {
	var (
		agentRef   string
		title      string
		ephemeral  bool
		priority   string
		listDetail bool
	)
	cmd := &cobra.Command{
		Use:   "run <what you want done>",
		Short: "Give the active agent a task and watch it work",
		Long: strings.TrimSpace(`
This is the main way to use synchro. It creates a task, runs it with the
active agent, prints the answer as it streams in, and records the result.

  synchro run "write a CLI that renames files by git history"

The active agent comes from the active team; pick one with
  synchro agent use <name>, or override it for a single run with --agent.`),
		Args: cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			a, err := st.app()
			if err != nil {
				return err
			}
			ag, err := a.requireAgentRef(agentRef)
			if err != nil {
				return err
			}

			description := strings.Join(args, " ")

			// A project is where generated code gets committed, so a DEVELOPER
			// agent needs one. Other roles work fine without.
			var projectID string
			if !ephemeral {
				p, perr := a.requireProjectForTask()
				if perr != nil {
					if ag.Role == model.RoleDeveloper {
						return perr
					}
					a.P.Warn("%v", perr)
					a.P.Hint("continuing without a project; generated files will not be committed to git")
				} else {
					projectID = p.ID
				}
			}

			t := &model.Task{
				ProjectID:       projectID,
				AssignedAgentID: ag.ID,
				Title:           firstNonEmpty(title, summarize(description)),
				Description:     description,
				Priority:        priority,
			}
			if !ephemeral {
				if err := a.Store.CreateTask(t); err != nil {
					return err
				}
			}

			res, err := a.ExecuteTask(t, ag, t.Title)
			if err != nil {
				if st.opt.JSON {
					return emitJSON(a.P, map[string]any{"task_id": t.ID, "error": err.Error()})
				}
				return err
			}
			if listDetail && len(res.Files) > 0 {
				a.P.Blank()
				a.P.Info("files")
				for _, f := range res.Files {
					a.P.Printf("    %s %s\n", a.P.Gray("-"), f.Path)
				}
			}
			if st.opt.JSON {
				return emitJSON(a.P, map[string]any{
					"task_id":  t.ID,
					"title":    t.Title,
					"agent":    ag.Name,
					"provider": res.Provider,
					"model":    res.Model,
					"output":   res.Output,
					"tokens":   res.TokensUsed,
					"files":    filePaths(res.Files),
					"commit":   commitOf(res),
					"duration": res.DurationSeconds,
				})
			}
			return nil
		},
	}
	cmd.Flags().StringVarP(&agentRef, "agent", "a", "", "agent to run as (default: active agent)")
	cmd.Flags().StringVar(&title, "title", "", "task title (default: first words of the description)")
	cmd.Flags().BoolVar(&ephemeral, "ephemeral", false, "do not save the task, and do not require a project")
	cmd.Flags().StringVar(&priority, "priority", "", "low, normal, high")
	cmd.Flags().BoolVar(&listDetail, "files", false, "list the generated file paths at the end")
	return cmd
}

func newPipelineCmd(st *rootState) *cobra.Command {
	var (
		mode   string
		dryRun bool
		list   bool
	)
	cmd := &cobra.Command{
		Use:     "pipeline <what you want done>",
		Aliases: []string{"pipe"},
		Short:   "Run a whole team through a multi-step chain",
		Long: strings.TrimSpace(`
A pipeline runs several agents in order, feeding each one's output into the next.
This is the difference between Synchro and a single chatbot: the researcher
grounds the spec, the developer implements it, and the reviewer attacks it.

The default mode, "idea", is the full chain:
  research → spec → implement → review

Pick another with --mode, or see them all with --list. Use --dry-run first if
you want to check which agents a mode will pick.`),
		Args: cobra.ArbitraryArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			a, err := st.app()
			if err != nil {
				return err
			}
			if list {
				return listPipelineModes(a)
			}
			if len(args) == 0 {
				return fmt.Errorf("describe what you want done, e.g. synchro pipeline \"a habit tracker with a web UI\"")
			}
			pm, ok := agents.PipelineModeByID(mode)
			if !ok {
				return fmt.Errorf("unknown mode %q. Run 'synchro pipeline --list' to see the options.", mode)
			}
			team, err := a.RequireTeam()
			if err != nil {
				return err
			}
			p, err := a.requireProjectForTask()
			if err != nil {
				return err
			}

			description := strings.Join(args, " ")
			stages := agents.BuildStages(a.Store.Agents(team.ID), pm, description)

			if dryRun {
				printPlannedPipeline(a, pm, stages)
				return nil
			}
			return a.runPipeline(p, pm, stages, description)
		},
	}
	cmd.Flags().StringVar(&mode, "mode", "idea", "pipeline mode: idea, spec, build, fullstack, review, research, critique")
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "show which agents would run, without running them")
	cmd.Flags().BoolVar(&list, "list", false, "list the available pipeline modes")
	return cmd
}

func listPipelineModes(a *App) error {
	rows := make([][]string, 0, len(agents.PipelineModes))
	for _, m := range agents.PipelineModes {
		roles := make([]string, 0, len(m.Roles))
		for _, r := range m.Roles {
			roles = append(roles, string(r))
		}
		rows = append(rows, []string{m.ID, m.Title, m.Description, strings.Join(roles, " → ")})
	}
	a.P.Table([]string{"MODE", "TITLE", "WHAT IT DOES", "CHAIN"}, rows)
	a.P.Blank()
	a.P.Hint("run one with: synchro pipeline --mode <id> \"<what you want done>\"")
	return nil
}

// printPlannedPipeline shows the chain without spending anything.
func printPlannedPipeline(a *App, pm agents.PipelineMode, stages []agents.Stage) {
	a.P.Printf("\n%s %s\n", a.P.Bold("▸"), a.P.Bold(pm.Title))
	a.P.Printf("  %s\n\n", a.P.Gray(pm.Description))
	for i, s := range stages {
		if s.Agent == nil {
			a.P.Printf("  %d. %s %s\n", i+1, a.P.Yellow("—"), a.P.Gray(s.Note))
			continue
		}
		arrow := ""
		if i < len(stages)-1 {
			arrow = " →"
		}
		a.P.Printf("  %d. %s %s %s\n", i+1, a.P.Bold(s.Agent.Name), a.P.Gray(fmt.Sprintf("(%s, %s/%s)", s.Agent.Role, s.Agent.Provider, s.Agent.Model)), a.P.Gray(arrow))
	}
	a.P.Blank()
	a.P.Hint("this is a dry run; drop --dry-run to execute it")
}

// runPipeline executes the stages, streaming each agent's answer, and prints a
// summary with the final output.
func (a *App) runPipeline(p *model.Project, pm agents.PipelineMode, stages []agents.Stage, description string) error {
	usable := 0
	for _, s := range stages {
		if s.Agent != nil {
			usable++
		}
	}
	if usable == 0 {
		return fmt.Errorf("this team has none of the roles this pipeline needs (%s)", roleChain(pm))
	}

	a.P.Printf("\n%s %s\n", a.P.Bold("▸"), a.P.Bold(pm.Title))
	a.P.Printf("  %s\n", a.P.Gray(description))
	a.P.Blank()

	e := a.NewEngine()
	var currentLabel string
	// The prefix names the speaker, so it belongs once per stage rather than
	// in front of every streamed chunk.
	needsPrefix := true
	e.OnDelta = func(text string) {
		if needsPrefix {
			_, _ = fmt.Fprint(a.P.Out, a.P.Gray("  ")+currentLabel+"> ")
			needsPrefix = false
		}
		_, _ = fmt.Fprint(a.P.Out, text)
	}
	e.OnStage = func(s agents.Stage, phase string) {
		switch phase {
		case agents.PhaseStart:
			a.P.Printf("\n%s %s %s\n", a.P.Gray("▸"), a.P.Bold(s.Label), a.P.Gray(fmt.Sprintf("· %s", s.Agent.Role)))
			currentLabel = s.Label
			needsPrefix = true
		case agents.PhaseFinish:
			if s.Agent != nil {
				a.P.Printf("\n")
			}
			needsPrefix = true
		}
	}

	results := e.RunPipeline(a.Ctx, p, stages)
	_ = pm

	a.P.Rule()
	okCount, failCount := 0, 0
	for _, r := range results {
		switch {
		case r.Skipped:
			a.P.Printf("%s %s %s\n", a.P.Yellow("–"), r.Stage.Label, a.P.Gray(r.Note))
		case r.Err != nil:
			failCount++
			a.P.Printf("%s %s\n", a.P.Red("✗"), a.P.Red(fmt.Sprintf("%s — %s", r.Stage.Label, r.Err)))
		default:
			okCount++
			extra := ""
			if r.Result != nil {
				bits := []string{fmt.Sprintf("%d tokens", r.Result.TokensUsed), fmt.Sprintf("%.1fs", r.Result.DurationSeconds)}
				if n := len(r.Result.Files); n > 0 {
					bits = append(bits, fmt.Sprintf("%d files", n))
				}
				if cost := costOf(r.Result); cost > 0 {
					bits = append(bits, "~"+money(cost))
				} else {
					bits = append(bits, "free")
				}
				extra = " · " + strings.Join(bits, " · ")
			}
			a.P.Printf("%s %s %s\n", a.P.Green("✓"), r.Stage.Label, a.P.Gray(strings.TrimSpace(extra)))
		}
	}
	a.P.Blank()

	if failCount > 0 {
		a.P.Hint("partial results are saved; re-run the failed stage with: synchro task run <id>")
		return fmt.Errorf("pipeline failed after %d of %d stages", okCount+failCount, len(stages))
	}
	if last := lastCompleted(results); last != nil && last.Result != nil && last.Result.Output != "" {
		a.P.Info("final output")
		a.P.Blank()
		a.P.Markdown(last.Result.Output)
		a.P.Blank()
	}
	a.P.Hint("saved to project %s   ·   %d/%d stages completed", p.Name, okCount, len(stages))
	return nil
}

func costOf(r *model.TaskResult) float64 {
	return llmCostOf(r.Provider, r.Model, r.TokensUsed)
}

func lastCompleted(results []agents.StageResult) *agents.StageResult {
	for i := len(results) - 1; i >= 0; i-- {
		if results[i].Err == nil && !results[i].Skipped && results[i].Result != nil {
			return &results[i]
		}
	}
	return nil
}

func roleChain(pm agents.PipelineMode) string {
	roles := make([]string, 0, len(pm.Roles))
	for _, r := range pm.Roles {
		roles = append(roles, string(r))
	}
	return strings.Join(roles, " or ")
}

func summarize(s string) string {
	oneLine := strings.Join(strings.Fields(s), " ")
	r := []rune(oneLine)
	if len(r) <= 60 {
		return oneLine
	}
	cut := 60
	for i := cut; i > 30; i-- {
		if r[i] == ' ' {
			cut = i
			break
		}
	}
	return string(r[:cut]) + "…"
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}

func emitJSON(p *ui.Printer, v any) error {
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	p.Printf("%s\n", b)
	return nil
}

func filePaths(files []model.CodeFile) []string {
	out := make([]string, 0, len(files))
	for _, f := range files {
		out = append(out, f.Path)
	}
	return out
}

func commitOf(res *model.TaskResult) string {
	if res.Repo == nil {
		return ""
	}
	return res.Repo.Commit
}
