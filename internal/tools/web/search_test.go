package web

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/anemos-labs/sleipnir/internal/perm"
	"github.com/anemos-labs/sleipnir/internal/tools"
)

// fakeBackend is a Backend that records calls and returns canned results.
type fakeBackend struct {
	mu      sync.Mutex
	queries []string
	maxes   []int
	results []SearchResult
	err     error
	block   chan struct{} // when set, Search waits for it or the context
}

func (f *fakeBackend) Name() string { return "fake" }

func (f *fakeBackend) Search(ctx context.Context, q string, max int) ([]SearchResult, error) {
	f.mu.Lock()
	f.queries = append(f.queries, q)
	f.maxes = append(f.maxes, max)
	f.mu.Unlock()
	if f.block != nil {
		select {
		case <-f.block:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	return f.results, f.err
}

func runSearch(t *testing.T, b Backend, env *tools.Env, input any) *tools.Result {
	t.Helper()
	var raw json.RawMessage
	switch v := input.(type) {
	case string:
		raw = json.RawMessage(v)
	default:
		bs, err := json.Marshal(v)
		if err != nil {
			t.Fatal(err)
		}
		raw = bs
	}
	if env == nil {
		env = &tools.Env{Agent: "a", Perm: perm.AllowAll{}}
	}
	res, err := (&searchTool{b: b}).Run(context.Background(), &tools.Call{ID: "s1", Name: "web_search", Input: raw, Env: env})
	if err != nil {
		t.Fatalf("harness error: %v", err)
	}
	return res
}

func TestSearchToolOutput(t *testing.T) {
	fb := &fakeBackend{results: []SearchResult{
		{Title: "Go <strong>Programming</strong> Language", URL: "https://go.dev/", Snippet: "Go is an <b>open source</b> language &amp; toolchain.\n\n  Build simple, reliable software."},
		{Title: "", URL: "https://pkg.go.dev/std", Snippet: ""},
		{Title: "Bad scheme", URL: "javascript:alert(1)", Snippet: "dropped"},
		{Title: "Relative", URL: "/relative/path", Snippet: "dropped"},
		{Title: "No URL", URL: "", Snippet: "dropped"},
		{Title: strings.Repeat("T", 500), URL: "http://example.com/a b", Snippet: strings.Repeat("snippet ", 200)},
	}}
	res := runSearch(t, fb, nil, map[string]any{"query": "golang"})
	if res.IsError {
		t.Fatal(res.Text)
	}
	want := "1. Go Programming Language\n" +
		"   https://go.dev/\n" +
		"   Go is an open source language & toolchain. Build simple, reliable software.\n" +
		"2. pkg.go.dev\n" +
		"   https://pkg.go.dev/std\n" +
		"3. " + strings.Repeat("T", 200) + "…\n" +
		"   http://example.com/a%20b\n" +
		"   " + strings.TrimSpace(strings.Repeat("snippet ", 37)) + " snippe…"
	// (the last snippet is clipped to 300 characters; compute rather than hand-count)
	got := res.Text
	lines := strings.Split(got, "\n")
	if len(lines) != 8 {
		t.Fatalf("%d lines:\n%s", len(lines), got)
	}
	if strings.Join(lines[:6], "\n") != strings.Join(strings.Split(want, "\n")[:6], "\n") {
		t.Errorf("first entries:\n%s\nwant:\n%s", strings.Join(lines[:6], "\n"), strings.Join(strings.Split(want, "\n")[:6], "\n"))
	}
	if snip := strings.TrimSpace(lines[7]); len([]rune(snip)) != 301 || !strings.HasSuffix(snip, "…") {
		t.Errorf("snippet = %d runes %q", len([]rune(snip)), snip)
	}
	if res.Meta["backend"] != "fake" || res.Meta["results"] != 3 {
		t.Errorf("meta = %v", res.Meta)
	}
	if res.Truncated {
		t.Error("search output truncated")
	}
}

func TestSearchNoResults(t *testing.T) {
	res := runSearch(t, &fakeBackend{}, nil, map[string]any{"query": `needle "in" haystack`})
	if res.IsError || res.Text != `No results for "needle \"in\" haystack".` {
		t.Errorf("result = %v %q", res.IsError, res.Text)
	}
}

func TestSearchInputHandling(t *testing.T) {
	tests := []struct {
		name      string
		input     string
		wantErr   string
		wantQuery string
		wantMax   int
	}{
		{"defaults", `{"query":"go"}`, "", "go", 5},
		{"explicit max", `{"query":"go","max_results":3}`, "", "go", 3},
		{"string max", `{"query":"go","max_results":"7"}`, "", "go", 7},
		{"float max", `{"query":"go","max_results":4.9}`, "", "go", 4},
		{"max capped at 10", `{"query":"go","max_results":500}`, "", "go", 10},
		{"zero max means default", `{"query":"go","max_results":0}`, "", "go", 5},
		{"negative max means default", `{"query":"go","max_results":-3}`, "", "go", 5},
		{"whitespace in the query is normalised", "{\"query\":\"  go \\n  generics\\t\"}", "", "go generics", 5},
		{"long query is clipped", `{"query":"` + strings.Repeat("é", 1000) + `"}`, "", strings.Repeat("é", 400), 5},
		{"unknown fields ignored", `{"query":"go","site":"go.dev"}`, "", "go", 5},
		{"missing query", `{}`, `missing required field "query"`, "", 0},
		{"typo", `{"q":"go"}`, `unknown field(s) "q"`, "", 0},
		{"blank query", `{"query":"   "}`, `missing required field "query"`, "", 0},
		{"query wrong type", `{"query":["a"]}`, `field "query" must be a string`, "", 0},
		{"max wrong type", `{"query":"go","max_results":"many"}`, `field "max_results" must be an integer`, "", 0},
		{"array input", `[]`, "must be a JSON object", "", 0},
		{"truncated json", `{"query":"go`, "not valid JSON", "", 0},
		{"empty input", ``, `missing required field "query"`, "", 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fb := &fakeBackend{results: []SearchResult{{Title: "x", URL: "https://x.test/"}}}
			res := runSearch(t, fb, nil, tt.input)
			if tt.wantErr != "" {
				if !res.IsError || !strings.Contains(res.Text, tt.wantErr) || !strings.HasPrefix(res.Text, "web_search: ") {
					t.Fatalf("result = %v %q, want error containing %q", res.IsError, res.Text, tt.wantErr)
				}
				if len(fb.queries) != 0 {
					t.Error("backend called for invalid input")
				}
				return
			}
			if res.IsError {
				t.Fatal(res.Text)
			}
			if len(fb.queries) != 1 || fb.queries[0] != tt.wantQuery || fb.maxes[0] != tt.wantMax {
				t.Errorf("backend saw %q max=%d, want %q max=%d", fb.queries, fb.maxes, tt.wantQuery, tt.wantMax)
			}
		})
	}
}

func TestSearchTrimsToMaxResults(t *testing.T) {
	var rs []SearchResult
	for i := 0; i < 30; i++ {
		rs = append(rs, SearchResult{Title: fmt.Sprintf("r%d", i), URL: fmt.Sprintf("https://x.test/%d", i)})
	}
	res := runSearch(t, &fakeBackend{results: rs}, nil, map[string]any{"query": "q", "max_results": 4})
	if n := strings.Count(res.Text, "\n") + 1; n != 8 { // 4 entries x (title + url)
		t.Errorf("%d lines for 4 results:\n%s", n, res.Text)
	}
	if !strings.HasPrefix(res.Text, "1. r0\n") || !strings.Contains(res.Text, "4. r3\n") || strings.Contains(res.Text, "5. ") {
		t.Errorf("text = %q", res.Text)
	}
}

func TestSearchErrorsAndPermission(t *testing.T) {
	t.Run("backend error is shown", func(t *testing.T) {
		res := runSearch(t, &fakeBackend{err: errors.New("brave search returned HTTP 429")}, nil, map[string]any{"query": "q"})
		if !res.IsError || res.Text != "web_search: brave search returned HTTP 429" {
			t.Errorf("result = %v %q", res.IsError, res.Text)
		}
	})
	t.Run("deadline", func(t *testing.T) {
		fb := &fakeBackend{err: fmt.Errorf("wrapped: %w", context.DeadlineExceeded)}
		res := runSearch(t, fb, nil, map[string]any{"query": "q"})
		if !res.IsError || !strings.Contains(res.Text, "timed out after 30s") {
			t.Errorf("result = %v %q", res.IsError, res.Text)
		}
	})
	t.Run("denied", func(t *testing.T) {
		fb := &fakeBackend{}
		p := &recordingPerm{allow: false, why: "search is disabled"}
		env := &tools.Env{Agent: "planner", Role: "planner", Perm: p}
		res := runSearch(t, fb, env, map[string]any{"query": "secret plans"})
		if !res.IsError || res.Text != "permission denied: search is disabled" {
			t.Errorf("result = %v %q", res.IsError, res.Text)
		}
		if len(fb.queries) != 0 {
			t.Error("a denied search reached the backend")
		}
		r := p.seen()[0]
		if r.Tool != "web_search" || !r.Network || r.Writes || r.Agent != "planner" || r.Role != "planner" || r.Summary != `search the web for "secret plans"` {
			t.Errorf("request = %+v", r)
		}
	})
	t.Run("cancellation reaches the backend", func(t *testing.T) {
		fb := &fakeBackend{block: make(chan struct{})}
		ctx, cancel := context.WithCancel(context.Background())
		time.AfterFunc(50*time.Millisecond, cancel)
		res, err := (&searchTool{b: fb}).Run(ctx, &tools.Call{Input: json.RawMessage(`{"query":"q"}`), Env: &tools.Env{Perm: perm.AllowAll{}}})
		if err != nil || !res.IsError {
			t.Errorf("result = %+v %v", res, err)
		}
	})
	t.Run("nil env fails closed", func(t *testing.T) {
		fb := &fakeBackend{}
		res, err := (&searchTool{b: fb}).Run(context.Background(), &tools.Call{Input: json.RawMessage(`{"query":"q"}`)})
		if err != nil || !res.IsError || !strings.Contains(res.Text, "no permission policy") {
			t.Errorf("result = %+v %v", res, err)
		}
		if len(fb.queries) != 0 {
			t.Error("a search without a permission policy reached the backend")
		}
	})
}

// ------------------------------------------------------------- backends

type upstream struct {
	*httptest.Server
	mu   sync.Mutex
	reqs []*http.Request
	body []string
}

func (u *upstream) last() (*http.Request, string) {
	u.mu.Lock()
	defer u.mu.Unlock()
	return u.reqs[len(u.reqs)-1], u.body[len(u.body)-1]
}

func newUpstream(t *testing.T, status int, ctype, body string) *upstream {
	t.Helper()
	u := &upstream{}
	u.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		u.mu.Lock()
		u.reqs = append(u.reqs, r.Clone(context.Background()))
		u.body = append(u.body, string(b))
		u.mu.Unlock()
		if ctype != "" {
			w.Header().Set("Content-Type", ctype)
		}
		w.WriteHeader(status)
		io.WriteString(w, body)
	}))
	t.Cleanup(u.Close)
	return u
}

