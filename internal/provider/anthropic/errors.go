package anthropic

import (
	"context"
	"encoding/json"
	"errors"
	"math"
	"net"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/reee344/sleipnir/internal/provider"
)

// errorEnvelope is the Messages API error body:
//
//	{"type":"error","error":{"type":"overloaded_error","message":"Overloaded"},"request_id":"req_..."}
//
// Gateways sometimes answer with OpenAI-style or plain-text bodies; both are
// tolerated and fall back to the raw text.
type errorEnvelope struct {
	Error struct {
		Type    string `json:"type"`
		Message string `json:"message"`
	} `json:"error"`
	RequestID string `json:"request_id"`
}

// mapHTTPError turns a non-200 response into a provider.Error.
func mapHTTPError(status int, h http.Header, body []byte, now time.Time, maxRetryAfter time.Duration) *provider.Error {
	var env errorEnvelope
	_ = json.Unmarshal(body, &env)
	msg := strings.TrimSpace(env.Error.Message)
	if msg == "" {
		msg = strings.TrimSpace(string(body))
		if len(msg) > 400 {
			msg = msg[:400] + "..."
		}
	}
	if msg == "" {
		msg = http.StatusText(status)
	}
	if id := firstNonEmpty(h.Get("Request-Id"), env.RequestID); id != "" {
		msg += " (request_id " + id + ")"
	}
	e := &provider.Error{Status: status, Message: msg, Raw: append(json.RawMessage(nil), body...)}
	e.Kind = kindFor(status, env.Error.Type, msg)
	if e.Kind == provider.ErrRateLimit || e.Kind == provider.ErrOverloaded || e.Kind == provider.ErrServer {
		e.RetryAfter = parseRetryAfter(h, now, maxRetryAfter, e.Kind == provider.ErrRateLimit)
	}
	return e
}

// inBandError maps an error event received inside a 200 stream. The stream had
// already started, so there is no HTTP status; Status stays zero and the API's
// error type leads the message.
func inBandError(typ, msg string, raw []byte) *provider.Error {
	e := &provider.Error{Message: strings.TrimSpace(typ + ": " + msg), Raw: append(json.RawMessage(nil), raw...)}
	e.Kind = kindFor(0, typ, msg)
	return e
}

// kindFor classifies a failure from its HTTP status and API error type. The
// type wins where the two disagree: gateways rewrite statuses, they rarely
// rewrite error bodies (and Anthropic asks them not to).
func kindFor(status int, typ, msg string) provider.ErrKind {
	lower := strings.ToLower(msg)
	switch {
	case typ == "overloaded_error" || status == 529:
		return provider.ErrOverloaded
	case typ == "billing_error" || status == 402 || creditExhausted(lower):
		return provider.ErrPayment
	case typ == "authentication_error" || typ == "permission_error" || status == 401 || status == 403:
		return provider.ErrAuth
	case typ == "rate_limit_error" || status == 429:
		return provider.ErrRateLimit
	case typ == "timeout_error" || status == 408:
		return provider.ErrTimeout
	case typ == "request_too_large" || status == 413:
		// A body over the size limit is fixed the same way as a prompt over the
		// window: send less. The agent answers ErrContextLength by compacting.
		return provider.ErrContextLength
	case typ == "api_error" || status >= 500:
		return provider.ErrServer
	case typ == "not_found_error" || status == 404:
		return provider.ErrBadRequest
	case typ == "invalid_request_error" || (status >= 400 && status < 500):
		return classify400(lower)
	case status == 0:
		// An unrecognised error event inside a live stream: assume the server
		// side failed and let the caller retry.
		return provider.ErrServer
	}
	return provider.ErrUnknown
}

// classify400 separates the 400s that have a specific recovery from the rest.
func classify400(lower string) provider.ErrKind {
	switch {
	case creditExhausted(lower):
		return provider.ErrPayment
	case contextTooLong(lower):
		return provider.ErrContextLength
	case thinkingBinding(lower):
		return provider.ErrThinkingBinding
	}
	return provider.ErrBadRequest
}

// creditExhausted: the API reports an empty balance as a 400, not a 402.
func creditExhausted(lower string) bool {
	return strings.Contains(lower, "credit balance is too low")
}

func contextTooLong(lower string) bool {
	for _, s := range []string{
		"prompt is too long", "context window", "context length", "maximum context",
		"exceed context limit", "exceeds the context", "input is too long", "too many tokens",
	} {
		if strings.Contains(lower, s) {
			return true
		}
	}
	return false
}

