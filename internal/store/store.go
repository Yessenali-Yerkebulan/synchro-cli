// Package store is the local persistence layer.
//
// The web app needed Postgres + Redis + RabbitMQ + MinIO. The CLI needs a JSON
// file. Everything lives under $SYNCHRO_HOME (default ~/.synchro) so a user can
// inspect, diff, back up or delete everything Synchro knows with ordinary tools.
package store

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"

	"github.com/synchro/synchro-cli/internal/model"
)

// ErrNotFound is returned when a lookup by id or name fails.
var ErrNotFound = errors.New("not found")

// Config is user settings, persisted to config.json.
type Config struct {
	// Provider and Model are the defaults applied to newly created agents.
	Provider string `json:"provider"`
	Model    string `json:"model"`

	// OllamaURL is the local inference endpoint. Defaults to the host's
	// localhost, since the CLI runs on the host rather than in a container.
	OllamaURL string `json:"ollama_url"`

	// Temperature and MaxTokens are request defaults; agents may override temp.
	Temperature float64 `json:"temperature"`
	MaxTokens   int     `json:"max_tokens"`

	// Search enables grounding for RESEARCHER agents.
	Search        bool   `json:"search"`
	SearchTimeout int    `json:"search_timeout_seconds"`
	Stream        bool   `json:"stream"`
	Color         string `json:"color"` // auto | always | never

	// AutoCommit is the default for new workspaces.
	AutoCommit bool `json:"auto_commit_agent_code"`

	// RequestTimeoutSeconds bounds a single non-streaming provider call.
	RequestTimeout int `json:"request_timeout_seconds"`

	// Remembered context so `synchro` with no args opens where you left off.
	ActiveWorkspaceID string `json:"active_workspace_id,omitempty"`
	ActiveTeamID      string `json:"active_team_id,omitempty"`
	ActiveAgentID     string `json:"active_agent_id,omitempty"`
	ActiveProjectID   string `json:"active_project_id,omitempty"`

	// PipelineMode is the chain `synchro pipeline` uses when none is given.
	PipelineMode string `json:"pipeline_mode,omitempty"`
}

// DefaultConfig returns the out-of-the-box settings: local Ollama, no key,
// streaming on, colour auto.
func DefaultConfig() Config {
	return Config{
		Provider:       "ollama",
		Model:          "qwen3",
		OllamaURL:      "http://127.0.0.1:11434",
		Temperature:    0.7,
		MaxTokens:      4096,
		Search:         true,
		SearchTimeout:  20,
		Stream:         true,
		Color:          "auto",
		RequestTimeout: 300,
	}
}

// State is the whole domain, persisted to state.json.
type State struct {
	Workspaces []model.Workspace         `json:"workspaces"`
	Teams      []model.Team              `json:"teams"`
	Agents     []model.Agent             `json:"agents"`
	Projects   []model.Project           `json:"projects"`
	Tasks      []model.Task              `json:"tasks"`
	Workflows  []model.Workflow          `json:"workflows"`
	Executions []model.WorkflowExecution `json:"executions"`
}

// Store owns the files and serializes all access.
type Store struct {
	mu    sync.Mutex
	dir   string
	cfg   Config
	state State
	creds map[string]model.Credential
}

// Open loads (or creates) the store at dir. An empty dir resolves to
// $SYNCHRO_HOME, then ~/.synchro.
func Open(dir string) (*Store, error) {
	if dir == "" {
		dir = os.Getenv("SYNCHRO_HOME")
	}
	if dir == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return nil, fmt.Errorf("cannot determine home directory: %w", err)
		}
		dir = filepath.Join(home, ".synchro")
	}
	abs, err := filepath.Abs(dir)
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(abs, 0o755); err != nil {
		return nil, fmt.Errorf("create %s: %w", abs, err)
	}
	s := &Store{dir: abs, creds: map[string]model.Credential{}}
	if err := s.load(); err != nil {
		return nil, err
	}
	return s, nil
}

