package agents

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/synchro/synchro-cli/internal/model"
)

// NodePhase values passed to OnNode.
const (
	NodeStart  = "start"
	NodeFinish = "finish"
	NodeFail   = "fail"
)

// NodeProgress is the per-node callback payload.
type NodeProgress struct {
	NodeID    string
	AgentID   string
	AgentName string
	Phase     string
	Output    string
	Error     string
	Tokens    int
	Attempt   int
	Retrying  bool
}

// RunWorkflow executes a workflow graph.
//
// The web app's executor (synchro/app/utils/langgraph_executor.py) walked a
// single growing list: it never branched after a merge, and a node reachable
// by two paths could be run twice. This is a real topological execution - every
// node waits for all of its predecessors and receives their combined output.
func (e *Engine) RunWorkflow(ctx context.Context, wf *model.Workflow, input string, onNode func(NodeProgress)) (*model.WorkflowExecution, error) {
	if err := wf.Graph.Validate(); err != nil {
		return nil, fmt.Errorf("invalid workflow: %w", err)
	}

	exec := &model.WorkflowExecution{
		ID:           model.NewID(),
		WorkflowID:   wf.ID,
		Status:       model.StatusRunning,
		InitialInput: input,
		StartedAt:    time.Now().UTC(),
	}
	start := time.Now()

	// Pending predecessor counts drive the schedule.
	indegree := map[string]int{}
	for _, n := range wf.Graph.Nodes {
		indegree[n.ID] = 0
	}
	successors := map[string][]string{}
	for _, e := range wf.Graph.Edges {
		indegree[e.To]++
		successors[e.From] = append(successors[e.From], e.To)
	}

	// Node input is the initial input for roots, and the concatenation of every
	// predecessor's output for everyone else.
	inputs := map[string]string{}
	for _, id := range wf.Graph.StartNodes() {
		inputs[id] = input
	}

	ready := append([]string(nil), wf.Graph.StartNodes()...)
	remaining := len(wf.Graph.Nodes)
	var order []string

	for len(ready) > 0 {
		if err := ctx.Err(); err != nil {
			exec.Status = model.StatusFailed
			exec.DurationSeconds = time.Since(start).Seconds()
			exec.FinishedAt = time.Now().UTC()
			return exec, err
		}

		nodeID := ready[0]
		ready = ready[1:]

		node, _ := wf.Graph.Node(nodeID)
		agent, err := e.Store.Agent(node.AgentID, "")
		if err != nil {
			// A node whose agent was deleted fails the run loudly; silently
			// skipping would produce a plausible-looking but wrong result.
			exec.Status = model.StatusFailed
			exec.DurationSeconds = time.Since(start).Seconds()
			exec.FinishedAt = time.Now().UTC()
			return exec, fmt.Errorf("workflow node %q references agent %q which no longer exists", nodeID, node.AgentID)
		}

		nodeStart := time.Now()
		if onNode != nil {
			onNode(NodeProgress{NodeID: nodeID, AgentID: agent.ID, AgentName: agent.Name, Phase: NodeStart})
		}

		// Route streamed tokens to this node's callback.
		prevDelta := e.OnDelta
		if onNode != nil {
			e.OnDelta = func(text string) {
				onNode(NodeProgress{NodeID: nodeID, AgentName: agent.Name, Output: text, Phase: "delta"})
			}
		}

		task := &model.Task{
			ProjectID:       firstProjectFor(wf.TeamID, e),
			AssignedAgentID: agent.ID,
			Title:           fmt.Sprintf("[%s] %s", wf.Name, agent.Name),
			Description:     inputs[nodeID],
			Status:          model.StatusRunning,
		}
		_ = e.Store.CreateTask(task)

		maxRetries, backoff := e.nodeRetryPolicy(wf, node)
		var res *model.TaskResult
		var runErr error
		attempts := 0
	retry:
		for attempt := 1; attempt <= maxRetries+1; attempt++ {
			attempts = attempt
			res, runErr = e.RunTask(ctx, task, agent)
			if runErr == nil {
				break
			}
			if attempt > maxRetries {
				break
			}
			// Report the attempt that just failed, once. The next iteration is
			// the retry itself and must not announce the same attempt again.
			if onNode != nil {
				onNode(NodeProgress{NodeID: nodeID, AgentName: agent.Name, Phase: NodeStart, Retrying: true, Attempt: attempt})
			}
			delay := time.Duration(backoff * float64(int(1)<<uint(attempt-1)) * float64(time.Second))
			select {
			case <-ctx.Done():
				runErr = ctx.Err()
				break retry
			case <-time.After(delay):
			}
		}
		e.OnDelta = prevDelta

		nr := model.NodeResult{
			NodeID:          nodeID,
			AgentID:         agent.ID,
			AgentName:       agent.Name,
			Attempts:        attempts,
			DurationSeconds: time.Since(nodeStart).Seconds(),
		}
		if runErr != nil {
			nr.Status = model.StatusFailed
			nr.Error = runErr.Error()
			task.Status = model.StatusFailed
			task.ErrorMessage = runErr.Error()
			_ = e.Store.UpdateTask(task)
			exec.NodeResults = append(exec.NodeResults, nr)
			order = append(order, nodeID)
			remaining--
			if onNode != nil {
				onNode(NodeProgress{NodeID: nodeID, AgentName: agent.Name, Phase: NodeFail, Error: runErr.Error(), Attempt: attempts})
			}
			exec.Status = model.StatusFailed
			exec.DurationSeconds = time.Since(start).Seconds()
			exec.FinishedAt = time.Now().UTC()
			return exec, runErr
		}

		nr.Status = model.StatusCompleted
		nr.Output = res.Output
		nr.TokensUsed = res.TokensUsed
		task.Status = model.StatusCompleted
		task.Result = res
		_ = e.Store.UpdateTask(task)
		exec.NodeResults = append(exec.NodeResults, nr)
		order = append(order, nodeID)
		remaining--
		if task.ProjectID != "" {
			_ = e.Store.AddSpent(task.ProjectID, llmCost(agent, res))
		}

		if onNode != nil {
			onNode(NodeProgress{NodeID: nodeID, AgentName: agent.Name, Phase: NodeFinish, Tokens: res.TokensUsed, Attempt: attempts})
		}

		// Propagate this node's output and unblock successors.
		for _, succ := range successors[nodeID] {
			inputs[succ] = joinInputs(inputs[succ], res.Output)
			indegree[succ]--
			if indegree[succ] == 0 {
				ready = append(ready, succ)
			}
		}
	}

	if remaining > 0 {
		// Nothing is runnable but nodes are left: the graph has a cycle.
		var stuck []string
		for _, n := range wf.Graph.Nodes {
			if indegree[n.ID] > 0 {
				stuck = append(stuck, n.ID)
			}
		}
		exec.Status = model.StatusFailed
		exec.DurationSeconds = time.Since(start).Seconds()
		exec.FinishedAt = time.Now().UTC()
		return exec, fmt.Errorf("workflow has a cycle involving: %s (these nodes never became runnable)", strings.Join(stuck, ", "))
	}

	// The final result is the output of the last node to complete.
	for i := len(exec.NodeResults) - 1; i >= 0; i-- {
		if exec.NodeResults[i].Status == model.StatusCompleted {
			exec.FinalResult = exec.NodeResults[i].Output
			break
		}
	}
	exec.Status = model.StatusCompleted
	exec.DurationSeconds = time.Since(start).Seconds()
	exec.FinishedAt = time.Now().UTC()
	return exec, nil
}

