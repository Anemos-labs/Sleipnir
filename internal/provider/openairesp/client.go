package openairesp

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"mime"
	"net"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/anemos-labs/sleipnir/internal/core"
	"github.com/anemos-labs/sleipnir/internal/cost"
	"github.com/anemos-labs/sleipnir/internal/provider"
)

// Authorizer supplies the bearer token of each request and learns when the endpoint refused one. An API key is a fixed token
// (StaticKey); a ChatGPT plan's is short-lived, and is renewed behind Token.
type Authorizer interface {
	// Token is the token to send now, renewed first when it is due. An error is final: the person has to sign in again.
	Token(ctx context.Context) (string, error)
	// Refused says the endpoint answered 401 to this token: the next Token must not return it.
	Refused(token string)
}

// StaticKey is an API key as an Authorizer.
type StaticKey string

// Token implements Authorizer.
func (k StaticKey) Token(context.Context) (string, error) { return string(k), nil }

// Refused implements Authorizer.
func (StaticKey) Refused(string) {}

// Config configures a Client.
type Config struct {
	Name    string // profile name, e.g. "openai"
	BaseURL string // ends before /responses
	Auth    Authorizer
	Headers map[string]string
	Options Options
	// Profile overrides the default automatic-caching profile.
	Profile *provider.Profile
	// FirstByteTimeout, StreamIdleTimeout, RequestTimeout and Limits are those of the other adapters (openaichat.Config).
	FirstByteTimeout  time.Duration
	StreamIdleTimeout time.Duration
	RequestTimeout    time.Duration
	Limits            provider.StreamLimits
	// AllowInsecureHTTP lets the token travel over plain http to a host that is not this machine.
	AllowInsecureHTTP bool
	// OnHeaders receives every response's headers (rate-limit accounting).
	OnHeaders func(http.Header)
	// HTTPClient carries the requests. Its redirect policy is tightened: a redirect to another origin is refused whatever the client says.
	HTTPClient *http.Client
}

// Client implements provider.Provider for the Responses API.
type Client struct {
	cfg     Config
	profile provider.Profile
	http    *http.Client
	keyErr  error // set when the token must not be sent to BaseURL
}

// New builds a Client.
func New(cfg Config) *Client {
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
	if cfg.Auth != nil && !cfg.AllowInsecureHTTP {
		if err := provider.CheckKeyTransport(cfg.BaseURL); err != nil {
			c.keyErr = err
		}
	}
	return c
}

// newTransport returns a transport sized for many concurrent streams: a swarm multiplexes dozens of long-lived requests over a few
// HTTP/2 connections.
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