func (s *Store) load() error {
	cfg := DefaultConfig()
	if err := readJSON(filepath.Join(s.dir, "config.json"), &cfg); err != nil && !errors.Is(err, ErrNotFound) {
		return fmt.Errorf("read config: %w", err)
	}
	s.cfg = cfg

	if err := readJSON(filepath.Join(s.dir, "state.json"), &s.state); err != nil && !errors.Is(err, ErrNotFound) {
		return fmt.Errorf("read state: %w", err)
	}

	// Credentials are a map on disk, decoded into a map here.
	var creds map[string]model.Credential
	if err := readJSON(filepath.Join(s.dir, "credentials.json"), &creds); err == nil {
		s.creds = creds
	}
	return nil
}

// Dir is the store root.
func (s *Store) Dir() string { return s.dir }

// Config returns a copy of the current config.
func (s *Store) Config() Config {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.cfg
}

// SetConfig applies fn to the config and persists it.
func (s *Store) SetConfig(fn func(*Config)) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	fn(&s.cfg)
	return s.writeLocked("config.json", &s.cfg)
}

// ReposDir is where generated code is committed, one git repo per project.
func (s *Store) ReposDir() string { return filepath.Join(s.dir, "repos") }

// Save persists the domain state.
func (s *Store) Save() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.writeLocked("state.json", &s.state)
}

func (s *Store) writeLocked(name string, v any) error {
	path := filepath.Join(s.dir, name)
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	// Write-then-rename so an interrupted write cannot truncate the store.
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

func readJSON(path string, v any) error {
	b, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return ErrNotFound
		}
		return err
	}
	if len(b) == 0 {
		return ErrNotFound
	}
	return json.Unmarshal(b, v)
}

// ---------------------------------------------------------------------------
// Workspaces
// ---------------------------------------------------------------------------

// CreateWorkspace inserts w, assigning an id if empty.
func (s *Store) CreateWorkspace(w *model.Workspace) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := nowUTC()
	if w.ID == "" {
		w.ID = model.NewID()
	}
	w.CreatedAt, w.UpdatedAt = now, now
	s.state.Workspaces = append(s.state.Workspaces, *w)
	return s.writeLocked("state.json", &s.state)
}

// UpdateWorkspace replaces the stored workspace with the same id.
func (s *Store) UpdateWorkspace(w *model.Workspace) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := range s.state.Workspaces {
		if s.state.Workspaces[i].ID == w.ID {
			w.CreatedAt = s.state.Workspaces[i].CreatedAt
			w.UpdatedAt = nowUTC()
			s.state.Workspaces[i] = *w
			return s.writeLocked("state.json", &s.state)
		}
	}
	return ErrNotFound
}

// Workspaces returns all workspaces, name-sorted.
func (s *Store) Workspaces() []model.Workspace {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := append([]model.Workspace(nil), s.state.Workspaces...)
	sort.Slice(out, func(i, j int) bool { return out[i].CreatedAt.Before(out[j].CreatedAt) })
	return out
}

// Workspace resolves by id or case-insensitive name.
func (s *Store) Workspace(ref string) (*model.Workspace, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.findWorkspace(ref)
}

func (s *Store) findWorkspace(ref string) (*model.Workspace, error) {
	for i := range s.state.Workspaces {
		if s.state.Workspaces[i].ID == ref {
			w := s.state.Workspaces[i]
			return &w, nil
		}
	}
	for i := range s.state.Workspaces {
		if strings.EqualFold(s.state.Workspaces[i].Name, ref) {
			w := s.state.Workspaces[i]
			return &w, nil
		}
	}
	return nil, ErrNotFound
}

