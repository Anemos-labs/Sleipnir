package openaichat

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/reee344/sleipnir/internal/provider"
)

func toolFrame(idx int, id, name, args string) string {
	return rawFrame(fmt.Sprintf(`{"id":"g","choices":[{"index":0,"delta":{"tool_calls":[{"index":%d,"id":%q,"type":"function","function":{"name":%q,"arguments":%q}}]}}]}`, idx, id, name, args))
}

func reasoningFrame(text string) string {
	return rawFrame(`{"id":"g","choices":[{"index":0,"delta":{"reasoning":"` + text + `"}}]}`)
}

func detailFrame(idx int, text string) string {
	return rawFrame(fmt.Sprintf(`{"id":"g","choices":[{"index":0,"delta":{"reasoning_details":[{"type":"reasoning.text","index":%d,"text":%q}]}}]}`, idx, text))
}

// limitCase streams from script under cfg and expects a provider error naming what.
func limitCase(t *testing.T, cfg Config, what string, script func(w http.ResponseWriter, fl http.Flusher, r *http.Request)) *scripted {
	t.Helper()
	s := newScripted(t, script)
	_, err := call(t, s.URL, cfg, nil)
	pe := wantProviderError(t, err, provider.ErrServer, true) // a misbehaving server: retried a bounded number of times
	if !strings.Contains(pe.Message, what) || !strings.Contains(pe.Message, "cancelled") {
		t.Fatalf("the error must name the limit (%q): %q", what, pe.Message)
	}
	return s
}

// streamForever writes frames from next until the client goes away.
func streamForever(next func(i int) string) func(w http.ResponseWriter, fl http.Flusher, r *http.Request) {
	return func(w http.ResponseWriter, fl http.Flusher, r *http.Request) {
		sse(w)
		for i := 0; r.Context().Err() == nil && i < 50_000_000; i++ {
			if _, err := fmt.Fprint(w, next(i)); err != nil {
				return
			}
		}
	}
}

func TestStreamLimitEndsTheRequestAndClosesTheConnection(t *testing.T) {
	// A server that streams for ever: the client must hang up on it, not just stop reading.
	s := limitCase(t, Config{Limits: provider.StreamLimits{MaxBytes: 1 << 20}}, "stream size", streamForever(func(int) string { return ": keep-alive padding padding\n" }))
	waitGone(t, s)
}

func TestEachStreamLimitTripsAndNamesItself(t *testing.T) {
	kb := strings.Repeat("y", 1024)
	for _, tc := range []struct {
		name   string
		limits provider.StreamLimits
		what   string
		next   func(i int) string
	}{
		{"total bytes", provider.StreamLimits{MaxBytes: 256 << 10}, "stream size", func(int) string { return frame(kb) }},
		{"line length", provider.StreamLimits{MaxLineBytes: 64 << 10}, "line length", func(int) string { return "data: " + strings.Repeat("z", 200<<10) }},
		{"events", provider.StreamLimits{MaxEvents: 500}, "500 events", func(int) string { return frame("a") }},
		{"answer text", provider.StreamLimits{MaxTextBytes: 100 << 10}, "answer text", func(int) string { return frame(kb) }},
		{"reasoning text", provider.StreamLimits{MaxTextBytes: 100 << 10}, "reasoning text", func(int) string { return reasoningFrame(kb) }},
		{"reasoning details", provider.StreamLimits{MaxTextBytes: 100 << 10}, "reasoning text", func(int) string { return detailFrame(0, kb) }},
		{"reasoning items", provider.StreamLimits{MaxBlocks: 50}, "50 reasoning items", func(i int) string { return detailFrame(i, "x") }},
		{"tool calls", provider.StreamLimits{MaxToolCalls: 10}, "10 tool calls", func(i int) string { return toolFrame(i, fmt.Sprint("c", i), "read", "{}") }},
		{"tool call arguments", provider.StreamLimits{MaxToolArgBytes: 64 << 10}, "tool call arguments", func(int) string { return toolFrame(0, "c", "write", kb) }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := limitCase(t, Config{Limits: tc.limits}, tc.what, streamForever(tc.next))
			waitGone(t, s)
		})
	}
}

