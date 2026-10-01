package anthropic_test

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/anemos-labs/sleipnir/internal/provider"
	"github.com/anemos-labs/sleipnir/internal/provider/anthropic"
)

// apiErr is the API's error body.
func apiErr(typ, msg string) string {
	return fmt.Sprintf(`{"type":"error","error":{"type":%q,"message":%q},"request_id":"req_fixture_1"}`, typ, msg)
}

func serveError(t *testing.T, status int, headers http.Header, body string) *httptest.Server {
	t.Helper()
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		for k, v := range headers {
			w.Header()[k] = v
		}
		w.WriteHeader(status)
		w.Write([]byte(body))
	}))
	t.Cleanup(ts.Close)
	return ts
}

func doErr(t *testing.T, ts *httptest.Server, cfg anthropic.Config) error {
	t.Helper()
	cfg.BaseURL = ts.URL
	_, err := anthropic.New(cfg).Do(context.Background(), &provider.Request{Prompt: hello("m")}, nil)
	if err == nil {
		t.Fatal("want an error")
	}
	return err
}

const (
	bindMsg    = "messages.5.content.0: Invalid `signature` in `thinking` block. The block is bound to a different conversation. Remove the block, or set `thinking.block_binding.prefix_mismatch_behavior` to \"drop_block\". That setting requires the `thinking-binding-controls-2026-08-01` value in the `anthropic-beta` header."
	bindMsgNew = "messages.5.content.0: Invalid `signature` in `thinking` block. The block is bound to a different conversation. Remove the block, or set `thinking.block_binding.prefix_mismatch_behavior` to \"drop_block\"."
)

func TestErrorMapping(t *testing.T) {
	h := func(kv ...string) http.Header {
		out := http.Header{}
		for i := 0; i+1 < len(kv); i += 2 {
			out.Set(kv[i], kv[i+1])
		}
		return out
	}
	cases := []struct {
		name      string
		status    int
		header    http.Header
		body      string
		kind      provider.ErrKind
		retryable bool
		contains  string
	}{
		{"401", 401, nil, apiErr("authentication_error", "invalid x-api-key"), provider.ErrAuth, false, "invalid x-api-key"},
		{"403", 403, nil, apiErr("permission_error", "not allowed"), provider.ErrAuth, false, "not allowed"},
		{"402", 402, nil, apiErr("billing_error", "billing problem"), provider.ErrPayment, false, "billing problem"},
		{"billing_error type on another status", 400, nil, apiErr("billing_error", "x"), provider.ErrPayment, false, ""},
		{"empty balance is a 400 on the real API", 400, nil, apiErr("invalid_request_error", "Your credit balance is too low to access the Anthropic API."), provider.ErrPayment, false, ""},
		{"429", 429, h("Retry-After", "7"), apiErr("rate_limit_error", "slow down"), provider.ErrRateLimit, true, "slow down"},
		{"529", 529, nil, apiErr("overloaded_error", "Overloaded"), provider.ErrOverloaded, true, "Overloaded"},
		{"overloaded type wins over the status", 400, nil, apiErr("overloaded_error", "Overloaded"), provider.ErrOverloaded, true, ""},
		{"529 without a body", 529, nil, "", provider.ErrOverloaded, true, "HTTP 529"},
		{"500", 500, nil, apiErr("api_error", "Internal server error"), provider.ErrServer, true, "Internal server error"},
		{"502 html from a proxy", 502, h("Content-Type", "text/html"), "<html><body><h1>502 Bad Gateway</h1>" + strings.Repeat("padding ", 200) + "</body></html>", provider.ErrServer, true, "502 Bad Gateway"},
		{"503 plain text", 503, nil, "upstream connect error", provider.ErrServer, true, "upstream connect error"},
		{"504", 504, nil, "", provider.ErrServer, true, ""},
		{"408", 408, nil, "", provider.ErrTimeout, true, ""},
		{"timeout_error", 504, nil, apiErr("timeout_error", "Request timed out"), provider.ErrTimeout, true, ""},
		{"413", 413, nil, apiErr("request_too_large", "Request exceeds the maximum allowed number of bytes."), provider.ErrContextLength, false, ""},
		{"prompt too long", 400, nil, apiErr("invalid_request_error", "prompt is too long: 250000 tokens > 200000 maximum"), provider.ErrContextLength, false, "prompt is too long"},
		{"input plus max_tokens over the window", 400, nil, apiErr("invalid_request_error", "input length and `max_tokens` exceed context limit: 188240 + 21333 > 200000, decrease input length or `max_tokens` and try again"), provider.ErrContextLength, false, ""},
		{"thinking binding, no beta header sent", 400, nil, apiErr("invalid_request_error", bindMsg), provider.ErrThinkingBinding, false, "bound to a different conversation"},
		{"thinking binding, beta header sent", 400, nil, apiErr("invalid_request_error", bindMsgNew), provider.ErrThinkingBinding, false, ""},
		{"thinking binding, first failing message named", 400, nil, apiErr("invalid_request_error", bindMsgNew+" messages.2 changed."), provider.ErrThinkingBinding, false, ""},
		{"edited thinking block", 400, nil, apiErr("invalid_request_error", "`thinking` or `redacted_thinking` blocks in the latest assistant message cannot be modified. These blocks must remain as they were in the original response."), provider.ErrThinkingBinding, false, ""},
		{"missing thinking block", 400, nil, apiErr("invalid_request_error", "messages.1.content.0.type: Expected `thinking` or `redacted_thinking`, but found `text`."), provider.ErrThinkingBinding, false, ""},
		{"tampered signature", 400, nil, apiErr("invalid_request_error", "messages.1.content.0: Invalid `signature` in `thinking` block."), provider.ErrThinkingBinding, false, ""},
		{"a gateway phrase", 400, nil, apiErr("invalid_request_error", "thinking block prefix mismatch"), provider.ErrThinkingBinding, false, ""},
		{"block_binding without its beta is not a binding failure", 400, nil, apiErr("invalid_request_error", "thinking.adaptive.block_binding: Extra inputs are not permitted"), provider.ErrBadRequest, false, ""},
		{"thinking config rejected by the model", 400, nil, apiErr("invalid_request_error", `"thinking.type.disabled" is not supported for this model. Use "thinking.type.adaptive" and "output_config.effort" to control thinking behavior.`), provider.ErrBadRequest, false, ""},
		{"plain bad request", 400, nil, apiErr("invalid_request_error", "messages: roles must alternate"), provider.ErrBadRequest, false, "roles must alternate"},
		{"not found", 404, nil, apiErr("not_found_error", "model: claude-nope"), provider.ErrBadRequest, false, "claude-nope"},
		{"422", 422, nil, `{"detail":"unprocessable"}`, provider.ErrBadRequest, false, "unprocessable"},
		{"OpenAI-style gateway error body", 400, nil, `{"error":{"message":"bad param","type":"invalid_request_error","code":"x"}}`, provider.ErrBadRequest, false, "bad param"},
		{"unexpected redirect status", 302, nil, "", provider.ErrUnknown, false, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ts := serveError(t, tc.status, tc.header, tc.body)
			err := doErr(t, ts, anthropic.Config{})
			pe := wantKind(t, err, tc.kind, tc.retryable)
			if pe.Status != tc.status {
				t.Errorf("status = %d", pe.Status)
			}
			if !strings.Contains(pe.Message, tc.contains) {
				t.Errorf("message = %q, want %q", pe.Message, tc.contains)
			}
			if len(pe.Message) > 600 {
				t.Errorf("message not truncated (%d bytes)", len(pe.Message))
			}
			if string(pe.Raw) != tc.body {
				t.Errorf("raw body must be kept verbatim")
			}
		})
	}
	t.Run("the request id is reported", func(t *testing.T) {
		ts := serveError(t, 500, http.Header{"Request-Id": {"req_header_9"}}, apiErr("api_error", "boom"))
		if pe, _ := provider.AsError(doErr(t, ts, anthropic.Config{})); !strings.Contains(pe.Message, "req_header_9") {
			t.Errorf("message = %q", pe.Message)
		}
		ts = serveError(t, 500, nil, apiErr("api_error", "boom"))
		if pe, _ := provider.AsError(doErr(t, ts, anthropic.Config{})); !strings.Contains(pe.Message, "req_fixture_1") {
			t.Errorf("message = %q", pe.Message)
		}
	})
}