const braveJSON = `{"query":{"original":"go"},"web":{"results":[
 {"title":"The <strong>Go</strong> Programming Language","url":"https://go.dev/","description":"Go is an <strong>open source</strong> programming language.","age":"2 days"},
 {"title":"Go (programming language) - Wikipedia","url":"https://en.wikipedia.org/wiki/Go_(programming_language)","description":"Go is a statically typed &amp; compiled language."},
 {"title":"Tour","url":"https://go.dev/tour/"}
]}}`

func TestBrave(t *testing.T) {
	up := newUpstream(t, 200, "application/json", braveJSON)
	b := &Brave{APIKey: "test-key-123", BaseURL: up.URL + "/res/v1/web/search"}
	if b.Name() != "brave" {
		t.Errorf("Name = %q", b.Name())
	}
	rs, err := b.Search(context.Background(), `go "generics" & more`, 7)
	if err != nil {
		t.Fatal(err)
	}
	req, _ := up.last()
	if req.Method != "GET" || req.URL.Path != "/res/v1/web/search" {
		t.Errorf("request = %s %s", req.Method, req.URL)
	}
	if q := req.URL.Query(); q.Get("q") != `go "generics" & more` || q.Get("count") != "7" {
		t.Errorf("query = %v", q)
	}
	if req.Header.Get("X-Subscription-Token") != "test-key-123" || req.Header.Get("Accept") != "application/json" {
		t.Errorf("headers = %v", req.Header)
	}
	if req.Header.Get("User-Agent") != "Sleipnir/1.0" {
		t.Errorf("User-Agent = %q", req.Header.Get("User-Agent"))
	}
	if len(rs) != 3 || rs[0].URL != "https://go.dev/" || rs[2].Snippet != "" {
		t.Fatalf("results = %+v", rs)
	}
	// Through the tool, markup and entities are cleaned.
	res := runSearch(t, b, nil, map[string]any{"query": "go"})
	if res.IsError || !strings.HasPrefix(res.Text, "1. The Go Programming Language\n   https://go.dev/\n   Go is an open source programming language.\n2. ") ||
		!strings.Contains(res.Text, "Go is a statically typed & compiled language.") {
		t.Errorf("text = %q", res.Text)
	}
	if !strings.Contains(res.Text, "https://en.wikipedia.org/wiki/Go_(programming_language)") {
		t.Errorf("parenthesised url mangled: %q", res.Text)
	}
	// count is capped at what the API allows.
	b.Search(context.Background(), "x", 500)
	if req, _ := up.last(); req.URL.Query().Get("count") != "20" {
		t.Errorf("count = %q", req.URL.Query().Get("count"))
	}
}

