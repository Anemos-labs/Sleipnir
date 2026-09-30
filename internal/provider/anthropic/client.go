package anthropic

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/reee344/sleipnir/internal/core"
	"github.com/reee344/sleipnir/internal/cost"
	"github.com/reee344/sleipnir/internal/provider"
)

const (
	defaultBaseURL = "https://api.anthropic.com"
	defaultVersion = "2023-06-01"
)

// Config configures a Client.
type Config struct {
	// Name is the profile name; default "anthropic".
	Name string
	// BaseURL is the API root; default https://api.anthropic.com. A URL that
	// already ends in /v1 is accepted.
	BaseURL string
	// APIKey authenticates the client. It is only ever sent as a header and
	// never appears in errors or logs.
	APIKey string
	// AuthStyle is "x-api-key" (default) or "bearer" (Authorization: Bearer).
	AuthStyle string
	// Version is the anthropic-version header; default 2023-06-01.
	Version string
	// Betas are anthropic-beta values sent on every request. The adapter adds the
	// ones a body needs by itself, and merges any anthropic-beta value found in
	// Headers instead of letting it replace the computed list.
	Betas []string
	// Headers are extra request headers.
	Headers map[string]string
	// Model is the model this client will mostly serve. It only sizes the
	// profile's minimum cacheable prefix (which differs per model, from 512 to
	// 4096 tokens); the wire behaviour always follows Prompt.Model.
	Model string
	// Options tunes body rendering.
	Options Options
	// Profile overrides the default profile (after a capability probe, or for a
	// gateway that drops cache_control or thinking).
	Profile *provider.Profile
	// StreamIdleTimeout aborts a stream that goes silent. Pings and comment lines
	// count as activity. Default 120s.
	StreamIdleTimeout time.Duration
	// RequestTimeout bounds a non-streaming call (warm-ups, keep-alives) from the
	// moment it is sent until its body is read. Default 10 minutes.
	RequestTimeout time.Duration
	// MaxRetryAfter caps the wait a 429 or 529 may ask for. Default 2 minutes.
	MaxRetryAfter time.Duration
	// SessionHeader, when set, carries Prompt.CacheKey under this header name
	// (for example "x-session-id" on gateways that route sticky sessions by it).
	// Anthropic itself has no routing control and ignores it.
	SessionHeader string
	// OnHeaders receives every response's headers (rate-limit accounting).
	OnHeaders func(http.Header)
	// OnWarnings receives what Build changed or could not honour, per request.
	OnWarnings func(req *provider.Request, w []Warning)
	HTTPClient *http.Client
}

// Client implements provider.Provider for the Anthropic Messages API.
type Client struct {
	cfg  Config
	http *http.Client

	mu      sync.RWMutex
	profile provider.Profile
}

// New builds a Client.
func New(cfg Config) *Client {
	if cfg.Name == "" {
		cfg.Name = "anthropic"
	}
	if cfg.BaseURL == "" {
		cfg.BaseURL = defaultBaseURL
	}
	cfg.BaseURL = strings.TrimRight(cfg.BaseURL, "/")
	if cfg.Version == "" {
		cfg.Version = defaultVersion
	}
	if cfg.StreamIdleTimeout <= 0 {
		cfg.StreamIdleTimeout = 120 * time.Second
	}
	if cfg.RequestTimeout <= 0 {
		cfg.RequestTimeout = 10 * time.Minute
	}
	if cfg.MaxRetryAfter <= 0 {
		cfg.MaxRetryAfter = 2 * time.Minute
	}
	hc := cfg.HTTPClient
	if hc == nil {
		hc = &http.Client{Transport: newTransport()}
	}
	prof := DefaultProfile(cfg.Name, cfg.BaseURL, cfg.Model)
	if cfg.Profile != nil {
		prof = *cfg.Profile
	}
	return &Client{cfg: cfg, http: hc, profile: prof}
}

// newTransport returns a transport sized for many concurrent streams: a swarm
// multiplexes dozens of long-lived requests over a few connections.
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

// DefaultProfile describes the first-party API: explicit breakpoints (at most
// four, 20-position lookback, 5-minute and 1-hour lifetimes), entries readable
// once the first response byte is out, reads that refresh the lifetime, thinking
// blocks that replay verbatim, turn-scoped system messages, thinking binding
// controls and zero-token pre-warms. model sizes the minimum cacheable prefix
// (an unknown or empty model gets a conservative 1024).
func DefaultProfile(name, baseURL, model string) provider.Profile {
	minPrefix := 1024
	if model != "" {
		if m, ok := cost.Defaults().Lookup(model); ok && m.Cache.MinPrefixTokens > 0 {
			minPrefix = m.Cache.MinPrefixTokens
		}
	}
	return provider.Profile{
		Name:              name,
		Dialect:           Dialect,
		BaseURL:           baseURL,
		Cache:             cost.AnthropicCacheModel(minPrefix),
		ReplayThinking:    true,
		BindingControls:   true,
		TurnScopedSystem:  true,
		PrewarmZeroTokens: true,
		StreamUsage:       true,
		// Anthropic returns neither token ids nor logprobs.
		CaptureTokens: false,
	}
}

