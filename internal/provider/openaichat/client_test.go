package openaichat_test

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/reee344/sleipnir/internal/core"
	"github.com/reee344/sleipnir/internal/provider"
	"github.com/reee344/sleipnir/internal/provider/mock"
	"github.com/reee344/sleipnir/internal/provider/openaichat"
)

func newClient(t *testing.T, cfg mock.Config, r mock.Responder, opts openaichat.Options) (*openaichat.Client, *mock.Server) {
	t.Helper()
	srv := mock.New(cfg, r)
	ts := srv.Start()
	t.Cleanup(ts.Close)
	c := openaichat.New(openaichat.Config{Name: "mock", BaseURL: ts.URL, APIKey: "k", Options: opts})
	return c, srv
}

func prompt(system string, msgs ...core.Message) *core.Prompt {
	p := &core.Prompt{Model: "mock-1", Params: core.Params{MaxTokens: 256}}
	if system != "" {
		p.System = []core.Block{core.Text(system)}
	}
	p.Messages = msgs
	return p
}

func user(s string) core.Message {
	return core.Message{Role: core.RoleUser, Blocks: []core.Block{core.Text(s)}}
}

// The time to first byte is when the first frame arrived, not when the answer ended: the adapter used to report the total as both,
// so a log could not tell an endpoint that queues from one that decodes slowly. The server holds back the rest of the answer until
// the client has seen the first frame, so the two times cannot be the same whatever the clock does.
func TestTimeToFirstByteIsTheFirstFrameAndNotTheEnd(t *testing.T) {
	released := make(chan struct{})
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		fl := w.(http.Flusher)
		io.WriteString(w, `data: {"id":"c1","model":"m","choices":[{"index":0,"delta":{"role":"assistant","content":"Hel"}}]}`+"\n\n")
		fl.Flush()
		select {
		case <-released:
		case <-r.Context().Done():
			return
		}
		io.WriteString(w, `data: {"id":"c1","model":"m","choices":[{"index":0,"delta":{"content":"lo"},"finish_reason":"stop"}],"usage":{"prompt_tokens":10,"completion_tokens":2,"total_tokens":12}}`+"\n\n")
		io.WriteString(w, "data: [DONE]\n\n")
		fl.Flush()
	}))
	t.Cleanup(ts.Close)
	c := openaichat.New(openaichat.Config{Name: "raw", BaseURL: ts.URL, APIKey: "k"})
	var once sync.Once
	resp, err := c.Do(context.Background(), &provider.Request{Prompt: prompt("sys", user("hi"))}, func(e provider.Event) {
		if e.Kind == provider.EvStart {
			once.Do(func() { close(released) })
		}
	})
	if err != nil {
		t.Fatal(err)
	}
	if resp.Turn.PlainText() != "Hello" {
		t.Fatalf("text = %q", resp.Turn.PlainText())
	}
	if resp.TTFB <= 0 || resp.TTFB >= resp.Total {
		t.Errorf("time to first byte %v, total %v: the first frame came before the answer was over", resp.TTFB, resp.Total)
	}
	// A reply that is not streamed has one moment: its first byte is its end.
	c2, _ := newClient(t, mock.Config{}, func(*mock.Call) mock.Reply { return mock.Reply{Text: "all at once"} }, openaichat.Options{})
	whole, err := c2.Do(context.Background(), &provider.Request{Prompt: prompt("sys", user("hi")), NoStream: true}, func(provider.Event) {})
	if err != nil {
		t.Fatal(err)
	}
	if whole.TTFB <= 0 || whole.TTFB > whole.Total {
		t.Errorf("a reply that was not streamed: time to first byte %v, total %v", whole.TTFB, whole.Total)
	}
}

