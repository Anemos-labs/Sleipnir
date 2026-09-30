package hooks

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net/url"
	"sort"
	"strings"
	"time"

	"github.com/reee344/sleipnir/internal/perm"
)

const (
	// MaxCommandBytes caps one hook command line.
	MaxCommandBytes = 16 << 10
	// maxParseErrors bounds how many problems one Parse reports.
	maxParseErrors = 20
	// maxGroups, maxHooksPerGroup and maxHooksPerEvent bound a hostile
	// configuration: an event that runs thousands of processes is not a hook setup.
	maxGroups        = 256
	maxHooksPerGroup = 64
	maxHooksPerEvent = 128
)

// Parse reads the value of a settings file's "hooks" key (config.Config.Hooks):
//
//	{"PreToolUse": [{"matcher": "Bash|Edit", "hooks": [
//	    {"type": "command", "command": "./check.sh", "timeout": 30}]}]}
//
// Event names may be spelled in any way Canonical accepts. The hooks it returns
// have no vouched-for origin, which means the Runner runs them only when it is
// itself trusted (or an Approve callback agrees): use ParseAs to mark hooks from
// the user's own configuration.
//
// Structural problems are errors, each naming the event and the index that is
// wrong ("hooks: PreToolUse[1].hooks[0]: command is required"), and all of them
// are reported together, sorted, so a user can fix them in one pass. Things
// that are valid Claude Code configuration but that Sleipnir does not act on
// (events it never fires, hook types "prompt" and "agent", fields such as
// "async") are not errors: they are skipped and listed in Set.Warnings.
func Parse(m map[string]json.RawMessage) (*Set, error) { return ParseAs("", "", m) }

// ParseAs is Parse for hooks whose origin is known. source labels the
// configuration in messages (a file name).
func ParseAs(origin Origin, source string, m map[string]json.RawMessage) (*Set, error) {
	p := &parser{origin: origin, source: source, set: &Set{byEvent: map[string][]Hook{}}}
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, key := range keys {
		p.event(key, m[key])
	}
	if len(p.errs) > 0 {
		sort.Strings(p.errs)
		errs := p.errs
		if len(errs) > maxParseErrors {
			errs = append(errs[:maxParseErrors:maxParseErrors], fmt.Sprintf("... and %d more problems", len(p.errs)-maxParseErrors))
		}
		return nil, errors.New(strings.Join(errs, "\n"))
	}
	return p.set, nil
}

type parser struct {
	origin Origin
	source string
	set    *Set
	errs   []string
}

func (p *parser) errorf(where, format string, args ...any) {
	prefix := "hooks: "
	if p.source != "" {
		prefix = "hooks (" + p.source + "): "
	}
	p.errs = append(p.errs, prefix+where+": "+fmt.Sprintf(format, args...))
}

func (p *parser) warnf(where, format string, args ...any) {
	prefix := ""
	if p.source != "" {
		prefix = p.source + ": "
	}
	p.set.warns = append(p.set.warns, prefix+where+": "+fmt.Sprintf(format, args...))
}

func isNull(raw json.RawMessage) bool { return bytes.Equal(bytes.TrimSpace(raw), []byte("null")) }

func (p *parser) event(key string, raw json.RawMessage) {
	event, ok := Canonical(key)
	if !ok {
		if isClaudeOnly(key) {
			p.warnf(key, "Sleipnir does not fire this event; its hooks are ignored")
			return
		}
		msg := fmt.Sprintf("unknown event %q", clip(key, 40))
		if s := suggest(key); s != "" {
			msg += fmt.Sprintf(" (did you mean %q?)", s)
		}
		p.errorf(clip(key, 40), "%s; the events are %s", msg, strings.Join(eventOrder, ", "))
		return
	}
	if isNull(raw) {
		return // a null removes the event in layered configuration
	}
	var groups []json.RawMessage
	if err := json.Unmarshal(raw, &groups); err != nil {
		p.errorf(key, "expected a list of matcher groups, [{\"matcher\": ..., \"hooks\": [...]}]: %v", jsonProblem(err))
		return
	}
	if len(groups) > maxGroups {
		p.errorf(key, "%d matcher groups; the limit is %d", len(groups), maxGroups)
		return
	}
	for gi, g := range groups {
		if len(p.set.byEvent[event]) > maxHooksPerEvent {
			break // already reported below
		}
		p.group(key, event, gi, g)
	}
	if n := len(p.set.byEvent[event]); n > maxHooksPerEvent {
		p.errorf(key, "%d hooks for one event; the limit is %d", n, maxHooksPerEvent)
	}
}

