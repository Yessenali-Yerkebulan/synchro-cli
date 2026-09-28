package cli

import (
	"fmt"
	"strings"

	"github.com/spf13/cobra"
	"github.com/synchro/synchro-cli/internal/agents"
	"github.com/synchro/synchro-cli/internal/model"
)

func newTeamCmd(st *rootState) *cobra.Command {
	cmd := &cobra.Command{
		Use:     "team <command>",
		Aliases: []string{"t"},
		Short:   "Manage AI teams",
		RunE: func(cmd *cobra.Command, args []string) error {
			a, err := st.app()
			if err != nil {
				return err
			}
			ws, err := a.RequireWorkspace()
			if err != nil {
				return err
			}
			teams := a.Store.Teams(ws.ID)
			if len(teams) == 0 {
				a.P.Info("no teams in %s yet.", ws.Name)
				a.P.Info("Start from a template:  synchro-cli team from mvp")
				return nil
			}
			active := ""
			if t, err := a.resolveTeam(); err == nil {
				active = t.ID
			}
			rows := make([][]string, 0, len(teams))
			for _, t := range teams {
				marker := ""
				if t.ID == active {
					marker = "*"
				}
				teamAgents := a.Store.Agents(t.ID)
				roles := map[string]bool{}
				for _, ag := range teamAgents {
					roles[string(ag.Role)] = true
				}
				rows = append(rows, []string{
					marker, t.Name, t.ID,
					fmt.Sprintf("%d", len(teamAgents)),
					strings.Join(sortedKeys(roles), ", "),
				})
			}
			a.P.Table([]string{"", "NAME", "ID", "AGENTS", "ROLES"}, rows)
			a.P.Blank()
			a.P.Hint("* = active. Switch with: synchro-cli team use <name>")
			return nil
		},
	}

	var desc string
	newCmd := &cobra.Command{
		Use:     "new <name>",
		Aliases: []string{"create", "add"},
		Short:   "Create an empty team",
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
			t := &model.Team{WorkspaceID: ws.ID, Name: args[0], Description: desc}
			if err := a.Store.CreateTeam(t); err != nil {
				return err
			}
			if err := a.SetActive(ws.ID, t.ID, "", ""); err != nil {
				return err
			}
			a.P.Success("team %s created (%s)", a.P.Bold(t.Name), t.ID)
			a.P.Hint("next: synchro-cli agent new \"<name>\" --role developer")
			return nil
		},
	}
	newCmd.Flags().StringVarP(&desc, "description", "d", "", "what this team does")

	fromCmd := &cobra.Command{
		Use:   "from <template> [name]",
		Short: "Create a team from a starter template",
		Long: strings.TrimSpace(`
Templates bundle a role, a system prompt and a job title for each agent, so a
new team is useful immediately instead of being a blank page.

Every agent is created against the default provider and model, which is local
Ollama by default: no key required.`),
		Args: cobra.RangeArgs(1, 2),
		RunE: func(cmd *cobra.Command, args []string) error {
			a, err := st.app()
			if err != nil {
				return err
			}
			ws, err := a.RequireWorkspace()
			if err != nil {
				return err
			}
			tpl, ok := agents.TemplateByID(args[0])
			if !ok {
				return fmt.Errorf("no template called %q. Available: %s", args[0], templateIDs())
			}
			name := tpl.Name
			if len(args) == 2 {
				name = args[1]
			}
			if err := createTeamFromTemplate(a, ws, tpl, name); err != nil {
				return err
			}
			return nil
		},
	}

	templatesCmd := &cobra.Command{
		Use:   "templates",
		Short: "List the starter templates",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			a, err := st.app()
			if err != nil {
				return err
			}
			rows := make([][]string, 0, len(agents.Templates))
			for _, t := range agents.Templates {
				roles := make([]string, 0, len(t.Agents))
				for _, ag := range t.Agents {
					roles = append(roles, string(ag.Role))
				}
				rows = append(rows, []string{t.ID, t.Name, t.Description, strings.Join(roles, " → ")})
			}
			a.P.Table([]string{"ID", "NAME", "WHAT IT DOES", "ROLES"}, rows)
			a.P.Blank()
			a.P.Hint("create one with: synchro-cli team from <id>")
			return nil
		},
	}

	useCmd := &cobra.Command{
		Use:   "use <name>",
		Short: "Switch to a team",
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
			t, err := a.Store.Team(args[0], ws.ID)
			if err != nil {
				return fmt.Errorf("no team called %q in workspace %q", args[0], ws.Name)
			}
			if err := a.SetActive(ws.ID, t.ID, "", ""); err != nil {
				return err
			}
			a.P.Success("now in team %s", a.P.Bold(t.Name))
			return nil
		},
	}

	showCmd := &cobra.Command{
		Use:   "show [name]",
		Short: "Show a team and its agents",
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
			var t *model.Team
			if ref == "" {
				t, err = a.RequireTeam()
				if err != nil {
					return err
				}
			} else {
				ws, err := a.RequireWorkspace()
				if err != nil {
					return err
				}
				t, err = a.Store.Team(ref, ws.ID)
				if err != nil {
					return fmt.Errorf("no team called %q", ref)
				}
			}
			a.P.Title(t.Name)
			if t.Description != "" {
				a.P.Printf("  %s\n\n", a.P.Dim(t.Description))
			}
			a.P.KeyValue("id", t.ID)
			if t.TemplateID != "" {
				a.P.KeyValue("from template", t.TemplateID)
			}
			a.P.KeyValue("workflows", fmt.Sprintf("%d", len(a.Store.Workflows(t.ID))))
			teamAgents := a.Store.Agents(t.ID)
			if len(teamAgents) == 0 {
				a.P.Blank()
				a.P.Info("this team has no agents yet")
				return nil
			}
			a.P.Blank()
			printAgentTable(a, teamAgents, "")
			return nil
		},
	}

	rmCmd := &cobra.Command{
		Use:     "rm <name>",
		Aliases: []string{"remove", "delete", "del"},
		Short:   "Delete a team and its agents, projects and workflows",
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
			t, err := a.Store.Team(args[0], ws.ID)
			if err != nil {
				return fmt.Errorf("no team called %q", args[0])
			}
			if !confirm(a, st.opt.Yes, fmt.Sprintf("delete team %q, its %d agents, %d projects and %d workflows?", t.Name, len(a.Store.Agents(t.ID)), len(a.Store.Projects(ws.ID)), len(a.Store.Workflows(t.ID)))) {
				a.P.Info("cancelled")
				return nil
			}
			if err := a.Store.DeleteTeam(t.ID, ws.ID); err != nil {
				return err
			}
			a.P.Success("team %s deleted", t.Name)
			return nil
		},
	}

	cmd.AddCommand(newCmd, fromCmd, templatesCmd, useCmd, showCmd, rmCmd)
	return cmd
}

