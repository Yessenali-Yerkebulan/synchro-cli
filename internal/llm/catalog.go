package llm

import (
	"strings"
)

// ModelInfo describes one model offered by a provider.
type ModelInfo struct {
	ID   string
	Note string
	// Free is true when using this model costs nothing. Free models are
	// reported as $0 and are never the subject of any limit.
	Free   bool
	Input  float64 // USD per 1K input tokens
	Output float64 // USD per 1K output tokens
}

// CuratedModels returns a hand-picked snapshot of a provider's models.
//
// These lists are a starting point, not a guarantee: vendors add, rename and
// retire models constantly. `synchro-cli models --refresh` queries each provider's
// live catalogue and is the authoritative source; this exists so a brand-new
// install with a key can immediately pick a model without a network round-trip.
func CuratedModels(provider string) []ModelInfo {
	switch strings.ToLower(provider) {
	case "ollama":
		return []ModelInfo{
			{ID: "qwen3", Note: "balanced general purpose, good instruction following", Free: true},
			{ID: "qwen3:8b", Note: "same family, fixed 8B size", Free: true},
			{ID: "llama3.2", Note: "strong general reasoning", Free: true},
			{ID: "deepseek-r1", Note: "shows its reasoning, slow but thorough", Free: true},
			{ID: "gemma3", Note: "Google's open weights, solid at small sizes", Free: true},
			{ID: "qwen2.5-coder", Note: "tuned for writing code", Free: true},
			{ID: "mistral", Note: "fast, European alternative", Free: true},
			{ID: "phi4", Note: "Microsoft's small model, punches above its weight", Free: true},
		}
	case "gemini":
		return []ModelInfo{
			{ID: "gemini-2.5-flash", Note: "free tier, strong all-rounder", Free: true},
			{ID: "gemini-2.5-flash-lite", Note: "free tier, cheapest and fastest", Free: true},
			{ID: "gemini-2.0-flash", Note: "free tier, previous generation", Free: true},
		}
	case "openrouter":
		return []ModelInfo{
			{ID: "deepseek/deepseek-r1-0528:free", Note: "reasoning, free tier", Free: true},
			{ID: "deepseek/deepseek-chat-v3-0324:free", Note: "chat, free tier", Free: true},
			{ID: "qwen/qwen3-235b-a22b:free", Note: "large MoE, free tier", Free: true},
			{ID: "qwen/qwen3-32b:free", Note: "free tier", Free: true},
			{ID: "meta-llama/llama-3.3-70b-instruct:free", Note: "free tier", Free: true},
			{ID: "mistralai/mistral-small-3.1-24b-instruct:free", Note: "free tier", Free: true},
			{ID: "gpt-oss-120b:free", Note: "open weights, free tier", Free: true},
		}
	case "groq":
		return []ModelInfo{
			{ID: "llama-3.3-70b-versatile", Note: "free tier, very fast", Free: true},
			{ID: "llama-3.1-8b-instant", Note: "free tier, lowest latency", Free: true},
			{ID: "openai/gpt-oss-120b", Note: "free tier, reasoning", Free: true},
			{ID: "openai/gpt-oss-20b", Note: "free tier, small and quick", Free: true},
			{ID: "qwen/qwen3-32b", Note: "free tier", Free: true},
		}
	case "openai":
		return []ModelInfo{
			{ID: "gpt-4o-mini", Input: 0.00015, Output: 0.0006, Note: "cheapest capable OpenAI model"},
			{ID: "gpt-4o", Input: 0.0025, Output: 0.01, Note: "multimodal, stronger"},
			{ID: "gpt-4.1-mini", Input: 0.0004, Output: 0.0016, Note: "long context, cheap"},
			{ID: "gpt-4.1", Input: 0.002, Output: 0.008, Note: "long context, stronger"},
		}
	case "anthropic":
		return []ModelInfo{
			{ID: "claude-3-5-haiku-latest", Input: 0.0008, Output: 0.004, Note: "cheapest Claude"},
			{ID: "claude-3-5-sonnet-latest", Input: 0.003, Output: 0.015, Note: "balanced"},
			{ID: "claude-3-7-sonnet-latest", Input: 0.003, Output: 0.015, Note: "extended reasoning"},
			{ID: "claude-sonnet-4-5", Input: 0.003, Output: 0.015, Note: "current generation"},
		}
	case "deepseek":
		return []ModelInfo{
			{ID: "deepseek-chat", Input: 0.00027, Output: 0.0011, Note: "very cheap general model"},
			{ID: "deepseek-reasoner", Input: 0.00055, Output: 0.00219, Note: "reasoning, cheap"},
		}
	}
	return nil
}

// LookupModel finds a model in a provider's curated list, matching on an exact
// id or an id prefix (so "qwen3" matches "qwen3:8b").
func LookupModel(provider, model string) (ModelInfo, bool) {
	models := CuratedModels(provider)
	for _, m := range models {
		if m.ID == model {
			return m, true
		}
	}
	for _, m := range models {
		if strings.HasPrefix(m.ID, model) {
			return m, true
		}
	}
	return ModelInfo{}, false
}

// IsFreeModel reports whether a model is expected to cost nothing.
//
// Unrecognised models on a free-tier provider are assumed free rather than
// expensive: the conservative choice for a tool whose whole promise is that it
// will not quietly spend money, and `synchro-cli models` makes the assumption
// visible instead of hiding it.
func IsFreeModel(provider, model string) bool {
	if strings.ToLower(provider) == "ollama" {
		return true
	}
	if m, ok := LookupModel(provider, model); ok {
		return m.Free
	}
	// OpenRouter encodes freeness in the id itself.
	if strings.HasSuffix(model, ":free") {
		return true
	}
	if p, ok := Lookup(provider); ok && p.FreeTier {
		return true
	}
	return false
}

// EstimateCostUSD returns the approximate dollar cost of a call.
//
// This is a local bookkeeping figure, exactly like the web app's Project.spent_usd.
// It never gates anything: no limit, no quota, no refusal.
func EstimateCostUSD(provider, model string, tokens int) float64 {
	if tokens <= 0 || IsFreeModel(provider, model) {
		return 0
	}
	m, ok := LookupModel(provider, model)
	if !ok {
		// Unknown paid model: charge a blended rate rather than guess zero.
		return float64(tokens) / 1000 * 0.003
	}
	// Blended estimate: mostly output for a typical agent run.
	blended := (m.Input*0.3 + m.Output*0.7) / 1000
	return float64(tokens) * blended
}
