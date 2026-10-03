package cli

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/synchro/synchro-cli/internal/model"
	"github.com/synchro/synchro-cli/internal/store"
	"github.com/synchro/synchro-cli/internal/ui"
)

// runIn executes the real command tree with the given state directory and
// returns everything it printed.
func runIn(t *testing.T, home string, args ...string) (string, error) {
	t.Helper()
	f, err := os.CreateTemp(t.TempDir(), "out")
	if err != nil {
		t.Fatalf("temp file: %v", err)
	}
	old := os.Stdout
	os.Stdout = f
	defer func() { os.Stdout = old }()

	st := &rootState{opt: Options{Home: home, NoColor: true}}
	root := newRootCmd(st)
	root.SetArgs(append(args, "--no-color"))
	cmdErr := root.Execute()

	if err := f.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	b, err := os.ReadFile(f.Name())
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	return string(b), cmdErr
}

// run is runIn against a throwaway state directory.
func run(t *testing.T, args ...string) (string, error) {
	t.Helper()
	home := t.TempDir()
	return runIn(t, home, append(args, "--home", home)...)
}

func newTestApp(t *testing.T, home string, buf *bytes.Buffer) *App {
	t.Helper()
	p := ui.NewWriter(buf, buf, false)
	p.Width = 100
	a, err := NewAppWithPrinter(home, p)
	if err != nil {
		t.Fatalf("NewAppWithPrinter: %v", err)
	}
	return a
}

// --refresh used to be dropped on the floor, so the command always showed the
// curated snapshot and still told the user to try the flag that was already
// right there.
func TestModelsRefreshFlagReachesTheTable(t *testing.T) {
	plain, err := run(t, "models", "gemini")
	if err != nil {
		t.Fatalf("models: %v", err)
	}
	if !strings.Contains(plain, "curated list") {
		t.Fatalf("expected the curated snapshot without --refresh, got:\n%s", plain)
	}

	refreshed, err := run(t, "models", "gemini", "--refresh")
	if err != nil {
		t.Fatalf("models --refresh: %v", err)
	}
	if strings.Contains(refreshed, "curated list") {
		t.Fatalf("--refresh was ignored, still showing the curated snapshot:\n%s", refreshed)
	}
	if !strings.Contains(refreshed, "gemini-2.5-flash") {
		t.Fatalf("--refresh dropped the provider's models:\n%s", refreshed)
	}
}

// --all and --refresh together must not swallow the provider name.
func TestModelsAllAndRefreshTogether(t *testing.T) {
	out, err := run(t, "models", "openai", "--all", "--refresh")
	if err != nil {
		t.Fatalf("models: %v", err)
	}
	if strings.Contains(out, "unknown provider") {
		t.Fatalf("provider name was mangled by the flags:\n%s", out)
	}
	if !strings.Contains(out, "OpenAI") {
		t.Fatalf("expected the OpenAI section:\n%s", out)
	}
}

func TestResolveRef(t *testing.T) {
	list := []model.Workspace{{Name: "alpha"}, {Name: "beta"}, {Name: "gamma"}}

	if w, ok := resolveRef(list, "2"); !ok || w.Name != "beta" {
		t.Errorf(`resolveRef(list, "2") = %q, %v; want beta, true`, w.Name, ok)
	}
	if w, ok := resolveRef(list, "1"); !ok || w.Name != "alpha" {
		t.Errorf(`resolveRef(list, "1") = %q, %v; want alpha, true`, w.Name, ok)
	}
	if w, ok := resolveRef(list, "gamma"); !ok || w.Name != "gamma" {
		t.Errorf(`resolveRef(list, "gamma") = %q, %v; want gamma, true`, w.Name, ok)
	}
	for _, ref := range []string{"0", "4", "-1", "", "nope"} {
		if _, ok := resolveRef(list, ref); ok {
			t.Errorf(`resolveRef(list, %q) matched, want no match`, ref)
		}
	}
	if _, ok := resolveRef([]model.Workspace(nil), "1"); ok {
		t.Error("resolveRef matched in an empty list")
	}
}

// A workflow whose graph does not validate has to come back as an error, not as
// a panic from walking a run that was never created.
func TestRunWorkflowOnInvalidGraphReturnsError(t *testing.T) {
	var buf bytes.Buffer
	a := newTestApp(t, t.TempDir(), &buf)

	wf := &model.Workflow{
		Name:  "broken",
		Graph: model.Graph{Nodes: []model.Node{{ID: "n1", AgentID: "a1"}}, Edges: []model.Edge{{From: "n1", To: "nope"}}},
	}
	if err := a.runWorkflow(wf, "go"); err == nil {
		t.Fatal("expected an error for an invalid graph")
	}
}

