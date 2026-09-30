package fs

import (
	"io"
	"os"
	"strings"
)

// gitignore semantics (gitignore(5)) as far as tools need them:
//
//   - blank lines and #comments are skipped; "\#" and "\!" escape the first char
//   - unescaped trailing spaces are dropped
//   - "!" re-includes; the last matching rule wins, and rules from deeper
//     .gitignore files beat rules from shallower ones
//   - a trailing "/" restricts a rule to directories
//   - a slash at the start or middle anchors the rule to its .gitignore's
//     directory; otherwise it matches at any depth
//   - "**" matches any number of directories; "dir/**" matches what is inside
//     dir but not dir itself
//   - a directory that is ignored is never entered, so nothing inside it can be
//     re-included (git behaves the same)

const (
	maxIgnoreFileBytes = 1 << 20
	maxIgnoreRules     = 20000
)

type rule struct {
	segs    []string
	negate  bool
	dirOnly bool
}

func trimTrailingSpaces(s string) string {
	end := len(s)
	for end > 0 && s[end-1] == ' ' {
		bs := 0
		for j := end - 2; j >= 0 && s[j] == '\\'; j-- {
			bs++
		}
		if bs%2 == 1 {
			break
		}
		end--
	}
	return s[:end]
}

// parseIgnoreLine compiles one gitignore line. ok is false for lines that
// contain no rule.
func parseIgnoreLine(line string) (rule, bool) {
	line = trimTrailingSpaces(strings.TrimSuffix(line, "\r"))
	if line == "" || line[0] == '#' {
		return rule{}, false
	}
	var r rule
	if line[0] == '!' {
		r.negate = true
		line = line[1:]
	}
	if strings.HasSuffix(line, "/") {
		r.dirOnly = true
		line = strings.TrimRight(line, "/")
	}
	if line == "" {
		return rule{}, false
	}
	anchored := strings.Contains(line, "/")
	line = strings.TrimLeft(line, "/")
	if line == "" {
		return rule{}, false
	}
	var segs []string
	if anchored {
		for _, s := range strings.Split(line, "/") {
			if s == "" || s == "." {
				continue
			}
			if s == "**" && len(segs) > 0 && segs[len(segs)-1] == "**" {
				continue
			}
			segs = append(segs, s)
		}
		if len(segs) == 0 || len(segs) > maxPatternSegments {
			return rule{}, false
		}
		// "dir/**" means everything inside dir, not dir itself. Since an ignored
		// directory is pruned, "every direct child" is equivalent and does not
		// need a special case in the matcher.
		if n := len(segs); n > 1 && segs[n-1] == "**" {
			segs[n-1] = "*"
		}
	} else {
		segs = []string{"**", line}
		if line == "**" {
			segs = []string{"**"}
		}
	}
	r.segs = segs
	return r, true
}

// ignoreFile is one parsed .gitignore.
type ignoreFile struct {
	// off is how many path segments separate the walk origin from the directory
	// holding this file; rules are matched against the remaining segments.
	off   int
	rules []rule
}

func parseIgnoreFile(data []byte, off int) *ignoreFile {
	f := &ignoreFile{off: off}
	for _, line := range strings.Split(string(data), "\n") {
		if r, ok := parseIgnoreLine(line); ok {
			f.rules = append(f.rules, r)
			if len(f.rules) >= maxIgnoreRules {
				break
			}
		}
	}
	if len(f.rules) == 0 {
		return nil
	}
	return f
}

// loadIgnoreFile reads path if it is a readable regular file (or a symlink to
// one) of sane size.
func loadIgnoreFile(path string, off int) *ignoreFile {
	f, err := os.Open(path)
	if err != nil {
		return nil
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil || !fi.Mode().IsRegular() {
		return nil
	}
	data, err := io.ReadAll(io.LimitReader(f, maxIgnoreFileBytes))
	if err != nil {
		return nil
	}
	return parseIgnoreFile(data, off)
}

// decide reports the verdict of the file's rules for rel (segments relative to
// the file's directory). decided is false when no rule matched.
func (f *ignoreFile) decide(rel []string, isDir bool) (ignored, decided bool) {
	for i := len(f.rules) - 1; i >= 0; i-- {
		r := &f.rules[i]
		if r.dirOnly && !isDir {
			continue
		}
		if matchSegs(r.segs, rel) {
			return !r.negate, true
		}
	}
	return false, false
}

// ignoreStack is the chain of .gitignore files in effect: shallow first.
type ignoreStack struct{ files []*ignoreFile }

func (s *ignoreStack) push(f *ignoreFile) { s.files = append(s.files, f) }
func (s *ignoreStack) pop()               { s.files = s.files[:len(s.files)-1] }

// ignored evaluates a path given as segments relative to the walk origin.
func (s *ignoreStack) ignored(segs []string, isDir bool) bool {
	for i := len(s.files) - 1; i >= 0; i-- {
		f := s.files[i]
		if len(segs) <= f.off {
			continue
		}
		if ig, ok := f.decide(segs[f.off:], isDir); ok {
			return ig
		}
	}
	return false
}

// overrideGlobs is grep's glob filter, with ripgrep's --glob semantics so the
// ripgrep and pure-Go paths select the same files: patterns use gitignore
// syntax relative to the search root; a pattern without "!" whitelists (once
// any exists, files matching none are skipped) and one with "!" excludes.
type overrideGlobs struct {
	rules       []rule // negate == exclude here
	hasPositive bool
}

// splitGlobList splits a user glob string into individual patterns on commas
// and whitespace that are outside braces.
func splitGlobList(s string) []string {
	var out []string
	depth := 0
	start := 0
	flush := func(end int) {
		if p := strings.TrimSpace(s[start:end]); p != "" {
			out = append(out, p)
		}
		start = end + 1
	}
	for i := 0; i < len(s); i++ {
		switch s[i] {
		case '\\':
			i++
		case '{':
			depth++
		case '}':
			if depth > 0 {
				depth--
			}
		case ',', ' ', '\t', '\n':
			if depth == 0 {
				flush(i)
			}
		}
	}
	flush(len(s))
	return out
}

func compileOverride(patterns []string) *overrideGlobs {
	o := &overrideGlobs{}
	for _, p := range patterns {
		if len(p) > maxPatternBytes {
			continue
		}
		neg := false
		if strings.HasPrefix(p, "!") {
			neg = true
			p = p[1:]
		}
		alts, ok := expandBraces(p, maxBraceAlternatives)
		if !ok {
			continue
		}
		for _, a := range alts {
			r, ok := parseIgnoreLine(a)
			if !ok {
				continue
			}
			r.negate = neg
			o.rules = append(o.rules, r)
			if !neg {
				o.hasPositive = true
			}
		}
	}
	return o
}

// excludes reports whether the entry must be skipped. Directories are only ever
// skipped by an explicit "!" match: a whitelist glob like *.go says nothing
// about which directories to enter.
func (o *overrideGlobs) excludes(segs []string, isDir bool) bool {
	if o == nil {
		return false
	}
	for i := len(o.rules) - 1; i >= 0; i-- {
		r := &o.rules[i]
		if r.dirOnly && !isDir {
			continue
		}
		if matchSegs(r.segs, segs) {
			return r.negate
		}
	}
	return o.hasPositive && !isDir
}
