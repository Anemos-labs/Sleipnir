package openairesp_test

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/anemos-labs/sleipnir/internal/core"
	"github.com/anemos-labs/sleipnir/internal/provider"
	"github.com/anemos-labs/sleipnir/internal/provider/openairesp"
	"github.com/anemos-labs/sleipnir/internal/provider/providertest"
)

// sse is a stream of frames, each as the endpoint sends it: an event line naming the type, and the data.
func sse(frames ...string) string {
	var sb strings.Builder
	for _, f := range frames {
		var t struct {
			Type string `json:"type"`
		}
		_ = json.Unmarshal([]byte(f), &t)
		fmt.Fprintf(&sb, "event: %s\ndata: %s\n\n", t.Type, f)
	}
	return sb.String()
}

const (
	created   = `{"type":"response.created","response":{"id":"resp_1","model":"gpt-test","status":"in_progress"}}`
	completed = `{"type":"response.completed","response":{"id":"resp_1","model":"gpt-test","status":"completed","usage":{"input_tokens":1200,"input_tokens_details":{"cached_tokens":1024},"output_tokens":90,"output_tokens_details":{"reasoning_tokens":60},"total_tokens":1290}}}`
)

var (
	textDeltas = []string{
		`{"type":"response.output_item.added","output_index":0,"item":{"id":"msg_1","type":"message","role":"assistant","content":[]}}`,
		`{"type":"response.output_text.delta","output_index":0,"delta":"Hel"}`,
		`{"type":"response.output_text.delta","output_index":0,"delta":"lo"}`,
		`{"type":"response.output_item.done","output_index":0,"item":{"id":"msg_1","type":"message","role":"assistant","content":[{"type":"output_text","text":"Hello"}]}}`,
	}
	toolFrames = []string{
		`{"type":"response.output_item.added","output_index":0,"item":{"id":"rs_1","type":"reasoning","summary":[]}}`,
		`{"type":"response.reasoning_summary_text.delta","output_index":0,"delta":"Reading the file."}`,
		`{"type":"response.output_item.done","output_index":0,"item":{"id":"rs_1","type":"reasoning","encrypted_content":"ENC","summary":[{"type":"summary_text","text":"Reading the file."}]}}`,
		`{"type":"response.output_item.added","output_index":1,"item":{"id":"fc_1","type":"function_call","call_id":"call_9","name":"read","arguments":""}}`,
		`{"type":"response.function_call_arguments.delta","output_index":1,"delta":"{\"path\":"}`,
		`{"type":"response.function_call_arguments.delta","output_index":1,"delta":"\"a.go\"}"}`,
		`{"type":"response.output_item.done","output_index":1,"item":{"id":"fc_1","type":"function_call","call_id":"call_9","name":"read","arguments":"{\"path\":\"a.go\"}"}}`,
	}
)

func server(t *testing.T, h http.HandlerFunc) *httptest.Server {
	t.Helper()
	ts := httptest.NewServer(h)
	t.Cleanup(ts.Close)
	return ts
}

func stream(body string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		io.WriteString(w, body)
	}
}

func client(ts *httptest.Server, auth openairesp.Authorizer, o openairesp.Options) *openairesp.Client {
	return openairesp.New(openairesp.Config{Name: "openai", BaseURL: ts.URL + "/v1", Auth: auth, Options: o})
}

func request() *provider.Request {
	return &provider.Request{Prompt: &core.Prompt{Model: "gpt-test", System: []core.Block{core.Text("sys")}, Params: core.Params{MaxTokens: 256},
		Messages: []core.Message{{Role: core.RoleUser, Blocks: []core.Block{core.Text("hi")}}}}}
}

func do(t *testing.T, c *openairesp.Client) (*provider.Response, []provider.Event, error) {
	t.Helper()
	var evs []provider.Event
	resp, err := c.Do(context.Background(), request(), func(e provider.Event) { evs = append(evs, e) })
	return resp, evs, err
}

