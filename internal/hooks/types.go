package hooks

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/url"
	"sort"
	"strings"
	"time"
)

// Origin says who vouches for a hook.
type Origin string

const (
	// OriginUser hooks come from configuration the user controls: their own
	// files, command-line flags, managed policy. They run without further trust.
	OriginUser Origin = "user"
	// OriginProject hooks come from the repository. They run only when the
	// Runner is trusted or its Approve callback agrees.
	OriginProject Origin = "project"
)

// Hook types.
const (
	TypeCommand = "command"
	TypeHTTP    = "http"
)

// Hook is one configured handler.
type Hook struct {
	// Event is the canonical event name.
	Event string
	// Matcher is the matcher as written ("" and "*" match everything).
	Matcher string
	// Type is TypeCommand or TypeHTTP.
	Type string
	// Command is the shell command line of a command hook.
	Command string
	// URL and Headers configure an http hook.
	URL     string
	Headers map[string]string
	// Timeout bounds one run; 0 means the Runner's default.
	Timeout time.Duration
	// FailClosed overrides Runner.FailClosed for this hook when set.
	FailClosed *bool
	// If is the hook's "if" condition as written. It is parsed but not
	// evaluated; see the package documentation.
	If string
	// Origin says who vouches for the hook.
	Origin Origin

	// Source labels the configuration the hook came from ("~/.sleipnir/config.json"),
	// for messages; Group and Index locate it there: hooks[Event][Group].hooks[Index].
	Source       string
	Group, Index int

	m *matcher
}

// String describes the hook for messages: its position and what it runs.
func (h Hook) String() string {
	what := h.Command
	if h.Type == TypeHTTP {
		what = redactURL(h.URL)
	}
	pos := fmt.Sprintf("%s[%d].hooks[%d]", h.Event, h.Group, h.Index)
	if h.Source != "" {
		pos = h.Source + " " + pos
	}
	return fmt.Sprintf("%s (%s: %s)", pos, h.Type, clip(oneLine(what), 60))
}

// Key identifies the hook by its behaviour, not its position: two hooks with
// the same event, matcher and action share a key, so an approval given to one
// covers the other and identical hooks from several files run once.
func (h Hook) Key() string {
	hdr := make([]string, 0, len(h.Headers))
	for k, v := range h.Headers {
		hdr = append(hdr, k+"\x00"+v)
	}
	sort.Strings(hdr)
	sum := sha256.Sum256([]byte(strings.Join([]string{h.Event, h.Matcher, h.Type, h.Command, h.URL, h.If, strings.Join(hdr, "\x01")}, "\x02")))
	return hex.EncodeToString(sum[:])
}

// behaviorKey identifies what the hook does, ignoring where and under which
// matcher it is configured: two hooks with the same key are the same action, and
// an event that matches both should not perform it twice.
func (h Hook) behaviorKey() string {
	c := h
	c.Event, c.Matcher = "", ""
	return c.Key()
}

// redactURL shows where an http hook posts without the parts that can carry a
// secret: userinfo, query string and fragment. Hook descriptions end up in
// messages and logs.
func redactURL(raw string) string {
	u, err := url.Parse(raw)
	if err != nil {
		return "(unparseable URL)"
	}
	u.User, u.RawQuery, u.Fragment = nil, "", ""
	return u.String()
}

// oneLine collapses whitespace runs to single spaces and removes surrounding whitespace.
func oneLine(s string) string { return strings.Join(strings.Fields(s), " ") }

// Set is the parsed hook configuration. It is immutable and safe for concurrent
// use.
type Set struct {
	byEvent map[string][]Hook
	warns   []string
}

// Empty reports whether the set has no hooks.
func (s *Set) Empty() bool { return s == nil || len(s.byEvent) == 0 }

// Events lists the events that have hooks, in canonical order.
func (s *Set) Events() []string {
	if s == nil {
		return nil
	}
	var out []string
	for _, e := range eventOrder {
		if len(s.byEvent[e]) > 0 {
			out = append(out, e)
		}
	}
	return out
}

// Hooks returns the hooks of an event in configuration order.
func (s *Set) Hooks(event string) []Hook {
	if s == nil {
		return nil
	}
	c, ok := Canonical(event)
	if !ok {
		return nil
	}
	return append([]Hook(nil), s.byEvent[c]...)
}

// Warnings lists things Parse accepted but the user should know: ignored events
// and hook types, fields that are not acted on.
func (s *Set) Warnings() []string {
	if s == nil {
		return nil
	}
	return append([]string(nil), s.warns...)
}

// Matching returns the hooks that apply to ev, in configuration order: those
// whose matcher matches the event's tool (or, for other events, the field the
// event's matcher is tried against).
func (s *Set) Matching(ev Event) ([]Hook, error) {
	name, ok := Canonical(ev.Name)
	if !ok {
		return nil, fmt.Errorf("hooks: unknown event %q", clip(ev.Name, 40))
	}
	if s == nil {
		return nil, nil
	}
	candidates, supported := matchTarget(ev, name)
	var out []Hook
	for _, h := range s.byEvent[name] {
		if !supported || h.m == nil || h.m.matches(candidates) {
			out = append(out, h)
		}
	}
	return out, nil
}

