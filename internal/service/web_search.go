package service

import (
	"context"
	"encoding/json"
	"fmt"
	"html"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"time"
)

// webSearcher answers web_search. The server uses Brave; tests use a fake.
type webSearcher interface {
	search(ctx context.Context, query string, count int) ([]webResult, error)
}

// webResult is one search hit: what web_search returns to the agent.
type webResult struct {
	Title   string `json:"title"`
	URL     string `json:"url"`
	Snippet string `json:"snippet,omitempty"`
	Age     string `json:"age,omitempty"`
}

// braveSearch calls the Brave Search API (https://api.search.brave.com).
type braveSearch struct {
	key, endpoint string
	http          *http.Client
}

func newBraveSearch(key string) *braveSearch {
	return &braveSearch{key: key, endpoint: "https://api.search.brave.com/res/v1/web/search", http: &http.Client{Timeout: 30 * time.Second}}
}

var tagPattern = regexp.MustCompile(`<[^>]*>`)

// plainSnippet turns Brave's snippet HTML (<strong> around matches, entities)
// into plain text.
func plainSnippet(s string) string {
	return oneLine(html.UnescapeString(tagPattern.ReplaceAllString(s, "")))
}

func (b *braveSearch) search(ctx context.Context, query string, count int) ([]webResult, error) {
	q := url.Values{"q": {query}, "count": {strconv.Itoa(count)}, "text_decorations": {"false"}}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, b.endpoint+"?"+q.Encode(), nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("X-Subscription-Token", b.key)
	resp, err := b.http.Do(req)
	if err != nil {
		return nil, problem(503, "unavailable", "Web search is unreachable right now; try again later or work without it.")
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return nil, problem(503, "unavailable", "Web search's answer could not be read.")
	}
	switch {
	case resp.StatusCode == http.StatusTooManyRequests:
		return nil, problem(429, "rate_limited", "Web search is busy; wait a little or work without it.")
	case resp.StatusCode != http.StatusOK:
		return nil, problem(503, "unavailable", fmt.Sprintf("Web search answered %d.", resp.StatusCode))
	}
	var out struct {
		Web struct {
			Results []struct {
				Title       string `json:"title"`
				URL         string `json:"url"`
				Description string `json:"description"`
				Age         string `json:"age"`
			} `json:"results"`
		} `json:"web"`
	}
	if err := json.Unmarshal(body, &out); err != nil {
		return nil, problem(503, "unavailable", "Web search's answer was not understood.")
	}
	results := []webResult{}
	for _, r := range out.Web.Results {
		results = append(results, webResult{Title: plainSnippet(r.Title), URL: r.URL, Snippet: plainSnippet(r.Description), Age: r.Age})
	}
	return results, nil
}