func TestTextStreamAndNonStream(t *testing.T) {
	c, _ := newClient(t, mock.Config{}, func(*mock.Call) mock.Reply { return mock.Reply{Text: "hello there, this is a streamed answer"} }, openaichat.Options{})
	for _, noStream := range []bool{false, true} {
		var sawStart, sawText bool
		resp, err := c.Do(context.Background(), &provider.Request{Prompt: prompt("sys", user("hi")), NoStream: noStream}, func(e provider.Event) {
			switch e.Kind {
			case provider.EvStart:
				sawStart = true
			case provider.EvText:
				sawText = true
			}
		})
		if err != nil {
			t.Fatalf("noStream=%v: %v", noStream, err)
		}
		if resp.Turn.PlainText() != "hello there, this is a streamed answer" {
			t.Fatalf("text = %q", resp.Turn.PlainText())
		}
		if !sawStart || (!noStream && !sawText) {
			t.Fatalf("events: start=%v text=%v", sawStart, sawText)
		}
		if resp.Stop != core.StopEnd || resp.CostUSD == nil || resp.Usage.OutputTokens == 0 {
			t.Fatalf("stop=%v cost=%v usage=%+v", resp.Stop, resp.CostUSD, resp.Usage)
		}
	}
}

func TestToolCallRoundTripIsByteStable(t *testing.T) {
	// Arguments with odd whitespace and key order must be echoed back exactly.
	weird := `{"path":  "a.txt",  "lines":[1,2],"z":1,"a":2}`
	var second []mock.Msg
	c, _ := newClient(t, mock.Config{}, func(call *mock.Call) mock.Reply {
		if len(call.Messages) <= 2 {
			return mock.Reply{Text: "reading", ToolCalls: []mock.ToolCall{{ID: "call_1", Name: "read", Args: weird}}}
		}
		second = call.Messages
		return mock.Reply{Text: "done"}
	}, openaichat.Options{})

	p := prompt("sys", user("read a.txt"))
	r1, err := c.Do(context.Background(), &provider.Request{Prompt: p}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if r1.Stop != core.StopToolUse {
		t.Fatalf("stop = %v", r1.Stop)
	}
	calls := r1.Turn.ToolCalls()
	if len(calls) != 1 || calls[0].ToolID != "call_1" || string(calls[0].Input) != weird {
		t.Fatalf("tool call = %+v", calls)
	}

	p2 := prompt("sys", user("read a.txt"),
		core.Message{Role: core.RoleAssistant, Blocks: r1.Turn.Blocks},
		core.Message{Role: core.RoleUser, Blocks: []core.Block{core.ToolResult("call_1", false, core.Text("file body"))}},
	)
	if _, err := c.Do(context.Background(), &provider.Request{Prompt: p2}, nil); err != nil {
		t.Fatal(err)
	}
	var asst *mock.Msg
	for i := range second {
		if second[i].Role == "assistant" {
			asst = &second[i]
		}
	}
	if asst == nil || len(asst.ToolCalls) != 1 || asst.ToolCalls[0].Args != weird {
		t.Fatalf("assistant tool call args not replayed verbatim: %+v", asst)
	}
	last := second[len(second)-1]
	if last.Role != "tool" || last.ToolCallID != "call_1" || last.Content != "file body" {
		t.Fatalf("tool result message = %+v", last)
	}
}

func TestInvalidToolArgumentsAreFlagged(t *testing.T) {
	c, _ := newClient(t, mock.Config{}, func(*mock.Call) mock.Reply {
		return mock.Reply{ToolCalls: []mock.ToolCall{{ID: "c1", Name: "edit", Args: `{"path": "x", "old`}}, Finish: "length"}
	}, openaichat.Options{})
	r, err := c.Do(context.Background(), &provider.Request{Prompt: prompt("", user("go"))}, nil)
	if err != nil {
		t.Fatal(err)
	}
	calls := r.Turn.ToolCalls()
	if len(calls) != 1 || calls[0].Invalid == "" {
		t.Fatalf("truncated arguments must be flagged invalid: %+v", calls)
	}
	if r.Stop != core.StopMaxTokens {
		t.Fatalf("stop = %v, want max_tokens", r.Stop)
	}
}

func TestReasoningDetailsReplayedVerbatim(t *testing.T) {
	details := json.RawMessage(`[{"type":"reasoning.text","text":"thinking hard","signature":"sig123","index":0}]`)
	var sawBack json.RawMessage
	c, _ := newClient(t, mock.Config{}, func(call *mock.Call) mock.Reply {
		for _, m := range call.Messages {
			if m.Role == "assistant" {
				sawBack = m.ReasoningDetails
			}
		}
		if len(call.Messages) == 1 {
			return mock.Reply{Text: "first", ReasoningDetails: details}
		}
		return mock.Reply{Text: "second"}
	}, openaichat.Options{})
	r1, err := c.Do(context.Background(), &provider.Request{Prompt: prompt("", user("q"))}, nil)
	if err != nil {
		t.Fatal(err)
	}
	var thinking *core.Block
	for i := range r1.Turn.Blocks {
		if r1.Turn.Blocks[i].Kind == core.BlockThinking {
			thinking = &r1.Turn.Blocks[i]
		}
	}
	if thinking == nil || thinking.WireFormat != openaichat.Dialect {
		t.Fatalf("reasoning_details not captured as replayable block: %+v", r1.Turn.Blocks)
	}
	p2 := prompt("", user("q"), core.Message{Role: core.RoleAssistant, Blocks: r1.Turn.Blocks}, user("again"))
	if _, err := c.Do(context.Background(), &provider.Request{Prompt: p2}, nil); err != nil {
		t.Fatal(err)
	}
	var a, b []map[string]any
	json.Unmarshal(sawBack, &a)
	json.Unmarshal(details, &b)
	if len(a) != 1 || a[0]["signature"] != "sig123" || a[0]["text"] != "thinking hard" {
		t.Fatalf("reasoning_details changed on replay: %s", sawBack)
	}
}

func TestPrefixCacheGrowsAcrossTurns(t *testing.T) {
	c, srv := newClient(t, mock.Config{Engine: mock.EngineConfig{BlockTokens: 16, MinCacheTokens: 64}}, nil, openaichat.Options{})
	sys := strings.Repeat("You are a careful engineer. ", 200) // ~1.4k tokens
	msgs := []core.Message{user("task: build the thing")}
	for i := 0; i < 6; i++ {
		r, err := c.Do(context.Background(), &provider.Request{Prompt: prompt(sys, msgs...)}, nil)
		if err != nil {
			t.Fatal(err)
		}
		msgs = append(msgs, core.Message{Role: core.RoleAssistant, Blocks: r.Turn.Blocks}, user("continue with step "+strings.Repeat("x", 100)))
	}
	st := srv.Stats()
	if st[0].Cached != 0 {
		t.Fatalf("first request cannot hit: %+v", st[0])
	}
	for i := 1; i < len(st); i++ {
		if st[i].HitRatio() < 0.80 {
			t.Fatalf("request %d hit ratio %.2f (cached %d of %d)", i, st[i].HitRatio(), st[i].Cached, st[i].PromptTokens)
		}
	}
}

func TestAffinityKeepsSharedPrefixOnOneEngine(t *testing.T) {
	sys := strings.Repeat("Shared project context. ", 300)
	// coldPrefills counts follow-up requests that had to prefill the shared
	// prefix from scratch: each is a full-price write another agent already paid.
	run := func(withKey bool) (coldPrefills int) {
		c, srv := newClient(t, mock.Config{Engines: 4}, nil, openaichat.Options{SessionHeader: true})
		p := prompt(sys, user("prime"))
		if withKey {
			p.CacheKey = "grp"
		}
		if _, err := c.Do(context.Background(), &provider.Request{Prompt: p}, nil); err != nil {
			t.Fatal(err)
		}
		for i := 0; i < 12; i++ {
			q := prompt(sys, user("agent task "+string(rune('a'+i))))
			if withKey {
				q.CacheKey = "grp"
			}
			if _, err := c.Do(context.Background(), &provider.Request{Prompt: q}, nil); err != nil {
				t.Fatal(err)
			}
		}
		for _, s := range srv.Stats()[1:] {
			if s.HitRatio() < 0.5 {
				coldPrefills++
			}
		}
		return
	}
	if got := run(true); got != 0 {
		t.Fatalf("with affinity every follow-up should reuse the primed engine, %d cold prefills", got)
	}
	// Without a key requests spread round-robin over 4 engines: three of them
	// have never seen the prefix.
	if got := run(false); got != 3 {
		t.Fatalf("without affinity expected 3 cold prefills (one per unprimed engine), got %d", got)
	}
}

func TestFanOutNeedsWarmup(t *testing.T) {
	sys := strings.Repeat("Shared project context. ", 400)
	// The first-token window is wide and the requests are sent together: entries publish at the first
	// token, and a loaded machine must not be able to start one request after another's first token.
	cfg := mock.Config{FirstToken: 250 * time.Millisecond, Engine: mock.EngineConfig{BlockTokens: 16, MinCacheTokens: 64}}
	fan := func(c *openaichat.Client, n int) {
		var wg, ready sync.WaitGroup
		start := make(chan struct{})
		for i := 0; i < n; i++ {
			wg.Add(1)
			ready.Add(1)
			go func(i int) {
				defer wg.Done()
				ready.Done()
				<-start
				p := prompt(sys, user("task "+string(rune('a'+i))))
				if _, err := c.Do(context.Background(), &provider.Request{Prompt: p}, nil); err != nil {
					t.Error(err)
				}
			}(i)
		}
		ready.Wait()
		close(start)
		wg.Wait()
	}

	c1, s1 := newClient(t, cfg, nil, openaichat.Options{})
	fan(c1, 8) // cold parallel burst
	cold := 0
	for _, s := range s1.Stats() {
		cold += s.Cached
	}

	c2, s2 := newClient(t, cfg, nil, openaichat.Options{})
	warm := &provider.Request{Prompt: prompt(sys, user("warm")), Warm: true}
	if _, err := c2.Do(context.Background(), warm, nil); err != nil {
		t.Fatal(err)
	}
	fan(c2, 8)
	hot := 0
	for _, s := range s2.Stats()[1:] {
		hot += s.Cached
	}
	if cold != 0 {
		t.Fatalf("parallel cold burst should read nothing (entries publish at first token), got %d cached tokens", cold)
	}
	if hot == 0 {
		t.Fatal("after a warm-up request the fan-out must hit the shared prefix")
	}
}

func TestErrorMapping(t *testing.T) {
	cases := []struct {
		name  string
		fault mock.Fault
		want  provider.ErrKind
		retry bool
	}{
		{"429", mock.Fault{Status: 429, Message: "slow down", RetryAfter: 3 * time.Second}, provider.ErrRateLimit, true},
		{"402", mock.Fault{Status: 402, Message: "no credits"}, provider.ErrPayment, false},
		{"401", mock.Fault{Status: 401, Message: "bad key"}, provider.ErrAuth, false},
		{"503", mock.Fault{Status: 503, Message: "no endpoint"}, provider.ErrServer, true},
		{"ctx", mock.Fault{Status: 400, Message: "maximum context length is 131072 tokens"}, provider.ErrContextLength, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := tc.fault
			c, _ := newClient(t, mock.Config{}, func(*mock.Call) mock.Reply { return mock.Reply{Fault: &f} }, openaichat.Options{})
			_, err := c.Do(context.Background(), &provider.Request{Prompt: prompt("", user("x"))}, nil)
			pe, ok := provider.AsError(err)
			if !ok {
				t.Fatalf("not a provider error: %v", err)
			}
			if pe.Kind != tc.want || pe.Retryable() != tc.retry {
				t.Fatalf("kind=%v retryable=%v", pe.Kind, pe.Retryable())
			}
			if tc.name == "429" && pe.RetryAfter != 3*time.Second {
				t.Fatalf("retry-after = %v", pe.RetryAfter)
			}
		})
	}
}

