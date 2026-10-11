package taskgen

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"unicode/utf8"
)

// PytestINIFromDir returns the pytest configuration of the project in dir (a
// checkout of the reference) as the text of a pytest.ini, for
// SuiteSpec.PytestINI. It looks where pytest does, first match wins: pytest.ini
// (even without a [pytest] section), the [tool.pytest.ini_options] table of
// pyproject.toml, the [pytest] section of tox.ini and the [tool:pytest] section
// of setup.cfg. A project with none gets "[pytest]\n". Of TOML, the table's
// strings (multi-line ones included), string arrays, booleans and numbers are
// understood; anything else (an inline table) is an error, and the caller
// writes the configuration itself.
func PytestINIFromDir(dir string) (string, error) {
	return pytestINI(func(name string) (string, bool, error) {
		b, err := os.ReadFile(filepath.Join(dir, name))
		if errors.Is(err, os.ErrNotExist) {
			return "", false, nil
		}
		if err != nil {
			return "", false, err
		}
		return string(b), true, nil
	})
}

// pytestINI is PytestINIFromDir over a reader of the project's files: read
// returns a file's content and whether it exists.
func pytestINI(read func(name string) (string, bool, error)) (string, error) {
	if src, ok, err := read("pytest.ini"); err != nil || ok {
		if err != nil {
			return "", err
		}
		body, found := iniSection(src, "pytest")
		if !found {
			return "[pytest]\n", nil
		}
		return "[pytest]\n" + body, nil
	}
	if src, ok, err := read("pyproject.toml"); err != nil {
		return "", err
	} else if ok {
		body, found, err := pyprojectPytest(src)
		if err != nil {
			return "", fmt.Errorf("pyproject.toml: %w", err)
		}
		if found {
			return "[pytest]\n" + body, nil
		}
	}
	for _, f := range []struct{ file, section string }{{"tox.ini", "pytest"}, {"setup.cfg", "tool:pytest"}} {
		src, ok, err := read(f.file)
		if err != nil {
			return "", err
		}
		if !ok {
			continue
		}
		if body, found := iniSection(src, f.section); found {
			return "[pytest]\n" + body, nil
		}
	}
	return "[pytest]\n", nil
}

// iniSection returns the lines of section name of an INI file, up to the next
// section header.
func iniSection(src, name string) (string, bool) {
	var b strings.Builder
	in, found := false, false
	lines := strings.Split(strings.ReplaceAll(src, "\r\n", "\n"), "\n")
	if n := len(lines); lines[n-1] == "" {
		lines = lines[:n-1] // the line break that ends the file
	}
	for _, line := range lines {
		t := strings.TrimSpace(line)
		if strings.HasPrefix(line, "[") && strings.HasSuffix(t, "]") {
			in = strings.TrimSpace(t[1:len(t)-1]) == name
			found = found || in
			continue
		}
		if in {
			b.WriteString(line)
			b.WriteByte('\n')
		}
	}
	return b.String(), found
}

// pytestArgsKeys are the pytest options of type "args": the value is split
// like a shell command line, so array items are quoted.
var pytestArgsKeys = map[string]bool{
	"addopts": true, "testpaths": true, "norecursedirs": true, "python_files": true, "python_classes": true,
	"python_functions": true, "required_plugins": true, "usefixtures": true, "doctest_optionflags": true,
}

// shellSafe matches words that need no quoting.
var shellSafe = regexp.MustCompile(`^[A-Za-z0-9_@%+=:,./*-]+$`)

// pyprojectPytest converts the [tool.pytest.ini_options] table of a
// pyproject.toml to the lines of an INI section.
func pyprojectPytest(src string) (string, bool, error) {
	src = strings.ReplaceAll(src, "\r\n", "\n")
	lines := strings.Split(src, "\n")
	start := -1
	for i, l := range lines {
		if strings.TrimSpace(l) == "[tool.pytest.ini_options]" {
			start = i + 1
			break
		}
	}
	if start < 0 {
		return "", false, nil
	}
	sc := &tomlScanner{s: strings.Join(lines[start:], "\n")}
	var b strings.Builder
	for {
		sc.skipBlank()
		if sc.eof() || sc.peek() == '[' {
			break
		}
		key, err := sc.key()
		if err != nil {
			return "", true, err
		}
		sc.skipSpace()
		if !sc.consume('=') {
			return "", true, fmt.Errorf("[tool.pytest.ini_options]: %s: expected =", key)
		}
		sc.skipSpace()
		val, err := sc.value()
		if err != nil {
			return "", true, fmt.Errorf("[tool.pytest.ini_options]: %s: %w", key, err)
		}
		sc.skipSpace()
		if !sc.eof() && sc.peek() == '#' {
			sc.skipComment()
		}
		if !sc.eof() && !sc.consume('\n') {
			return "", true, fmt.Errorf("[tool.pytest.ini_options]: %s: unexpected text after the value", key)
		}
		line, err := iniValue(key, val)
		if err != nil {
			return "", true, err
		}
		b.WriteString(line)
	}
	return b.String(), true, nil
}

