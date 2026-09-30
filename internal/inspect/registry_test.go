package inspect

import (
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"
)

// sessionTree builds a directory shaped like `sleipnir rl rollout` output plus
// the traps a hostile or messy tree can contain.
func sessionTree(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	mk := func(rel string, seed int64) {
		writeSynth(t, filepath.Join(root, filepath.FromSlash(rel)), synthCfg{Seed: seed, Workers: 1, Steps: 10, Session: "s-" + filepath.Base(rel)})
	}
	mk("r001/taskA/0", 1)
	mk("r001/taskA/1", 2)
	mk("r001/taskB/0", 3)
	mk("loose", 4)
	// Must be ignored: blobs, checkpoints and VCS directories, and too-deep trees.
	for _, d := range []string{"r001/taskA/0/blobs/x", "r001/checkpoints/y", ".git/z", "node_modules/w", "deep/a/b/c/d/e"} {
		if err := os.MkdirAll(filepath.Join(root, filepath.FromSlash(d)), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(root, filepath.FromSlash(d), "events.jsonl"), []byte(`{"seq":1,"ts":"2026-01-01T00:00:00Z","type":"log.open"}`+"\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	// A symlinked directory and a symlinked log are not followed.
	outside := t.TempDir()
	writeSynth(t, outside, synthCfg{Seed: 9, Workers: 1, Steps: 5})
	if err := os.Symlink(outside, filepath.Join(root, "linkdir")); err == nil {
		if err := os.MkdirAll(filepath.Join(root, "linklog"), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(filepath.Join(outside, "events.jsonl"), filepath.Join(root, "linklog", "events.jsonl")); err != nil {
			t.Fatal(err)
		}
	}
	return root
}

func ids(l SessionList) []string {
	var out []string
	for _, s := range l.Sessions {
		out = append(out, s.ID)
	}
	sort.Strings(out)
	return out
}

func TestDiscoveryFindsSessionsAndOnlySessions(t *testing.T) {
	root := sessionTree(t)
	list, err := Sessions(root, Options{})
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"loose", "r001/taskA/0", "r001/taskA/1", "r001/taskB/0"}
	if list.Mode != "multi" || strings.Join(ids(list), ",") != strings.Join(want, ",") {
		t.Fatalf("mode %s sessions %v, want %v (no blobs/, checkpoints/, .git, node_modules, too-deep or symlinked entries)", list.Mode, ids(list), want)
	}
	for _, s := range list.Sessions {
		if s.Digest == nil || s.Digest.Requests == 0 || s.Digest.Model != "claude-sonnet-5-5" || s.Bytes == 0 || s.Digest.HitRatio <= 0 {
			t.Errorf("session %s digest = %+v", s.ID, s.Digest)
		}
		if strings.ContainsRune(s.ID, '\\') || strings.HasPrefix(s.ID, "/") || strings.Contains(s.ID, "..") {
			t.Errorf("session id %q is not a clean relative path", s.ID)
		}
	}
	if list.Root != filepath.Base(root) {
		t.Errorf("root = %q, want only the base name", list.Root)
	}

	// A session directory is a single-session root.
	one, err := Sessions(filepath.Join(root, "loose"), Options{})
	if err != nil || one.Mode != "single" || len(one.Sessions) != 1 || one.Sessions[0].ID != "." || one.Current != "." {
		t.Errorf("single = %+v err %v", one, err)
	}
	if _, err := Sessions(t.TempDir(), Options{}); err == nil || !strings.Contains(err.Error(), "no events.jsonl") {
		t.Errorf("an empty directory: %v", err)
	}
	if _, err := Sessions(filepath.Join(root, "nope"), Options{}); err == nil {
		t.Error("a missing directory did not fail")
	}
}

func TestOpenSessionMatchesOnlyDiscoveredIDs(t *testing.T) {
	root := sessionTree(t)
	s, err := OpenSession(root, "r001/taskA/1", Options{})
	if err != nil {
		t.Fatal(err)
	}
	if s.Summary().Session.Name != "r001/taskA/1" || s.Summary().Totals.Requests == 0 {
		t.Errorf("opened %+v", s.Summary().Session)
	}
	for _, bad := range []string{"", "nope", "../loose", "r001/../loose", "/etc", "r001/taskA", "r001\\taskA\\0", "r001/taskA/0/", "./loose", "loose/../loose", "%2e%2e"} {
		if _, err := OpenSession(root, bad, Options{}); err == nil {
			t.Errorf("OpenSession(%q) succeeded: ids are looked up, never joined into a path", bad)
		}
	}
	// In a single-session directory the id is ignored.
	if _, err := OpenSession(filepath.Join(root, "loose"), "", Options{}); err != nil {
		t.Errorf("single-session open: %v", err)
	}
}

func TestEntryLoadsInTheBackgroundAndProgressIsReported(t *testing.T) {
	dir, _ := synthDir(t, synthCfg{Workers: 2, Steps: 40})
	reg, err := newRegistry(dir, Options{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	e := reg.get("")
	if e == nil {
		t.Fatal("no entry for the only session")
	}
	// A zero wait never blocks: it starts the load and answers "not ready" or, if the
	// goroutine was quick, the session.
	sess, ready, err := e.ensure(Options{}, 0, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !ready {
		deadline := time.Now().Add(10 * time.Second)
		for !ready && time.Now().Before(deadline) {
			sess, ready, err = e.ensure(Options{}, 50*time.Millisecond, nil)
		}
	}
	if !ready || err != nil || sess == nil || sess.Summary().Totals.Requests == 0 {
		t.Fatalf("session did not load: ready=%v err=%v", ready, err)
	}
	if e.read.Load() == 0 || e.read.Load() != e.total.Load() {
		t.Errorf("progress = %d of %d", e.read.Load(), e.total.Load())
	}
	if again := reg.only(); again != e {
		t.Error("only() must return the single entry")
	}
}

func TestLoadedSessionsAreBoundedInMultiSessionMode(t *testing.T) {
	root := t.TempDir()
	n := maxLoaded + 4
	for i := 0; i < n; i++ {
		writeSynth(t, filepath.Join(root, "s"+string(rune('a'+i))), synthCfg{Seed: int64(i + 1), Workers: 0, MgrSteps: 3, Steps: 3})
	}
	reg, err := newRegistry(root, Options{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < n; i++ {
		e := reg.get("s" + string(rune('a'+i)))
		if _, ready, err := e.ensure(Options{}, 5*time.Second, nil); !ready || err != nil {
			t.Fatalf("load %d: ready=%v err=%v", i, ready, err)
		}
		time.Sleep(2 * time.Millisecond) // distinct lastUse
	}
	reg.trim()
	loaded := 0
	for _, e := range reg.entries {
		if e.loaded() != nil {
			loaded++
		} else if e.digest == nil {
			t.Errorf("session %s was dropped without keeping its digest", e.id)
		}
	}
	if loaded != maxLoaded {
		t.Errorf("%d sessions loaded, want %d", loaded, maxLoaded)
	}
	// The dropped ones load again on demand.
	for _, e := range reg.entries {
		if e.loaded() == nil {
			if s, ready, err := e.ensure(Options{}, 5*time.Second, nil); !ready || err != nil || s == nil {
				t.Errorf("reloading %s: ready=%v err=%v", e.id, ready, err)
			}
			break
		}
	}
}

func TestSessionListOrdersLiveFirstThenNewest(t *testing.T) {
	root := t.TempDir()
	for _, n := range []string{"old", "new", "live"} {
		writeSynth(t, filepath.Join(root, n), synthCfg{Workers: 0, MgrSteps: 2, Steps: 2, Session: n})
	}
	now := time.Now()
	for n, age := range map[string]time.Duration{"old": 3 * time.Hour, "new": time.Hour, "live": time.Second} {
		if err := os.Chtimes(filepath.Join(root, n, "events.jsonl"), now.Add(-age), now.Add(-age)); err != nil {
			t.Fatal(err)
		}
	}
	reg, err := newRegistry(root, Options{Now: func() time.Time { return now }}, nil)
	if err != nil {
		t.Fatal(err)
	}
	l := reg.list("")
	var order []string
	for _, s := range l.Sessions {
		order = append(order, s.ID)
	}
	if strings.Join(order, ",") != "live,new,old" || !l.Sessions[0].Live || l.Sessions[1].Live {
		t.Errorf("order = %v live flags %v %v", order, l.Sessions[0].Live, l.Sessions[1].Live)
	}
}

func TestRolloutEpisodesAreShownInTheList(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "task1", "0")
	writeSynth(t, dir, synthCfg{Workers: 0, MgrSteps: 2, Steps: 2})
	ep := `{"id":"task1/0","task_id":"task1","sample":0,"outcome":{"verifier":{"pass":false,"score":0.2}},"reward":{"total":-0.1},"flags":["budget_exceeded"]}`
	if err := os.WriteFile(filepath.Join(dir, "episode.json"), []byte(ep), 0o600); err != nil {
		t.Fatal(err)
	}
	list, err := Sessions(root, Options{})
	if err != nil || len(list.Sessions) != 1 {
		t.Fatalf("%v %v", list, err)
	}
	e := list.Sessions[0].Episode
	if e == nil || e.TaskID != "task1" || e.Pass == nil || *e.Pass || e.Reward != -0.1 || len(e.Flags) != 1 {
		t.Errorf("episode = %+v", e)
	}
}

func TestASymlinkedRootIsResolved(t *testing.T) {
	root := sessionTree(t)
	link := filepath.Join(t.TempDir(), "latest")
	if err := os.Symlink(root, link); err != nil {
		t.Skip("symlinks unavailable:", err)
	}
	list, err := Sessions(link, Options{})
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"loose", "r001/taskA/0", "r001/taskA/1", "r001/taskB/0"}
	if strings.Join(ids(list), ",") != strings.Join(want, ",") {
		t.Errorf("through a symlinked root: %v, want %v", ids(list), want)
	}
	if s, err := OpenSession(link, "loose", Options{}); err != nil || s.Summary().Totals.Requests == 0 {
		t.Errorf("OpenSession through a symlinked root: %v", err)
	}
}

func TestAFailedLoadIsRetriedLater(t *testing.T) {
	dir, _ := synthDir(t, synthCfg{Workers: 0, MgrSteps: 2, Steps: 2})
	reg, err := newRegistry(dir, Options{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	e := reg.get("")
	log := filepath.Join(dir, "events.jsonl")
	saved, err := os.ReadFile(log)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(log); err != nil {
		t.Fatal(err)
	}
	if _, ready, err := e.ensure(Options{}, 5*time.Second, nil); !ready || err == nil {
		t.Fatalf("a missing log: ready=%v err=%v", ready, err)
	}
	if err := os.WriteFile(log, saved, 0o600); err != nil {
		t.Fatal(err)
	}
	// The failure is remembered briefly, so a broken log is not re-read on every poll...
	if _, _, err := e.ensure(Options{}, 5*time.Second, nil); err == nil {
		t.Error("the failed load was retried at once")
	}
	// ...and forgotten after a while, so a log that got fixed or finished loads.
	e.mu.Lock()
	e.errAt = time.Now().Add(-time.Minute)
	e.mu.Unlock()
	s, ready, err := e.ensure(Options{}, 5*time.Second, nil)
	if !ready || err != nil || s == nil || s.Summary().Totals.Requests == 0 {
		t.Fatalf("retry: ready=%v err=%v", ready, err)
	}
}
