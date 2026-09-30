package anthropic_test

// Transport hardening of the Messages adapter: the same findings the chat adapter
// fixed (docs/reviews/security-robustness.md S27, S28, S30, S31, S45, and C-08 of the
// concurrency review): redirects, usage and cost figures, response limits, error text,
// key transport and time-to-first-byte.

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
	"sync/atomic"
	"testing"
	"time"

	"github.com/reee344/sleipnir/internal/core"
	"github.com/reee344/sleipnir/internal/provider"
	"github.com/reee344/sleipnir/internal/provider/anthropic"
)

// Hostile characters are built at run time so that none sits in this file.
var (
	bidi = string(rune(0x202e))
	zwsp = string(rune(0x200b))
)

const attack = "\x1b]52;c;ZXZpbA==\a\x1b[2J\x1b[31m"

func evt(name, data string) string { return "event: " + name + "\ndata: " + data + "\n\n" }

func startMsg(usage string) string {
	return evt("message_start", `{"type":"message_start","message":{"id":"msg_x","type":"message","role":"assistant","model":"m","content":[],"usage":`+usage+`}}`)
}

func blockStart(idx int, block string) string {
	return evt("content_block_start", fmt.Sprintf(`{"type":"content_block_start","index":%d,"content_block":%s}`, idx, block))
}

func delta(idx int, d string) string {
	return evt("content_block_delta", fmt.Sprintf(`{"type":"content_block_delta","index":%d,"delta":%s}`, idx, d))
}

func textDelta(idx int, s string) string {
	b, _ := json.Marshal(s)
	return delta(idx, `{"type":"text_delta","text":`+string(b)+`}`)
}

func jsonDelta(idx int, s string) string {
	b, _ := json.Marshal(s)
	return delta(idx, `{"type":"input_json_delta","partial_json":`+string(b)+`}`)
}

func stop(idx int) string {
	return evt("content_block_stop", fmt.Sprintf(`{"type":"content_block_stop","index":%d}`, idx))
}

func msgDelta(reason, usage string) string {
	return evt("message_delta", `{"type":"message_delta","delta":{"stop_reason":"`+reason+`"},"usage":`+usage+`}`)
}

const msgStop = "event: message_stop\ndata: {\"type\":\"message_stop\"}\n\n"

func okStream(text string) string {
	return startMsg(`{"input_tokens":3,"output_tokens":1}`) + blockStart(0, `{"type":"text","text":""}`) + textDelta(0, text) + stop(0) +
		msgDelta("end_turn", `{"output_tokens":2}`) + msgStop
}

func doStream(t *testing.T, url string, cfg anthropic.Config) (*provider.Response, error) {
	t.Helper()
	cfg.BaseURL = url
	return anthropic.New(cfg).Do(context.Background(), &provider.Request{Prompt: hello("m")}, nil)
}

func hang(w http.ResponseWriter, fl http.Flusher, r *http.Request) { <-r.Context().Done() }

// ---- S27: redirects --------------------------------------------------------------------------------

type collector struct {
	*httptest.Server
	mu   sync.Mutex
	hits int
	body string
	hdr  http.Header
	URL  string
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
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, okStream("leaked"))
	}))
	t.Cleanup(c.Server.Close)
	c.URL = strings.Replace(c.Server.URL, "127.0.0.1", "localhost", 1)
	return c
}

func (c *collector) saw() (int, string, http.Header) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.hits, c.body, c.hdr
}

func TestRedirectToAnotherOriginIsRefusedWithoutFollowing(t *testing.T) {
	for _, status := range []int{301, 302, 303, 307, 308} {
		for _, noStream := range []bool{false, true} {
			t.Run(fmt.Sprintf("%d/nostream=%v", status, noStream), func(t *testing.T) {
				target := newCollector(t)
				src := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					http.Redirect(w, r, target.URL+"/collect?leak=1", status)
				}))
				defer src.Close()
				c := anthropic.New(anthropic.Config{
					BaseURL: src.URL, APIKey: "test-key-not-a-secret",
					Headers: map[string]string{"X-Org": "acme", "X-Custom-Credential": "custom-secret"},
				})
				resp, err := c.Do(context.Background(), &provider.Request{Prompt: hello("m"), NoStream: noStream}, nil)
				if resp != nil {
					t.Fatalf("a response came back from a redirect target: %+v", resp)
				}
				pe := wantKind(t, err, provider.ErrBadRequest, false)
				if !pe.NoRetry || !strings.Contains(pe.Message, "another origin") || !strings.Contains(pe.Message, "localhost:") {
					t.Errorf("the refusal must name the target host and not be retried: %+v", pe)
				}
				if strings.Contains(pe.Message, "leak=1") || strings.Contains(pe.Message, "/collect") {
					t.Errorf("the refusal must not echo the redirect's path or query: %q", pe.Message)
				}
				if hits, body, hdr := target.saw(); hits != 0 || body != "" || hdr != nil {
					t.Fatalf("the redirect target received a request: hits=%d body=%q headers=%v", hits, body, hdr)
				}
			})
		}
	}
}