func TestTavily(t *testing.T) {
	up := newUpstream(t, 200, "application/json", `{"query":"go","results":[
	 {"title":"Go","url":"https://go.dev/","content":"The Go programming language.","score":0.9,"raw_content":null},
	 {"title":"Docs","url":"https://go.dev/doc/","content":"Documentation.","score":0.5}]}`)
	tv := &Tavily{APIKey: "test-tavily-key", BaseURL: up.URL + "/search"}
	if tv.Name() != "tavily" {
		t.Errorf("Name = %q", tv.Name())
	}
	rs, err := tv.Search(context.Background(), "go generics", 4)
	if err != nil {
		t.Fatal(err)
	}
	req, body := up.last()
	if req.Method != "POST" || req.URL.Path != "/search" {
		t.Errorf("request = %s %s", req.Method, req.URL)
	}
	if req.Header.Get("Authorization") != "Bearer test-tavily-key" || req.Header.Get("Content-Type") != "application/json" {
		t.Errorf("headers = %v", req.Header)
	}
	var sent map[string]any
	if err := json.Unmarshal([]byte(body), &sent); err != nil {
		t.Fatal(err)
	}
	if sent["query"] != "go generics" || sent["max_results"] != float64(4) || sent["search_depth"] != "basic" || sent["include_answer"] != false {
		t.Errorf("body = %s", body)
	}
	if strings.Contains(body, "test-tavily-key") {
		t.Error("the API key leaked into the request body")
	}
	if len(rs) != 2 || rs[0].Snippet != "The Go programming language." || rs[1].URL != "https://go.dev/doc/" {
		t.Errorf("results = %+v", rs)
	}
	tv.Search(context.Background(), "x", 99)
	if _, body := up.last(); !strings.Contains(body, `"max_results":20`) {
		t.Errorf("max_results not capped: %s", body)
	}
}

