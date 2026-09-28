package cli

import (
	"fmt"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"
	"github.com/synchro/synchro-cli/internal/model"
	"github.com/synchro/synchro-cli/internal/repo"
)

func newCommitCmd(st *rootState) *cobra.Command {
	return &cobra.Command{
		Use:   "commit <task-id>",
		Short: "Commit a task's generated files to the project repository",
		Long: strings.TrimSpace(`
When a workspace has auto-commit turned off, a DEVELOPER agent's files stay on
the task result until you approve them. This is the approval step: it writes
them into ~/.synchro/repos/<project> and makes a commit.

Turn auto-commit on for a workspace with:  synchro ws edit <name> --auto-commit`),
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			a, err := st.app()
			if err != nil {
				return err
			}
			t, err := a.Store.Task(args[0])
			if err != nil {
				return fmt.Errorf("no task with id or title %q", args[0])
			}
			e := a.NewEngine()
			info, err := e.CommitFiles(t.ID)
			if err != nil {
				return err
			}
			a.P.Success("committed %d file(s) as %s", len(t.Result.Files), info.Commit)
			a.P.KeyValue("path", info.Path)
			a.P.Blank()
			for _, l := range repo.Log(a.Store.ReposDir(), t.ProjectID, 5) {
				a.P.Printf("  %s\n", a.P.Gray(l))
			}
			return nil
		},
	}
}

func newFilesCmd(st *rootState) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "files [project]",
		Short: "List the code generated for a project",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			a, err := st.app()
			if err != nil {
				return err
			}
			p, err := a.projectArg(args)
			if err != nil {
				return err
			}
			files := repo.ListFiles(a.Store.ReposDir(), p.ID)
			if len(files) == 0 {
				a.P.Info("no committed code for %s yet.", p.Name)
				a.P.Hint("generated code lands here after a developer agent runs")
				return nil
			}
			for _, f := range files {
				a.P.Printf("  %s\n", f)
			}
			a.P.Blank()
			a.P.Hint("%d file(s) · view one with: synchro files show <path>", len(files))
			return nil
		},
	}
	cmd.AddCommand(&cobra.Command{
		Use:   "show <path>",
		Short: "Print a generated file",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			a, err := st.app()
			if err != nil {
				return err
			}
			p, err := a.resolveProject()
			if err != nil || p == nil {
				return fmt.Errorf("no project selected")
			}
			content, err := repo.Show(a.Store.ReposDir(), p.ID, args[0])
			if err != nil {
				return err
			}
			fmt.Fprint(a.P.Out, content)
			return nil
		},
	})
	cmd.AddCommand(&cobra.Command{
		Use:   "open [project]",
		Short: "Print the path to the project's repository",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			a, err := st.app()
			if err != nil {
				return err
			}
			p, err := a.projectArg(args)
			if err != nil {
				return err
			}
			a.P.Println(a.Store.ReposDir() + "/" + p.ID)
			return nil
		},
	})
	return cmd
}

// projectArg resolves a project from an optional positional argument.
func (a *App) projectArg(args []string) (*model.Project, error) {
	if len(args) == 1 {
		ws, err := a.RequireWorkspace()
		if err != nil {
			return nil, err
		}
		p, err := a.Store.Project(args[0], ws.ID)
		if err != nil {
			return nil, fmt.Errorf("no project called %q", args[0])
		}
		return p, nil
	}
	p, err := a.resolveProject()
	if err != nil {
		return nil, err
	}
	if p == nil {
		return nil, fmt.Errorf("no project selected. Create one with: synchro project new <name>")
	}
	return p, nil
}

func newReportCmd(st *rootState) *cobra.Command {
	var save bool
	cmd := &cobra.Command{
		Use:   "report [project]",
		Short: "Generate a Markdown delivery report for a project",
		Long: strings.TrimSpace(`
Summarises a project's completed tasks and its git history into a
client-facing document: Overview, What was built, Next steps.

It is a starting draft written by an agent, not an audited statement of what
was built. The prompt is explicit that it must not invent anything.`),
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			a, err := st.app()
			if err != nil {
				return err
			}
			p, err := a.projectArg(args)
			if err != nil {
				return err
			}
			team, err := a.Store.Team(p.TeamID, "")
			if err != nil {
				return fmt.Errorf("this project's team is gone: %w", err)
			}
			teamAgents := a.Store.Agents(team.ID)
			if len(teamAgents) == 0 {
				return fmt.Errorf("this project's team has no agents to write the report")
			}
			// A PM writes a client-facing document better than a developer.
			reporter := &teamAgents[0]
			for i := range teamAgents {
				if teamAgents[i].Role == model.RoleProductManager {
					reporter = &teamAgents[i]
					break
				}
			}

			prompt := buildReportPrompt(a, p, teamAgents)
			t := &model.Task{
				ProjectID:       p.ID,
				AssignedAgentID: reporter.ID,
				Title:           "Delivery report for " + p.Name,
				Description:     prompt,
			}
			if err := a.Store.CreateTask(t); err != nil {
				return err
			}
			res, err := a.ExecuteTask(t, reporter, t.Title)
			if err != nil {
				return err
			}
			p.DeliveryReport = res.Output
			p.DeliveryReportAt = nowUTC()
			if err := a.Store.UpdateProject(p); err != nil {
				return err
			}
			if save {
				path := filepath.Join(a.Store.Dir(), "report-"+p.ID+".md")
				if err := writeFile(path, res.Output); err != nil {
					return err
				}
				a.P.Success("report written to %s", path)
				return nil
			}
			a.P.Blank()
			a.P.Markdown(res.Output)
			a.P.Blank()
			a.P.Hint("saved to the project. Write it to a file with: synchro report %s --save", p.Name)
			return nil
		},
	}
	cmd.Flags().BoolVar(&save, "save", false, "write the report to a file instead of printing it")
	return cmd
}

// buildReportPrompt assembles the report request from real project data only.
func buildReportPrompt(a *App, p *model.Project, teamAgents []model.Agent) string {
	names := map[string]string{}
	for _, ag := range teamAgents {
		names[ag.ID] = fmt.Sprintf("%s (%s)", ag.Name, ag.Role)
	}
	var lines []string
	for _, t := range a.Store.Tasks(p.ID) {
		if t.Status != model.StatusCompleted || t.Result == nil {
			continue
		}
		label := names[t.AssignedAgentID]
		if label == "" {
			label = "Unknown agent"
		}
		snippet := t.Result.Output
		if len(snippet) > 400 {
			snippet = snippet[:400] + "..."
		}
		lines = append(lines, fmt.Sprintf("- [%s] %s\n  %s", label, t.Title, snippet))
	}
	commits := repo.Log(a.Store.ReposDir(), p.ID, 20)
	commitText := "(no code committed yet)"
	if len(commits) > 0 {
		commitText = strings.Join(commits, "\n")
	}
	taskText := "(no completed tasks yet)"
	if len(lines) > 0 {
		taskText = strings.Join(lines, "\n")
	}
	desc := p.Description
	if desc == "" {
		desc = "(no description provided)"
	}
	return fmt.Sprintf(`Write a clear, client-facing delivery report in Markdown for the project below.
Structure it with headings: Overview, What was built, and Next steps. Base it
only on the information given - do not invent features or results that are not
listed, and if something is missing, say it is missing.

Project: %s
Description: %s
Status: %s

Completed work:
%s

Git commit history:
%s
`, p.Name, desc, p.Status, taskText, commitText)
}
