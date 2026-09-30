package core_test

import (
	"bytes"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"unicode"
	"unicode/utf8"
)

// update rewrites the golden files under testdata/golden instead of comparing with
// them:
//
//	go test ./internal/core -run TestCanonicalGolden -update
//
// A golden here is the exact byte encoding that feeds cache keys and wire hashes.
// Read docs/BUILDING.md ("Changing prompt bytes") before accepting a new one: the
// diff is the change you are about to price.
var update = flag.Bool("update", false, "rewrite golden files under testdata/golden")

// golden compares got with testdata/golden/<name> (or rewrites it with -update).
func golden(t *testing.T, name string, got []byte) {
	t.Helper()
	path := filepath.Join("testdata", "golden", name)
	if *update {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, got, 0o644); err != nil {
			t.Fatal(err)
		}
		return
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("missing golden file %s (create it with: go test ./internal/core -run TestCanonicalGolden -update): %v", path, err)
	}
	if !bytes.Equal(want, got) {
		t.Fatalf("the canonical bytes changed: output differs from %s\n%s\n"+
			"If this change is intended, follow docs/BUILDING.md (\"Changing prompt bytes\"), then run: go test ./internal/core -run TestCanonicalGolden -update",
			path, lineDiff(want, got))
	}
}

// lineDiff names the first lines that differ, by case name (the first tab-separated
// field of a golden line), with the byte offset of the first difference in each.
func lineDiff(want, got []byte) string {
	wl, gl := strings.Split(string(want), "\n"), strings.Split(string(got), "\n")
	var sb strings.Builder
	shown, differing := 0, 0
	for i := 0; i < len(wl) || i < len(gl); i++ {
		var w, g string
		if i < len(wl) {
			w = wl[i]
		}
		if i < len(gl) {
			g = gl[i]
		}
		if w == g {
			continue
		}
		differing++
		if shown == 3 {
			continue
		}
		shown++
		name := w
		if name == "" {
			name = g
		}
		if tab := strings.IndexByte(name, '\t'); tab >= 0 {
			name = name[:tab]
		}
		off := 0
		for off < len(w) && off < len(g) && w[off] == g[off] {
			off++
		}
		fmt.Fprintf(&sb, "  line %d, case %q, first byte that differs: %d\n    want: %s\n    got:  %s\n", i+1, name, off, around(w, off), around(g, off))
	}
	if differing == 0 { // the same lines, so the difference is in the trailing newlines
		return fmt.Sprintf("  the lines are the same but the ends differ: %d bytes want, %d got (a trailing newline?)", len(want), len(got))
	}
	fmt.Fprintf(&sb, "  (%d line(s) differ, %d shown)", differing, shown)
	return sb.String()
}

// around returns the part of s near byte off (the first byte that differs), cut on
// character boundaries, with ... where it was cut, so that a change late in a long line
// is still in view.
func around(s string, off int) string {
	const before, after = 50, 110
	lo, hi := max(0, off-before), min(len(s), off+after)
	for lo > 0 && !utf8.RuneStart(s[lo]) {
		lo--
	}
	for hi < len(s) && !utf8.RuneStart(s[hi]) {
		hi++
	}
	out := s[lo:hi]
	if lo > 0 {
		out = "..." + out
	}
	if hi < len(s) {
		out += "..."
	}
	return out
}

// show renders bytes for a golden line: as they are when every rune is graphic, so
// the file reads like the JSON it holds, and as a Go-quoted ASCII string ("q:...")
// otherwise (DEL, NEL, U+2028 and other characters a reviewer cannot see). Either
// way the text is exact; the sha256 next to it is computed from the bytes.
func show(b []byte) string {
	if utf8.Valid(b) {
		graphic := true
		for _, r := range string(b) {
			if !unicode.IsGraphic(r) {
				graphic = false
				break
			}
		}
		if graphic {
			return string(b)
		}
	}
	return "q:" + strconv.QuoteToASCII(string(b))
}
