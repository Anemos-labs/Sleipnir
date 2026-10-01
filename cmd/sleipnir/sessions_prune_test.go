package main

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

var pruneNow = time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)

// makeSession makes a session directory whose newest file is age old, with some size to it.
func makeSession(t *testing.T, root, id string, age time.Duration, size int) string {
	t.Helper()
	dir := filepath.Join(root, id)
	if err := os.MkdirAll(filepath.Join(dir, "blobs"), 0o755); err != nil {
		t.Fatal(err)
	}
	for name, n := range map[string]int{"events.jsonl": size, "blobs/b1": size} {
		if err := os.WriteFile(filepath.Join(dir, name), make([]byte, n), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	when := pruneNow.Add(-age)
	for _, p := range []string{filepath.Join(dir, "events.jsonl"), filepath.Join(dir, "blobs/b1"), filepath.Join(dir, "blobs"), dir} {
		if err := os.Chtimes(p, when, when); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

func ids(items []pruneItem) []string {
	var out []string
	for _, it := range items {
		out = append(out, it.id)
	}
	return out
}

// Old sessions go, the newest few never do whatever their age, a session written to a moment ago is taken for a running one, and what is
// not a session (a directory with no event log, a file, a link) is not touched.
func TestPlanPruneKeepsTheNewestTheRecentAndWhatIsNotASession(t *testing.T) {
	root := t.TempDir()
	day := 24 * time.Hour
	makeSession(t, root, "s-100d", 100*day, 1000)
	makeSession(t, root, "s-80d", 80*day, 1000)
	makeSession(t, root, "s-50d", 50*day, 2000)
	makeSession(t, root, "s-31d", 31*day, 1000)
	makeSession(t, root, "s-29d", 29*day, 1000)
	makeSession(t, root, "s-1h", time.Hour, 1000)
	makeSession(t, root, "s-5m", 5*time.Minute, 1000)
	if err := os.MkdirAll(filepath.Join(root, "notes"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "a-file"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	outside := t.TempDir()
	if err := os.WriteFile(filepath.Join(outside, "events.jsonl"), []byte("not yours"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(root, "a-link")); err != nil {
		t.Skipf("no symbolic links here: %v", err)
	}

	p, err := planPrune(root, pruneNow, 30*day, 2)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := ids(p.items), []string{"s-100d", "s-80d", "s-50d", "s-31d"}; !reflect.DeepEqual(got, want) {
		t.Errorf("to delete: %v, want %v (oldest first; 29 days is not old enough, and the newest two are kept)", got, want)
	}
	if p.total != 7 || p.keptNewest != 2 {
		t.Errorf("total %d, kept newest %d: want 7 sessions, 2 kept", p.total, p.keptNewest)
	}
	if want := int64(2000 + 2000 + 4000 + 2000); p.freed != want {
		t.Errorf("freed %d, want %d: the size of the log and of everything under the directory", p.freed, want)
	}
	if !reflect.DeepEqual(p.skipped, []string{"notes"}) {
		t.Errorf("skipped %v: a directory with no event log is left alone and said so; a file and a link are not even that", p.skipped)
	}

	// any age and nothing kept: all that is not in use goes; the session written five minutes ago is taken for a running one
	p, err = planPrune(root, pruneNow, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := ids(p.items), []string{"s-100d", "s-80d", "s-50d", "s-31d", "s-29d", "s-1h"}; !reflect.DeepEqual(got, want) {
		t.Errorf("to delete with any age: %v, want %v", got, want)
	}
	if !reflect.DeepEqual(p.inUse, []string{"s-5m"}) {
		t.Errorf("in use: %v, want the session written five minutes ago", p.inUse)
	}
}

func TestParseAgeAndTheTextsAroundIt(t *testing.T) {
	for in, want := range map[string]time.Duration{"30d": 30 * 24 * time.Hour, "2w": 14 * 24 * time.Hour, "36h": 36 * time.Hour, "90m": 90 * time.Minute, "0": 0, "1.5d": 36 * time.Hour} {
		if got, err := parseAge(in); err != nil || got != want {
			t.Errorf("parseAge(%q) = %v, %v; want %v", in, got, err, want)
		}
	}
	for _, in := range []string{"", "soon", "-3d", "d", "3 days"} {
		if _, err := parseAge(in); err == nil {
			t.Errorf("parseAge(%q) must fail", in)
		}
	}
	for d, want := range map[time.Duration]string{5 * time.Minute: "5m", 3 * time.Hour: "3h", 49 * time.Hour: "2d", 80 * 24 * time.Hour: "80d", 200 * 24 * time.Hour: "6mo"} {
		if got := ageText(d); got != want {
			t.Errorf("ageText(%v) = %q, want %q", d, got, want)
		}
	}
	for n, want := range map[int64]string{0: "0 B", 1023: "1023 B", 2048: "2 KB", 31 << 20: "31 MB", 3 << 30: "3.0 GB"} {
		if got := sizeText(n); got != want {
			t.Errorf("sizeText(%d) = %q, want %q", n, got, want)
		}
	}
}

// The command lists what it would delete and deletes nothing, until it is told to.
func TestSessionsPruneNeedsYesToDelete(t *testing.T) {
	w := newWorld(t, "")
	root := filepath.Join(w.state, "sessions")
	day := 24 * time.Hour
	// the command runs now, not at pruneNow: the sessions are made relative to the real clock
	old := time.Now().Add(-90 * day)
	for _, id := range []string{"20260101-000000-aaaaaa", "20260102-000000-bbbbbb"} {
		dir := filepath.Join(root, id)
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		log := filepath.Join(dir, "events.jsonl")
		if err := os.WriteFile(log, []byte(strings.Repeat("x", 4096)), 0o644); err != nil {
			t.Fatal(err)
		}
		for _, p := range []string{log, dir} {
			if err := os.Chtimes(p, old, old); err != nil {
				t.Fatal(err)
			}
		}
	}
	fresh := filepath.Join(root, "20260930-000000-cccccc")
	if err := os.MkdirAll(fresh, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(fresh, "events.jsonl"), []byte("{}\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	res := w.run("", "sessions", "prune", "--keep", "1")
	if res.code != 0 || !strings.Contains(res.stdout, "would delete 2 of 3 sessions") || !strings.Contains(res.stdout, "nothing deleted: run again with --yes") {
		t.Fatalf("status %d\nstdout: %s\nstderr: %s", res.code, res.stdout, res.stderr)
	}
	for _, id := range []string{"20260101-000000-aaaaaa", "20260102-000000-bbbbbb", "20260930-000000-cccccc"} {
		if !exists(filepath.Join(root, id)) {
			t.Errorf("%s was deleted without --yes", id)
		}
	}

	res = w.run("", "sessions", "prune", "--keep", "1", "--yes")
	if res.code != 0 || !strings.Contains(res.stdout, "deleted 2 sessions") {
		t.Fatalf("status %d\nstdout: %s\nstderr: %s", res.code, res.stdout, res.stderr)
	}
	if exists(filepath.Join(root, "20260101-000000-aaaaaa")) || exists(filepath.Join(root, "20260102-000000-bbbbbb")) {
		t.Error("the old sessions are still there")
	}
	if !exists(fresh) {
		t.Error("the new session was deleted")
	}

	res = w.run("", "sessions", "prune", "--yes")
	if res.code != 0 || !strings.Contains(res.stdout, "nothing to prune") {
		t.Errorf("a second run: status %d\n%s", res.code, res.stdout)
	}
}

func TestSessionsPruneRefusesWhatItDoesNotUnderstand(t *testing.T) {
	w := newWorld(t, "")
	for _, c := range []struct {
		args []string
		want string
	}{
		{[]string{"sessions", "prune", "--older-than", "soon"}, "is not an age"},
		{[]string{"sessions", "prune", "--keep", "-1"}, "must not be negative"},
		{[]string{"sessions", "prune", "extra"}, "unexpected argument"},
	} {
		res := w.run("", c.args...)
		if res.code == 0 || !strings.Contains(res.stderr, c.want) {
			t.Errorf("%v: status %d, stderr %q; want a failure that says %q", c.args, res.code, res.stderr, c.want)
		}
	}
	if res := w.run("", "sessions", "prune"); res.code != 0 || !strings.Contains(res.stderr, "no sessions yet") {
		t.Errorf("no state directory yet: status %d, stderr %q", res.code, res.stderr)
	}
}