// iniValue writes one key of the converted table: a scalar on its line, a
// string with line breaks as its first line there and the others as indented
// continuation lines (pytest reads both forms alike: options it splits like a
// shell command line or by line ignore the blank lines and the indentation, which
// are dropped), an array as one item per indented line (quoted for options
// pytest splits like a shell command line).
func iniValue(key string, v any) (string, error) {
	switch x := v.(type) {
	case string:
		if strings.ContainsAny(x, "\r\n") {
			var lines []string
			for _, l := range strings.Split(strings.ReplaceAll(x, "\r\n", "\n"), "\n") {
				if l = strings.TrimSpace(l); l != "" {
					lines = append(lines, l)
				}
			}
			if len(lines) == 0 {
				return key + " =\n", nil
			}
			var b strings.Builder
			b.WriteString(key + " = " + lines[0] + "\n")
			for _, l := range lines[1:] {
				b.WriteString("    " + l + "\n")
			}
			return b.String(), nil
		}
		return key + " = " + x + "\n", nil
	case []string:
		var b strings.Builder
		b.WriteString(key + " =\n")
		for _, item := range x {
			if strings.ContainsAny(item, "\r\n") {
				return "", fmt.Errorf("%s: an item with a line break cannot be written to an ini file", key)
			}
			if pytestArgsKeys[key] && !shellSafe.MatchString(item) {
				item = shq(item)
			}
			b.WriteString("    " + item + "\n")
		}
		return b.String(), nil
	}
	return "", fmt.Errorf("%s: unsupported value", key)
}

// tomlScanner reads the small part of TOML a pytest table uses.
type tomlScanner struct {
	s string
	i int
}

func (t *tomlScanner) eof() bool  { return t.i >= len(t.s) }
func (t *tomlScanner) peek() byte { return t.s[t.i] }

// consume advances past c when it is next.
func (t *tomlScanner) consume(c byte) bool {
	if !t.eof() && t.peek() == c {
		t.i++
		return true
	}
	return false
}

// skipSpace skips blanks and tabs on the current line.
func (t *tomlScanner) skipSpace() {
	for !t.eof() && (t.peek() == ' ' || t.peek() == '\t') {
		t.i++
	}
}

// skipComment skips to the end of the line.
func (t *tomlScanner) skipComment() {
	for !t.eof() && t.peek() != '\n' {
		t.i++
	}
}

// skipBlank skips white space, line breaks and comments.
func (t *tomlScanner) skipBlank() {
	for !t.eof() {
		switch t.peek() {
		case ' ', '\t', '\n', '\r':
			t.i++
		case '#':
			t.skipComment()
		default:
			return
		}
	}
}

// bareKey matches a TOML bare key.
var bareKey = regexp.MustCompile(`^[A-Za-z0-9_-]+`)

// tomlScalar matches a TOML boolean or number.
var tomlScalar = regexp.MustCompile(`^(true|false|[-+]?[0-9][0-9_.eE+-]*)`)

// key reads a bare or quoted key.
func (t *tomlScanner) key() (string, error) {
	if !t.eof() && (t.peek() == '"' || t.peek() == '\'') {
		return t.str()
	}
	k := bareKey.FindString(t.s[t.i:])
	if k == "" {
		return "", fmt.Errorf("[tool.pytest.ini_options]: cannot read a key at %q", snippet(t.s[t.i:]))
	}
	t.i += len(k)
	return k, nil
}

// value reads a string, an array of strings, a boolean or a number; booleans
// and numbers come back as their text.
func (t *tomlScanner) value() (any, error) {
	if t.eof() {
		return nil, errors.New("missing value")
	}
	switch c := t.peek(); {
	case c == '"' || c == '\'':
		return t.str()
	case c == '[':
		t.i++
		var items []string
		for {
			t.skipBlank()
			if t.consume(']') {
				return items, nil
			}
			if t.eof() || (t.peek() != '"' && t.peek() != '\'') {
				return nil, errors.New("only arrays of strings are supported")
			}
			s, err := t.str()
			if err != nil {
				return nil, err
			}
			items = append(items, s)
			t.skipBlank()
			if t.consume(']') {
				return items, nil
			}
			if !t.consume(',') {
				return nil, errors.New("expected , or ] in an array")
			}
		}
	default:
		tok := tomlScalar.FindString(t.s[t.i:])
		if tok == "" {
			return nil, fmt.Errorf("unsupported value %q", snippet(t.s[t.i:]))
		}
		t.i += len(tok)
		return tok, nil
	}
}

