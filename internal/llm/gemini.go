package llm

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// callGemini implements the Google Generative Language API.
func callGemini(ctx context.Context, p Provider, req Request) (*Result, error) {
	genCfg := map[string]any{"temperature": req.Temperature}
	if req.MaxTokens > 0 {
		genCfg["maxOutputTokens"] = req.MaxTokens
	}
	payload := map[string]any{
		"contents": []map[string]any{
			{"role": "user", "parts": []map[string]string{{"text": req.Prompt}}},
		},
		"generationConfig": genCfg,
	}
	if req.System != "" {
		payload["systemInstruction"] = map[string]any{
			"parts": []map[string]string{{"text": req.System}},
		}
	}

	// The model id may or may not already carry the "models/" prefix.
	model := strings.TrimPrefix(req.Model, "models/")
	endpoint := fmt.Sprintf("%s/v1beta/models/%s:generateContent", strings.TrimRight(p.BaseURL, "/"), url.PathEscape(model))

	// The key is always a query parameter. Build the query properly so a
	// non-streaming call does not end up with a bare "&key=".
	query := url.Values{}
	query.Set("key", req.APIKey)
	if req.Stream {
		query.Set("alt", "sse")
	}
	endpoint += "?" + query.Encode()

	start := time.Now()
	resp, err := doJSON(ctx, req.HTTPClient, http.MethodPost, endpoint, nil, payload)
	if err != nil {
		return nil, fmt.Errorf("gemini unreachable: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, readErrorBody("gemini", resp)
	}

	res := &Result{Provider: "gemini", Model: req.Model}
	var sb strings.Builder
	emit := func(t string) {
		sb.WriteString(t)
		if req.Stream && req.OnDelta != nil {
			req.OnDelta(t)
		}
	}

	type part struct {
		Text string `json:"text"`
	}
	type candidate struct {
		Content struct {
			Parts []part `json:"parts"`
		} `json:"content"`
	}
	type envelope struct {
		Candidates    []candidate `json:"candidates"`
		UsageMetadata *struct {
			TotalTokenCount int `json:"totalTokenCount"`
		} `json:"usageMetadata"`
	}

	handle := func(e envelope) {
		for _, c := range e.Candidates {
			for _, pt := range c.Content.Parts {
				emit(pt.Text)
			}
		}
		if e.UsageMetadata != nil && e.UsageMetadata.TotalTokenCount > 0 {
			res.TokensUsed = e.UsageMetadata.TotalTokenCount
		}
	}

	if !req.Stream {
		var full envelope
		if err := json.NewDecoder(resp.Body).Decode(&full); err != nil {
			return nil, fmt.Errorf("gemini: decode: %w", err)
		}
		handle(full)
		res.Output = sb.String()
		res.TokensEstimated = res.TokensUsed == 0
		if res.TokensEstimated {
			res.TokensUsed = estimateTokens(res.Output)
		}
		res.Duration = time.Since(start)
		return res, nil
	}

	// Streaming with alt=sse yields "data: {...}" lines.
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
		var e envelope
		if err := json.Unmarshal([]byte(data), &e); err != nil {
			continue
		}
		handle(e)
	}
	if err := sc.Err(); err != nil {
		return nil, fmt.Errorf("gemini: stream: %w", err)
	}
	res.Output = sb.String()
	res.TokensEstimated = res.TokensUsed == 0
	if res.TokensEstimated {
		res.TokensUsed = estimateTokens(res.Output)
	}
	res.Duration = time.Since(start)
	return res, nil
}

// ListCloudModels returns the model ids a provider currently exposes.
// Used by `synchro models --refresh` so the tool never goes stale as vendors
// rename or retire models.
func ListCloudModels(ctx context.Context, p Provider, apiKey string, client *http.Client) ([]string, error) {
	if client == nil {
		client = &http.Client{Timeout: 20 * time.Second}
	}
	base := strings.TrimRight(p.BaseURL, "/")

	var urlStr string
	switch {
	case p.Name == "gemini":
		urlStr = base + "/v1beta/models?pageSize=200&key=" + url.QueryEscape(apiKey)
	case p.Name == "openrouter":
		// OpenRouter marks free models in the id, so filter client-side.
		urlStr = base + "/models"
	default:
		// Every remaining provider is OpenAI-compatible.
		urlStr = base + "/models"
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, urlStr, nil)
	if err != nil {
		return nil, err
	}
	if apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+apiKey)
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, readErrorBody(p.Name, resp)
	}

	switch {
	case p.Name == "gemini":
		var out struct {
			Models []struct {
				Name string `json:"name"`
			} `json:"models"`
		}
		if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
			return nil, err
		}
		var ids []string
		for _, m := range out.Models {
			// Gemini exposes embedding and tuning endpoints alongside chat;
			// only generative chat models are useful here.
			if strings.Contains(m.Name, "embedding") || strings.Contains(m.Name, "tuned") {
				continue
			}
			ids = append(ids, strings.TrimPrefix(m.Name, "models/"))
		}
		return ids, nil
	case p.Name == "openrouter":
		var out struct {
			Data []struct {
				ID string `json:"id"`
			} `json:"data"`
		}
		if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
			return nil, err
		}
		var ids []string
		for _, m := range out.Data {
			if strings.HasSuffix(m.ID, ":free") {
				ids = append(ids, m.ID)
			}
		}
		return ids, nil
	default:
		var out struct {
			Data []struct {
				ID string `json:"id"`
			} `json:"data"`
		}
		if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
			return nil, err
		}
		ids := make([]string, 0, len(out.Data))
		for _, m := range out.Data {
			if m.ID != "" {
				ids = append(ids, m.ID)
			}
		}
		return ids, nil
	}
}