// DeleteWorkspace cascades to every object it owns.
func (s *Store) DeleteWorkspace(ref string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	ws, err := s.findWorkspace(ref)
	if err != nil {
		return err
	}
	id := ws.ID

	keep := func(wsID string) bool { return wsID != id }
	agents := map[string]bool{}
	for _, t := range s.state.Teams {
		if t.WorkspaceID == id {
			for _, a := range s.state.Agents {
				if a.TeamID == t.ID {
					agents[a.ID] = true
				}
			}
		}
	}
	projects := map[string]bool{}
	for _, p := range s.state.Projects {
		if p.WorkspaceID == id {
			projects[p.ID] = true
		}
	}
	teams := map[string]bool{}
	for _, t := range s.state.Teams {
		if t.WorkspaceID == id {
			teams[t.ID] = true
		}
	}

	s.state.Workspaces = filter(s.state.Workspaces, func(w model.Workspace) bool { return w.ID != id })
	s.state.Teams = filter(s.state.Teams, func(t model.Team) bool { return keep(t.WorkspaceID) })
	s.state.Agents = filter(s.state.Agents, func(a model.Agent) bool { return !agents[a.ID] })
	s.state.Projects = filter(s.state.Projects, func(p model.Project) bool { return !projects[p.ID] })
	s.state.Tasks = filter(s.state.Tasks, func(t model.Task) bool { return !projects[t.ProjectID] })

	// Workflows and their executions hang off teams, which were just removed.
	wfIDs := map[string]bool{}
	for _, w := range s.state.Workflows {
		if teams[w.TeamID] {
			wfIDs[w.ID] = true
		}
	}
	s.state.Workflows = filter(s.state.Workflows, func(w model.Workflow) bool { return !teams[w.TeamID] })
	s.state.Executions = filter(s.state.Executions, func(e model.WorkflowExecution) bool { return !wfIDs[e.WorkflowID] })

	if s.cfg.ActiveWorkspaceID == id {
		s.cfg.ActiveWorkspaceID = ""
	}
	if err := s.writeLocked("state.json", &s.state); err != nil {
		return err
	}
	return s.writeLocked("config.json", &s.cfg)
}

// ---------------------------------------------------------------------------
// Teams
// ---------------------------------------------------------------------------

// CreateTeam inserts t.
func (s *Store) CreateTeam(t *model.Team) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := nowUTC()
	if t.ID == "" {
		t.ID = model.NewID()
	}
	t.CreatedAt, t.UpdatedAt = now, now
	s.state.Teams = append(s.state.Teams, *t)
	return s.writeLocked("state.json", &s.state)
}

// UpdateTeam replaces the stored team with the same id.
func (s *Store) UpdateTeam(t *model.Team) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := range s.state.Teams {
		if s.state.Teams[i].ID == t.ID {
			t.CreatedAt = s.state.Teams[i].CreatedAt
			t.UpdatedAt = nowUTC()
			s.state.Teams[i] = *t
			return s.writeLocked("state.json", &s.state)
		}
	}
	return ErrNotFound
}

// Teams lists teams, optionally filtered to a workspace.
func (s *Store) Teams(workspaceID string) []model.Team {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := []model.Team{}
	for _, t := range s.state.Teams {
		if workspaceID == "" || t.WorkspaceID == workspaceID {
			out = append(out, t)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].CreatedAt.Before(out[j].CreatedAt) })
	return out
}

// Team resolves by id or name within a workspace (name matching is scoped to
// the workspace so two workspaces can both have a team called "Core Team").
func (s *Store) Team(ref, workspaceID string) (*model.Team, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, t := range s.state.Teams {
		if t.ID == ref {
			c := t
			return &c, nil
		}
	}
	for _, t := range s.state.Teams {
		if workspaceID != "" && t.WorkspaceID != workspaceID {
			continue
		}
		if strings.EqualFold(t.Name, ref) {
			c := t
			return &c, nil
		}
	}
	return nil, ErrNotFound
}