// Profile implements provider.Provider.
func (c *Client) Profile() provider.Profile {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.profile
}

// SetProfile replaces the profile (after a capability probe). It is safe to call
// while requests are in flight; each request uses the profile it started with.
func (c *Client) SetProfile(p provider.Profile) {
	c.mu.Lock()
	c.profile = p
	c.mu.Unlock()
}

// String helps logs. The API key is never included.
func (c *Client) String() string { return fmt.Sprintf("anthropic(%s %s)", c.cfg.Name, c.cfg.BaseURL) }

var _ provider.Provider = (*Client)(nil)

// optionsFor derives the render options for one request from the client's
// static options, the profile and the request.
func (c *Client) optionsFor(req *provider.Request, prof provider.Profile) (Options, []Warning) {
	o := c.cfg.Options
	var ws []Warning
	o.NoTurnScopedSystem = o.NoTurnScopedSystem || !prof.TurnScopedSystem
	o.NoThinkingReplay = o.NoThinkingReplay || !prof.ReplayThinking
	o.NoZeroMaxTokens = o.NoZeroMaxTokens || !prof.PrewarmZeroTokens
	o.Warm = req.Warm
	o.BindingMode = ""
	if req.BindingMode != "" {
		if prof.BindingControls {
			o.BindingMode = req.BindingMode
		} else {
			// Sending block_binding without its beta is a hard 400, and a gateway
			// that strips the header keeps the field. Better to run without it.
			ws = append(ws, Warning{Code: "binding_unsupported", Message: "endpoint profile has no thinking binding controls; BindingMode not sent"})
		}
	}
	return o, ws
}

// Do implements provider.Provider.
func (c *Client) Do(ctx context.Context, req *provider.Request, on func(provider.Event)) (*provider.Response, error) {
	if on == nil {
		on = func(provider.Event) {}
	}
	if req == nil || req.Prompt == nil {
		return nil, &provider.Error{Kind: provider.ErrBadRequest, Message: "anthropic: request has no prompt"}
	}
	prof := c.Profile()
	stream := !req.NoStream && !req.Warm
	opts, cw := c.optionsFor(req, prof)
	built, err := BuildReport(req.Prompt, opts, stream)
	if err != nil {
		return nil, &provider.Error{Kind: provider.ErrBadRequest, Message: err.Error(), Err: err}
	}
	if ws := append(cw, built.Warnings...); len(ws) > 0 && c.cfg.OnWarnings != nil {
		c.cfg.OnWarnings(req, ws)
	}

	parent := ctx
	start := time.Now()
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	// One watchdog for the whole exchange: it covers a server that never sends
	// headers, a stream that stalls, and a non-streaming body that hangs. The
	// derived context is cancelled by it, so the reason is recorded separately:
	// "the caller cancelled" and "the server went quiet" are handled differently.
	timeout := c.cfg.RequestTimeout
	if stream {
		timeout = c.cfg.StreamIdleTimeout
	}
	var fired atomic.Bool
	idle := time.AfterFunc(timeout, func() { fired.Store(true); cancel() })
	defer idle.Stop()

	hr, err := http.NewRequestWithContext(ctx, http.MethodPost, messagesURL(c.cfg.BaseURL), bytes.NewReader(built.Body))
	if err != nil {
		return nil, &provider.Error{Kind: provider.ErrBadRequest, Message: err.Error(), Err: err}
	}
	c.setHeaders(hr, req, built.Betas, stream)

	resp, err := c.http.Do(hr)
	if err != nil {
		return nil, transportError(parent, fired.Load(), timeout, err)
	}
	defer resp.Body.Close()
	idle.Reset(timeout)
	if c.cfg.OnHeaders != nil {
		c.cfg.OnHeaders(resp.Header)
	}
	body := &activityReader{r: resp.Body, timer: idle, d: timeout}

	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(io.LimitReader(body, 1<<20))
		return nil, mapHTTPError(resp.StatusCode, resp.Header, b, time.Now(), c.cfg.MaxRetryAfter)
	}

	// Trust what the server sent over what we asked for: a gateway may answer a
	// streaming request with one JSON body (or the reverse).
	ct := strings.ToLower(resp.Header.Get("Content-Type"))
	sse := stream
	switch {
	case strings.HasPrefix(ct, "text/event-stream"):
		sse = true
	case strings.HasPrefix(ct, "application/json"):
		sse = false
	}

	var res *result
	var ttfb time.Duration
	if sse {
		res, ttfb, err = readStream(body, start, on)
		if err != nil {
			if ctx.Err() != nil {
				return nil, transportError(parent, fired.Load(), timeout, ctx.Err())
			}
			return nil, err
		}
	} else {
		// The response begins when its headers arrive; entries this request wrote
		// are readable from here.
		ttfb = time.Since(start)
		on(provider.Event{Kind: provider.EvStart, RequestID: resp.Header.Get("Request-Id"), Elapsed: ttfb})
		b, rerr := io.ReadAll(io.LimitReader(body, 64<<20))
		if rerr != nil {
			return nil, transportError(parent, fired.Load(), timeout, rerr)
		}
		res, err = decodeBody(b)
		if err != nil {
			return nil, err
		}
		emitBlocks(res, on)
	}

	usage := res.usage.normalize()
	out := &provider.Response{
		ID:              res.id,
		Model:           res.model,
		Turn:            core.Turn{Role: core.RoleAssistant, Blocks: res.blocks, Origin: core.OriginModel, Model: res.model},
		Usage:           usage,
		Stop:            res.stop,
		StopDetail:      res.stopDetail,
		Transformations: res.transforms,
		TTFB:            ttfb,
		Total:           time.Since(start),
		RawUsage:        res.rawUsage,
		Provider:        "anthropic",
	}
	on(provider.Event{Kind: provider.EvUsage, Usage: &out.Usage, RequestID: res.id})
	return out, nil
}