func TestSameOriginRedirectIsFollowedWithItsBodyAndHeaders(t *testing.T) {
	var mu sync.Mutex
	var gotBody, gotKey string
	mux := http.NewServeMux()
	mux.HandleFunc("/v1/messages", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/v2/messages", http.StatusTemporaryRedirect)
	})
	mux.HandleFunc("/v2/messages", func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		mu.Lock()
		gotBody, gotKey = string(b), r.Header.Get("X-Api-Key")
		mu.Unlock()
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, okStream("followed"))
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()
	resp, err := doStream(t, srv.URL, anthropic.Config{APIKey: "test-key-not-a-secret"})
	if err != nil || resp.Turn.PlainText() != "followed" {
		t.Fatalf("a same-origin redirect must work: %v %v", resp, err)
	}
	mu.Lock()
	defer mu.Unlock()
	if !strings.Contains(gotBody, `"model":"m"`) || gotKey != "test-key-not-a-secret" {
		t.Fatalf("the redirected request lost its body or key: %q %q", gotBody, gotKey)
	}
}

func TestSuppliedHTTPClientCannotFollowRedirectsAcrossOrigins(t *testing.T) {
	target := newCollector(t)
	src := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target.URL, http.StatusTemporaryRedirect)
	}))
	defer src.Close()
	permissive := &http.Client{Timeout: 5 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return nil }}
	_, err := doStream(t, src.URL, anthropic.Config{APIKey: "k", HTTPClient: permissive})
	wantKind(t, err, provider.ErrBadRequest, false)
	if hits, _, _ := target.saw(); hits != 0 {
		t.Fatalf("the redirect target was contacted %d time(s)", hits)
	}
}

// ---- S28: usage ------------------------------------------------------------------------------------

func TestUsageCountersAreClamped(t *testing.T) {
	for name, tc := range map[string]struct {
		start, delta string
		want         core.Usage
	}{
		"negative counters": {
			`{"input_tokens":-100,"cache_creation_input_tokens":-1,"cache_read_input_tokens":-9,"output_tokens":-5000000}`,
			`{"output_tokens":-7}`,
			core.Usage{},
		},
		"absurd counters are capped": {
			`{"input_tokens":9223372036854775807,"cache_read_input_tokens":1e30,"output_tokens":1}`,
			`{"output_tokens":99999999999999999999}`,
			core.Usage{InputTokens: provider.MaxUsageTokens, CacheReadTokens: provider.MaxUsageTokens, OutputTokens: provider.MaxUsageTokens},
		},
		"counters that are not numbers do not drop the report": {
			`{"input_tokens":"lots","cache_read_input_tokens":null,"output_tokens":1}`,
			`{"output_tokens":42,"cache_creation":"weird"}`,
			core.Usage{OutputTokens: 42},
		},
		"reasoning cannot exceed the output": {
			`{"input_tokens":10,"output_tokens":1}`,
			`{"output_tokens":20,"output_tokens_details":{"thinking_tokens":99999999}}`,
			core.Usage{InputTokens: 10, OutputTokens: 20, ReasoningTokens: 20},
		},
		"an ordinary report": {
			`{"input_tokens":25,"cache_read_input_tokens":100,"output_tokens":1}`,
			`{"output_tokens":6}`,
			core.Usage{InputTokens: 25, CacheReadTokens: 100, OutputTokens: 6},
		},
	} {
		t.Run(name, func(t *testing.T) {
			s := newSlow(t, func(w http.ResponseWriter, fl http.Flusher, r *http.Request) {
				sseHeaders(w)
				fmt.Fprint(w, startMsg(tc.start), blockStart(0, `{"type":"text","text":""}`), textDelta(0, "answer"), stop(0), msgDelta("end_turn", tc.delta), msgStop)
			})
			resp, err := doStream(t, s.URL, anthropic.Config{})
			if err != nil {
				t.Fatal(err)
			}
			if resp.Turn.PlainText() != "answer" {
				t.Fatalf("the content was lost with the usage report: %q", resp.Turn.PlainText())
			}
			if resp.Usage != tc.want {
				t.Fatalf("usage = %+v, want %+v", resp.Usage, tc.want)
			}
			if resp.CostUSD != nil {
				t.Errorf("this adapter reports no gateway cost, got %v", *resp.CostUSD)
			}
			var sum core.Usage
			for i := 0; i < 100_000; i++ {
				sum = sum.Add(resp.Usage)
			}
			if sum.InputTokens < 0 || sum.OutputTokens < 0 || sum.CacheReadTokens < 0 {
				t.Fatalf("the counters overflow an accumulator: %+v", sum)
			}
		})
	}
}

