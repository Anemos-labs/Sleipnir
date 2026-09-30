package provider

import (
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
)

func mustURL(t *testing.T, s string) *url.URL {
	t.Helper()
	u, err := url.Parse(s)
	if err != nil {
		t.Fatal(err)
	}
	return u
}

func TestSameOrigin(t *testing.T) {
	for _, tc := range []struct {
		a, b string
		want bool
	}{
		{"https://api.example.com/v1", "https://api.example.com/v2/other?x=1", true},
		{"https://api.example.com", "https://API.EXAMPLE.COM/", true},
		{"https://api.example.com", "https://api.example.com:443/", true},
		{"http://api.example.com", "http://api.example.com:80/", true},
		{"https://api.example.com.", "https://api.example.com/", true},
		{"http://127.0.0.1:8080/a", "http://127.0.0.1:8080/b", true},
		{"https://api.example.com/v1", "https://evil.example.com/v1", false},
		{"https://api.example.com", "https://api.example.com.evil.example/", false},
		{"https://api.example.com", "https://sub.api.example.com/", false},
		{"https://example.com", "https://api.example.com/", false},
		{"https://api.example.com", "http://api.example.com/", false}, // scheme downgrade
		{"http://api.example.com", "https://api.example.com/", false}, // even an upgrade is another origin
		{"https://api.example.com", "https://api.example.com:8443/", false},
		{"http://127.0.0.1:8080", "http://127.0.0.1:8081", false},
		{"http://127.0.0.1:8080", "http://localhost:8080", false}, // same machine, different origin
		{"https://api.example.com", "https://user:pass@api.example.com/", true},
	} {
		if got := SameOrigin(mustURL(t, tc.a), mustURL(t, tc.b)); got != tc.want {
			t.Errorf("SameOrigin(%s, %s) = %v, want %v", tc.a, tc.b, got, tc.want)
		}
	}
	if SameOrigin(nil, mustURL(t, "https://a")) || SameOrigin(mustURL(t, "https://a"), nil) {
		t.Error("a missing URL is nobody's origin")
	}
}

func TestIsLoopbackHost(t *testing.T) {
	for host, want := range map[string]bool{
		"localhost": true, "LOCALHOST": true, "localhost.": true,
		"127.0.0.1": true, "127.1.2.3": true, "127.255.255.254": true,
		"::1": true, "::ffff:127.0.0.1": true,
		"128.0.0.1": false, "10.0.0.1": false, "192.168.1.1": false, "0.0.0.0": false, "::": false,
		"127.0.0.1.evil.example": false, "localhost.evil.example": false, "evil-localhost": false,
		"foo.localhost": false, "127.1": false, "0x7f000001": false, "2130706433": false, "017700000001": false,
		"": false, "example.com": false, "::1%lo": false,
	} {
		if got := IsLoopbackHost(host); got != want {
			t.Errorf("IsLoopbackHost(%q) = %v, want %v", host, got, want)
		}
	}
}

func TestCheckKeyTransport(t *testing.T) {
	for _, tc := range []struct {
		url string
		bad string // substring of the error; "" means allowed
	}{
		{"https://api.openai.com/v1", ""},
		{"HTTPS://API.OPENAI.COM/v1", ""},
		{"https://user:pw@gateway.example/v1?x=1", ""},
		{"http://localhost:8000/v1", ""},
		{"http://127.0.0.1:11434", ""},
		{"http://[::1]:8080/v1", ""},
		{"http://127.8.9.10/v1", ""},
		{"http://collector.attacker.example/v1", "plain http to collector.attacker.example"},
		{"http://api.openai.com/v1", "plain http to api.openai.com"},
		{"http://10.0.0.5:8000/v1", "plain http to 10.0.0.5"},
		{"http://192.168.1.10/v1", "plain http"},
		{"http://0.0.0.0:8000", "plain http"},
		{"http://localhost.evil.example/v1", "plain http"},
		{"http://127.0.0.1.evil.example/v1", "plain http"},
		{"http://user@evil.example/v1", "plain http to evil.example"},
		{"ftp://example.com/v1", "scheme"},
		{"file:///etc/passwd", "absolute http(s) URL"},
		{"//example.com/v1", "is not http or https"},
		{"example.com/v1", "absolute http(s) URL"},
		{"", "absolute http(s) URL"},
		{"https://", "absolute http(s) URL"},
		{"http://:8080", "absolute http(s) URL"},
		{"https://exa mple.com", "absolute http(s) URL"},
	} {
		err := CheckKeyTransport(tc.url)
		switch {
		case tc.bad == "" && err != nil:
			t.Errorf("CheckKeyTransport(%q) refused: %v", tc.url, err)
		case tc.bad != "" && (err == nil || !strings.Contains(err.Error(), tc.bad)):
			t.Errorf("CheckKeyTransport(%q) = %v, want an error containing %q", tc.url, err, tc.bad)
		}
	}
	var ins *InsecureKeyError
	if err := CheckKeyTransport("http://example.com"); !errors.As(err, &ins) || ins.Host != "example.com" {
		t.Errorf("the refusal must be an *InsecureKeyError: %v", err)
	}
}