// The default limits leave real responses alone: a large answer, a large tool call and
// a fan-out of parallel calls are all well inside them.
func TestDefaultLimitsAllowLargeLegitimateResponses(t *testing.T) {
	big := strings.Repeat("The quick brown fox jumps over the lazy dog. ", 25_000)            // ~1.1 MB of text
	args := `{"path":"a.txt","content":"` + strings.Repeat("line of code;\\n", 70_000) + `"}` // ~1 MB of arguments
	s := newScripted(t, func(w http.ResponseWriter, fl http.Flusher, r *http.Request) {
		sse(w)
		for off := 0; off < len(big); off += 4096 {
			fmt.Fprint(w, frame(big[off:min(off+4096, len(big))]))
		}
		for off := 0; off < len(args); off += 4096 {
			chunk := args[off:min(off+4096, len(args))]
			fmt.Fprint(w, rawFrame(fmt.Sprintf(`{"choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"id":"c0","function":{"name":"write","arguments":%q}}]}}]}`, chunk)))
		}
		for i := 1; i < 100; i++ { // 100 parallel calls in one turn
			fmt.Fprint(w, toolFrame(i, fmt.Sprint("c", i), "read", `{"path":"f.go"}`))
		}
		fmt.Fprint(w, finish("tool_calls"))
	})
	resp, err := call(t, s.URL, Config{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if got := resp.Turn.PlainText(); got != big {
		t.Fatalf("text was altered: %d bytes, want %d", len(got), len(big))
	}
	if calls := resp.Turn.ToolCalls(); len(calls) != 100 || calls[0].Invalid != "" {
		t.Fatalf("tool calls = %d (invalid %q)", len(calls), calls[0].Invalid)
	}
}

func TestLimitsAreInclusiveAtTheBoundary(t *testing.T) {
	run := func(n int) error {
		s := newScripted(t, func(w http.ResponseWriter, fl http.Flusher, r *http.Request) {
			sse(w)
			for i := 0; i < n; i++ {
				fmt.Fprint(w, toolFrame(i, fmt.Sprint("c", i), "read", "{}"))
			}
			fmt.Fprint(w, finish("tool_calls"))
		})
		_, err := call(t, s.URL, Config{Limits: provider.StreamLimits{MaxToolCalls: 5}}, nil)
		return err
	}
	if err := run(5); err != nil {
		t.Fatalf("exactly at the limit must work: %v", err)
	}
	if err := run(6); err == nil {
		t.Fatal("one past the limit must fail")
	}
	// Text exactly at the limit.
	s := newScripted(t, func(w http.ResponseWriter, fl http.Flusher, r *http.Request) {
		sse(w)
		fmt.Fprint(w, frame(strings.Repeat("a", 1000)), finish("stop"))
	})
	if _, err := call(t, s.URL, Config{Limits: provider.StreamLimits{MaxTextBytes: 1000}}, nil); err != nil {
		t.Fatalf("text exactly at the limit: %v", err)
	}
	if _, err := call(t, s.URL, Config{Limits: provider.StreamLimits{MaxTextBytes: 999}}, nil); err == nil {
		t.Fatal("text one byte over the limit must fail")
	}
}

func TestNegativeLimitTurnsOneOff(t *testing.T) {
	s := newScripted(t, func(w http.ResponseWriter, fl http.Flusher, r *http.Request) {
		sse(w)
		for i := 0; i < provider.DefaultMaxToolCalls+1; i++ {
			fmt.Fprint(w, toolFrame(i, fmt.Sprint("c", i), "read", "{}"))
		}
		fmt.Fprint(w, finish("tool_calls"))
	})
	if _, err := call(t, s.URL, Config{}, nil); err == nil {
		t.Fatalf("%d tool calls exceed the default limit", provider.DefaultMaxToolCalls+1)
	}
	resp, err := call(t, s.URL, Config{Limits: provider.StreamLimits{MaxToolCalls: -1}}, nil)
	if err != nil || len(resp.Turn.ToolCalls()) != provider.DefaultMaxToolCalls+1 {
		t.Fatalf("with the limit off: %v", err)
	}
}

// Text that has already been streamed to the caller when the limit trips is the
// caller's to discard: the agent does, on the EvReset that precedes a retry. What the
// adapter guarantees is that no response is returned.
func TestALimitedStreamReturnsNoResponse(t *testing.T) {
	s := newScripted(t, streamForever(func(int) string { return frame(strings.Repeat("q", 512)) }))
	var seen int
	resp, err := call(t, s.URL, Config{Limits: provider.StreamLimits{MaxTextBytes: 64 << 10}}, func(e provider.Event) {
		if e.Kind == provider.EvText {
			seen += len(e.Text)
		}
	})
	if resp != nil || err == nil {
		t.Fatalf("resp=%v err=%v", resp, err)
	}
	if seen == 0 || seen > 64<<10 {
		t.Fatalf("text events delivered before the limit: %d bytes", seen)
	}
}

func TestNonStreamingBodyIsBoundedAndItsContentToo(t *testing.T) {
	huge := newScripted(t, func(w http.ResponseWriter, fl http.Flusher, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"id":"g","choices":[{"message":{"role":"assistant","content":"`)
		for i := 0; i < 3000; i++ {
			fmt.Fprint(w, strings.Repeat("x", 1024))
		}
		fmt.Fprint(w, `"},"finish_reason":"stop"}]}`)
	})
	req := &provider.Request{Prompt: secRevPrompt("hi"), NoStream: true}
	_, err := New(Config{Name: "t", BaseURL: huge.URL, Limits: provider.StreamLimits{MaxBytes: 1 << 20}}).Do(context.Background(), req, nil)
	if pe := wantProviderError(t, err, provider.ErrServer, true); !strings.Contains(pe.Message, "response size") {
		t.Fatalf("%v", pe)
	}
	_, err = New(Config{Name: "t", BaseURL: huge.URL, Limits: provider.StreamLimits{MaxTextBytes: 1 << 20}}).Do(context.Background(), req, nil)
	if pe := wantProviderError(t, err, provider.ErrServer, true); !strings.Contains(pe.Message, "answer text") {
		t.Fatalf("%v", pe)
	}
	if _, err = New(Config{Name: "t", BaseURL: huge.URL}).Do(context.Background(), req, nil); err != nil {
		t.Fatalf("3 MB is inside the default limits: %v", err)
	}
}

// A server that trickles a byte now and then never trips the idle timeout; the
// overall deadline is what ends it.
func TestASlowDripIsCutOffByTheOverallDeadline(t *testing.T) {
	s := newScripted(t, func(w http.ResponseWriter, fl http.Flusher, r *http.Request) {
		sse(w)
		for r.Context().Err() == nil {
			if _, err := fmt.Fprint(w, ": drip\n"); err != nil {
				return
			}
			fl.Flush()
			time.Sleep(20 * time.Millisecond)
		}
	})
	start := time.Now()
	_, err := call(t, s.URL, Config{StreamIdleTimeout: time.Minute, Limits: provider.StreamLimits{MaxDuration: 250 * time.Millisecond}}, nil)
	pe := wantProviderError(t, err, provider.ErrTimeout, true)
	if d := time.Since(start); d < 200*time.Millisecond || d > 5*time.Second {
		t.Errorf("took %v", d)
	}
	if !strings.Contains(pe.Message, "still running after 250ms") {
		t.Errorf("message = %q", pe.Message)
	}
	waitGone(t, s)
}
