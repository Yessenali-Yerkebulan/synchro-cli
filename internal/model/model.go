// Package model holds the Synchro domain types.
//
// This is a local-first port of the Synchro web app (FastAPI + Postgres). The
// shape of the domain is intentionally identical - workspaces own teams, teams
// own agents and workflows, teams own projects, projects own tasks - so that
// the CLI and the web app stay conceptually interchangeable. The differences
// are deliberate and are listed on each type.
package model

import (
	"crypto/rand"
	"fmt"
	"math/big"
	"time"
)

// Role is an agent's function in the team. Ported 1:1 from
// synchro/app/models/agent.py:AgentRole.
type Role string

const (
	RoleCEO            Role = "CEO"
	RoleResearcher     Role = "RESEARCHER"
	RoleSynthesizer    Role = "SYNTHESIZER"
	RoleValidator      Role = "VALIDATOR"
	RoleProductManager Role = "PRODUCT_MANAGER"
	RoleDeveloper      Role = "DEVELOPER"
	RoleQA             Role = "QA"
	RoleMarketer       Role = "MARKETER"
	RoleCritic         Role = "CRITIC"
)

// AllRoles is the ordered list used by CLI help and validation.
var AllRoles = []Role{
	RoleCEO, RoleResearcher, RoleSynthesizer, RoleValidator,
	RoleProductManager, RoleDeveloper, RoleQA, RoleMarketer, RoleCritic,
}

func (r Role) Valid() bool {
	for _, x := range AllRoles {
		if x == r {
			return true
		}
	}
	return false
}

// ParseRole normalizes free-form user input ("product manager" -> PRODUCT_MANAGER).
func ParseRole(s string) (Role, bool) {
	norm := func(v string) string {
		out := make([]rune, 0, len(v))
		for _, c := range v {
			switch {
			case c == ' ', c == '-', c == '_', c == '.':
			case c >= 'a' && c <= 'z':
				out = append(out, c-32)
			default:
				out = append(out, c)
			}
		}
		return string(out)
	}
	up := norm(s)
	for _, r := range AllRoles {
		// Compare with separators stripped from both sides: "PRODUCT_MANAGER",
		// "product manager" and "product-manager" all have to match.
		if norm(string(r)) == up {
			return r, true
		}
	}
	return "", false
}

// Status is a shared lifecycle flag for agents, teams and tasks.
type Status string

const (
	StatusPending   Status = "PENDING"
	StatusRunning   Status = "RUNNING"
	StatusCompleted Status = "COMPLETED"
	StatusFailed    Status = "FAILED"
	StatusActive    Status = "ACTIVE"
	StatusInactive  Status = "INACTIVE"
)

// Workspace is a top-level container. Unlimited by design: the web app capped
// this at 1 on the FREE plan (synchro/app/data/plan_limits.py), which is gone here.
type Workspace struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
	// AutoCommit makes DEVELOPER agents commit generated files to the project
	// repo without a human "approve" step. Defaults to false, same as the web app.
	AutoCommitAgentCode bool      `json:"auto_commit_agent_code"`
	CreatedAt           time.Time `json:"created_at"`
	UpdatedAt           time.Time `json:"updated_at"`
}

// Team is a named group of agents inside a workspace.
type Team struct {
	ID          string `json:"id"`
	WorkspaceID string `json:"workspace_id"`
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
	// TemplateID records which starter template produced this team, if any.
	TemplateID string    `json:"template_id,omitempty"`
	CreatedAt  time.Time `json:"created_at"`
	UpdatedAt  time.Time `json:"updated_at"`
}

// Agent is one LLM-backed worker.
type Agent struct {
	ID           string `json:"id"`
	TeamID       string `json:"team_id"`
	Name         string `json:"name"`
	Role         Role   `json:"role"`
	JobTitle     string `json:"job_title,omitempty"`
	Description  string `json:"description,omitempty"`
	SystemPrompt string `json:"system_prompt,omitempty"`
	// Provider and Model default to Ollama because it is the only option that
	// is unconditionally free with no account and no key.
	Provider string `json:"provider"`
	Model    string `json:"model"`
	Status   Status `json:"status"`
	// Temperature is per-agent so a CRITIC can run at 0.2 and a MARKETER at 0.9.
	Temperature float64   `json:"temperature"`
	CreatedAt   time.Time `json:"created_at"`
	UpdatedAt   time.Time `json:"updated_at"`
}