func TestMidStreamErrorIsTerminal(t *testing.T) {
	c, _ := newClient(t, mock.Config{}, func(*mock.Call) mock.Reply {
		return mock.Reply{Text: "abc", Fault: &mock.Fault{Status: 502, Message: "Provider disconnected", MidStream: true}}
	}, openaichat.Options{})
	_, err := c.Do(context.Background(), &provider.Request{Prompt: prompt("", user("x"))}, nil)
	pe, ok := provider.AsError(err)
	if !ok || pe.Kind != provider.ErrServer {
		t.Fatalf("want retryable server error, got %v", err)
	}
}

func TestProtocolViolationsAreRejectedByServer(t *testing.T) {
	c, _ := newClient(t, mock.Config{}, nil, openaichat.Options{})
	// A tool result with no matching tool call must be a 400, proving the mock
	// is strict enough to catch adapter ordering bugs.
	p := prompt("", user("x"), core.Message{Role: core.RoleUser, Blocks: []core.Block{core.ToolResult("ghost", false, core.Text("?"))}})
	_, err := c.Do(context.Background(), &provider.Request{Prompt: p}, nil)
	pe, ok := provider.AsError(err)
	if !ok || pe.Kind != provider.ErrBadRequest {
		t.Fatalf("want bad request, got %v", err)
	}
}

