package llm

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

const reply = "Hello there.\n\nFILE: main.go\n```go\npackage main\n```\n"

// serve starts a fake OpenAI-compatible endpoint that returns reply, in either
// streaming or non-streaming form depending on the request.
func serve(t *testing.T, wantModel string) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Model  string `json:"model"`
			Stream bool   `json:"stream"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		if req.Model != wantModel {
			t.Errorf("model sent = %q, want %q", req.Model, wantModel)
		}

		if !req.Stream {
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(map[string]any{
				"choices": []any{map[string]any{
					"message": map[string]any{"role": "assistant", "content": reply},
				}},
				"usage": map[string]any{"total_tokens": 42},
			})
			return
		}

		w.Header().Set("Content-Type", "text/event-stream")
		for _, word := range strings.SplitAfter(reply, " ") {
			b, _ := json.Marshal(map[string]any{
				"choices": []any{map[string]any{
					"delta": map[string]any{"content": word},
				}},
			})
			fmt.Fprintf(w, "data: %s\n\n", b)
		}
		// A final chunk carrying usage only, which is how OpenAI reports
		// token counts for a streamed call.
		usage, _ := json.Marshal(map[string]any{
			"choices": []any{},
			"usage":   map[string]any{"total_tokens": 42},
		})
		fmt.Fprintf(w, "data: %s\n\n", usage)
		fmt.Fprint(w, "data: [DONE]\n\n")
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestCallCapturesOutput(t *testing.T) {
	// A provider call has to return the text it streamed, not just deliver it
	// to OnDelta. The task result is built from Result.Output, so an empty
	// field silently loses the whole answer.
	for _, stream := range []bool{false, true} {
		t.Run(fmt.Sprintf("stream=%v", stream), func(t *testing.T) {
			srv := serve(t, "test-model")

			var deltas strings.Builder
			res, err := Call(t.Context(), Request{
				Provider: "openai",
				Model:    "test-model",
				APIKey:   "k",
				Prompt:   "hi",
				Stream:   stream,
				OnDelta:  func(s string) { deltas.WriteString(s) },
				BaseURL:  srv.URL,
			})
			if err != nil {
				t.Fatalf("Call: %v", err)
			}
			if res.Output != reply {
				t.Errorf("Output = %q, want the full reply", res.Output)
			}
			// OnDelta is a streaming hook, so it only fires when streaming was
			// asked for; the text still has to arrive in Output either way.
			if stream && deltas.String() != reply {
				t.Errorf("streamed deltas = %q, want the full reply", deltas.String())
			}
			if !stream && deltas.Len() != 0 {
				t.Errorf("deltas fired on a non-streaming call: %q", deltas.String())
			}
			if res.TokensUsed != 42 {
				t.Errorf("TokensUsed = %d, want the provider's 42", res.TokensUsed)
			}
			if res.TokensEstimated {
				t.Error("TokensEstimated should be false when the provider reports usage")
			}
		})
	}
}

func TestCallEstimatesTokensWhenProviderIsSilent(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Stream bool `json:"stream"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		w.Header().Set("Content-Type", "text/event-stream")
		b, _ := json.Marshal(map[string]any{
			"choices": []any{map[string]any{"delta": map[string]any{"content": reply}}},
		})
		fmt.Fprintf(w, "data: %s\n\n", b)
		fmt.Fprint(w, "data: [DONE]\n\n")
	}))
	defer srv.Close()

	res, err := Call(t.Context(), Request{
		Provider: "openai", Model: "m", APIKey: "k", Prompt: "hi",
		Stream: true, BaseURL: srv.URL,
	})
	if err != nil {
		t.Fatalf("Call: %v", err)
	}
	if res.Output != reply {
		t.Errorf("Output = %q, want the full reply", res.Output)
	}
	if !res.TokensEstimated || res.TokensUsed == 0 {
		t.Errorf("expected an estimated non-zero token count, got %d (estimated=%v)", res.TokensUsed, res.TokensEstimated)
	}
}

func TestCallUsesBaseURLOverride(t *testing.T) {
	// The override has to reach the wire, otherwise a self-hosted
	// OpenAI-compatible server cannot be used at all.
	srv := serve(t, "m")
	res, err := Call(t.Context(), Request{
		Provider: "openai", Model: "m", APIKey: "k", Prompt: "hi", BaseURL: srv.URL,
	})
	if err != nil {
		t.Fatalf("Call: %v", err)
	}
	if res.Output != reply {
		t.Errorf("Output = %q", res.Output)
	}
}

func TestCallRejectsUnknownProvider(t *testing.T) {
	if _, err := Call(t.Context(), Request{Provider: "nope", Model: "m", APIKey: "k"}); err == nil {
		t.Error("expected an error for an unknown provider")
	}
}

func TestCallRequiresKey(t *testing.T) {
	_, err := Call(t.Context(), Request{Provider: "openai", Model: "m"})
	var ke *KeyRequiredError
	if err == nil {
		t.Fatal("expected a key error")
	}
	if !asKeyError(err, &ke) {
		t.Errorf("error = %T, want *KeyRequiredError", err)
	}
	if ke.Provider.SignupURL == "" {
		t.Error("the key error should point at a signup URL")
	}
}

func asKeyError(err error, target **KeyRequiredError) bool {
	ke, ok := err.(*KeyRequiredError)
	if ok {
		*target = ke
	}
	return ok
}

func TestOllamaCapturesOutput(t *testing.T) {
	// Same requirement on the Ollama protocol, both response shapes.
	for _, stream := range []bool{false, true} {
		t.Run(fmt.Sprintf("stream=%v", stream), func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				if !stream {
					json.NewEncoder(w).Encode(map[string]any{
						"message":           map[string]any{"content": reply},
						"prompt_eval_count": 10,
						"eval_count":        20,
					})
					return
				}
				for _, word := range strings.SplitAfter(reply, " ") {
					json.NewEncoder(w).Encode(map[string]any{
						"message": map[string]any{"content": word},
					})
				}
				json.NewEncoder(w).Encode(map[string]any{
					"message":           map[string]any{"content": ""},
					"done":              true,
					"prompt_eval_count": 10,
					"eval_count":        20,
				})
			}))
			defer srv.Close()

			res, err := Call(t.Context(), Request{
				Provider: "ollama", Model: "m", Prompt: "hi",
				Stream: stream, BaseURL: srv.URL,
			})
			if err != nil {
				t.Fatalf("Call: %v", err)
			}
			if res.Output != reply {
				t.Errorf("Output = %q, want the full reply", res.Output)
			}
			if res.TokensUsed != 30 {
				t.Errorf("TokensUsed = %d, want 30", res.TokensUsed)
			}
		})
	}
}
