package openaichat

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/reee344/sleipnir/internal/core"
	"github.com/reee344/sleipnir/internal/cost"
	"github.com/reee344/sleipnir/internal/provider"
)

// Config configures a Client.
type Config struct {
	Name    string // profile name, e.g. "heimdall"
	BaseURL string // ends before /chat/completions
	APIKey  string
	Headers map[string]string
	Options Options
	// Profile overrides the default automatic-caching profile.
	Profile *provider.Profile
	// FirstByteTimeout bounds the wait from sending a streaming request until the
	// first byte of the response body arrives (a keep-alive comment counts). A
	// server that accepts the request and never answers is otherwise a request
	// that never ends. Default: StreamIdleTimeout when that is set, else 120s,
	// which is generous for a reasoning model that says nothing until it has
	// finished thinking. Expiry is a provider.ErrTimeout, which the agent retries;
	// a request that got no response at all twice is not retried a third time.
	FirstByteTimeout time.Duration
	// StreamIdleTimeout aborts a stream that goes silent once it has started
	// (keep-alive comments count as activity). Default 60s.
	StreamIdleTimeout time.Duration
	// RequestTimeout bounds a non-streaming call (warm-ups, keep-alives) from the
	// moment it is sent until its body is read. Default 10 minutes.
	RequestTimeout time.Duration
	// Limits bounds what one response may cost in bytes, events, text, tool calls
	// and time (zero fields take the provider defaults).
	Limits provider.StreamLimits
	// AllowInsecureHTTP lets the API key travel over plain http to a host that is
	// not this machine (a trusted LAN proxy). By default a key is only sent over
	// https, or over http to a loopback address.
	AllowInsecureHTTP bool
	// OnHeaders receives every response's headers (rate-limit accounting).
	OnHeaders func(http.Header)
	// HTTPClient carries the requests. Its redirect policy is tightened: a redirect
	// to another origin is refused whatever the client says.
	HTTPClient *http.Client
}

// Client implements provider.Provider for chat completions.
type Client struct {
	cfg     Config
	profile provider.Profile
	http    *http.Client
	keyErr  error // set when the API key must not be sent to BaseURL
}

// New builds a Client.
func New(cfg Config) *Client {
	cfg.Options.defaults()
	idleSet := cfg.StreamIdleTimeout > 0
	if !idleSet {
		cfg.StreamIdleTimeout = provider.DefaultStreamIdleTimeout
	}
	if cfg.FirstByteTimeout <= 0 {
		cfg.FirstByteTimeout = provider.DefaultFirstByteTimeout
		if idleSet {
			cfg.FirstByteTimeout = cfg.StreamIdleTimeout
		}
	}
	if cfg.RequestTimeout <= 0 {
		cfg.RequestTimeout = provider.DefaultRequestTimeout
	}
	cfg.Limits = cfg.Limits.Normalized()
	cfg.BaseURL = strings.TrimRight(cfg.BaseURL, "/")
	hc := cfg.HTTPClient
	if hc == nil {
		hc = &http.Client{Transport: newTransport()}
	}
	prof := DefaultProfile(cfg.Name, cfg.BaseURL)
	if cfg.Profile != nil {
		prof = *cfg.Profile
	}
	c := &Client{cfg: cfg, profile: prof, http: provider.HardenClient(hc)}
	if cfg.APIKey != "" && !cfg.AllowInsecureHTTP {
		if err := provider.CheckKeyTransport(cfg.BaseURL); err != nil {
			c.keyErr = err
		}
	}
	return c
}

// newTransport returns a transport sized for many concurrent streams: a swarm
// multiplexes dozens of long-lived requests over a few HTTP/2 connections.
func newTransport() *http.Transport {
	return &http.Transport{
		Proxy:                 http.ProxyFromEnvironment,
		ForceAttemptHTTP2:     true,
		MaxIdleConns:          256,
		MaxIdleConnsPerHost:   128,
		IdleConnTimeout:       90 * time.Second,
		TLSHandshakeTimeout:   15 * time.Second,
		ExpectContinueTimeout: time.Second,
		DialContext:           (&net.Dialer{Timeout: 15 * time.Second, KeepAlive: 30 * time.Second}).DialContext,
	}
}