func TestTokenCapture(t *testing.T) {
	srv := mock.New(mock.Config{}, func(call *mock.Call) mock.Reply {
		return mock.Reply{Text: "the answer is forty two", ToolCalls: []mock.ToolCall{{ID: "c1", Name: "read", Args: `{"path":"a.go"}`}}}
	})
	ts := srv.Start()
	t.Cleanup(ts.Close)
	prof := openaichat.DefaultProfile("mock", ts.URL)
	prof.CaptureTokens = true
	c := openaichat.New(openaichat.Config{Name: "mock", BaseURL: ts.URL, APIKey: "k", Profile: &prof})

	p := prompt("sys", user("what is the answer to everything"))
	for _, noStream := range []bool{false, true} {
		resp, err := c.Do(context.Background(), &provider.Request{Prompt: p, NoStream: noStream, Capture: true}, nil)
		if err != nil {
			t.Fatal(err)
		}
		tr := resp.Tokens
		if tr == nil || len(tr.PromptIDs) == 0 || len(tr.CompletionIDs) == 0 {
			t.Fatalf("noStream=%v: expected a token trace, got %+v", noStream, tr)
		}
		if len(tr.Logprobs) != len(tr.CompletionIDs) || !tr.Consistent() {
			t.Fatalf("logprobs %d vs ids %d", len(tr.Logprobs), len(tr.CompletionIDs))
		}
	}
	// Not asked for: nothing captured and nothing requested from the server.
	resp, err := c.Do(context.Background(), &provider.Request{Prompt: p}, nil)
	if err != nil || resp.Tokens != nil {
		t.Fatalf("capture off must yield no trace: %v %+v", err, resp.Tokens)
	}
	// An endpoint that cannot capture is never asked to.
	plain := openaichat.New(openaichat.Config{Name: "mock", BaseURL: ts.URL, APIKey: "k"})
	if resp, err := plain.Do(context.Background(), &provider.Request{Prompt: p, Capture: true}, nil); err != nil || resp.Tokens != nil {
		t.Fatalf("profile without capture support: %v %+v", err, resp.Tokens)
	}

	// Append-only prompts give prefix-stable prompt ids: the property the RL
	// exporter relies on to pack a segment into one sequence.
	r1, _ := c.Do(context.Background(), &provider.Request{Prompt: p, Capture: true}, nil)
	p2 := prompt("sys", user("what is the answer to everything"),
		core.Message{Role: core.RoleAssistant, Blocks: r1.Turn.Blocks},
		core.Message{Role: core.RoleUser, Blocks: []core.Block{core.ToolResult("c1", false, core.Text("package main"))}})
	r2, err := c.Do(context.Background(), &provider.Request{Prompt: p2, Capture: true}, nil)
	if err != nil {
		t.Fatal(err)
	}
	a, b := r1.Tokens.PromptIDs, r2.Tokens.PromptIDs
	if len(b) <= len(a) {
		t.Fatalf("second prompt must be longer: %d vs %d", len(b), len(a))
	}
	for i := range a {
		if a[i] != b[i] {
			t.Fatalf("prompt ids diverge at %d", i)
		}
	}
}