func TestSearXNG(t *testing.T) {
	up := newUpstream(t, 200, "application/json", `{"query":"go","number_of_results":0,"results":[
	 {"title":"Go","url":"https://go.dev/","content":"Go language","engine":"duckduckgo"},
	 {"title":"Other","url":"https://example.org/","content":"More","engine":"bing"},
	 {"title":"Third","url":"https://example.net/","content":"Third"}]}`)
	for _, base := range []string{up.URL, up.URL + "/", up.URL + "/search", up.URL + "/search/"} {
		s := NewSearXNG(base)
		if s.Name() != "searxng" {
			t.Errorf("Name = %q", s.Name())
		}
		rs, err := s.Search(context.Background(), "go lang", 2)
		if err != nil {
			t.Fatalf("base %q: %v", base, err)
		}
		req, _ := up.last()
		if req.URL.Path != "/search" || req.URL.Query().Get("q") != "go lang" || req.URL.Query().Get("format") != "json" {
			t.Errorf("base %q: request = %s", base, req.URL)
		}
		if len(rs) != 3 { // searxng has no count parameter; the tool trims
			t.Errorf("base %q: %d results", base, len(rs))
		}
	}
	res := runSearch(t, NewSearXNG(up.URL), nil, map[string]any{"query": "go", "max_results": 2})
	if strings.Count(res.Text, "https://") != 2 {
		t.Errorf("tool did not trim to max_results: %q", res.Text)
	}

	t.Run("credentials in the base url are sent as basic auth", func(t *testing.T) {
		u := strings.Replace(up.URL, "http://", "http://alice:s3cret@", 1)
		if _, err := NewSearXNG(u).Search(context.Background(), "x", 1); err != nil {
			t.Fatal(err)
		}
		req, _ := up.last()
		if user, pass, ok := req.BasicAuth(); !ok || user != "alice" || pass != "s3cret" {
			t.Errorf("basic auth = %v %v %v", user, pass, ok)
		}
	})
	t.Run("json format disabled", func(t *testing.T) {
		forbidden := newUpstream(t, 403, "text/html", "<h1>Forbidden</h1>")
		_, err := NewSearXNG(forbidden.URL).Search(context.Background(), "x", 1)
		if err == nil || !strings.Contains(err.Error(), "json format disabled") {
			t.Errorf("err = %v", err)
		}
	})
	t.Run("bad base url", func(t *testing.T) {
		for _, bad := range []string{"", "not a url", "http://"} {
			if _, err := NewSearXNG(bad).Search(context.Background(), "x", 1); err == nil {
				t.Errorf("%q accepted", bad)
			}
		}
	})
}