// DefaultProfile describes an automatic-prefix-caching chat endpoint. The
// numbers are conservative starting points; `sleipnir doctor` measures the
// real granularity and refines them.
func DefaultProfile(name, baseURL string) provider.Profile {
	return provider.Profile{
		Name:    name,
		Dialect: Dialect,
		BaseURL: baseURL,
		Cache: cost.CacheModel{
			Auto:             true,
			MinPrefixTokens:  64,
			Granularity:      16,
			TTLs:             []time.Duration{time.Hour},
			ReadRefreshesTTL: true,
			ReadableAfter:    cost.ReadableAtFirstByte,
			KeyRouting:       true,
		},
		StreamUsage: true,
	}
}

// Profile implements provider.Provider.
func (c *Client) Profile() provider.Profile { return c.profile }

// SetProfile replaces the profile (after a capability probe).
func (c *Client) SetProfile(p provider.Profile) { c.profile = p }

// Do implements provider.Provider.
func (c *Client) Do(ctx context.Context, req *provider.Request, on func(provider.Event)) (*provider.Response, error) {
	if on == nil {
		on = func(provider.Event) {}
	}
	if req == nil || req.Prompt == nil {
		return nil, &provider.Error{Kind: provider.ErrBadRequest, Message: "openaichat: request has no prompt"}
	}
	if c.keyErr != nil {
		return nil, keyTransportError(c.keyErr)
	}
	p := req.Prompt
	stream := !req.NoStream && !req.Warm
	if req.Warm {
		// Prefill is all a warm-up needs: one output token, no streaming.
		cp := *p
		cp.Params.MaxTokens = 1
		p = &cp
	}
	opts := c.cfg.Options
	opts.CaptureTokens = req.Capture && c.profile.CaptureTokens
	body, err := Build(p, opts, stream)
	if err != nil {
		return nil, &provider.Error{Kind: provider.ErrBadRequest, Message: err.Error(), Err: err}
	}

	parent := ctx
	start := time.Now()
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	// One watchdog for the whole exchange, armed before the request is sent: it
	// covers a server that accepts the connection and never answers, as well as one
	// that stalls mid-stream. It cancels the derived context, so it records why:
	// "the caller cancelled" and "the server went quiet" are handled differently.
	first, idle, total := c.cfg.FirstByteTimeout, c.cfg.StreamIdleTimeout, c.cfg.Limits.MaxDuration
	if !stream {
		first, idle, total = c.cfg.RequestTimeout, c.cfg.RequestTimeout, 0
	}
	wd := provider.NewWatchdog(cancel, first, idle, total, req)
	wd.Start()
	defer wd.Stop()

	hr, err := http.NewRequestWithContext(ctx, http.MethodPost, c.cfg.BaseURL+"/chat/completions", bytes.NewReader(body))
	if err != nil {
		return nil, &provider.Error{Kind: provider.ErrBadRequest, Message: provider.SanitizeText(err.Error(), 0), Err: err}
	}
	hr.Header.Set("Content-Type", "application/json")
	if stream {
		hr.Header.Set("Accept", "text/event-stream")
	}
	if c.cfg.APIKey != "" {
		hr.Header.Set("Authorization", "Bearer "+c.cfg.APIKey)
	}
	if c.cfg.Options.SessionHeader && p.CacheKey != "" {
		hr.Header.Set("X-Session-Id", p.CacheKey)
	}
	for k, v := range c.cfg.Headers {
		hr.Header.Set(k, v)
	}

	resp, err := c.http.Do(hr)
	if err != nil {
		return nil, c.failure(parent, wd, err)
	}
	defer resp.Body.Close()
	if c.cfg.OnHeaders != nil {
		c.cfg.OnHeaders(resp.Header)
	}
	rd := wd.Reader(resp.Body)
	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(io.LimitReader(rd, 1<<20))
		return nil, mapHTTPError(resp.StatusCode, resp.Header, b)
	}

	var acc *accumulator
	if stream {
		acc, err = readStreamLimits(rd, start, on, c.cfg.Limits)
		if err != nil {
			return nil, c.failure(parent, wd, err)
		}
	} else {
		b, rerr := provider.ReadCapped(rd, c.cfg.Limits.MaxBytes)
		if rerr != nil {
			return nil, c.failure(parent, wd, rerr)
		}
		acc = newAccumulatorLimits(c.cfg.Limits)
		var c2 chunk
		if err := json.Unmarshal(b, &c2); err != nil {
			return nil, &provider.Error{Kind: provider.ErrServer, Message: "unparseable response: " + provider.SanitizeText(err.Error(), 0), Raw: provider.CapRaw(b)}
		}
		if c2.Error != nil {
			return nil, mapInBandError(c2.Error)
		}
		if c2.Usage != nil {
			acc.rawUsage = rawUsageOf(b)
		}
		if err := acc.feed(&c2, start, on); err != nil {
			return nil, err
		}
	}

	if !acc.started {
		// A reply with no content frames at all (for example an empty
		// completion) still counts as started for gating purposes.
		on(provider.Event{Kind: provider.EvStart, RequestID: acc.id, Elapsed: time.Since(start)})
	}
	turn, stop := acc.build(fallbackToolID)
	out := &provider.Response{
		ID:       acc.id,
		Model:    acc.model,
		Turn:     turn,
		Usage:    acc.usage.normalize(), // also drops an unusable cost from acc.usage
		Stop:     stop,
		RawUsage: acc.rawUsage,
		Total:    time.Since(start),
		Provider: acc.provider,
	}
	if acc.usage != nil {
		out.CostUSD = provider.ValidCost(acc.usage.Cost)
	}
	if opts.CaptureTokens {
		out.Tokens = acc.trace(acc.model)
	}
	out.TTFB = out.Total
	on(provider.Event{Kind: provider.EvUsage, Usage: &out.Usage, RequestID: acc.id})
	return out, nil
}