// nodeRetryPolicy resolves per-node overrides against workflow defaults.
func (e *Engine) nodeRetryPolicy(wf *model.Workflow, node model.Node) (maxRetries int, backoff float64) {
	maxRetries, backoff = wf.MaxRetries, wf.BackoffSeconds
	if maxRetries < 0 {
		maxRetries = 0
	}
	if backoff <= 0 {
		backoff = 2
	}
	if node.MaxRetries != nil {
		maxRetries = *node.MaxRetries
	}
	if node.RetryBackoffSeconds != nil {
		backoff = *node.RetryBackoffSeconds
	}
	return maxRetries, backoff
}

// firstProjectFor picks a project to attach workflow-generated tasks to. A
// workflow is team-scoped and may run before any project exists, in which case
// tasks are stored with an empty project id rather than being dropped.
func firstProjectFor(teamID string, e *Engine) string {
	for _, p := range e.Store.Projects("") {
		if p.TeamID == teamID {
			return p.ID
		}
	}
	return ""
}

func joinInputs(existing, add string) string {
	if existing == "" {
		return add
	}
	return existing + "\n\n" + add
}

// BuildLinearGraph is the common case: chain every agent in order. It is what
// `synchro-cli wf new <name> --all` uses, and it produces a graph that is correct by
// construction rather than something the user has to wire up.
func BuildLinearGraph(agents []model.Agent) model.Graph {
	g := model.Graph{}
	for i, a := range agents {
		g.Nodes = append(g.Nodes, model.Node{ID: fmt.Sprintf("n%d", i+1), AgentID: a.ID})
		if i > 0 {
			g.Edges = append(g.Edges, model.Edge{From: fmt.Sprintf("n%d", i), To: fmt.Sprintf("n%d", i+1)})
		}
	}
	return g
}

// ErrNoNodes is returned when a workflow definition is empty.
var ErrNoNodes = errors.New("workflow has no nodes")
