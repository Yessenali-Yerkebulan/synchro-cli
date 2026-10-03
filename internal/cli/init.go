package cli

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"github.com/synchro/synchro-cli/internal/agents"
	"github.com/synchro/synchro-cli/internal/llm"
	"github.com/synchro/synchro-cli/internal/model"
	"github.com/synchro/synchro-cli/internal/store"
)

func newInitCmd(st *rootState) *cobra.Command {
	var (
		template  string
		workspace string
		noWizard  bool
	)
	cmd := &cobra.Command{
		Use:   "init",
		Short: "Set up your first workspace, team and model",
		Long: strings.TrimSpace(`
Walks through the minimum needed to be productive:

  1. detect a local Ollama install and pick a model
  2. optionally store a free cloud key (Gemini, OpenRouter or Groq)
  3. create a workspace and a team

Nothing here is required except a model to talk to, and nothing costs money.
Run it again at any time to add another provider.`),
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			a, err := st.app()
			if err != nil {
				return err
			}
			interactive := !noWizard && !st.opt.Yes && a.P.IsTTY

			a.P.Wordmark(fmt.Sprintf("v%s  ·  setup", Version))
			a.P.Printf("  %s\n\n", a.P.Gray("No account, no subscription, no server. Everything stays in "+a.Store.Dir()))

			if err := setupProvider(a, interactive); err != nil {
				return err
			}
			if err := setupCloudKey(a, interactive); err != nil {
				return err
			}

			wsName := workspace
			if wsName == "" {
				wsName = "default"
			}
			ws := &model.Workspace{Name: wsName}
			if err := a.Store.CreateWorkspace(ws); err != nil {
				return err
			}
			a.P.Success("workspace %s created", a.P.Bold(ws.Name))

			tplID := template
			if tplID == "" {
				tplID = "mvp"
			}
			tpl, ok := agents.TemplateByID(tplID)
			if !ok {
				tpl, _ = agents.TemplateByID("mvp")
			}
			if err := createTeamFromTemplate(a, ws, tpl, tpl.Name); err != nil {
				return err
			}

			a.P.Blank()
			a.P.Title("You're set up")
			a.P.Printf("  %s\n", a.P.Bold("synchro-cli run \"<what you want built>\""))
			a.P.Printf("  %s\n", a.P.Bold("synchro-cli"))
			a.P.Blank()
			a.P.Hint("  the second one opens the interactive shell")
			a.P.Hint("  check your setup any time with: synchro-cli doctor")
			return nil
		},
	}
	cmd.Flags().StringVar(&template, "template", "mvp", "starter team template to create")
	cmd.Flags().StringVar(&workspace, "workspace", "", "name for the first workspace (default: default)")
	cmd.Flags().BoolVar(&noWizard, "no-wizard", false, "accept all defaults without asking anything")
	return cmd
}

// setupProvider picks a model. Local Ollama is tried first because it is the
// only option that needs no account at all.
func setupProvider(a *App, interactive bool) error {
	a.P.Heading("Model")

	base := a.Store.Config().OllamaURL
	ctx, cancel := context.WithTimeout(a.Ctx, 4*time.Second)
	models, err := llm.ListLocalModels(ctx, base, nil)
	cancel()

	if err != nil {
		a.P.Warn("no Ollama at %s", base)
		a.P.Hint("install it from https://ollama.com, then: ollama pull qwen3")
		a.P.Hint("or skip it and use a free cloud key in the next step")
		if err := a.Store.SetConfig(func(c *store.Config) { c.Provider = "ollama" }); err != nil {
			return err
		}
		return nil
	}

	if len(models) == 0 {
		a.P.Warn("Ollama is running but has no models")
		a.P.Hint("pull one: ollama pull qwen3")
		return nil
	}

	names := make([]string, 0, len(models))
	for _, m := range models {
		names = append(names, m.Name)
	}
	a.P.Success("found %d local model(s) at %s", len(models), base)

	// Prefer a model we know the quality of, if it happens to be installed.
	// The order matters: the first match wins and nothing later can override it.
	chosen := names[0]
preference:
	for _, want := range []string{"qwen3", "llama3.2", "gemma3", "mistral"} {
		for _, n := range names {
			if strings.HasPrefix(n, want) {
				chosen = n
				break preference
			}
		}
	}

	if interactive && len(names) > 1 {
		a.P.Blank()
		for i, n := range names {
			a.P.Printf("    %s %d. %s\n", a.P.Gray(" "), i+1, n)
		}
		a.P.Printf("  %s", a.P.Gray("which one? [1]: "))
		var answer string
		fmt.Scanln(&answer)
		if answer != "" {
			var idx int
			if _, err := fmt.Sscanf(answer, "%d", &idx); err == nil && idx >= 1 && idx <= len(names) {
				chosen = names[idx-1]
			}
		}
	}

	if err := a.Store.SetConfig(func(c *store.Config) {
		c.Provider = "ollama"
		c.Model = chosen
	}); err != nil {
		return err
	}
	a.P.Success("using ollama/%s - local, unlimited, free", chosen)
	return nil
}

