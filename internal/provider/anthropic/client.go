package anthropic

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/anemos-labs/sleipnir/internal/core"
	"github.com/anemos-labs/sleipnir/internal/cost"
	"github.com/anemos-labs/sleipnir/internal/provider"
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
	// FirstByteTimeout bounds the wait from sending a streaming request until the
	// first byte of the response body arrives (a ping counts). A server that
	// accepts the request and never answers is otherwise a request that never
	// ends. Default: StreamIdleTimeout when that is set, else 120s, which is
	// generous for a model that thinks before it says anything. Expiry is a
	// provider.ErrTimeout, which the agent retries; a request that got no response
	// at all twice is not retried a third time.
	FirstByteTimeout time.Duration
	// StreamIdleTimeout aborts a stream that goes silent once it has started. Pings
	// and comment lines count as activity. Default 60s.
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
	// HTTPClient carries the requests. Its redirect policy is tightened: a redirect
	// to another origin is refused whatever the client says.
	HTTPClient *http.Client
}

// Client implements provider.Provider for the Anthropic Messages API.
type Client struct {
	cfg    Config
	http   *http.Client
	keyErr error // set when the API key must not be sent to BaseURL

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
	if cfg.MaxRetryAfter <= 0 {
		cfg.MaxRetryAfter = 2 * time.Minute
	}
	cfg.Limits = cfg.Limits.Normalized()
	hc := cfg.HTTPClient
	if hc == nil {
		hc = &http.Client{Transport: newTransport()}
	}
	models := cfg.Options.Models
	if models == nil {
		models = DefaultModelInfo
	}
	prof := defaultProfile(cfg.Name, cfg.BaseURL, cfg.Model, models)
	if cfg.Profile != nil {
		prof = *cfg.Profile
	}
	c := &Client{cfg: cfg, http: provider.HardenClient(hc), profile: prof}
	if cfg.APIKey != "" && !cfg.AllowInsecureHTTP {
		if err := provider.CheckKeyTransport(messagesURL(cfg.BaseURL)); err != nil {
			c.keyErr = err
		}
	}
	return c
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
// blocks that replay verbatim, thinking binding controls and zero-token
// pre-warms. model sizes the minimum cacheable prefix (an unknown or empty model
// gets a conservative 1024) and decides TurnScopedSystem: the families without
// mid-conversation system messages (Sonnet 5, Opus 4.7 and earlier, Haiku) get
// false, so kv delivers the board view another way instead of earning a 400.
func DefaultProfile(name, baseURL, model string) provider.Profile {
	return defaultProfile(name, baseURL, model, DefaultModelInfo)
}

func defaultProfile(name, baseURL, model string, models ModelResolver) provider.Profile {
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
		TurnScopedSystem:  models(model).midSystem(),
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

// String helps logs. The API key is never included, nor is anything after the
// host and path of the base URL (user name, password, query).
func (c *Client) String() string {
	return fmt.Sprintf("anthropic(%s %s)", c.cfg.Name, provider.RedactURL(c.cfg.BaseURL))
}

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
	if c.keyErr != nil {
		return nil, keyTransportError(c.keyErr)
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

	// One watchdog for the whole exchange, armed before the request is sent: it
	// covers a server that accepts the connection and never answers, a stream that
	// stalls, and a non-streaming body that hangs. The derived context is cancelled
	// by it, so the reason is recorded separately: "the caller cancelled" and "the
	// server went quiet" are handled differently.
	first, idle, total := c.cfg.FirstByteTimeout, c.cfg.StreamIdleTimeout, c.cfg.Limits.MaxDuration
	if !stream {
		first, idle, total = c.cfg.RequestTimeout, c.cfg.RequestTimeout, 0
	}
	wd := provider.NewWatchdog(cancel, first, idle, total, req)
	wd.Start()
	defer wd.Stop()

	hr, err := http.NewRequestWithContext(ctx, http.MethodPost, messagesURL(c.cfg.BaseURL), bytes.NewReader(built.Body))
	if err != nil {
		return nil, &provider.Error{Kind: provider.ErrBadRequest, Message: provider.SanitizeText(err.Error(), 0), Err: err}
	}
	c.setHeaders(hr, req, built.Betas, stream)

	resp, err := c.http.Do(hr)
	if err != nil {
		return nil, c.failure(parent, wd, err)
	}
	defer resp.Body.Close()
	if c.cfg.OnHeaders != nil {
		c.cfg.OnHeaders(resp.Header)
	}
	body := wd.Reader(resp.Body)

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
		res, ttfb, err = readStream(body, start, on, c.cfg.Limits)
		if err != nil {
			return nil, c.failure(parent, wd, err)
		}
	} else {
		// The response begins when its headers arrive; entries this request wrote
		// are readable from here.
		ttfb = time.Since(start)
		on(provider.Event{Kind: provider.EvStart, RequestID: provider.SanitizeText(resp.Header.Get("Request-Id"), 128), Elapsed: ttfb})
		b, rerr := provider.ReadCapped(body, c.cfg.Limits.MaxBytes)
		if rerr != nil {
			return nil, c.failure(parent, wd, rerr)
		}
		res, err = decodeBody(b)
		if err != nil {
			return nil, err
		}
		if err := checkResultLimits(res, c.cfg.Limits); err != nil {
			return nil, err
		}
		emitBlocks(res, on)
	}

	usage := res.usage.normalize()
	res.id, res.model = provider.SanitizeText(res.id, 256), provider.SanitizeText(res.model, 256)
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
		return nil, unparseable(err, b)
	}
	if head.Type == "error" && head.Error != nil {
		return nil, inBandError(head.Error.Type, head.Error.Message, b)
	}
	res, err := parseMessage(b)
	if err != nil {
		return nil, unparseable(err, b)
	}
	return res, nil
}

// unparseable wraps a response parse failure as a provider server error with sanitized text and
// bounded raw response bytes.
func unparseable(err error, body []byte) *provider.Error {
	raw := body
	if len(raw) > 4096 {
		raw = raw[:4096]
	}
	return &provider.Error{Kind: provider.ErrServer, Message: "unparseable response: " + provider.SanitizeText(err.Error(), 0), Raw: provider.CapRaw(raw)}
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

// splitBetas trims comma-separated beta names and discards empty entries.
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

// messagesURL appends the Anthropic messages path while avoiding a duplicate trailing v1
// component.
func messagesURL(base string) string {
	base = strings.TrimRight(base, "/")
	if strings.HasSuffix(base, "/v1") {
		return base + "/messages"
	}
	return base + "/v1/messages"
}
