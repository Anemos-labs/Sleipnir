package checkpoint

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/anemos-labs/sleipnir/internal/events"
)

func TestBeginIssuesShortOrderedIDs(t *testing.T) {
	e := newEnv(t)
	var ids []string
	for i := 0; i < 3; i++ {
		ids = append(ids, e.s.Begin(fmt.Sprintf("prompt %d", i)))
	}
	if want := []string{"cp_0001", "cp_0002", "cp_0003"}; !reflect.DeepEqual(ids, want) {
		t.Fatalf("ids = %v, want %v", ids, want)
	}
	list := e.s.List()
	if len(list) != 3 || list[0].ID != "cp_0001" || list[2].Label != "prompt 2" {
		t.Fatalf("List = %+v", list)
	}
}

func TestBeginCleansLabels(t *testing.T) {
	e := newEnv(t)
	e.s.Begin("  fix\nthe   bug\t please  ")
	e.s.Begin(strings.Repeat("é", 500))
	list := e.s.List()
	if list[0].Label != "fix the bug please" {
		t.Fatalf("label = %q", list[0].Label)
	}
	if n := len([]rune(list[1].Label)); n != maxLabelRunes+1 { // +1 for the ellipsis
		t.Fatalf("long label has %d runes", n)
	}
}

func TestBeforeRecordsPreStateOncePerCheckpoint(t *testing.T) {
	e := newEnv(t)
	e.write("a.txt", "v0\n")
	cp := e.s.Begin("p")
	e.edit("a1", "a.txt", "v1\n")
	e.edit("a2", "a.txt", "v2\n") // second agent, same checkpoint: must not overwrite the pre-state
	e.edit("a1", "a.txt", "v3\n")

	list := e.s.List()
	if !reflect.DeepEqual(list[0].Files, []string{"a.txt"}) || !reflect.DeepEqual(list[0].Agents, []string{"a1", "a2"}) {
		t.Fatalf("info = %+v", list[0])
	}
	rep := mustRestore(t, e.s, cp, RestoreOpts{})
	if got := e.read("a.txt"); got != "v0\n" {
		t.Fatalf("a.txt = %q, want the original", got)
	}
	if !rep.OK() || !rep.Rewound {
		t.Fatalf("report = %+v", rep)
	}
}

func TestEditingBeforeAnyBeginUsesAnImplicitCheckpoint(t *testing.T) {
	e := newEnv(t)
	e.write("a.txt", "orig")
	e.edit("a1", "a.txt", "changed")
	list := e.s.List()
	if len(list) != 1 || list[0].Label != "session start" {
		t.Fatalf("List = %+v", list)
	}
	mustRestore(t, e.s, "", RestoreOpts{})
	if e.read("a.txt") != "orig" {
		t.Fatal("implicit checkpoint did not restore")
	}
}

func TestRestoreModifyCreateDelete(t *testing.T) {
	e := newEnv(t)
	e.write("mod.txt", "old mod")
	e.write("del.txt", "old del")
	e.write("untouched.txt", "same")
	cp := e.s.Begin("p")
	e.edit("a1", "mod.txt", "new mod")
	e.remove("a1", "del.txt")
	e.edit("a1", "sub/created.txt", "brand new")

	rep := mustRestore(t, e.s, cp, RestoreOpts{})
	if !rep.OK() {
		t.Fatalf("report: %s\n%+v", rep.Summary(), rep.Files)
	}
	if e.read("mod.txt") != "old mod" || e.read("del.txt") != "old del" || e.read("untouched.txt") != "same" {
		t.Fatal("contents not restored")
	}
	if e.exists("sub/created.txt") || e.exists("sub") {
		t.Fatal("created file and its directory should be gone")
	}
	if got := result(t, rep, "mod.txt").Action; got != ActionRestore {
		t.Fatalf("mod action = %s", got)
	}
	if got := result(t, rep, "del.txt").Action; got != ActionRecreate {
		t.Fatalf("del action = %s", got)
	}
	if got := result(t, rep, "sub/created.txt").Action; got != ActionDelete {
		t.Fatalf("created action = %s", got)
	}
	if rep.Count(OutcomeDone) < 3 {
		t.Fatalf("summary: %s", rep.Summary())
	}
}

