package cli

import (
	"fmt"
	"strings"

	"github.com/spf13/cobra"
	"github.com/synchro/synchro/internal/agents"
	"github.com/synchro/synchro/internal/llm"
	"github.com/synchro/synchro/internal/model"
)

func newAgentCmd(st *rootState) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "agent <command>",
		Short: "Manage agents",
		RunE: func(cmd *cobra.Command, args []string) error {
			a, err := st.app()
			if err != nil {
				return err
			}
			team, err := a.RequireTeam()
			if err != nil {
				return err
			}
			list := a.Store.Agents(team.ID)
			if len(list) == 0 {
				a.P.Info("team %q has no agents yet.", team.Name)
				a.P.Info("add one:  synchro agent new \"Developer\" --role developer")
				return nil
			}
			active := ""
			if ag, err := a.resolveAgent(); err == nil {
				active = ag.ID
			}
			if st.opt.JSON {
				a.P.Printf("[")
				for i, ag := range list {
					if i > 0 {
						a.P.Printf(",")
					}
					a.P.Printf("{\"id\":%q,\"name\":%q,\"role\":%q,\"provider\":%q,\"model\":%q}", ag.ID, ag.Name, ag.Role, ag.Provider, ag.Model)
				}
				a.P.Printf("]\n")
				return nil
			}
			printAgentTable(a, list, active)
			a.P.Blank()
			a.P.Hint("* = active. Switch with: synchro agent use <name|number>")
			return nil
		},
	}

	var (
		role, jobTitle, description, prompt, provider, modelName string
		temperature                                              float64
	)
	newCmd := &cobra.Command{
		Use:     "new <name>",
		Aliases: []string{"create", "add"},
		Short:   "Create an agent",
		Long: strings.TrimSpace(`
The role is not cosmetic: it decides how the agent behaves.

  researcher    grounds its answer in live web search results, with citations
  developer     answers with real files, parsed out of the reply and committed
                to the project's git repository
  qa            reviews another agent's work for concrete bugs
  product_manager, marketer, critic, ceo, synthesizer, validator
                use the matching system prompt from a template when you have one

If you do not pass --prompt, the role's default system prompt is used, which is
the fastest way to get a working agent.`),
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			a, err := st.app()
			if err != nil {
				return err
			}
			team, err := a.RequireTeam()
			if err != nil {
				return err
			}
			cfg := a.Store.Config()

			r := model.RoleDeveloper
			if role != "" {
				parsed, ok := model.ParseRole(role)
				if !ok {
					return fmt.Errorf("unknown role %q. Valid roles: %s", role, roleList())
				}
				r = parsed
			}
			p := provider
			if p == "" {
				p = cfg.Provider
			}
			m := modelName
			if m == "" {
				m = cfg.Model
			}
			sp := prompt
			if sp == "" {
				sp = DefaultSystemPrompt(r)
			}
			if temperature == 0 {
				temperature = cfg.Temperature
			}
			if temperature == 0 {
				temperature = 0.7
			}
			ag := &model.Agent{
				TeamID:       team.ID,
				Name:         args[0],
				Role:         r,
				JobTitle:     jobTitle,
				Description:  description,
				SystemPrompt: sp,
				Provider:     p,
				Model:        m,
				Status:       model.StatusActive,
				Temperature:  temperature,
			}
			if err := a.Store.CreateAgent(ag); err != nil {
				return err
			}
			if err := a.SetActive("", "", ag.ID, ""); err != nil {
				return err
			}
			a.P.Success("agent %s (%s) created", a.P.Bold(ag.Name), ag.Role)
			a.P.KeyValue("model", fmt.Sprintf("%s/%s", ag.Provider, ag.Model))
			if !llm.IsFreeModel(ag.Provider, ag.Model) {
				cost := llm.EstimateCostUSD(ag.Provider, ag.Model, 1000)
				a.P.Hint("this model is billable to your own key (~%s per 1K tokens)", money(cost))
			} else {
				a.P.Hint("this model is free")
			}
			return nil
		},
	}
	newCmd.Flags().StringVar(&role, "role", "", "agent role (researcher, developer, qa, ...)")
	newCmd.Flags().StringVar(&jobTitle, "job-title", "", "short title shown in listings")
	newCmd.Flags().StringVarP(&description, "description", "d", "", "one-line description")
	newCmd.Flags().StringVar(&prompt, "prompt", "", "system prompt (defaults to the role's)")
	newCmd.Flags().StringVar(&provider, "provider", "", "provider override (ollama, gemini, openrouter, ...)")
	newCmd.Flags().StringVar(&modelName, "model", "", "model override")
	newCmd.Flags().Float64Var(&temperature, "temperature", 0, "sampling temperature")

	useCmd := &cobra.Command{
		Use:   "use <name|number>",
		Short: "Switch the active agent",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			a, err := st.app()
			if err != nil {
				return err
			}
			team, err := a.RequireTeam()
			if err != nil {
				return err
			}
			ag, err := a.Store.Agent(args[0], team.ID)
			if err != nil {
				return fmt.Errorf("no agent called %q in team %q", args[0], team.Name)
			}
			if err := a.SetActive("", "", ag.ID, ""); err != nil {
				return err
			}
			a.P.Success("now acting as %s (%s)", a.P.Bold(ag.Name), ag.Role)
			return nil
		},
	}

	showCmd := &cobra.Command{
		Use:   "show [name]",
		Short: "Show an agent's configuration",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			a, err := st.app()
			if err != nil {
				return err
			}
			var ag *model.Agent
			if len(args) == 1 {
				team, err := a.RequireTeam()
				if err != nil {
					return err
				}
				ag, err = a.Store.Agent(args[0], team.ID)
				if err != nil {
					return fmt.Errorf("no agent called %q", args[0])
				}
			} else {
				ag, err = a.RequireAgent()
				if err != nil {
					return err
				}
			}
			a.P.Title(ag.Name)
			if ag.JobTitle != "" {
				a.P.Printf("  %s\n\n", a.P.Dim(ag.JobTitle))
			}
			a.P.KeyValue("id", ag.ID)
			a.P.KeyValue("role", string(ag.Role))
			a.P.KeyValue("provider", ag.Provider)
			a.P.KeyValue("model", ag.Model)
			a.P.KeyValue("temperature", fmt.Sprintf("%.2f", ag.Temperature))
			a.P.KeyValue("status", string(ag.Status))
			if ag.Description != "" {
				a.P.KeyValue("about", ag.Description)
			}
			if ag.SystemPrompt != "" {
				a.P.Blank()
				a.P.Info("system prompt")
				for _, line := range strings.Split(ag.SystemPrompt, "\n") {
					a.P.Printf("    %s\n", a.P.Dim(line))
				}
			}
			return nil
		},
	}

	var (
		editRole, editProvider, editModel, editPrompt, editStatus, editJobTitle string
		editTemp                                                                float64
	)
	editCmd := &cobra.Command{
		Use:   "edit <name|number>",
		Short: "Change an agent's configuration",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			a, err := st.app()
			if err != nil {
				return err
			}
			team, err := a.RequireTeam()
			if err != nil {
				return err
			}
			ag, err := a.Store.Agent(args[0], team.ID)
			if err != nil {
				return fmt.Errorf("no agent called %q", args[0])
			}
			changed := []string{}
			if editRole != "" {
				parsed, ok := model.ParseRole(editRole)
				if !ok {
					return fmt.Errorf("unknown role %q. Valid roles: %s", editRole, roleList())
				}
				ag.Role = parsed
				changed = append(changed, "role")
			}
			if editJobTitle != "" {
				ag.JobTitle = editJobTitle
				changed = append(changed, "job title")
			}
			if editProvider != "" {
				if _, ok := llm.Lookup(editProvider); !ok {
					return fmt.Errorf("unknown provider %q. Known: %s", editProvider, strings.Join(llm.ProviderNames(), ", "))
				}
				ag.Provider = editProvider
				changed = append(changed, "provider")
			}
			if editModel != "" {
				ag.Model = editModel
				changed = append(changed, "model")
			}
			if cmd.Flags().Changed("temperature") {
				ag.Temperature = editTemp
				changed = append(changed, "temperature")
			}
			if editStatus != "" {
				ag.Status = model.Status(strings.ToUpper(editStatus))
				changed = append(changed, "status")
			}
			if cmd.Flags().Changed("prompt") {
				ag.SystemPrompt = editPrompt
				changed = append(changed, "system prompt")
			}
			if len(changed) == 0 {
				return fmt.Errorf("nothing to change. Pass a flag, e.g. --model qwen3")
			}
			if err := a.Store.UpdateAgent(ag); err != nil {
				return err
			}
			a.P.Success("updated %s: %s", a.P.Bold(ag.Name), strings.Join(changed, ", "))
			return nil
		},
	}
	editCmd.Flags().StringVar(&editRole, "role", "", "new role")
	editCmd.Flags().StringVar(&editJobTitle, "job-title", "", "new job title")
	editCmd.Flags().StringVar(&editProvider, "provider", "", "new provider")
	editCmd.Flags().StringVar(&editModel, "model", "", "new model")
	editCmd.Flags().Float64Var(&editTemp, "temperature", 0, "new temperature")
	editCmd.Flags().StringVar(&editStatus, "status", "", "active or inactive")
	editCmd.Flags().StringVar(&editPrompt, "prompt", "", "replace the system prompt")

	rmCmd := &cobra.Command{
		Use:     "rm <name|number>",
		Aliases: []string{"remove", "delete", "del"},
		Short:   "Delete an agent",
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			a, err := st.app()
			if err != nil {
				return err
			}
			team, err := a.RequireTeam()
			if err != nil {
				return err
			}
			ag, err := a.Store.Agent(args[0], team.ID)
			if err != nil {
				return fmt.Errorf("no agent called %q", args[0])
			}
			if !confirm(a, st.opt.Yes, fmt.Sprintf("delete agent %q?", ag.Name)) {
				a.P.Info("cancelled")
				return nil
			}
			if err := a.Store.DeleteAgent(ag.ID, team.ID); err != nil {
				return err
			}
			a.P.Success("agent %s deleted", ag.Name)
			return nil
		},
	}

	cmd.AddCommand(newCmd, useCmd, showCmd, editCmd, rmCmd)
	return cmd
}

