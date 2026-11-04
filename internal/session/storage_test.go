package session_test

import (
	"context"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/anemos-labs/sleipnir/internal/config"
	"github.com/anemos-labs/sleipnir/internal/gitx"
	"github.com/anemos-labs/sleipnir/internal/session"
	"github.com/anemos-labs/sleipnir/internal/workspace"
)

// ageAll sets the modification time of everything under dir (links not followed) to at.
func ageAll(t *testing.T, dir string, at time.Time) {
	t.Helper()
	err := filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.Type()&fs.ModeSymlink != 0 {
			return err
		}
		return os.Chtimes(p, at, at)
	})
	if err != nil {
		t.Fatal(err)
	}
}

// fakeSession records a session directory with an event log and a scratch file, last written at.
func fakeSession(t *testing.T, home, id string, at time.Time) string {
	t.Helper()
	dir := filepath.Join(session.SessionsDir(home), id)
	if err := os.MkdirAll(filepath.Join(dir, "tmp", "go-build"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "events.jsonl"), []byte(`{"type":"session.start"}`+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "tmp", "go-build", "obj"), []byte(strings.Repeat("x", 4096)), 0o600); err != nil {
		t.Fatal(err)
	}
	ageAll(t, dir, at)
	return dir
}

// The cleanup of the data directory: an old session goes, the newest are kept, the scratch directory of a session nobody holds is
// emptied once it has gone a day unwritten, and the worktrees of a session that is gone are released, their unsaved edits committed
// onto their branches first, in the data directory and in the cache directory of earlier versions alike. A tree whose owner is
// running is left as it is. A dry run measures all of that and changes nothing.
func TestStorageCleanupRemovesWhatIsOldAndKeepsTheWork(t *testing.T) {
	t.Setenv("SLEIPNIR_HOME", "")
	ctx := context.Background()
	repo := isoRepo(t)
	home := t.TempDir()
	now := time.Now()
	old := now.Add(-60 * 24 * time.Hour)

	oldSess := fakeSession(t, home, "20260101-000000-aaaaaa", old)
	keptSess := fakeSession(t, home, "20260102-000000-bbbbbb", old.Add(time.Hour)) // among the newest two: kept, its scratch emptied
	freshSess := fakeSession(t, home, "20260103-000000-cccccc", now.Add(-time.Hour))

	r, err := gitx.Open(repo)
	if err != nil {
		t.Fatal(err)
	}
	makeTree := func(top, sid string) *workspace.Tree {
		m := &workspace.Manager{Repo: r, Dir: filepath.Join(top, sid), Prefix: "sleipnir/" + sid}
		tr, err := m.Create(ctx, "be-1", workspace.CreateOptions{})
		if err != nil {
			t.Fatal(err)
		}
		return tr
	}
	legacy := filepath.Join(home, ".cache", "sleipnir", "worktrees")
	gone := makeTree(session.WorktreesDir(home), "20250101-000000-dddddd")
	goneOld := makeTree(legacy, "20250102-000000-eeeeee")
	running := makeTree(session.WorktreesDir(home), "20250103-000000-ffffff")
	for _, tr := range []*workspace.Tree{gone, goneOld} {
		if err := os.WriteFile(filepath.Join(tr.Path, "wip.txt"), []byte("unsaved work of "+tr.Branch+"\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		crash(t, repo, tr.Path)
	}
	for _, tr := range []*workspace.Tree{gone, goneOld, running} {
		ageAll(t, filepath.Dir(tr.Path), old)
	}

	opts := session.CleanOptions{OlderThan: 30 * 24 * time.Hour, Keep: 2, Now: now}
	dry := opts
	dry.DryRun = true
	plan, err := session.CleanStorage(ctx, home, dry)
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.Sessions.Delete) != 1 || plan.Sessions.Delete[0].ID != filepath.Base(oldSess) || plan.Scratch != 1 || len(plan.Trees) != 3 || plan.Freed() == 0 {
		t.Fatalf("dry run: %d sessions, %d scratch, trees %v, %d bytes", len(plan.Sessions.Delete), plan.Scratch, plan.Trees, plan.Freed())
	}
	for _, p := range []string{oldSess, filepath.Join(keptSess, "tmp", "go-build", "obj"), gone.Path, goneOld.Path} {
		if _, err := os.Stat(p); err != nil {
			t.Fatalf("a dry run removed %s", p)
		}
	}

	rep, err := session.CleanStorage(ctx, home, opts)
	if err != nil {
		t.Fatal(err)
	}
	if len(rep.Errors) > 0 {
		t.Errorf("errors: %v", rep.Errors)
	}
	if _, err := os.Stat(oldSess); !os.IsNotExist(err) {
		t.Errorf("the old session is still there: %v", err)
	}
	if _, err := os.Stat(filepath.Join(keptSess, "events.jsonl")); err != nil {
		t.Errorf("the newest old session was deleted: %v", err)
	}
	if _, err := os.Stat(filepath.Join(keptSess, "tmp")); !os.IsNotExist(err) {
		t.Errorf("the scratch directory of an idle session was not emptied: %v", err)
	}
	if _, err := os.Stat(filepath.Join(freshSess, "tmp", "go-build", "obj")); err != nil {
		t.Errorf("the scratch directory of a session written an hour ago was emptied: %v", err)
	}
	for _, tr := range []*workspace.Tree{gone, goneOld} {
		if _, err := os.Stat(tr.Path); !os.IsNotExist(err) {
			t.Errorf("the tree of a session that is gone is still there: %s", tr.Path)
		}
		if got := git(t, repo, "show", tr.Branch+":wip.txt"); got != "unsaved work of "+tr.Branch {
			t.Errorf("the unsaved work of %s was not kept on its branch: %q", tr.Branch, got)
		}
	}
	if len(rep.Kept) != 3 {
		t.Errorf("the branches holding the salvaged work are not reported: %v", rep.Kept)
	}
	if _, err := os.Stat(running.Path); err != nil {
		t.Errorf("the tree of a running owner was removed: %v", err)
	}
	if _, err := os.Stat(legacy); !os.IsNotExist(err) {
		t.Errorf("the old cache location is still there once empty: %v", err)
	}
}

// The automatic cleanup runs once a day at most, and never when the person turned it off.
func TestAutoCleanRunsOnceADayUnlessTurnedOff(t *testing.T) {
	t.Setenv("SLEIPNIR_HOME", "")
	ctx := context.Background()
	home := t.TempDir()
	now := time.Now()
	fakeSession(t, home, "20260101-000000-aaaaaa", now.Add(-90*24*time.Hour))
	if rep, err := session.AutoClean(ctx, home, config.Storage{Clean: "off"}, nil, now); rep != nil || err != nil {
		t.Fatalf("turned off: %+v %v", rep, err)
	}
	rep, err := session.AutoClean(ctx, home, config.Storage{KeepSessions: 1}, nil, now)
	if err != nil || rep == nil {
		t.Fatalf("first run: %+v %v", rep, err)
	}
	if rep2, err := session.AutoClean(ctx, home, config.Storage{KeepSessions: 1}, nil, now.Add(time.Hour)); rep2 != nil || err != nil {
		t.Fatalf("an hour later it ran again: %+v %v", rep2, err)
	}
	if rep3, err := session.AutoClean(ctx, home, config.Storage{KeepSessions: 1}, nil, now.Add(25*time.Hour)); rep3 == nil || err != nil {
		t.Fatalf("a day later it did not run: %+v %v", rep3, err)
	}
}

// The usage lists the areas of the data directory, scratch apart from the sessions, and the worktrees of earlier versions.
func TestStorageUsageNamesEachArea(t *testing.T) {
	t.Setenv("SLEIPNIR_HOME", "")
	home := t.TempDir()
	fakeSession(t, home, "20260101-000000-aaaaaa", time.Now())
	legacy := filepath.Join(home, ".cache", "sleipnir", "worktrees", "x", "be-1")
	if err := os.MkdirAll(legacy, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(legacy, "f"), []byte("12345"), 0o600); err != nil {
		t.Fatal(err)
	}
	areas, total := session.StorageUsage(home)
	got := map[string]int64{}
	var sum int64
	for _, a := range areas {
		got[a.Key] = a.Bytes
		sum += a.Bytes
	}
	if got["scratch"] != 4096 || got["sessions"] <= 0 || got["legacy-worktrees"] != 5 {
		t.Errorf("areas: %+v", areas)
	}
	if sum != total {
		t.Errorf("the areas add up to %d, the total is %d", sum, total)
	}
}