func TestNonStreamingHostileUsageIsClampedToo(t *testing.T) {
	s := newSlow(t, func(w http.ResponseWriter, fl http.Flusher, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"id":"m","type":"message","role":"assistant","model":"m","content":[{"type":"text","text":"x"}],"stop_reason":"end_turn","usage":{"input_tokens":-1,"cache_creation_input_tokens":1e40,"output_tokens":-3}}`)
	})
	c := anthropic.New(anthropic.Config{BaseURL: s.URL})
	resp, err := c.Do(context.Background(), &provider.Request{Prompt: hello("m"), NoStream: true}, nil)
	if err != nil {
		t.Fatal(err)
	}
	u := resp.Usage
	if u.InputTokens != 0 || u.OutputTokens != 0 || u.CacheWriteTokens() != provider.MaxUsageTokens {
		t.Fatalf("usage = %+v", u)
	}
	// The audit record keeps what the server sent.
	if !strings.Contains(string(resp.RawUsage), "1e40") {
		t.Fatalf("RawUsage = %s", resp.RawUsage)
	}
}

// ---- S30: response limits ---------------------------------------------------------------------------

func flood(next func(i int) string) func(http.ResponseWriter, http.Flusher, *http.Request) {
	return func(w http.ResponseWriter, fl http.Flusher, r *http.Request) {
		sseHeaders(w)
		fmt.Fprint(w, startMsg(`{"input_tokens":1,"output_tokens":1}`), blockStart(0, `{"type":"text","text":""}`))
		for i := 0; r.Context().Err() == nil && i < 50_000_000; i++ {
			if _, err := fmt.Fprint(w, next(i)); err != nil {
				return
			}
		}
	}
}

func waitGone(t *testing.T, s *slowServer) {
	t.Helper()
	select {
	case <-s.gone:
	case <-time.After(5 * time.Second):
		t.Fatal("the connection was not closed: the server never saw the client go away")
	}
}

func TestEachStreamLimitTripsAndClosesTheConnection(t *testing.T) {
	kb := strings.Repeat("y", 1024)
	for _, tc := range []struct {
		name   string
		limits provider.StreamLimits
		what   string
		script func(http.ResponseWriter, http.Flusher, *http.Request)
	}{
		{"total bytes", provider.StreamLimits{MaxBytes: 256 << 10}, "stream size", flood(func(int) string { return ": padding padding padding\n" })},
		{"line length", provider.StreamLimits{MaxLineBytes: 64 << 10}, "line length", flood(func(int) string { return "data: " + strings.Repeat("z", 200<<10) })},
		{"events", provider.StreamLimits{MaxEvents: 300}, "300 events", flood(func(int) string { return textDelta(0, "a") })},
		{"answer text", provider.StreamLimits{MaxTextBytes: 100 << 10}, "answer text", flood(func(int) string { return textDelta(0, kb) })},
		{"reasoning text", provider.StreamLimits{MaxTextBytes: 100 << 10}, "reasoning text", func(w http.ResponseWriter, fl http.Flusher, r *http.Request) {
			sseHeaders(w)
			fmt.Fprint(w, startMsg(`{"input_tokens":1,"output_tokens":1}`), blockStart(0, `{"type":"thinking","thinking":"","signature":""}`))
			for i := 0; r.Context().Err() == nil && i < 1_000_000; i++ {
				fmt.Fprint(w, delta(0, `{"type":"thinking_delta","thinking":"`+kb+`"}`))
			}
		}},
		{"signature", provider.StreamLimits{MaxTextBytes: 100 << 10}, "reasoning text", func(w http.ResponseWriter, fl http.Flusher, r *http.Request) {
			sseHeaders(w)
			fmt.Fprint(w, startMsg(`{"input_tokens":1,"output_tokens":1}`), blockStart(0, `{"type":"thinking","thinking":"","signature":""}`))
			for i := 0; r.Context().Err() == nil && i < 1_000_000; i++ {
				fmt.Fprint(w, delta(0, `{"type":"signature_delta","signature":"`+kb+`"}`))
			}
		}},
		{"tool arguments", provider.StreamLimits{MaxToolArgBytes: 64 << 10}, "tool call arguments", func(w http.ResponseWriter, fl http.Flusher, r *http.Request) {
			sseHeaders(w)
			fmt.Fprint(w, startMsg(`{"input_tokens":1,"output_tokens":1}`), blockStart(0, `{"type":"tool_use","id":"t1","name":"write","input":{}}`))
			for i := 0; r.Context().Err() == nil && i < 1_000_000; i++ {
				fmt.Fprint(w, jsonDelta(0, kb))
			}
		}},
		{"tool calls", provider.StreamLimits{MaxToolCalls: 10}, "10 tool calls", func(w http.ResponseWriter, fl http.Flusher, r *http.Request) {
			sseHeaders(w)
			fmt.Fprint(w, startMsg(`{"input_tokens":1,"output_tokens":1}`))
			for i := 0; r.Context().Err() == nil && i < 1_000_000; i++ {
				fmt.Fprint(w, blockStart(i, fmt.Sprintf(`{"type":"tool_use","id":"t%d","name":"read","input":{}}`, i)))
			}
		}},
		{"content blocks", provider.StreamLimits{MaxBlocks: 40}, "40 content blocks", func(w http.ResponseWriter, fl http.Flusher, r *http.Request) {
			sseHeaders(w)
			fmt.Fprint(w, startMsg(`{"input_tokens":1,"output_tokens":1}`))
			for i := 0; r.Context().Err() == nil && i < 1_000_000; i++ {
				fmt.Fprint(w, blockStart(i, `{"type":"text","text":""}`))
			}
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := newSlow(t, tc.script)
			_, err := doStream(t, s.URL, anthropic.Config{Limits: tc.limits})
			pe := wantKind(t, err, provider.ErrServer, true) // a misbehaving server: retried a bounded number of times
			if !strings.Contains(pe.Message, tc.what) || !strings.Contains(pe.Message, "cancelled") {
				t.Fatalf("the error must name the limit (%q): %q", tc.what, pe.Message)
			}
			waitGone(t, s)
		})
	}
}

func TestDefaultLimitsAllowLargeLegitimateResponses(t *testing.T) {
	big := strings.Repeat("The quick brown fox jumps over the lazy dog. ", 25_000) // ~1.1 MB
	args := `{"path":"a.txt","content":"` + strings.Repeat("line of code;\\n", 70_000) + `"}`
	s := newSlow(t, func(w http.ResponseWriter, fl http.Flusher, r *http.Request) {
		sseHeaders(w)
		fmt.Fprint(w, startMsg(`{"input_tokens":1,"output_tokens":1}`), blockStart(0, `{"type":"text","text":""}`))
		for off := 0; off < len(big); off += 4096 {
			fmt.Fprint(w, textDelta(0, big[off:min(off+4096, len(big))]))
		}
		fmt.Fprint(w, stop(0), blockStart(1, `{"type":"tool_use","id":"t1","name":"write","input":{}}`))
		for off := 0; off < len(args); off += 4096 {
			fmt.Fprint(w, jsonDelta(1, args[off:min(off+4096, len(args))]))
		}
		fmt.Fprint(w, stop(1))
		for i := 2; i < 100; i++ { // parallel calls
			fmt.Fprint(w, blockStart(i, fmt.Sprintf(`{"type":"tool_use","id":"t%d","name":"read","input":{}}`, i)), jsonDelta(i, `{"path":"f.go"}`), stop(i))
		}
		fmt.Fprint(w, msgDelta("tool_use", `{"output_tokens":9}`), msgStop)
	})
	resp, err := doStream(t, s.URL, anthropic.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if got := resp.Turn.PlainText(); got != big {
		t.Fatalf("text was altered: %d bytes, want %d", len(got), len(big))
	}
	if calls := resp.Turn.ToolCalls(); len(calls) != 99 || calls[0].Invalid != "" {
		t.Fatalf("tool calls = %d", len(calls))
	}
}

func TestLimitsAreInclusiveAtTheBoundary(t *testing.T) {
	run := func(n int, lim provider.StreamLimits) error {
		s := newSlow(t, func(w http.ResponseWriter, fl http.Flusher, r *http.Request) {
			sseHeaders(w)
			fmt.Fprint(w, startMsg(`{"input_tokens":1,"output_tokens":1}`))
			for i := 0; i < n; i++ {
				fmt.Fprint(w, blockStart(i, fmt.Sprintf(`{"type":"tool_use","id":"t%d","name":"read","input":{}}`, i)), stop(i))
			}
			fmt.Fprint(w, msgDelta("tool_use", `{"output_tokens":1}`), msgStop)
		})
		_, err := doStream(t, s.URL, anthropic.Config{Limits: lim})
		return err
	}
	if err := run(5, provider.StreamLimits{MaxToolCalls: 5}); err != nil {
		t.Fatalf("exactly at the limit must work: %v", err)
	}
	if err := run(6, provider.StreamLimits{MaxToolCalls: 5}); err == nil {
		t.Fatal("one past the limit must fail")
	}
	if err := run(300, provider.StreamLimits{MaxToolCalls: -1, MaxBlocks: -1}); err != nil {
		t.Fatalf("with the limits off: %v", err)
	}
}

func TestNonStreamingBodyAndContentAreBounded(t *testing.T) {
	huge := newSlow(t, func(w http.ResponseWriter, fl http.Flusher, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"id":"m","type":"message","model":"m","stop_reason":"end_turn","content":[{"type":"text","text":"`)
		for i := 0; i < 3000; i++ {
			fmt.Fprint(w, strings.Repeat("x", 1024))
		}
		fmt.Fprint(w, `"}],"usage":{}}`)
	})
	req := &provider.Request{Prompt: hello("m"), NoStream: true}
	_, err := anthropic.New(anthropic.Config{BaseURL: huge.URL, Limits: provider.StreamLimits{MaxBytes: 1 << 20}}).Do(context.Background(), req, nil)
	if pe := wantKind(t, err, provider.ErrServer, true); !strings.Contains(pe.Message, "response size") {
		t.Fatalf("%v", pe)
	}
	_, err = anthropic.New(anthropic.Config{BaseURL: huge.URL, Limits: provider.StreamLimits{MaxTextBytes: 1 << 20}}).Do(context.Background(), req, nil)
	if pe := wantKind(t, err, provider.ErrServer, true); !strings.Contains(pe.Message, "answer text") {
		t.Fatalf("%v", pe)
	}
	if _, err = anthropic.New(anthropic.Config{BaseURL: huge.URL}).Do(context.Background(), req, nil); err != nil {
		t.Fatalf("3 MB is inside the default limits: %v", err)
	}
}