func TestRestoreAcrossCheckpointsLandsOnTheOldestState(t *testing.T) {
	e := newEnv(t)
	e.write("a.txt", "v0\n")
	e.write("keep.txt", "keep\n")

	cp1 := e.s.Begin("first")
	e.edit("a1", "a.txt", "v1\n")
	e.edit("a1", "one.txt", "made in cp1\n")

	cp2 := e.s.Begin("second")
	e.edit("a2", "a.txt", "v2\n")
	e.edit("a2", "two.txt", "made in cp2\n")

	cp3 := e.s.Begin("third, no edits")

	// Rewinding to cp2 undoes cp2 and cp3 only.
	rep := mustRestore(t, e.s, cp2, RestoreOpts{})
	if !rep.OK() || !rep.Rewound {
		t.Fatalf("rewind cp2: %s", rep.Summary())
	}
	if e.read("a.txt") != "v1\n" || e.exists("two.txt") || e.read("one.txt") != "made in cp1\n" {
		t.Fatal("rewind to cp2 restored the wrong states")
	}
	list := e.s.List()
	if len(list) != 2 || list[1].ID != cp2 || len(list[1].Files) != 0 {
		t.Fatalf("after rewind to %s, List = %+v (cp3=%s must be gone, cp2 empty)", cp2, list, cp3)
	}
	if !reflect.DeepEqual(list[0].Files, []string{"a.txt", "one.txt"}) {
		t.Fatalf("cp1 must be intact: %+v", list[0])
	}

	// New work goes into a fresh checkpoint whose id is never a reused one.
	if id := e.s.Begin("again"); id != "cp_0004" {
		t.Fatalf("next id = %s, want cp_0004", id)
	}
	e.edit("a1", "a.txt", "v-again\n")

	// Rewinding to cp1 now spans cp1, cp2 (empty) and cp4: a.txt lands on v0.
	rep = mustRestore(t, e.s, cp1, RestoreOpts{})
	if !rep.OK() {
		t.Fatalf("rewind cp1: %s", rep.Summary())
	}
	if e.read("a.txt") != "v0\n" || e.exists("one.txt") || e.read("keep.txt") != "keep\n" {
		t.Fatal("rewind to cp1 did not land on the oldest state")
	}
}

func TestRestoreOldestWinsWhenSeveralCheckpointsTouchedTheFile(t *testing.T) {
	e := newEnv(t)
	e.write("f", "v0")
	cp1 := e.s.Begin("1")
	e.edit("a", "f", "v1")
	e.s.Begin("2")
	e.edit("a", "f", "v2")
	e.s.Begin("3")
	e.edit("a", "f", "v3")
	// Jump straight back three checkpoints.
	mustRestore(t, e.s, cp1, RestoreOpts{})
	if got := e.read("f"); got != "v0" {
		t.Fatalf("f = %q, want v0", got)
	}
}

func TestAbsentCreatedRestoredToAbsentRemovesCreatedDirectories(t *testing.T) {
	e := newEnv(t)
	cp := e.s.Begin("p")
	e.edit("a1", "new/deep/dir/file.txt", "x")
	e.edit("a1", "new/deep/other.txt", "y")

	rep := mustRestore(t, e.s, cp, RestoreOpts{})
	if !rep.OK() {
		t.Fatalf("%s\n%+v", rep.Summary(), rep.Files)
	}
	if e.exists("new") {
		t.Fatal("directories created by the edits should have been removed")
	}
	for _, d := range []string{"new/deep/dir", "new/deep", "new"} {
		if r := result(t, rep, d); r.Action != ActionRmdir || r.Outcome != OutcomeDone {
			t.Fatalf("%s: %+v", d, r)
		}
	}
}

func TestCreatedDirectoryWithForeignFilesIsLeftAlone(t *testing.T) {
	e := newEnv(t)
	cp := e.s.Begin("p")
	e.edit("a1", "out/gen/a.txt", "x")
	e.write("out/gen/user-notes.txt", "mine") // someone else put a file there

	rep := mustRestore(t, e.s, cp, RestoreOpts{})
	if !rep.OK() {
		t.Fatalf("%s", rep.Summary())
	}
	if e.exists("out/gen/a.txt") {
		t.Fatal("recorded file should be gone")
	}
	if e.read("out/gen/user-notes.txt") != "mine" {
		t.Fatal("foreign file must survive")
	}
	if r := result(t, rep, "out/gen"); r.Outcome != OutcomeUnchanged || !strings.Contains(r.Detail, "not empty") {
		t.Fatalf("out/gen result = %+v", r)
	}
}

