package openaichat

// Security review repros for docs/reviews/security-robustness.md.
//
// TestSecReview_* are gated behind SLEIPNIR_REVIEW=1 and assert the SECURE behaviour, so
// they FAIL while the finding is open:
//
//	SLEIPNIR_REVIEW=1 go test -count=1 -run TestSecReview ./internal/provider/openaichat
//
// TestSecSound_* are ungated regression checks for behaviour the review found sound.

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/reee344/sleipnir/internal/core"
	"github.com/reee344/sleipnir/internal/provider"
)

func secRevGate(t *testing.T) {
	t.Helper()
	if os.Getenv("SLEIPNIR_REVIEW") == "" {
		t.Skip("security-review repro: set SLEIPNIR_REVIEW=1 (asserts the secure behaviour, fails while the finding is open)")
	}
}

func secRevPrompt(text string) *core.Prompt {
	return &core.Prompt{Model: "m", Messages: []core.Message{{Role: core.RoleUser, Blocks: []core.Block{core.Text(text)}}}}
}

// S26a: Retry-After is parsed as seconds with no upper bound and no overflow check. The
// governor turns it into a swarm-wide pause (see swarm S26b), and agent.backoff sleeps it.
func TestSecReview_S26a_RetryAfterIsUnboundedAndOverflows(t *testing.T) {
	secRevGate(t)
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

// S28: usage numbers are trusted verbatim: a negative completion_tokens/cost makes the
// agent's and the swarm's dollar budgets unreachable (fail open).
func TestSecReview_S28_NegativeUsageAndCostAreAccepted(t *testing.T) {
	secRevGate(t)
	neg := -1.0e6
	u := &usage{PromptTokens: 100, CompletionTokens: -5_000_000, Cost: &neg}
	n := u.normalize()
	t.Logf("normalised usage: %+v cost=%v", n, *u.Cost)
	if n.OutputTokens < 0 {
		t.Errorf("S28: negative completion_tokens survived normalisation (OutputTokens=%d)", n.OutputTokens)
	}
	if u.Cost != nil && *u.Cost < 0 {
		t.Errorf("S28: negative usage.cost (%v) is passed on as the exact charge and added to the agent's spend", *u.Cost)
	}
}

// S29: streamed reasoning becomes a thinking block whose text Turn.PlainText() joins in front
// of the answer; kv.ParsePatch is fed PlainText().
func TestSecReview_S29_ReasoningIsPartOfPlainText(t *testing.T) {
	secRevGate(t)
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

// S27: the client follows redirects. A 307/308 re-POSTs the full prompt to the new host and
// forwards every custom header (only Authorization/Cookie are stripped by net/http).
func TestSecReview_S27_RedirectReplaysPromptAndCustomHeaders(t *testing.T) {
	secRevGate(t)
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

// S30: the streaming path has no byte cap (the non-streaming path caps at 64 MiB). A gateway
// that streams forever fills memory: text, reasoning, tool-call args and the SSE line buffer
// are all unbounded strings.Builders.
func TestSecReview_S30_StreamingResponseIsUnbounded(t *testing.T) {
	secRevGate(t)
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

// S31: error messages from the endpoint are passed on verbatim: a JSON error.message is
// uncapped (only the non-JSON fallback is cut at 400 bytes) and control/escape bytes
// reach the event log and the terminal.
func TestSecReview_S31_ErrorMessageIsUncappedAndUnsanitised(t *testing.T) {
	secRevGate(t)
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