func TestASlowDripIsCutOffByTheOverallDeadline(t *testing.T) {
	s := newSlow(t, func(w http.ResponseWriter, fl http.Flusher, r *http.Request) {
		sseHeaders(w)
		for r.Context().Err() == nil {
			if _, err := fmt.Fprint(w, "event: ping\ndata: {\"type\": \"ping\"}\n\n"); err != nil {
				return
			}
			fl.Flush()
			time.Sleep(20 * time.Millisecond)
		}
	})
	start := time.Now()
	_, err := doStream(t, s.URL, anthropic.Config{StreamIdleTimeout: time.Minute, Limits: provider.StreamLimits{MaxDuration: 250 * time.Millisecond}})
	pe := wantKind(t, err, provider.ErrTimeout, true)
	if d := time.Since(start); d < 200*time.Millisecond || d > 5*time.Second || !strings.Contains(pe.Message, "still running after 250ms") {
		t.Fatalf("took %v: %q", d, pe.Message)
	}
	waitGone(t, s)
}

// A signature that a gateway chunked reassembles (the API itself sends it whole). The
// pieces differ from each other: a chunk that repeats the signature so far is read as a
// cumulative report, which is the adapter's documented tolerance.
func TestChunkedSignatureIsReassembled(t *testing.T) {
	var sig strings.Builder
	for i := 0; i < 20_000; i++ {
		sig.WriteByte(byte('A' + i%26))
	}
	want := sig.String()
	s := newSlow(t, func(w http.ResponseWriter, fl http.Flusher, r *http.Request) {
		sseHeaders(w)
		fmt.Fprint(w, startMsg(`{"input_tokens":1,"output_tokens":1}`), blockStart(0, `{"type":"thinking","thinking":"","signature":""}`))
		for i := 0; i < len(want); i++ {
			fmt.Fprint(w, delta(0, `{"type":"signature_delta","signature":"`+want[i:i+1]+`"}`))
		}
		fmt.Fprint(w, stop(0), msgDelta("end_turn", `{"output_tokens":1}`), msgStop)
	})
	resp, err := doStream(t, s.URL, anthropic.Config{Limits: provider.StreamLimits{MaxEvents: -1}})
	if err != nil {
		t.Fatal(err)
	}
	if got := string(resp.Turn.Blocks[0].Wire); !strings.Contains(got, `"signature":"`+want+`"`) {
		t.Fatalf("the signature was not reassembled (%d bytes of wire)", len(got))
	}
}

