package agents

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"
)

// SearchResult is one web hit used to ground a RESEARCHER agent.
type SearchResult struct {
	Title   string
	URL     string
	Snippet string
}

// Searcher fetches web results. Two implementations are wired up: a keyless
// DuckDuckGo scraper (the default, always free) and Tavily (used only if the
// user happens to have TAVILY_API_KEY exported, same variable the web app used).
type Searcher interface {
	Search(ctx context.Context, query string, limit int) ([]SearchResult, error)
}

// SearchOptions configures the default searcher.
type SearchOptions struct {
	// TavilyKey, when non-empty, switches to Tavily.
	TavilyKey string
	Timeout   time.Duration
	Client    *http.Client
}

// NewSearcher returns the best available searcher.
func NewSearcher(opts SearchOptions) Searcher {
	if opts.TavilyKey != "" {
		return &tavily{key: opts.TavilyKey, client: opts.Client, timeout: opts.Timeout}
	}
	return &duckduckgo{client: opts.Client, timeout: opts.Timeout}
}

// Search runs a query and returns up to limit results. Search is best-effort:
// a failure returns an empty slice rather than an error, because grounding an
// agent is an enhancement and a flaky network should not sink the task.
func Search(ctx context.Context, s Searcher, query string, limit int) []SearchResult {
	if s == nil || strings.TrimSpace(query) == "" {
		return nil
	}
	if limit <= 0 {
		limit = 5
	}
	res, err := s.Search(ctx, query, limit)
	if err != nil {
		return nil
	}
	return res
}

// FormatSearchContext renders results as a grounding block for the prompt.
func FormatSearchContext(results []SearchResult) string {
	if len(results) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString("Web search results for the task topic. Cite these URLs when you make a factual claim, and say so explicitly when you are inferring rather than citing:\n\n")
	for _, r := range results {
		fmt.Fprintf(&b, "- %s\n  %s\n", r.Title, r.URL)
		if r.Snippet != "" {
			fmt.Fprintf(&b, "  %s\n", r.Snippet)
		}
	}
	b.WriteString("\n")
	return b.String()
}

// ---------------------------------------------------------------------------
// DuckDuckGo (no key required)
// ---------------------------------------------------------------------------

type duckduckgo struct {
	client  *http.Client
	timeout time.Duration
}

var ddgLinkRe = regexp.MustCompile(`<a[^>]+href="([^"]*uddg=[^"]+)"[^>]*>(.*?)</a>`)
var tagRe = regexp.MustCompile(`<[^>]+>`)

func (d *duckduckgo) httpClient() *http.Client {
	if d.client != nil {
		return d.client
	}
	t := d.timeout
	if t <= 0 {
		t = 20 * time.Second
	}
	return &http.Client{Timeout: t}
}

// Search scrapes DuckDuckGo's keyless HTML endpoint. It is not an official
// API, so failures degrade to "no results" rather than propagating.
func (d *duckduckgo) Search(ctx context.Context, query string, limit int) ([]SearchResult, error) {
	endpoint := "https://lite.duckduckgo.com/lite/?q=" + url.QueryEscape(query)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "Mozilla/5.0 (compatible; synchro-cli/1.0)")
	req.Header.Set("Accept", "text/html")

	resp, err := d.httpClient().Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("duckduckgo: HTTP %d", resp.StatusCode)
	}
	buf := make([]byte, 0, 256*1024)
	tmp := make([]byte, 32*1024)
	for {
		n, err := resp.Body.Read(tmp)
		buf = append(buf, tmp[:n]...)
		if err != nil || len(buf) > 2*1024*1024 {
			break
		}
	}
	body := string(buf)

	var out []SearchResult
	seen := map[string]bool{}
	for _, m := range ddgLinkRe.FindAllStringSubmatch(body, -1) {
		raw, title := m[1], strings.TrimSpace(tagRe.ReplaceAllString(m[2], ""))
		u, err := url.Parse(raw)
		if err != nil {
			continue
		}
		target := u.Query().Get("uddg")
		if target == "" {
			continue
		}
		if unescaped, err := url.QueryUnescape(target); err == nil {
			target = unescaped
		}
		if seen[target] || title == "" {
			continue
		}
		seen[target] = true
		out = append(out, SearchResult{Title: unescape(title), URL: target})
		if len(out) >= limit {
			break
		}
	}
	return out, nil
}

func unescape(s string) string {
	r := strings.NewReplacer("&amp;", "&", "&lt;", "<", "&gt;", ">", "&quot;", `"`, "&#x27;", "'", "&#39;", "'")
	return r.Replace(s)
}

// ---------------------------------------------------------------------------
// Tavily (optional, only when a key is present)
// ---------------------------------------------------------------------------

type tavily struct {
	key     string
	client  *http.Client
	timeout time.Duration
}

func (t *tavily) httpClient() *http.Client {
	if t.client != nil {
		return t.client
	}
	to := t.timeout
	if to <= 0 {
		to = 20 * time.Second
	}
	return &http.Client{Timeout: to}
}

func (t *tavily) Search(ctx context.Context, query string, limit int) ([]SearchResult, error) {
	payload, _ := json.Marshal(map[string]any{
		"api_key":     t.key,
		"query":       query,
		"max_results": limit,
	})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, "https://api.tavily.com/search", strings.NewReader(string(payload)))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := t.httpClient().Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("tavily: HTTP %d", resp.StatusCode)
	}
	var out struct {
		Results []struct {
			Title   string `json:"title"`
			URL     string `json:"url"`
			Content string `json:"content"`
		} `json:"results"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, err
	}
	res := make([]SearchResult, 0, len(out.Results))
	for _, r := range out.Results {
		snippet := r.Content
		if len(snippet) > 240 {
			snippet = snippet[:240] + "..."
		}
		res = append(res, SearchResult{Title: r.Title, URL: r.URL, Snippet: snippet})
	}
	return res, nil
}
