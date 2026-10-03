package cli

import (
	"fmt"
	"strings"

	"github.com/spf13/cobra"
	"github.com/synchro/synchro-cli/internal/agents"
	"github.com/synchro/synchro-cli/internal/model"
)

func newWorkflowCmd(st *rootState) *cobra.Command {
	cmd := &cobra.Command{
		Use:     "wf <command>",
		Aliases: []string{"workflow", "flow"},
		Short:   "Build and run multi-agent workflows",
		Long: strings.TrimSpace(`
A workflow is a graph: each node is an agent, and each edge says which agent
runs next. Nodes wait for all of their predecessors and receive their combined
output, so a graph can genuinely branch and merge.

  synchro-cli wf new "ship a feature" --agents researcher,developer,qa
  synchro-cli wf run "ship a feature" --input "add dark mode"

Workflows are the difference between a chat and a process: the output of one
agent is the input of the next, with no copy-paste in between.`),
		RunE: func(cmd *cobra.Command, args []string) error {
			a, err := st.app()
			if err != nil {
				return err
			}
			team, err := a.RequireTeam()
			if err != nil {
				return err
			}
			list := a.Store.Workflows(team.ID)
			if len(list) == 0 {
				a.P.Info("no workflows in %s yet.", team.Name)
				a.P.Info("create one with:  synchro-cli wf new \"<name>\" --agents researcher,developer,qa")
				return nil
			}
			rows := make([][]string, 0, len(list))
			for _, w := range list {
				rows = append(rows, []string{
					w.Name, w.ID,
					fmt.Sprintf("%d nodes, %d edges", len(w.Graph.Nodes), len(w.Graph.Edges)),
					fmt.Sprintf("%d runs", len(a.Store.Executions(w.ID))),
				})
			}
			a.P.Table([]string{"NAME", "ID", "GRAPH", "HISTORY"}, rows)
			a.P.Blank()
			a.P.Hint("run one with: synchro-cli wf run <name> --input \"<what to work on>\"")
			return nil
		},
	}

	var (
		agentList string
		desc      string
		retries   int
		backoff   float64
	)
	newCmd := &cobra.Command{
		Use:     "new <name>",
		Aliases: []string{"create", "add"},
		Short:   "Create a workflow by chaining agents in order",
		Long: strings.TrimSpace(`
Agents are chained in the order given, so this is a linear graph:

  synchro-cli wf new "research then write" --agents researcher,developer

Names are resolved to agents in the active team, by name or by number.`),
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
			if strings.TrimSpace(agentList) == "" {
				return fmt.Errorf("pass the agents to chain, e.g. --agents researcher,developer,qa")
			}
			teamAgents := a.Store.Agents(team.ID)
			var chosen []model.Agent
			for _, ref := range strings.Split(agentList, ",") {
				ref = strings.TrimSpace(ref)
				if ref == "" {
					continue
				}
				ag, err := a.Store.Agent(ref, team.ID)
				if err != nil {
					// Fall back to matching by role, which is what people mean
					// when they type "researcher".
					role, ok := model.ParseRole(ref)
					if !ok {
						return fmt.Errorf("no agent or role called %q in team %q", ref, team.Name)
					}
					found := false
					for _, cand := range teamAgents {
						if cand.Role == role {
							ag, found = &cand, true
							break
						}
					}
					if !found {
						return fmt.Errorf("team %q has no agent with role %s", team.Name, role)
					}
				}
				chosen = append(chosen, *ag)
			}
			if len(chosen) < 2 {
				return fmt.Errorf("a workflow needs at least two nodes; you gave %d", len(chosen))
			}
			wf := &model.Workflow{
				TeamID:         team.ID,
				Name:           args[0],
				Description:    desc,
				Graph:          agents.BuildLinearGraph(chosen),
				MaxRetries:     retries,
				BackoffSeconds: backoff,
			}
			if err := a.Store.CreateWorkflow(wf); err != nil {
				return err
			}
			a.P.Success("workflow %s created (%s)", a.P.Bold(wf.Name), wf.ID)
			printWorkflowGraph(a, wf)
			return nil
		},
	}
	newCmd.Flags().StringVar(&agentList, "agents", "", "comma-separated agents or roles, in order")
	newCmd.Flags().StringVarP(&desc, "description", "d", "", "what this workflow does")
	newCmd.Flags().IntVar(&retries, "retries", 2, "retries per node on failure")
	newCmd.Flags().Float64Var(&backoff, "backoff", 2, "seconds before the first retry; doubles each time")

	runCmd := &cobra.Command{
		Use:   "run <name>",
		Short: "Run a workflow",
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
			wf, err := a.Store.Workflow(args[0], team.ID)
			if err != nil {
				return fmt.Errorf("no workflow called %q in team %q", args[0], team.Name)
			}
			input, _ := cmd.Flags().GetString("input")
			if strings.TrimSpace(input) == "" {
				return fmt.Errorf("a workflow needs something to work on:  synchro-cli wf run %s --input \"<what to work on>\"", wf.Name)
			}
			return a.runWorkflow(wf, input)
		},
	}
	runCmd.Flags().StringP("input", "i", "", "the initial input passed to the first node")

	showCmd := &cobra.Command{
		Use:     "show <name>",
		Aliases: []string{"graph"},
		Short:   "Show a workflow's graph and run history",
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
			wf, err := a.Store.Workflow(args[0], team.ID)
			if err != nil {
				return fmt.Errorf("no workflow called %q", args[0])
			}
			a.P.Title(wf.Name)
			if wf.Description != "" {
				a.P.Printf("  %s\n\n", a.P.Dim(wf.Description))
			}
			a.P.KeyValue("id", wf.ID)
			a.P.KeyValue("retries per node", fmt.Sprintf("%d", wf.MaxRetries))
			a.P.Blank()
			printWorkflowGraph(a, wf)

			execs := a.Store.Executions(wf.ID)
			if len(execs) == 0 {
				a.P.Blank()
				a.P.Info("never run yet")
				return nil
			}
			a.P.Blank()
			a.P.Info("history")
			for _, ex := range execs {
				marker := a.P.Green("✓")
				if ex.Status != model.StatusCompleted {
					marker = a.P.Red("✗")
				}
				a.P.Printf("    %s %s %s\n", marker, ex.ID, a.P.Gray(fmt.Sprintf("%s · %.1fs · %d nodes", ex.StartedAt.Local().Format("01-02 15:04"), ex.DurationSeconds, len(ex.NodeResults))))
			}
			return nil
		},
	}

	rmCmd := &cobra.Command{
		Use:     "rm <name>",
		Aliases: []string{"remove", "delete", "del"},
		Short:   "Delete a workflow and its run history",
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
			wf, err := a.Store.Workflow(args[0], team.ID)
			if err != nil {
				return fmt.Errorf("no workflow called %q", args[0])
			}
			if !confirm(a, st.opt.Yes, fmt.Sprintf("delete workflow %q and its run history?", wf.Name)) {
				a.P.Info("cancelled")
				return nil
			}
			if err := a.Store.DeleteWorkflow(wf.ID, team.ID); err != nil {
				return err
			}
			a.P.Success("workflow %s deleted", wf.Name)
			return nil
		},
	}

	cmd.AddCommand(newCmd, runCmd, showCmd, rmCmd)
	return cmd
}

