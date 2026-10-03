package cli

import (
	"fmt"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"
	"github.com/synchro/synchro-cli/internal/model"
	"github.com/synchro/synchro-cli/internal/repo"
)

func newProjectCmd(st *rootState) *cobra.Command {
	cmd := &cobra.Command{
		Use:     "project <command>",
		Aliases: []string{"proj", "p"},
		Short:   "Manage projects",
		Long: strings.TrimSpace(`
A project is where an agent's work accumulates. Generated code from DEVELOPER
agents is committed into a real git repository at ~/.synchro/repos/<project>.`),
		RunE: func(cmd *cobra.Command, args []string) error {
			a, err := st.app()
			if err != nil {
				return err
			}
			ws, err := a.RequireWorkspace()
			if err != nil {
				return err
			}
			projects := a.Store.Projects(ws.ID)
			if len(projects) == 0 {
				a.P.Info("no projects in %s yet.", ws.Name)
				a.P.Info("create one with:  synchro-cli project new <name>")
				return nil
			}
			active := ""
			if p, err := a.resolveProject(); err == nil && p != nil {
				active = p.ID
			}
			rows := make([][]string, 0, len(projects))
			for _, p := range projects {
				marker := ""
				if p.ID == active {
					marker = "*"
				}
				rows = append(rows, []string{
					marker, p.Name, p.ID, string(p.Status),
					fmt.Sprintf("%d tasks", len(a.Store.Tasks(p.ID))),
					fmt.Sprintf("%d commits", len(repo.Log(a.Store.ReposDir(), p.ID, 1000))),
				})
			}
			a.P.Table([]string{"", "NAME", "ID", "STATUS", "TASKS", "CODE"}, rows)
			a.P.Blank()
			a.P.Hint("* = active. Switch with: synchro-cli project use <name>")
			return nil
		},
	}

	var desc, category, market string
	var budget float64
	newCmd := &cobra.Command{
		Use:     "new <name>",
		Aliases: []string{"create", "add"},
		Short:   "Create a project",
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			a, err := st.app()
			if err != nil {
				return err
			}
			ws, err := a.RequireWorkspace()
			if err != nil {
				return err
			}
			team, err := a.RequireTeam()
			if err != nil {
				return err
			}
			p := &model.Project{
				WorkspaceID:  ws.ID,
				TeamID:       team.ID,
				Name:         args[0],
				Description:  desc,
				Category:     category,
				TargetMarket: market,
				BudgetUSD:    budget,
			}
			if err := a.Store.CreateProject(p); err != nil {
				return err
			}
			if err := a.SetActive(ws.ID, team.ID, "", p.ID); err != nil {
				return err
			}
			a.P.Success("project %s created (%s)", a.P.Bold(p.Name), p.ID)
			a.P.Hint("code will be committed to %s", a.P.Dim(filepath.Join(a.Store.ReposDir(), p.ID)))
			return nil
		},
	}
	newCmd.Flags().StringVarP(&desc, "description", "d", "", "what this project is")
	newCmd.Flags().StringVar(&category, "category", "", "e.g. saas, mobile, internal-tool")
	newCmd.Flags().StringVar(&market, "market", "", "target market")
	newCmd.Flags().Float64Var(&budget, "budget", 0, "soft budget in dollars (informational only)")

	useCmd := &cobra.Command{
		Use:   "use <name>",
		Short: "Switch to a project",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			a, err := st.app()
			if err != nil {
				return err
			}
			ws, err := a.RequireWorkspace()
			if err != nil {
				return err
			}
			p, err := a.Store.Project(args[0], ws.ID)
			if err != nil {
				return fmt.Errorf("no project called %q", args[0])
			}
			if err := a.SetActive(ws.ID, "", "", p.ID); err != nil {
				return err
			}
			a.P.Success("now in project %s", a.P.Bold(p.Name))
			return nil
		},
	}

	showCmd := &cobra.Command{
		Use:   "show [name]",
		Short: "Show a project, its tasks and its commit history",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			a, err := st.app()
			if err != nil {
				return err
			}
			var p *model.Project
			if len(args) == 1 {
				ws, err := a.RequireWorkspace()
				if err != nil {
					return err
				}
				p, err = a.Store.Project(args[0], ws.ID)
				if err != nil {
					return fmt.Errorf("no project called %q", args[0])
				}
			} else {
				p, err = a.resolveProject()
				if err != nil || p == nil {
					return fmt.Errorf("no project selected. Create one with: synchro-cli project new <name>")
				}
			}
			a.P.Title(p.Name)
			if p.Description != "" {
				a.P.Printf("  %s\n\n", a.P.Dim(p.Description))
			}
			a.P.KeyValue("id", p.ID)
			a.P.KeyValue("status", string(p.Status))
			if p.Category != "" {
				a.P.KeyValue("category", p.Category)
			}
			if p.TargetMarket != "" {
				a.P.KeyValue("market", p.TargetMarket)
			}
			tasks := a.Store.Tasks(p.ID)
			a.P.KeyValue("tasks", fmt.Sprintf("%d", len(tasks)))
			if p.BudgetUSD > 0 {
				a.P.KeyValue("estimated spend", fmt.Sprintf("%s of a %s soft budget", money(p.SpentUSD), money(p.BudgetUSD)))
			} else if p.SpentUSD > 0 {
				a.P.KeyValue("estimated spend", money(p.SpentUSD))
			}
			if p.DeliveryReport != "" {
				a.P.KeyValue("delivery report", p.DeliveryReportAt.Local().Format("2006-01-02 15:04"))
			}

			if log := repo.Log(a.Store.ReposDir(), p.ID, 10); len(log) > 0 {
				a.P.Blank()
				a.P.Info("commits")
				for _, l := range log {
					a.P.Printf("    %s\n", a.P.Gray(l))
				}
			}
			if len(tasks) > 0 {
				a.P.Blank()
				a.P.Info("tasks")
				rows := make([][]string, 0, len(tasks))
				for _, t := range tasks {
					name := "-"
					if ag, err := a.Store.Agent(t.AssignedAgentID, ""); err == nil {
						name = ag.Name
					}
					title := t.Title
					if r := []rune(title); len(r) > 40 {
						title = string(r[:40]) + "..."
					}
					rows = append(rows, []string{t.ID, string(t.Status), name, title})
				}
				a.P.Table([]string{"ID", "STATUS", "AGENT", "TITLE"}, rows)
			}
			return nil
		},
	}

	rmCmd := &cobra.Command{
		Use:     "rm <name>",
		Aliases: []string{"remove", "delete", "del"},
		Short:   "Delete a project and its tasks",
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			a, err := st.app()
			if err != nil {
				return err
			}
			ws, err := a.RequireWorkspace()
			if err != nil {
				return err
			}
			p, err := a.Store.Project(args[0], ws.ID)
			if err != nil {
				return fmt.Errorf("no project called %q", args[0])
			}
			if !confirm(a, st.opt.Yes, fmt.Sprintf("delete project %q and its %d tasks?", p.Name, len(a.Store.Tasks(p.ID)))) {
				a.P.Info("cancelled")
				return nil
			}
			if err := a.Store.DeleteProject(p.ID, ws.ID); err != nil {
				return err
			}
			a.P.Success("project %s deleted", p.Name)
			a.P.Hint("generated code in %s was left on disk", a.Store.ReposDir())
			return nil
		},
	}

	cmd.AddCommand(newCmd, useCmd, showCmd, rmCmd)
	return cmd
}
