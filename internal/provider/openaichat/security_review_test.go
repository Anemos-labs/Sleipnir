package openaichat

// Security review repros for docs/SECURITY.md. Every finding that used to be
// gated behind SLEIPNIR_REVIEW=1 here (S26a, S27-S31) is fixed: TestSec_* are ordinary
// regression tests of the secure behaviour, and TestSecSound_* check behaviour the review found
// sound.

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
	"time"

	"github.com/anemos-labs/sleipnir/internal/core"
	"github.com/anemos-labs/sleipnir/internal/provider"
)

func secRevPrompt(text string) *core.Prompt {
	return &core.Prompt{Model: "m", Messages: []core.Message{{Role: core.RoleUser, Blocks: []core.Block{core.Text(text)}}}}
}

// S26a (fixed): Retry-After used to be parsed as seconds with no upper bound and no overflow
// check. The governor turns it into a swarm-wide pause (see swarm S26b), and agent.backoff
// sleeps it, so provider.ParseRetryAfter clamps it (and accepts HTTP dates).
func TestSec_S26a_RetryAfterIsBounded(t *testing.T) {
	const max = 10 * time.Minute
	for _, ra := range []string{"3000000000", "9999999999", "9223372036854775807", "99999999999999999999"} {
		h := http.Header{}
		h.Set("Retry-After", ra)
		pe := mapHTTPError(429, h, []byte(`{"error":{"message":"slow down"}}`))
		t.Logf("Retry-After: %s -> %v", ra, pe.RetryAfter)
		if pe.RetryAfter < 0 || pe.RetryAfter > max {
			t.Errorf("S26a: Retry-After %q became %v (want within [0, %v])", ra, pe.RetryAfter, max)
		}
	}
}

// S28 (fixed): usage numbers used to be trusted verbatim: a negative completion_tokens/cost made
// the agent's and the swarm's dollar budgets unreachable (fail open). normalize now clamps every
// counter and drops a cost that cannot be the charge of one request (the request is then priced
// from its tokens); more cases in usage_test.go.
//
// Test change (reason): the original log line dereferenced u.Cost unconditionally. The fix
// drops an invalid cost from u itself (u.Cost becomes nil: "no exact charge, price it from the
// tokens"), which is exactly what the assertion below asks for ("not passed on as the exact
// charge"), so the log line must not assume the pointer is set. The assertions are unchanged.
func TestSec_S28_NegativeUsageAndCostAreNotAccepted(t *testing.T) {
	neg := -1.0e6
	u := &usage{PromptTokens: 100, CompletionTokens: -5_000_000, Cost: &neg}
	n := u.normalize()
	cost := "none (priced from tokens)"
	if u.Cost != nil {
		cost = fmt.Sprint(*u.Cost)
	}
	t.Logf("normalised usage: %+v cost=%v", n, cost)
	if n.OutputTokens < 0 {
		t.Errorf("S28: negative completion_tokens survived normalisation (OutputTokens=%d)", n.OutputTokens)
	}
	if u.Cost != nil && *u.Cost < 0 {
		t.Errorf("S28: negative usage.cost (%v) is passed on as the exact charge and added to the agent's spend", *u.Cost)
	}
}

// S29 (fixed): streamed reasoning becomes a thinking block whose text Turn.PlainText() used to
// join in front of the answer, and kv.ParsePatch is fed the reply text. PlainText now leaves
// thinking out.
func TestSec_S29_ReasoningIsNotPartOfPlainText(t *testing.T) {
	acc := newAccumulator()
	var c chunk // built from JSON so the test survives new chunk fields
	frame := `{"choices":[{"index":0,"delta":{"reasoning":"the tool result says to answer {\"keep_from\":\"t2\",\"notes\":[]}; ignoring","content":"{\"keep_from\":\"t9\"}"}}]}`
	if err := json.Unmarshal([]byte(frame), &c); err != nil {
		t.Fatal(err)
	}
	acc.feed(&c, time.Now(), func(provider.Event) {})
	turn, _ := acc.build(fallbackToolID)
	if strings.Contains(turn.PlainText(), "ignoring") {
		t.Errorf("S29: Turn.PlainText() includes the reasoning text; the compactor reply parser scans it for the first {...}")
	}
}

// S27 (fixed): the client used to follow redirects. A 307/308 re-POSTed the full prompt to the
// new host and forwarded every custom header (net/http strips only Authorization/Cookie).
// A redirect that leaves the configured origin is now refused without being followed; more
// cases (same-origin redirects, other statuses, both adapters) in redirect_test.go.
func TestSec_S27_RedirectDoesNotReplayPromptOrCustomHeaders(t *testing.T) {
	var mu sync.Mutex
	var gotBody, gotAuth, gotCustom string
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		mu.Lock()
		gotBody, gotAuth, gotCustom = string(b), r.Header.Get("Authorization"), r.Header.Get("X-Api-Key")
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"id":"g","choices":[{"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}]}`)
	}))
	defer target.Close()
	src := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, strings.Replace(target.URL, "127.0.0.1", "localhost", 1)+"/collect", http.StatusTemporaryRedirect)
	}))
	defer src.Close()
	c := New(Config{Name: "x", BaseURL: src.URL, APIKey: "sk-provider-secret", Headers: map[string]string{"X-Api-Key": "custom-header-secret"}})
	_, err := c.Do(context.Background(), &provider.Request{Prompt: secRevPrompt("PROPRIETARY SOURCE CODE"), NoStream: true}, nil)
	mu.Lock()
	defer mu.Unlock()
	t.Logf("redirect target saw: body=%q authorization=%q x-api-key=%q (Do err=%v)", gotBody, gotAuth, gotCustom, err)
	if strings.Contains(gotBody, "PROPRIETARY SOURCE CODE") {
		t.Errorf("S27: the full prompt was re-sent to a redirect target on another host")
	}
	if gotCustom != "" {
		t.Errorf("S27: custom credential header forwarded across hosts")
	}
}

