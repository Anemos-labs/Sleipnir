package mdfile

import (
	"fmt"
	"strings"
)

// MaxArgsBytes bounds the argument string of a skill or command invocation.
const MaxArgsBytes = 32 << 10

// CleanArgs prepares an argument string typed by a user or produced by a
// model: over-long input is an error, and NULs, control characters and hidden
// Unicode are removed (newlines and tabs survive: a pasted paragraph is a
// legitimate argument).
func CleanArgs(args string) (string, error) {
	if len(args) > MaxArgsBytes {
		return "", fmt.Errorf("arguments are %d bytes long; the limit is %d", len(args), MaxArgsBytes)
	}
	s, _ := Sanitize(strings.ToValidUTF8(args, "�"))
	return strings.TrimSpace(strings.ReplaceAll(s, "\r", "")), nil
}

// SplitArgs splits an argument string into words the way a shell would:
// white space separates words, single and double quotes group them (an empty
// pair of quotes is an empty word), and a backslash escapes the next character
// outside single quotes. An unterminated quote runs to the end of the input.
func SplitArgs(s string) []string {
	var out []string
	var cur strings.Builder
	have := false // a word is in progress, so "" still counts as one
	inS, inD := false, false
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case inS:
			if c == '\'' {
				inS = false
			} else {
				cur.WriteByte(c)
			}
		case inD:
			switch {
			case c == '"':
				inD = false
			case c == '\\' && i+1 < len(s) && strings.IndexByte("\"\\$`", s[i+1]) >= 0:
				i++
				cur.WriteByte(s[i])
			default:
				cur.WriteByte(c)
			}
		case c == '\'':
			inS, have = true, true
		case c == '"':
			inD, have = true, true
		case c == '\\' && i+1 < len(s):
			i++
			cur.WriteByte(s[i])
			have = true
		case c == ' ' || c == '\t' || c == '\n':
			if have {
				out = append(out, cur.String())
				cur.Reset()
				have = false
			}
		default:
			cur.WriteByte(c)
			have = true
		}
	}
	if have {
		out = append(out, cur.String())
	}
	return out
}

// ShellQuote quotes s as one word for a POSIX shell.
func ShellQuote(s string) string {
	if s == "" {
		return "''"
	}
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// ArgMode says how arguments are spelled where they are substituted.
type ArgMode uint8

const (
	// ArgsText inserts arguments as typed.
	ArgsText ArgMode = iota
	// ArgsShell inserts each word single-quoted, so that an argument can never
	// add an operator ("; rm -rf ~") to a shell command it is substituted into.
	ArgsShell
)

// Substitute replaces the argument placeholders of tmpl in one pass:
//
//	$ARGUMENTS      all arguments
//	$ARGUMENTS[N]   the Nth word, counting from 0
//	$1 .. $9        the Nth word, counting from 1 ($10 and up are left alone)
//
// A missing word is empty. Prefix a placeholder with a second "$" to keep it
// literal ("$$1" gives "$1"). used reports whether any placeholder was found,
// so that a caller can append the arguments when the template had no place for
// them.
//
// The pass is single: the replacement text is copied to the output and never
// scanned again, so an argument that itself contains "$1" or "$ARGUMENTS" is
// inserted verbatim rather than expanded.
func Substitute(tmpl, args string, mode ArgMode) (out string, used bool) {
	if strings.IndexByte(tmpl, '$') < 0 {
		return tmpl, false
	}
	words := SplitArgs(args)
	raw := strings.TrimSpace(args)
	all := func() string {
		if mode != ArgsShell {
			return raw
		}
		q := make([]string, len(words))
		for i, w := range words {
			q[i] = ShellQuote(w)
		}
		return strings.Join(q, " ")
	}
	one := func(i int) string {
		if i < 0 || i >= len(words) {
			return ""
		}
		if mode == ArgsShell {
			return ShellQuote(words[i])
		}
		return words[i]
	}

	const kw = "ARGUMENTS"
	var b strings.Builder
	b.Grow(len(tmpl) + len(raw))
	for i := 0; i < len(tmpl); {
		j := strings.IndexByte(tmpl[i:], '$')
		if j < 0 {
			b.WriteString(tmpl[i:])
			break
		}
		b.WriteString(tmpl[i : i+j])
		i += j
		rest := tmpl[i+1:]
		idx, idxLen, isIdx := 0, 0, false
		if strings.HasPrefix(rest, kw+"[") {
			if k := strings.IndexByte(rest, ']'); k > len(kw)+1 {
				idx, isIdx = smallInt(rest[len(kw)+1 : k])
				idxLen = k + 1
			}
		}
		switch {
		case strings.HasPrefix(rest, "$") && escapable(rest[1:]):
			b.WriteByte('$') // "$$1": the second dollar's placeholder is copied as text below
			i += 2
		case isIdx:
			b.WriteString(one(idx))
			used = true
			i += 1 + idxLen
		case strings.HasPrefix(rest, kw) && !identChar(rest, len(kw)):
			b.WriteString(all())
			used = true
			i += 1 + len(kw)
		case rest != "" && rest[0] >= '1' && rest[0] <= '9' && (len(rest) == 1 || rest[1] < '0' || rest[1] > '9'):
			b.WriteString(one(int(rest[0] - '1')))
			used = true
			i += 2
		default:
			b.WriteByte('$')
			i++
		}
	}
	return b.String(), used
}

// escapable reports whether s begins a placeholder that "$$" may escape.
func escapable(s string) bool {
	if s == "" {
		return false
	}
	return s[0] >= '1' && s[0] <= '9' || strings.HasPrefix(s, "ARGUMENTS")
}

// identChar recognizes an ASCII letter, digit, or underscore at a nonnegative byte index,
// returning false beyond the string.
func identChar(s string, i int) bool {
	if i >= len(s) {
		return false
	}
	c := s[i]
	return c == '_' || c >= '0' && c <= '9' || c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z'
}

// smallInt parses one to three ASCII decimal digits without a sign, reporting false for any other
// input.
func smallInt(s string) (int, bool) {
	if s == "" || len(s) > 3 {
		return 0, false
	}
	n := 0
	for i := 0; i < len(s); i++ {
		if s[i] < '0' || s[i] > '9' {
			return 0, false
		}
		n = n*10 + int(s[i]-'0')
	}
	return n, true
}
