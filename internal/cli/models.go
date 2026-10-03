package cli

import (
	"context"
	"fmt"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"github.com/synchro/synchro-cli/internal/llm"
	"github.com/synchro/synchro-cli/internal/store"
)

func newModelsCmd(st *rootState) *cobra.Command {
	var (
		refresh bool
		all     bool
	)
	cmd := &cobra.Command{
		Use:   "models [provider]",
		Short: "List the models you can use right now",
		Long: strings.TrimSpace(`
Lists models per provider, marking which ones cost nothing.

  synchro-cli models              every provider, curated lists
  synchro-cli models ollama       just one provider
  synchro-cli models --refresh    ask each provider for its live catalogue
  synchro-cli models --all        include paid models

Free options, in order of how little setup they need:
  ollama      runs on this machine, unlimited, works offline
  gemini      free tier, a Google AI Studio key
  openrouter  any model whose id ends in :free
  groq        free tier, very fast`),
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			a, err := st.app()
			if err != nil {
				return err
			}
			provider := ""
			if len(args) == 1 {
				provider = args[0]
			}
			if all {
				provider += " --all"
			}
			if refresh {
				provider += " --refresh"
			}
			return printModelTable(a, provider)
		},
	}
	cmd.Flags().BoolVar(&refresh, "refresh", false, "query each provider for its current catalogue")
	cmd.Flags().BoolVar(&all, "all", false, "include paid providers and models")
	return cmd
}

// printModelTable backs both `synchro-cli models` and the shell's /models.
func printModelTable(a *App, arg string) error {
	arg = strings.TrimSpace(arg)
	live := false
	all := false
	for _, f := range strings.Fields(arg) {
		switch f {
		case "--all", "all":
			all = true
		case "--refresh", "refresh":
			live = true
		}
	}
	arg = strings.Join(removeFlags(strings.Fields(arg)), " ")

	providers := llm.Registry
	if arg != "" {
		p, ok := llm.Lookup(arg)
		if !ok {
			return fmt.Errorf("unknown provider %q. Known: %s", arg, strings.Join(llm.ProviderNames(), ", "))
		}
		providers = []llm.Provider{p}
	}
	if !all {
		var filtered []llm.Provider
		for _, p := range providers {
			if p.FreeTier {
				filtered = append(filtered, p)
			}
		}
		// With no provider named, show the free ones; with one named, honour
		// the request even if it is paid.
		if arg == "" {
			providers = filtered
		}
	}
	if len(providers) == 0 {
		a.P.Info("no free providers configured. Try: synchro-cli models --all")
		return nil
	}
	for i, p := range providers {
		if i > 0 {
			a.P.Blank()
		}
		printProviderModels(a, p, live)
	}
	return nil
}

func removeFlags(fields []string) []string {
	var out []string
	for _, f := range fields {
		if strings.HasPrefix(f, "-") || f == "all" || f == "refresh" {
			continue
		}
		out = append(out, f)
	}
	return out
}