func TestSearchBackendFailures(t *testing.T) {
	type mk func(base string) Backend
	backends := map[string]mk{
		"brave":   func(b string) Backend { return &Brave{APIKey: "k", BaseURL: b} },
		"tavily":  func(b string) Backend { return &Tavily{APIKey: "k", BaseURL: b} },
		"searxng": func(b string) Backend { return NewSearXNG(b) },
	}
	tests := []struct {
		name   string
		status int
		ctype  string
		body   string
		want   []string
	}{
		{"unauthorized", 401, "application/json", `{"error":"invalid key"}`, []string{"HTTP 401", "authentication failed", "invalid key"}},
		{"forbidden", 403, "application/json", `{"error":"nope"}`, []string{"HTTP 403"}},
		{"rate limited", 429, "application/json", `{"error":"slow down"}`, []string{"HTTP 429", "rate limit exceeded"}},
		{"server error", 502, "text/html", "<html>Bad Gateway</html>", []string{"HTTP 502", "failing"}},
		{"not json", 200, "text/html", "<html>login page</html>", []string{"not the expected JSON"}},
		{"empty body", 200, "application/json", "", []string{"not the expected JSON"}},
		{"json of the wrong shape", 200, "application/json", `[1,2,3]`, []string{"not the expected JSON"}},
		{"huge error body is clipped", 500, "text/plain", strings.Repeat("boom ", 10000), []string{"HTTP 500"}},
	}
	for name, mkBackend := range backends {
		for _, tt := range tests {
			t.Run(name+"/"+tt.name, func(t *testing.T) {
				if name == "searxng" && tt.status == 403 {
					t.Skip("403 has its own hint for searxng")
				}
				up := newUpstream(t, tt.status, tt.ctype, tt.body)
				base := up.URL
				if name != "searxng" {
					base += "/search"
				}
				_, err := mkBackend(base).Search(context.Background(), "q", 3)
				if err == nil {
					t.Fatal("expected an error")
				}
				for _, w := range tt.want {
					if !strings.Contains(err.Error(), w) {
						t.Errorf("error %q lacks %q", err, w)
					}
				}
				if !strings.Contains(err.Error(), name) {
					t.Errorf("error %q does not name the backend", err)
				}
				if len(err.Error()) > 500 {
					t.Errorf("error is %d bytes", len(err.Error()))
				}
				if strings.Contains(err.Error(), "k\"") {
					t.Errorf("error may leak the key: %q", err)
				}
			})
		}
	}

	t.Run("connection refused", func(t *testing.T) {
		up := newUpstream(t, 200, "application/json", "{}")
		base := up.URL
		up.Close()
		for name, mkBackend := range backends {
			_, err := mkBackend(base).Search(context.Background(), "q", 3)
			if err == nil || !strings.Contains(err.Error(), name+" search request failed") {
				t.Errorf("%s: err = %v", name, err)
			}
			if err != nil && strings.Contains(err.Error(), "http://") {
				t.Errorf("%s: url leaked into the error: %v", name, err)
			}
		}
	})
	t.Run("oversized response", func(t *testing.T) {
		up := newUpstream(t, 200, "application/json", `{"web":{"results":[]},"pad":"`+strings.Repeat("x", 5<<20)+`"}`)
		_, err := (&Brave{APIKey: "k", BaseURL: up.URL}).Search(context.Background(), "q", 3)
		if err == nil || !strings.Contains(err.Error(), "larger than 4 MiB") {
			t.Errorf("err = %v", err)
		}
	})
	t.Run("client timeout", func(t *testing.T) {
		release := make(chan struct{})
		slow := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { <-release }))
		t.Cleanup(func() { close(release); slow.Close() })
		b := &Brave{APIKey: "k", BaseURL: slow.URL, Client: &http.Client{Timeout: 200 * time.Millisecond}}
		start := time.Now()
		if _, err := b.Search(context.Background(), "q", 3); err == nil {
			t.Error("expected a timeout")
		}
		if time.Since(start) > 5*time.Second {
			t.Error("timeout not honoured")
		}
	})
	t.Run("context cancellation", func(t *testing.T) {
		release := make(chan struct{})
		slow := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { <-release }))
		t.Cleanup(func() { close(release); slow.Close() })
		ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
		defer cancel()
		if _, err := (&Tavily{APIKey: "k", BaseURL: slow.URL}).Search(ctx, "q", 3); err == nil {
			t.Error("expected an error")
		}
	})
}

