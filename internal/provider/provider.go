// Package provider defines the boundary between the harness and model APIs.
//
// Adapters translate the provider-neutral core.Prompt into a wire request and
// stream the reply back as core blocks. They must not reorder or re-serialise
// prompt content: the cache engine has already decided the bytes, and an
// adapter that "helpfully" normalises them silently destroys the prefix cache.
package provider

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/reee344/sleipnir/internal/core"
	"github.com/reee344/sleipnir/internal/cost"
	"github.com/reee344/sleipnir/internal/kv"
)

// Profile is what the harness knows about an endpoint's behaviour. Static
// defaults come from the adapter; a capability probe can refine them (a
// gateway may or may not pass cache markers or thinking signatures through).
type Profile struct {
	Name    string
	Dialect string
	BaseURL string

	Cache cost.CacheModel

	// ReplayThinking: thinking blocks can be echoed back verbatim.
	ReplayThinking bool
	// BindingControls: the endpoint accepts thinking.block_binding controls, so
	// declared history rewrites can ask for drop instead of a 400.
	BindingControls bool
	// TurnScopedSystem: role=system messages with clear_at are supported.
	TurnScopedSystem bool
	// ChatCacheControl: cache_control markers are accepted inside
	// OpenAI-style chat content parts (gateways that front Anthropic models).
	ChatCacheControl bool
	// PrewarmZeroTokens: max_tokens 0 populates the cache without generating.
	PrewarmZeroTokens bool
	// StreamUsage: usage arrives in the stream.
	StreamUsage bool
	// CaptureTokens: the endpoint can return prompt/completion token ids and
	// sampling logprobs (self-hosted vLLM/SGLang-style servers), which RL
	// training needs at the LLM boundary.
	CaptureTokens bool
}

// KVCaps derives the renderer's view of the profile.
func (p Profile) KVCaps() kv.Caps {
	return kv.Caps{
		Dialect:         p.Dialect,
		MaxBreakpoints:  p.Cache.MaxBreakpoints,
		LookbackBlocks:  p.Cache.LookbackBlocks,
		MinPrefixTokens: p.Cache.MinPrefixTokens,
		ReplayThinking:  p.ReplayThinking,
		CacheKeys:       p.Cache.KeyRouting,

		TurnScopedSystem: p.TurnScopedSystem,
	}
}

// Request is one model call.
type Request struct {
	Prompt *core.Prompt
	// Label tags the call in logs ("be-1/turn 42", "compactor:be-1").
	Label string
	// Warm populates the cache and returns without generating.
	Warm bool
	// NoStream forces a non-streaming call (keep-alives).
	NoStream bool
	// Betas are extra beta feature flags for this call.
	Betas []string
	// BindingMode sets thinking.block_binding.prefix_mismatch_behavior
	// ("drop_block" or "error"); empty leaves the endpoint default.
	BindingMode string
	// Capture asks the endpoint for token ids and logprobs; ignored (and the
	// response has no Tokens) when the profile cannot provide them.
	Capture bool
}

// EventKind classifies streaming events.
type EventKind int

const (
	// EvStart fires when the first response bytes arrive. Entries written by
	// this request become readable from this moment on some providers, so the
	// fan-out gate listens for it.
	EvStart EventKind = iota
	EvText
	EvThinking
	EvToolStart
	EvToolDelta
	EvBlockDone
	EvUsage
	// EvReset tells the consumer to discard partial output shown so far: the
	// request is being retried from scratch.
	EvReset
)

// Event is one streaming update.
type Event struct {
	Kind      EventKind
	Index     int
	Text      string
	ToolID    string
	ToolName  string
	Block     *core.Block
	Usage     *core.Usage
	RequestID string
	Elapsed   time.Duration
}

// Transformation records something the endpoint did to the request that the
// caller did not ask for (for example dropping a thinking block whose binding
// no longer matched). These are training-data and regression signals.
type Transformation struct {
	Type   string `json:"type"`
	Path   string `json:"path,omitempty"`
	Reason string `json:"reason,omitempty"`
}

