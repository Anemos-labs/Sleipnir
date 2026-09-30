package openaichat

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/reee344/sleipnir/internal/provider"
)

// collector is a server on another origin (localhost instead of 127.0.0.1: the same
// machine, a different host name) that records everything it is sent.
type collector struct {
	*httptest.Server
	mu   sync.Mutex
	hits int
	body string
	hdr  http.Header
	URL  string // http://localhost:port
}

func newCollector(t *testing.T) *collector {
	t.Helper()
	c := &collector{}
	c.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		c.mu.Lock()
		c.hits++
		c.body, c.hdr = string(b), r.Header.Clone()
		c.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, okBody)
	}))
	t.Cleanup(c.Server.Close)
	c.URL = strings.Replace(c.Server.URL, "127.0.0.1", "localhost", 1)
	return c
}

func (c *collector) saw() (hits int, body string, hdr http.Header) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.hits, c.body, c.hdr
}

func TestRedirectToAnotherOriginIsRefusedWithoutFollowing(t *testing.T) {
	for _, status := range []int{301, 302, 303, 307, 308} {
		for _, stream := range []bool{true, false} {
			t.Run(fmt.Sprintf("%d/stream=%v", status, stream), func(t *testing.T) {
				target := newCollector(t)
				src := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					http.Redirect(w, r, target.URL+"/collect?leak=1", status)
				}))
				defer src.Close()
				c := New(Config{
					Name: "x", BaseURL: src.URL, APIKey: "sk-provider-secret",
					Headers: map[string]string{"X-Api-Key": "custom-header-secret", "X-Org": "acme"},
				})
				resp, err := c.Do(context.Background(), &provider.Request{Prompt: secRevPrompt("PROPRIETARY SOURCE CODE"), NoStream: !stream}, nil)
				if resp != nil {
					t.Fatalf("a response came back from a redirect target: %+v", resp)
				}
				pe := wantProviderError(t, err, provider.ErrBadRequest, false)
				if !pe.NoRetry {
					t.Error("the refusal must not be retried")
				}
				if !strings.Contains(pe.Message, "another origin") || !strings.Contains(pe.Message, "localhost:") {
					t.Errorf("the refusal must name the target host: %q", pe.Message)
				}
				if strings.Contains(pe.Message, "leak=1") || strings.Contains(pe.Message, "/collect") {
					t.Errorf("the refusal must not echo the redirect's path or query: %q", pe.Message)
				}
				hits, body, hdr := target.saw()
				if hits != 0 || body != "" || hdr != nil {
					t.Fatalf("the redirect target received a request: hits=%d body=%q headers=%v", hits, body, hdr)
				}
			})
		}
	}
}

func TestSameOriginRedirectIsFollowedWithItsBody(t *testing.T) {
	var mu sync.Mutex
	var gotBody, gotAuth, gotOrg string
	mux := http.NewServeMux()
	mux.HandleFunc("/v1/chat/completions", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/v2/chat/completions", http.StatusTemporaryRedirect)
	})
	mux.HandleFunc("/v2/chat/completions", func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		mu.Lock()
		gotBody, gotAuth, gotOrg = string(b), r.Header.Get("Authorization"), r.Header.Get("X-Org")
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, okBody)
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()
	c := New(Config{Name: "x", BaseURL: srv.URL + "/v1", APIKey: "sk-k", Headers: map[string]string{"X-Org": "acme"}})
	resp, err := c.Do(context.Background(), &provider.Request{Prompt: secRevPrompt("hello there"), NoStream: true}, nil)
	if err != nil || resp.Turn.PlainText() != "ok" {
		t.Fatalf("a same-origin redirect must work: %v %v", resp, err)
	}
	mu.Lock()
	defer mu.Unlock()
	if !strings.Contains(gotBody, "hello there") || gotAuth != "Bearer sk-k" || gotOrg != "acme" {
		t.Fatalf("the redirected request lost its body or headers: body=%q auth=%q org=%q", gotBody, gotAuth, gotOrg)
	}
}

func TestRedirectLoopsEnd(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, r.URL.Path, http.StatusTemporaryRedirect)
	}))
	defer srv.Close()
	_, err := New(Config{Name: "x", BaseURL: srv.URL}).Do(context.Background(), &provider.Request{Prompt: secRevPrompt("hi"), NoStream: true}, nil)
	if err == nil || !strings.Contains(err.Error(), "stopped after") {
		t.Fatalf("a redirect loop must end with an error: %v", err)
	}
}

// A caller-supplied client (the RL harness passes one) cannot loosen the policy.
func TestSuppliedHTTPClientCannotFollowRedirectsAcrossOrigins(t *testing.T) {
	target := newCollector(t)
	src := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target.URL, http.StatusTemporaryRedirect)
	}))
	defer src.Close()
	permissive := &http.Client{Timeout: 5 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return nil }}
	c := New(Config{Name: "x", BaseURL: src.URL, APIKey: "k", HTTPClient: permissive})
	_, err := c.Do(context.Background(), &provider.Request{Prompt: secRevPrompt("secret prompt"), NoStream: true}, nil)
	wantProviderError(t, err, provider.ErrBadRequest, false)
	if hits, _, _ := target.saw(); hits != 0 {
		t.Fatalf("the redirect target was contacted %d time(s)", hits)
	}
	// And the caller's own client is left as it was.
	if permissive.CheckRedirect == nil {
		t.Fatal("the caller's client was modified")
	}
}

func TestRedirectRefusalIsNotClassifiedAsACancellation(t *testing.T) {
	target := newCollector(t)
	src := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target.URL, http.StatusFound)
	}))
	defer src.Close()
	_, err := New(Config{Name: "x", BaseURL: src.URL}).Do(context.Background(), &provider.Request{Prompt: secRevPrompt("hi")}, nil)
	pe := wantProviderError(t, err, provider.ErrBadRequest, false)
	if strings.Contains(pe.Message, "cancelled") {
		t.Fatalf("%q", pe.Message)
	}
}
