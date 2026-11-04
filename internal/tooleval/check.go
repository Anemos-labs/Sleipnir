package tooleval

import (
	"bytes"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"

	"github.com/anemos-labs/sleipnir/internal/core"
)

// Check reports whether the calls of a response meet e under the prompt's tools, and when they do not, why: the first
// reason, in the order a reader fixes them (a call the harness would refuse, then the rules).
func Check(calls []core.Block, tools []core.ToolSpec, e Expect) (bool, string) {
	byName := map[string]core.ToolSpec{}
	for _, t := range tools {
		byName[t.Name] = t
	}
	for _, c := range calls {
		if why := invalid(c, byName); why != "" {
			return false, why
		}
	}
	n := len(calls)
	switch {
	case n < e.MinCalls:
		return false, fmt.Sprintf("%d calls, fewer than %d", n, e.MinCalls)
	case e.MaxCalls > 0 && n > e.MaxCalls:
		return false, fmt.Sprintf("%d calls, more than %d", n, e.MaxCalls)
	}
	for _, r := range e.None {
		for _, c := range calls {
			if matches(r, c) {
				return false, "called " + shown(c) + why(r)
			}
		}
	}
	for _, r := range e.All {
		if !anyMatch(r, calls) {
			return false, "no call " + r.String() + why(r) + did(calls)
		}
	}
	if len(e.Any) > 0 {
		for _, r := range e.Any {
			if anyMatch(r, calls) {
				return true, ""
			}
		}
		names := make([]string, len(e.Any))
		for i, r := range e.Any {
			names[i] = r.String()
		}
		return false, "none of " + strings.Join(names, ", ") + did(calls)
	}
	return true, ""
}

// why is a rule's reason as the report adds it.
func why(r Rule) string {
	if r.Why == "" {
		return ""
	}
	return ": " + r.Why
}

// did says what the response did instead.
func did(calls []core.Block) string {
	if len(calls) == 0 {
		return " (no call: answered in text)"
	}
	var s []string
	for _, c := range calls {
		s = append(s, shown(c))
	}
	if len(s) > 4 {
		s = append(s[:4], fmt.Sprintf("and %d more", len(s)-4))
	}
	return " (called " + strings.Join(s, ", ") + ")"
}

// shown is a call as the report prints it: the tool and its arguments, clipped.
func shown(c core.Block) string {
	in := strings.Join(strings.Fields(string(c.Input)), " ")
	if len(in) > 160 {
		in = in[:160] + "…"
	}
	return c.ToolName + " " + in
}

// anyMatch reports whether some call matches r.
func anyMatch(r Rule, calls []core.Block) bool {
	for _, c := range calls {
		if matches(r, c) {
			return true
		}
	}
	return false
}

// matches reports whether call c matches rule r.
func matches(r Rule, c core.Block) bool {
	if c.ToolName != r.Tool {
		return false
	}
	if len(r.Args) == 0 {
		return true
	}
	var in map[string]any
	if json.Unmarshal(c.Input, &in) != nil {
		return false
	}
	for k, want := range r.Args {
		if !argMatches(want, in[k], k) {
			return false
		}
	}
	return true
}

// argMatches reports whether argument got matches want (see Rule).
func argMatches(want, got any, key string) bool {
	switch w := want.(type) {
	case string:
		re, err := regexp.Compile(w)
		if err != nil {
			return false
		}
		if got == nil {
			return re.MatchString("")
		}
		if s, ok := got.(string); ok {
			return re.MatchString(s)
		}
		b, _ := json.Marshal(got)
		return re.MatchString(string(b))
	case []any:
		g, ok := got.([]any)
		if !ok {
			return false
		}
		for _, we := range w {
			found := false
			for _, ge := range g {
				if argMatches(we, ge, key) {
					found = true
					break
				}
			}
			if !found {
				return false
			}
		}
		return true
	}
	a, _ := json.Marshal(want)
	b, _ := json.Marshal(got)
	return bytes.Equal(a, b)
}

