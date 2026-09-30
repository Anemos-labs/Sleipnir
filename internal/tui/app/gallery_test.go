package app

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/reee344/sleipnir/internal/tui/state"
	"github.com/reee344/sleipnir/internal/tui/widget"
)

// repoFile is a path in the repository, found by walking up from the package to go.mod.
func repoFile(t *testing.T, rel string) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return filepath.Join(dir, filepath.FromSlash(rel))
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatal("no go.mod above the package")
		}
		dir = parent
	}
}

const (
	showcaseLog     = "docs/media/showcase/events.jsonl"
	galleryManifest = "docs/media/gallery.json"
)

// The recordings in docs/media are made from the recorded session and nothing else. If a screen or a widget changes, they no longer
// are what the code draws, and this says so: scripts/record-demo.sh draws them again.
func TestTheCommittedGalleryIsWhatTheCodeDraws(t *testing.T) {
	recs, err := LoadGallery(repoFile(t, galleryManifest))
	if err != nil {
		t.Fatal(err)
	}
	if len(recs) < 2 {
		t.Fatalf("the gallery has %d recordings", len(recs))
	}
	docs, err := RenderGallery(repoFile(t, showcaseLog), recs)
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range recs {
		name := r.Name + ".svg"
		want, err := os.ReadFile(repoFile(t, "docs/media/"+name))
		if err != nil {
			t.Errorf("%v (scripts/record-demo.sh draws it)", err)
			continue
		}
		if string(want) != docs[name] {
			t.Errorf("docs/media/%s is not what the code draws from %s: run scripts/record-demo.sh and commit the result", name, showcaseLog)
		}
		if r.StillAt > 0 {
			if _, err := os.Stat(repoFile(t, "docs/media/"+r.Name+".png")); err != nil {
				t.Errorf("the still of %s is missing: %v", r.Name, err)
			}
		}
		if r.Caption == "" {
			t.Errorf("%s has no caption", r.Name)
		}
	}
}

// The session the recordings are made from has the story they claim to show: a swarm that fans out over one prefix, mail, an agent the
// harness tells it is going in circles, a provider that loses its cache (and what that cost), a compaction at a warm moment and one at a
// cold one, a merge that is sent back and a board that ends merged. If the demo is changed and the session made again, this
// is what has to stay true of it.
func TestTheShowcaseSessionHasTheStoryTheGalleryTells(t *testing.T) {
	st, err := state.Fold(repoFile(t, showcaseLog))
	if err != nil {
		t.Fatal(err)
	}
	sn := st.Snapshot()
	if s := sn.Stats; s.Unknown != 0 || s.Bad != 0 || s.Panics != 0 || s.Corrupt != 0 {
		t.Errorf("the terminal UI does not understand the recorded log: %+v", s)
	}
	if n := len(Agents(sn)); n != 9 {
		t.Errorf("%d agents, want the manager, three scouts, four writers and a reviewer", n)
	}
	if sn.Board.Counts.Merged != 8 || sn.Merge.Counts.Bounced < 1 || sn.Mail.Counts.Sent < 2 {
		t.Errorf("board %+v, merge queue %+v, mail %+v", sn.Board.Counts, sn.Merge.Counts, sn.Mail.Counts)
	}
	if sn.Totals.Anomalies < 3 {
		t.Errorf("%d cache anomalies: the provider's lost cache is the recording's break", sn.Totals.Anomalies)
	}
	for _, id := range []string{"be-1", "be-2"} {
		a := pick(sn, id)
		if a.ID != id {
			t.Fatalf("no agent %s", id)
		}
		switch id {
		case "be-2":
			if len(a.Anomalies) < 3 || a.Stuck.Nudges < 1 {
				t.Errorf("be-2 has %d anomalies and %d nudges: its recording is about a break and a stuck moment", len(a.Anomalies), a.Stuck.Nudges)
			}
			if len(a.Compacts) < 1 || a.Compacts[0].Moment != "warm" {
				t.Errorf("be-2's compactions: %+v", a.Compacts)
			}
		case "be-1":
			if len(a.Compacts) < 1 || a.Compacts[len(a.Compacts)-1].Moment != "cold" {
				t.Errorf("be-1's compaction must be at a cold moment, the fold's whole point: %+v", a.Compacts)
			}
		}
	}

	// and what is drawn from it is not empty on any screen
	for v := View(0); v < numViews; v++ {
		mem := NewMemory()
		mem.Seed(sn)
		text := plainText(Draw(Scene{Snap: sn, View: v, Agent: "be-2", Cols: 100, Rows: 44, Pal: widget.DefaultPalette(), Mem: mem, Mode: Mode{Final: true}}))
		if !strings.Contains(text, "SLEIPNIR") || strings.Count(text, "\n") != 44 {
			t.Errorf("%s is not a screen:\n%s", v, text)
		}
	}
}
