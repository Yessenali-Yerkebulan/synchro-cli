// Package llm is a dependency-free multi-provider LLM client.
//
// The web app had a plan gate: PLAN_LIMITS.allowed_providers restricted FREE
// accounts to Ollama, and CredentialService rejected every other BYOK key with
// an "Upgrade to..." error. There is no equivalent concept here. Every provider
// in this registry is usable the moment a key exists (or, for Ollama, the
// moment it runs). Nothing in this package can refuse a call because of who
// the caller is.
package llm

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// Style identifies the wire protocol a provider speaks.
type Style string

const (
	StyleOllama    Style = "ollama"
	StyleOpenAI    Style = "openai"
	StyleAnthropic Style = "anthropic"
	StyleGemini    Style = "gemini"
)

// Provider describes one backend Synchro can talk to.
type Provider struct {
	// Name is the lowercase identifier stored on an agent.
	Name  string
	Title string
	// Style selects the wire protocol.
	Style Style
	// BaseURL is overridable via `synchro config set ollama_url ...` or the
	// SYNCHRO_<PROVIDER>_URL environment variable.
	BaseURL string
	// KeyRequired is false only for self-hosted providers.
	KeyRequired bool
	// KeyEnv is the environment variable Synchro also checks, so a user can
	// export a key instead of running `synchro keys set`.
	KeyEnv string
	// SignupURL is where a free key comes from.
	SignupURL string
	// FreeTier is true when the provider offers a usable free path. This is
	// surfaced to the user as information only; it is never enforced.
	FreeTier bool
	// FreeNote explains the free path in one short line.
	FreeNote string
	// Local marks providers running on the user's own machine.
	Local bool
	// DocsURL points at the provider's API docs.
	DocsURL string
}

// Registry lists every provider Synchro supports, free ones first.
var Registry = []Provider{
	{
		Name:        "ollama",
		Title:       "Ollama (local, runs on this machine)",
		Style:       StyleOllama,
		BaseURL:     "http://127.0.0.1:11434",
		KeyRequired: false,
		KeyEnv:      "",
		FreeTier:    true,
		FreeNote:    "Unlimited and offline. Nothing ever leaves your machine.",
		Local:       true,
		DocsURL:     "https://ollama.com",
	},
	{
		Name:        "gemini",
		Title:       "Google Gemini (free tier)",
		Style:       StyleGemini,
		BaseURL:     "https://generativelanguage.googleapis.com",
		KeyRequired: true,
		KeyEnv:      "GEMINI_API_KEY",
		SignupURL:   "https://aistudio.google.com/apikey",
		FreeTier:    true,
		FreeNote:    "Free tier, no card required. Rate-limited, not billed.",
		DocsURL:     "https://ai.google.dev/gemini-api/docs",
	},
	{
		Name:        "openrouter",
		Title:       "OpenRouter (models tagged :free)",
		Style:       StyleOpenAI,
		BaseURL:     "https://openrouter.ai/api/v1",
		KeyRequired: true,
		KeyEnv:      "OPENROUTER_API_KEY",
		SignupURL:   "https://openrouter.ai/keys",
		FreeTier:    true,
		FreeNote:    "Any model whose id ends in ':free' costs nothing.",
		DocsURL:     "https://openrouter.ai/docs",
	},
	{
		Name:        "groq",
		Title:       "Groq (free tier, very fast)",
		Style:       StyleOpenAI,
		BaseURL:     "https://api.groq.com/openai/v1",
		KeyRequired: true,
		KeyEnv:      "GROQ_API_KEY",
		SignupURL:   "https://console.groq.com/keys",
		FreeTier:    true,
		FreeNote:    "Free tier. Extremely low latency inference.",
		DocsURL:     "https://console.groq.com/docs",
	},
	{
		Name:        "openai",
		Title:       "OpenAI",
		Style:       StyleOpenAI,
		BaseURL:     "https://api.openai.com/v1",
		KeyRequired: true,
		KeyEnv:      "OPENAI_API_KEY",
		SignupURL:   "https://platform.openai.com/api-keys",
		DocsURL:     "https://platform.openai.com/docs",
	},
	{
		Name:        "anthropic",
		Title:       "Anthropic Claude",
		Style:       StyleAnthropic,
		BaseURL:     "https://api.anthropic.com",
		KeyRequired: true,
		KeyEnv:      "ANTHROPIC_API_KEY",
		SignupURL:   "https://console.anthropic.com/settings/keys",
		DocsURL:     "https://docs.anthropic.com/en/api",
	},
	{
		Name:        "deepseek",
		Title:       "DeepSeek",
		Style:       StyleOpenAI,
		BaseURL:     "https://api.deepseek.com",
		KeyRequired: true,
		KeyEnv:      "DEEPSEEK_API_KEY",
		SignupURL:   "https://platform.deepseek.com/api_keys",
		DocsURL:     "https://api-docs.deepseek.com",
	},
}

