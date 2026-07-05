package thinking

import (
	"context"
	"fmt"
	"html"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"

	"github.com/stukennedy/kyotee/internal/provider"
)

// WebSearch is the v1 grounding tool (spec 04 §3): it makes the
// prime-minister case work end-to-end. It queries DuckDuckGo's HTML
// endpoint — no API key required — and returns titles, URLs, and snippets.
type WebSearch struct {
	HTTPClient *http.Client
	BaseURL    string // override for tests; default https://html.duckduckgo.com/html/
	MaxResults int
}

func (w *WebSearch) Def() provider.ToolDef {
	return provider.ToolDef{
		Name:        "web_search",
		Description: "Search the web for current information. Use for any present-state fact (office-holders, prices, latest versions, live status) that could be stale in training data.",
		Schema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"query": map[string]any{
					"type":        "string",
					"description": "The search query",
				},
			},
			"required": []any{"query"},
		},
	}
}

var (
	ddgResultRe  = regexp.MustCompile(`(?s)<a[^>]+class="result__a"[^>]*href="([^"]+)"[^>]*>(.*?)</a>`)
	ddgSnippetRe = regexp.MustCompile(`(?s)<a[^>]+class="result__snippet"[^>]*>(.*?)</a>`)
	tagRe        = regexp.MustCompile(`<[^>]+>`)
)

func (w *WebSearch) Exec(ctx context.Context, input map[string]any) (string, error) {
	query, _ := input["query"].(string)
	if strings.TrimSpace(query) == "" {
		return "", fmt.Errorf("web_search: empty query")
	}

	baseURL := w.BaseURL
	if baseURL == "" {
		baseURL = "https://html.duckduckgo.com/html/"
	}
	client := w.HTTPClient
	if client == nil {
		client = &http.Client{Timeout: 30 * time.Second}
	}
	maxResults := w.MaxResults
	if maxResults == 0 {
		maxResults = 5
	}

	form := url.Values{"q": {query}}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, baseURL, strings.NewReader(form.Encode()))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("User-Agent", "kyotee/1.0 (harness web_search tool)")

	resp, err := client.Do(req)
	if err != nil {
		return "", fmt.Errorf("web_search: %w", err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 2<<20))
	if err != nil {
		return "", err
	}
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("web_search: status %d", resp.StatusCode)
	}

	links := ddgResultRe.FindAllStringSubmatch(string(body), maxResults)
	snippets := ddgSnippetRe.FindAllStringSubmatch(string(body), maxResults)
	if len(links) == 0 {
		return "No results found for: " + query, nil
	}

	var b strings.Builder
	fmt.Fprintf(&b, "Search results for %q:\n", query)
	for i, m := range links {
		title := cleanHTML(m[2])
		link := decodeDDGHref(m[1])
		fmt.Fprintf(&b, "\n%d. %s\n   %s\n", i+1, title, link)
		if i < len(snippets) {
			fmt.Fprintf(&b, "   %s\n", cleanHTML(snippets[i][1]))
		}
	}
	return b.String(), nil
}

func cleanHTML(s string) string {
	return strings.TrimSpace(html.UnescapeString(tagRe.ReplaceAllString(s, "")))
}

// decodeDDGHref unwraps DuckDuckGo's redirect links (//duckduckgo.com/l/?uddg=<url>).
func decodeDDGHref(href string) string {
	href = html.UnescapeString(href)
	if u, err := url.Parse(href); err == nil {
		if target := u.Query().Get("uddg"); target != "" {
			return target
		}
	}
	return href
}