// Found by putting the failures of a real endpoint in front of the adapter (internal/provider/chaos): an endpoint that did not stream
// answered a streaming request with the whole completion as JSON, and the adapter, which expected frames, called it a cut connection
// and asked again, for ever; a page of HTML with a 200 got the same words, which sent the person looking for a network fault. The
// whole completion is read as one now, an error in that body is the endpoint's own error, and a page is named for what it is.
func TestAnEndpointThatAnswersAStreamingRequestWithOneJSONCompletionIsRead(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		_, _ = io.WriteString(w, `{"id":"c1","model":"m","choices":[{"index":0,"message":{"role":"assistant","content":"the whole answer"},"finish_reason":"stop"}],"usage":{"prompt_tokens":10,"completion_tokens":3,"total_tokens":13}}`)
	}))
	defer ts.Close()
	c := openaichat.New(openaichat.Config{Name: "t", BaseURL: ts.URL, APIKey: "k"})
	var started bool
	resp, err := c.Do(context.Background(), &provider.Request{Prompt: prompt("", user("hi"))}, func(e provider.Event) {
		if e.Kind == provider.EvStart {
			started = true
		}
	})
	if err != nil || resp == nil {
		t.Fatalf("%v %v", resp, err)
	}
	if got := resp.Turn.PlainText(); got != "the whole answer" || resp.Usage.InputTokens+resp.Usage.CacheReadTokens < 10 && resp.Usage.OutputTokens != 3 {
		t.Fatalf("answer %q usage %+v", got, resp.Usage)
	}
	if !started {
		t.Error("the start of the answer was never reported: the gate that holds back the other agents waits for it")
	}
}