func (p *parser) group(key, event string, gi int, raw json.RawMessage) {
	where := fmt.Sprintf("%s[%d]", key, gi)
	var obj map[string]json.RawMessage
	if err := json.Unmarshal(raw, &obj); err != nil || obj == nil {
		p.errorf(where, "expected an object like {\"matcher\": \"Bash\", \"hooks\": [...]}")
		return
	}
	var matcherText string
	if mr, ok := obj["matcher"]; ok && !isNull(mr) {
		if err := json.Unmarshal(mr, &matcherText); err != nil {
			p.errorf(where+".matcher", "expected a string")
			return
		}
	}
	m, err := compileMatcher(matcherText)
	if err != nil {
		p.errorf(where+".matcher", "%v", err)
		return
	}
	hr, ok := obj["hooks"]
	if !ok {
		if _, direct := obj["command"]; direct {
			p.errorf(where, "found a hook where a matcher group is expected; wrap it: {\"hooks\": [{...}]}")
		} else {
			p.errorf(where, "\"hooks\" is required")
		}
		return
	}
	var hooks []json.RawMessage
	if err := json.Unmarshal(hr, &hooks); err != nil {
		p.errorf(where+".hooks", "expected a list of hooks: %v", jsonProblem(err))
		return
	}
	if len(hooks) > maxHooksPerGroup {
		p.errorf(where+".hooks", "%d hooks; the limit is %d", len(hooks), maxHooksPerGroup)
		return
	}
	for k := range obj {
		if k != "matcher" && k != "hooks" {
			p.warnf(where, "unknown field %q is ignored", clip(k, 30))
		}
	}
	for hi, h := range hooks {
		p.hook(event, matcherText, m, gi, hi, where+fmt.Sprintf(".hooks[%d]", hi), h)
	}
}

// hookFields are the fields of a hook object this package understands or
// deliberately ignores; anything else is reported.
var (
	knownHookFields   = []string{"type", "command", "url", "headers", "timeout", "if", "failClosed", "statusMessage"}
	ignoredHookFields = []string{"async", "once", "shell", "asyncRewake", "allowedEnvVars"}
)