func TestAnAnswerIsStreamedAndAccountedFor(t *testing.T) {
	ts := server(t, stream(sse(append(append([]string{created}, textDeltas...), completed)...)))
	resp, evs, err := do(t, client(ts, openairesp.StaticKey("k"), openairesp.Options{}))
	if err != nil {
		t.Fatal(err)
	}
	if resp.Turn.PlainText() != "Hello" || resp.Stop != core.StopEnd || resp.ID != "resp_1" || resp.Model != "gpt-test" {
		t.Errorf("turn %q stop %v id %q model %q", resp.Turn.PlainText(), resp.Stop, resp.ID, resp.Model)
	}
	// Responses reports the whole prompt and the cached part: the harness wants the uncached remainder and the cache read apart
	if u := resp.Usage; u.InputTokens != 176 || u.CacheReadTokens != 1024 || u.OutputTokens != 90 || u.ReasoningTokens != 60 {
		t.Errorf("usage = %+v", u)
	}
	var text string
	starts := 0
	for _, e := range evs {
		switch e.Kind {
		case provider.EvStart:
			starts++
		case provider.EvText:
			text += e.Text
		}
	}
	if text != "Hello" || starts != 1 || evs[0].Kind != provider.EvStart || evs[len(evs)-1].Kind != provider.EvUsage {
		t.Errorf("events: text %q, %d starts, first %v, last %v", text, starts, evs[0].Kind, evs[len(evs)-1].Kind)
	}
	if b := resp.Turn.Blocks[0]; b.WireFormat != openairesp.Dialect || !json.Valid(b.Wire) {
		t.Errorf("the message is kept as it came, for a turn that has reasoning to go back with: %+v", b)
	}
}

func TestAToolCallWithReasoningKeepsBothVerbatim(t *testing.T) {
	ts := server(t, stream(sse(append(append([]string{created}, toolFrames...), completed)...)))
	resp, evs, err := do(t, client(ts, openairesp.StaticKey("k"), openairesp.Options{}))
	if err != nil {
		t.Fatal(err)
	}
	if resp.Stop != core.StopToolUse || len(resp.Turn.Blocks) != 2 {
		t.Fatalf("stop %v, blocks %+v", resp.Stop, resp.Turn.Blocks)
	}
	th, call := resp.Turn.Blocks[0], resp.Turn.Blocks[1]
	if th.Kind != core.BlockThinking || th.Text != "Reading the file." || !strings.Contains(string(th.Wire), `"encrypted_content":"ENC"`) {
		t.Errorf("reasoning block %+v", th)
	}
	if call.Kind != core.BlockToolUse || call.ToolID != "call_9" || call.ToolName != "read" || string(call.Input) != `{"path":"a.go"}` || !strings.Contains(string(call.Wire), `"id":"fc_1"`) {
		t.Errorf("call block %+v", call)
	}
	var started, args string
	for _, e := range evs {
		switch e.Kind {
		case provider.EvToolStart:
			started = e.ToolID + "/" + e.ToolName
		case provider.EvToolDelta:
			args += e.Text
		}
	}
	if started != "call_9/read" || args != `{"path":"a.go"}` {
		t.Errorf("tool events: %q %q", started, args)
	}
}

// The Codex backend and some gateways send the finished items only in the completed event; they are read from there.
func TestItemsOnlyInTheCompletedEventAreRead(t *testing.T) {
	done := `{"type":"response.completed","response":{"id":"r","model":"m","status":"completed","output":[{"type":"message","role":"assistant","content":[{"type":"output_text","text":"late"}]}],"usage":{"input_tokens":10,"output_tokens":2}}}`
	ts := server(t, stream(sse(created, done)))
	resp, _, err := do(t, client(ts, openairesp.StaticKey("k"), openairesp.Options{}))
	if err != nil || resp.Turn.PlainText() != "late" {
		t.Fatalf("%v %+v", err, resp)
	}
}