// ---- S31: error text ---------------------------------------------------------------------------------

func cleanText(t *testing.T, s string) {
	t.Helper()
	if len(s) > provider.MaxErrorText+256 {
		t.Errorf("text of %d bytes", len(s))
	}
	for _, bad := range []string{"\x1b", "\a", "\n", "\r", bidi, zwsp} {
		if strings.Contains(s, bad) {
			t.Errorf("text carries %q: %q", bad, s[:min(len(s), 120)])
		}
	}
}

func TestErrorTextIsCappedAndSanitised(t *testing.T) {
	msg := "bad" + attack + bidi + zwsp + strings.Repeat("x", 900_000)
	for name, tc := range map[string]struct {
		status  int
		body    string
		headers http.Header
	}{
		"api error body":     {400, jsonErr("invalid_request_error", msg), nil},
		"huge api error":     {500, jsonErr("api_error", strings.Repeat("y", 5<<20)), nil},
		"plain text":         {502, "<html>" + attack + strings.Repeat("z", 100_000), nil},
		"with newlines":      {503, "line one\nsleipnir: session ok\r\n" + attack, nil},
		"hostile request id": {500, jsonErr("api_error", "boom"), http.Header{"Request-Id": {"req_" + attack + strings.Repeat("i", 5000)}}},
		"error body is null": {400, `{"error":null}`, nil},
	} {
		t.Run(name, func(t *testing.T) {
			ts := serveError(t, tc.status, tc.headers, tc.body)
			err := doErr(t, ts, anthropic.Config{})
			pe, ok := provider.AsError(err)
			if !ok {
				t.Fatal(err)
			}
			cleanText(t, pe.Message)
			cleanText(t, pe.Error())
			if len(pe.Raw) > provider.MaxRawBytes {
				t.Errorf("raw is %d bytes", len(pe.Raw))
			}
		})
	}
}

func TestErrorClassificationSurvivesSanitising(t *testing.T) {
	ts := serveError(t, 400, nil, jsonErr("invalid_request_error", "prompt is too "+zwsp+"long: 250000 tokens"+attack))
	wantKind(t, doErr(t, ts, anthropic.Config{}), provider.ErrContextLength, false)
}

