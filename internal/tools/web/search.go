package web

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"io"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/reee344/sleipnir/internal/core"
	"github.com/reee344/sleipnir/internal/harden"
	"github.com/reee344/sleipnir/internal/perm"
	"github.com/reee344/sleipnir/internal/tools"
)

// SearchResult is one hit.
type SearchResult struct {
	Title   string
	URL     string
	Snippet string
}

// Backend is a web search provider. Implementations return at most max results
// (the tool trims defensively) and errors that read well to a model: they are
// shown to it verbatim. The interface is deliberately one method; a backend that
// also implements Named is identified by name in results metadata.
type Backend interface {
	Search(ctx context.Context, query string, max int) ([]SearchResult, error)
}

// Named is optionally implemented by a Backend.
type Named interface{ Name() string }

func backendName(b Backend) string {
	if n, ok := b.(Named); ok {
		return n.Name()
	}
	return fmt.Sprintf("%T", b)
}

// BackendFromEnv builds a Backend from the environment, or returns nil when none
// is configured (in which case Register leaves web_search out). BRAVE_API_KEY
// wins over TAVILY_API_KEY, which wins over SEARXNG_URL.
func BackendFromEnv() Backend {
	// The keys are read with harden.Secret: a harness that moved its API keys out of
	// the process environment (harden.MoveKeys) still finds them.
	if k := strings.TrimSpace(harden.Secret("BRAVE_API_KEY")); k != "" {
		return NewBrave(k)
	}
	if k := strings.TrimSpace(harden.Secret("TAVILY_API_KEY")); k != "" {
		return NewTavily(k)
	}
	if u := strings.TrimSpace(os.Getenv("SEARXNG_URL")); u != "" {
		return NewSearXNG(u)
	}
	return nil
}

const (
	maxResults     = 10
	defaultResults = 5
	maxQueryChars  = 400
	maxAPIBody     = 4 << 20
	searchTimeout  = 30 * time.Second
)

// apiClient is what backends use unless overridden. It goes through the
// environment's proxy settings like any other client of a public API; the
// endpoints are operator-configured, so the SSRF guard does not apply (a local
// SearXNG on localhost is the normal setup).
func apiClient(c *http.Client) *http.Client {
	if c != nil {
		return c
	}
	return &http.Client{Timeout: 20 * time.Second}
}

// apiError is a non-2xx answer from a search API.
type apiError struct {
	Backend string
	Status  int
	Body    string
}

func (e *apiError) Error() string {
	hint := ""
	switch {
	case e.Status == http.StatusUnauthorized || e.Status == http.StatusForbidden:
		hint = " (authentication failed; check the API key)"
	case e.Status == http.StatusTooManyRequests:
		hint = " (rate limit exceeded; wait a moment before searching again)"
	case e.Status >= 500:
		hint = " (the search service is failing)"
	}
	msg := fmt.Sprintf("%s search returned HTTP %d%s", e.Backend, e.Status, hint)
	if e.Body != "" {
		msg += ": " + e.Body
	}
	return msg
}

// callJSON performs req and decodes a JSON answer into out.
func callJSON(client *http.Client, backend string, req *http.Request, out any) error {
	req.Header.Set("User-Agent", userAgent)
	resp, err := apiClient(client).Do(req)
	if err != nil {
		var ue *url.Error
		if errors.As(err, &ue) {
			err = ue.Err
		}
		return fmt.Errorf("%s search request failed: %v", backend, err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxAPIBody+1))
	if err != nil {
		return fmt.Errorf("%s search: reading the response failed: %v", backend, err)
	}
	if len(body) > maxAPIBody {
		return fmt.Errorf("%s search: the response is larger than %d MiB", backend, maxAPIBody>>20)
	}
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return &apiError{Backend: backend, Status: resp.StatusCode, Body: clipRunes(oneLine(string(bytes.ToValidUTF8(body, nil))), 200)}
	}
	if err := json.Unmarshal(body, out); err != nil {
		return fmt.Errorf("%s search: the response is not the expected JSON (%v)", backend, shortJSONErr(err))
	}
	return nil
}

func shortJSONErr(err error) string {
	s := err.Error()
	if len(s) > 120 {
		s = s[:120] + "…"
	}
	return s
}

// ------------------------------------------------------------------ Brave

// Brave is the Brave Search API (https://api.search.brave.com).
type Brave struct {
	APIKey  string
	BaseURL string // default https://api.search.brave.com/res/v1/web/search
	Client  *http.Client
}