func TestAnIncompleteResponseSaysWhyItStopped(t *testing.T) {
	for reason, want := range map[string]core.StopReason{"max_output_tokens": core.StopMaxTokens, "content_filter": core.StopRefusal, "other": core.StopOther} {
		inc := `{"type":"response.incomplete","response":{"id":"r","model":"m","status":"incomplete","incomplete_details":{"reason":"` + reason + `"},"usage":{"input_tokens":5,"output_tokens":5}}}`
		ts := server(t, stream(sse(append(append([]string{created}, textDeltas...), inc)...)))
		resp, _, err := do(t, client(ts, openairesp.StaticKey("k"), openairesp.Options{}))
		if err != nil || resp.Stop != want {
			t.Errorf("%s: %v %v", reason, err, resp)
		}
	}
}

// A stream that just ends is a connection that was cut: taking it for a short answer would end a run on half a sentence.
func TestAStreamThatEndsBeforeTheResponseIsOverIsAnError(t *testing.T) {
	ts := server(t, stream(sse(append([]string{created}, textDeltas[:3]...)...)))
	_, _, err := do(t, client(ts, openairesp.StaticKey("k"), openairesp.Options{}))
	if pe, ok := provider.AsError(err); !ok || pe.Kind != provider.ErrNetwork || !pe.Retryable() {
		t.Errorf("a cut stream is a retryable network error: %v", err)
	}
}

func TestErrorsInTheStreamAreClassified(t *testing.T) {
	for _, tc := range []struct {
		name, frame string
		kind        provider.ErrKind
	}{
		{"failed rate limit", `{"type":"response.failed","response":{"status":"failed","error":{"code":"rate_limit_exceeded","message":"slow down"}}}`, provider.ErrRateLimit},
		{"failed context", `{"type":"response.failed","response":{"status":"failed","error":{"code":"context_length_exceeded","message":"too long"}}}`, provider.ErrContextLength},
		{"failed server", `{"type":"response.failed","response":{"status":"failed","error":{"code":"server_error","message":"oops"}}}`, provider.ErrServer},
		{"failed with no reason", `{"type":"response.failed","response":{"status":"failed"}}`, provider.ErrServer},
		{"error event", `{"type":"error","code":"invalid_request_error","message":"bad"}`, provider.ErrBadRequest},
		{"plan limit", `{"type":"error","code":"usage_limit_reached","message":"you have reached your plan's usage limit"}`, provider.ErrPayment},
	} {
		ts := server(t, stream(sse(created, tc.frame)))
		_, _, err := do(t, client(ts, openairesp.StaticKey("k"), openairesp.Options{}))
		if pe, ok := provider.AsError(err); !ok || pe.Kind != tc.kind {
			t.Errorf("%s: %v, want %v", tc.name, err, tc.kind)
		}
	}
}

func TestStatusesAreClassifiedAndAPlansLimitIsNotRetried(t *testing.T) {
	for _, tc := range []struct {
		status int
		body   string
		kind   provider.ErrKind
	}{
		{429, `{"error":{"message":"slow down","type":"rate_limit_exceeded"}}`, provider.ErrRateLimit},
		{429, `{"error":{"message":"You have hit your usage limit","type":"usage_limit_reached"}}`, provider.ErrPayment},
		{400, `{"error":{"message":"This model's maximum context length is 128000 tokens"}}`, provider.ErrContextLength},
		{403, `{"error":{"message":"no"}}`, provider.ErrAuth},
		{503, `oops`, provider.ErrServer},
	} {
		ts := server(t, func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(tc.status); io.WriteString(w, tc.body) })
		_, _, err := do(t, client(ts, openairesp.StaticKey("k"), openairesp.Options{}))
		if pe, ok := provider.AsError(err); !ok || pe.Kind != tc.kind || pe.Status != tc.status {
			t.Errorf("%d %s: %v, want %v", tc.status, tc.body, err, tc.kind)
		}
	}
}

type rotating struct {
	mu      sync.Mutex
	tokens  []string
	refused []string
	next    int
	err     error
}

func (a *rotating) Token(context.Context) (string, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.err != nil {
		return "", a.err
	}
	t := a.tokens[min(a.next, len(a.tokens)-1)]
	return t, nil
}

func (a *rotating) Refused(tok string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.refused = append(a.refused, tok)
	a.next++
}

