package agents

import (
	"testing"

	"github.com/synchro/synchro/internal/model"
)

func TestPipelineModeByID(t *testing.T) {
	// Every mode must be reachable by its own id.
	for _, want := range PipelineModes {
		got, ok := PipelineModeByID(want.ID)
		if !ok {
			t.Errorf("PipelineModeByID(%q) not found", want.ID)
			continue
		}
		if got.ID != want.ID {
			t.Errorf("PipelineModeByID(%q).ID = %q", want.ID, got.ID)
		}
	}

	// Case and surrounding space are normalised; a real alias resolves too.
	if got, ok := PipelineModeByID("  BUILD "); !ok || got.ID != "build" {
		t.Errorf("PipelineModeByID(%q) = %q/%v, want build", "  BUILD ", got.ID, ok)
	}
	if got, ok := PipelineModeByID("not-a-mode"); ok {
		t.Errorf("PipelineModeByID(%q) = %q, want not found", "not-a-mode", got.ID)
	}
}

func TestBuildStagesPicksAgentPerRole(t *testing.T) {
	team := []model.Agent{
		{ID: "d1", Name: "Dev", Role: model.RoleDeveloper, Status: model.StatusActive},
		{ID: "p1", Name: "PM", Role: model.RoleProductManager, Status: model.StatusActive},
		{ID: "q1", Name: "QA", Role: model.RoleQA, Status: model.StatusActive},
	}
	mode := PipelineMode{ID: "test", Roles: []model.Role{model.RoleDeveloper, model.RoleQA}}
	stages := BuildStages(team, mode, "goal")

	if len(stages) != 2 {
		t.Fatalf("got %d stages, want 2", len(stages))
	}
	if stages[0].Agent == nil || stages[0].Agent.ID != "d1" {
		t.Errorf("stage 1 agent = %v, want d1", stages[0].Agent)
	}
	if stages[1].Agent == nil || stages[1].Agent.ID != "q1" {
		t.Errorf("stage 2 agent = %v, want q1", stages[1].Agent)
	}
	if stages[0].Input != "goal" {
		t.Errorf("stage input = %q, want the goal", stages[0].Input)
	}
}

func TestBuildStagesReportsMissingRole(t *testing.T) {
	// A role the team cannot fill must be visible, not dropped.
	team := []model.Agent{
		{ID: "d1", Name: "Dev", Role: model.RoleDeveloper, Status: model.StatusActive},
	}
	mode := PipelineMode{ID: "test", Roles: []model.Role{model.RoleDeveloper, model.RoleQA}}
	stages := BuildStages(team, mode, "goal")

	if len(stages) != 2 {
		t.Fatalf("got %d stages, want 2 (the missing role must still appear)", len(stages))
	}
	if stages[1].Agent != nil {
		t.Errorf("stage 2 should have no agent, got %v", stages[1].Agent)
	}
	if stages[1].Note == "" {
		t.Error("stage 2 should carry a note explaining the missing role")
	}
}

func TestBuildStagesReusesAgentWhenRoleRepeats(t *testing.T) {
	// One developer asked for twice has to be used twice, rather than leaving
	// the second stage empty.
	team := []model.Agent{
		{ID: "d1", Name: "Solo Dev", Role: model.RoleDeveloper, Status: model.StatusActive},
	}
	mode := PipelineMode{ID: "fullstack", Roles: []model.Role{model.RoleDeveloper, model.RoleDeveloper}}
	stages := BuildStages(team, mode, "goal")

	if len(stages) != 2 {
		t.Fatalf("got %d stages, want 2", len(stages))
	}
	for i, s := range stages {
		if s.Agent == nil || s.Agent.ID != "d1" {
			t.Errorf("stage %d agent = %v, want d1", i+1, s.Agent)
		}
	}
	if stages[0].Label == stages[1].Label {
		t.Error("a repeated role should be labelled differently per stage")
	}
}

func TestBuildStagesSkipsInactiveAgents(t *testing.T) {
	team := []model.Agent{
		{ID: "d1", Name: "Off", Role: model.RoleDeveloper, Status: model.StatusInactive},
	}
	mode := PipelineMode{ID: "test", Roles: []model.Role{model.RoleDeveloper}}
	stages := BuildStages(team, mode, "goal")

	// With no active agent, the stage falls back to the inactive one rather
	// than reporting "no agent", because the role does exist.
	if len(stages) != 1 {
		t.Fatalf("got %d stages, want 1", len(stages))
	}
	if stages[0].Agent == nil || stages[0].Agent.ID != "d1" {
		t.Errorf("agent = %v, want the inactive d1 as a fallback", stages[0].Agent)
	}
}
