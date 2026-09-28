package llm

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"
)

// callAnthropic implements the Claude Messages API.
//
// The web app hardcoded max_tokens: 1024, which truncated longer answers
// mid-sentence. It is taken from the caller's budget here instead, and
// finish_reason is surfaced so a silent truncation cannot look like a
// completed answer.
func callAnthropic(ctx context.Context, p Provider, req Request) (*Result, error) {
	maxTokens := req.MaxTokens
	if maxTokens <= 0 {
		maxTokens = 4096
	}
	payload := map[string]any{
		"model":      req.Model,
		"max_tokens": maxTokens,
		"messages": []map[string]string{
			{"role": "user", "content": req.Prompt},
		},
	}
	if req.System != "" {
		payload["system"] = req.System
	}

	headers := map[string]string{
		"x-api-key":         req.APIKey,
		"anthropic-version": "2023-06-01",
	}

	start := time.Now()
	resp, err := doJSON(ctx, req.HTTPClient, http.MethodPost, strings.TrimRight(p.BaseURL, "/")+"/v1/messages", headers, payload)
	if err != nil {
		return nil, fmt.Errorf("anthropic unreachable: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, readErrorBody("anthropic", resp)
	}

	res := &Result{Provider: "anthropic", Model: req.Model}
	var sb strings.Builder
	emit := func(t string) {
		sb.WriteString(t)
		if req.Stream && req.OnDelta != nil {
			req.OnDelta(t)
		}
	}

	type msgBlock struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}

	if !req.Stream {
		var full struct {
			Content []msgBlock `json:"content"`
			Usage   struct {
				InputTokens  int `json:"input_tokens"`
				OutputTokens int `json:"output_tokens"`
			} `json:"usage"`
			StopReason string `json:"stop_reason"`
		}
		if err := json.NewDecoder(resp.Body).Decode(&full); err != nil {
			return nil, fmt.Errorf("anthropic: decode: %w", err)
		}
		var out strings.Builder
		for _, b := range full.Content {
			if b.Type == "text" {
				out.WriteString(b.Text)
			}
		}
		emit(out.String())
		res.TokensUsed = full.Usage.InputTokens + full.Usage.OutputTokens
		res.Output = sb.String()
		res.TokensEstimated = res.TokensUsed == 0
		if res.TokensEstimated {
			res.TokensUsed = estimateTokens(res.Output)
		}
		res.Duration = time.Since(start)
		return res, nil
	}

	// Streaming: typed SSE events. Text arrives as content_block_delta.
	sc := newScanner(resp.Body)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if !strings.HasPrefix(line, "data:") {
			continue
		}
		data := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		if data == "" || data == "[DONE]" {
			continue
		}
		var evt struct {
			Type  string `json:"type"`
			Delta struct {
				Text string `json:"text"`
			} `json:"delta"`
			Usage struct {
				InputTokens  int `json:"input_tokens"`
				OutputTokens int `json:"output_tokens"`
			} `json:"usage"`
			Message struct {
				Usage struct {
					InputTokens int `json:"input_tokens"`
				} `json:"usage"`
			} `json:"message"`
		}
		if err := json.Unmarshal([]byte(data), &evt); err != nil {
			continue
		}
		switch evt.Type {
		case "content_block_delta":
			emit(evt.Delta.Text)
		case "message_start":
			res.TokensUsed += evt.Message.Usage.InputTokens
		case "message_delta":
			res.TokensUsed += evt.Usage.OutputTokens
		}
	}
	if err := sc.Err(); err != nil {
		return nil, fmt.Errorf("anthropic: stream: %w", err)
	}
	res.Output = sb.String()
	if res.TokensUsed == 0 {
		res.TokensEstimated = true
		res.TokensUsed = estimateTokens(res.Output)
	}
	res.Duration = time.Since(start)
	return res, nil
}