// A token that was good and is refused (renewed elsewhere, revoked) is asked for again, once, before the request fails.
func TestARefusedTokenIsRenewedOnceAndTheRequestRepeated(t *testing.T) {
	var seen []string
	ts := server(t, func(w http.ResponseWriter, r *http.Request) {
		seen = append(seen, r.Header.Get("Authorization"))
		if r.Header.Get("Authorization") == "Bearer old" {
			w.WriteHeader(http.StatusUnauthorized)
			io.WriteString(w, `{"error":{"message":"token expired"}}`)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		io.WriteString(w, sse(append(append([]string{created}, textDeltas...), completed)...))
	})
	auth := &rotating{tokens: []string{"old", "new"}}
	resp, _, err := do(t, client(ts, auth, openairesp.Options{}))
	if err != nil || resp.Turn.PlainText() != "Hello" {
		t.Fatalf("%v %+v", err, resp)
	}
	if strings.Join(seen, ",") != "Bearer old,Bearer new" || len(auth.refused) != 1 || auth.refused[0] != "old" {
		t.Errorf("requests %v, refused %v", seen, auth.refused)
	}
	// a token that is refused again is an authentication error, and is not asked for a third time
	ts2 := server(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(401)
		io.WriteString(w, `{"error":{"message":"no"}}`)
	})
	_, _, err = do(t, client(ts2, &rotating{tokens: []string{"a", "b"}}, openairesp.Options{}))
	if pe, ok := provider.AsError(err); !ok || pe.Kind != provider.ErrAuth || pe.Retryable() {
		t.Errorf("a refusal after the renewal is final: %v", err)
	}
}

func TestAnAuthorizerThatCannotRenewEndsTheRequestWithWhatToDo(t *testing.T) {
	ts := server(t, stream(""))
	_, _, err := do(t, client(ts, &rotating{err: fmt.Errorf("the ChatGPT login has ended: run `sleipnir login chatgpt`")}, openairesp.Options{}))
	pe, ok := provider.AsError(err)
	if !ok || pe.Kind != provider.ErrAuth || pe.Retryable() || !strings.Contains(pe.Message, "sleipnir login chatgpt") {
		t.Errorf("%v", err)
	}
}

func TestAKeyIsNotSentOverPlainHTTPToAnotherHost(t *testing.T) {
	c := openairesp.New(openairesp.Config{Name: "x", BaseURL: "http://example.com/v1", Auth: openairesp.StaticKey("sk-secret")})
	_, err := c.Do(context.Background(), request(), nil)
	if pe, ok := provider.AsError(err); !ok || pe.Retryable() || strings.Contains(pe.Message, "sk-secret") {
		t.Errorf("%v", err)
	}
}

func TestAWholeJSONBodyIsReadAsTheCompletedResponse(t *testing.T) {
	ts := server(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"id":"r1","model":"m","status":"completed","output":[{"type":"message","role":"assistant","content":[{"type":"output_text","text":"whole"}]}],"usage":{"input_tokens":4,"output_tokens":1}}`)
	})
	resp, _, err := do(t, client(ts, openairesp.StaticKey("k"), openairesp.Options{}))
	if err != nil || resp.Turn.PlainText() != "whole" || resp.Usage.InputTokens != 4 {
		t.Fatalf("%v %+v", err, resp)
	}
	ts2 := server(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"error":{"code":"server_error","message":"boom"}}`)
	})
	if _, _, err = do(t, client(ts2, openairesp.StaticKey("k"), openairesp.Options{})); err == nil {
		t.Error("a body that is an error is an error")
	}
}

// The plan takes no output limit, so a warm-up would be a whole answer: it is not sent.
func TestAWarmUpOnAPlanSendsNothing(t *testing.T) {
	hits := 0
	ts := server(t, func(w http.ResponseWriter, r *http.Request) { hits++ })
	c := client(ts, openairesp.StaticKey("k"), openairesp.Options{Plan: true})
	req := request()
	req.Warm = true
	if _, err := c.Do(context.Background(), req, nil); err != nil || hits != 0 {
		t.Errorf("err %v, %d requests", err, hits)
	}
}