// DefaultProfile describes OpenAI's automatic prefix cache: no markers, a floor of 1024 tokens and steps of 128, kept for minutes.
// `sleipnir doctor` measures the real numbers.
func DefaultProfile(name, baseURL string) provider.Profile {
	return provider.Profile{Name: name, Dialect: Dialect, BaseURL: baseURL, Cache: cost.OpenAICacheModel(), ReplayThinking: true, StreamUsage: true}
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
		return nil, &provider.Error{Kind: provider.ErrBadRequest, Message: "openairesp: request has no prompt"}
	}
	if c.keyErr != nil {
		return nil, provider.KeyTransportError(c.keyErr)
	}
	if c.cfg.Auth == nil {
		return nil, &provider.Error{Kind: provider.ErrAuth, Message: "no credentials are configured for this provider", NoRetry: true}
	}
	p := req.Prompt
	if req.Warm && c.cfg.Options.Plan {
		// The plan takes no output limit, so a warm-up would be a whole answer: it is not made, and the first real request writes the cache.
		on(provider.Event{Kind: provider.EvStart})
		out := &provider.Response{Model: idText(p.Model), Turn: core.Turn{Role: core.RoleAssistant, Origin: core.OriginModel, Model: idText(p.Model)}, Stop: core.StopEnd}
		on(provider.Event{Kind: provider.EvUsage, Usage: &out.Usage})
		return out, nil
	}
	body, err := Build(p, c.cfg.Options, req.Warm)
	if err != nil {
		return nil, &provider.Error{Kind: provider.ErrBadRequest, Message: err.Error(), Err: err}
	}

	parent := ctx
	begin := time.Now()
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	// One watchdog for the whole exchange, armed before the request is sent: it covers a server that accepts the connection and never
	// answers, as well as one that stalls mid-stream.
	wd := provider.NewWatchdog(cancel, c.cfg.FirstByteTimeout, c.cfg.StreamIdleTimeout, c.cfg.Limits.MaxDuration, req)
	wd.Start()
	defer wd.Stop()

	var resp *http.Response
	for attempt := 0; ; attempt++ {
		token, terr := c.cfg.Auth.Token(ctx)
		if terr != nil {
			return nil, &provider.Error{Kind: provider.ErrAuth, Message: provider.SanitizeText(terr.Error(), 0), Err: terr, NoRetry: true}
		}
		hr, rerr := http.NewRequestWithContext(ctx, http.MethodPost, c.cfg.BaseURL+"/responses", bytes.NewReader(body))
		if rerr != nil {
			return nil, &provider.Error{Kind: provider.ErrBadRequest, Message: provider.SanitizeText(rerr.Error(), 0), Err: rerr}
		}
		hr.Header.Set("Content-Type", "application/json")
		hr.Header.Set("Accept", "text/event-stream")
		if token != "" { // a server on this machine may take none
			hr.Header.Set("Authorization", "Bearer "+token)
		}
		for k, v := range c.cfg.Headers {
			hr.Header.Set(k, v)
		}
		resp, err = c.http.Do(hr)
		if err != nil {
			return nil, c.failure(parent, wd, err)
		}
		if c.cfg.OnHeaders != nil {
			c.cfg.OnHeaders(resp.Header)
		}
		if resp.StatusCode == http.StatusUnauthorized && attempt == 0 {
			// A token that was good a moment ago and is refused now (it was renewed elsewhere, or revoked): ask for a fresh one, once.
			_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<16))
			resp.Body.Close()
			c.cfg.Auth.Refused(token)
			continue
		}
		break
	}
	defer resp.Body.Close()
	rd := wd.Reader(resp.Body)
	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(io.LimitReader(rd, 1<<20))
		return nil, httpError(resp.StatusCode, resp.Header, b)
	}

	ct, _, _ := mime.ParseMediaType(resp.Header.Get("Content-Type"))
	var acc *accumulator
	switch ct {
	case "text/html":
		// What a proxy, a captive portal or a gateway says when the service is not there: a server that failed, not a stream that was cut.
		b, _ := io.ReadAll(io.LimitReader(rd, 4096))
		return nil, &provider.Error{Kind: provider.ErrServer, Raw: provider.CapRaw(b),
			Message: "the endpoint answered with an HTML page instead of a stream (a proxy, gateway or captive portal?): " + provider.SanitizeText(provider.HTMLText(string(b)), 160)}
	case "application/json":
		// An endpoint that does not stream answers the whole response at once: read it as the completed event it is.
		b, rerr := provider.ReadCapped(rd, c.cfg.Limits.MaxBytes)
		if rerr != nil {
			return nil, c.failure(parent, wd, rerr)
		}
		var r response
		if jerr := json.Unmarshal(b, &r); jerr != nil {
			return nil, &provider.Error{Kind: provider.ErrServer, Message: "unparseable response: " + provider.SanitizeText(jerr.Error(), 0), Raw: provider.CapRaw(b)}
		}
		if r.Error != nil {
			return nil, mapAPIError(r.Error)
		}
		acc = newAccumulator(c.cfg.Limits)
		acc.rawUsage = rawUsageOf(b)
		if ferr := acc.feed(&event{Type: "response.completed", Response: &r}, begin, on); ferr != nil {
			return nil, ferr
		}
	default:
		acc, err = readStream(rd, begin, on, c.cfg.Limits)
		if err != nil {
			return nil, c.failure(parent, wd, err)
		}
	}

	acc.start(begin, on) // a reply with no frames at all still counts as started, for the warm gate
	if acc.model == "" {
		acc.model = idText(p.Model)
	}
	turn, stop := acc.build()
	out := &provider.Response{
		ID: acc.id, Model: acc.model, Turn: turn, Usage: acc.usage.normalize(), Stop: stop, RawUsage: acc.rawUsage, Total: time.Since(begin),
	}
	out.TTFB = min(acc.ttfb, out.Total)
	if out.TTFB <= 0 {
		out.TTFB = out.Total
	}
	on(provider.Event{Kind: provider.EvUsage, Usage: &out.Usage, RequestID: acc.id})
	return out, nil
}

// failure turns an error from the transport or the body into the provider error the caller sees: a redirect that left the origin, a
// deadline of the watchdog (a timeout, never "cancelled"), an error the adapter already classified, and last the caller's own
// cancellation or deadline and plain network failures.
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
	return provider.TransportError(parent, err)
}

// httpError classifies a non-200 answer. On a plan the limit that is reached is the plan's, which waiting a minute does not lift.
func httpError(status int, h http.Header, body []byte) *provider.Error {
	pe := provider.HTTPError(status, h, body)
	if status == http.StatusTooManyRequests && strings.Contains(strings.ToLower(string(body)), "usage_limit") {
		pe.Kind = provider.ErrPayment
	}
	return pe
}

func fallbackToolID(name, args string, n int) string {
	sum := sha256.Sum256([]byte(name + "\x00" + args + "\x00" + strconv.Itoa(n)))
	return "call_" + hex.EncodeToString(sum[:6])
}

// compile-time interface check.
var _ provider.Provider = (*Client)(nil)

// String helps logs. The token is never included, nor is anything after the host and path of the base URL.
func (c *Client) String() string {
	return fmt.Sprintf("openairesp(%s %s)", c.cfg.Name, provider.RedactURL(c.cfg.BaseURL))
}