// printWorkflowGraph draws the chain so the structure is obvious at a glance.
func printWorkflowGraph(a *App, wf *model.Workflow) {
	names := map[string]string{}
	for _, n := range wf.Graph.Nodes {
		agentName := n.AgentID
		if ag, err := a.Store.Agent(n.AgentID, ""); err == nil {
			agentName = ag.Name
		}
		names[n.ID] = agentName
	}
	for _, start := range wf.Graph.StartNodes() {
		var walk func(id string, depth int, seen map[string]bool)
		walk = func(id string, depth int, seen map[string]bool) {
			if seen[id] {
				a.P.Printf("%s%s %s\n", strings.Repeat("    ", depth), a.P.Gray("└─"), a.P.Bold(names[id]))
				return
			}
			seen[id] = true
			label := names[id]
			if depth == 0 {
				a.P.Printf("%s%s\n", a.P.Green("● "), a.P.Bold(label))
			} else {
				a.P.Printf("%s%s\n", strings.Repeat("    ", depth)+a.P.Gray("└─ "), a.P.Bold(label))
			}
			for _, next := range wf.Graph.NextNodes(id) {
				walk(next, depth+1, seen)
			}
		}
		walk(start, 0, map[string]bool{})
	}
}

// runWorkflow executes a graph, streaming each node and printing a summary.
func (a *App) runWorkflow(wf *model.Workflow, input string) error {
	a.P.Printf("\n%s %s\n", a.P.Bold("▸"), a.P.Bold(wf.Name))
	a.P.Printf("  %s\n", a.P.Gray(input))
	a.P.Blank()

	e := a.NewEngine()
	stream := a.P.NewStream(false)
	var current string

	streamed := false
	e.OnDelta = func(text string) {
		if !streamed {
			// Print the node label once, then stream inline.
			a.P.Printf("%s %s\n", a.P.Gray("▸"), a.P.Bold(current))
			streamed = true
		}
		_, _ = stream.Write([]byte(text))
	}

	exec, err := e.RunWorkflow(a.Ctx, wf, input, func(p agents.NodeProgress) {
		switch p.Phase {
		case agents.NodeStart:
			streamed = false
			current = p.AgentName
			if p.Retrying {
				a.P.Warn("node %s attempt %d failed, retrying", p.NodeID, p.Attempt)
			}
		case agents.NodeFinish:
			stream.Flush()
			streamed = false
			a.P.Printf("%s %s %s\n", a.P.Green("✓"), a.P.Bold(p.AgentName), a.P.Gray(fmt.Sprintf("%d tokens", p.Tokens)))
			a.P.Blank()
		case agents.NodeFail:
			stream.Flush()
			streamed = false
			a.P.Printf("%s %s %s\n", a.P.Red("✗"), a.P.Bold(p.AgentName), a.P.Red(p.Error))
		}
	})
	stream.Flush()

	if exec != nil {
		if _, serr := a.Store.SaveExecution(exec); serr != nil {
			a.P.Warn("could not save run history: %v", serr)
		}
	}

	// A graph that never validated produces no run at all; say so instead of
	// walking an empty result set.
	if exec == nil {
		if err != nil {
			return err
		}
		return fmt.Errorf("workflow %q produced no run", wf.Name)
	}

	a.P.Rule()
	for _, nr := range exec.NodeResults {
		marker := a.P.Green("✓")
		if nr.Status != model.StatusCompleted {
			marker = a.P.Red("✗")
		}
		detail := fmt.Sprintf("%.1fs", nr.DurationSeconds)
		if nr.Attempts > 1 {
			detail += fmt.Sprintf(", %d attempts", nr.Attempts)
		}
		a.P.Printf("%s %s %s\n", marker, nr.AgentName, a.P.Gray(detail))
	}
	a.P.Blank()

	if exec.Status != model.StatusCompleted {
		return err
	}
	if exec.FinalResult != "" {
		a.P.Info("final result")
		a.P.Blank()
		a.P.Markdown(exec.FinalResult)
		a.P.Blank()
	}
	a.P.Hint("run saved as %s", exec.ID)
	return nil
}