// printProviderModels shows what is usable for one provider: local models when
// Ollama is running, a live catalogue if requested, and the curated snapshot
// otherwise.
func printProviderModels(a *App, p llm.Provider, refresh bool) {
	title := a.P.Bold(p.Title)
	if p.FreeTier {
		title += "  " + a.P.Green("[free]")
	} else {
		title += "  " + a.P.Gray("[paid]")
	}
	a.P.Printf("%s\n", title)
	if p.FreeNote != "" {
		a.P.Printf("  %s\n", a.P.Gray(p.FreeNote))
	}

	e := a.NewEngine()
	key := e.ResolveAPIKey(p.Name)

	type row struct{ id, note, cost string }
	var rows []row

	switch p.Name {
	case "ollama":
		base := a.Store.Config().OllamaURL
		ctx, cancel := context.WithTimeout(a.Ctx, 5*time.Second)
		models, err := llm.ListLocalModels(ctx, base, nil)
		cancel()
		if err != nil {
			a.P.Printf("  %s\n", a.P.Yellow(fmt.Sprintf("not reachable at %s", base)))
			a.P.Printf("  %s\n", a.P.Gray("install it: https://ollama.com  ·  then: ollama pull qwen3"))
		} else if len(models) == 0 {
			a.P.Printf("  %s\n", a.P.Gray("running, but no models installed. Try: ollama pull qwen3"))
		} else {
			sort.Slice(models, func(i, j int) bool { return models[i].Name < models[j].Name })
			a.P.Printf("  %s\n", a.P.Gray(fmt.Sprintf("%d model(s) installed:", len(models))))
			for _, m := range models {
				rows = append(rows, row{id: m.Name, note: m.Family, cost: "free"})
			}
		}
	default:
		if p.KeyRequired && key == "" {
			a.P.Printf("  %s\n", a.P.Yellow("no API key set"))
			a.P.Printf("  %s\n", a.P.Gray(fmt.Sprintf("get one free at %s, then: synchro-cli keys set %s", p.SignupURL, p.Name)))
		}
		var live []string
		if refresh && (key != "" || !p.KeyRequired) {
			ctx, cancel := context.WithTimeout(a.Ctx, 20*time.Second)
			ids, err := llm.ListCloudModels(ctx, p, key, nil)
			cancel()
			if err != nil {
				a.P.Printf("  %s\n", a.P.Yellow("live lookup failed: "+err.Error()))
			} else {
				live = ids
			}
		}
		if len(live) > 0 {
			a.P.Printf("  %s\n", a.P.Gray(fmt.Sprintf("%d model(s) available now:", len(live))))
			sort.Strings(live)
			for _, id := range live {
				cost := a.P.Green("free")
				if !llm.IsFreeModel(p.Name, id) {
					cost = a.P.Gray(money(llm.EstimateCostUSD(p.Name, id, 1000)) + "/1K")
				}
				rows = append(rows, row{id: id, cost: stripANSI(cost)})
			}
		} else {
			for _, m := range llm.CuratedModels(p.Name) {
				cost := a.P.Green("free")
				if !m.Free {
					cost = a.P.Gray(fmt.Sprintf("~%s/1K", money((m.Input*0.3+m.Output*0.7)/1000)))
				}
				rows = append(rows, row{id: m.ID, note: m.Note, cost: stripANSI(cost)})
			}
			if !refresh {
				a.P.Printf("  %s\n", a.P.Gray("(curated list — run with --refresh for the live catalogue)"))
			}
		}
	}

	if len(rows) == 0 {
		return
	}
	a.P.Blank()
	table := make([][]string, 0, len(rows))
	for _, r := range rows {
		table = append(table, []string{r.id, r.note, r.cost})
	}
	a.P.Table([]string{"MODEL", "NOTES", "COST"}, table)
}

// stripANSI removes escape sequences so they do not corrupt table alignment.
func stripANSI(s string) string {
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		if s[i] == 0x1b {
			for i < len(s) && s[i] != 'm' {
				i++
			}
			continue
		}
		b.WriteByte(s[i])
	}
	return b.String()
}