// Lookup returns the provider with the given name.
func Lookup(name string) (Provider, bool) {
	name = strings.ToLower(strings.TrimSpace(name))
	for _, p := range Registry {
		if p.Name == name {
			return p, true
		}
	}
	// Tolerate the uppercase enum spelling used by the web app.
	switch strings.ToUpper(name) {
	case "CLAUDE":
		return Lookup("anthropic")
	}
	return Provider{}, false
}

// ProviderNames lists all provider names.
func ProviderNames() []string {
	out := make([]string, 0, len(Registry))
	for _, p := range Registry {
		out = append(out, p.Name)
	}
	return out
}

// FreeProviderNames lists only providers with a genuinely free path.
func FreeProviderNames() []string {
	var out []string
	for _, p := range Registry {
		if p.FreeTier {
			out = append(out, p.Name)
		}
	}
	return out
}

// Request is one completion call.
type Request struct {
	Provider string
	Model    string
	APIKey   string
	// System is the agent's persona. Passed as a first-class system prompt on
	// every provider rather than being prepended to the user message, so
	// caching and prompt-caching work where the provider supports it.
	System string
	// Prompt is the user turn: task title, description and role instructions.
	Prompt      string
	Temperature float64
	MaxTokens   int
	// Stream enables incremental delivery to OnDelta.
	Stream  bool
	OnDelta func(string)
	// Timeout bounds the whole call including streaming.
	Timeout time.Duration
	// BaseURL overrides the provider's built-in endpoint. It is what lets a
	// self-hosted server, a proxy or a different-region endpoint be used
	// without a code change; empty means "use the registry default".
	BaseURL string
	// HTTPClient is injectable for tests.
	HTTPClient *http.Client
}

// Result is a completed call.
type Result struct {
	Output string
	// TokensUsed is the provider's own count when reported, otherwise a
	// length-based estimate with TokensEstimated set.
	TokensUsed      int
	TokensEstimated bool
	Provider        string
	Model           string
	Duration        time.Duration
}

// Error is a provider-side failure with enough context to act on.
type Error struct {
	Provider   string
	StatusCode int
	Body       string
}

func (e *Error) Error() string {
	if e.StatusCode == 0 {
		return fmt.Sprintf("%s: %s", e.Provider, e.Body)
	}
	return fmt.Sprintf("%s: HTTP %d: %s", e.Provider, e.StatusCode, truncate(e.Body, 500))
}

func truncate(s string, n int) string {
	s = strings.TrimSpace(s)
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}