// str reads a basic ("...") or literal ('...') string, single-line or
// multi-line.
func (t *tomlScanner) str() (string, error) {
	q := t.peek()
	if strings.HasPrefix(t.s[t.i:], strings.Repeat(string(q), 3)) {
		return t.multiline(q)
	}
	j := t.i + 1
	for j < len(t.s) && t.s[j] != q && t.s[j] != '\n' {
		if q == '"' && t.s[j] == '\\' {
			j++
		}
		j++
	}
	if j >= len(t.s) || t.s[j] != q {
		return "", errors.New("unterminated string")
	}
	raw := t.s[t.i : j+1]
	t.i = j + 1
	if q == '\'' {
		return raw[1 : len(raw)-1], nil
	}
	s, err := strconv.Unquote(raw)
	if err != nil {
		return "", fmt.Errorf("cannot read the string %s: %w", raw, err)
	}
	return s, nil
}

// multiline reads a multi-line basic ("""...""") or literal (”'...”') string:
// a line break right after the opening delimiter is not part of it, up to two
// quotes may stand right before the closing delimiter, and in a basic string
// escapes are read and a backslash at the end of a line removes the line break
// and the white space after it.
func (t *tomlScanner) multiline(q byte) (string, error) {
	j := t.i + 3
	for {
		if j >= len(t.s) {
			return "", errors.New("unterminated multi-line string")
		}
		if q == '"' && t.s[j] == '\\' {
			j += 2
			continue
		}
		if t.s[j] == q && strings.HasPrefix(t.s[j:], strings.Repeat(string(q), 3)) {
			n := 3
			for j+n < len(t.s) && t.s[j+n] == q {
				n++
			}
			if n > 5 {
				return "", errors.New("too many quotes closing a multi-line string")
			}
			body := t.s[t.i+3 : j+n-3]
			t.i = j + n
			body = strings.TrimPrefix(strings.TrimPrefix(body, "\r"), "\n")
			if q == '\'' {
				return body, nil
			}
			return unescapeTOML(body)
		}
		j++
	}
}

// unescapeTOML reads the escapes of a multi-line basic string's body, and its
// line-ending backslashes.
func unescapeTOML(body string) (string, error) {
	var b strings.Builder
	for i := 0; i < len(body); i++ {
		c := body[i]
		if c != '\\' {
			b.WriteByte(c)
			continue
		}
		if i+1 >= len(body) {
			return "", errors.New("a string ends in a backslash")
		}
		// A backslash followed by white space up to a line break joins the lines.
		k := i + 1
		for k < len(body) && (body[k] == ' ' || body[k] == '\t') {
			k++
		}
		if k < len(body) && (body[k] == '\n' || body[k] == '\r') {
			for k < len(body) && strings.IndexByte(" \t\r\n", body[k]) >= 0 {
				k++
			}
			i = k - 1
			continue
		}
		i++
		switch e := body[i]; e {
		case 'b':
			b.WriteByte('\b')
		case 't':
			b.WriteByte('\t')
		case 'n':
			b.WriteByte('\n')
		case 'f':
			b.WriteByte('\f')
		case 'r':
			b.WriteByte('\r')
		case 'e':
			b.WriteByte(0x1b)
		case '"', '\\':
			b.WriteByte(e)
		case 'x', 'u', 'U':
			n := map[byte]int{'x': 2, 'u': 4, 'U': 8}[e]
			if i+1+n > len(body) {
				return "", fmt.Errorf("a short \\%c escape", e)
			}
			r, err := strconv.ParseUint(body[i+1:i+1+n], 16, 32)
			if err != nil {
				return "", fmt.Errorf("a bad \\%c escape: %w", e, err)
			}
			// TOML escapes name Unicode scalar values: no surrogate, nothing past U+10FFFF.
			if r > utf8.MaxRune || !utf8.ValidRune(rune(r)) {
				return "", fmt.Errorf("a bad \\%c escape: %s is not a Unicode scalar value", e, body[i+1:i+1+n])
			}
			b.WriteRune(rune(r))
			i += n
		default:
			return "", fmt.Errorf("an unknown escape \\%c", e)
		}
	}
	return b.String(), nil
}

// snippet returns s up to its first line break, at most 40 bytes, for an error
// message.
func snippet(s string) string {
	s = firstLine(s)
	if len(s) > 40 {
		s = s[:40]
	}
	return s
}
