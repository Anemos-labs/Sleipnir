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
	// StreamIdleTimeout aborts a stream that goes silent (keep-alive comments
	// count as activity). Default 120s.
	StreamIdleTimeout time.Duration
	// OnHeaders receives every response's headers (rate-limit accounting).
	OnHeaders  func(http.Header)
	HTTPClient *http.Client
}

// Client implements provider.Provider for chat completions.
type Client struct {
	cfg     Config
	profile provider.Profile
	http    *http.Client
}

// New builds a Client.
func New(cfg Config) *Client {
	cfg.Options.defaults()
	if cfg.StreamIdleTimeout == 0 {
		cfg.StreamIdleTimeout = 120 * time.Second
	}
	cfg.BaseURL = strings.TrimRight(cfg.BaseURL, "/")
	hc := cfg.HTTPClient
	if hc == nil {
		hc = &http.Client{Transport: newTransport()}
	}
	prof := DefaultProfile(cfg.Name, cfg.BaseURL)
	if cfg.Profile != nil {
		prof = *cfg.Profile
	}
	return &Client{cfg: cfg, profile: prof, http: hc}
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

	start := time.Now()
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	hr, err := http.NewRequestWithContext(ctx, http.MethodPost, c.cfg.BaseURL+"/chat/completions", bytes.NewReader(body))
	if err != nil {
		return nil, &provider.Error{Kind: provider.ErrBadRequest, Message: err.Error(), Err: err}
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
		return nil, transportError(ctx, err)
	}
	defer resp.Body.Close()
	if c.cfg.OnHeaders != nil {
		c.cfg.OnHeaders(resp.Header)
	}
	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
		return nil, mapHTTPError(resp.StatusCode, resp.Header, b)
	}

	var acc *accumulator
	if stream {
		// Watchdog: abort a stream that has gone silent.
		idle := time.AfterFunc(c.cfg.StreamIdleTimeout, cancel)
		defer idle.Stop()
		acc, err = readStream(&activityReader{r: resp.Body, timer: idle, d: c.cfg.StreamIdleTimeout}, start, on)
		if err != nil {
			if ctx.Err() != nil {
				return nil, transportError(ctx, ctx.Err())
			}
			return nil, err
		}
	} else {
		b, rerr := io.ReadAll(io.LimitReader(resp.Body, 64<<20))
		if rerr != nil {
			return nil, transportError(ctx, rerr)
		}
		acc = newAccumulator()
		var c2 chunk
		if err := json.Unmarshal(b, &c2); err != nil {
			return nil, &provider.Error{Kind: provider.ErrServer, Message: "unparseable response: " + err.Error(), Raw: b}
		}
		if c2.Error != nil {
			return nil, mapInBandError(c2.Error)
		}
		if c2.Usage != nil {
			if raw, err := json.Marshal(c2.Usage); err == nil {
				acc.rawUsage = raw
			}
		}
		acc.feed(&c2, start, on)
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
		Usage:    acc.usage.normalize(),
		Stop:     stop,
		RawUsage: acc.rawUsage,
		Total:    time.Since(start),
		Provider: acc.provider,
	}
	if acc.usage != nil {
		out.CostUSD = acc.usage.Cost
	}
	if opts.CaptureTokens {
		out.Tokens = acc.trace(acc.model)
	}
	out.TTFB = out.Total
	on(provider.Event{Kind: provider.EvUsage, Usage: &out.Usage, RequestID: acc.id})
	return out, nil
}

// activityReader resets the idle watchdog on every read.
type activityReader struct {
	r     io.Reader
	timer *time.Timer
	d     time.Duration
}

func (a *activityReader) Read(p []byte) (int, error) {
	n, err := a.r.Read(p)
	if n > 0 {
		a.timer.Reset(a.d)
	}
	return n, err
}

func fallbackToolID(name, args string, n int) string {
	sum := sha256.Sum256([]byte(name + "\x00" + args + "\x00" + strconv.Itoa(n)))
	return "call_" + hex.EncodeToString(sum[:6])
}

func transportError(ctx context.Context, err error) *provider.Error {
	if errors.Is(ctx.Err(), context.Canceled) && !errors.Is(err, context.DeadlineExceeded) {
		return &provider.Error{Kind: provider.ErrNetwork, Message: "request cancelled", Err: err}
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
	e := &provider.Error{Status: status, Raw: body}
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
	pe := &provider.Error{Status: code, Message: e.Message, Raw: mustJSON(e)}
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

// String helps logs.
func (c *Client) String() string { return fmt.Sprintf("openaichat(%s %s)", c.cfg.Name, c.cfg.BaseURL) }

var _ = core.Text