// failure turns an error from the transport or the body into the provider error
// the caller sees. In order: a redirect that left the origin, a deadline of the
// watchdog (a timeout, never "cancelled"), an error the adapter already
// classified, and last the caller's own cancellation or deadline and plain
// network failures.
func (c *Client) failure(parent context.Context, wd *provider.Watchdog, err error) *provider.Error {
	if re, ok := provider.AsRedirect(err); ok {
		return provider.RedirectFailure(re)
	}
	if pe := wd.Failure(err); pe != nil {
		return pe
	}
	if parent.Err() == nil { // the caller has not given up: what the adapter already classified stands
		if pe, ok := provider.AsError(err); ok {
			return pe
		}
	}
	return transportError(parent, err)
}

// keyTransportError is the refusal to send an API key where it would cross the
// network unencrypted (or where the URL is unusable). It is never retried.
func keyTransportError(err error) *provider.Error {
	msg := err.Error()
	var ins *provider.InsecureKeyError
	if errors.As(err, &ins) {
		msg += "; use an https URL or a loopback address, or allow it deliberately for this provider (allow_insecure_http in your user config)"
	}
	return &provider.Error{Kind: provider.ErrBadRequest, Message: provider.SanitizeText(msg, 0), Err: err, NoRetry: true}
}

// rawUsageOf extracts the usage object of a response body verbatim, for the audit
// record; an object too large to keep is replaced by a marker.
func rawUsageOf(body []byte) json.RawMessage {
	var r struct {
		Usage json.RawMessage `json:"usage"`
	}
	if json.Unmarshal(body, &r) != nil || len(r.Usage) == 0 || string(r.Usage) == "null" {
		return nil
	}
	if len(r.Usage) > 4096 {
		return json.RawMessage(fmt.Sprintf(`{"truncated":true,"bytes":%d}`, len(r.Usage)))
	}
	return append(json.RawMessage(nil), r.Usage...)
}

func fallbackToolID(name, args string, n int) string {
	sum := sha256.Sum256([]byte(name + "\x00" + args + "\x00" + strconv.Itoa(n)))
	return "call_" + hex.EncodeToString(sum[:6])
}

