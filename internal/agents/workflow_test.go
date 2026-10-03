package agents

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/synchro/synchro-cli/internal/model"
	"github.com/synchro/synchro-cli/internal/store"
)

// failingEngine points ollama at a closed port so every call fails immediately
// and no test ever reaches the network.
func failingEngine(t *testing.T) (*Engine, *model.Agent) {
	t.Helper()
	s := mustStore(t)
	if err := s.SetConfig(func(c *store.Config) { c.OllamaURL = "http://127.0.0.1:1" }); err != nil {
		t.Fatalf("SetConfig: %v", err)
	}
	ws := &model.Workspace{Name: "w"}
	if err := s.CreateWorkspace(ws); err != nil {
		t.Fatalf("CreateWorkspace: %v", err)
	}
	team := &model.Team{WorkspaceID: ws.ID, Name: "t"}
	if err := s.CreateTeam(team); err != nil {
		t.Fatalf("CreateTeam: %v", err)
	}
	ag := &model.Agent{TeamID: team.ID, Name: "dev", Role: model.RoleDeveloper, Provider: "ollama", Model: "qwen3"}
	if err := s.CreateAgent(ag); err != nil {
		t.Fatalf("CreateAgent: %v", err)
	}
	return &Engine{Store: s}, ag
}

func oneNodeWorkflow(ag *model.Agent, retries int, backoff float64) *model.Workflow {
	return &model.Workflow{
		Name:           "wf",
		TeamID:         ag.TeamID,
		Graph:          model.Graph{Nodes: []model.Node{{ID: "n1", AgentID: ag.ID}}},
		MaxRetries:     retries,
		BackoffSeconds: backoff,
	}
}

// A node that keeps failing used to announce the same attempt twice: once when
// the retry was scheduled and again when it started.
func TestRetryAnnouncesEachAttemptOnce(t *testing.T) {
	e, ag := failingEngine(t)
	wf := oneNodeWorkflow(ag, 2, 0.01)

	seen := map[int]int{}
	_, err := e.RunWorkflow(context.Background(), wf, "go", func(p NodeProgress) {
		if p.Retrying {
			seen[p.Attempt]++
		}
	})
	if err == nil {
		t.Fatal("expected the run to fail")
	}
	if len(seen) != 2 {
		t.Fatalf("got retry notices for attempts %v, want 2 (attempts 1 and 2)", seen)
	}
	for attempt, n := range seen {
		if n != 1 {
			t.Errorf("attempt %d announced %d times, want 1", attempt, n)
		}
	}
}

// A graph that never validates produces no run at all, not a run with no nodes.
func TestInvalidGraphReturnsNoExecution(t *testing.T) {
	e, ag := failingEngine(t)
	wf := oneNodeWorkflow(ag, 0, 0)
	wf.Graph.Edges = []model.Edge{{From: "n1", To: "nope"}}

	exec, err := e.RunWorkflow(context.Background(), wf, "go", nil)
	if err == nil {
		t.Fatal("expected an invalid-graph error")
	}
	if exec != nil {
		t.Fatalf("got an execution for an invalid graph: %+v", exec)
	}
}

// Cancelling must not spend the rest of the retry budget.
func TestCancelStopsRetrying(t *testing.T) {
	e, ag := failingEngine(t)
	wf := oneNodeWorkflow(ag, 5, 0.01)

	ctx, cancel := context.WithCancel(context.Background())
	attempts := 0
	_, err := e.RunWorkflow(ctx, wf, "go", func(p NodeProgress) {
		if p.Retrying {
			attempts++
			cancel()
		}
	})
	if err == nil {
		t.Fatal("expected the run to fail after cancellation")
	}
	if attempts != 1 {
		t.Fatalf("kept retrying after cancel: %d notices", attempts)
	}
}

// A workflow run costs money just like a pipeline run, so it has to be recorded
// against the project it ran for.
func TestWorkflowRecordsSpend(t *testing.T) {
	e, ag := failingEngine(t)
	// Answer every call locally: the run has to succeed for spend to be booked.
	// Ollama is free, so bill the run to a priced provider to see the effect.
	if err := e.Store.SetConfig(func(c *store.Config) { c.Stream = false }); err != nil {
		t.Fatalf("SetConfig: %v", err)
	}
	e.HTTP = stubChat(t, "here is the code")
	ag.Provider, ag.Model = "openai", "gpt-4o"
	if err := e.Store.SetCredential("openai", "test-key", ""); err != nil {
		t.Fatalf("SetCredential: %v", err)
	}
	if err := e.Store.UpdateAgent(ag); err != nil {
		t.Fatalf("UpdateAgent: %v", err)
	}

	p := &model.Project{WorkspaceID: e.Store.Workspaces()[0].ID, TeamID: ag.TeamID, Name: "demo"}
	if err := e.Store.CreateProject(p); err != nil {
		t.Fatalf("CreateProject: %v", err)
	}
	before := spendTotal(t, e.Store, p.ID)
	if before != 0 {
		t.Fatalf("fresh project already has spend: %v", before)
	}

	wf := oneNodeWorkflow(ag, 0, 0)
	exec, err := e.RunWorkflow(context.Background(), wf, "go", nil)
	if err != nil {
		t.Fatalf("RunWorkflow: %v", err)
	}
	if exec.Status != model.StatusCompleted {
		t.Fatalf("run status = %q, want completed", exec.Status)
	}
	if len(exec.NodeResults) != 1 || exec.NodeResults[0].Status != model.StatusCompleted {
		t.Fatalf("node results = %+v, want one completed node", exec.NodeResults)
	}
	if got := spendTotal(t, e.Store, p.ID); got <= 0 {
		t.Error("a completed workflow run booked no spend")
	}
	// The task the run created has to belong to the project it ran for.
	tasks := e.Store.Tasks(p.ID)
	if len(tasks) != 1 {
		t.Fatalf("project has %d task(s), want 1", len(tasks))
	}
	if tasks[0].Status != model.StatusCompleted {
		t.Errorf("task status = %q, want completed", tasks[0].Status)
	}
}

func spendTotal(t *testing.T, s *store.Store, projectID string) float64 {
	t.Helper()
	p, err := s.Project(projectID, "")
	if err != nil {
		t.Fatalf("Project: %v", err)
	}
	return p.SpentUSD
}

// stubChat answers every chat call with a canned reply in whichever shape the
// provider expects, so tests can run the full success path with no model and no
// network.
func stubChat(t *testing.T, reply string) *http.Client {
	t.Helper()
	ollama := fmt.Sprintf(`{"message":{"role":"assistant","content":%q},"done":true,"prompt_eval_count":100,"eval_count":50}`, reply)
	openai := fmt.Sprintf(`{"choices":[{"index":0,"message":{"role":"assistant","content":%q},"finish_reason":"stop"}],"usage":{"total_tokens":150}}`, reply)
	return &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		body := ollama
		if strings.Contains(r.URL.Host, "openai") {
			body = openai
		}
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     http.Header{"Content-Type": []string{"application/json"}},
			Body:       io.NopCloser(strings.NewReader(body)),
			Request:    r,
		}, nil
	})}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