func TestErrorsInsideAStreamAreSanitised(t *testing.T) {
	for name, script := range map[string]string{
		"error event":                     startMsg(`{"input_tokens":1,"output_tokens":1}`) + evt("error", `{"type":"error","error":{"type":"overloaded_error","message":`+jsonString("Overloaded"+attack+strings.Repeat("p", 80_000))+`}}`),
		"malformed event":                 startMsg(`{"input_tokens":1,"output_tokens":1}`) + "event: content_block_delta\ndata: {" + attack + strings.Repeat("{", 100) + "\n\n",
		"malformed message":               "event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"id\":[" + attack + "}}\n\n",
		"malformed block start":           startMsg(`{"input_tokens":1,"output_tokens":1}`) + "event: content_block_start\ndata: {\"type\":\"content_block_start\",\"index\":0,\"content_block\":[1," + attack + "]}\n\n",
		"an event name that is an attack": startMsg(`{"input_tokens":1,"output_tokens":1}`) + "event: content_block_delta" + attack + "\ndata: {\n\n",
	} {
		t.Run(name, func(t *testing.T) {
			s := newSlow(t, func(w http.ResponseWriter, fl http.Flusher, r *http.Request) {
				sseHeaders(w)
				fmt.Fprint(w, script)
			})
			_, err := doStream(t, s.URL, anthropic.Config{})
			pe, ok := provider.AsError(err)
			if !ok {
				t.Fatalf("%v", err)
			}
			cleanText(t, pe.Message)
			cleanText(t, err.Error())
			if len(pe.Raw) > provider.MaxRawBytes {
				t.Errorf("raw is %d bytes", len(pe.Raw))
			}
		})
	}
	t.Run("in-band error in a non-streaming body", func(t *testing.T) {
		s := newSlow(t, func(w http.ResponseWriter, fl http.Flusher, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprint(w, jsonErr("overloaded_error", "busy"+attack))
		})
		_, err := anthropic.New(anthropic.Config{BaseURL: s.URL}).Do(context.Background(), &provider.Request{Prompt: hello("m"), NoStream: true}, nil)
		cleanText(t, wantKind(t, err, provider.ErrOverloaded, true).Message)
	})
	t.Run("unparseable body", func(t *testing.T) {
		s := newSlow(t, func(w http.ResponseWriter, fl http.Flusher, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprint(w, "<html>"+attack+strings.Repeat("h", 200_000))
		})
		_, err := anthropic.New(anthropic.Config{BaseURL: s.URL}).Do(context.Background(), &provider.Request{Prompt: hello("m"), NoStream: true}, nil)
		pe := wantKind(t, err, provider.ErrServer, true)
		cleanText(t, pe.Message)
		if len(pe.Raw) > 4096 {
			t.Errorf("raw is %d bytes", len(pe.Raw))
		}
	})
	t.Run("identifiers the server chose", func(t *testing.T) {
		s := newSlow(t, func(w http.ResponseWriter, fl http.Flusher, r *http.Request) {
			sseHeaders(w)
			fmt.Fprint(w, evt("message_start", `{"type":"message_start","message":{"id":`+jsonString("msg"+attack+strings.Repeat("i", 5000))+`,"model":`+jsonString("m"+bidi+"odel")+`,"content":[],"usage":{}}}`)+
				blockStart(0, `{"type":"text","text":""}`)+textDelta(0, "hi")+stop(0)+msgDelta("end_turn", `{}`)+msgStop)
		})
		resp, err := doStream(t, s.URL, anthropic.Config{})
		if err != nil {
			t.Fatal(err)
		}
		for _, v := range []string{resp.ID, resp.Model, resp.Turn.Model} {
			cleanText(t, v)
			if len(v) > 256 {
				t.Errorf("identifier of %d bytes", len(v))
			}
		}
		if resp.Model != "model" {
			t.Errorf("model %q", resp.Model)
		}
	})
}

func jsonString(s string) string { b, _ := json.Marshal(s); return string(b) }

// jsonErr is the API's error body with properly JSON-encoded text: apiErr formats with %q,
// which writes control characters in Go syntax that is not valid JSON.
func jsonErr(typ, msg string) string {
	return `{"type":"error","error":{"type":` + jsonString(typ) + `,"message":` + jsonString(msg) + `},"request_id":"req_fixture_1"}`
}

// ---- S45 (transport half): the key ------------------------------------------------------------------

type stubTransport struct {
	mu   sync.Mutex
	seen []*http.Request
}

func (s *stubTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	s.mu.Lock()
	s.seen = append(s.seen, r)
	s.mu.Unlock()
	return &http.Response{
		StatusCode: 200, Request: r, Header: http.Header{"Content-Type": {"text/event-stream"}},
		Body: io.NopCloser(strings.NewReader(okStream("ok"))),
	}, nil
}

func refuseAll(t *testing.T) *http.Client {
	t.Helper()
	return &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		t.Errorf("a request was sent to %s", r.URL)
		return nil, errors.New("blocked")
	})}
}

func TestAPIKeyIsNeverSentOverPlainHTTPToAnotherHost(t *testing.T) {
	for _, style := range []string{"", "bearer"} {
		for _, url := range []string{
			"http://collector.attacker.example",
			"http://api.anthropic.com",
			"http://10.1.2.3:8000/api",
			"http://user:pw@collector.attacker.example/v1",
			"http://localhost.attacker.example",
		} {
			t.Run(style+"/"+url, func(t *testing.T) {
				c := anthropic.New(anthropic.Config{BaseURL: url, APIKey: "test-key-not-a-secret", AuthStyle: style, HTTPClient: refuseAll(t)})
				_, err := c.Do(context.Background(), &provider.Request{Prompt: hello("m")}, nil)
				pe := wantKind(t, err, provider.ErrBadRequest, false)
				if !pe.NoRetry || !strings.Contains(pe.Message, "plain http") || !strings.Contains(pe.Message, "allow_insecure_http") {
					t.Errorf("the refusal must say what to do: %+v", pe)
				}
				if strings.Contains(pe.Message, "test-key-not-a-secret") || strings.Contains(pe.Message, "pw") {
					t.Errorf("the refusal leaks a credential: %q", pe.Message)
				}
			})
		}
	}
}