func TestModeBitsAreRestored(t *testing.T) {
	e := newEnv(t)
	e.writeMode("run.sh", "#!/bin/sh\n", 0o775) // 0o775 is not what a umask of 022 would produce
	e.writeMode("mode-only.txt", "same content", 0o640)
	cp := e.s.Begin("p")
	if err := e.s.Before("a1", e.abs("run.sh")); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(e.abs("run.sh"), []byte("changed\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(e.abs("run.sh"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := e.s.Before("a1", e.abs("mode-only.txt")); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(e.abs("mode-only.txt"), 0o600); err != nil {
		t.Fatal(err)
	}

	diffs, err := e.s.Diff(cp)
	if err != nil {
		t.Fatal(err)
	}
	var modeOnly *FileDiff
	for i := range diffs {
		if diffs[i].Path == "mode-only.txt" {
			modeOnly = &diffs[i]
		}
	}
	if modeOnly == nil || modeOnly.Unified != "" || !strings.Contains(modeOnly.Note, "mode 0640 -> 0600") {
		t.Fatalf("mode-only diff = %+v", modeOnly)
	}

	rep := mustRestore(t, e.s, cp, RestoreOpts{})
	if !rep.OK() {
		t.Fatalf("%s", rep.Summary())
	}
	if got := e.mode("run.sh").Perm(); got != 0o775 {
		t.Fatalf("run.sh mode = %o, want 775 (the umask must not apply)", got)
	}
	if e.read("run.sh") != "#!/bin/sh\n" {
		t.Fatal("run.sh content not restored")
	}
	if got := e.mode("mode-only.txt").Perm(); got != 0o640 {
		t.Fatalf("mode-only.txt mode = %o, want 640", got)
	}
	if a := result(t, rep, "mode-only.txt").Action; a != ActionChmod {
		t.Fatalf("mode-only action = %s", a)
	}
}

func TestSymlinkReplacedByRegularFileIsRelinked(t *testing.T) {
	e := newEnv(t)
	e.write("real.txt", "target content")
	if err := os.Symlink("real.txt", e.abs("link")); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	cp := e.s.Begin("p")
	// A tool that writes atomically replaces the link itself with a regular file.
	if err := e.s.Before("a1", e.abs("link")); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(e.abs("link")); err != nil {
		t.Fatal(err)
	}
	e.write("link", "now a regular file")

	rep := mustRestore(t, e.s, cp, RestoreOpts{})
	if !rep.OK() {
		t.Fatalf("%s\n%+v", rep.Summary(), rep.Files)
	}
	target, err := os.Readlink(e.abs("link"))
	if err != nil || target != "real.txt" {
		t.Fatalf("link not restored: target=%q err=%v", target, err)
	}
	if e.read("real.txt") != "target content" {
		t.Fatal("link target content changed")
	}
	if a := result(t, rep, "link").Action; a != ActionRelink {
		t.Fatalf("action = %s", a)
	}
}

func TestWritingThroughASymlinkRestoresTheTarget(t *testing.T) {
	e := newEnv(t)
	e.write("real.txt", "original")
	if err := os.Symlink("real.txt", e.abs("link")); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	cp := e.s.Begin("p")
	e.edit("a1", "link", "written through the link") // follows the link

	if e.read("real.txt") != "written through the link" {
		t.Fatal("test setup: the write should have gone through the link")
	}
	mustRestore(t, e.s, cp, RestoreOpts{})
	if e.read("real.txt") != "original" {
		t.Fatal("the link's target was not restored")
	}
	if target, err := os.Readlink(e.abs("link")); err != nil || target != "real.txt" {
		t.Fatalf("link itself must be intact: %q %v", target, err)
	}
}

func TestDanglingSymlinkWriteCreatesAndRemovesTarget(t *testing.T) {
	e := newEnv(t)
	if err := os.Symlink("later.txt", e.abs("link")); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	cp := e.s.Begin("p")
	e.edit("a1", "link", "created via dangling link")
	if !e.exists("later.txt") {
		t.Fatal("test setup: target should exist now")
	}
	rep := mustRestore(t, e.s, cp, RestoreOpts{})
	if !rep.OK() {
		t.Fatalf("%s", rep.Summary())
	}
	if e.exists("later.txt") {
		t.Fatal("the file created through the dangling link should be gone")
	}
	if _, err := os.Readlink(e.abs("link")); err != nil {
		t.Fatalf("link must remain: %v", err)
	}
}

func TestSymlinkLoopDoesNotHang(t *testing.T) {
	e := newEnv(t)
	if err := os.Symlink("b", e.abs("a")); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	if err := os.Symlink("a", e.abs("b")); err != nil {
		t.Fatal(err)
	}
	e.s.Begin("p")
	done := make(chan error, 1)
	go func() { done <- e.s.Before("a1", e.abs("a")) }()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Before hung on a symlink loop")
	}
}

