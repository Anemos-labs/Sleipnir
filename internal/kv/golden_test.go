package kv_test

import (
	"bytes"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf8"
)

// The golden tests of this package pin the bytes that become the prompt cache key:
//
//	TestGoldenRenderedStack  what kv.Render makes of a fixed session, for every hot mode on
//	                         an Anthropic-style and an OpenAI-style route, and the per-layer
//	                         hashes and routing keys          testdata/golden/render/
//	TestGoldenToolSpecs      the tool list every agent sends        testdata/golden/tools/
//	TestRendererVersion      kv.RendererVersion, and the rule that bumping it is deliberate
//
// (The constitution has its own, next to its text: internal/agent, TestGoldenConstitution,
// and the canonical encoding of blocks and messages is pinned in internal/core,
// TestCanonicalGolden.)
//
// Each byte is pinned by exactly one of them, so a change fails one clearly named test. An
// intended change means: read docs/BUILDING.md ("Changing prompt bytes"), run
//
//	go test ./internal/kv -run Golden -update
//
// read the diff of testdata/golden, and do what that page says about RendererVersion, the
// CHANGELOG and the price from `sleipnir sim`.
var update = flag.Bool("update", false, "rewrite golden files under testdata/golden")

// golden compares got with testdata/golden/<name> (or rewrites it with -update).
func golden(t *testing.T, name string, got []byte) {
	t.Helper()
	path := filepath.Join("testdata", "golden", filepath.FromSlash(name))
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
		t.Fatalf("missing golden file %s (create it with: go test ./internal/kv -run Golden -update): %v", path, err)
	}
	if !bytes.Equal(want, got) {
		t.Fatalf("the prompt bytes changed: output differs from %s\n%s\n"+
			"If this change is intended, follow docs/BUILDING.md (\"Changing prompt bytes\"), then run: go test ./internal/kv -run Golden -update",
			path, lineDiff(want, got))
	}
}

// lineDiff names what changed: the header that governs the first line that differs (the
// section, message, block or layer of a transcript; the tool of tools.json), then that line
// as it was and as it is, cut around the first byte that differs so that a change late in a
// long line is still in view, and how many lines differ in all.
func lineDiff(want, got []byte) string {
	wl, gl := strings.Split(string(want), "\n"), strings.Split(string(got), "\n")
	first, differing := -1, 0
	for i := 0; i < len(wl) || i < len(gl); i++ {
		var w, g string
		if i < len(wl) {
			w = wl[i]
		}
		if i < len(gl) {
			g = gl[i]
		}
		if w != g {
			if first < 0 {
				first = i
			}
			differing++
		}
	}
	if first < 0 { // the same lines, so the difference is in the trailing newlines
		return fmt.Sprintf("  the lines are the same but the ends differ: %d bytes want, %d got (a trailing newline?)", len(want), len(got))
	}
	line := func(ls []string) string {
		if first < len(ls) {
			return ls[first]
		}
		return "(end of file)"
	}
	w, g := line(wl), line(gl)
	off := 0
	for off < len(w) && off < len(g) && w[off] == g[off] {
		off++
	}
	var sb strings.Builder
	if h := governing(gl, first); h != "" {
		fmt.Fprintf(&sb, "  in:   %s\n", h)
	}
	fmt.Fprintf(&sb, "  line %d, first difference at byte %d:\n    want: %s\n    got:  %s\n", first+1, off, around(w, off), around(g, off))
	fmt.Fprintf(&sb, "  (%d line(s) differ in all)", differing)
	return sb.String()
}

// governing names the nearest header at or above line i: "@@ ..." (a message, block or
// tool of a transcript), "== ..." (a section), or a tool's "name" line in tools.json. It is
// empty for a file with no headers (tools/hashes.txt: every line names itself).
func governing(lines []string, i int) string {
	for ; i >= 0; i-- {
		if i >= len(lines) {
			continue
		}
		l := lines[i]
		if strings.HasPrefix(l, "@@ ") || strings.HasPrefix(l, "== ") || strings.HasPrefix(l, "    \"name\": ") {
			return strings.TrimSpace(l)
		}
	}
	return ""
}

// around returns the part of s near byte off, cut on character boundaries, with ... where
// it was cut.
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