// Response is a completed model call.
type Response struct {
	ID              string
	Model           string
	Turn            core.Turn
	Usage           core.Usage
	Stop            core.StopReason
	StopDetail      string
	Transformations []Transformation
	TTFB            time.Duration
	Total           time.Duration
	// RawUsage is the provider's usage object verbatim, for audit.
	RawUsage json.RawMessage
	// CostUSD is the exact charge when the gateway reports one (Heimdall's
	// usage.cost); nil otherwise.
	CostUSD *float64
	// Provider names the upstream that served the request, when known.
	Provider string
	// Tokens is the token trace of the call when Request.Capture was honoured.
	Tokens *core.TokenTrace
}

// Provider is implemented by every adapter.
type Provider interface {
	Profile() Profile
	Do(ctx context.Context, req *Request, on func(Event)) (*Response, error)
}

// ErrKind classifies failures for retry and recovery decisions.
type ErrKind int

const (
	ErrUnknown ErrKind = iota
	ErrNetwork
	ErrTimeout
	ErrAuth
	ErrRateLimit
	ErrOverloaded
	ErrServer
	ErrBadRequest
	ErrContextLength
	// ErrThinkingBinding: the endpoint rejected a replayed thinking block
	// because the history changed since it was produced.
	ErrThinkingBinding
	ErrRefusal
	// ErrPayment: credits exhausted or a spend limit was hit. Never retried.
	ErrPayment
)

func (k ErrKind) String() string {
	return [...]string{"unknown", "network", "timeout", "auth", "rate_limit", "overloaded", "server", "bad_request", "context_length", "thinking_binding", "refusal", "payment"}[k]
}

// Error is a provider failure with enough structure to act on.
type Error struct {
	Kind       ErrKind
	Status     int
	Message    string
	RetryAfter time.Duration
	Raw        json.RawMessage
	Err        error
}

func (e *Error) Error() string {
	if e.Status != 0 {
		return fmt.Sprintf("provider: %s (http %d): %s", e.Kind, e.Status, e.Message)
	}
	return fmt.Sprintf("provider: %s: %s", e.Kind, e.Message)
}

func (e *Error) Unwrap() error { return e.Err }

// DefaultMaxRetryAfter caps how long a server may ask a client to wait.
const DefaultMaxRetryAfter = 2 * time.Minute

// ParseRetryAfter reads a Retry-After value: whole or decimal seconds, or an HTTP
// date. The result is clamped to [0, max] (DefaultMaxRetryAfter when max <= 0):
// the agent trusts this number over its own backoff, so a misconfigured gateway
// or a daily quota asking for hours must not be able to park an agent, and a
// huge number must not overflow a Duration. ok is false when v holds no usable
// value.
func ParseRetryAfter(v string, now time.Time, max time.Duration) (d time.Duration, ok bool) {
	if max <= 0 {
		max = DefaultMaxRetryAfter
	}
	v = strings.TrimSpace(v)
	if v == "" {
		return 0, false
	}
	if f, err := strconv.ParseFloat(v, 64); err == nil && !math.IsNaN(f) {
		switch {
		case f <= 0:
			return 0, true
		case f >= max.Seconds():
			return max, true
		}
		return time.Duration(f * float64(time.Second)), true
	}
	if t, err := http.ParseTime(v); err == nil {
		return min(max, max0(t.Sub(now))), true
	}
	return 0, false
}

func max0(d time.Duration) time.Duration {
	if d < 0 {
		return 0
	}
	return d
}

// Retryable reports whether the same request may succeed if repeated.
func (e *Error) Retryable() bool {
	switch e.Kind {
	case ErrNetwork, ErrTimeout, ErrRateLimit, ErrOverloaded, ErrServer:
		return true
	}
	return false
}

// AsError extracts a *Error from err.
func AsError(err error) (*Error, bool) {
	var pe *Error
	if errors.As(err, &pe) {
		return pe, true
	}
	return nil, false
}