func TestAPIKeyMayGoToLoopbackAndToHTTPS(t *testing.T) {
	var gotKey atomic.Value
	s := newSlow(t, func(w http.ResponseWriter, fl http.Flusher, r *http.Request) {
		gotKey.Store(r.Header.Get("X-Api-Key"))
		sseHeaders(w)
		fmt.Fprint(w, okStream("ok"))
	})
	if _, err := doStream(t, s.URL, anthropic.Config{APIKey: "test-key-not-a-secret"}); err != nil || gotKey.Load() != "test-key-not-a-secret" {
		t.Fatalf("loopback http must work with a key: %v (%v)", err, gotKey.Load())
	}
	stub := &stubTransport{}
	for _, url := range []string{"https://api.anthropic.com", "https://gateway.example:8443/anthropic/v1", "http://localhost:8000", "http://[::1]:9", "http://127.9.9.9"} {
		stub.seen = nil
		c := anthropic.New(anthropic.Config{BaseURL: url, APIKey: "test-key-not-a-secret", HTTPClient: &http.Client{Transport: stub}})
		if _, err := c.Do(context.Background(), &provider.Request{Prompt: hello("m")}, nil); err != nil {
			t.Errorf("%s: %v", url, err)
			continue
		}
		if len(stub.seen) != 1 || stub.seen[0].Header.Get("X-Api-Key") != "test-key-not-a-secret" {
			t.Errorf("%s: request not sent with the key", url)
		}
	}
}

func TestPlainHTTPWithoutAKeyAndTheDeliberateException(t *testing.T) {
	stub := &stubTransport{}
	c := anthropic.New(anthropic.Config{BaseURL: "http://gpu-box.lan:8000", HTTPClient: &http.Client{Transport: stub}})
	if _, err := c.Do(context.Background(), &provider.Request{Prompt: hello("m")}, nil); err != nil || len(stub.seen) != 1 {
		t.Fatalf("a keyless local server over http is fine: %v", err)
	}
	stub = &stubTransport{}
	c = anthropic.New(anthropic.Config{BaseURL: "http://gpu-box.lan:8000", APIKey: "test-key-not-a-secret", AllowInsecureHTTP: true, HTTPClient: &http.Client{Transport: stub}})
	if _, err := c.Do(context.Background(), &provider.Request{Prompt: hello("m")}, nil); err != nil || len(stub.seen) != 1 {
		t.Fatalf("AllowInsecureHTTP: %v", err)
	}
}

func TestClientStringRedactsTheBaseURL(t *testing.T) {
	c := anthropic.New(anthropic.Config{Name: "gw", BaseURL: "https://user:hunter2@gateway.example/anthropic?token=abc", APIKey: "test-key-not-a-secret"})
	s := c.String()
	for _, leak := range []string{"hunter2", "user", "token=abc", "test-key-not-a-secret"} {
		if strings.Contains(s, leak) {
			t.Errorf("String() leaks %q: %s", leak, s)
		}
	}
}

// ---- C-08: time to first byte ------------------------------------------------------------------------

func TestASilentServerEndsTheRequestAtTheFirstByteDeadline(t *testing.T) {
	s := newSlow(t, hang)
	start := time.Now()
	_, err := doStream(t, s.URL, anthropic.Config{FirstByteTimeout: 150 * time.Millisecond, StreamIdleTimeout: time.Hour})
	pe := wantKind(t, err, provider.ErrTimeout, true)
	if d := time.Since(start); d < 120*time.Millisecond || d > 3*time.Second || !strings.Contains(pe.Message, "no response from the server within 150ms") {
		t.Fatalf("took %v: %q", d, pe.Message)
	}
	waitGone(t, s)
}

func TestHeadersAloneDoNotEndTheFirstByteWait(t *testing.T) {
	s := newSlow(t, func(w http.ResponseWriter, fl http.Flusher, r *http.Request) {
		sseHeaders(w)
		fl.Flush()
		time.Sleep(350 * time.Millisecond) // longer than idle (100ms), shorter than first byte (2s)
		fmt.Fprint(w, okStream("late but fine"))
	})
	resp, err := doStream(t, s.URL, anthropic.Config{FirstByteTimeout: 2 * time.Second, StreamIdleTimeout: 100 * time.Millisecond})
	if err != nil || resp.Turn.PlainText() != "late but fine" {
		t.Fatalf("a slow start inside the first-byte deadline must work: %v %v", resp, err)
	}
	s2 := newSlow(t, func(w http.ResponseWriter, fl http.Flusher, r *http.Request) {
		sseHeaders(w)
		fl.Flush()
		<-r.Context().Done()
	})
	start := time.Now()
	_, err = doStream(t, s2.URL, anthropic.Config{FirstByteTimeout: 250 * time.Millisecond, StreamIdleTimeout: time.Hour})
	pe := wantKind(t, err, provider.ErrTimeout, true)
	if d := time.Since(start); d < 200*time.Millisecond || d > 3*time.Second || !strings.Contains(pe.Message, "no response") {
		t.Fatalf("headers then silence: took %v, %q", d, pe.Message)
	}
}

