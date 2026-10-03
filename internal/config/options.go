package config

import (
	"fmt"
	"math"
	"slices"
	"sort"
	"strings"
)

// The options a provider entry may carry ("providers.<name>.options"), by dialect.
// session.buildChat and session.buildAnthropic read exactly these keys; the test
// TestProviderOptionsMatchTheBuilders (internal/session) parses those functions and
// fails when this list and what they read differ, so an option cannot be added to
// one side only.

type optKind uint8

const (
	optBool optKind = iota + 1
	optString
	optInt     // a whole number, not negative
	optSeconds // a number of seconds: not negative, and at most a day
	optStrings // a list of strings
	optObject
)

// String returns a user-facing description of the expected provider option type.
func (k optKind) String() string {
	switch k {
	case optBool:
		return "true or false"
	case optString:
		return "a string"
	case optInt:
		return "a whole number"
	case optSeconds:
		return "a number of seconds"
	case optStrings:
		return "a list of strings"
	case optObject:
		return "an object"
	}
	return "a value"
}

type optionSpec struct {
	kind optKind
	// values are the strings the adapter understands, compared without regard to
	// case; empty means any string is passed through.
	values []string
}

// maxOptionSeconds is the longest timeout the adapters accept (they cap at a day).
const maxOptionSeconds = 86400

// commonOptions are read for every dialect.
var commonOptions = map[string]optionSpec{
	"first_byte_timeout_sec":  {kind: optSeconds},
	"stream_idle_timeout_sec": {kind: optSeconds},
	"stream_timeout_sec":      {kind: optSeconds},
	"request_timeout_sec":     {kind: optSeconds},
	"extra_body":              {kind: optObject},
	"capture_tokens":          {kind: optBool},
	"context_window":          {kind: optInt}, // tokens: what the server really gives the model (a local server's catalogue does not say)
}

// chatOptions are the extras of the chat-completions dialect.
var chatOptions = map[string]optionSpec{
	"session_header":         {kind: optBool},
	"cache_key_body":         {kind: optBool},
	"cache_control_parts":    {kind: optBool},
	"system_role":            {kind: optString, values: []string{"system", "developer"}},
	"max_tokens_field":       {kind: optString, values: []string{"max_tokens", "max_completion_tokens"}},
	"reasoning_effort_field": {kind: optString},
}

// anthropicOptions are the extras of the Messages dialect.
var anthropicOptions = map[string]optionSpec{
	"auth_style":            {kind: optString, values: []string{"x-api-key", "bearer"}},
	"version":               {kind: optString},
	"betas":                 {kind: optStrings},
	"session_header_name":   {kind: optString},
	"cache_control":         {kind: optBool},
	"no_turn_scoped_system": {kind: optBool},
	"no_thinking_replay":    {kind: optBool},
	"no_zero_max_tokens":    {kind: optBool},
	"default_max_tokens":    {kind: optInt},
	"thinking_budget":       {kind: optInt},
	"max_breakpoints":       {kind: optInt},
	"thinking_display":      {kind: optString, values: []string{"summarized", "omitted", "updates"}},
}

// optionSpecs returns the options a provider of the dialect accepts. ok is false for
// a dialect that has none to check against (one whose adapter is not built).
func optionSpecs(dialect string) (specs map[string]optionSpec, ok bool) {
	var extra map[string]optionSpec
	switch dialect {
	case DialectOpenAIChat:
		extra = chatOptions
	case DialectAnthropic:
		extra = anthropicOptions
	default:
		return nil, false
	}
	out := make(map[string]optionSpec, len(commonOptions)+len(extra))
	for k, s := range commonOptions {
		out[k] = s
	}
	for k, s := range extra {
		out[k] = s
	}
	return out, true
}

// ProviderOptionNames lists, sorted, the option names a provider of the dialect
// reads (empty for a dialect that reads none).
func ProviderOptionNames(dialect string) []string {
	specs, _ := optionSpecs(dialect)
	names := make([]string, 0, len(specs))
	for k := range specs {
		names = append(names, k)
	}
	sort.Strings(names)
	return names
}

// providerOptions checks the options of one provider entry: a name the dialect does
// not read is warned about (it has no effect, and is most likely a typo or an option
// of the other dialect), a value of the wrong kind is an error.
func (v *validator) providerOptions(base []string, p Provider) {
	if len(p.Options) == 0 {
		return
	}
	dialect := p.EffectiveDialect()
	specs, ok := optionSpecs(dialect)
	if !ok {
		return
	}
	names := make([]string, 0, len(specs))
	for k := range specs {
		names = append(names, k)
	}
	sort.Strings(names)
	for _, key := range sortedKeys(p.Options) {
		at := append(slices.Clone(base), "options", key)
		spec, known := specs[key]
		if !known {
			msg := fmt.Sprintf("unknown option %q for the %s dialect; it is ignored", key, dialect)
			switch {
			case slices.Contains(ProviderOptionNames(otherDialect(dialect)), key):
				msg += fmt.Sprintf(" (%q is an option of the %s dialect)", key, otherDialect(dialect))
			default:
				if s := closest(key, names); s != "" {
					msg += fmt.Sprintf(" (did you mean %q?)", s)
				}
			}
			v.warn(at, "%s", msg)
			continue
		}
		v.optionValue(at, spec, p.Options[key])
	}
}

// otherDialect selects OpenAI Chat for Anthropic input and Anthropic otherwise for cross-dialect
// diagnostics.
func otherDialect(d string) string {
	if d == DialectAnthropic {
		return DialectOpenAIChat
	}
	return DialectAnthropic
}

func (v *validator) optionValue(at []string, spec optionSpec, val any) {
	bad := func() { v.err(at, "must be %s", spec.kind) }
	switch spec.kind {
	case optBool:
		if _, ok := val.(bool); !ok {
			bad()
		}
	case optString:
		s, ok := val.(string)
		if !ok {
			bad()
			return
		}
		if len(spec.values) > 0 && !slices.ContainsFunc(spec.values, func(x string) bool { return strings.EqualFold(x, s) }) {
			v.err(at, "must be one of %s, got %q", strings.Join(spec.values, ", "), s)
		}
	case optInt, optSeconds:
		f, ok := optNumber(val)
		switch {
		case !ok || math.IsNaN(f) || math.IsInf(f, 0):
			bad()
		case spec.kind == optInt && f != math.Trunc(f):
			v.err(at, "must be a whole number, got %v", f)
		case f < 0:
			v.err(at, "must not be negative, got %v", f)
		case spec.kind == optSeconds && f > maxOptionSeconds:
			v.warn(at, "%v seconds is more than a day; it is capped at %d", f, maxOptionSeconds)
		}
	case optStrings:
		list, ok := val.([]any)
		if !ok {
			if _, isStrs := val.([]string); isStrs {
				return
			}
			bad()
			return
		}
		for _, e := range list {
			if _, ok := e.(string); !ok {
				bad()
				return
			}
		}
	case optObject:
		if _, ok := val.(map[string]any); !ok {
			bad()
		}
	}
}

// optNumber reads a JSON number as the option readers do (float64 from a decoded
// file, int from code).
func optNumber(v any) (float64, bool) {
	switch n := v.(type) {
	case float64:
		return n, true
	case int:
		return float64(n), true
	case int64:
		return float64(n), true
	}
	return 0, false
}
