package cli

import (
	"fmt"
	"strings"

	"github.com/spf13/cobra"
	"github.com/synchro/synchro/internal/model"
)

func newWSCmd(st *rootState) *cobra.Command {
	cmd := &cobra.Command{
		Use:     "ws <command>",
		Aliases: []string{"workspace", "w"},
		Short:   "Manage workspaces",
		Long: strings.TrimSpace(`
A workspace is the top-level container: it owns teams, projects and the
auto-commit setting.

Unlike the web app, there is no limit on how many you can create.`),
		RunE: func(cmd *cobra.Command, args []string) error {
			return listWorkspaces(st)
		},
	}

	var desc string
	var autoCommit bool
	newCmd := &cobra.Command{
		Use:     "new <name>",
		Aliases: []string{"create", "add"},
		Short:   "Create a workspace",
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			a, err := st.app()
			if err != nil {
				return err
			}
			cfg := a.Store.Config()
			w := &model.Workspace{
				Name:                args[0],
				Description:         desc,
				AutoCommitAgentCode: autoCommit || cfg.AutoCommit,
			}
			if err := a.Store.CreateWorkspace(w); err != nil {
				return err
			}
			if err := a.SetActive(w.ID, "", "", ""); err != nil {
				return err
			}
			a.P.Success("workspace %s created (%s)", a.P.Bold(w.Name), w.ID)
			a.P.Hint("next: synchro team from mvp")
			return nil
		},
	}
	newCmd.Flags().StringVarP(&desc, "description", "d", "", "what this workspace is for")
	newCmd.Flags().BoolVar(&autoCommit, "auto-commit", false, "automatically commit generated code to git")

	useCmd := &cobra.Command{
		Use:   "use <name>",
		Short: "Switch to a workspace",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			a, err := st.app()
			if err != nil {
				return err
			}
			w, err := a.Store.Workspace(args[0])
			if err != nil {
				return fmt.Errorf("no workspace called %q", args[0])
			}
			if err := a.SetActive(w.ID, "", "", ""); err != nil {
				return err
			}
			a.P.Success("now in workspace %s", a.P.Bold(w.Name))
			return nil
		},
	}

	showCmd := &cobra.Command{
		Use:   "show [name]",
		Short: "Show workspace details",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			a, err := st.app()
			if err != nil {
				return err
			}
			ref := ""
			if len(args) == 1 {
				ref = args[0]
			}
			var w *model.Workspace
			if ref == "" {
				w, err = a.RequireWorkspace()
				if err != nil {
					return err
				}
			} else {
				w, err = a.Store.Workspace(ref)
				if err != nil {
					return fmt.Errorf("no workspace called %q", ref)
				}
			}
			teams := a.Store.Teams(w.ID)
			projects := a.Store.Projects(w.ID)
			a.P.Title(w.Name)
			if w.Description != "" {
				a.P.Printf("  %s\n\n", a.P.Dim(w.Description))
			}
			a.P.KeyValue("id", w.ID)
			a.P.KeyValue("teams", fmt.Sprintf("%d", len(teams)))
			a.P.KeyValue("projects", fmt.Sprintf("%d", len(projects)))
			a.P.KeyValue("auto-commit", boolStr(w.AutoCommitAgentCode))
			a.P.KeyValue("created", w.CreatedAt.Local().Format("2006-01-02 15:04"))
			if len(teams) > 0 {
				a.P.Blank()
				a.P.Info("teams")
				for _, t := range teams {
					n := len(a.Store.Agents(t.ID))
					a.P.Printf("    %s %s %s\n", a.P.Bold(t.Name), a.P.Gray(t.ID), a.P.Gray(fmt.Sprintf("(%d agents)", n)))
				}
			}
			return nil
		},
	}

	rmCmd := &cobra.Command{
		Use:     "rm <name>",
		Aliases: []string{"remove", "delete", "del"},
		Short:   "Delete a workspace and everything in it",
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			a, err := st.app()
			if err != nil {
				return err
			}
			w, err := a.Store.Workspace(args[0])
			if err != nil {
				return fmt.Errorf("no workspace called %q", args[0])
			}
			if !confirm(a, st.opt.Yes, fmt.Sprintf("delete workspace %q and all its teams, projects and tasks?", w.Name)) {
				a.P.Info("cancelled")
				return nil
			}
			if err := a.Store.DeleteWorkspace(w.ID); err != nil {
				return err
			}
			a.P.Success("workspace %s deleted", w.Name)
			return nil
		},
	}

	cmd.AddCommand(newCmd, useCmd, showCmd, rmCmd)
	return cmd
}

func listWorkspaces(st *rootState) error {
	a, err := st.app()
	if err != nil {
		return err
	}
	all := a.Store.Workspaces()
	if len(all) == 0 {
		a.P.Info("no workspaces yet. Create one with:  synchro ws new <name>")
		return nil
	}
	active := ""
	if w, err := a.resolveWorkspace(); err == nil {
		active = w.ID
	}
	if st.opt.JSON {
		a.P.Printf("[")
		for i, w := range all {
			if i > 0 {
				a.P.Printf(",")
			}
			a.P.Printf("{\"id\":%q,\"name\":%q,\"description\":%q}", w.ID, w.Name, w.Description)
		}
		a.P.Printf("]\n")
		return nil
	}
	rows := make([][]string, 0, len(all))
	for _, w := range all {
		marker := ""
		if w.ID == active {
			marker = "*"
		}
		rows = append(rows, []string{
			marker, w.Name, w.ID,
			fmt.Sprintf("%d teams, %d projects", len(a.Store.Teams(w.ID)), len(a.Store.Projects(w.ID))),
		})
	}
	a.P.Table([]string{"", "NAME", "ID", "CONTENTS"}, rows)
	a.P.Blank()
	a.P.Hint("* = active. Switch with: synchro ws use <name>")
	return nil
}

func boolStr(b bool) string {
	if b {
		return "yes"
	}
	return "no"
}
