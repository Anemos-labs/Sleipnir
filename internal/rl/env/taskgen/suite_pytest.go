package taskgen

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
)

// PytestINIFromDir returns the pytest configuration of the project in dir (a
// checkout of the reference) as the text of a pytest.ini, for
// SuiteSpec.PytestINI. It looks where pytest does, first match wins: pytest.ini
// (even without a [pytest] section), the [tool.pytest.ini_options] table of
// pyproject.toml, the [pytest] section of tox.ini and the [tool:pytest] section
// of setup.cfg. A project with none gets "[pytest]\n". Of TOML, the table's
// strings, string arrays, booleans and numbers are understood; anything else (a
// multi-line string, an inline table) is an error, and the caller writes the
// configuration itself.
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

// iniValue writes one key of the converted table: a scalar on its line, an
// array as one item per indented line (quoted for options pytest splits like a
// shell command line).
func iniValue(key string, v any) (string, error) {
	switch x := v.(type) {
	case string:
		if strings.ContainsAny(x, "\r\n") {
			return "", fmt.Errorf("%s: a value with a line break cannot be written to an ini file", key)
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

// str reads a basic ("...") or literal ('...') single-line string.
func (t *tomlScanner) str() (string, error) {
	q := t.peek()
	if strings.HasPrefix(t.s[t.i:], strings.Repeat(string(q), 3)) {
		return "", errors.New("multi-line strings are not supported")
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

// snippet returns s up to its first line break, at most 40 bytes, for an error
// message.
func snippet(s string) string {
	s = firstLine(s)
	if len(s) > 40 {
		s = s[:40]
	}
	return s
}