// Call dispatches to the provider's protocol implementation.
func Call(ctx context.Context, req Request) (*Result, error) {
	p, ok := Lookup(req.Provider)
	if !ok {
		return nil, fmt.Errorf("unknown provider %q (known: %s)", req.Provider, strings.Join(ProviderNames(), ", "))
	}
	// A caller-supplied endpoint wins over the registry default, so a
	// self-hosted server or a proxy can be used without editing the registry.
	if v := strings.TrimSpace(req.BaseURL); v != "" {
		p.BaseURL = v
	}
	if p.KeyRequired && strings.TrimSpace(req.APIKey) == "" {
		return nil, &KeyRequiredError{Provider: p}
	}

	if req.HTTPClient == nil {
		timeout := req.Timeout
		if timeout <= 0 {
			timeout = 10 * time.Minute
		}
		req.HTTPClient = &http.Client{Timeout: timeout}
	}
	if req.Timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, req.Timeout)
		defer cancel()
	}

	switch p.Style {
	case StyleOllama:
		return callOllama(ctx, p, req)
	case StyleOpenAI:
		return callOpenAI(ctx, p, req)
	case StyleAnthropic:
		return callAnthropic(ctx, p, req)
	case StyleGemini:
		return callGemini(ctx, p, req)
	}
	return nil, fmt.Errorf("provider %q has no implementation", p.Name)
}

// KeyRequiredError is returned when a provider needs a key and none is set.
type KeyRequiredError struct{ Provider Provider }

func (e *KeyRequiredError) Error() string {
	var b strings.Builder
	fmt.Fprintf(&b, "%s needs an API key. Get a free one at %s, then run:", e.Provider.Title, e.Provider.SignupURL)
	if e.Provider.KeyEnv != "" {
		fmt.Fprintf(&b, "\n\n  set %s=<your-key>    (environment variable)\n  or: synchro keys set %s", e.Provider.KeyEnv, e.Provider.Name)
	} else {
		fmt.Fprintf(&b, "\n\n  synchro keys set %s", e.Provider.Name)
	}
	return b.String()
}

// ---------------------------------------------------------------------------
// streaming helpers
// ---------------------------------------------------------------------------

// scanner is a line-oriented reader used by every streaming protocol.
type scanner struct{ sc *bufio.Scanner }

func newScanner(r io.Reader) *scanner {
	sc := bufio.NewScanner(r)
	// Streaming responses can carry very long lines (a whole essay in one
	// JSON object for non-OpenAI-style providers).
	sc.Buffer(make([]byte, 0, 64*1024), 8*1024*1024)
	return &scanner{sc: sc}
}

func (s *scanner) Scan() bool   { return s.sc.Scan() }
func (s *scanner) Text() string { return s.sc.Text() }
func (s *scanner) Err() error   { return s.sc.Err() }

// readErrorBody converts a non-2xx response into an *Error, trimming the
// provider's JSON envelope down to something a human can read.
func readErrorBody(provider string, resp *http.Response) *Error {
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 64*1024))
	body := strings.TrimSpace(string(raw))
	var envelope struct {
		Error json.RawMessage `json:"error"`
		// Gemini puts the message at the top level.
		Message string `json:"message"`
	}
	if err := json.Unmarshal(raw, &envelope); err == nil {
		if len(envelope.Error) > 0 {
			var s string
			if json.Unmarshal(envelope.Error, &s) == nil && s != "" {
				body = s
			} else {
				var obj struct {
					Message string `json:"message"`
				}
				if json.Unmarshal(envelope.Error, &obj) == nil && obj.Message != "" {
					body = obj.Message
				} else {
					body = string(envelope.Error)
				}
			}
		} else if envelope.Message != "" {
			body = envelope.Message
		}
	}
	if body == "" {
		body = http.StatusText(resp.StatusCode)
	}
	return &Error{Provider: provider, StatusCode: resp.StatusCode, Body: body}
}

// estimateTokens is the fallback when a provider omits usage. Roughly four
// characters per token for English prose and code.
func estimateTokens(s string) int {
	if s == "" {
		return 0
	}
	return (len(s) + 3) / 4
}

// doJSON performs a JSON request and returns the response for streaming.
func doJSON(ctx context.Context, client *http.Client, method, url string, headers map[string]string, payload any) (*http.Response, error) {
	var body io.Reader
	if payload != nil {
		b, err := json.Marshal(payload)
		if err != nil {
			return nil, err
		}
		body = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, url, body)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	return client.Do(req)
}