// DeleteTeam cascades to its agents, projects, workflows and their tasks.
func (s *Store) DeleteTeam(ref, workspaceID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	t, err := s.findTeam(ref, workspaceID)
	if err != nil {
		return err
	}
	id := t.ID
	projects := map[string]bool{}
	for _, p := range s.state.Projects {
		if p.TeamID == id {
			projects[p.ID] = true
		}
	}
	wfIDs := map[string]bool{}
	for _, w := range s.state.Workflows {
		if w.TeamID == id {
			wfIDs[w.ID] = true
		}
	}
	s.state.Teams = filter(s.state.Teams, func(x model.Team) bool { return x.ID != id })
	s.state.Agents = filter(s.state.Agents, func(a model.Agent) bool { return a.TeamID != id })
	s.state.Projects = filter(s.state.Projects, func(p model.Project) bool { return !projects[p.ID] })
	s.state.Tasks = filter(s.state.Tasks, func(x model.Task) bool { return !projects[x.ProjectID] })
	s.state.Workflows = filter(s.state.Workflows, func(w model.Workflow) bool { return w.TeamID != id })
	s.state.Executions = filter(s.state.Executions, func(e model.WorkflowExecution) bool { return !wfIDs[e.WorkflowID] })

	if s.cfg.ActiveTeamID == id {
		s.cfg.ActiveTeamID = ""
	}
	if err := s.writeLocked("state.json", &s.state); err != nil {
		return err
	}
	return s.writeLocked("config.json", &s.cfg)
}

func (s *Store) findTeam(ref, workspaceID string) (*model.Team, error) {
	for _, t := range s.state.Teams {
		if t.ID == ref {
			c := t
			return &c, nil
		}
	}
	for _, t := range s.state.Teams {
		if workspaceID != "" && t.WorkspaceID != workspaceID {
			continue
		}
		if strings.EqualFold(t.Name, ref) {
			c := t
			return &c, nil
		}
	}
	return nil, ErrNotFound
}

// ---------------------------------------------------------------------------
// Agents
// ---------------------------------------------------------------------------

// CreateAgent inserts a.
func (s *Store) CreateAgent(a *model.Agent) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := nowUTC()
	if a.ID == "" {
		a.ID = model.NewID()
	}
	if a.Status == "" {
		a.Status = model.StatusActive
	}
	a.CreatedAt, a.UpdatedAt = now, now
	s.state.Agents = append(s.state.Agents, *a)
	return s.writeLocked("state.json", &s.state)
}

// UpdateAgent replaces the stored agent with the same id.
func (s *Store) UpdateAgent(a *model.Agent) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := range s.state.Agents {
		if s.state.Agents[i].ID == a.ID {
			a.CreatedAt = s.state.Agents[i].CreatedAt
			a.UpdatedAt = nowUTC()
			s.state.Agents[i] = *a
			return s.writeLocked("state.json", &s.state)
		}
	}
	return ErrNotFound
}

