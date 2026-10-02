package provider

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// What the adapters of OpenAI-style endpoints (chat completions, Responses) share when a request fails: the status of the answer, or the
// failure of the transport, as the *Error the agent acts on.

// HTTPError turns a non-200 answer into an *Error. The body is the vendor's, so its text is bounded and cleaned before it is kept;
// the kind comes from the status (and, for a 400, from words that say the prompt was too long).
func HTTPError(status int, h http.Header, body []byte) *Error {
	e := &Error{Status: status, Raw: CapRaw(body)}
	var env struct {
		Error struct {
			Message  string `json:"message"`
			Metadata struct {
				ProviderMessage string `json:"provider_message"`
				ProviderName    string `json:"provider_name"`
			} `json:"metadata"`
		} `json:"error"`
	}
	_ = json.Unmarshal(body, &env)
	msg := env.Error.Message
	if msg == "" {
		msg = strings.TrimSpace(string(body))
		if len(msg) > 400 {
			msg = msg[:400]
		}
	}
	if pm := env.Error.Metadata.ProviderMessage; pm != "" {
		msg += " (" + pm + ")"
	}
	// A marketplace names the provider whose attempt failed: with several behind one model id, that
	// is what tells a person to route around it (a whole provider was down for a real swarm run).
	if pn := env.Error.Metadata.ProviderName; pn != "" {
		msg += " [provider " + pn + "]"
	}
	// The text is the endpoint's, and it goes to the event log, the terminal and
	// possibly to other agents: bounded, and stripped of control characters,
	// escape sequences and invisible characters.
	msg = SanitizeText(msg, MaxErrorText)
	if msg == "" {
		msg = strings.TrimSpace(fmt.Sprintf("HTTP %d %s", status, http.StatusText(status)))
	}
	e.Message = msg
	if d, ok := ParseRetryAfter(h.Get("Retry-After"), time.Now(), 0); ok {
		e.RetryAfter = d
	}
	lower := strings.ToLower(msg)
	switch {
	case status == 401 || status == 403:
		e.Kind = ErrAuth
	case status == 402:
		e.Kind = ErrPayment
	case status == 408:
		e.Kind = ErrTimeout
	case status == 429:
		e.Kind = ErrRateLimit
	case status == 529:
		e.Kind = ErrOverloaded
	case status >= 500:
		e.Kind = ErrServer
	case status == 400 || status == 404 || status == 413 || status == 422:
		e.Kind = ErrBadRequest
		if strings.Contains(lower, "context length") || strings.Contains(lower, "context_length") ||
			strings.Contains(lower, "maximum context") || strings.Contains(lower, "too many tokens") ||
			strings.Contains(lower, "exceeds the context") || status == 413 {
			e.Kind = ErrContextLength
		}
	default:
		e.Kind = ErrUnknown
	}
	return e
}

// TransportError classifies a failure that happened below HTTP. parent is the caller's context: its cancellation is "cancelled" (which
// the agent does not retry: it has stopped anyway), its deadline a timeout.
func TransportError(parent context.Context, err error) *Error {
	msg := SanitizeText(err.Error(), 0)
	if errors.Is(parent.Err(), context.Canceled) && !errors.Is(err, context.DeadlineExceeded) {
		return &Error{Kind: ErrNetwork, Message: "request cancelled", Err: err}
	}
	var ne net.Error
	if errors.As(err, &ne) && ne.Timeout() {
		return &Error{Kind: ErrTimeout, Message: msg, Err: err}
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return &Error{Kind: ErrTimeout, Message: msg, Err: err}
	}
	return &Error{Kind: ErrNetwork, Message: plainNetwork(err, msg), Err: err}
}

// plainNetwork says a failure to reach the endpoint in words a person can act on ("cannot connect to 127.0.0.1:9: connection refused (is the
// server running?)") where Go's own text is `Post "http://127.0.0.1:9/api/v1/chat/completions": dial tcp 127.0.0.1:9: connect: connection
// refused`. What it does not recognise it leaves as it is.
func plainNetwork(err error, msg string) string {
	host := ""
	var ue *url.Error
	if errors.As(err, &ue) {
		if u, perr := url.Parse(ue.URL); perr == nil {
			host = u.Host
		}
	}
	if host == "" {
		return msg
	}
	lower := strings.ToLower(msg)
	var dns *net.DNSError
	switch {
	case strings.Contains(lower, "connection refused") || strings.Contains(lower, "actively refused"):
		return "cannot connect to " + host + ": connection refused (is the server running, and is the address right?)"
	case errors.As(err, &dns):
		return "cannot find " + host + " (" + dns.Err + "): check the address and your network"
	case strings.Contains(lower, "certificate"):
		return "the certificate of " + host + " is not accepted (" + SanitizeText(ue.Err.Error(), 120) + ")"
	}
	return msg
}

// KeyTransportError is the refusal to send a key where it would cross the network unencrypted (or where the URL is unusable). It is
// never retried.
func KeyTransportError(err error) *Error {
	msg := err.Error()
	var ins *InsecureKeyError
	if errors.As(err, &ins) {
		msg += "; use an https URL or a loopback address, or allow it deliberately for this provider (allow_insecure_http in your user config)"
	}
	return &Error{Kind: ErrBadRequest, Message: SanitizeText(msg, 0), Err: err, NoRetry: true}
}

// HTMLText is the words of a page, roughly: its tags taken out and its white space folded, enough for a one-line message about what a
// proxy, a captive portal or a gateway said instead of a stream.
func HTMLText(page string) string {
	var sb strings.Builder
	inTag := false
	for _, r := range page {
		switch {
		case r == '<':
			inTag = true
			sb.WriteByte(' ')
		case r == '>':
			inTag = false
		case !inTag:
			sb.WriteRune(r)
		}
	}
	return strings.Join(strings.Fields(sb.String()), " ")
}