func (p *parser) hook(event, matcherText string, m *matcher, gi, hi int, where string, raw json.RawMessage) {
	var obj map[string]json.RawMessage
	if err := json.Unmarshal(raw, &obj); err != nil || obj == nil {
		p.errorf(where, "expected an object like {\"type\": \"command\", \"command\": \"...\"}")
		return
	}
	bad := false // a field had the wrong type and was reported
	str := func(name string) (string, bool) {
		r, ok := obj[name]
		if !ok || isNull(r) {
			return "", false
		}
		var s string
		if err := json.Unmarshal(r, &s); err != nil {
			p.errorf(where+"."+name, "expected a string")
			bad = true
			return "", false
		}
		return s, true
	}
	h := Hook{Event: event, Matcher: matcherText, Origin: p.origin, Source: p.source, Group: gi, Index: hi, m: m}

	typ, hasType := str("type")
	command, hasCommand := str("command")
	target, hasURL := str("url")
	cond, hasCond := str("if")
	if bad {
		return
	}
	if !hasType {
		switch {
		case hasCommand:
			typ = TypeCommand
		case hasURL:
			typ = TypeHTTP
		default:
			p.errorf(where, "\"type\" is required (\"command\" or \"http\")")
			return
		}
	}
	h.Type = typ
	switch typ {
	case TypeCommand:
		if strings.TrimSpace(command) == "" {
			p.errorf(where, "command is required")
			return
		}
		if len(command) > MaxCommandBytes {
			p.errorf(where, "command is %d bytes long; the limit is %d", len(command), MaxCommandBytes)
			return
		}
		if strings.IndexByte(command, 0) >= 0 {
			p.errorf(where, "command contains a NUL byte")
			return
		}
		h.Command = command
	case TypeHTTP:
		u, err := url.Parse(strings.TrimSpace(target))
		switch {
		case !hasURL || strings.TrimSpace(target) == "":
			p.errorf(where, "url is required for an http hook")
			return
		case err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "":
			p.errorf(where, "url must be an absolute http or https URL")
			return
		case u.User != nil:
			p.errorf(where, "url must not contain credentials; use a header")
			return
		}
		h.URL = u.String()
		if hr, ok := obj["headers"]; ok && !isNull(hr) {
			var hdr map[string]string
			if err := json.Unmarshal(hr, &hdr); err != nil {
				p.errorf(where+".headers", "expected an object of strings")
				return
			}
			for k, v := range hdr {
				if !validHeader(k, v) {
					p.errorf(where+".headers", "header %q is not valid", clip(k, 30))
					return
				}
			}
			h.Headers = hdr
		}
	default:
		p.warnf(where, "hook type %q is not supported; the hook is ignored (supported: command, http)", clip(typ, 30))
		return
	}

	if tr, ok := obj["timeout"]; ok && !isNull(tr) {
		var sec float64
		if err := json.Unmarshal(tr, &sec); err != nil {
			p.errorf(where+".timeout", "expected a number of seconds")
			return
		}
		if sec < 0 || math.IsNaN(sec) || math.IsInf(sec, 0) {
			p.errorf(where+".timeout", "must be a positive number of seconds, got %v", sec)
			return
		}
		h.Timeout = time.Duration(math.Min(sec, 86400) * float64(time.Second))
	}
	if fr, ok := obj["failClosed"]; ok && !isNull(fr) {
		var b bool
		if err := json.Unmarshal(fr, &b); err != nil {
			p.errorf(where+".failClosed", "expected true or false")
			return
		}
		h.FailClosed = &b
	}
	if hasCond && strings.TrimSpace(cond) != "" {
		cond = strings.TrimSpace(cond)
		if _, err := perm.ParseRule(perm.Deny, cond); err != nil {
			p.errorf(where+".if", "%v", jsonProblem(err))
			return
		}
		switch event {
		case PreToolUse, PostToolUse, PostToolUseFailure, PermissionRequest:
			h.If = cond
		default:
			p.warnf(where, "\"if\" only applies to tool events; it is ignored on %s", event)
		}
	}
	for k := range obj {
		if contains(knownHookFields, k) {
			continue
		}
		if contains(ignoredHookFields, k) {
			p.warnf(where, "field %q is not supported and is ignored", k)
			continue
		}
		p.warnf(where, "unknown field %q is ignored", clip(k, 30))
	}
	p.set.byEvent[event] = append(p.set.byEvent[event], h)
}

func contains(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}

// validHeader reports whether k and v can be an HTTP header: an RFC 7230 token
// name, and a value without control characters (no header injection).
func validHeader(k, v string) bool {
	if k == "" || len(k) > 128 || len(v) > 4096 {
		return false
	}
	for i := 0; i < len(k); i++ {
		c := k[i]
		isToken := c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || strings.IndexByte("!#$%&'*+-.^_`|~", c) >= 0
		if !isToken {
			return false
		}
	}
	for i := 0; i < len(v); i++ {
		if c := v[i]; c < 0x20 && c != '\t' || c == 0x7F {
			return false
		}
	}
	return true
}

// jsonProblem trims encoding/json's error text to what a person needs.
func jsonProblem(err error) string {
	if err == nil {
		return "unexpected value"
	}
	var ute *json.UnmarshalTypeError
	if errors.As(err, &ute) {
		return fmt.Sprintf("found %s", ute.Value)
	}
	return err.Error()
}

// suggest returns the canonical event name closest to key, if it is close enough
// to be a typo.
func suggest(key string) string {
	k := normEvent(key)
	best, bestD := "", 1<<30
	for _, e := range eventOrder {
		if d := editDistance(k, normEvent(e)); d < bestD {
			best, bestD = e, d
		}
	}
	if bestD <= 2 || (bestD <= 3 && len(k) > 8) {
		return best
	}
	return ""
}

func editDistance(a, b string) int {
	prev := make([]int, len(b)+1)
	for j := range prev {
		prev[j] = j
	}
	for i := 1; i <= len(a); i++ {
		cur := make([]int, len(b)+1)
		cur[0] = i
		for j := 1; j <= len(b); j++ {
			cost := 1
			if a[i-1] == b[j-1] {
				cost = 0
			}
			cur[j] = min(prev[j]+1, cur[j-1]+1, prev[j-1]+cost)
		}
		prev = cur
	}
	return prev[len(b)]
}
