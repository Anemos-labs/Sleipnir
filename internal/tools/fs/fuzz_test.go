package fs

import (
	"strings"
	"testing"
	"unicode/utf8"
)

// The fuzz targets run their seed corpus as ordinary tests; run them for real
// with, e.g., go test -fuzz=FuzzParsePatch -fuzztime=30s ./internal/tools/fs.

func FuzzParsePatch(f *testing.F) {
	seeds := []string{
		"*** Begin Patch\n*** Add File: a\n+x\n*** End Patch",
		"*** Begin Patch\n*** Update File: a\n@@ ctx\n c\n-o\n+n\n*** End of File\n*** End Patch",
		"*** Begin Patch\n*** Update File: a\n*** Move to: b\n@@\n-x\n+y\n*** End Patch",
		"apply_patch <<'EOF'\n*** Begin Patch\n*** Delete File: a\n*** End Patch\nEOF",
		"*** Begin Patch\n*** Update File: a\n@@ -1,2 +1,2 @@ f\n-a\n+b\n\\ No newline at end of file\n*** End Patch",
		"*** Begin Patch\n*** End Patch",
		"",
		"\x00",
	}
	for _, s := range seeds {
		f.Add(s, "one\ntwo\nthree\n")
	}
	f.Fuzz(func(t *testing.T, patch, file string) {
		ops, msg := parsePatch(patch)
		if msg != "" {
			return
		}
		for _, op := range ops {
			if op.kind != opUpdate {
				continue
			}
			lines := splitDoc(file)
			out, _, _ := applyHunks("f", lines, op.hunks, dominantEOL(file))
			_ = joinDoc(out)
		}
	})
}

func FuzzSegMatch(f *testing.F) {
	for _, s := range [][2]string{{"*.go", "a.go"}, {"[a-c]?", "bx"}, {"\\*", "*"}, {"[", "["}, {"[[:alpha:]", "a"}, {"a*b*c", "aXbYc"}, {"", ""}, {"[!]", "]"}, {"*é", "café"}} {
		f.Add(s[0], s[1])
	}
	f.Fuzz(func(t *testing.T, pat, name string) {
		got := segMatch(pat, name)
		// A pattern made only of a literal (no metacharacters) matches exactly itself.
		if !strings.ContainsAny(pat, "*?[\\") && utf8.ValidString(pat) {
			if want := pat == name; got != want {
				t.Fatalf("segMatch(%q, %q) = %v, want %v", pat, name, got, want)
			}
		}
		// "*" matches everything.
		if !segMatch("*", name) {
			t.Fatalf(`"*" must match %q`, name)
		}
	})
}

func FuzzGlobCompileAndMatch(f *testing.F) {
	for _, s := range []string{"**/*.go", "src/{a,b}/**", "[a-z]*", "a/**/b/**/c", "{a,{b,c}}", "\\{x", "**", "a//b", "./x/"} {
		f.Add(s, "src/a/b/c.go")
	}
	f.Fuzz(func(t *testing.T, pattern, path string) {
		g, err := compileGlob(pattern)
		if err != nil {
			return
		}
		segs := splitPath(path)
		_ = g.match(segs)
		_ = g.couldContain(segs)
	})
}

func FuzzIgnoreFile(f *testing.F) {
	for _, s := range []string{"*.log\n!keep.log\nbuild/\n/x\na/**/b\n", "\\#a\n\\!b\nc\\ \n", "**\n", "a/**\n", "[\n", "!\n/\n"} {
		f.Add(s, "a/b/keep.log")
	}
	f.Fuzz(func(t *testing.T, content, path string) {
		file := parseIgnoreFile([]byte(content), 0)
		if file == nil {
			return
		}
		segs := splitPath(path)
		file.decide(segs, false)
		file.decide(segs, true)
		var st ignoreStack
		st.push(file)
		st.ignored(segs, false)
	})
}