func TestInsecureKeyErrorEchoesNoControlCharacters(t *testing.T) {
	e := &InsecureKeyError{Host: "evil\x1b]52;c;AAAA\a.example"}
	if s := e.Error(); strings.ContainsAny(s, "\x1b\a") {
		t.Fatalf("%q", s)
	}
}

func TestRedactURL(t *testing.T) {
	for in, want := range map[string]string{
		"https://api.openai.com/v1":                         "https://api.openai.com/v1",
		"https://user:secret@gateway.example/v1?k=abc":      "https://gateway.example/v1",
		"https://gateway.example/v1#frag":                   "https://gateway.example/v1",
		"HTTP://LOCALHOST:8080/x?token=1":                   "http://LOCALHOST:8080/x",
		"http://user@host.example":                          "http://host.example",
		"not a url":                                         "<invalid URL>",
		"":                                                  "<invalid URL>",
		"https://gateway.example/v1/tok\x1b[31men?a=b":      "<invalid URL>",
		"https://host.example/" + strings.Repeat("a", 5000): "https://host.example/" + strings.Repeat("a", 490) + "…",
	} {
		got := RedactURL(in)
		if len(in) > 1000 { // the long one: only the shape matters
			if len(got) > 512 || !strings.HasPrefix(got, "https://host.example/aaa") {
				t.Errorf("RedactURL(long) = %d bytes", len(got))
			}
			continue
		}
		if got != want {
			t.Errorf("RedactURL(%q) = %q, want %q", in, got, want)
		}
		if strings.Contains(got, "secret") || strings.Contains(got, "token=") || strings.Contains(got, "k=abc") {
			t.Errorf("RedactURL(%q) leaks: %q", in, got)
		}
	}
}

// ---- redirects ---------------------------------------------------------------------------------

func TestSameOriginRedirectPolicy(t *testing.T) {
	first := &http.Request{URL: mustURL(t, "https://api.example.com/v1/chat")}
	hop := func(u string) *http.Request { return &http.Request{URL: mustURL(t, u)} }

	if err := SameOriginRedirects(hop("https://api.example.com/v1/chat/"), []*http.Request{first}); err != nil {
		t.Errorf("a same-origin redirect must be followed: %v", err)
	}
	err := SameOriginRedirects(hop("https://user:pw@collector.example:8443/x?q=secret#f"), []*http.Request{first})
	re, ok := AsRedirect(err)
	if !ok || re.Target != "https://collector.example:8443" {
		t.Fatalf("a foreign redirect must be refused, naming only the origin: %v", err)
	}
	if strings.Contains(err.Error(), "secret") || strings.Contains(err.Error(), "pw") || strings.Contains(err.Error(), "/x") {
		t.Errorf("the refusal leaks part of the target URL: %v", err)
	}
	if pe := RedirectFailure(re); pe.Kind != ErrBadRequest || pe.Retryable() || !pe.NoRetry || !strings.Contains(pe.Message, "collector.example:8443") {
		t.Errorf("failure = %+v", pe)
	}
	// Chains are judged against the ORIGINAL origin: A -> A -> B is refused at B.
	viaSame := []*http.Request{first, hop("https://api.example.com/step2")}
	if _, ok := AsRedirect(SameOriginRedirects(hop("https://elsewhere.example/"), viaSame)); !ok {
		t.Error("a redirect chain must not launder the origin")
	}
	// A downgrade is another origin.
	if _, ok := AsRedirect(SameOriginRedirects(hop("http://api.example.com/v1/chat"), []*http.Request{first})); !ok {
		t.Error("https -> http on the same host must be refused")
	}
	// Chains are bounded.
	var many []*http.Request
	for i := 0; i <= MaxRedirects; i++ {
		many = append(many, first)
	}
	if err := SameOriginRedirects(hop("https://api.example.com/x"), many); err == nil || !strings.Contains(err.Error(), "stopped after") {
		t.Errorf("too many redirects: %v", err)
	}
}