// DefaultSystemPrompt returns the built-in prompt for a role, taken from the
// closest template so a hand-made agent behaves like a templated one.
func DefaultSystemPrompt(r model.Role) string {
	// Prefer a template agent with the same role and the most specific prompt.
	best := ""
	for _, t := range agents.Templates {
		for _, ta := range t.Agents {
			if ta.Role == string(r) && len(ta.SystemPrompt) > len(best) {
				best = ta.SystemPrompt
			}
		}
	}
	if best != "" {
		return best
	}
	return fallbackPrompt(r)
}

// fallbackPrompt covers roles that no template ships.
func fallbackPrompt(r model.Role) string {
	switch r {
	case model.RoleCEO:
		return `You are the CEO of a small product team. Given a task, decide what actually matters: the one goal, the two or three things that must be true for it to succeed, and what to explicitly not do. Be decisive and brief. When other agents' work is provided as input, evaluate it against those criteria rather than restating it.`
	case model.RoleSynthesizer:
		return `You are a synthesizer. Given several agents' outputs, merge them into one coherent result: resolve contradictions explicitly by saying which source you trust and why, drop points that do not support the conclusion, and keep every specific claim with its citation. Produce a single answer, not a summary of summaries.`
	case model.RoleValidator:
		return `You are a validator. Given a claim, a spec or a piece of work, check it against its stated requirements and report what holds and what does not. For each problem, state the requirement it violates, how to reproduce or observe it, and the smallest fix. Do not introduce new requirements and do not pad with praise.`
	case model.RoleResearcher:
		return `You are a research analyst. Ground every claim in the search results provided, cite the source URL next to it, and explicitly flag anything you are inferring rather than citing. If the results do not answer the question, say that plainly instead of filling the gap from memory.`
	case model.RoleDeveloper:
		return `You are a pragmatic full-stack developer. Implement what is asked as working code, not a description of code. Prefer simple, direct solutions over premature abstraction, and avoid adding features or configuration that were not requested. Make a reasonable assumption and note it briefly rather than leaving the work incomplete.`
	}
	return fmt.Sprintf("You are an AI agent working as %s. Be concrete, specific and honest about what you do not know.", strings.ToLower(string(r)))
}

func roleList() string {
	out := make([]string, 0, len(model.AllRoles))
	for _, r := range model.AllRoles {
		out = append(out, strings.ToLower(string(r)))
	}
	return strings.Join(out, ", ")
}