// createTeamFromTemplate instantiates a template as a real team + agents.
func createTeamFromTemplate(a *App, ws *model.Workspace, tpl agents.Template, name string) error {
	cfg := a.Store.Config()
	t := &model.Team{
		WorkspaceID: ws.ID,
		Name:        name,
		Description: tpl.Description,
		TemplateID:  tpl.ID,
	}
	if err := a.Store.CreateTeam(t); err != nil {
		return err
	}
	for _, ta := range tpl.Agents {
		role, ok := model.ParseRole(ta.Role)
		if !ok {
			// A template with a bad role is a bug in this binary, not user
			// input, so say so rather than quietly creating a Developer.
			return fmt.Errorf("template %s agent %q has unknown role %q", tpl.ID, ta.Name, ta.Role)
		}
		ag := &model.Agent{
			TeamID:       t.ID,
			Name:         ta.Name,
			Role:         role,
			JobTitle:     ta.JobTitle,
			Description:  ta.Description,
			SystemPrompt: ta.SystemPrompt,
			Provider:     cfg.Provider,
			Model:        cfg.Model,
			Status:       model.StatusActive,
			Temperature:  cfg.Temperature,
		}
		if err := a.Store.CreateAgent(ag); err != nil {
			return err
		}
	}
	// Preselect the template's lead agent, so a fresh install can run a task
	// without the user choosing anything. Store.Agents is name-sorted, so look
	// the agent up by name rather than taking the first of the list.
	first := ""
	if len(tpl.Agents) > 0 {
		if lead, err := a.Store.Agent(tpl.Agents[0].Name, t.ID); err == nil {
			first = lead.ID
		}
	}
	if err := a.SetActive(ws.ID, t.ID, first, ""); err != nil {
		return err
	}
	a.P.Success("team %s created from template %s", a.P.Bold(t.Name), tpl.ID)
	rows := make([][]string, 0, len(tpl.Agents))
	for i, ta := range tpl.Agents {
		rows = append(rows, []string{fmt.Sprintf("%d", i+1), ta.Name, ta.Role, a.P.Gray(fmt.Sprintf("%s/%s", cfg.Provider, cfg.Model))})
	}
	a.P.Table([]string{"#", "AGENT", "ROLE", "MODEL"}, rows)
	a.P.Blank()
	a.P.Hint("try it:  synchro-cli run \"<what you want built>\"")
	return nil
}

func printAgentTable(a *App, list []model.Agent, activeID string) {
	rows := make([][]string, 0, len(list))
	for i, ag := range list {
		marker := ""
		if ag.ID == activeID {
			marker = "*"
		}
		rows = append(rows, []string{
			marker,
			fmt.Sprintf("%d", i+1),
			ag.Name,
			string(ag.Role),
			fmt.Sprintf("%s/%s", ag.Provider, ag.Model),
		})
	}
	a.P.Table([]string{"", "#", "AGENT", "ROLE", "MODEL"}, rows)
}

func templateIDs() string {
	ids := make([]string, 0, len(agents.Templates))
	for _, t := range agents.Templates {
		ids = append(ids, t.ID)
	}
	return strings.Join(ids, ", ")
}

func sortedKeys(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	// Stable order: sort by role name.
	for i := 0; i < len(out); i++ {
		for j := i + 1; j < len(out); j++ {
			if out[j] < out[i] {
				out[i], out[j] = out[j], out[i]
			}
		}
	}
	return out
}