// Agents lists agents of a team, role-sorted.
func (s *Store) Agents(teamID string) []model.Agent {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := []model.Agent{}
	for _, a := range s.state.Agents {
		if teamID == "" || a.TeamID == teamID {
			out = append(out, a)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// Agent resolves by id, name, or 1-based index within a team.
func (s *Store) Agent(ref, teamID string) (*model.Agent, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.findAgent(ref, teamID)
}

func (s *Store) findAgent(ref, teamID string) (*model.Agent, error) {
	scope := []model.Agent{}
	for _, a := range s.state.Agents {
		if teamID == "" || a.TeamID == teamID {
			scope = append(scope, a)
		}
	}
	for _, a := range scope {
		if a.ID == ref {
			c := a
			return &c, nil
		}
	}
	if n, err := atoi(ref); err == nil && n >= 1 && n <= len(scope) {
		sorted := append([]model.Agent(nil), scope...)
		sort.Slice(sorted, func(i, j int) bool { return sorted[i].Name < sorted[j].Name })
		c := sorted[n-1]
		return &c, nil
	}
	for _, a := range scope {
		if strings.EqualFold(a.Name, ref) {
			c := a
			return &c, nil
		}
	}
	return nil, ErrNotFound
}

// DeleteAgent removes the agent; its tasks stay but become unassigned.
func (s *Store) DeleteAgent(ref, teamID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	a, err := s.findAgent(ref, teamID)
	if err != nil {
		return err
	}
	s.state.Agents = filter(s.state.Agents, func(x model.Agent) bool { return x.ID != a.ID })
	for i := range s.state.Tasks {
		if s.state.Tasks[i].AssignedAgentID == a.ID {
			s.state.Tasks[i].AssignedAgentID = ""
			s.state.Tasks[i].UpdatedAt = nowUTC()
		}
	}
	if s.cfg.ActiveAgentID == a.ID {
		s.cfg.ActiveAgentID = ""
	}
	if err := s.writeLocked("state.json", &s.state); err != nil {
		return err
	}
	return s.writeLocked("config.json", &s.cfg)
}

// ---------------------------------------------------------------------------
// Projects
// ---------------------------------------------------------------------------

// CreateProject inserts p.
func (s *Store) CreateProject(p *model.Project) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := nowUTC()
	if p.ID == "" {
		p.ID = model.NewID()
	}
	if p.Status == "" {
		p.Status = model.StatusPending
	}
	p.CreatedAt, p.UpdatedAt = now, now
	s.state.Projects = append(s.state.Projects, *p)
	return s.writeLocked("state.json", &s.state)
}

// UpdateProject replaces the stored project with the same id.
func (s *Store) UpdateProject(p *model.Project) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := range s.state.Projects {
		if s.state.Projects[i].ID == p.ID {
			p.CreatedAt = s.state.Projects[i].CreatedAt
			p.UpdatedAt = nowUTC()
			s.state.Projects[i] = *p
			return s.writeLocked("state.json", &s.state)
		}
	}
	return ErrNotFound
}

// Projects lists projects, optionally filtered to a workspace.
func (s *Store) Projects(workspaceID string) []model.Project {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := []model.Project{}
	for _, p := range s.state.Projects {
		if workspaceID == "" || p.WorkspaceID == workspaceID {
			out = append(out, p)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].CreatedAt.Before(out[j].CreatedAt) })
	return out
}

// Project resolves by id or name.
func (s *Store) Project(ref, workspaceID string) (*model.Project, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.findProject(ref, workspaceID)
}

func (s *Store) findProject(ref, workspaceID string) (*model.Project, error) {
	for _, p := range s.state.Projects {
		if p.ID == ref {
			c := p
			return &c, nil
		}
	}
	for _, p := range s.state.Projects {
		if workspaceID != "" && p.WorkspaceID != workspaceID {
			continue
		}
		if strings.EqualFold(p.Name, ref) {
			c := p
			return &c, nil
		}
	}
	return nil, ErrNotFound
}

// DeleteProject removes the project and its tasks.
func (s *Store) DeleteProject(ref, workspaceID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	p, err := s.findProject(ref, workspaceID)
	if err != nil {
		return err
	}
	s.state.Projects = filter(s.state.Projects, func(x model.Project) bool { return x.ID != p.ID })
	s.state.Tasks = filter(s.state.Tasks, func(t model.Task) bool { return t.ProjectID != p.ID })
	if s.cfg.ActiveProjectID == p.ID {
		s.cfg.ActiveProjectID = ""
	}
	if err := s.writeLocked("state.json", &s.state); err != nil {
		return err
	}
	return s.writeLocked("config.json", &s.cfg)
}

// ---------------------------------------------------------------------------
// Tasks
// ---------------------------------------------------------------------------

// CreateTask inserts t.
func (s *Store) CreateTask(t *model.Task) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := nowUTC()
	if t.ID == "" {
		t.ID = model.NewID()
	}
	if t.Status == "" {
		t.Status = model.StatusPending
	}
	t.CreatedAt, t.UpdatedAt = now, now
	s.state.Tasks = append(s.state.Tasks, *t)
	return s.writeLocked("state.json", &s.state)
}