// compileArgs checks that every regular expression of a rule's arguments compiles.
func compileArgs(args map[string]any) (int, error) {
	n := 0
	var walk func(v any) error
	walk = func(v any) error {
		switch w := v.(type) {
		case string:
			n++
			_, err := regexp.Compile(w)
			return err
		case []any:
			for _, e := range w {
				if err := walk(e); err != nil {
					return err
				}
			}
		}
		return nil
	}
	for _, v := range args {
		if err := walk(v); err != nil {
			return n, err
		}
	}
	return n, nil
}

// invalid says why the harness would refuse call c before it ran, "" when it would not: arguments that are not JSON, a tool
// the prompt does not have, or arguments its schema does not accept.
func invalid(c core.Block, tools map[string]core.ToolSpec) string {
	if c.Invalid != "" {
		return fmt.Sprintf("%s: the arguments are not JSON (%s)", c.ToolName, c.Invalid)
	}
	t, ok := tools[c.ToolName]
	if !ok {
		return fmt.Sprintf("called %q, which is not a tool", c.ToolName)
	}
	var in any
	if len(bytes.TrimSpace(c.Input)) == 0 {
		in = map[string]any{}
	} else if err := json.Unmarshal(c.Input, &in); err != nil {
		return fmt.Sprintf("%s: the arguments are not JSON (%v)", c.ToolName, err)
	}
	var schema map[string]any
	if len(t.InputSchema) == 0 || json.Unmarshal(t.InputSchema, &schema) != nil {
		return ""
	}
	if why := fits(in, schema, ""); why != "" {
		return c.ToolName + ": " + why
	}
	return ""
}

// fits checks v against the parts of a JSON schema a tool call goes wrong on: type, required properties, enum, and the
// items and properties within. Anything else in the schema is not checked.
func fits(v any, schema map[string]any, at string) string {
	name := at
	if name == "" {
		name = "the arguments"
	}
	if enum, ok := schema["enum"].([]any); ok {
		hit := false
		for _, e := range enum {
			a, _ := json.Marshal(e)
			b, _ := json.Marshal(v)
			if bytes.Equal(a, b) {
				hit = true
				break
			}
		}
		if !hit {
			b, _ := json.Marshal(v)
			return fmt.Sprintf("%s is %s, not one of %v", name, b, enum)
		}
	}
	typ, _ := schema["type"].(string)
	switch typ {
	case "object":
		m, ok := v.(map[string]any)
		if !ok {
			return name + " is not an object"
		}
		if req, ok := schema["required"].([]any); ok {
			for _, r := range req {
				if k, _ := r.(string); k != "" {
					if _, ok := m[k]; !ok {
						if at == "" {
							return fmt.Sprintf("no %q argument", k)
						}
						return fmt.Sprintf("%s has no %q", name, k)
					}
				}
			}
		}
		props, _ := schema["properties"].(map[string]any)
		for k, pv := range m {
			ps, ok := props[k].(map[string]any)
			if !ok || pv == nil {
				continue
			}
			if why := fits(pv, ps, join(at, k)); why != "" {
				return why
			}
		}
	case "array":
		a, ok := v.([]any)
		if !ok {
			return name + " is not an array"
		}
		if is, ok := schema["items"].(map[string]any); ok {
			for i, e := range a {
				if why := fits(e, is, fmt.Sprintf("%s[%d]", strings.TrimPrefix(join(at, ""), "."), i)); why != "" {
					return why
				}
			}
		}
	case "string":
		if _, ok := v.(string); !ok {
			return name + " is not a string"
		}
	case "integer":
		f, ok := v.(float64)
		if !ok || f != float64(int64(f)) {
			return name + " is not an integer"
		}
	case "number":
		if _, ok := v.(float64); !ok {
			return name + " is not a number"
		}
	case "boolean":
		if _, ok := v.(bool); !ok {
			return name + " is not a boolean"
		}
	}
	return ""
}

// join names a property under a path.
func join(at, k string) string {
	if at == "" {
		return k
	}
	if k == "" {
		return at
	}
	return at + "." + k
}