// Project is a unit of work owned by a team.
type Project struct {
	ID           string `json:"id"`
	WorkspaceID  string `json:"workspace_id"`
	TeamID       string `json:"team_id"`
	Name         string `json:"name"`
	Description  string `json:"description,omitempty"`
	Category     string `json:"category,omitempty"`
	TargetMarket string `json:"target_market,omitempty"`
	Status       Status `json:"status"`
	// BudgetUSD and SpentUSD are informational only. The web app stored these
	// and never enforced them; here they can never gate anything either.
	BudgetUSD        float64   `json:"budget_usd"`
	SpentUSD         float64   `json:"spent_usd"`
	DeliveryReport   string    `json:"delivery_report_markdown,omitempty"`
	DeliveryReportAt time.Time `json:"delivery_report_generated_at,omitempty"`
	CreatedAt        time.Time `json:"created_at"`
	UpdatedAt        time.Time `json:"updated_at"`
}

// CodeFile is one file an agent produced.
type CodeFile struct {
	Path    string `json:"path"`
	Content string `json:"content"`
}

// Source is a web-search citation attached to a result.
type Source struct {
	Title string `json:"title"`
	URL   string `json:"url"`
}

// RepoInfo records where generated code was committed.
type RepoInfo struct {
	Commit string `json:"commit"`
	Path   string `json:"path"`
}

// TaskResult is the persisted outcome of one agent run.
type TaskResult struct {
	Output     string     `json:"output"`
	Files      []CodeFile `json:"files,omitempty"`
	Provider   string     `json:"provider"`
	Model      string     `json:"model"`
	TokensUsed int        `json:"tokens_used"`
	// TokensEstimated is true when the provider did not report usage and we
	// fell back to a length-based approximation.
	TokensEstimated bool `json:"tokens_estimated,omitempty"`
	// Truncated is true when the provider stopped at the token budget, so the
	// output is the beginning of an answer rather than the whole thing.
	Truncated bool      `json:"truncated,omitempty"`
	Sources   []Source  `json:"sources,omitempty"`
	Repo      *RepoInfo `json:"repo,omitempty"`
	// RepoUnchanged is true when auto-commit ran but git found nothing to
	// commit, because the model regenerated byte-identical files. Without it
	// the absence of Repo is indistinguishable from auto-commit being off.
	RepoUnchanged   bool    `json:"repo_unchanged,omitempty"`
	DurationSeconds float64 `json:"duration_seconds,omitempty"`
}

// Task is one unit of work assigned to an agent.
type Task struct {
	ID              string      `json:"id"`
	ProjectID       string      `json:"project_id"`
	AssignedAgentID string      `json:"assigned_agent_id"`
	ParentTaskID    string      `json:"parent_task_id,omitempty"`
	Title           string      `json:"title"`
	Description     string      `json:"description,omitempty"`
	Priority        string      `json:"priority,omitempty"`
	Language        string      `json:"language,omitempty"`
	Status          Status      `json:"status"`
	ErrorMessage    string      `json:"error_message,omitempty"`
	Result          *TaskResult `json:"result,omitempty"`
	CreatedAt       time.Time   `json:"created_at"`
	UpdatedAt       time.Time   `json:"updated_at"`
}

// Node is one agent invocation inside a workflow graph.
type Node struct {
	ID      string `json:"id"`
	AgentID string `json:"agent_id"`
	// MaxRetries and RetryBackoffSeconds are optional per-node overrides of
	// the executor defaults, matching the web app's graph_definition shape.
	MaxRetries          *int     `json:"max_retries,omitempty"`
	RetryBackoffSeconds *float64 `json:"retry_backoff_seconds,omitempty"`
}

// Edge is a directed link between two workflow nodes.
type Edge struct {
	From string `json:"from"`
	To   string `json:"to"`
}

// Graph is the workflow definition: nodes plus edges.
type Graph struct {
	Nodes []Node `json:"nodes"`
	Edges []Edge `json:"edges"`
}

// StartNodes returns nodes with no inbound edges, falling back to the first
// node. Ported from langgraph_executor.py:get_start_nodes.
func (g Graph) StartNodes() []string {
	if len(g.Nodes) == 0 {
		return nil
	}
	inbound := map[string]bool{}
	for _, e := range g.Edges {
		inbound[e.To] = true
	}
	var starts []string
	for _, n := range g.Nodes {
		if !inbound[n.ID] {
			starts = append(starts, n.ID)
		}
	}
	if len(starts) == 0 {
		return []string{g.Nodes[0].ID}
	}
	return starts
}

