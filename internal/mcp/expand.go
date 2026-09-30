package mcp

import (
	"fmt"
	"sort"
	"strings"
)

// Configuration strings may reference variables: ${NAME} and ${NAME:-default}.
//
// The values come from an explicit map the caller passes in, never from the
// process environment. That is a security property, not a convenience: a
// project-level MCP file is written by whoever wrote the repository, and if
// ${ANYTHING} silently resolved against the harness's own environment, a
// repository could name "${ANTHROPIC_API_KEY}" in a header of a server it
// controls. With an explicit map, the caller decides exactly which variables
// configuration may see, and ServerConfig.EnvRefs tells an approval prompt
// which ones a given entry asks for.
//
// Grammar:
//
//	${NAME}           the value of NAME; an error if NAME is not in the map
//	${NAME:-default}  the value of NAME, or default when it is unset or empty;
//	                  default may itself contain ${...} references
//	$${               a literal "${"
//	$NAME, $          left alone (no braces, no expansion)
//
// Values are inserted verbatim and are not expanded again.

// UnsetVarError reports a ${NAME} reference with no value and no default.
type UnsetVarError struct{ Name string }

func (e *UnsetVarError) Error() string {
	return fmt.Sprintf("variable %s is not set (write ${%s:-default} to allow a default)", e.Name, e.Name)
}

const (
	maxExpandDepth = 8
	maxExpandedLen = 64 << 10
)

// EnvMap converts "K=V" entries (os.Environ() format) into a map, for callers
// that deliberately want to hand configuration the process environment or a
// filtered part of it. The explicit call at the call site is the point.
func EnvMap(environ []string) map[string]string {
	m := make(map[string]string, len(environ))
	for _, kv := range environ {
		k, v, ok := strings.Cut(kv, "=")
		if !ok || k == "" {
			continue
		}
		m[k] = v
	}
	return m
}

// expandString expands the references in s using env.
func expandString(s string, env map[string]string) (string, error) {
	out, err := expandDepth(s, env, nil, 0)
	if err != nil {
		return "", err
	}
	if len(out) > maxExpandedLen {
		return "", fmt.Errorf("expanded value is longer than %d bytes", maxExpandedLen)
	}
	return out, nil
}

// refsIn returns the variable names s references, without needing values.
func refsIn(s string) []string {
	if !strings.Contains(s, "${") {
		return nil
	}
	var refs []string
	_, _ = expandDepth(s, nil, &refs, 0)
	return refs
}

// expandDepth is the expander. With collect != nil it only records names
// (missing values are not errors), which is how EnvRefs shares the parser.
func expandDepth(s string, env map[string]string, collect *[]string, depth int) (string, error) {
	if !strings.Contains(s, "$") {
		return s, nil
	}
	if depth > maxExpandDepth {
		return "", fmt.Errorf("${...} references nested deeper than %d levels", maxExpandDepth)
	}
	var b strings.Builder
	for i := 0; i < len(s); {
		c := s[i]
		if c != '$' {
			b.WriteByte(c)
			i++
			continue
		}
		rest := s[i:]
		switch {
		case strings.HasPrefix(rest, "$${"):
			b.WriteString("${")
			i += 3
			continue
		case !strings.HasPrefix(rest, "${"):
			b.WriteByte('$')
			i++
			continue
		}
		end, err := closingBrace(s, i+2)
		if err != nil {
			return "", err
		}
		val, err := expandRef(s[i+2:end], env, collect, depth)
		if err != nil {
			return "", err
		}
		b.WriteString(val)
		if b.Len() > maxExpandedLen {
			return "", fmt.Errorf("expanded value is longer than %d bytes", maxExpandedLen)
		}
		i = end + 1
	}
	return b.String(), nil
}

// closingBrace finds the '}' that closes the "${" whose body starts at from,
// counting nested "${" and skipping "$${" escapes.
func closingBrace(s string, from int) (int, error) {
	depth := 1
	for i := from; i < len(s); i++ {
		switch {
		case strings.HasPrefix(s[i:], "$${"):
			i += 2
		case strings.HasPrefix(s[i:], "${"):
			depth++
			i++
		case s[i] == '}':
			depth--
			if depth == 0 {
				return i, nil
			}
		}
	}
	return 0, fmt.Errorf("unterminated ${ (missing closing brace)")
}

func expandRef(body string, env map[string]string, collect *[]string, depth int) (string, error) {
	name, def, hasDef := strings.Cut(body, ":-")
	if !validVarName(name) {
		return "", fmt.Errorf("invalid variable name %q in ${...}", clipForError(name))
	}
	if collect != nil {
		*collect = append(*collect, name)
		if hasDef {
			if _, err := expandDepth(def, env, collect, depth+1); err != nil {
				return "", err
			}
		}
		return "", nil
	}
	v, ok := env[name]
	if hasDef {
		if ok && v != "" {
			return v, nil
		}
		return expandDepth(def, env, nil, depth+1)
	}
	if !ok {
		return "", &UnsetVarError{Name: name}
	}
	return v, nil
}

func validVarName(n string) bool {
	if n == "" {
		return false
	}
	for i := 0; i < len(n); i++ {
		c := n[i]
		switch {
		case c == '_' || c >= 'A' && c <= 'Z' || c >= 'a' && c <= 'z':
		case c >= '0' && c <= '9' && i > 0:
		default:
			return false
		}
	}
	return true
}

// clipForError bounds config-derived text placed in an error message.
func clipForError(s string) string {
	s, _ = truncateRunes(cleanText(s), 40)
	return s
}

func sortedUnique(in []string) []string {
	if len(in) == 0 {
		return nil
	}
	m := make(map[string]struct{}, len(in))
	for _, s := range in {
		m[s] = struct{}{}
	}
	out := make([]string, 0, len(m))
	for s := range m {
		out = append(out, s)
	}
	sort.Strings(out)
	return out
}