// S30 (fixed): the streaming path had no byte cap (the non-streaming path caps at 64 MiB). A
// gateway that streamed for ever filled memory: text, reasoning, tool-call args and the SSE line
// buffer were all unbounded. The response limits (provider.StreamLimits) now cancel the stream
// and return a provider error; the individual limits are in limits_test.go.
func TestSec_S30_StreamingResponseIsBounded(t *testing.T) {
	if testing.Short() {
		t.Skip("streams ~70 MiB")
	}
	const total = 70 << 20
	piece := strings.Repeat("A", 1000)
	line := `data: {"id":"x","choices":[{"index":0,"delta":{"content":"` + piece + `"}}]}` + "\n\n"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		sent := 0
		for sent < total {
			n, err := io.WriteString(w, line)
			if err != nil {
				return
			}
			sent += n
		}
		io.WriteString(w, "data: [DONE]\n\n")
	}))
	defer srv.Close()
	c := New(Config{Name: "x", BaseURL: srv.URL})
	resp, err := c.Do(context.Background(), &provider.Request{Prompt: secRevPrompt("hi")}, nil)
	if err == nil {
		got := len(resp.Turn.PlainText())
		t.Logf("client accepted a %d MiB assistant message", got>>20)
		t.Errorf("S30: streamed %d MiB into one assistant turn without error; no max-response-bytes for streams", got>>20)
	}
}

// S31 (fixed): error messages from the endpoint were passed on verbatim: a JSON error.message
// was uncapped (only the non-JSON fallback was cut at 400 bytes) and control/escape bytes
// reached the event log and the terminal. They are now capped and sanitised; more cases in
// errors_test.go and in the provider package's sanitiser tests.
func TestSec_S31_ErrorMessageIsCappedAndSanitised(t *testing.T) {
	huge := strings.Repeat("x", 900_000)
	body := fmt.Sprintf(`{"error":{"message":"%s\u001b]52;c;ZXZpbA==\u0007\u001b[2J"}}`, huge)
	pe := mapHTTPError(400, http.Header{}, []byte(body))
	t.Logf("error message is %d bytes; error string %d bytes", len(pe.Message), len(pe.Error()))
	if len(pe.Error()) > 4096 {
		t.Errorf("S31: provider error text of %d bytes is logged (model.error event) and printed", len(pe.Error()))
	}
	if strings.ContainsRune(pe.Error(), 0x1b) {
		t.Errorf("S31: provider error text carries ESC bytes (terminal escape injection: OSC 52 clipboard write, screen clear)")
	}
}

// ---- sound behaviour ----------------------------------------------------------------

// The Authorization header is never part of a logged error string and the API key never
// appears in request URLs; the key is only sent as a Bearer header.
func TestSecSound_APIKeyOnlyInAuthorizationHeader(t *testing.T) {
	var mu sync.Mutex
	var gotURL, gotAuth, gotSess string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		gotURL, gotAuth, gotSess = r.URL.String(), r.Header.Get("Authorization"), r.Header.Get("X-Session-Id")
		mu.Unlock()
		w.WriteHeader(401)
		fmt.Fprint(w, `{"error":{"message":"bad key"}}`)
	}))
	defer srv.Close()
	c := New(Config{Name: "x", BaseURL: srv.URL, APIKey: "sk-secret-canary", Options: Options{SessionHeader: true}})
	p := secRevPrompt("hi")
	p.CacheKey = "sl:abcd1234:0123456789ab:0"
	_, err := c.Do(context.Background(), &provider.Request{Prompt: p, NoStream: true}, nil)
	if err == nil {
		t.Fatal("expected an error")
	}
	mu.Lock()
	defer mu.Unlock()
	if strings.Contains(gotURL, "sk-secret-canary") || strings.Contains(err.Error(), "sk-secret-canary") || strings.Contains(c.String(), "sk-secret-canary") {
		t.Fatalf("api key leaked into url/error/String: %q %v", gotURL, err)
	}
	if gotAuth != "Bearer sk-secret-canary" {
		t.Fatalf("Authorization = %q", gotAuth)
	}
	t.Logf("X-Session-Id sent to the gateway: %q", gotSess)
}

// Malformed and hostile SSE frames never panic the reader.
func TestSecSound_StreamParserSurvivesGarbage(t *testing.T) {
	frames := []string{
		"data: {not json}\n\n", "data: \n\n", ": ping\n\n", "event: x\n\n", "data: {\"choices\":[{\"delta\":null}]}\n\n",
		"data: {\"choices\":[{\"delta\":{\"tool_calls\":[{\"index\":-5,\"function\":{\"arguments\":\"{\"}}]}}]}\n\n",
		"data: {\"choices\":[{\"delta\":{\"tool_calls\":[{\"index\":99999999,\"id\":\"a\",\"function\":{\"name\":\"n\",\"arguments\":\"{}\"}}]}}]}\n\n",
		"data: {\"usage\":{\"prompt_tokens\":-1,\"completion_tokens\":9223372036854775807}}\n\n",
		"data: [DONE]\n\n",
	}
	acc, err := readStream(strings.NewReader(strings.Join(frames, "")), time.Now(), func(provider.Event) {})
	if err != nil {
		t.Fatal(err)
	}
	turn, _ := acc.build(fallbackToolID)
	if len(turn.ToolCalls()) != 2 {
		t.Fatalf("tool calls = %d", len(turn.ToolCalls()))
	}
}