// NewBrave returns a Brave backend using key.
func NewBrave(key string) *Brave { return &Brave{APIKey: key} }

func (b *Brave) Name() string { return "brave" }

func (b *Brave) Search(ctx context.Context, query string, max int) ([]SearchResult, error) {
	base := b.BaseURL
	if base == "" {
		base = "https://api.search.brave.com/res/v1/web/search"
	}
	u, err := url.Parse(base)
	if err != nil {
		return nil, fmt.Errorf("brave search: bad base URL: %v", err)
	}
	q := u.Query()
	q.Set("q", query)
	q.Set("count", strconv.Itoa(min(max, 20)))
	u.RawQuery = q.Encode()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("X-Subscription-Token", b.APIKey)

	var resp struct {
		Web struct {
			Results []struct {
				Title       string `json:"title"`
				URL         string `json:"url"`
				Description string `json:"description"`
			} `json:"results"`
		} `json:"web"`
	}
	if err := callJSON(b.Client, "brave", req, &resp); err != nil {
		return nil, err
	}
	out := make([]SearchResult, 0, len(resp.Web.Results))
	for _, r := range resp.Web.Results {
		out = append(out, SearchResult{Title: r.Title, URL: r.URL, Snippet: r.Description})
	}
	return out, nil
}

// ------------------------------------------------------------------ Tavily

// Tavily is the Tavily search API (https://api.tavily.com).
type Tavily struct {
	APIKey  string
	BaseURL string // default https://api.tavily.com/search
	Client  *http.Client
}

// NewTavily returns a Tavily backend using key.
func NewTavily(key string) *Tavily { return &Tavily{APIKey: key} }

func (t *Tavily) Name() string { return "tavily" }

func (t *Tavily) Search(ctx context.Context, query string, max int) ([]SearchResult, error) {
	base := t.BaseURL
	if base == "" {
		base = "https://api.tavily.com/search"
	}
	payload, _ := json.Marshal(map[string]any{
		"query":               query,
		"max_results":         min(max, 20),
		"search_depth":        "basic",
		"include_answer":      false,
		"include_raw_content": false,
	})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, base, bytes.NewReader(payload))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Authorization", "Bearer "+t.APIKey)

	var resp struct {
		Results []struct {
			Title   string `json:"title"`
			URL     string `json:"url"`
			Content string `json:"content"`
		} `json:"results"`
	}
	if err := callJSON(t.Client, "tavily", req, &resp); err != nil {
		return nil, err
	}
	out := make([]SearchResult, 0, len(resp.Results))
	for _, r := range resp.Results {
		out = append(out, SearchResult{Title: r.Title, URL: r.URL, Snippet: r.Content})
	}
	return out, nil
}

// ------------------------------------------------------------------ SearXNG

// SearXNG is a SearXNG instance (https://docs.searxng.org). The instance must
// have the JSON output format enabled (search.formats in settings.yml).
type SearXNG struct {
	BaseURL string // e.g. http://localhost:8080
	Client  *http.Client
}

// NewSearXNG returns a SearXNG backend for the instance at baseURL.
func NewSearXNG(baseURL string) *SearXNG { return &SearXNG{BaseURL: baseURL} }

func (s *SearXNG) Name() string { return "searxng" }

func (s *SearXNG) Search(ctx context.Context, query string, max int) ([]SearchResult, error) {
	base := strings.TrimRight(strings.TrimSpace(s.BaseURL), "/")
	if !strings.HasSuffix(base, "/search") {
		base += "/search"
	}
	u, err := url.Parse(base)
	if err != nil || u.Host == "" {
		return nil, fmt.Errorf("searxng search: %q is not a usable SEARXNG_URL", s.BaseURL)
	}
	q := u.Query()
	q.Set("q", query)
	q.Set("format", "json")
	u.RawQuery = q.Encode()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/json")

	var resp struct {
		Results []struct {
			Title   string `json:"title"`
			URL     string `json:"url"`
			Content string `json:"content"`
		} `json:"results"`
	}
	if err := callJSON(s.Client, "searxng", req, &resp); err != nil {
		var ae *apiError
		if errors.As(err, &ae) && ae.Status == http.StatusForbidden {
			return nil, fmt.Errorf("searxng search returned HTTP 403: the instance probably has the json format disabled (add json to search.formats in its settings)")
		}
		return nil, err
	}
	out := make([]SearchResult, 0, len(resp.Results))
	for _, r := range resp.Results {
		out = append(out, SearchResult{Title: r.Title, URL: r.URL, Snippet: r.Content})
	}
	return out, nil
}