func TestDirectoryStates(t *testing.T) {
	e := newEnv(t)
	if err := os.MkdirAll(e.abs("keepdir"), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(e.abs("keepdir"), 0o750); err != nil {
		t.Fatal(err)
	}
	cp := e.s.Begin("p")
	// A tool removes an existing (empty) directory and creates a new one.
	if err := e.s.Before("a1", e.abs("keepdir")); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(e.abs("keepdir")); err != nil {
		t.Fatal(err)
	}
	if err := e.s.Before("a1", e.abs("madedir")); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(e.abs("madedir"), 0o755); err != nil {
		t.Fatal(err)
	}

	rep := mustRestore(t, e.s, cp, RestoreOpts{})
	if !rep.OK() {
		t.Fatalf("%s\n%+v", rep.Summary(), rep.Files)
	}
	if e.exists("madedir") {
		t.Fatal("directory created by the tool should be removed")
	}
	if !e.exists("keepdir") || e.mode("keepdir").Perm() != 0o750 {
		t.Fatalf("deleted directory should be back with its mode; exists=%v", e.exists("keepdir"))
	}
}

func TestFileReplacedByEmptyDirectoryIsRestored(t *testing.T) {
	e := newEnv(t)
	e.write("thing", "was a file")
	cp := e.s.Begin("p")
	if err := e.s.Before("a1", e.abs("thing")); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(e.abs("thing")); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(e.abs("thing"), 0o755); err != nil {
		t.Fatal(err)
	}
	rep := mustRestore(t, e.s, cp, RestoreOpts{})
	if !rep.OK() || e.read("thing") != "was a file" {
		t.Fatalf("%s\n%+v", rep.Summary(), rep.Files)
	}
}

func TestPopulatedDirectoryInTheWayIsNeverRemoved(t *testing.T) {
	e := newEnv(t)
	e.write("thing", "was a file")
	cp := e.s.Begin("p")
	if err := e.s.Before("a1", e.abs("thing")); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(e.abs("thing")); err != nil {
		t.Fatal(err)
	}
	e.write("thing/precious.txt", "someone's data")
	rep := mustRestore(t, e.s, cp, RestoreOpts{Force: true})
	if r := result(t, rep, "thing"); r.Outcome != OutcomeFailed || !strings.Contains(r.Detail, "directory with contents") {
		t.Fatalf("thing = %+v", r)
	}
	if e.read("thing/precious.txt") != "someone's data" {
		t.Fatal("data inside the directory must survive")
	}
}

func TestPersistenceAcrossNewOnTheSameDir(t *testing.T) {
	e := newEnv(t)
	e.write("a.txt", "v0")
	e.write("gone.txt", "will be deleted")
	cp1 := e.s.Begin("first")
	e.edit("a1", "a.txt", "v1")
	e.remove("a2", "gone.txt")
	e.edit("a2", "new.txt", "n")
	cp2 := e.s.Begin("second")
	before := e.s.List()

	s2 := e.reopen() // "restart"
	if got := s2.List(); !sameInfos(got, before) {
		t.Fatalf("List after reopen differs:\n got %+v\nwant %+v", got, before)
	}
	if w := s2.Warnings(); len(w) != 0 {
		t.Fatalf("warnings: %v", w)
	}
	// Ids continue where they left off.
	if id := s2.Begin("third"); id != "cp_0003" {
		t.Fatalf("id after reopen = %s", id)
	}
	// And rewind works on the reopened store.
	rep := mustRestore(t, s2, cp1, RestoreOpts{})
	if !rep.OK() {
		t.Fatalf("%s", rep.Summary())
	}
	if e.read("a.txt") != "v0" || e.read("gone.txt") != "will be deleted" || e.exists("new.txt") {
		t.Fatal("restore after reopen did not rewind")
	}
	_ = cp2
}

func TestIDsAreNotReusedAcrossRestartsAfterARewind(t *testing.T) {
	e := newEnv(t)
	e.write("f", "0")
	cp1 := e.s.Begin("1")
	e.edit("a", "f", "1")
	e.s.Begin("2")
	e.s.Begin("3")
	mustRestore(t, e.s, cp1, RestoreOpts{}) // drops cp2 and cp3
	s2 := e.reopen()
	if id := s2.Begin("next"); id != "cp_0004" {
		t.Fatalf("id = %s: ids must never be reused, even across a restart", id)
	}
}

func TestPathsOutsideTheRootAreRecordedAbsolute(t *testing.T) {
	e := newEnv(t)
	outside := filepath.Join(e.base, "elsewhere")
	if err := os.MkdirAll(outside, 0o755); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(outside, "f.txt")
	if err := os.WriteFile(target, []byte("outside-orig"), 0o644); err != nil {
		t.Fatal(err)
	}
	e.write("inside.txt", "inside-orig")
	cp := e.s.Begin("p")
	e.edit("a1", target, "outside-new")
	// A relative path escaping the root is resolved against the root, then recorded absolute.
	if err := e.s.Before("a1", "../elsewhere/g.txt"); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(outside, "g.txt"), []byte("created outside"), 0o644); err != nil {
		t.Fatal(err)
	}
	e.edit("a1", "inside.txt", "inside-new")

	info := e.s.List()[0]
	realOutside, err := filepath.EvalSymlinks(outside) // keys are recorded with symlinks resolved
	if err != nil {
		t.Fatal(err)
	}
	wantOutside := filepath.ToSlash(filepath.Join(realOutside, "f.txt"))
	wantG := filepath.ToSlash(filepath.Join(realOutside, "g.txt"))
	if !reflect.DeepEqual(info.Files, sortedCopy([]string{"inside.txt", wantOutside, wantG})) {
		t.Fatalf("files = %v", info.Files)
	}
	rep := mustRestore(t, e.s, cp, RestoreOpts{})
	if !rep.OK() {
		t.Fatalf("%s\n%+v", rep.Summary(), rep.Files)
	}
	if b, _ := os.ReadFile(target); string(b) != "outside-orig" {
		t.Fatalf("outside file = %q", b)
	}
	if _, err := os.Stat(filepath.Join(outside, "g.txt")); err == nil {
		t.Fatal("file created outside the root should be removed")
	}
	if e.read("inside.txt") != "inside-orig" {
		t.Fatal("inside file not restored")
	}
}

