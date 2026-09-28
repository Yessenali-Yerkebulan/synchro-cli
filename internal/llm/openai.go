package llm

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"
)

// callOpenAI implements the OpenAI chat-completions protocol, which is also
// spoken by DeepSeek, OpenRouter and Groq. All four are handled here.
func callOpenAI(ctx context.Context, p Provider, req Request) (*Result, error) {
	type msg struct {
		Role    string `json:"role"`
		Content string `json:"content"`
	}
	messages := []msg{}
	if req.System != "" {
		messages = append(messages, msg{"system", req.System})
	}
	messages = append(messages, msg{"user", req.Prompt})

	payload := map[string]any{
		"model":       req.Model,
		"messages":    messages,
		"temperature": req.Temperature,
		"stream":      req.Stream,
	}
	if req.MaxTokens > 0 {
		payload["max_tokens"] = req.MaxTokens
	}
	if req.Stream {
		// Ask for usage in the final chunk; providers that ignore this simply
		// omit it and we fall back to an estimate.
		payload["stream_options"] = map[string]any{"include_usage": true}
	}

	headers := map[string]string{"Authorization": "Bearer " + req.APIKey}
	// OpenRouter attributes traffic to the app that made it; harmless and
	// makes usage visible to anyone reading their dashboard.
	if p.Name == "openrouter" {
		headers["HTTP-Referer"] = "https://github.com/synchro/synchro-cli"
		headers["X-Title"] = "synchro-cli"
	}

	start := time.Now()
	resp, err := doJSON(ctx, req.HTTPClient, http.MethodPost, strings.TrimRight(p.BaseURL, "/")+"/chat/completions", headers, payload)
	if err != nil {
		return nil, fmt.Errorf("%s unreachable: %w", p.Name, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, readErrorBody(p.Name, resp)
	}

	res := &Result{Provider: p.Name, Model: req.Model}
	var sb strings.Builder
	emit := func(t string) {
		sb.WriteString(t)
		if req.Stream && req.OnDelta != nil {
			req.OnDelta(t)
		}
	}

	type chunk struct {
		Choices []struct {
			Delta struct {
				Content string `json:"content"`
			} `json:"delta"`
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
			FinishReason string `json:"finish_reason"`
		} `json:"choices"`
		Usage *struct {
			TotalTokens int `json:"total_tokens"`
		} `json:"usage"`
	}

	// Non-streaming: a single JSON object with the whole answer.
	if !req.Stream {
		var full chunk
		if err := json.NewDecoder(resp.Body).Decode(&full); err != nil {
			return nil, fmt.Errorf("%s: decode: %w", p.Name, err)
		}
		if len(full.Choices) == 0 {
			return nil, &Error{Provider: p.Name, Body: "response contained no choices"}
		}
		emit(full.Choices[0].Message.Content)
		if full.Usage != nil {
			res.TokensUsed = full.Usage.TotalTokens
		}
		res.TokensEstimated = res.TokensUsed == 0
		if res.TokensEstimated {
			res.TokensUsed = estimateTokens(res.Output)
		}
		res.Duration = time.Since(start)
		return res, nil
	}

	// Streaming: SSE, one JSON object per "data:" line, terminated by [DONE].
	sc := newScanner(resp.Body)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || !strings.HasPrefix(line, "data:") {
			continue
		}
		data := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		if data == "[DONE]" {
			break
		}
		var c chunk
		if err := json.Unmarshal([]byte(data), &c); err != nil {
			continue
		}
		if len(c.Choices) > 0 {
			emit(c.Choices[0].Delta.Content)
		}
		if c.Usage != nil && c.Usage.TotalTokens > 0 {
			res.TokensUsed = c.Usage.TotalTokens
		}
	}
	if err := sc.Err(); err != nil {
		return nil, fmt.Errorf("%s: stream: %w", p.Name, err)
	}
	res.TokensEstimated = res.TokensUsed == 0
	if res.TokensEstimated {
		res.TokensUsed = estimateTokens(res.Output)
	}
	res.Duration = time.Since(start)
	return res, nil
}