// UpdateTask replaces the stored task with the same id.
func (s *Store) UpdateTask(t *model.Task) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := range s.state.Tasks {
		if s.state.Tasks[i].ID == t.ID {
			t.CreatedAt = s.state.Tasks[i].CreatedAt
			t.UpdatedAt = nowUTC()
			s.state.Tasks[i] = *t
			return s.writeLocked("state.json", &s.state)
		}
	}
	return ErrNotFound
}

// Tasks lists tasks, optionally filtered to a project.
func (s *Store) Tasks(projectID string) []model.Task {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := []model.Task{}
	for _, t := range s.state.Tasks {
		if projectID == "" || t.ProjectID == projectID {
			out = append(out, t)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].CreatedAt.Before(out[j].CreatedAt) })
	return out
}

// Task resolves by id or title.
func (s *Store) Task(ref string) (*model.Task, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, t := range s.state.Tasks {
		if t.ID == ref {
			c := t
			return &c, nil
		}
	}
	for _, t := range s.state.Tasks {
		if strings.EqualFold(t.Title, ref) {
			c := t
			return &c, nil
		}
	}
	return nil, ErrNotFound
}

// DeleteTask removes a task.
func (s *Store) DeleteTask(ref string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	t, err := s.findTask(ref)
	if err != nil {
		return err
	}
	s.state.Tasks = filter(s.state.Tasks, func(x model.Task) bool { return x.ID != t.ID })
	return s.writeLocked("state.json", &s.state)
}

func (s *Store) findTask(ref string) (*model.Task, error) {
	for _, t := range s.state.Tasks {
		if t.ID == ref {
			c := t
			return &c, nil
		}
	}
	for _, t := range s.state.Tasks {
		if strings.EqualFold(t.Title, ref) {
			c := t
			return &c, nil
		}
	}
	return nil, ErrNotFound
}

// AddSpent records a cost delta on a project. Purely informational.
func (s *Store) AddSpent(projectID string, usd float64) error {
	if projectID == "" || usd == 0 {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := range s.state.Projects {
		if s.state.Projects[i].ID == projectID {
			s.state.Projects[i].SpentUSD += usd
			s.state.Projects[i].UpdatedAt = nowUTC()
			return s.writeLocked("state.json", &s.state)
		}
	}
	return ErrNotFound
}

// ---------------------------------------------------------------------------
// Workflows
// ---------------------------------------------------------------------------

// CreateWorkflow inserts w.
func (s *Store) CreateWorkflow(w *model.Workflow) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := nowUTC()
	if w.ID == "" {
		w.ID = model.NewID()
	}
	if w.MaxRetries <= 0 {
		w.MaxRetries = 2
	}
	if w.BackoffSeconds <= 0 {
		w.BackoffSeconds = 2
	}
	w.CreatedAt, w.UpdatedAt = now, now
	s.state.Workflows = append(s.state.Workflows, *w)
	return s.writeLocked("state.json", &s.state)
}

// UpdateWorkflow replaces the stored workflow with the same id.
func (s *Store) UpdateWorkflow(w *model.Workflow) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := range s.state.Workflows {
		if s.state.Workflows[i].ID == w.ID {
			w.CreatedAt = s.state.Workflows[i].CreatedAt
			w.UpdatedAt = nowUTC()
			s.state.Workflows[i] = *w
			return s.writeLocked("state.json", &s.state)
		}
	}
	return ErrNotFound
}

// Workflows lists workflows, optionally filtered to a team.
func (s *Store) Workflows(teamID string) []model.Workflow {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := []model.Workflow{}
	for _, w := range s.state.Workflows {
		if teamID == "" || w.TeamID == teamID {
			out = append(out, w)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].CreatedAt.Before(out[j].CreatedAt) })
	return out
}

// Workflow resolves by id or name within a team.
func (s *Store) Workflow(ref, teamID string) (*model.Workflow, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, w := range s.state.Workflows {
		if w.ID == ref {
			c := w
			return &c, nil
		}
	}
	for _, w := range s.state.Workflows {
		if teamID != "" && w.TeamID != teamID {
			continue
		}
		if strings.EqualFold(w.Name, ref) {
			c := w
			return &c, nil
		}
	}
	return nil, ErrNotFound
}

