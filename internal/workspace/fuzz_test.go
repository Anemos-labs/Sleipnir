package workspace

import (
	"strings"
	"testing"
	"unicode/utf8"
)

// escapeGlob turns a literal path into a pattern that matches exactly it (and what
// lies beneath it).
func escapeGlob(lit string) string {
	var sb strings.Builder
	for i := 0; i < len(lit); i++ {
		switch lit[i] {
		case '\\', '*', '?', '[', ']', '{', '}', ',':
			sb.WriteByte('\\')
		}
		sb.WriteByte(lit[i])
	}
	return sb.String()
}

// The scope helpers are the code that decides which agents may run side by side
// and whether a tree stayed inside its lease, so they must hold their invariants
// for whatever an agent or a configuration file puts in a pattern.
func FuzzScope(f *testing.F) {
	seeds := [][2]string{
		{"internal/**", "internal/swarm/leases.go"},
		{"**/*.go", "cmd/app/main.go"},
		{"{a,b}/x", "a/x/y"},
		{"[!a-c]*", "d.txt"},
		{`a\*b`, "a*b"},
		{"docs/", "docs/guide.md"},
		{"", "x"},
		{"a/../b", "b"},
		{"*", "a/b"},
		{"[[:digit:]]?", "1x"},
		{"{a,{b,c}}/**/z", "c/d/e/z"},
		{"é*", "éa"},
	}
	for _, s := range seeds {
		f.Add(s[0], s[1])
	}
	f.Fuzz(func(t *testing.T, pattern, file string) {
		matched := Match(pattern, file)
		if Covered([]string{pattern}, file) != matched {
			t.Fatalf("Covered and Match disagree on %q / %q", pattern, file)
		}
		out := OutOfScope([]string{file}, []string{pattern})
		if matched == (len(out) > 0) {
			t.Fatalf("OutOfScope(%q, %q) = %v but Match = %v", file, pattern, out, matched)
		}
		// Overlap is symmetric in whether it finds one.
		ab := len(Overlap([]string{pattern}, []string{file})) > 0
		ba := len(Overlap([]string{file}, []string{pattern})) > 0
		if ab != ba {
			t.Fatalf("Overlap is not symmetric for %q and %q", pattern, file)
		}
		segs, ok := splitPath(file)
		if !ok {
			return
		}
		lit := strings.Join(segs, "/")
		esc := escapeGlob(lit)
		if !Match(esc, lit) {
			t.Fatalf("the escaped literal %q does not match itself (%q)", esc, lit)
		}
		// A pattern that covers a file overlaps that file's own name.
		if matched && len(Overlap([]string{pattern}, []string{esc})) == 0 {
			t.Fatalf("%q covers %q but is said not to overlap it (%q)", pattern, lit, esc)
		}
	})
}

// parseHunks reads text an agent's merge left in a file, so it must survive any
// bytes, keep its caps and never invent a region.
func FuzzParseHunks(f *testing.F) {
	f.Add("a\n<<<<<<< ours\nx\n||||||| base\ny\n=======\nz\n>>>>>>> theirs\nb\n")
	f.Add("<<<<<<< ours\n=======\n>>>>>>> theirs")
	f.Add("<<<<<<< a\n<<<<<<< b\n=======\n>>>>>>> c\n>>>>>>> d\n")
	f.Add("=======\n>>>>>>> x\n||||||| y\n")
	f.Add("<<<<<<< ours\r\nx\r\n=======\r\ny\r\n>>>>>>> theirs\r\n")
	f.Add("<<<<<<< ours\n" + strings.Repeat("é", 3000) + "\n=======\n\xff\xfe\n>>>>>>> theirs\n")
	f.Fuzz(func(t *testing.T, text string) {
		hunks := parseHunks("f.txt", []byte(text))
		lines := strings.Count(text, "\n") + 1
		if len(hunks)*3 > lines {
			t.Fatalf("%d hunks from %d lines: a region needs three marker lines", len(hunks), lines)
		}
		valid := utf8.ValidString(text)
		for _, h := range hunks {
			if h.File != "f.txt" || h.Line < 1 || h.Line > lines {
				t.Fatalf("bad hunk position: %+v (%d lines)", h, lines)
			}
			for _, s := range []string{h.Ours, h.Base, h.Theirs} {
				if len(s) > maxSnippetBytes {
					t.Fatalf("snippet of %d bytes exceeds the cap", len(s))
				}
				if valid && !utf8.ValidString(s) {
					t.Fatalf("a snippet of valid text was cut in the middle of a character: %q", s)
				}
			}
		}
	})
}