func TestTheRequestCarriesTheBearerTokenAndTheRightPath(t *testing.T) {
	var got struct{ path, auth, accept, body string }
	ts := server(t, func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		got.path, got.auth, got.accept, got.body = r.URL.Path, r.Header.Get("Authorization"), r.Header.Get("Accept"), string(b)
		w.Header().Set("Content-Type", "text/event-stream")
		io.WriteString(w, sse(created, completed))
	})
	if _, _, err := do(t, client(ts, openairesp.StaticKey("sk-1"), openairesp.Options{})); err != nil {
		t.Fatal(err)
	}
	if got.path != "/v1/responses" || got.auth != "Bearer sk-1" || got.accept != "text/event-stream" || !strings.Contains(got.body, `"store":false`) {
		t.Errorf("%+v", got)
	}
}

// The contract every adapter owes its caller (providertest.Check), on streams cut up the way a transport may cut them.
func TestTheAdapterKeepsTheContractWhateverTheEndpointSends(t *testing.T) {
	a := providertest.Adapter{Name: "openairesp", Model: "gpt-test", New: func(hc *http.Client) provider.Provider {
		return openairesp.New(openairesp.Config{Name: "openai", BaseURL: "https://api.test/v1", Auth: openairesp.StaticKey("k"), HTTPClient: hc})
	}}
	bodies := map[string]string{
		"text":       sse(append(append([]string{created}, textDeltas...), completed)...),
		"tool":       sse(append(append([]string{created}, toolFrames...), completed)...),
		"cut":        sse(append([]string{created}, textDeltas[:2]...)...),
		"failed":     sse(created, `{"type":"response.failed","response":{"error":{"code":"server_error","message":"x\u001b[31m"}}}`),
		"junk":       "data: not json\n\ndata: {\n\n: ping\n\n",
		"empty":      "",
		"huge usage": sse(created, `{"type":"response.completed","response":{"usage":{"input_tokens":99999999999999999999,"output_tokens":-5,"input_tokens_details":{"cached_tokens":"7"}}}}`),
		"bad args":   sse(created, `{"type":"response.output_item.done","output_index":0,"item":{"type":"function_call","call_id":"c","name":"n","arguments":"{\"a\":"}}`, completed),
		"no ids":     sse(created, `{"type":"response.output_item.done","output_index":0,"item":{"type":"function_call","name":"n","arguments":"{}"}}`, completed),
	}
	for name, body := range bodies {
		for cname, ch := range map[string]providertest.Chunking{"whole": providertest.Whole, "by 7": providertest.Every(7), "by 1": providertest.Every(1)} {
			o := a.Run(t, []byte(body), ch)
			t.Run(name+"/"+cname, func(t *testing.T) { providertest.Check(t, o) })
		}
	}
	// the same answer however the transport cuts it
	whole := a.Run(t, []byte(bodies["tool"]), providertest.Whole)
	for _, ch := range []providertest.Chunking{providertest.Every(1), providertest.Every(13), providertest.SplitAt(40)} {
		if d := whole.Diff(a.Run(t, []byte(bodies["tool"]), ch)); d != "" {
			t.Errorf("a stream cut up differently came out differently:\n%s", d)
		}
	}
}

func TestLimitsEndAHugeResponse(t *testing.T) {
	big := strings.Repeat("x", 1000)
	frames := []string{created}
	for i := 0; i < 50; i++ {
		frames = append(frames, `{"type":"response.output_text.delta","output_index":0,"delta":"`+big+`"}`)
	}
	ts := server(t, stream(sse(frames...)))
	c := openairesp.New(openairesp.Config{Name: "x", BaseURL: ts.URL + "/v1", Auth: openairesp.StaticKey("k"), Limits: provider.StreamLimits{MaxTextBytes: 10_000}})
	_, err := c.Do(context.Background(), request(), nil)
	if pe, ok := provider.AsError(err); !ok || pe.Kind != provider.ErrServer || !strings.Contains(pe.Message, "answer text") {
		t.Errorf("a response that outgrows its limit ends: %v", err)
	}
}
