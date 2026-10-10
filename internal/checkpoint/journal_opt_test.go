package checkpoint

import (
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

// Without EnableJournal a store keeps no write journal: After saves no content and the
// manifest holds no entry, so a session that no web page shows costs what it always did.
func TestTheJournalIsOffUntilEnabled(t *testing.T) {
	e := newEnv(t)
	e.write("f.txt", "zero\n")
	cp := e.s.Begin("p")
	before := e.blobs.Len()
	for i := range 5 {
		e.editAfter("w", "f.txt", strings.Repeat("line\n", i+1))
	}
	if n := e.blobs.Len() - before; n != 1 {
		t.Fatalf("%d blobs saved, want only the pre-image", n)
	}
	if ws := e.s.Writes("f.txt"); len(ws) != 0 {
		t.Fatalf("journal without EnableJournal: %+v", ws)
	}
	manifest, err := os.ReadFile(filepath.Join(e.dir, cp+".json"))
	if err != nil || strings.Contains(string(manifest), `"writes"`) {
		t.Fatalf("manifest: %s %v", manifest, err)
	}
	// Restore still tells the agents' last write from a later edit: After's fingerprint.
	if err := os.WriteFile(e.abs("f.txt"), []byte("human\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if r := result(t, mustRestore(t, e.s, cp, RestoreOpts{DryRun: true}), "f.txt"); r.Outcome != OutcomeConflict {
		t.Fatalf("f.txt = %+v", r)
	}
}

// Enabled, the journal keeps content within its budget; past the budget an entry keeps
// who wrote but no content (authorship is then approximate). Records made before it was
// enabled are approximate too.
func TestEnabledJournalIsBounded(t *testing.T) {
	e := newEnv(t)
	e.write("early.txt", "a\n")
	e.s.Begin("p")
	e.editAfter("w", "early.txt", "a\nb\n") // before the journal: not journaled
	e.s.EnableJournal(40)
	e.editAfter("w", "early.txt", "a\nb\nc\n")
	if a, err := e.s.AuthorshipAt("", "early.txt"); err != nil || a.Exact {
		t.Fatalf("history from before the journal must be approximate: %+v %v", a, err)
	}
	e.editAfter("x", "small.txt", "1234567890\n")        // 11 bytes: kept
	e.editAfter("x", "big.txt", strings.Repeat("z", 60)) // over what is left: not kept
	if ws := e.s.Writes("small.txt"); len(ws) != 1 || ws[0].Blob == "" {
		t.Fatalf("small.txt: %+v", ws)
	}
	if ws := e.s.Writes("big.txt"); len(ws) != 1 || ws[0].Blob != "" || ws[0].Agent != "x" {
		t.Fatalf("big.txt past the budget: %+v", ws)
	}
	if a, err := e.s.AuthorshipAt("", "big.txt"); err != nil || a.Exact {
		t.Fatalf("big.txt authorship: %+v %v", a, err)
	}
	e.s.EnableJournal(1 << 20) // a second call changes nothing
	e.editAfter("x", "big2.txt", strings.Repeat("y", 60))
	if ws := e.s.Writes("big2.txt"); len(ws) != 1 || ws[0].Blob != "" {
		t.Fatalf("the budget is the first call's: %+v", ws)
	}
}

// One event per change, cheap: the count and the one new path, never the file list.
func TestOnEventCarriesTheCountAndTheNewPath(t *testing.T) {
	e := newEnv(t)
	var mu sync.Mutex
	var got []Event
	e.s.OnEvent(func(ev Event) {
		mu.Lock()
		got = append(got, ev)
		mu.Unlock()
	})
	cp := e.s.Begin("turn")
	e.editAfter("w", "a.txt", "x")
	e.editAfter("w", "a.txt", "y")
	e.editAfter("v", "b.txt", "z")
	mu.Lock()
	defer mu.Unlock()
	want := []Event{{ID: cp, Label: "turn", Files: 0}, {ID: cp, Label: "turn", Files: 1, Path: "a.txt"}, {ID: cp, Label: "turn", Files: 2, Path: "b.txt"}}
	if len(got) != len(want) {
		t.Fatalf("events %+v", got)
	}
	for i := range want {
		g := got[i]
		g.Time = want[i].Time
		if g != want[i] {
			t.Fatalf("event %d = %+v, want %+v", i, got[i], want[i])
		}
	}
	if a := e.s.Agents(cp); len(a) != 2 || a[0] != "v" || a[1] != "w" {
		t.Fatalf("agents %v", a)
	}
}
