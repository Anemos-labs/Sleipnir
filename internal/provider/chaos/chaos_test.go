package chaos_test

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/anemos-labs/sleipnir/internal/core"
	"github.com/anemos-labs/sleipnir/internal/provider"
	"github.com/anemos-labs/sleipnir/internal/provider/chaos"
	"github.com/anemos-labs/sleipnir/internal/provider/mock"
	"github.com/anemos-labs/sleipnir/internal/provider/openaichat"
)

// ---- what a fault is to an HTTP client ---------------------------------------------------------------------------------------

// threeFrames is a response of three flushed frames: 27 bytes in all, 9 each.
var threeFrames = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/event-stream")
	for _, f := range []string{"data: one", "data: two", "data: 3rd"} {
		_, _ = io.WriteString(w, f)
		_, _ = io.WriteString(w, "\n\n")
		if fl, ok := w.(http.Flusher); ok {
			fl.Flush()
		}
	}
})

const whole = "data: one\n\ndata: two\n\ndata: 3rd\n\n"

func get(t *testing.T, plan chaos.Plan, timeout time.Duration) (status int, header http.Header, body string, err error) {
	t.Helper()
	h := chaos.New(threeFrames, plan)
	ts := httptest.NewServer(h)
	t.Cleanup(ts.Close)
	c := &http.Client{Timeout: timeout, Transport: &http.Transport{DisableKeepAlives: true}}
	resp, err := c.Get(ts.URL)
	if err != nil {
		return 0, nil, "", err
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(resp.Body)
	return resp.StatusCode, resp.Header, string(b), err
}

func TestEachFaultIsWhatItSaysToAnHTTPClient(t *testing.T) {
	const guard = 30 * time.Second // the hang guard of the waits that must end; nothing here asserts that something took less
	t.Run("pass", func(t *testing.T) {
		st, _, body, err := get(t, chaos.Script(), guard)
		if err != nil || st != 200 || body != whole {
			t.Fatalf("%d %q %v", st, body, err)
		}
	})
	t.Run("status with a body and a retry-after", func(t *testing.T) {
		st, h, body, err := get(t, chaos.Script(chaos.Limited(1500*time.Millisecond)), guard)
		if err != nil || st != 429 || !strings.Contains(body, "Rate limit") || h.Get("Retry-After") != "2" {
			t.Fatalf("%d %v %q %v: a retry-after is in whole seconds, rounded up", st, h, body, err)
		}
	})
	t.Run("a proxy's page", func(t *testing.T) {
		st, h, body, err := get(t, chaos.Script(chaos.BadGateway()), guard)
		if err != nil || st != 502 || !strings.Contains(body, "<h1>502 Bad Gateway</h1>") || !strings.HasPrefix(h.Get("Content-Type"), "text/html") {
			t.Fatalf("%d %v %q %v", st, h, body, err)
		}
	})
	t.Run("reset", func(t *testing.T) {
		if _, _, body, err := get(t, chaos.Script(chaos.Fault{Kind: chaos.Reset}), guard); err == nil {
			t.Fatalf("a reset connection gave %q and no error", body)
		}
	})
	t.Run("abort after nine bytes", func(t *testing.T) {
		_, _, body, err := get(t, chaos.Script(chaos.Fault{Kind: chaos.Abort, After: 9}), guard)
		if !errors.Is(err, io.ErrUnexpectedEOF) || body != "data: one" {
			t.Fatalf("%q, %v: want the first nine bytes and an unexpected end of file", body, err)
		}
	})
	t.Run("abort in the middle of a frame", func(t *testing.T) {
		_, _, body, err := get(t, chaos.Script(chaos.Fault{Kind: chaos.Abort, After: 13}), guard)
		if !errors.Is(err, io.ErrUnexpectedEOF) || body != whole[:13] {
			t.Fatalf("%q, %v", body, err)
		}
	})
	t.Run("truncate after eleven bytes", func(t *testing.T) {
		_, _, body, err := get(t, chaos.Script(chaos.Fault{Kind: chaos.Truncate, After: 11}), guard)
		if err != nil || body != whole[:11] {
			t.Fatalf("%q, %v: the body ends cleanly after eleven bytes", body, err)
		}
	})
	t.Run("a cut beyond the end of the response is no cut", func(t *testing.T) {
		_, _, body, err := get(t, chaos.Script(chaos.Fault{Kind: chaos.Truncate, After: 1000}), guard)
		if err != nil || body != whole {
			t.Fatalf("%q, %v", body, err)
		}
	})
	t.Run("a stall that the client gives up on", func(t *testing.T) {
		_, _, _, err := get(t, chaos.Script(chaos.Fault{Kind: chaos.Stall, For: time.Hour}), 100*time.Millisecond)
		var ne interface{ Timeout() bool }
		if !errors.As(err, &ne) || !ne.Timeout() {
			t.Fatalf("%v, want a timeout", err)
		}
	})
	t.Run("a stall in the middle that ends", func(t *testing.T) {
		_, _, body, err := get(t, chaos.Script(chaos.Fault{Kind: chaos.StallMidStream, After: 9, For: 50 * time.Millisecond}), guard)
		if err != nil || body != whole {
			t.Fatalf("%q, %v: a stall shorter than the client's patience is a slow answer, and the whole of it arrives", body, err)
		}
	})
	t.Run("garbage", func(t *testing.T) {
		st, h, body, err := get(t, chaos.Script(chaos.Fault{Kind: chaos.Garbage}), guard)
		if err != nil || st != 200 || strings.HasPrefix(body, "data:") || !strings.HasPrefix(h.Get("Content-Type"), "text/html") {
			t.Fatalf("%d %v %q %v", st, h, body, err)
		}
	})
	t.Run("a trickle", func(t *testing.T) {
		_, _, body, err := get(t, chaos.Script(chaos.Fault{Kind: chaos.Trickle, For: time.Millisecond}), guard)
		if err != nil || body != whole {
			t.Fatalf("%q, %v: a slow-loris sends the whole of it, a byte at a time", body, err)
		}
	})
}

func TestScriptFaultsTheRequestsInOrderAndThenServesThemAsTheyAre(t *testing.T) {
	h := chaos.New(threeFrames, chaos.Script(chaos.Down(), chaos.Limited(time.Second)))
	ts := httptest.NewServer(h)
	defer ts.Close()
	var got []int
	for i := 0; i < 4; i++ {
		resp, err := http.Get(ts.URL)
		if err != nil {
			t.Fatal(err)
		}
		_, _ = io.Copy(io.Discard, resp.Body)
		resp.Body.Close()
		got = append(got, resp.StatusCode)
	}
	if want := []int{503, 429, 200, 200}; len(got) != 4 || got[0] != want[0] || got[1] != want[1] || got[2] != want[2] || got[3] != want[3] {
		t.Fatalf("statuses %v, want %v", got, want)
	}
	if h.Faulted() != 2 || len(h.Events()) != 4 || h.Events()[1].Fault.Status != 429 || h.Events()[0].N != 1 {
		t.Fatalf("events %+v", h.Events())
	}
}

func TestRandomIsAFunctionOfTheSeedAndTheRequestNumber(t *testing.T) {
	kinds := []chaos.Fault{chaos.Down(), chaos.BadGateway(), {Kind: chaos.Reset}}
	a, b := chaos.Random(7, 0.3, kinds...), chaos.Random(7, 0.3, kinds...)
	other := chaos.Random(8, 0.3, kinds...)
	faulted, differs := 0, 0
	const draws = 2000
	// The same plan asked in a different order says the same: concurrent requests do not change what happens to each.
	for i := draws; i >= 1; i-- {
		fa, fb := a.Next(i, nil), b.Next(i, nil)
		if fa != fb {
			t.Fatalf("request %d: %v then %v", i, fa, fb)
		}
		if fa.Kind != chaos.Pass {
			faulted++
		}
		if other.Next(i, nil) != fa {
			differs++
		}
	}
	if rate := float64(faulted) / draws; rate < 0.25 || rate > 0.35 {
		t.Errorf("%d of %d requests faulted, want about 30%%", faulted, draws)
	}
	if differs == 0 {
		t.Error("another seed made the same plan")
	}
	if chaos.Random(1, 1, kinds...).Next(1, nil).Kind == chaos.Pass || chaos.Random(1, 0, kinds...).Next(1, nil).Kind != chaos.Pass {
		t.Error("a rate of 1 faults every request and a rate of 0 none")
	}
}

// ---- what a fault is to the provider adapter ---------------------------------------------------------------------------------

// provide runs one streaming request through the adapter, in front of a mock endpoint that answers "an answer, whole" and is wrapped in plan.
func provide(t *testing.T, plan chaos.Plan, mutate func(*openaichat.Config)) (*provider.Response, error, *chaos.Handler) {
	t.Helper()
	srv := mock.New(mock.Config{}, func(*mock.Call) mock.Reply {
		return mock.Reply{Text: "an answer, whole, with enough words in it to span several frames of the stream"}
	})
	h := chaos.New(srv.Handler(), plan)
	ts := httptest.NewServer(h)
	t.Cleanup(ts.Close)
	cfg := openaichat.Config{Name: "mock", BaseURL: ts.URL, APIKey: "k", Options: openaichat.Options{SessionHeader: true, CacheKeyBody: true}}
	if mutate != nil {
		mutate(&cfg)
	}
	c := openaichat.New(cfg)
	p := &core.Prompt{Model: "mock-1", Params: core.Params{MaxTokens: 256}, Messages: []core.Message{{Role: core.RoleUser, Blocks: []core.Block{core.Text("hi")}}}}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	resp, err := c.Do(ctx, &provider.Request{Prompt: p}, func(provider.Event) {})
	return resp, err, h
}

// Whatever the endpoint does, the adapter says what it was in the terms the agent decides by: an error that is retryable when
// repeating the request can help, one that is not when it cannot, and never an answer made of what arrived before the failure.
func TestTheAdapterClassifiesWhatTheEndpointDid(t *testing.T) {
	for _, c := range []struct {
		name      string
		fault     chaos.Fault
		kind      provider.ErrKind
		retryable bool
		mutate    func(*openaichat.Config)
	}{
		{"the database is down", chaos.Down(), provider.ErrServer, true, nil},
		{"ownership was lost", chaos.Lost(), provider.ErrServer, true, nil},
		{"an internal error", chaos.Internal(), provider.ErrServer, true, nil},
		{"a proxy's bad gateway", chaos.BadGateway(), provider.ErrServer, true, nil},
		{"a rate limit", chaos.Limited(2 * time.Second), provider.ErrRateLimit, true, nil},
		{"a refused key", chaos.Refused(), provider.ErrAuth, false, nil},
		{"a reset before the answer", chaos.Fault{Kind: chaos.Reset}, provider.ErrNetwork, true, nil},
		{"a connection that dies mid-stream", chaos.Fault{Kind: chaos.Abort, After: 300}, provider.ErrNetwork, true, nil},
		{"a stream that ends without its last frame", chaos.Fault{Kind: chaos.Truncate, After: 300}, provider.ErrNetwork, true, nil},
		{"a server that says nothing", chaos.Fault{Kind: chaos.Stall, For: time.Hour}, provider.ErrTimeout, true,
			func(c *openaichat.Config) { c.FirstByteTimeout = 150 * time.Millisecond }},
		{"a stream that goes silent", chaos.Fault{Kind: chaos.StallMidStream, After: 300, For: time.Hour}, provider.ErrTimeout, true,
			func(c *openaichat.Config) { c.StreamIdleTimeout = 150 * time.Millisecond }},
		{"an HTML page with a 200", chaos.Fault{Kind: chaos.Garbage}, provider.ErrServer, true, nil},
	} {
		t.Run(c.name, func(t *testing.T) {
			resp, err, _ := provide(t, chaos.Script(c.fault), c.mutate)
			if err == nil {
				t.Fatalf("no error: the adapter returned %+v for an endpoint that did this", resp)
			}
			var pe *provider.Error
			if !errors.As(err, &pe) {
				t.Fatalf("%v is not a provider.Error", err)
			}
			if pe.Kind != c.kind || pe.Retryable() != c.retryable {
				t.Errorf("%v: kind %v retryable %v, want %v and %v", err, pe.Kind, pe.Retryable(), c.kind, c.retryable)
			}
			if c.fault.Kind == chaos.Status && c.fault.RetryAfter > 0 && pe.RetryAfter != c.fault.RetryAfter {
				t.Errorf("retry-after %v, want %v", pe.RetryAfter, c.fault.RetryAfter)
			}
			if resp != nil {
				t.Errorf("a failed request returned an answer too: %+v", resp)
			}
		})
	}
}

// A slow answer is not a failed one: a trickle that stays inside the idle timeout is read to the end.
func TestASlowStreamThatKeepsTalkingIsAnAnswer(t *testing.T) {
	resp, err, _ := provide(t, chaos.Script(chaos.Fault{Kind: chaos.Trickle, For: 200 * time.Microsecond}), func(c *openaichat.Config) { c.StreamIdleTimeout = 5 * time.Second })
	if err != nil || resp == nil {
		t.Fatalf("%v %v", resp, err)
	}
	if got := resp.Turn.PlainText(); !strings.Contains(got, "an answer, whole") {
		t.Fatalf("the answer: %q", got)
	}
}
