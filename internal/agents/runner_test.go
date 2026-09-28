package agents

import (
	"os"
	"testing"

	"github.com/synchro/synchro-cli/internal/store"
)

func TestBaseURLFallsBackToRegistry(t *testing.T) {
	// No override anywhere: the registry default is used.
	os.Unsetenv("SYNCHRO_OPENAI_URL")
	e := &Engine{Store: mustStore(t)}
	if got := e.baseURL("openai"); got != "https://api.openai.com/v1" {
		t.Errorf("baseURL(openai) = %q, want the registry default", got)
	}
}

func TestBaseURLHonoursEnvOverride(t *testing.T) {
	// This is what lets a self-hosted OpenAI-compatible server be used: point
	// SYNCHRO_OPENAI_URL at it and no code change is needed.
	t.Setenv("SYNCHRO_OPENAI_URL", "http://127.0.0.1:8080/v1")
	e := &Engine{Store: mustStore(t)}
	if got := e.baseURL("openai"); got != "http://127.0.0.1:8080/v1" {
		t.Errorf("baseURL(openai) = %q, want the override", got)
	}
}

func TestBaseURLPrefersOllamaConfig(t *testing.T) {
	// ollama_url is the documented setting, so it has to win over the env var.
	s := mustStore(t)
	s.SetConfig(func(c *store.Config) { c.OllamaURL = "http://192.168.1.10:11434" })
	t.Setenv("SYNCHRO_OLLAMA_URL", "http://127.0.0.1:11434")
	e := &Engine{Store: s}
	if got := e.baseURL("ollama"); got != "http://192.168.1.10:11434" {
		t.Errorf("baseURL(ollama) = %q, want the configured URL", got)
	}
}

func TestBaseURLUnknownProvider(t *testing.T) {
	e := &Engine{Store: mustStore(t)}
	if got := e.baseURL("nope"); got != "" {
		t.Errorf("baseURL(nope) = %q, want empty", got)
	}
}

func mustStore(t *testing.T) *store.Store {
	t.Helper()
	s, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	return s
}