// sameInfos compares listings, using Equal for times (a JSON round trip
// preserves the instant but not necessarily the Location pointer).
func sameInfos(a, b []Info) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		x, y := a[i], b[i]
		if x.ID != y.ID || x.Label != y.Label || !x.Time.Equal(y.Time) ||
			!reflect.DeepEqual(x.Files, y.Files) || !reflect.DeepEqual(x.Agents, y.Agents) || !reflect.DeepEqual(x.Unsaved, y.Unsaved) {
			return false
		}
	}
	return true
}

func sortedCopy(in []string) []string {
	out := append([]string(nil), in...)
	sort.Strings(out)
	return out
}

func TestRelativePathsResolveAgainstTheRoot(t *testing.T) {
	e := newEnv(t)
	e.write("dir/a.txt", "orig")
	cp := e.s.Begin("p")
	if err := e.s.Before("a1", "dir/a.txt"); err != nil { // relative: resolved against the root, not the process cwd
		t.Fatal(err)
	}
	if err := os.WriteFile(e.abs("dir/a.txt"), []byte("changed"), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := e.s.List()[0].Files; !reflect.DeepEqual(got, []string{"dir/a.txt"}) {
		t.Fatalf("files = %v", got)
	}
	mustRestore(t, e.s, cp, RestoreOpts{})
	if e.read("dir/a.txt") != "orig" {
		t.Fatal("not restored")
	}
}

func TestTwoSpellingsOfOnePathAreOneRecord(t *testing.T) {
	e := newEnv(t)
	if err := os.MkdirAll(e.abs("real"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("real", e.abs("alias")); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	e.write("real/f.txt", "orig")
	cp := e.s.Begin("p")
	e.edit("a1", "real/f.txt", "v1")
	e.edit("a2", "alias/f.txt", "v2") // same file through a symlinked directory
	info := e.s.List()[0]
	if !reflect.DeepEqual(info.Files, []string{"real/f.txt"}) {
		t.Fatalf("files = %v, want a single record", info.Files)
	}
	mustRestore(t, e.s, cp, RestoreOpts{})
	if e.read("real/f.txt") != "orig" {
		t.Fatal("not restored to the original")
	}
}

func TestBeforeRejectsBadPaths(t *testing.T) {
	e := newEnv(t)
	if err := e.s.Before("a", ""); err == nil {
		t.Fatal("empty path should be an error")
	}
	if err := e.s.Before("a", "x\x00y"); err == nil {
		t.Fatal("NUL in path should be an error")
	}
}

func TestUnknownCheckpointIDs(t *testing.T) {
	e := newEnv(t)
	if _, err := e.s.Restore("", RestoreOpts{}); !errors.Is(err, ErrUnknownCheckpoint) {
		t.Fatalf("empty store: err = %v", err)
	}
	e.s.Begin("1")
	e.s.Begin("2")
	for _, id := range []string{"cp_0099", "nope", "99"} {
		if _, err := e.s.Restore(id, RestoreOpts{}); !errors.Is(err, ErrUnknownCheckpoint) {
			t.Fatalf("Restore(%q): err = %v", id, err)
		}
		if _, err := e.s.Diff(id); !errors.Is(err, ErrUnknownCheckpoint) {
			t.Fatalf("Diff(%q): err = %v", id, err)
		}
	}
	// Friendly spellings of an existing id work.
	for _, id := range []string{"cp_0002", "cp_2", "2", "0002", " cp_0002 "} {
		if _, err := e.s.Restore(id, RestoreOpts{DryRun: true}); err != nil {
			t.Fatalf("Restore(%q): %v", id, err)
		}
	}
}

func TestCorruptCheckpointFilesAreSkippedWithAWarning(t *testing.T) {
	e := newEnv(t)
	e.write("a.txt", "orig")
	e.s.Begin("good")
	e.edit("a1", "a.txt", "changed")
	if err := os.WriteFile(filepath.Join(e.dir, "cp_0002.json"), []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(e.dir, "cp_0003.json"), []byte(`{"v":1,"id":"cp_0009","seq":9}`), 0o600); err != nil {
		t.Fatal(err)
	}
	s2 := e.reopen()
	if got := s2.List(); len(got) != 1 || got[0].ID != "cp_0001" {
		t.Fatalf("List = %+v", got)
	}
	if len(s2.Warnings()) != 2 {
		t.Fatalf("warnings = %v", s2.Warnings())
	}
	// A skipped file keeps its number: the next id must not overwrite it.
	if id := s2.Begin("x"); id != "cp_0004" {
		t.Fatalf("id = %s, want cp_0004 (cp_0002 and cp_0003 are taken by unreadable files)", id)
	}
}

func TestManifestRecordsCannotEscapeTheRoot(t *testing.T) {
	e := newEnv(t)
	e.s.Begin("p")
	doc := `{"v":1,"id":"cp_0001","seq":1,"label":"evil","time":"2026-01-01T00:00:00Z","files":[` +
		`{"path":"../../etc/passwd","pre":{"kind":"absent"}},` +
		`{"path":"ok.txt","pre":{"kind":"absent"}}]}`
	if err := os.WriteFile(filepath.Join(e.dir, "cp_0001.json"), []byte(doc), 0o600); err != nil {
		t.Fatal(err)
	}
	s2 := e.reopen()
	if got := s2.List()[0].Files; !reflect.DeepEqual(got, []string{"ok.txt"}) {
		t.Fatalf("files = %v; the escaping record must be dropped", got)
	}
	if len(s2.Warnings()) != 1 {
		t.Fatalf("warnings = %v", s2.Warnings())
	}
}

func TestBeforeFailsWhenTheBlobStoreFailsAndRetries(t *testing.T) {
	base := t.TempDir()
	root := filepath.Join(base, "proj")
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatal(err)
	}
	fb := &flakyBlobs{Blobs: events.NewMemBlobs()}
	s, err := New(filepath.Join(base, "cp"), fb, root)
	if err != nil {
		t.Fatal(err)
	}
	f := filepath.Join(root, "a.txt")
	if err := os.WriteFile(f, []byte("orig"), 0o644); err != nil {
		t.Fatal(err)
	}
	s.Begin("p")
	fb.setFailPut(true)
	if err := s.Before("a1", f); err == nil || !strings.Contains(err.Error(), "disk full") {
		t.Fatalf("err = %v; a harness failure must stop the edit", err)
	}
	if got := s.List()[0].Files; len(got) != 0 {
		t.Fatalf("failed snapshot must not leave a record: %v", got)
	}
	fb.setFailPut(false)
	if err := s.Before("a1", f); err != nil {
		t.Fatalf("retry: %v", err)
	}
	if got := s.List()[0].Files; !reflect.DeepEqual(got, []string{"a.txt"}) {
		t.Fatalf("files = %v", got)
	}
}

func TestBeforeFailsWhenTheManifestCannotBeWritten(t *testing.T) {
	e := newEnv(t)
	e.write("a.txt", "orig")
	e.s.Begin("p")
	// Make the manifest path a directory so the atomic rename onto it fails.
	target := filepath.Join(e.dir, "cp_0001.json")
	if err := os.Remove(target); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(target, "x"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := e.s.Before("a1", e.abs("a.txt")); err == nil {
		t.Fatal("Before must fail when the snapshot cannot be made durable")
	}
	if got := e.s.List()[0].Files; len(got) != 0 {
		t.Fatalf("record must be rolled back: %v", got)
	}
}

// The core concurrency guarantee: when Before returns to any agent, the
// original content is already saved, even if that agent raced another one that
// is still busy taking the snapshot.
func TestConcurrentBeforeAcrossManyAgents(t *testing.T) {
	base := t.TempDir()
	root := filepath.Join(base, "proj")
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatal(err)
	}
	fb := &flakyBlobs{Blobs: events.NewMemBlobs(), delay: 5 * time.Millisecond} // slow snapshots widen the race window
	s, err := New(filepath.Join(base, "cp"), fb, root)
	if err != nil {
		t.Fatal(err)
	}
	const nFiles, nAgents = 12, 24
	orig := map[string]string{}
	for i := 0; i < nFiles; i++ {
		name := fmt.Sprintf("f%02d.txt", i)
		orig[name] = fmt.Sprintf("original %d\n", i)
		if err := os.WriteFile(filepath.Join(root, name), []byte(orig[name]), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	cp := s.Begin("swarm phase")

	var wg sync.WaitGroup
	for a := 0; a < nAgents; a++ {
		wg.Add(1)
		go func(a int) {
			defer wg.Done()
			agent := fmt.Sprintf("agent-%02d", a)
			// Each agent walks the files in its own rotation so they collide constantly.
			for k := 0; k < nFiles; k++ {
				name := fmt.Sprintf("f%02d.txt", (k+a)%nFiles)
				p := filepath.Join(root, name)
				if err := s.Before(agent, p); err != nil {
					t.Errorf("Before: %v", err)
					return
				}
				// The write happens strictly after Before returned. If Before had
				// returned early, this would corrupt a snapshot still being taken.
				if err := os.WriteFile(p, []byte("written by "+agent), 0o644); err != nil {
					t.Errorf("write: %v", err)
					return
				}
				s.After(agent, p)
			}
		}(a)
	}
	// Readers and a Begin-free checkpoint listing run alongside the writers.
	stop := make(chan struct{})
	var rw sync.WaitGroup
	rw.Add(1)
	go func() {
		defer rw.Done()
		for {
			select {
			case <-stop:
				return
			default:
				_ = s.List()
				if _, err := s.Diff(cp); err != nil {
					t.Errorf("Diff: %v", err)
					return
				}
			}
		}
	}()
	wg.Wait()
	close(stop)
	rw.Wait()

	info := s.List()[0]
	if len(info.Files) != nFiles {
		t.Fatalf("%d files recorded, want %d (exactly one record per path)", len(info.Files), nFiles)
	}
	if len(info.Agents) != nAgents {
		t.Fatalf("%d agents recorded, want %d", len(info.Agents), nAgents)
	}
	rep := mustRestore(t, s, cp, RestoreOpts{})
	if !rep.OK() {
		t.Fatalf("%s\n%+v", rep.Summary(), rep.Files)
	}
	for name, want := range orig {
		b, err := os.ReadFile(filepath.Join(root, name))
		if err != nil || string(b) != want {
			t.Fatalf("%s = %q (%v), want the original %q", name, b, err, want)
		}
	}
}

func TestConcurrentBeginBeforeAndList(t *testing.T) {
	e := newEnv(t)
	for i := 0; i < 8; i++ {
		e.write(fmt.Sprintf("f%d", i), "orig")
	}
	var wg sync.WaitGroup
	for g := 0; g < 8; g++ {
		wg.Add(1)
		go func(g int) {
			defer wg.Done()
			for i := 0; i < 20; i++ {
				if i%5 == 0 {
					e.s.Begin(fmt.Sprintf("g%d-%d", g, i))
				}
				if err := e.s.Before(fmt.Sprintf("a%d", g), e.abs(fmt.Sprintf("f%d", (g+i)%8))); err != nil {
					t.Errorf("Before: %v", err)
					return
				}
				_ = e.s.List()
			}
		}(g)
	}
	wg.Wait()
	ids := map[string]bool{}
	for _, info := range e.s.List() {
		if ids[info.ID] {
			t.Fatalf("duplicate checkpoint id %s", info.ID)
		}
		ids[info.ID] = true
	}
	if want := 8 * 4; len(ids) != want { // 8 goroutines, a Begin at i = 0, 5, 10, 15
		t.Fatalf("%d checkpoints, want %d", len(ids), want)
	}
}

func TestAfterOnAnUnrecordedPathIsHarmless(t *testing.T) {
	e := newEnv(t)
	e.s.After("a1", e.abs("never-recorded.txt")) // no checkpoint yet, no record: must be a no-op
	e.s.Begin("p")
	e.s.After("a1", e.abs("never-recorded.txt"))
	e.s.After("a1", "")
	if got := e.s.List()[0].Files; len(got) != 0 {
		t.Fatalf("After must not create records: %v", got)
	}
}

func TestNewWithoutABlobStoreKeepsItsOwnUnderTheDirectory(t *testing.T) {
	base := t.TempDir()
	root := filepath.Join(base, "proj")
	dir := filepath.Join(base, "cp")
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatal(err)
	}
	f := filepath.Join(root, "a.txt")
	if err := os.WriteFile(f, []byte("orig"), 0o644); err != nil {
		t.Fatal(err)
	}
	s, err := New(dir, nil, root)
	if err != nil {
		t.Fatal(err)
	}
	cp := s.Begin("p")
	if err := s.Before("a1", f); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(f, []byte("changed"), 0o644); err != nil {
		t.Fatal(err)
	}
	// A fresh Store (a restart) finds the content again because it lives on disk.
	s2, err := New(dir, nil, root)
	if err != nil {
		t.Fatal(err)
	}
	if rep := mustRestore(t, s2, cp, RestoreOpts{}); !rep.OK() {
		t.Fatalf("%s", rep.Summary())
	}
	if b, _ := os.ReadFile(f); string(b) != "orig" {
		t.Fatalf("a.txt = %q", b)
	}
}

func TestNewValidatesItsArguments(t *testing.T) {
	if _, err := New("", events.NewMemBlobs(), t.TempDir()); err == nil {
		t.Fatal("an empty dir must be an error")
	}
	// An empty root means the working directory: recorded paths are relative to it.
	wd := t.TempDir()
	t.Chdir(wd)
	s, err := New(filepath.Join(t.TempDir(), "cp"), events.NewMemBlobs(), "")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile("f.txt", []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	s.Begin("p")
	if err := s.Before("a", "f.txt"); err != nil {
		t.Fatal(err)
	}
	if got := s.List()[0].Files; !reflect.DeepEqual(got, []string{"f.txt"}) {
		t.Fatalf("files = %v", got)
	}
	// dir may not be creatable.
	file := filepath.Join(t.TempDir(), "afile")
	os.WriteFile(file, []byte("x"), 0o644)
	if _, err := New(filepath.Join(file, "sub"), events.NewMemBlobs(), wd); err == nil {
		t.Fatal("a dir under a regular file must be an error")
	}
}