func newKeysCmd(st *rootState) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "keys <command>",
		Short: "Manage provider API keys",
		Long: strings.TrimSpace(`
Keys are stored in ~/.synchro/credentials.json, a plain file only you can read.
You can also skip this entirely and export the provider's own environment
variable instead - synchro checks those too.

No key is ever required for Ollama.`),
		RunE: func(cmd *cobra.Command, args []string) error {
			a, err := st.app()
			if err != nil {
				return err
			}
			return listKeys(a)
		},
	}

	var label string
	setCmd := &cobra.Command{
		Use:   "set <provider> [key]",
		Short: "Store an API key for a provider",
		Long: strings.TrimSpace(`
Pass the key as an argument, or omit it to be prompted without echo:

  synchro-cli keys set gemini
  synchro-cli keys set openrouter sk-or-v1-...`),
		Args: cobra.RangeArgs(1, 2),
		RunE: func(cmd *cobra.Command, args []string) error {
			a, err := st.app()
			if err != nil {
				return err
			}
			p, ok := llm.Lookup(args[0])
			if !ok {
				return fmt.Errorf("unknown provider %q. Known: %s", args[0], strings.Join(llm.ProviderNames(), ", "))
			}
			if !p.KeyRequired {
				a.P.Info("%s does not need a key — it runs locally", p.Title)
				return nil
			}
			var key string
			if len(args) == 2 {
				key = args[1]
			} else {
				key, err = promptSecret(fmt.Sprintf("%s API key", p.Name))
				if err != nil {
					return err
				}
			}
			key = strings.TrimSpace(key)
			if key == "" {
				return fmt.Errorf("empty key")
			}
			if err := a.Store.SetCredential(p.Name, key, label); err != nil {
				return err
			}
			a.P.Success("key stored for %s", p.Name)

			// Point the user at a free model to actually try.
			free := llm.CuratedModels(p.Name)
			for _, m := range free {
				if m.Free {
					a.P.Hint("try it:  synchro-cli agent edit <name> --provider %s --model %s", p.Name, m.ID)
					return nil
				}
			}
			return nil
		},
	}
	setCmd.Flags().StringVar(&label, "label", "", "optional note about where this key is from")

	rmCmd := &cobra.Command{
		Use:     "rm <provider>",
		Aliases: []string{"remove", "delete", "del", "unset"},
		Short:   "Delete a stored key",
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			a, err := st.app()
			if err != nil {
				return err
			}
			if _, ok := a.Store.Credential(args[0]); !ok {
				return fmt.Errorf("no stored key for %q", args[0])
			}
			if err := a.Store.DeleteCredential(args[0]); err != nil {
				return err
			}
			a.P.Success("key for %s removed", args[0])
			return nil
		},
	}

	cmd.AddCommand(setCmd, rmCmd)
	return cmd
}

// listKeys shows which providers are ready to use, without revealing secrets.
func listKeys(a *App) error {
	e := a.NewEngine()
	rows := make([][]string, 0, len(llm.Registry))
	for _, p := range llm.Registry {
		status := ""
		switch {
		case !p.KeyRequired:
			status = a.P.Green("ready (no key needed)")
		case e.ResolveAPIKey(p.Name) != "":
			status = a.P.Green("ready (key set)")
		default:
			status = a.P.Gray("needs a key")
		}
		rows = append(rows, []string{p.Name, p.Title, status, p.KeyEnv})
	}
	a.P.Table([]string{"PROVIDER", "WHAT", "STATUS", "ENV VAR"}, rows)
	a.P.Blank()
	a.P.Hint("store one with: synchro-cli keys set <provider>")
	a.P.Hint("free keys: %s", strings.Join(freeSignupURLs(), "  "))
	return nil
}

func freeSignupURLs() []string {
	var out []string
	for _, p := range llm.Registry {
		if p.FreeTier && p.SignupURL != "" {
			out = append(out, p.Name+": "+p.SignupURL)
		}
	}
	return out
}

// promptSecret reads a key from the terminal without echoing it.
func promptSecret(prompt string) (string, error) {
	fmt.Fprintf(os.Stdout, "%s: ", prompt)
	fd := int(os.Stdin.Fd())
	if !isTerminal(fd) {
		var line string
		if _, err := fmt.Fscanln(os.Stdin, &line); err != nil {
			return "", fmt.Errorf("could not read input")
		}
		fmt.Fprintln(os.Stdout)
		return line, nil
	}
	// term.ReadPassword handles the no-echo handling for us.
	b, err := readPassword(fd)
	fmt.Fprintln(os.Stdout)
	if err != nil {
		return "", err
	}
	return string(b), nil
}