func TestRetryAfter(t *testing.T) {
	date := func(d time.Duration) string { return time.Now().Add(d).UTC().Format(http.TimeFormat) }
	stamp := func(d time.Duration) string { return time.Now().Add(d).UTC().Format(time.RFC3339) }
	within := func(got, lo, hi time.Duration) bool { return got >= lo && got <= hi }
	rl := func(kv ...string) http.Header {
		h := http.Header{}
		for i := 0; i+1 < len(kv); i += 2 {
			h.Set(kv[i], kv[i+1])
		}
		return h
	}
	cases := []struct {
		name     string
		status   int
		header   http.Header
		cfg      anthropic.Config
		lo, hi   time.Duration
		wantKind provider.ErrKind
	}{
		{"seconds", 429, rl("Retry-After", "7"), anthropic.Config{}, 7 * time.Second, 7 * time.Second, provider.ErrRateLimit},
		{"decimal seconds", 429, rl("Retry-After", "1.5"), anthropic.Config{}, 1500 * time.Millisecond, 1500 * time.Millisecond, provider.ErrRateLimit},
		{"http date", 429, rl("Retry-After", date(10*time.Second)), anthropic.Config{}, 7 * time.Second, 10 * time.Second, provider.ErrRateLimit},
		{"a date in the past", 429, rl("Retry-After", date(-time.Hour)), anthropic.Config{}, 0, 0, provider.ErrRateLimit},
		{"zero", 429, rl("Retry-After", "0"), anthropic.Config{}, 0, 0, provider.ErrRateLimit},
		{"negative", 429, rl("Retry-After", "-5"), anthropic.Config{}, 0, 0, provider.ErrRateLimit},
		{"absurd seconds are capped", 429, rl("Retry-After", "99999999"), anthropic.Config{}, 2 * time.Minute, 2 * time.Minute, provider.ErrRateLimit},
		{"astronomical", 429, rl("Retry-After", "1e300"), anthropic.Config{}, 2 * time.Minute, 2 * time.Minute, provider.ErrRateLimit},
		{"infinity", 429, rl("Retry-After", "Inf"), anthropic.Config{}, 2 * time.Minute, 2 * time.Minute, provider.ErrRateLimit},
		{"NaN is ignored", 429, rl("Retry-After", "NaN"), anthropic.Config{}, 0, 0, provider.ErrRateLimit},
		{"garbage is ignored", 429, rl("Retry-After", "soon"), anthropic.Config{}, 0, 0, provider.ErrRateLimit},
		{"an absurd date is capped", 429, rl("Retry-After", date(30*24*time.Hour)), anthropic.Config{}, 2 * time.Minute, 2 * time.Minute, provider.ErrRateLimit},
		{"the cap is configurable", 429, rl("Retry-After", "600"), anthropic.Config{MaxRetryAfter: 10 * time.Second}, 10 * time.Second, 10 * time.Second, provider.ErrRateLimit},
		{"529 honours it too", 529, rl("Retry-After", "3"), anthropic.Config{}, 3 * time.Second, 3 * time.Second, provider.ErrOverloaded},
		{"500 honours it too", 500, rl("Retry-After", "4"), anthropic.Config{}, 4 * time.Second, 4 * time.Second, provider.ErrServer},
		{"reset headers when there is no Retry-After: the exhausted bucket decides",
			429, rl(
				"anthropic-ratelimit-requests-remaining", "10", "anthropic-ratelimit-requests-reset", stamp(5*time.Second),
				"anthropic-ratelimit-tokens-remaining", "0", "anthropic-ratelimit-tokens-reset", stamp(30*time.Second),
				"anthropic-ratelimit-input-tokens-remaining", "0", "anthropic-ratelimit-input-tokens-reset", stamp(20*time.Second)),
			anthropic.Config{}, 25 * time.Second, 30 * time.Second, provider.ErrRateLimit},
		{"reset headers with nothing exhausted: the earliest",
			429, rl(
				"anthropic-ratelimit-requests-remaining", "10", "anthropic-ratelimit-requests-reset", stamp(45*time.Second),
				"anthropic-ratelimit-tokens-remaining", "5", "anthropic-ratelimit-tokens-reset", stamp(20*time.Second)),
			anthropic.Config{}, 15 * time.Second, 20 * time.Second, provider.ErrRateLimit},
		{"an absurd reset is capped", 429, rl("anthropic-ratelimit-tokens-remaining", "0", "anthropic-ratelimit-tokens-reset", "2099-01-01T00:00:00Z"), anthropic.Config{}, 2 * time.Minute, 2 * time.Minute, provider.ErrRateLimit},
		{"a reset in the past", 429, rl("anthropic-ratelimit-tokens-remaining", "0", "anthropic-ratelimit-tokens-reset", "2001-01-01T00:00:00Z"), anthropic.Config{}, 0, 0, provider.ErrRateLimit},
		{"a garbled reset is ignored", 429, rl("anthropic-ratelimit-tokens-remaining", "0", "anthropic-ratelimit-tokens-reset", "tomorrow"), anthropic.Config{}, 0, 0, provider.ErrRateLimit},
		{"Retry-After beats the reset headers", 429, rl("Retry-After", "2", "anthropic-ratelimit-tokens-remaining", "0", "anthropic-ratelimit-tokens-reset", stamp(50*time.Second)), anthropic.Config{}, 2 * time.Second, 2 * time.Second, provider.ErrRateLimit},
		{"reset headers only guide rate limits", 500, rl("anthropic-ratelimit-tokens-remaining", "0", "anthropic-ratelimit-tokens-reset", stamp(50*time.Second)), anthropic.Config{}, 0, 0, provider.ErrServer},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ts := serveError(t, tc.status, tc.header, apiErr(map[provider.ErrKind]string{provider.ErrRateLimit: "rate_limit_error", provider.ErrOverloaded: "overloaded_error", provider.ErrServer: "api_error"}[tc.wantKind], "wait"))
			pe := wantKind(t, doErr(t, ts, tc.cfg), tc.wantKind, true)
			if !within(pe.RetryAfter, tc.lo, tc.hi) {
				t.Errorf("RetryAfter = %v, want %v..%v", pe.RetryAfter, tc.lo, tc.hi)
			}
		})
	}
}

func TestResponseHeadersAreReportedOnErrorsToo(t *testing.T) {
	ts := serveError(t, 429, http.Header{"Retry-After": {"1"}, "Anthropic-Ratelimit-Tokens-Remaining": {"0"}}, apiErr("rate_limit_error", "x"))
	var got http.Header
	doErr(t, ts, anthropic.Config{OnHeaders: func(h http.Header) { got = h }})
	if got.Get("Anthropic-Ratelimit-Tokens-Remaining") != "0" {
		t.Errorf("headers = %v", got)
	}
}