func TestBackendFromEnv(t *testing.T) {
	clear := func(t *testing.T) {
		for _, k := range []string{"BRAVE_API_KEY", "TAVILY_API_KEY", "SEARXNG_URL"} {
			t.Setenv(k, "")
		}
	}
	tests := []struct {
		name string
		env  map[string]string
		want string // backend name, "" for nil
	}{
		{"nothing configured", nil, ""},
		{"blank values are ignored", map[string]string{"BRAVE_API_KEY": "  ", "TAVILY_API_KEY": "\t", "SEARXNG_URL": " "}, ""},
		{"brave", map[string]string{"BRAVE_API_KEY": "b"}, "brave"},
		{"tavily", map[string]string{"TAVILY_API_KEY": "t"}, "tavily"},
		{"searxng", map[string]string{"SEARXNG_URL": "http://localhost:8080"}, "searxng"},
		{"brave wins over the rest", map[string]string{"BRAVE_API_KEY": "b", "TAVILY_API_KEY": "t", "SEARXNG_URL": "http://x"}, "brave"},
		{"tavily wins over searxng", map[string]string{"TAVILY_API_KEY": "t", "SEARXNG_URL": "http://x"}, "tavily"},
		{"blank brave falls through", map[string]string{"BRAVE_API_KEY": " ", "SEARXNG_URL": "http://x"}, "searxng"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			clear(t)
			for k, v := range tt.env {
				t.Setenv(k, v)
			}
			b := BackendFromEnv()
			if tt.want == "" {
				if b != nil {
					t.Fatalf("BackendFromEnv() = %#v, want a nil interface", b)
				}
				return
			}
			if b == nil || backendName(b) != tt.want {
				t.Fatalf("BackendFromEnv() = %#v, want %s", b, tt.want)
			}
		})
	}
	// The configured values are used.
	clear(t)
	t.Setenv("BRAVE_API_KEY", " key-with-spaces ")
	if b, ok := BackendFromEnv().(*Brave); !ok || b.APIKey != "key-with-spaces" {
		t.Errorf("backend = %#v", BackendFromEnv())
	}
	clear(t)
	t.Setenv("SEARXNG_URL", "http://searx.local:8888")
	if b, ok := BackendFromEnv().(*SearXNG); !ok || b.BaseURL != "http://searx.local:8888" {
		t.Errorf("backend = %#v", BackendFromEnv())
	}
	// Register adds web_search exactly when a backend exists.
	clear(t)
	reg := tools.NewRegistry()
	Register(reg, Config{Backend: BackendFromEnv()})
	if _, ok := reg.Get("web_search"); ok {
		t.Error("web_search registered without a backend")
	}
	if _, ok := reg.Get("web_fetch"); !ok {
		t.Error("web_fetch missing")
	}
}