// NextNodes returns the direct successors of nodeID.
func (g Graph) NextNodes(nodeID string) []string {
	var out []string
	for _, e := range g.Edges {
		if e.From == nodeID {
			out = append(out, e.To)
		}
	}
	return out
}

// Node looks up a node definition by ID.
func (g Graph) Node(id string) (Node, bool) {
	for _, n := range g.Nodes {
		if n.ID == id {
			return n, true
		}
	}
	return Node{}, false
}

// Validate checks the graph is runnable: at least one node, every node has an
// agent, and every edge endpoint exists.
func (g Graph) Validate() error {
	if len(g.Nodes) == 0 {
		return fmt.Errorf("graph has no nodes")
	}
	ids := map[string]bool{}
	for _, n := range g.Nodes {
		if n.ID == "" {
			return fmt.Errorf("graph has a node with an empty id")
		}
		if ids[n.ID] {
			return fmt.Errorf("duplicate node id %q", n.ID)
		}
		if n.AgentID == "" {
			return fmt.Errorf("node %q has no agent assigned", n.ID)
		}
		ids[n.ID] = true
	}
	for _, e := range g.Edges {
		if !ids[e.From] {
			return fmt.Errorf("edge references unknown node %q", e.From)
		}
		if !ids[e.To] {
			return fmt.Errorf("edge references unknown node %q", e.To)
		}
	}
	return nil
}

// Workflow is a reusable multi-agent DAG bound to a team.
type Workflow struct {
	ID             string    `json:"id"`
	TeamID         string    `json:"team_id"`
	Name           string    `json:"name"`
	Description    string    `json:"description,omitempty"`
	Graph          Graph     `json:"graph_definition"`
	MaxRetries     int       `json:"max_retries"`
	BackoffSeconds float64   `json:"retry_backoff_seconds"`
	CreatedAt      time.Time `json:"created_at"`
	UpdatedAt      time.Time `json:"updated_at"`
}

// NodeResult is the outcome of a single workflow node.
type NodeResult struct {
	NodeID          string  `json:"node_id"`
	AgentID         string  `json:"agent_id"`
	AgentName       string  `json:"agent_name"`
	Status          Status  `json:"status"`
	Output          string  `json:"output,omitempty"`
	Error           string  `json:"error,omitempty"`
	TokensUsed      int     `json:"tokens_used,omitempty"`
	Attempts        int     `json:"attempts"`
	DurationSeconds float64 `json:"duration_seconds,omitempty"`
}

// WorkflowExecution is a run of a Workflow.
type WorkflowExecution struct {
	ID              string       `json:"id"`
	WorkflowID      string       `json:"workflow_id"`
	Status          Status       `json:"status"`
	InitialInput    string       `json:"initial_input"`
	NodeResults     []NodeResult `json:"node_results"`
	FinalResult     string       `json:"final_result,omitempty"`
	DurationSeconds float64      `json:"duration_seconds"`
	StartedAt       time.Time    `json:"started_at"`
	FinishedAt      time.Time    `json:"finished_at"`
}

// Credential is a stored provider API key. Unlike the web app, these are
// plaintext in a 0600 file: there is no server, so there is no multi-tenant
// key material to protect with a symmetric key that ships in the repo.
type Credential struct {
	Provider  string    `json:"provider"`
	APIKey    string    `json:"api_key"`
	Label     string    `json:"label,omitempty"`
	UpdatedAt time.Time `json:"updated_at"`
}

// NewID returns a short, URL-safe, collision-resistant identifier. The web app
// used UUIDs; short ids are far nicer to type at a CLI prompt.
func NewID() string {
	const alphabet = "0123456789abcdefghijklmnopqrstuvwxyz"
	b := make([]byte, 10)
	max := big.NewInt(int64(len(alphabet)))
	for i := range b {
		n, err := rand.Int(rand.Reader, max)
		if err != nil {
			// crypto/rand failing is unrecoverable for id generation; fall back
			// to a timestamp so we degrade rather than crash.
			return fmt.Sprintf("t%d", time.Now().UnixNano())
		}
		b[i] = alphabet[n.Int64()]
	}
	return string(b)
}