// ------------------------------------------------------------------ the tool

type searchTool struct{ b Backend }

func (*searchTool) Spec() core.ToolSpec {
	return core.ToolSpec{
		Name: "web_search",
		Description: "Search the web and return a numbered list of results (title, URL, snippet). " +
			"Follow up with web_fetch to read a result. Keep queries specific; " +
			"max_results (default 5, at most 10) controls how many results come back.",
		InputSchema: json.RawMessage(`{"type":"object","properties":{` +
			`"query":{"type":"string","description":"Search query"},` +
			`"max_results":{"type":"integer","description":"Number of results, 1-10 (default 5)"}},` +
			`"required":["query"]}`),
		ReadOnly: true,
	}
}

func (t *searchTool) Run(ctx context.Context, c *tools.Call) (*tools.Result, error) {
	if c.Env == nil {
		c.Env = &tools.Env{}
	}
	env := c.Env.Defaults()

	a, err := parseArgs(c.Input)
	if err != nil {
		return fail(env, "web_search: %v", err), nil
	}
	query, present, err := a.str("query")
	switch {
	case err != nil:
		return fail(env, "web_search: %v", err), nil
	case !present || strings.TrimSpace(query) == "":
		return fail(env, "web_search: %v", a.missing("query", "query", "max_results")), nil
	}
	n, has, err := a.integer("max_results")
	if err != nil {
		return fail(env, "web_search: %v", err), nil
	}
	count := defaultResults
	if has && n > 0 {
		count = int(min(n, maxResults))
	}
	query = clipRunesPlain(oneLine(query), maxQueryChars)

	dec := env.Perm.Check(ctx, perm.Request{
		Agent:   env.Agent,
		Role:    env.Role,
		Tool:    "web_search",
		Input:   c.Input,
		Summary: "search the web for " + strconv.Quote(clipRunesPlain(query, 120)),
		Network: true,
	})
	if !dec.Allow {
		if dec.Reason != "" {
			return fail(env, "permission denied: %s", dec.Reason), nil
		}
		return fail(env, "permission denied"), nil
	}

	ctx, cancel := context.WithTimeout(ctx, searchTimeout)
	defer cancel()
	results, err := t.b.Search(ctx, query, count)
	if err != nil {
		if errors.Is(err, context.DeadlineExceeded) {
			return fail(env, "web_search: the search timed out after %s", fmtDuration(searchTimeout)), nil
		}
		return fail(env, "web_search: %v", err), nil
	}
	results = cleanResults(results, count)
	res := env.Finish(formatResults(query, results), false)
	res.Meta = map[string]any{"backend": backendName(t.b), "results": len(results)}
	return res, nil
}

func clipRunesPlain(s string, n int) string {
	if r := []rune(s); len(r) > n {
		return string(r[:n])
	}
	return s
}

var tagRe = regexp.MustCompile(`<[^>]*>`)

// cleanText strips markup (Brave wraps matches in <strong>) and entities, and
// bounds the length.
func cleanSnippet(s string, n int) string {
	s = html.UnescapeString(tagRe.ReplaceAllString(s, ""))
	s = oneLine(stripInvisible(strings.ToValidUTF8(s, "")))
	return clipRunes(s, n)
}

// cleanResults drops entries without a usable http(s) URL and trims the list.
func cleanResults(in []SearchResult, max int) []SearchResult {
	out := make([]SearchResult, 0, len(in))
	for _, r := range in {
		u, err := url.Parse(strings.TrimSpace(r.URL))
		if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
			continue
		}
		title := cleanSnippet(r.Title, 200)
		if title == "" {
			title = cleanSnippet(u.Host, 200) // hosts from upstream JSON may hold any bytes
		}
		if title == "" {
			title = u.String()
		}
		out = append(out, SearchResult{Title: title, URL: u.String(), Snippet: cleanSnippet(r.Snippet, 300)})
		if len(out) == max {
			break
		}
	}
	return out
}

// formatResults renders a compact numbered list: the model needs titles to
// choose and URLs to fetch, and a line of snippet to judge relevance.
func formatResults(query string, rs []SearchResult) string {
	if len(rs) == 0 {
		return "No results for " + strconv.Quote(query) + "."
	}
	var sb strings.Builder
	for i, r := range rs {
		if i > 0 {
			sb.WriteByte('\n')
		}
		fmt.Fprintf(&sb, "%d. %s\n   %s", i+1, r.Title, r.URL)
		if r.Snippet != "" {
			sb.WriteString("\n   " + r.Snippet)
		}
	}
	return sb.String()
}