// DeleteWorkflow removes the workflow and its execution history.
func (s *Store) DeleteWorkflow(ref, teamID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	w, err := s.findWorkflow(ref, teamID)
	if err != nil {
		return err
	}
	s.state.Workflows = filter(s.state.Workflows, func(x model.Workflow) bool { return x.ID != w.ID })
	s.state.Executions = filter(s.state.Executions, func(e model.WorkflowExecution) bool { return e.WorkflowID != w.ID })
	return s.writeLocked("state.json", &s.state)
}

func (s *Store) findWorkflow(ref, teamID string) (*model.Workflow, error) {
	for _, w := range s.state.Workflows {
		if w.ID == ref {
			c := w
			return &c, nil
		}
	}
	for _, w := range s.state.Workflows {
		if teamID != "" && w.TeamID != teamID {
			continue
		}
		if strings.EqualFold(w.Name, ref) {
			c := w
			return &c, nil
		}
	}
	return nil, ErrNotFound
}

// SaveExecution persists a run and returns the stored copy.
func (s *Store) SaveExecution(e *model.WorkflowExecution) (*model.WorkflowExecution, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if e.ID == "" {
		e.ID = model.NewID()
	}
	replaced := false
	for i := range s.state.Executions {
		if s.state.Executions[i].ID == e.ID {
			s.state.Executions[i] = *e
			replaced = true
			break
		}
	}
	if !replaced {
		s.state.Executions = append(s.state.Executions, *e)
	}
	// Keep history bounded; a local tool should not grow without limit.
	if len(s.state.Executions) > 200 {
		s.state.Executions = s.state.Executions[len(s.state.Executions)-200:]
	}
	if err := s.writeLocked("state.json", &s.state); err != nil {
		return nil, err
	}
	c := *e
	return &c, nil
}

// Executions lists runs of a workflow, newest first.
func (s *Store) Executions(workflowID string) []model.WorkflowExecution {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := []model.WorkflowExecution{}
	for _, e := range s.state.Executions {
		if workflowID == "" || e.WorkflowID == workflowID {
			out = append(out, e)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].StartedAt.After(out[j].StartedAt) })
	return out
}

// ---------------------------------------------------------------------------
// Credentials
// ---------------------------------------------------------------------------

// SetCredential stores or replaces the key for a provider.
func (s *Store) SetCredential(provider, apiKey, label string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.creds[strings.ToLower(provider)] = model.Credential{
		Provider:  strings.ToLower(provider),
		APIKey:    apiKey,
		Label:     label,
		UpdatedAt: nowUTC(),
	}
	return s.writeLocked("credentials.json", &s.creds)
}

// Credential returns the stored key for a provider, if any.
func (s *Store) Credential(provider string) (model.Credential, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	c, ok := s.creds[strings.ToLower(provider)]
	return c, ok
}

// DeleteCredential removes a stored key.
func (s *Store) DeleteCredential(provider string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.creds, strings.ToLower(provider))
	return s.writeLocked("credentials.json", &s.creds)
}

// ProvidersWithKeys lists providers that have a stored key.
func (s *Store) ProvidersWithKeys() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]string, 0, len(s.creds))
	for p := range s.creds {
		out = append(out, p)
	}
	sort.Strings(out)
	return out
}

// ---------------------------------------------------------------------------
// helpers
// ---------------------------------------------------------------------------

func filter[T any](in []T, keep func(T) bool) []T {
	out := in[:0:0]
	for _, v := range in {
		if keep(v) {
			out = append(out, v)
		}
	}
	return out
}

func atoi(s string) (int, error) {
	if s == "" {
		return 0, ErrNotFound
	}
	n := 0
	for _, c := range s {
		if c < '0' || c > '9' {
			return 0, ErrNotFound
		}
		n = n*10 + int(c-'0')
	}
	return n, nil
}