// transportError classifies a failure that happened below HTTP. parent is the
// caller's context: its cancellation is "cancelled" (which the agent does not
// retry: it has stopped anyway), its deadline a timeout.
func transportError(parent context.Context, err error) *provider.Error {
	msg := provider.SanitizeText(err.Error(), 0)
	if errors.Is(parent.Err(), context.Canceled) && !errors.Is(err, context.DeadlineExceeded) {
		return &provider.Error{Kind: provider.ErrNetwork, Message: "request cancelled", Err: err}
	}
	var ne net.Error
	if errors.As(err, &ne) && ne.Timeout() {
		return &provider.Error{Kind: provider.ErrTimeout, Message: msg, Err: err}
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return &provider.Error{Kind: provider.ErrTimeout, Message: msg, Err: err}
	}
	return &provider.Error{Kind: provider.ErrNetwork, Message: msg, Err: err}
}

// errorEnvelope covers the shapes gateways and vendors use.
type errorEnvelope struct {
	Error struct {
		Code     json.RawMessage `json:"code"`
		Type     string          `json:"type"`
		Message  string          `json:"message"`
		Metadata struct {
			ErrorType       string `json:"error_type"`
			ProviderMessage string `json:"provider_message"`
		} `json:"metadata"`
	} `json:"error"`
}

func mapHTTPError(status int, h http.Header, body []byte) *provider.Error {
	e := &provider.Error{Status: status, Raw: provider.CapRaw(body)}
	var env errorEnvelope
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
	// The text is the endpoint's, and it goes to the event log, the terminal and
	// possibly to other agents: bounded, and stripped of control characters,
	// escape sequences and invisible characters.
	msg = provider.SanitizeText(msg, provider.MaxErrorText)
	e.Message = msg
	if d, ok := provider.ParseRetryAfter(h.Get("Retry-After"), time.Now(), 0); ok {
		e.RetryAfter = d
	}
	lower := strings.ToLower(msg)
	switch {
	case status == 401 || status == 403:
		e.Kind = provider.ErrAuth
	case status == 402:
		e.Kind = provider.ErrPayment
	case status == 408:
		e.Kind = provider.ErrTimeout
	case status == 429:
		e.Kind = provider.ErrRateLimit
	case status == 529:
		e.Kind = provider.ErrOverloaded
	case status >= 500:
		e.Kind = provider.ErrServer
	case status == 400 || status == 404 || status == 413 || status == 422:
		e.Kind = provider.ErrBadRequest
		if strings.Contains(lower, "context length") || strings.Contains(lower, "context_length") ||
			strings.Contains(lower, "maximum context") || strings.Contains(lower, "too many tokens") ||
			strings.Contains(lower, "exceeds the context") || status == 413 {
			e.Kind = provider.ErrContextLength
		}
	default:
		e.Kind = provider.ErrUnknown
	}
	return e
}

func mapInBandError(e *apiError) *provider.Error {
	code := 0
	var n int
	if json.Unmarshal(e.Code, &n) == nil {
		code = n
	}
	pe := &provider.Error{Status: code, Message: provider.SanitizeText(e.Message, provider.MaxErrorText), Raw: provider.CapRaw(mustJSON(e))}
	switch {
	case code == 429:
		pe.Kind = provider.ErrRateLimit
	case code >= 500 || code == 0:
		pe.Kind = provider.ErrServer
	case code == 402:
		pe.Kind = provider.ErrPayment
	case code == 401 || code == 403:
		pe.Kind = provider.ErrAuth
	default:
		pe.Kind = provider.ErrBadRequest
	}
	return pe
}

func mustJSON(v any) json.RawMessage {
	b, _ := json.Marshal(v)
	return b
}

// compile-time interface check.
var _ provider.Provider = (*Client)(nil)

// String helps logs. The API key is never included, nor is anything after the
// host and path of the base URL (user name, password, query).
func (c *Client) String() string {
	return fmt.Sprintf("openaichat(%s %s)", c.cfg.Name, provider.RedactURL(c.cfg.BaseURL))
}

var _ = core.Text