// Merge combines sets, keeping configuration order: the hooks of the first set
// come before those of the second for each event. nil sets are skipped. Hooks
// keep their own Source, Group and Index, so messages still say where a hook
// was configured.
func Merge(sets ...*Set) *Set {
	out := &Set{byEvent: map[string][]Hook{}}
	for _, s := range sets {
		if s == nil {
			continue
		}
		for e, hs := range s.byEvent {
			out.byEvent[e] = append(out.byEvent[e], hs...)
		}
		out.warns = append(out.warns, s.warns...)
	}
	return out
}

// Event is one occurrence a hook may react to.
type Event struct {
	// Name is the event, in any spelling Canonical accepts.
	Name string
	// Tool is the tool involved (PreToolUse, PostToolUse, PostToolUseFailure,
	// PermissionRequest), by its Sleipnir name; hooks see and match its Claude
	// Code name as well.
	Tool string
	// Input is the tool's input as JSON.
	Input json.RawMessage
	// Output is the tool's result text (PostToolUse) or its error.
	Output string
	// Agent and Role identify the agent the event belongs to; for SubagentStart
	// and SubagentStop they are the subagent's.
	Agent, Role string
	SessionID   string
	// Cwd is the agent's working directory; hooks run there.
	Cwd string
	// Extra adds event-specific fields to the payload: "prompt" (UserPromptSubmit),
	// "source" (SessionStart: startup, resume, clear or compact), "reason"
	// (SessionEnd), "message" and "notification_type" (Notification), "trigger"
	// (PreCompact and PostCompact: manual or auto), and "stop_hook_active" (Stop
	// and SubagentStop: true when the agent is already continuing because an
	// earlier Stop hook blocked, which is how a hook avoids blocking forever). Keys
	// that the payload defines itself are ignored. SessionStart, SessionEnd,
	// Notification, PreCompact and PostCompact hooks are matched on "source",
	// "reason", "notification_type" and "trigger".
	Extra map[string]any
}

// Decision is a hook's answer to a permission question.
type Decision string

const (
	// DecisionNone means no hook expressed an opinion.
	DecisionNone Decision = ""
	// Allow lets the action proceed without asking. It answers a question that
	// would otherwise be put to a human; it never overrides a denial by the
	// permission engine.
	Allow Decision = "allow"
	// Ask sends the action to the human even where rules would allow it.
	Ask Decision = "ask"
	// Deny refuses the action.
	Deny Decision = "deny"
)

// Result is what the hooks of one event decided, combined deterministically:
// hooks run in parallel, but their answers are folded in configuration order.
type Result struct {
	// Blocked says the hooks stopped what the event stands for: the tool call
	// (PreToolUse), the prompt (UserPromptSubmit), the stop (Stop, SubagentStop),
	// the compaction (PreCompact). On PostToolUse and PostToolUseFailure the
	// action already happened, and Blocked with Reason means "tell the model".
	Blocked bool
	// Decision is the combined permission answer on PreToolUse and
	// PermissionRequest: Deny beats Ask beats Allow. A blocked PreToolUse or
	// PermissionRequest is always Deny.
	Decision Decision
	// Reason explains a block or decision, in configuration order. It is written
	// for the model or the user to act on.
	Reason string
	// AdditionalContext is text for the model to see, concatenated in
	// configuration order and capped.
	AdditionalContext string
	// UpdatedInput replaces the tool input (PreToolUse). When several hooks
	// return one, the first in configuration order wins and the others are
	// reported in Errors. Never set on a denial.
	UpdatedInput json.RawMessage
	// Errors are failures that do not block: a hook that crashed, timed out,
	// printed invalid JSON, or was not run because it is not trusted. Show them to
	// the user.
	Errors []HookError
	// Stop asks the harness to end the whole run ("continue": false); StopReason
	// says why. It takes precedence over everything else.
	Stop       bool
	StopReason string
	// Messages are systemMessage texts for the user, in configuration order.
	Messages []string
	// Runs describes each hook that was considered, in configuration order.
	Runs []Run
}

// HookError is a hook failure that does not block.
type HookError struct {
	Hook    string
	Message string
}

// Error prefixes a hook diagnostic with the hook's name.
func (e HookError) Error() string { return e.Hook + ": " + e.Message }

// Run records what happened to one hook.
type Run struct {
	Hook     string
	Origin   Origin
	Duration time.Duration
	// ExitCode is the process exit status (-1 when it never ran or was killed by
	// the runner; 128+N when a signal ended it).
	ExitCode int
	TimedOut bool
	// Skipped says why the hook did not run ("" when it did).
	Skipped string
}

// Ran reports how many hooks actually ran.
func (r Result) Ran() int {
	n := 0
	for _, run := range r.Runs {
		if run.Skipped == "" {
			n++
		}
	}
	return n
}
