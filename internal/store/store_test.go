package store

import (
	"path/filepath"
	"testing"

	"github.com/synchro/synchro-cli/internal/model"
)

func newTestStore(t *testing.T) *Store {
	t.Helper()
	s, err := Open(t.TempDir())
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	return s
}

func TestDefaultsOnFreshStore(t *testing.T) {
	s := newTestStore(t)
	if got := s.Config().Provider; got != "ollama" {
		t.Errorf("default provider = %q, want ollama", got)
	}
	if len(s.Workspaces()) != 0 {
		t.Error("a fresh store should have no workspaces")
	}
	if s.Config().OllamaURL == "" {
		t.Error("OllamaURL should have a default")
	}
}

func TestWorkspaceCRUD(t *testing.T) {
	s := newTestStore(t)
	ws := &model.Workspace{Name: "default"}
	if err := s.CreateWorkspace(ws); err != nil {
		t.Fatalf("CreateWorkspace: %v", err)
	}
	if ws.ID == "" {
		t.Fatal("CreateWorkspace did not assign an id")
	}

	// Resolve by name, then by id.
	byName, err := s.Workspace("default")
	if err != nil || byName.ID != ws.ID {
		t.Fatalf("Workspace(name) = %v/%v", byName, err)
	}
	byID, err := s.Workspace(ws.ID)
	if err != nil || byID.Name != "default" {
		t.Fatalf("Workspace(id) = %v/%v", byID, err)
	}
	if _, err := s.Workspace("missing"); err == nil {
		t.Error("expected ErrNotFound for an unknown workspace")
	}

	ws.Name = "renamed"
	if err := s.UpdateWorkspace(ws); err != nil {
		t.Fatalf("UpdateWorkspace: %v", err)
	}
	if got, _ := s.Workspace(ws.ID); got.Name != "renamed" {
		t.Errorf("name = %q, want renamed", got.Name)
	}

	if err := s.DeleteWorkspace(ws.ID); err != nil {
		t.Fatalf("DeleteWorkspace: %v", err)
	}
	if len(s.Workspaces()) != 0 {
		t.Error("workspace should be gone")
	}
}

func TestAgentLookupByIndex(t *testing.T) {
	s := newTestStore(t)
	ws := &model.Workspace{Name: "w"}
	must(t, s.CreateWorkspace(ws))
	team := &model.Team{WorkspaceID: ws.ID, Name: "t"}
	must(t, s.CreateTeam(team))

	for _, name := range []string{"Alpha", "Bravo", "Charlie"} {
		must(t, s.CreateAgent(&model.Agent{TeamID: team.ID, Name: name, Role: model.RoleDeveloper}))
	}

	// Agents come back name-sorted, so index 1 is Alpha.
	list := s.Agents(team.ID)
	if len(list) != 3 {
		t.Fatalf("got %d agents, want 3", len(list))
	}
	first, err := s.Agent("1", team.ID)
	if err != nil {
		t.Fatalf("Agent(1): %v", err)
	}
	if first.Name != "Alpha" {
		t.Errorf("Agent(1) = %q, want Alpha", first.Name)
	}
	if _, err := s.Agent("9", team.ID); err == nil {
		t.Error("expected an error for an out-of-range index")
	}
}

func TestDeleteWorkspaceCascades(t *testing.T) {
	s := newTestStore(t)
	ws := &model.Workspace{Name: "w"}
	must(t, s.CreateWorkspace(ws))
	team := &model.Team{WorkspaceID: ws.ID, Name: "t"}
	must(t, s.CreateTeam(team))
	ag := &model.Agent{TeamID: team.ID, Name: "a", Role: model.RoleDeveloper}
	must(t, s.CreateAgent(ag))
	proj := &model.Project{WorkspaceID: ws.ID, TeamID: team.ID, Name: "p"}
	must(t, s.CreateProject(proj))
	must(t, s.CreateTask(&model.Task{ProjectID: proj.ID, AssignedAgentID: ag.ID, Title: "t"}))

	must(t, s.DeleteWorkspace(ws.ID))

	if len(s.Teams(ws.ID)) != 0 {
		t.Error("teams should be gone with the workspace")
	}
	if len(s.Agents(team.ID)) != 0 {
		t.Error("agents should be gone with the team")
	}
	if len(s.Projects(ws.ID)) != 0 {
		t.Error("projects should be gone with the workspace")
	}
	if len(s.Tasks(proj.ID)) != 0 {
		t.Error("tasks should be gone with the project")
	}
}

func TestSetActiveAndClear(t *testing.T) {
	s := newTestStore(t)
	ws := &model.Workspace{Name: "w"}
	must(t, s.CreateWorkspace(ws))
	team := &model.Team{WorkspaceID: ws.ID, Name: "t"}
	must(t, s.CreateTeam(team))
	ag := &model.Agent{TeamID: team.ID, Name: "a", Role: model.RoleDeveloper}
	must(t, s.CreateAgent(ag))

	must(t, s.SetConfig(func(c *Config) {
		c.ActiveWorkspaceID = ws.ID
		c.ActiveTeamID = team.ID
		c.ActiveAgentID = ag.ID
	}))
	cfg := s.Config()
	if cfg.ActiveWorkspaceID != ws.ID || cfg.ActiveAgentID != ag.ID {
		t.Errorf("active ids not stored: %+v", cfg)
	}

	// Clearing a workspace must drop the dependent selections too, otherwise
	// the shell would resolve a team from a workspace that is gone.
	must(t, s.SetConfig(func(c *Config) {
		c.ActiveWorkspaceID = ""
		c.ActiveTeamID = ""
	}))
	cfg = s.Config()
	if cfg.ActiveTeamID != "" {
		t.Errorf("team should have been cleared, got %q", cfg.ActiveTeamID)
	}
}

func TestCredentialsAreNotWorldReadable(t *testing.T) {
	dir := t.TempDir()
	s, err := Open(dir)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	must(t, s.SetCredential("gemini", "secret-key", "test"))
	if _, ok := s.Credential("gemini"); !ok {
		t.Error("credential should be readable")
	}
	if got := s.ProvidersWithKeys(); len(got) != 1 || got[0] != "gemini" {
		t.Errorf("ProvidersWithKeys = %v", got)
	}
	must(t, s.DeleteCredential("gemini"))
	if _, ok := s.Credential("gemini"); ok {
		t.Error("credential should be gone")
	}

	// It has to survive a reopen, since that is the normal path.
	again, err := Open(dir)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	if len(again.ProvidersWithKeys()) != 0 {
		t.Error("the key should still be deleted after a reopen")
	}
}

func TestReposDirIsUnderStateDir(t *testing.T) {
	dir := t.TempDir()
	s, err := Open(dir)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	want := filepath.Join(dir, "repos")
	if got := s.ReposDir(); got != want {
		t.Errorf("ReposDir = %q, want %q", got, want)
	}
}

func TestAddSpentIgnoresUnknownProject(t *testing.T) {
	s := newTestStore(t)
	// An empty project id and an unknown one must both be no-ops, not errors:
	// bookkeeping should never fail a run.
	if err := s.AddSpent("", 1.5); err != nil {
		t.Errorf("AddSpent(\"\") = %v, want nil", err)
	}
	if err := s.AddSpent("nope", 1.5); err == nil {
		t.Error("AddSpent on an unknown project should report not-found")
	}
}

func must(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}