func TestIdleTimeoutAloneBoundsTheWaitForTheFirstByte(t *testing.T) {
	s := newSlow(t, hang)
	start := time.Now()
	_, err := doStream(t, s.URL, anthropic.Config{StreamIdleTimeout: 150 * time.Millisecond})
	wantKind(t, err, provider.ErrTimeout, true)
	if d := time.Since(start); d > 3*time.Second {
		t.Errorf("took %v", d)
	}
}

func TestAStreamThatGoesSilentAfterItsFirstByteIsAnIdleTimeout(t *testing.T) {
	s := newSlow(t, func(w http.ResponseWriter, fl http.Flusher, r *http.Request) {
		sseHeaders(w)
		fmt.Fprint(w, startEvent)
		fl.Flush()
		<-r.Context().Done()
	})
	start := time.Now()
	_, err := doStream(t, s.URL, anthropic.Config{FirstByteTimeout: 30 * time.Second, StreamIdleTimeout: 150 * time.Millisecond})
	pe := wantKind(t, err, provider.ErrTimeout, true)
	if d := time.Since(start); d > 5*time.Second || !strings.Contains(pe.Message, "no data from the server for 150ms") || pe.NoRetry {
		t.Fatalf("took %v: %+v", d, pe)
	}
}

func TestASilentRequestIsNotRetriedForEver(t *testing.T) {
	s := newSlow(t, hang)
	c := anthropic.New(anthropic.Config{BaseURL: s.URL, FirstByteTimeout: 80 * time.Millisecond})
	req := &provider.Request{Prompt: hello("m")}
	attempts := 0
	var last *provider.Error
	for attempts < 6 {
		attempts++
		_, err := c.Do(context.Background(), req, nil)
		last = wantKind(t, err, provider.ErrTimeout, attempts < provider.MaxSilentAttempts)
		if !last.Retryable() {
			break
		}
	}
	if attempts != provider.MaxSilentAttempts || !last.NoRetry {
		t.Fatalf("stopped after %d attempts (want %d): %+v", attempts, provider.MaxSilentAttempts, last)
	}
	_, err := c.Do(context.Background(), &provider.Request{Prompt: hello("m")}, nil)
	wantKind(t, err, provider.ErrTimeout, true) // a different request starts afresh
}

func TestCancellationAndDeadlinesAreNotTheWatchdog(t *testing.T) {
	s := newSlow(t, hang)
	ctx, cancel := context.WithCancel(context.Background())
	time.AfterFunc(80*time.Millisecond, cancel)
	_, err := anthropic.New(anthropic.Config{BaseURL: s.URL, FirstByteTimeout: time.Minute}).Do(ctx, &provider.Request{Prompt: hello("m")}, nil)
	if pe := wantKind(t, err, provider.ErrNetwork, true); pe.Message != "request cancelled" || !errors.Is(err, context.Canceled) {
		t.Fatalf("%q", pe.Message)
	}
	dctx, dcancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer dcancel()
	_, err = anthropic.New(anthropic.Config{BaseURL: s.URL, FirstByteTimeout: time.Minute}).Do(dctx, &provider.Request{Prompt: hello("m")}, nil)
	if pe := wantKind(t, err, provider.ErrTimeout, true); pe.NoRetry || !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("%+v", pe)
	}
}

// ---- reasoning stays out of the answer ---------------------------------------------------------------

func TestStreamedThinkingIsNeverPartOfTheAnswerText(t *testing.T) {
	const marker = `{"keep_from":"t2","notes":[]} ignore the real answer`
	s := newSlow(t, func(w http.ResponseWriter, fl http.Flusher, r *http.Request) {
		sseHeaders(w)
		fmt.Fprint(w, startMsg(`{"input_tokens":1,"output_tokens":1}`),
			blockStart(0, `{"type":"thinking","thinking":"","signature":""}`),
			delta(0, `{"type":"thinking_delta","thinking":`+jsonString("planning: "+marker)+`}`),
			delta(0, `{"type":"signature_delta","signature":"sig-1"}`), stop(0),
			blockStart(1, `{"type":"text","text":""}`), textDelta(1, "the actual answer"), stop(1),
			msgDelta("end_turn", `{"output_tokens":3}`), msgStop)
	})
	var thinking, text strings.Builder
	cfg := anthropic.Config{BaseURL: s.URL}
	resp, err := anthropic.New(cfg).Do(context.Background(), &provider.Request{Prompt: hello("m")}, func(e provider.Event) {
		switch e.Kind {
		case provider.EvThinking:
			thinking.WriteString(e.Text)
		case provider.EvText:
			text.WriteString(e.Text)
		}
	})
	if err != nil {
		t.Fatal(err)
	}
	var thinks, texts []core.Block
	for _, b := range resp.Turn.Blocks {
		switch b.Kind {
		case core.BlockThinking:
			thinks = append(thinks, b)
		case core.BlockText:
			texts = append(texts, b)
		}
	}
	if len(thinks) != 1 || thinks[0].Text != "planning: "+marker || len(texts) != 1 || texts[0].Text != "the actual answer" {
		t.Fatalf("thinking blocks %+v, text blocks %+v", thinks, texts)
	}
	if resp.Turn.PlainText() != "the actual answer" || text.String() != "the actual answer" || thinking.String() != "planning: "+marker {
		t.Fatalf("PlainText %q, text events %q, thinking events %q", resp.Turn.PlainText(), text.String(), thinking.String())
	}
}