// decodeBody parses a non-streaming body, which may also be an error object
// delivered with a 200.
func decodeBody(b []byte) (*result, error) {
	var head struct {
		Type  string `json:"type"`
		Error *struct {
			Type    string `json:"type"`
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal(b, &head); err != nil {
		return nil, &provider.Error{Kind: provider.ErrServer, Message: "unparseable response: " + err.Error(), Raw: truncRaw(b)}
	}
	if head.Type == "error" && head.Error != nil {
		return nil, inBandError(head.Error.Type, head.Error.Message, b)
	}
	res, err := parseMessage(b)
	if err != nil {
		return nil, &provider.Error{Kind: provider.ErrServer, Message: "unparseable response: " + err.Error(), Raw: truncRaw(b)}
	}
	return res, nil
}

func truncRaw(b []byte) json.RawMessage {
	if len(b) > 4096 {
		b = b[:4096]
	}
	return append(json.RawMessage(nil), b...)
}

// emitBlocks replays a non-streaming message as the events a stream would have
// produced, so consumers render both the same way.
func emitBlocks(res *result, on func(provider.Event)) {
	for i := range res.blocks {
		b := res.blocks[i]
		switch b.Kind {
		case core.BlockText:
			on(provider.Event{Kind: provider.EvText, Index: i, Text: b.Text})
		case core.BlockThinking:
			if b.Text != "" {
				on(provider.Event{Kind: provider.EvThinking, Index: i, Text: b.Text})
			}
		case core.BlockToolUse:
			on(provider.Event{Kind: provider.EvToolStart, Index: i, ToolID: b.ToolID, ToolName: b.ToolName})
			on(provider.Event{Kind: provider.EvToolDelta, Index: i, ToolID: b.ToolID, ToolName: b.ToolName, Text: string(b.Input)})
		}
		on(provider.Event{Kind: provider.EvBlockDone, Index: i, Block: &b})
	}
}

// setHeaders writes the request headers. Required betas travel with the body
// fields that need them; a caller-supplied anthropic-beta header is merged, not
// allowed to replace the computed list.
func (c *Client) setHeaders(hr *http.Request, req *provider.Request, required []string, stream bool) {
	h := hr.Header
	h.Set("Content-Type", "application/json")
	h.Set("User-Agent", "sleipnir")
	h.Set("Anthropic-Version", c.cfg.Version)
	if stream {
		h.Set("Accept", "text/event-stream")
	} else {
		h.Set("Accept", "application/json")
	}
	if k := c.cfg.APIKey; k != "" {
		if strings.EqualFold(c.cfg.AuthStyle, "bearer") {
			h.Set("Authorization", "Bearer "+k)
		} else {
			h.Set("X-Api-Key", k)
		}
	}
	if c.cfg.SessionHeader != "" && req.Prompt.CacheKey != "" {
		h.Set(c.cfg.SessionHeader, req.Prompt.CacheKey)
	}
	lists := [][]string{c.cfg.Betas, req.Betas, required}
	for k, v := range c.cfg.Headers {
		if strings.EqualFold(k, "anthropic-beta") {
			lists = append(lists, splitBetas(v))
			continue
		}
		h.Set(k, v)
	}
	if betas := mergeBetas(lists...); len(betas) > 0 {
		h.Set("Anthropic-Beta", strings.Join(betas, ","))
	}
}

func splitBetas(s string) []string {
	var out []string
	for _, p := range strings.Split(s, ",") {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

// mergeBetas concatenates beta lists, dropping blanks and duplicates, keeping
// first-seen order.
func mergeBetas(lists ...[]string) []string {
	var out []string
	seen := map[string]bool{}
	for _, l := range lists {
		for _, b := range l {
			if b = strings.TrimSpace(b); b != "" && !seen[b] {
				seen[b] = true
				out = append(out, b)
			}
		}
	}
	return out
}

func messagesURL(base string) string {
	base = strings.TrimRight(base, "/")
	if strings.HasSuffix(base, "/v1") {
		return base + "/messages"
	}
	return base + "/v1/messages"
}

// activityReader resets the idle watchdog on every read that returns data.
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
