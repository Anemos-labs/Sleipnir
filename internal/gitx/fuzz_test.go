package gitx

import (
	"bytes"
	"strings"
	"testing"
	"unicode/utf8"
)

// The parsers read what git prints about a repository somebody else prepared:
// names, messages and paths are arbitrary bytes. None of them may panic, loop, or
// read past their input, whatever they are given.

func FuzzParseStatus(f *testing.F) {
	f.Add([]byte("# branch.oid abc\x00# branch.head main\x00# branch.upstream origin/main\x00# branch.ab +1 -2\x001 .M N... 100644 100644 100644 a b path with spaces\x002 R. N... 100644 100644 100644 a b R100 new\x00old\x00u UU N... 1 2 3 4 a b c f.txt\x00? untracked\x00"))
	f.Add([]byte("2 R. N... 100644 100644 100644 a b R100 only-new-no-old"))
	f.Add([]byte("\x00\x00\x001 \x00u \x00# \x00? \x00"))
	f.Fuzz(func(t *testing.T, data []byte) {
		st := parseStatus(data)
		_ = st.Clean()
		for _, c := range st.Staged {
			if c.Code == 0 {
				t.Fatalf("zero status code in %+v", c)
			}
		}
	})
}

func FuzzParseNameStatusAndNumstat(f *testing.F) {
	f.Add([]byte("M\x00a.txt\x00R100\x00old\x00new\x00D\x00gone\x00"), []byte("1\t2\ta.txt\x00-\t-\tbin\x000\t0\t\x00old\x00new\x00"))
	f.Add([]byte("R\x00only"), []byte("\t\t"))
	f.Fuzz(func(t *testing.T, ns, num []byte) {
		files, _ := parseNameStatus(ns, 50)
		if len(files) > 50 {
			t.Fatalf("%d files past the cap", len(files))
		}
		applyNumstat(files, num)
		if s := renderStat(files); len(files) > 0 && !strings.HasSuffix(s, "\n") {
			t.Fatalf("stat does not end in a newline: %q", s)
		}
	})
}

func FuzzParseCommit(f *testing.F) {
	f.Add([]byte("tree abc\nparent def\nauthor A <a@x> 1700000000 +0000\ncommitter C <c@x> 1700000001 +0100\ngpgsig -----BEGIN\n line\n\nsubject\n\nbody\n"))
	f.Add([]byte("author <<<> > 99999999999999999999 x\n\n"))
	f.Add([]byte(""))
	f.Fuzz(func(t *testing.T, raw []byte) {
		c := parseCommit("sha", raw)
		if strings.Contains(c.Subject, "\n") {
			t.Fatalf("subject spans lines: %q", c.Subject)
		}
	})
}

func FuzzCutAtFileBoundary(f *testing.F) {
	f.Add([]byte("diff --git a/a b/a\n+x\ndiff --git a/b b/b\n+y\n"), 30)
	f.Add([]byte("diff --git a/a b/a\n+diff --git in content\n"), 25)
	f.Fuzz(func(t *testing.T, patch []byte, max int) {
		if max < 1 || max >= len(patch) {
			return
		}
		files := make([]FileChange, 8)
		for i := range files {
			files[i].Path = string(rune('a' + i))
		}
		kept, omitted := cutAtFileBoundary(patch, max, files)
		if len(kept) > max || !bytes.HasPrefix(patch, kept) {
			t.Fatalf("kept %d bytes of a %d-byte cap, prefix=%v", len(kept), max, bytes.HasPrefix(patch, kept))
		}
		if len(omitted) > len(files) {
			t.Fatalf("omitted %d of %d files", len(omitted), len(files))
		}
		if len(kept) > 0 && !bytes.HasSuffix(kept, []byte("\n")) {
			t.Fatalf("kept part does not end at a line boundary: %q", kept)
		}
	})
}

func FuzzClassifyAndValidate(f *testing.F) {
	f.Add("fatal: not a git repository")
	f.Add("CONFLICT (content): Merge conflict in a\x00b")
	f.Fuzz(func(t *testing.T, s string) {
		_ = classify(s)
		_ = firstLines(s, 3)
		if err := ValidateBranchName(s); err == nil {
			// anything accepted is printable ASCII without the characters git forbids
			for _, c := range []byte(s) {
				if c < 0x21 || c > 0x7e {
					t.Fatalf("accepted %q", s)
				}
			}
			if strings.HasPrefix(s, "-") || strings.Contains(s, "..") {
				t.Fatalf("accepted %q", s)
			}
		}
		if p, err := cleanRelPath("x", s); err == nil {
			if strings.HasPrefix(p, "/") || strings.HasPrefix(p, "../") || p == ".." || p == "." || !utf8.ValidString(p) && utf8.ValidString(s) {
				t.Fatalf("cleanRelPath(%q) = %q", s, p)
			}
		}
		_ = validateRev("x", s)
	})
}