func TestSearchResultsAreStrippedOfHiddenCharacters(t *testing.T) {
	fb := &fakeBackend{results: []SearchResult{{
		Title:   "Ti\U000E0049\U000E0067\u202etle",
		URL:     "https://x.test/",
		Snippet: "sni\u200b\u2066ppet \U000E0041",
	}}}
	res := runSearch(t, fb, nil, map[string]any{"query": "q"})
	if want := "1. Title\n   https://x.test/\n   snippet"; res.Text != want {
		t.Errorf("text = %q, want %q", res.Text, want)
	}
}

func TestCleanSnippet(t *testing.T) {
	tests := []struct {
		in   string
		max  int
		want string
	}{
		{"plain", 100, "plain"},
		{"<b>bold</b> and <i>italic</i>", 100, "bold and italic"},
		{"a &amp; b &lt;c&gt; &quot;d&quot; &#39;e&#39;", 100, `a & b <c> "d" 'e'`},
		{"  lots \n\n of \t space  ", 100, "lots of space"},
		{"<script>alert(1)</script>text", 100, "alert(1)text"}, // markup is stripped, not executed; the text is data
		{strings.Repeat("é", 50), 10, strings.Repeat("é", 10) + "…"},
		{"", 10, ""},
		{"bad \xff utf8", 100, "bad utf8"},
		{"<unclosed tag", 100, "<unclosed tag"},
	}
	for _, tt := range tests {
		if got := cleanSnippet(tt.in, tt.max); got != tt.want {
			t.Errorf("cleanSnippet(%q, %d) = %q, want %q", tt.in, tt.max, got, tt.want)
		}
	}
}

// A backend needs only Search; Name is optional.
type bareBackend struct{}

func (bareBackend) Search(context.Context, string, int) ([]SearchResult, error) {
	return []SearchResult{{Title: "t", URL: "https://x.test/"}}, nil
}

func TestMinimalBackend(t *testing.T) {
	res := runSearch(t, bareBackend{}, nil, map[string]any{"query": "q"})
	if res.IsError || res.Meta["backend"] != "web.bareBackend" || !strings.Contains(res.Text, "https://x.test/") {
		t.Errorf("result = %v %v %q", res.IsError, res.Meta, res.Text)
	}
	reg := tools.NewRegistry()
	Register(reg, Config{Backend: bareBackend{}})
	if _, ok := reg.Get("web_search"); !ok {
		t.Error("web_search not registered for a minimal backend")
	}
}

func TestConfigFromEnv(t *testing.T) {
	clearAll := func(t *testing.T) {
		for _, k := range []string{"BRAVE_API_KEY", "TAVILY_API_KEY", "SEARXNG_URL", "HTTPS_PROXY", "https_proxy", "HTTP_PROXY", "http_proxy"} {
			t.Setenv(k, "")
		}
	}
	clearAll(t)
	if cfg := ConfigFromEnv(); cfg.Backend != nil || cfg.Proxy != nil || cfg.AllowPrivate || len(cfg.AllowHosts) != 0 {
		t.Errorf("empty environment gave %+v", cfg)
	}
	t.Setenv("HTTPS_PROXY", "http://proxy.local:3128")
	t.Setenv("TAVILY_API_KEY", "t")
	cfg := ConfigFromEnv()
	if cfg.Proxy == nil || cfg.Backend == nil || backendName(cfg.Backend) != "tavily" {
		t.Errorf("config = %+v", cfg)
	}
	if cfg.AllowPrivate {
		t.Error("the environment must never switch the SSRF guard off")
	}
	clearAll(t)
	t.Setenv("http_proxy", "http://proxy.local:3128")
	if ConfigFromEnv().Proxy == nil {
		t.Error("lower-case http_proxy ignored")
	}
}