func FuzzApplyEdit(f *testing.F) {
	f.Add("a\nb\nc\n", "b", "B", false)
	f.Add("a\r\nb\r\n", "a\nb", "x\ny", false)
	f.Add("x x x", "x", "y", true)
	f.Add("", "", "seed", false)
	f.Add("\xef\xbb\xbfa", "a", "b", false)
	f.Fuzz(func(t *testing.T, cur, old, repl string, all bool) {
		got, n, msg := applyOne(cur, editSpec{old: old, new: repl, all: all}, "f")
		if msg != "" {
			return
		}
		if n < 1 {
			t.Fatalf("success with %d replacements", n)
		}
		// Without any CR involved, the edit is exactly strings.Replace.
		if !strings.Contains(cur, "\r") && !strings.Contains(old, "\r") && !strings.Contains(repl, "\r") && old != "" {
			want := strings.Replace(cur, old, repl, 1)
			if all {
				want = strings.ReplaceAll(cur, old, repl)
			}
			if got != want {
				t.Fatalf("applyOne(%q, %q, %q, all=%v) = %q, want %q", cur, old, repl, all, got, want)
			}
		}
	})
}

func FuzzDiffReplay(f *testing.F) {
	f.Add("a\nb\nc\n", "a\nx\nc\n")
	f.Add("", "x\n")
	f.Add("x\ny", "y")
	f.Add("1\n2\n3\n4\n5\n6\n7\n8\n9\n", "1\n2\nX\n4\n5\n6\n7\n8\nY\n")
	f.Fuzz(func(t *testing.T, a, b string) {
		if strings.ContainsAny(a+b, "\r") || !utf8.ValidString(a) || !utf8.ValidString(b) {
			return
		}
		al := splitForDiff(a)
		bl := splitForDiff(b)
		for _, l := range append(append([]string(nil), al...), bl...) {
			if len(l) > maxDiffLineCh/2 {
				return // rendered diff lines are clipped, so they cannot be replayed exactly
			}
		}
		d := diffFiles(a, b, 2, 0)
		// Removing what the diff removes and adding what it adds must turn a into b.
		got := replayHunks(t, al, d.lines)
		if strings.Join(got, "\n") != strings.Join(bl, "\n") {
			t.Fatalf("replay mismatch for %q -> %q: got %q", a, b, got)
		}
	})
}

func FuzzExpandBraces(f *testing.F) {
	for _, s := range []string{"{a,b}", "{a,{b,c}}x", "[{]", "\\{a,b\\}", "{", "}", "{,}", "a{b,c}d{e,f}"} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, pat string) {
		out, ok := expandBraces(pat, 64)
		if ok && len(out) > 64 {
			t.Fatalf("limit exceeded: %d", len(out))
		}
	})
}

func FuzzSplitGlobListAndOverride(f *testing.F) {
	for _, s := range []string{"*.go", "*.{go,md} !vendor", "a,b c", "{", "}}", "\\", "!"} {
		f.Add(s, "a/b.go")
	}
	f.Fuzz(func(t *testing.T, globs, path string) {
		o := compileOverride(splitGlobList(globs))
		o.excludes(splitPath(path), false)
		o.excludes(splitPath(path), true)
	})
}

func FuzzDisplayLine(f *testing.F) {
	f.Add("hello")
	f.Add("caf\xe9")
	f.Add(strings.Repeat("é", 3000))
	f.Fuzz(func(t *testing.T, s string) {
		out := displayLine([]byte(s))
		if !utf8.ValidString(out) {
			t.Fatalf("displayLine produced invalid UTF-8 for %q", s)
		}
		if g := grepText(s); !utf8.ValidString(g) {
			t.Fatalf("grepText produced invalid UTF-8 for %q", s)
		}
		if q := safeName(s); strings.ContainsAny(q, "\n\r\x00") {
			t.Fatalf("safeName left control characters in %q", q)
		}
	})
}