// setupCloudKey optionally stores a free key for a hosted provider.
func setupCloudKey(a *App, interactive bool) error {
	a.P.Heading("Free cloud key (optional)")

	if !interactive {
		a.P.Info("skipping - re-run 'synchro-cli init' or use 'synchro-cli keys set <provider>' any time")
		return nil
	}

	options := []string{"gemini", "openrouter", "groq"}
	a.P.Printf("  %s\n", a.P.Gray("A free key gives you faster and stronger models. All three have a free tier with no card."))
	for i, o := range options {
		p, _ := llm.Lookup(o)
		a.P.Printf("    %s %d. %-12s %s\n", a.P.Gray(" "), i+1, p.Name, a.P.Gray(p.SignupURL))
	}
	a.P.Printf("    %s 4. skip\n", a.P.Gray(" "))
	a.P.Printf("  %s", a.P.Gray("which one? [4]: "))
	var answer string
	fmt.Scanln(&answer)

	choice := 4
	if answer != "" {
		fmt.Sscanf(answer, "%d", &choice)
	}
	if choice < 1 || choice > 3 {
		a.P.Info("skipped")
		return nil
	}
	p, _ := llm.Lookup(options[choice-1])
	key, err := promptSecret("  " + p.Name + " key (invisible)")
	if err != nil {
		return err
	}
	key = strings.TrimSpace(key)
	if key == "" {
		a.P.Info("skipped")
		return nil
	}
	if err := a.Store.SetCredential(p.Name, key, "added during setup"); err != nil {
		return err
	}
	a.P.Success("key stored for %s", p.Name)
	return nil
}

func newDoctorCmd(st *rootState) *cobra.Command {
	return &cobra.Command{
		Use:   "doctor",
		Short: "Check that everything needed is working",
		Long: strings.TrimSpace(`
Verifies the state directory, git, a reachable Ollama and your default model,
then tells you what to do about anything that is not ready.`),
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			a, err := st.app()
			if err != nil {
				return err
			}
			return runDoctor(a)
		},
	}
}

// runDoctor is shared by `synchro-cli doctor` and the shell's /doctor.
func runDoctor(a *App) error {
	problems := 0
	warn := func(format string, args ...any) {
		problems++
		a.P.Printf("  %s %s\n", a.P.Yellow("!"), fmt.Sprintf(format, args...))
	}
	ok := func(format string, args ...any) {
		a.P.Printf("  %s %s\n", a.P.Green("ok"), fmt.Sprintf(format, args...))
	}
	note := func(format string, args ...any) {
		a.P.Printf("  %s %s\n", a.P.Gray("-"), fmt.Sprintf(format, args...))
	}

	a.P.Title("synchro-cli doctor")
	a.P.Blank()

	// State directory.
	if err := checkWritable(a.Store.Dir()); err != nil {
		warn("state directory %s is not writable: %v", a.Store.Dir(), err)
	} else {
		ok("state directory %s", a.Store.Dir())
	}

	// git, needed to commit generated code.
	if path, err := exec.LookPath("git"); err != nil {
		warn("git not found on PATH - generated code will not be committed. Install git: https://git-scm.com")
	} else if v, err := exec.Command(path, "--version").Output(); err == nil {
		ok("%s", strings.TrimSpace(string(v)))
	} else {
		ok("git found at %s", path)
	}

	// Ollama. A missing Ollama is only a problem when nothing else can serve a
	// model, so this records readiness instead of warning outright; the verdict
	// is decided once the cloud keys are known.
	cfg := a.Store.Config()
	ctx, cancel := context.WithTimeout(a.Ctx, 4*time.Second)
	models, ollamaErr := llm.ListLocalModels(ctx, cfg.OllamaURL, nil)
	cancel()
	ollamaReady := ollamaErr == nil && len(models) > 0
	if ollamaErr != nil {
		a.P.Printf("  %s %s\n", a.P.Yellow("!"), fmt.Sprintf("no Ollama at %s", cfg.OllamaURL))
		a.P.Printf("      %s\n", a.P.Gray("optional but recommended: https://ollama.com"))
	} else if len(models) == 0 {
		a.P.Printf("  %s %s\n", a.P.Yellow("!"), fmt.Sprintf("Ollama is running at %s but has no models", cfg.OllamaURL))
		a.P.Printf("      %s\n", a.P.Gray("pull one: ollama pull qwen3"))
	} else {
		ok("Ollama at %s with %d model(s)", cfg.OllamaURL, len(models))
	}

	// Provider keys.
	e := a.NewEngine()
	ready := 0
	for _, p := range llm.Registry {
		if p.KeyRequired && e.ResolveAPIKey(p.Name) != "" {
			ready++
			ok("%s key available", p.Name)
		}
	}
	if ready == 0 {
		note("no cloud keys set - fine if Ollama works")
	}

	// The verdict that matters: can this install actually answer a prompt?
	if !ollamaReady && ready == 0 {
		warn("no model is reachable - start Ollama (ollama pull qwen3) or set a free key (synchro-cli keys set gemini)")
	}

	// Default model sanity.
	if _, ok := llm.Lookup(cfg.Provider); !ok {
		warn("default provider %q is not a known provider", cfg.Provider)
	}
	if llm.IsFreeModel(cfg.Provider, cfg.Model) {
		ok("default model %s/%s is free", cfg.Provider, cfg.Model)
	} else {
		note("default model %s/%s is billable to your own key", cfg.Provider, cfg.Model)
	}

	// Counts.
	wss := len(a.Store.Workspaces())
	if wss == 0 {
		note("no workspaces yet - run: synchro-cli init")
	} else {
		ok("%d workspace(s), %d agent(s), %d project(s), %d task(s)",
			wss, len(a.Store.Agents("")), len(a.Store.Projects("")), len(a.Store.Tasks("")))
	}

	a.P.Blank()
	if problems == 0 {
		a.P.Success("everything checks out")
		return nil
	}
	a.P.Warn("%d item(s) need attention", problems)
	return nil
}

func checkWritable(dir string) error {
	p := dir + string(filepath.Separator) + ".write-test"
	if err := writeFile(p, "ok"); err != nil {
		return err
	}
	return os.Remove(p)
}
