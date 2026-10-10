package session

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

// makePruneSession makes a session directory whose newest file is age old, with size bytes in its log and as much in a blob.
func makePruneSession(t *testing.T, root, id string, now time.Time, age time.Duration, size int) string {
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
	when := now.Add(-age)
	for _, p := range []string{filepath.Join(dir, "events.jsonl"), filepath.Join(dir, "blobs/b1"), filepath.Join(dir, "blobs"), dir} {
		if err := os.Chtimes(p, when, when); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

func ids(rs []Recorded) []string {
	var out []string
	for _, r := range rs {
		out = append(out, r.ID)
	}
	return out
}

// The rule of `sleipnir sessions prune` (cmd/sleipnir's own test has the same expectations): old sessions go, the newest few never
// do, a session written to a moment ago is taken for a running one, and what is not a session is not touched.
func TestPlanPruneDirIsTheCLIRule(t *testing.T) {
	now := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
	root := t.TempDir()
	day := 24 * time.Hour
	makePruneSession(t, root, "s-100d", now, 100*day, 1000)
	makePruneSession(t, root, "s-80d", now, 80*day, 1000)
	makePruneSession(t, root, "s-50d", now, 50*day, 2000)
	makePruneSession(t, root, "s-31d", now, 31*day, 1000)
	makePruneSession(t, root, "s-29d", now, 29*day, 1000)
	makePruneSession(t, root, "s-1h", now, time.Hour, 1000)
	makePruneSession(t, root, "s-5m", now, 5*time.Minute, 1000)
	if err := os.MkdirAll(filepath.Join(root, "notes"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "a-file"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	p, err := PlanPruneDir(root, now, 30*day, 2, nil)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := ids(p.Delete), []string{"s-100d", "s-80d", "s-50d", "s-31d"}; !reflect.DeepEqual(got, want) {
		t.Errorf("to delete: %v, want %v", got, want)
	}
	if p.Total != 7 || p.KeptNewest != 2 || p.Bytes != 10000 || !reflect.DeepEqual(p.Skipped, []string{"notes"}) {
		t.Errorf("plan: %+v", p)
	}
	p, _ = PlanPruneDir(root, now, 0, 0, nil)
	if got, want := ids(p.Delete), []string{"s-100d", "s-80d", "s-50d", "s-31d", "s-29d", "s-1h"}; !reflect.DeepEqual(got, want) || !reflect.DeepEqual(p.InUse, []string{"s-5m"}) {
		t.Errorf("any age: %v, in use %v", got, p.InUse)
	}
	// a live session counts as the newest and is never deleted, whatever its age
	p, _ = PlanPruneDir(root, now, 0, 1, []string{"s-100d"})
	if got, want := ids(p.Delete), []string{"s-80d", "s-50d", "s-31d", "s-29d", "s-1h"}; !reflect.DeepEqual(got, want) || p.KeptNewest != 1 {
		t.Errorf("live: %v kept %d, want %v and 1", got, p.KeptNewest, want)
	}
	if _, err := PlanPruneDir(filepath.Join(root, "none"), now, 0, 0, nil); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("no directory: %v", err)
	}
}

// PlanPrune moves a session another process holds out of the deletions, and fills the rows as the listing does.
func TestPlanPruneSkipsALockedSession(t *testing.T) {
	home := t.TempDir()
	t.Setenv("SLEIPNIR_HOME", filepath.Join(home, "state"))
	root := SessionsDir(home)
	now := time.Now()
	held := makePruneSession(t, root, "20260101-000000-aaaaaa", now, 90*24*time.Hour, 100)
	free := makePruneSession(t, root, "20260102-000000-bbbbbb", now, 80*24*time.Hour, 100)
	unlock, err := lockDir(held)
	if err != nil {
		t.Fatal(err)
	}
	defer unlock()
	// lockDir made the lock file: give the directory its age back
	old := now.Add(-90 * 24 * time.Hour)
	_ = os.Chtimes(held, old, old)
	p, err := PlanPrune(home, now, 30*24*time.Hour, 0, nil)
	if err != nil {
		t.Fatal(err)
	}
	if got := ids(p.Delete); !reflect.DeepEqual(got, []string{filepath.Base(free)}) || !reflect.DeepEqual(p.Locked, []string{filepath.Base(held)}) {
		t.Fatalf("delete %v, locked %v", got, p.Locked)
	}
	if p.Bytes != 200 {
		t.Errorf("bytes %d", p.Bytes)
	}
	t.Setenv("SLEIPNIR_HOME", filepath.Join(home, "none"))
	if p, err := PlanPrune("", now, 0, 0, nil); err != nil || len(p.Delete) != 0 {
		t.Errorf("no sessions yet: %+v %v", p, err)
	}
}

// RemoveRecorded deletes what it is asked to, with prune's safety: a held session stays, an id that is not one touches nothing.
func TestRemoveRecordedIsAsSafeAsPrune(t *testing.T) {
	home := t.TempDir()
	t.Setenv("SLEIPNIR_HOME", filepath.Join(home, "state"))
	root := SessionsDir(home)
	now := time.Now()
	a := makePruneSession(t, root, "20260101-000000-aaaaaa", now, time.Hour, 100)
	b := makePruneSession(t, root, "20260102-000000-bbbbbb", now, time.Hour, 100)
	outside := t.TempDir()
	if err := os.WriteFile(filepath.Join(outside, "events.jsonl"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	unlock, err := lockDir(b)
	if err != nil {
		t.Fatal(err)
	}
	defer unlock()
	rel, _ := filepath.Rel(root, outside)
	got := RemoveRecorded(context.Background(), home, []string{filepath.Base(a), filepath.Base(b), rel, "20260103-000000-cccccc"})
	if len(got) != 4 || got[0].Err != nil || got[0].Bytes != 200 {
		t.Fatalf("a: %+v", got)
	}
	if got[1].Err == nil || !strings.Contains(got[1].Err.Error(), "in use") {
		t.Errorf("a held session was deleted or not refused by its lock: %v", got[1].Err)
	}
	if got[2].Err == nil || got[3].Err == nil {
		t.Errorf("a path and an unknown id: %v, %v", got[2].Err, got[3].Err)
	}
	if _, err := os.Stat(a); !os.IsNotExist(err) {
		t.Error("a is still there")
	}
	if _, err := os.Stat(b); err != nil {
		t.Error("the held session was deleted")
	}
	if _, err := os.Stat(filepath.Join(outside, "events.jsonl")); err != nil {
		t.Error("a directory outside the sessions was touched")
	}
}

func TestParseAge(t *testing.T) {
	for in, want := range map[string]time.Duration{"30d": 30 * 24 * time.Hour, "2w": 14 * 24 * time.Hour, "36h": 36 * time.Hour, "0": 0, "1.5d": 36 * time.Hour, "106751d": 106751 * 24 * time.Hour} {
		if got, err := ParseAge(in); err != nil || got != want {
			t.Errorf("ParseAge(%q) = %v, %v", in, got, err)
		}
	}
	for _, in := range []string{"", "soon", "-3d", "d", "3 days", "NaNd", "nanw", "Infd", "+Infw", "-Infd", "1e300d", "9999999999d", "106752d", "15251w"} {
		if _, err := ParseAge(in); err == nil || !strings.Contains(err.Error(), "is not an age") {
			t.Errorf("ParseAge(%q): %v", in, err)
		}
	}
}
