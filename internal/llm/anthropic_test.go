package llm

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// serveAnthropic answers /v1/messages the way the real API does: one JSON
// object, or typed SSE events when the request asked to stream.
func serveAnthropic(t *testing.T, stopReason string) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Stream bool `json:"stream"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)

		if !req.Stream {
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(map[string]any{
				"content":     []any{map[string]any{"type": "text", "text": "partial answer"}},
				"stop_reason": stopReason,
				"usage":       map[string]any{"input_tokens": 10, "output_tokens": 5},
			})
			return
		}

		w.Header().Set("Content-Type", "text/event-stream")
		events := []map[string]any{
			{"type": "message_start", "message": map[string]any{"usage": map[string]any{"input_tokens": 10}}},
			{"type": "content_block_delta", "delta": map[string]any{"text": "partial answer"}},
			{"type": "message_delta", "delta": map[string]any{"stop_reason": stopReason}, "usage": map[string]any{"output_tokens": 5}},
		}
		for _, e := range events {
			b, _ := json.Marshal(e)
			fmt.Fprintf(w, "data: %s\n\n", b)
		}
		fmt.Fprint(w, "data: [DONE]\n\n")
	}))
	t.Cleanup(srv.Close)
	return srv
}

func anthropicRequest(t *testing.T, base string, stream bool) *Result {
	t.Helper()
	p, ok := Lookup("anthropic")
	if !ok {
		t.Fatal("anthropic is not in the registry")
	}
	p.BaseURL = base
	res, err := callAnthropic(context.Background(), p, Request{
		Model:     "claude-sonnet-4-5",
		Prompt:    "hi",
		Stream:    stream,
		APIKey:    "test",
		MaxTokens: 16,
	})
	if err != nil {
		t.Fatalf("callAnthropic: %v", err)
	}
	return res
}

// Hitting the token budget is not the same as finishing, and the caller has to
// be able to tell the difference.
func TestAnthropicReportsTokenLimitTruncation(t *testing.T) {
	srv := serveAnthropic(t, "max_tokens")

	if res := anthropicRequest(t, srv.URL, false); !res.Truncated {
		t.Error("stop_reason=max_tokens was not reported as truncation")
	}
	if res := anthropicRequest(t, srv.URL, true); !res.Truncated {
		t.Error("streamed stop_reason=max_tokens was not reported as truncation")
	}
}

func TestAnthropicNormalStopIsNotTruncation(t *testing.T) {
	srv := serveAnthropic(t, "end_turn")

	res := anthropicRequest(t, srv.URL, false)
	if res.Truncated {
		t.Error("a normal end_turn was reported as truncation")
	}
	if res.Output != "partial answer" {
		t.Errorf("output = %q, want the assistant text", res.Output)
	}
	if res.TokensUsed != 15 {
		t.Errorf("tokens = %d, want 15", res.TokensUsed)
	}
}

func TestAnthropicStreamStillEmitsText(t *testing.T) {
	srv := serveAnthropic(t, "end_turn")

	var streamed strings.Builder
	p, _ := Lookup("anthropic")
	p.BaseURL = srv.URL
	res, err := callAnthropic(context.Background(), p, Request{
		Model:  "claude-sonnet-4-5",
		Prompt: "hi",
		Stream: true,
		OnDelta: func(s string) {
			streamed.WriteString(s)
		},
	})
	if err != nil {
		t.Fatalf("callAnthropic: %v", err)
	}
	if streamed.String() != "partial answer" {
		t.Errorf("streamed %q, want %q", streamed.String(), "partial answer")
	}
	if res.TokensUsed != 15 {
		t.Errorf("tokens = %d, want 15 from message_start plus message_delta", res.TokensUsed)
	}
}