func TestWSEditChangesAutoCommit(t *testing.T) {
	home := t.TempDir()
	if _, err := runIn(t, home, "ws", "new", "shop", "--home", home); err != nil {
		t.Fatalf("ws new: %v", err)
	}

	wsCmd := newWSCmd(&rootState{opt: Options{Home: home}})
	edit, _, err := wsCmd.Find([]string{"edit", "shop"})
	if err != nil {
		t.Fatalf("ws edit is not registered: %v", err)
	}
	// Turning it on and back off has to work, which is why the flag is read
	// with Changed rather than by its value.
	for _, want := range []bool{true, false} {
		if err := edit.Flags().Set("auto-commit", boolText(want)); err != nil {
			t.Fatalf("set flag: %v", err)
		}
		if err := edit.RunE(edit, []string{"shop"}); err != nil {
			t.Fatalf("ws edit --auto-commit=%v: %v", want, err)
		}
		s, err := store.Open(home)
		if err != nil {
			t.Fatalf("reopen store: %v", err)
		}
		w, err := s.Workspace("shop")
		if err != nil {
			t.Fatalf("Workspace: %v", err)
		}
		if w.AutoCommitAgentCode != want {
			t.Errorf("auto-commit = %v, want %v", w.AutoCommitAgentCode, want)
		}
	}

	// No flags at all is a mistake worth naming, not a silent no-op.
	freshEdit, _, err := newWSCmd(&rootState{opt: Options{Home: home}}).Find([]string{"edit", "shop"})
	if err != nil {
		t.Fatalf("ws edit: %v", err)
	}
	if err := freshEdit.RunE(freshEdit, []string{"shop"}); err == nil {
		t.Error("expected ws edit with no flags to complain")
	}
}

func boolText(b bool) string {
	if b {
		return "true"
	}
	return "false"
}

// The commit help points at `ws edit`, so that command has to exist.
func TestCommitHelpPointsAtARealCommand(t *testing.T) {
	help, err := run(t, "commit", "--help")
	if err != nil {
		t.Fatalf("commit --help: %v", err)
	}
	if !strings.Contains(help, "ws edit") {
		t.Fatalf("commit help no longer mentions ws edit:\n%s", help)
	}
	wsHelp, err := run(t, "ws", "--help")
	if err != nil {
		t.Fatalf("ws --help: %v", err)
	}
	if !strings.Contains(wsHelp, "edit") {
		t.Errorf("commit help points at `ws edit` but ws has no edit command:\n%s", wsHelp)
	}
}

// Non-latin titles must not be cut mid-rune on their way to the terminal.
func TestProjectShowKeepsUTF8TitlesIntact(t *testing.T) {
	home := t.TempDir()
	var buf bytes.Buffer
	a := newTestApp(t, home, &buf)

	ws := &model.Workspace{Name: "w"}
	if err := a.Store.CreateWorkspace(ws); err != nil {
		t.Fatalf("CreateWorkspace: %v", err)
	}
	p := &model.Project{WorkspaceID: ws.ID, Name: "demo"}
	if err := a.Store.CreateProject(p); err != nil {
		t.Fatalf("CreateProject: %v", err)
	}
	title := strings.Repeat("я", 60) // 60 runes, 120 bytes: cut lands mid-rune at 40
	if err := a.Store.CreateTask(&model.Task{ProjectID: p.ID, Title: title}); err != nil {
		t.Fatalf("CreateTask: %v", err)
	}

	out, err := runIn(t, home, "project", "show", "--home", home)
	if err != nil {
		t.Fatalf("project show: %v", err)
	}
	if strings.Contains(out, "\ufffd") {
		t.Errorf("title was cut mid-rune:\n%q", out)
	}
	if !strings.Contains(out, strings.Repeat("я", 20)) {
		t.Errorf("expected the truncated title, got:\n%q", out)
	}
}

func TestStoreHomeIsCreated(t *testing.T) {
	home := filepath.Join(t.TempDir(), "nested", "state")
	if _, err := runIn(t, home, "ws", "new", "x", "--home", home); err != nil {
		t.Fatalf("ws new: %v", err)
	}
	if _, err := os.Stat(filepath.Join(home, "config.json")); err != nil {
		t.Fatalf("state not written to --home: %v", err)
	}
}

// auto_commit is a global default, and a workspace has to be able to opt out of
// it at creation time or there is no way to say no.
func TestWSNewCanOptOutOfAutoCommit(t *testing.T) {
	home := t.TempDir()
	seed, err := store.Open(home)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	if err := seed.SetConfig(func(c *store.Config) { c.AutoCommit = true }); err != nil {
		t.Fatalf("seed config: %v", err)
	}

	if _, err := runIn(t, home, "ws", "new", "optedout", "--home", home, "--auto-commit=false"); err != nil {
		t.Fatalf("ws new: %v", err)
	}
	s, err := store.Open(home)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	w, err := s.Workspace("optedout")
	if err != nil {
		t.Fatalf("Workspace: %v", err)
	}
	if w.AutoCommitAgentCode {
		t.Error("auto_commit=true in the config overrode an explicit --auto-commit=false")
	}

	// Without the flag the config default still applies.
	if _, err := runIn(t, home, "ws", "new", "optedin", "--home", home); err != nil {
		t.Fatalf("ws new: %v", err)
	}
	s, err = store.Open(home)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	w, err = s.Workspace("optedin")
	if err != nil {
		t.Fatalf("Workspace: %v", err)
	}
	if !w.AutoCommitAgentCode {
		t.Error("auto_commit=true in the config was ignored")
	}
}