// showConfig prints every setting, shared by `synchro-cli config` and /config.
func showConfig(a *App) error {
	cfg := a.Store.Config()
	rows := [][]string{
		{"provider", cfg.Provider, "default provider for new agents"},
		{"model", cfg.Model, "default model for new agents"},
		{"ollama_url", cfg.OllamaURL, "local inference endpoint"},
		{"temperature", fmt.Sprintf("%.2f", cfg.Temperature), "default sampling temperature"},
		{"max_tokens", fmt.Sprintf("%d", cfg.MaxTokens), "output token budget per call"},
		{"stream", boolStr(cfg.Stream), "stream answers as they arrive"},
		{"search", boolStr(cfg.Search), "ground researcher agents in web search"},
		{"auto_commit", boolStr(cfg.AutoCommit), "default auto-commit for new workspaces"},
		{"color", cfg.Color, "auto, always or never"},
		{"request_timeout", fmt.Sprintf("%ds", cfg.RequestTimeout), "per-call timeout"},
	}
	if cfg.PipelineMode != "" {
		rows = append(rows, []string{"pipeline_mode", cfg.PipelineMode, "chain used by /pipeline and synchro-cli pipeline"})
	}
	a.P.Table([]string{"SETTING", "VALUE", "WHAT IT DOES"}, rows)
	a.P.Blank()
	a.P.KeyValue("state dir", a.Store.Dir())
	a.P.Hint("change one with: synchro-cli config set <key> <value>")
	return nil
}

func newConfigCmd(st *rootState) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "config <command>",
		Short: "Read and change settings",
		RunE: func(cmd *cobra.Command, args []string) error {
			a, err := st.app()
			if err != nil {
				return err
			}
			return showConfig(a)
		},
	}

	setCmd := &cobra.Command{
		Use:   "set <key> <value>",
		Short: "Change a setting",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			a, err := st.app()
			if err != nil {
				return err
			}
			key, value := strings.ToLower(args[0]), args[1]
			var applyErr error
			err = a.Store.SetConfig(func(c *store.Config) {
				switch key {
				case "provider":
					if _, ok := llm.Lookup(value); !ok {
						applyErr = fmt.Errorf("unknown provider %q. Known: %s", value, strings.Join(llm.ProviderNames(), ", "))
						return
					}
					c.Provider = strings.ToLower(value)
				case "model":
					c.Model = value
				case "ollama_url":
					c.OllamaURL = value
				case "temperature":
					var f float64
					if _, err := fmt.Sscanf(value, "%g", &f); err != nil {
						applyErr = fmt.Errorf("temperature must be a number")
						return
					}
					c.Temperature = f
				case "max_tokens":
					var n int
					if _, err := fmt.Sscanf(value, "%d", &n); err != nil || n <= 0 {
						applyErr = fmt.Errorf("max_tokens must be a positive number")
						return
					}
					c.MaxTokens = n
				case "stream":
					b, err := parseBool(value)
					if err != nil {
						applyErr = err
						return
					}
					c.Stream = b
				case "search":
					b, err := parseBool(value)
					if err != nil {
						applyErr = err
						return
					}
					c.Search = b
				case "auto_commit":
					b, err := parseBool(value)
					if err != nil {
						applyErr = err
						return
					}
					c.AutoCommit = b
				case "color":
					switch strings.ToLower(value) {
					case "auto", "always", "never":
						c.Color = strings.ToLower(value)
					default:
						applyErr = fmt.Errorf("color must be auto, always or never")
					}
				case "request_timeout":
					var n int
					if _, err := fmt.Sscanf(value, "%d", &n); err != nil || n <= 0 {
						applyErr = fmt.Errorf("request_timeout must be a positive number of seconds")
						return
					}
					c.RequestTimeout = n
				default:
					applyErr = fmt.Errorf("unknown setting %q", key)
				}
			})
			if applyErr != nil {
				return applyErr
			}
			if err != nil {
				return err
			}
			a.P.Success("%s = %s", key, value)
			return nil
		},
	}

	cmd.AddCommand(setCmd)
	return cmd
}

func parseBool(s string) (bool, error) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "1", "true", "yes", "on":
		return true, nil
	case "0", "false", "no", "off":
		return false, nil
	}
	return false, fmt.Errorf("expected true or false, got %q", s)
}