func TestAsRedirectFindsTheErrorInsideURLError(t *testing.T) {
	inner := &RedirectError{Target: "https://x.example"}
	wrapped := &url.Error{Op: "Post", URL: "https://api.example.com", Err: inner}
	if got, ok := AsRedirect(fmt.Errorf("outer: %w", wrapped)); !ok || got != inner {
		t.Fatal("AsRedirect must see through url.Error and wrapping")
	}
	if _, ok := AsRedirect(errors.New("boom")); ok {
		t.Fatal("false positive")
	}
	if _, ok := AsRedirect(nil); ok {
		t.Fatal("nil is not a redirect")
	}
}

// End to end through net/http: the body and headers of a POST never reach another
// origin, whatever the redirect status.
func TestHardenClientNeverSendsThePostToAnotherOrigin(t *testing.T) {
	var hits atomic.Int32
	var gotBody, gotKey string
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		b, _ := io.ReadAll(r.Body)
		gotBody, gotKey = string(b), r.Header.Get("X-Api-Key")
	}))
	defer target.Close()
	away := strings.Replace(target.URL, "127.0.0.1", "localhost", 1)

	for _, status := range []int{301, 302, 303, 307, 308} {
		t.Run(fmt.Sprint(status), func(t *testing.T) {
			src := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				http.Redirect(w, r, away+"/collect", status)
			}))
			defer src.Close()
			hits.Store(0)
			req, _ := http.NewRequest(http.MethodPost, src.URL+"/v1/chat/completions", strings.NewReader("PROPRIETARY SOURCE CODE"))
			req.Header.Set("X-Api-Key", "custom-secret")
			resp, err := HardenClient(&http.Client{}).Do(req)
			if resp != nil {
				resp.Body.Close()
			}
			re, ok := AsRedirect(err)
			if !ok {
				t.Fatalf("want a refused redirect, got %v", err)
			}
			if !strings.Contains(re.Target, "localhost") {
				t.Errorf("the refusal must name the target host: %v", re)
			}
			if hits.Load() != 0 || gotBody != "" || gotKey != "" {
				t.Fatalf("the redirect target received %d request(s): body %q, key %q", hits.Load(), gotBody, gotKey)
			}
		})
	}
}

func TestHardenClientFollowsSameOriginRedirectsAndKeepsTheBody(t *testing.T) {
	var body, key string
	mux := http.NewServeMux()
	mux.HandleFunc("/old", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/new", http.StatusTemporaryRedirect)
	})
	mux.HandleFunc("/new", func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		body, key = string(b), r.Header.Get("X-Api-Key")
		w.WriteHeader(204)
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()
	req, _ := http.NewRequest(http.MethodPost, srv.URL+"/old", strings.NewReader("payload"))
	req.Header.Set("X-Api-Key", "k")
	resp, err := HardenClient(nil).Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != 204 || body != "payload" || key != "k" {
		t.Fatalf("status %d body %q key %q", resp.StatusCode, body, key)
	}
}

func TestHardenClientComposesWithTheCallersPolicyAndDoesNotMutateIt(t *testing.T) {
	var refused atomic.Int32
	orig := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error {
		refused.Add(1)
		return http.ErrUseLastResponse
	}}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { http.Redirect(w, r, "/next", 302) }))
	defer srv.Close()
	resp, err := HardenClient(orig).Get(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != 302 || refused.Load() != 1 {
		t.Fatalf("the caller's stricter policy must still apply: status %d, consulted %d times", resp.StatusCode, refused.Load())
	}
	// The original client is untouched.
	if fmt.Sprintf("%p", orig.CheckRedirect) == "" || orig.CheckRedirect == nil {
		t.Fatal("the caller's client lost its policy")
	}
	hard := HardenClient(orig)
	if hard == orig {
		t.Fatal("HardenClient must return a copy")
	}
}