// thinkingBinding recognises the rejection of a replayed thinking block whose
// history changed (or that was edited): the recovery is to strip thinking
// durably or ask for drop_block, never to resend the same body.
//
// A field the endpoint does not know ("block_binding: Extra inputs are not
// permitted") is a different failure, a missing beta header, and is a plain bad
// request.
func thinkingBinding(lower string) bool {
	if strings.Contains(lower, "extra inputs are not permitted") {
		return false
	}
	for _, s := range []string{
		"bound to a different conversation",
		"invalid `signature`",
		"cannot be modified",
		"expected `thinking` or `redacted_thinking`",
	} {
		if strings.Contains(lower, s) && strings.Contains(lower, "thinking") {
			return true
		}
	}
	if strings.Contains(lower, "thinking") {
		for _, s := range []string{"prefix", "binding", "bound", "signature"} {
			if strings.Contains(lower, s) {
				return true
			}
		}
	}
	return false
}

func firstNonEmpty(ss ...string) string {
	for _, s := range ss {
		if s != "" {
			return s
		}
	}
	return ""
}

// parseRetryAfter reads how long to wait from Retry-After (seconds, decimal
// seconds or an HTTP date) and, for rate limits without one, from the
// anthropic-ratelimit-*-reset timestamps of the buckets that are exhausted.
//
// The result is capped at max. A misconfigured gateway or a daily quota can ask
// for hours, and the agent trusts this value over its own backoff: an absurd
// number would park an agent, and with it possibly the whole swarm's queue.
func parseRetryAfter(h http.Header, now time.Time, max time.Duration, useReset bool) time.Duration {
	if max <= 0 {
		max = 2 * time.Minute
	}
	clamp := func(d time.Duration) time.Duration {
		switch {
		case d < 0:
			return 0
		case d > max:
			return max
		}
		return d
	}
	if v := strings.TrimSpace(h.Get("Retry-After")); v != "" {
		if f, err := strconv.ParseFloat(v, 64); err == nil && !math.IsNaN(f) {
			switch {
			case f <= 0:
				return 0
			case f >= max.Seconds():
				return max
			}
			return clamp(time.Duration(f * float64(time.Second)))
		}
		if t, err := http.ParseTime(v); err == nil {
			return clamp(t.Sub(now))
		}
	}
	if !useReset {
		return 0
	}
	var exhausted, all []time.Duration
	for name := range h {
		lname := strings.ToLower(name)
		const pre, suf = "anthropic-ratelimit-", "-reset"
		if !strings.HasPrefix(lname, pre) || !strings.HasSuffix(lname, suf) {
			continue
		}
		t, err := time.Parse(time.RFC3339, strings.TrimSpace(h.Get(name)))
		if err != nil {
			continue
		}
		d := t.Sub(now)
		all = append(all, d)
		bucket := strings.TrimSuffix(strings.TrimPrefix(lname, pre), suf)
		if rem, err := strconv.Atoi(strings.TrimSpace(h.Get(pre + bucket + "-remaining"))); err == nil && rem <= 0 {
			exhausted = append(exhausted, d)
		}
	}
	switch {
	case len(exhausted) > 0:
		// Every exhausted bucket must refill before the request can succeed.
		return clamp(maxDur(exhausted))
	case len(all) > 0:
		return clamp(minDur(all))
	}
	return 0
}

func maxDur(ds []time.Duration) time.Duration {
	m := ds[0]
	for _, d := range ds[1:] {
		m = max(m, d)
	}
	return m
}

func minDur(ds []time.Duration) time.Duration {
	m := ds[0]
	for _, d := range ds[1:] {
		m = min(m, d)
	}
	return m
}

// transportError classifies a failure that happened below HTTP. parent is the
// caller's context; watchdog reports that our own idle timer fired. The two must
// not be confused: the derived context is cancelled in both cases, but only the
// caller cancelling is "cancelled" - a silent stream is a timeout, and is retried.
func transportError(parent context.Context, watchdog bool, idle time.Duration, err error) *provider.Error {
	switch {
	case watchdog:
		return &provider.Error{Kind: provider.ErrTimeout, Message: "no data from the server for " + idle.String(), Err: err}
	case errors.Is(parent.Err(), context.Canceled):
		return &provider.Error{Kind: provider.ErrNetwork, Message: "request cancelled", Err: parent.Err()}
	case errors.Is(parent.Err(), context.DeadlineExceeded):
		return &provider.Error{Kind: provider.ErrTimeout, Message: "deadline exceeded", Err: parent.Err()}
	}
	var ne net.Error
	if errors.As(err, &ne) && ne.Timeout() {
		return &provider.Error{Kind: provider.ErrTimeout, Message: err.Error(), Err: err}
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return &provider.Error{Kind: provider.ErrTimeout, Message: err.Error(), Err: err}
	}
	return &provider.Error{Kind: provider.ErrNetwork, Message: err.Error(), Err: err}
}
