package config

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
)

// Severity ranks an Issue.
type Severity int

const (
	SeverityWarning Severity = iota
	SeverityError
)

// String returns error for SeverityError and warning for other severity values.
func (s Severity) String() string {
	if s == SeverityError {
		return "error"
	}
	return "warning"
}

// MarshalText makes severities readable in JSON.
func (s Severity) MarshalText() ([]byte, error) { return []byte(s.String()), nil }

// Issue is one problem found in configuration: a syntax error, a value of the
// wrong type, an invalid value, an unknown key, something that looks like a
// secret. Where it came from is filled in when known.
type Issue struct {
	Severity Severity `json:"severity"`
	// Source is the file (or "env:NAME", or "overrides") that supplied the
	// offending value.
	Source string `json:"source,omitempty"`
	// Line and Col locate it in Source, 1-based; 0 when unknown.
	Line int `json:"line,omitempty"`
	Col  int `json:"col,omitempty"`
	// Path is the field path, such as providers.openai.dialect.
	Path    string `json:"path,omitempty"`
	Message string `json:"message"`

	// segs is Path as segments (provider names may contain dots), used to find
	// where a merged value came from.
	segs []string
	// code lets Load recognise issues it reports itself elsewhere.
	code string
}

// Error formats the issue as "file:line:col: path: message". Issue implements
// error so it can be returned, joined and matched with errors.As.
func (i Issue) Error() string {
	var b strings.Builder
	if i.Source != "" {
		b.WriteString(i.Source)
		if i.Line > 0 {
			fmt.Fprintf(&b, ":%d:%d", i.Line, i.Col)
		}
		b.WriteString(": ")
	}
	if i.Path != "" {
		b.WriteString(i.Path)
		b.WriteString(": ")
	}
	b.WriteString(i.Message)
	return b.String()
}

// String is Error with the severity in front.
func (i Issue) String() string { return i.Severity.String() + ": " + i.Error() }

// Errors joins the error-severity issues into one error (nil when there are
// none). Each element can be recovered with errors.As into an Issue.
func Errors(issues []Issue) error {
	var errs []error
	for _, i := range issues {
		if i.Severity == SeverityError {
			errs = append(errs, i)
		}
	}
	return errors.Join(errs...)
}

// fmtPath renders path segments for humans: dotted, with names that are not
// plain identifiers quoted and list indexes as [n].
func fmtPath(segs []string) string {
	var b strings.Builder
	for i, s := range segs {
		switch {
		case isIndexSeg(s):
			b.WriteString(s)
		case plainSeg(s):
			if i > 0 {
				b.WriteByte('.')
			}
			b.WriteString(s)
		default:
			b.WriteString("[" + strconv.Quote(s) + "]")
		}
	}
	return b.String()
}

// isIndexSeg recognizes a bracketed, nonempty sequence of decimal digits in a field path.
func isIndexSeg(s string) bool {
	return len(s) >= 3 && s[0] == '[' && s[len(s)-1] == ']' && strings.Trim(s[1:len(s)-1], "0123456789") == ""
}

// plainSeg accepts nonempty ASCII configuration path segments containing letters, digits,
// underscores, or hyphens.
func plainSeg(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '_', r == '-':
		default:
			return false
		}
	}
	return true
}

// cloneSegs allocates an independent path-segment slice containing segs followed by more.
func cloneSegs(segs []string, more ...string) []string {
	out := make([]string, 0, len(segs)+len(more))
	out = append(out, segs...)
	return append(out, more...)
}
