package llm

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"
)

// callOllama talks to a local Ollama daemon via /api/chat.
//
// The web app used /api/generate, which takes a single flattened prompt and
// therefore has no real system role. /api/chat is used here so the agent's
// system prompt is a genuine system turn, which materially changes how
// instruction-following models behave.
func callOllama(ctx context.Context, p Provider, req Request) (*Result, error) {
	type msg struct {
		Role    string `json:"role"`
		Content string `json:"content"`
	}
	payload := map[string]any{
		"model":  req.Model,
		"stream": req.Stream,
		"messages": []msg{
			{Role: "system", Content: req.System},
			{Role: "user", Content: req.Prompt},
		},
		"options": map[string]any{
			"temperature": req.Temperature,
		},
	}
	if req.MaxTokens > 0 {
		payload["options"].(map[string]any)["num_predict"] = req.MaxTokens
	}

	start := time.Now()
	resp, err := doJSON(ctx, req.HTTPClient, http.MethodPost, p.BaseURL+"/api/chat", nil, payload)
	if err != nil {
		return nil, fmt.Errorf("ollama unreachable at %s: %w", p.BaseURL, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, readErrorBody("ollama", resp)
	}

	res := &Result{Provider: "ollama", Model: req.Model}
	var sb strings.Builder
	emit := func(t string) {
		sb.WriteString(t)
		if req.Stream && req.OnDelta != nil {
			req.OnDelta(t)
		}
	}

	if !req.Stream {
		var full struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
			PromptTokens int `json:"prompt_eval_count"`
			EvalCount    int `json:"eval_count"`
		}
		if err := json.NewDecoder(resp.Body).Decode(&full); err != nil {
			return nil, fmt.Errorf("ollama: decode: %w", err)
		}
		emit(full.Message.Content)
		res.TokensUsed = full.PromptTokens + full.EvalCount
		res.TokensEstimated = res.TokensUsed == 0
		if res.TokensEstimated {
			res.TokensUsed = estimateTokens(res.Output)
		}
		res.Duration = time.Since(start)
		return res, nil
	}

	// Streaming: newline-delimited JSON, one object per token batch.
	sc := newScanner(resp.Body)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			continue
		}
		var chunk struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
			Done         bool   `json:"done"`
			PromptTokens int    `json:"prompt_eval_count"`
			EvalCount    int    `json:"eval_count"`
			Error        string `json:"error"`
		}
		if err := json.Unmarshal([]byte(line), &chunk); err != nil {
			// A malformed line mid-stream is not worth aborting the run over.
			continue
		}
		if chunk.Error != "" {
			return nil, &Error{Provider: "ollama", Body: chunk.Error}
		}
		emit(chunk.Message.Content)
		if chunk.Done {
			res.TokensUsed = chunk.PromptTokens + chunk.EvalCount
		}
	}
	if err := sc.Err(); err != nil {
		return nil, fmt.Errorf("ollama: stream: %w", err)
	}
	res.TokensEstimated = res.TokensUsed == 0
	if res.TokensEstimated {
		res.TokensUsed = estimateTokens(res.Output)
	}
	res.Duration = time.Since(start)
	return res, nil
}

// LocalModel is a model installed in a local Ollama daemon.
type LocalModel struct {
	Name   string `json:"name"`
	Size   int64  `json:"size"`
	Family string `json:"family"`
}

// ListLocalModels queries the Ollama daemon for installed models. It is used by
// `synchro models` and by the setup wizard to offer only what actually exists.
func ListLocalModels(ctx context.Context, baseURL string, client *http.Client) ([]LocalModel, error) {
	if client == nil {
		client = &http.Client{Timeout: 10 * time.Second}
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimRight(baseURL, "/")+"/api/tags", nil)
	if err != nil {
		return nil, err
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, readErrorBody("ollama", resp)
	}
	var out struct {
		Models []struct {
			Name   string `json:"name"`
			Size   int64  `json:"size"`
			Family string `json:"details"`
		} `json:"models"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, err
	}
	models := make([]LocalModel, 0, len(out.Models))
	for _, m := range out.Models {
		family := ""
		var details struct {
			Family string `json:"family"`
		}
		if json.Unmarshal([]byte(m.Family), &details) == nil {
			family = details.Family
		}
		models = append(models, LocalModel{Name: m.Name, Size: m.Size, Family: family})
	}
	return models, nil
}

// Ping reports whether an Ollama daemon answers at baseURL.
func Ping(ctx context.Context, baseURL string, client *http.Client) error {
	if client == nil {
		client = &http.Client{Timeout: 3 * time.Second}
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimRight(baseURL, "/")+"/api/tags", nil)
	if err != nil {
		return err
	}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return readErrorBody("ollama", resp)
	}
	return nil
}
