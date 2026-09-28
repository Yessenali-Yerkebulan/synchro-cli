package cli

import (
	"fmt"
	"strings"

	"github.com/spf13/cobra"
	"github.com/synchro/synchro-cli/internal/model"
)

func newTaskCmd(st *rootState) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "task <command>",
		Short: "Manage and run tasks",
		RunE: func(cmd *cobra.Command, args []string) error {
			a, err := st.app()
			if err != nil {
				return err
			}
			project, err := a.resolveProject()
			if err != nil {
				return err
			}
			var tasks []model.Task
			if project != nil {
				tasks = a.Store.Tasks(project.ID)
			} else {
				tasks = a.Store.Tasks("")
			}
			if len(tasks) == 0 {
				a.P.Info("no tasks yet.")
				a.P.Info("run one straight away with:  synchro run \"<what you want done>\"")
				return nil
			}
			names := map[string]string{}
			for _, t := range tasks {
				if ag, err := a.Store.Agent(t.AssignedAgentID, ""); err == nil {
					names[t.ID] = ag.Name
				} else {
					names[t.ID] = "—"
				}
			}
			if st.opt.JSON {
				a.P.Printf("[")
				for i, t := range tasks {
					if i > 0 {
						a.P.Printf(",")
					}
					out := ""
					if t.Result != nil {
						out = t.Result.Output
					}
					a.P.Printf("{\"id\":%q,\"title\":%q,\"status\":%q,\"agent\":%q,\"output\":%q}", t.ID, t.Title, t.Status, names[t.ID], out)
				}
				a.P.Printf("]\n")
				return nil
			}
			rows := make([][]string, 0, len(tasks))
			for i, t := range tasks {
				title := t.Title
				if len([]rune(title)) > 46 {
					title = string([]rune(title)[:46]) + "…"
				}
				extra := ""
				if t.Result != nil {
					if n := len(t.Result.Files); n > 0 {
						extra = fmt.Sprintf("%d files", n)
					}
					if t.Result.Repo != nil {
						extra = "committed " + t.Result.Repo.Commit
					}
				}
				rows = append(rows, []string{
					fmt.Sprintf("%d", i+1), t.ID, string(t.Status), names[t.ID], title, extra,
				})
			}
			a.P.Table([]string{"#", "ID", "STATUS", "AGENT", "TITLE", ""}, rows)
			a.P.Blank()
			a.P.Hint("run one with: synchro task run <id>    ·    read one: synchro task show <id>")
			return nil
		},
	}

	var desc, agentRef, priority string
	newCmd := &cobra.Command{
		Use:     "new <title>",
		Aliases: []string{"create", "add"},
		Short:   "Create a task without running it",
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			a, err := st.app()
			if err != nil {
				return err
			}
			project, err := a.requireProjectForTask()
			if err != nil {
				return err
			}
			ag, err := a.requireAgentRef(agentRef)
			if err != nil {
				return err
			}
			t := &model.Task{
				ProjectID:       project.ID,
				AssignedAgentID: ag.ID,
				Title:           args[0],
				Description:     desc,
				Priority:        priority,
			}
			if err := a.Store.CreateTask(t); err != nil {
				return err
			}
			a.P.Success("task %s created for %s", t.ID, ag.Name)
			a.P.Hint("run it with: synchro task run %s", t.ID)
			return nil
		},
	}
	newCmd.Flags().StringVarP(&desc, "description", "d", "", "full task description")
	newCmd.Flags().StringVar(&agentRef, "agent", "", "agent to assign (default: active agent)")
	newCmd.Flags().StringVar(&priority, "priority", "", "low, normal, high")

	runCmd := &cobra.Command{
		Use:   "run [task-id]",
		Short: "Run a task with its assigned agent",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			a, err := st.app()
			if err != nil {
				return err
			}
			var t *model.Task
			if len(args) == 1 {
				t, err = a.Store.Task(args[0])
				if err != nil {
					return fmt.Errorf("no task with id or title %q", args[0])
				}
			} else {
				list := a.Store.Tasks("")
				if len(list) == 0 {
					return fmt.Errorf("no tasks yet. Create one: synchro task new \"<title>\"")
				}
				t = &list[len(list)-1]
			}
			ag, err := a.Store.Agent(t.AssignedAgentID, "")
			if err != nil {
				return fmt.Errorf("task %s has no agent assigned. Assign one with: synchro task new --agent <name>", t.ID)
			}
			_, err = a.ExecuteTask(t, ag, t.Title)
			return err
		},
	}

	showCmd := &cobra.Command{
		Use:   "show <task-id>",
		Short: "Show a task and its full result",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			a, err := st.app()
			if err != nil {
				return err
			}
			t, err := a.Store.Task(args[0])
			if err != nil {
				return fmt.Errorf("no task with id or title %q", args[0])
			}
			a.P.Title(t.Title)
			a.P.Blank()
			a.P.KeyValue("id", t.ID)
			a.P.KeyValue("status", string(t.Status))
			if ag, err := a.Store.Agent(t.AssignedAgentID, ""); err == nil {
				a.P.KeyValue("agent", fmt.Sprintf("%s (%s, %s/%s)", ag.Name, ag.Role, ag.Provider, ag.Model))
			}
			a.P.KeyValue("created", t.CreatedAt.Local().Format("2006-01-02 15:04"))
			if t.Description != "" {
				a.P.KeyValue("description", t.Description)
			}
			if t.ErrorMessage != "" {
				a.P.Blank()
				a.P.Error("%s", t.ErrorMessage)
			}
			if t.Result == nil {
				a.P.Blank()
				a.P.Info("this task has not been run yet")
				return nil
			}
			if len(t.Result.Sources) > 0 {
				a.P.Blank()
				a.P.Info("sources")
				for _, s := range t.Result.Sources {
					a.P.Printf("    %s %s\n", a.P.Gray("-"), a.P.Gray(s.URL))
				}
			}
			if t.Result.Output != "" {
				a.P.Blank()
				a.P.Info("output")
				a.P.Blank()
				a.P.Markdown(t.Result.Output)
			}
			if len(t.Result.Files) > 0 {
				a.P.Blank()
				a.P.Info("generated files")
				for _, f := range t.Result.Files {
					lines := strings.Count(f.Content, "\n") + 1
					commit := "not committed"
					if t.Result.Repo != nil {
						commit = "commit " + t.Result.Repo.Commit
					}
					a.P.Printf("    %s %s %s\n", a.P.Bold(f.Path), a.P.Gray(fmt.Sprintf("(%d lines)", lines)), a.P.Gray(commit))
				}
				if t.Result.Repo == nil {
					a.P.Blank()
					a.P.Hint("commit them with: synchro commit %s", t.ID)
				}
			}
			return nil
		},
	}

	rmCmd := &cobra.Command{
		Use:     "rm <task-id>",
		Aliases: []string{"remove", "delete", "del"},
		Short:   "Delete a task",
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			a, err := st.app()
			if err != nil {
				return err
			}
			t, err := a.Store.Task(args[0])
			if err != nil {
				return fmt.Errorf("no task with id or title %q", args[0])
			}
			if !confirm(a, st.opt.Yes, fmt.Sprintf("delete task %q?", t.Title)) {
				a.P.Info("cancelled")
				return nil
			}
			if err := a.Store.DeleteTask(t.ID); err != nil {
				return err
			}
			a.P.Success("task deleted")
			return nil
		},
	}

	cmd.AddCommand(newCmd, runCmd, showCmd, rmCmd)
	return cmd
}

// requireProjectForTask resolves the project a task belongs to, or explains how
// to make one. Running a task without a project would leave generated code with
// nowhere to be committed.
func (a *App) requireProjectForTask() (*model.Project, error) {
	p, err := a.resolveProject()
	if err != nil {
		return nil, err
	}
	if p == nil {
		return nil, fmt.Errorf("this command needs a project so generated code has somewhere to go.\ncreate one with:  synchro project new <name>")
	}
	return p, nil
}

// requireAgentRef resolves an agent from an explicit ref, or the active one.
func (a *App) requireAgentRef(ref string) (*model.Agent, error) {
	if ref == "" {
		return a.RequireAgent()
	}
	ag, err := a.Store.Agent(ref, "")
	if err != nil {
		return nil, fmt.Errorf("no agent called %q", ref)
	}
	return ag, nil
}
