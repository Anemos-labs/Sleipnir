package agent_test

import (
	"bytes"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/anemos-labs/sleipnir/internal/agent"
	"github.com/anemos-labs/sleipnir/internal/core"
	"github.com/anemos-labs/sleipnir/internal/kv"
)

// update rewrites the golden files under testdata/golden instead of comparing with them:
//
//	go test ./internal/agent -run TestGoldenConstitution -update
//
// The constitution is the deepest cached layer: its bytes are the first bytes of every
// request of every agent, so a changed byte is a new prefix for the whole swarm. Read
// docs/BUILDING.md ("Changing prompt bytes") before accepting a new one.
var update = flag.Bool("update", false, "rewrite golden files under testdata/golden")

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
		t.Fatalf("missing golden file %s (create it with: go test ./internal/agent -run TestGoldenConstitution -update): %v", path, err)
	}
	if !bytes.Equal(want, got) {
		t.Fatalf("the constitution changed: output differs from %s\n%s\n"+
			"If this change is intended, follow docs/BUILDING.md (\"Changing prompt bytes\"), then run: go test ./internal/agent -run TestGoldenConstitution -update",
			path, lineDiff(want, got))
	}
}

// lineDiff shows the first line that differs, the section ("# heading") it is in, and how
// many lines differ.
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
		return "(end of text)"
	}
	w, g := line(wl), line(gl)
	off := 0
	for off < len(w) && off < len(g) && w[off] == g[off] {
		off++
	}
	section := "(before the first heading)"
	for i := first; i >= 0; i-- {
		if i < len(gl) && strings.HasPrefix(gl[i], "# ") {
			section = gl[i]
			break
		}
	}
	return fmt.Sprintf("  in section: %s\n  line %d, first difference at byte %d:\n    want: %s\n    got:  %s\n  (%d line(s) differ in all; %d bytes want, %d got)",
		section, first+1, off, around(w, off), around(g, off), differing, len(want), len(got))
}

// around returns the part of s near byte off, cut on character boundaries, with ... where
// it was cut, so that a change late in a long line is still in view.
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

// TestGoldenConstitution pins the system prompt every agent of a session sends: the text
// Constitution returns for a single agent and for a swarm, and its hashes, both as written
// and as the const layer the session builds from it (which is what is rendered into the
// system block). The text is in testdata/golden/constitution_solo.txt and
// constitution_swarm.txt, the hashes in constitution_hashes.txt. The other tests of the
// constitution (what it must say, how long it may be) are about meaning; this one is about
// bytes.
func TestGoldenConstitution(t *testing.T) {
	variants := []struct {
		name string
		opts agent.ConstitutionOpts
	}{
		{"solo", agent.ConstitutionOpts{}},
		{"swarm", agent.ConstitutionOpts{Swarm: true}},
	}
	var hashes strings.Builder
	hashes.WriteString("# The constitution, as written and as the const layer the session renders into the system block\n")
	hashes.WriteString("# (Session.build: kv.NewLayer(\"const\", kv.KindConst, 1, one frozen segment holding the text); the layer trims trailing newlines)\n")
	hashes.WriteString("# columns: variant, what, byte count, tokens at 4 bytes per token, sha256\n")
	hashes.WriteString("# regenerate: go test ./internal/agent -run TestGoldenConstitution -update   (read docs/BUILDING.md, \"Changing prompt bytes\", first)\n")
	est := core.NewBytesEstimator().WithRatio(4)
	for _, v := range variants {
		text := agent.Constitution(v.opts)
		layer := kv.NewLayer("const", kv.KindConst, 1, []kv.Segment{{Text: text, Vol: kv.VolFrozen}})
		fmt.Fprintf(&hashes, "%s\ttext\t%d\t%d\t%s\n", v.name, len(text), est.Tokens(text), core.HashString(text))
		fmt.Fprintf(&hashes, "%s\tlayer\t%d\t%d\t%s\n", v.name, len(layer.Text()), layer.Tokens(est), layer.Hash())
		t.Run(v.name, func(t *testing.T) {
			golden(t, "constitution_"+v.name+".txt", []byte(text))
			// Every agent of a session asks for the same text: it is a pure function of its options.
			for i := 0; i < 5; i++ {
				if again := agent.Constitution(v.opts); again != text {
					t.Fatalf("the constitution is not a pure function of its options (call %d differs)", i)
				}
			}
		})
	}
	t.Run("hashes", func(t *testing.T) {
		golden(t, "constitution_hashes.txt", []byte(hashes.String()))
	})
}