func TestAnErrorBodyAnswered200IsTheEndpointsError(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"error":{"message":"The server is overloaded, try again later","type":"overloaded_error"}}`)
	}))
	defer ts.Close()
	c := openaichat.New(openaichat.Config{Name: "t", BaseURL: ts.URL, APIKey: "k"})
	_, err := c.Do(context.Background(), &provider.Request{Prompt: prompt("", user("hi"))}, nil)
	var pe *provider.Error
	if !errors.As(err, &pe) || !pe.Retryable() || !strings.Contains(pe.Message, "overloaded") {
		t.Fatalf("%v: want the endpoint's own error, retryable", err)
	}
}

func TestAPageOfHTMLAnswered200IsNamedForWhatItIs(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = io.WriteString(w, `<html><head><title>Sign in to the network</title></head><body><h1>Welcome</h1><p>Please accept the terms.</p></body></html>`)
	}))
	defer ts.Close()
	c := openaichat.New(openaichat.Config{Name: "t", BaseURL: ts.URL, APIKey: "k"})
	_, err := c.Do(context.Background(), &provider.Request{Prompt: prompt("", user("hi"))}, nil)
	var pe *provider.Error
	if !errors.As(err, &pe) || pe.Kind != provider.ErrServer || !pe.Retryable() {
		t.Fatalf("%v: want a server failure, retryable", err)
	}
	for _, want := range []string{"HTML page", "captive portal", "Sign in to the network", "Please accept the terms"} {
		if !strings.Contains(pe.Message, want) {
			t.Errorf("the message %q lacks %q", pe.Message, want)
		}
	}
	if strings.Contains(pe.Message, "<") {
		t.Errorf("the message carries markup: %q", pe.Message)
	}
}

// Arguments the model cut off are not JSON, and an endpoint that checks the history refuses the whole next request for them ("Assistant
// tool call function.arguments must be valid JSON"): a swarm run ended on that. They are replayed as an empty object; the tool result
// already says what went wrong.
func TestInvalidToolArgumentsAreReplayedAsAnEmptyObject(t *testing.T) {
	var replayed string
	c, _ := newClient(t, mock.Config{}, func(call *mock.Call) mock.Reply {
		for _, m := range call.Messages {
			if m.Role == "assistant" && len(m.ToolCalls) == 1 {
				replayed = m.ToolCalls[0].Args
			}
		}
		if len(call.Messages) == 1 {
			return mock.Reply{ToolCalls: []mock.ToolCall{{ID: "c1", Name: "edit", Args: `{"path": "x", "old`}}, Finish: "length"}
		}
		return mock.Reply{Text: "ok"}
	}, openaichat.Options{})
	r, err := c.Do(context.Background(), &provider.Request{Prompt: prompt("", user("go"))}, nil)
	if err != nil {
		t.Fatal(err)
	}
	tr := core.Message{Role: core.RoleUser, Blocks: []core.Block{{Kind: core.BlockToolResult, ToolID: "c1", IsError: true, Result: []core.Block{core.Text("cut off")}}}}
	p2 := prompt("", user("go"), core.Message{Role: core.RoleAssistant, Blocks: r.Turn.Blocks}, tr)
	if _, err := c.Do(context.Background(), &provider.Request{Prompt: p2}, nil); err != nil {
		t.Fatal(err)
	}
	if replayed != "{}" {
		t.Errorf("replayed arguments %q, want {}", replayed)
	}
}
