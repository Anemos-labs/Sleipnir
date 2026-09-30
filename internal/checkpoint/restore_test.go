package checkpoint

import (
	"bytes"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/reee344/sleipnir/internal/events"
)

func TestPartialFailureIsReportedPerFileAndRetryable(t *testing.T) {
	e := newEnv(t)
	e.write("a.txt", "a-orig")
	e.write("sub/x.txt", "x-orig")
	e.write("c.txt", "c-orig")
	cp := e.s.Begin("p")
	e.edit("a1", "a.txt", "a-new")
	e.edit("a1", "sub/x.txt", "x-new")
	e.edit("a1", "c.txt", "c-new")
	// Sabotage one restore: the directory holding x.txt is replaced by a plain
	// file, so recreating sub/x.txt cannot work (and this fails for root too).
	if err := os.RemoveAll(e.abs("sub")); err != nil {
		t.Fatal(err)
	}
	e.write("sub", "i am a file now")

	rep, err := e.s.Restore(cp, RestoreOpts{})
	if err != nil {
		t.Fatalf("per-file failures must not be a Go error: %v", err)
	}
	if rep.OK() || rep.Err() == nil {
		t.Fatalf("report should not be OK: %s", rep.Summary())
	}
	if rep.Count(OutcomeDone) != 2 || rep.Count(OutcomeFailed) != 1 {
		t.Fatalf("done=%d failed=%d\n%+v", rep.Count(OutcomeDone), rep.Count(OutcomeFailed), rep.Files)
	}
	if r := result(t, rep, "sub/x.txt"); r.Outcome != OutcomeFailed || !strings.Contains(r.Detail, "parent directory") {
		t.Fatalf("sub/x.txt = %+v", r)
	}
	if e.read("a.txt") != "a-orig" || e.read("c.txt") != "c-orig" {
		t.Fatal("the other files must still be restored")
	}
	if rep.Rewound {
		t.Fatal("an incomplete rewind must not claim to have rewound the history")
	}
	if !strings.Contains(rep.Err().Error(), "sub/x.txt") || !strings.Contains(rep.Summary(), "1 failed") {
		t.Fatalf("Err=%v Summary=%s", rep.Err(), rep.Summary())
	}
	// Only the failed file keeps its record.
	if got := e.s.List()[0].Files; !reflect.DeepEqual(got, []string{"sub/x.txt"}) {
		t.Fatalf("files still recorded = %v", got)
	}

	// Fix the obstruction and retry: the rest of the rewind completes.
	if err := os.Remove(e.abs("sub")); err != nil {
		t.Fatal(err)
	}
	rep = mustRestore(t, e.s, cp, RestoreOpts{})
	if !rep.OK() || !rep.Rewound {
		t.Fatalf("retry: %s", rep.Summary())
	}
	if e.read("sub/x.txt") != "x-orig" {
		t.Fatal("retry did not restore sub/x.txt")
	}
}

func TestMissingBlobFailsThatFileAndLeavesItUntouched(t *testing.T) {
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
	write := func(name, content string) {
		if err := os.WriteFile(filepath.Join(root, name), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("good.txt", "good-orig")
	write("bad.txt", "bad-orig")
	cp := s.Begin("p")
	for _, n := range []string{"good.txt", "bad.txt"} {
		if err := s.Before("a1", filepath.Join(root, n)); err != nil {
			t.Fatal(err)
		}
		write(n, "changed")
	}
	// Lose bad.txt's saved content.
	s.mu.Lock()
	h := s.cps[0].index["bad.txt"].Pre.Blob
	s.mu.Unlock()
	fb.failGetFor(h)

	rep := mustRestore(t, s, cp, RestoreOpts{})
	if r := result(t, rep, "bad.txt"); r.Outcome != OutcomeFailed || !strings.Contains(r.Detail, "missing from the blob store") {
		t.Fatalf("bad.txt = %+v", r)
	}
	if b, _ := os.ReadFile(filepath.Join(root, "bad.txt")); string(b) != "changed" {
		t.Fatalf("a file whose backup is unavailable must be left as it is, got %q", b)
	}
	if b, _ := os.ReadFile(filepath.Join(root, "good.txt")); string(b) != "good-orig" {
		t.Fatal("good.txt should be restored")
	}
}

func TestCorruptBlobIsDetected(t *testing.T) {
	e := newEnv(t)
	e.write("a.txt", "orig")
	cp := e.s.Begin("p")
	e.edit("a1", "a.txt", "changed")
	// Point the record at a blob with different content.
	other, _ := e.blobs.Put([]byte("something else"))
	e.s.mu.Lock()
	e.s.cps[0].index["a.txt"].Pre.Blob = other
	e.s.mu.Unlock()
	rep := mustRestore(t, e.s, cp, RestoreOpts{})
	if r := result(t, rep, "a.txt"); r.Outcome != OutcomeFailed || !strings.Contains(r.Detail, "checksum") {
		t.Fatalf("a.txt = %+v", r)
	}
	if e.read("a.txt") != "changed" {
		t.Fatal("corrupt backup must not be written")
	}
}

func TestHugeFilesAreRecordedAsUnsavedAndRefusedOnRestore(t *testing.T) {
	e := newEnv(t)
	big := e.abs("big.bin")
	f, err := os.Create(big)
	if err != nil {
		t.Fatal(err)
	}
	if err := f.Truncate(MaxFileBytes + 1); err != nil { // sparse: cheap to create
		f.Close()
		t.Skipf("cannot create a sparse file here: %v", err)
	}
	f.Close()
	e.write("small.txt", "small-orig")

	cp := e.s.Begin("p")
	if err := e.s.Before("a1", big); err != nil {
		t.Fatalf("a too-large file must not block the edit: %v", err)
	}
	e.edit("a1", "small.txt", "small-new")
	if err := os.WriteFile(big, []byte("tiny now"), 0o644); err != nil {
		t.Fatal(err)
	}

	info := e.s.List()[0]
	if !reflect.DeepEqual(info.Unsaved, []string{"big.bin"}) {
		t.Fatalf("Unsaved = %v", info.Unsaved)
	}
	diffs, err := e.s.Diff(cp)
	if err != nil {
		t.Fatal(err)
	}
	var bigDiff FileDiff
	for _, d := range diffs {
		if d.Path == "big.bin" {
			bigDiff = d
		}
	}
	if bigDiff.Path == "" || !strings.Contains(bigDiff.Note, "not saved") || bigDiff.Unified != "" {
		t.Fatalf("big.bin diff = %+v", bigDiff)
	}

	rep := mustRestore(t, e.s, cp, RestoreOpts{})
	r := result(t, rep, "big.bin")
	if r.Outcome != OutcomeUnrestorable || !strings.Contains(r.Detail, "too large") {
		t.Fatalf("big.bin = %+v", r)
	}
	if b, _ := os.ReadFile(big); string(b) != "tiny now" {
		t.Fatal("an unrestorable file must be left alone")
	}
	if e.read("small.txt") != "small-orig" {
		t.Fatal("other files are still restored")
	}
	if rep.Rewound || rep.OK() {
		t.Fatalf("report should be incomplete: %s", rep.Summary())
	}
	// Force does not make the impossible possible.
	rep = mustRestore(t, e.s, cp, RestoreOpts{Force: true})
	if r := result(t, rep, "big.bin"); r.Outcome != OutcomeUnrestorable {
		t.Fatalf("with Force: %+v", r)
	}
}

func TestUnreadableFilesAreRecordedAsUnsaved(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root can read anything, so nothing is unreadable")
	}
	e := newEnv(t)
	e.writeMode("secret.txt", "hidden", 0o000)
	e.s.Begin("p")
	if err := e.s.Before("a1", e.abs("secret.txt")); err != nil {
		t.Fatalf("Before must not fail on an unreadable file: %v", err)
	}
	info := e.s.List()[0]
	if !reflect.DeepEqual(info.Unsaved, []string{"secret.txt"}) {
		t.Fatalf("Unsaved = %v", info.Unsaved)
	}
	rep := mustRestore(t, e.s, "", RestoreOpts{})
	if r := result(t, rep, "secret.txt"); r.Outcome != OutcomeUnchanged && r.Outcome != OutcomeUnrestorable {
		t.Fatalf("secret.txt = %+v", r)
	}
}

func TestBinaryFilesRoundTripAndDiffAsBinary(t *testing.T) {
	e := newEnv(t)
	orig := append([]byte("\x89PNG\r\n\x1a\n\x00\x00\x00\rIHDR"), bytes.Repeat([]byte{0, 1, 2, 255}, 100)...)
	if err := os.WriteFile(e.abs("img.png"), orig, 0o644); err != nil {
		t.Fatal(err)
	}
	cp := e.s.Begin("p")
	if err := e.s.Before("a1", e.abs("img.png")); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(e.abs("img.png"), append(orig[:20:20], 9, 9, 9), 0o644); err != nil {
		t.Fatal(err)
	}
	diffs, _ := e.s.Diff(cp)
	if len(diffs) != 1 || !diffs[0].Binary || diffs[0].Unified != "" || !strings.Contains(diffs[0].Note, "binary") {
		t.Fatalf("diffs = %+v", diffs)
	}
	if diffs[0].OldSize != int64(len(orig)) || diffs[0].NewSize != 23 {
		t.Fatalf("sizes = %d -> %d", diffs[0].OldSize, diffs[0].NewSize)
	}
	mustRestore(t, e.s, cp, RestoreOpts{})
	got, _ := os.ReadFile(e.abs("img.png"))
	if !bytes.Equal(got, orig) {
		t.Fatal("binary content not restored byte for byte")
	}
}

func TestDiffReportsAddedModifiedDeleted(t *testing.T) {
	e := newEnv(t)
	e.write("mod.txt", "one\ntwo\nthree\n")
	e.write("del.txt", "bye\n")
	e.write("same.txt", "unchanged\n")
	cp := e.s.Begin("p")
	e.edit("a1", "mod.txt", "one\nTWO\nthree\n")
	e.remove("a2", "del.txt")
	e.edit("a1", "add.txt", "hello\n")
	if err := e.s.Before("a1", e.abs("same.txt")); err != nil { // touched but rewritten identically
		t.Fatal(err)
	}
	if err := os.WriteFile(e.abs("same.txt"), []byte("unchanged\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	diffs, err := e.s.Diff(cp)
	if err != nil {
		t.Fatal(err)
	}
	byPath := map[string]FileDiff{}
	var order []string
	for _, d := range diffs {
		byPath[d.Path] = d
		order = append(order, d.Path)
	}
	if !reflect.DeepEqual(order, []string{"add.txt", "del.txt", "mod.txt"}) {
		t.Fatalf("paths (sorted, unchanged omitted) = %v", order)
	}
	if d := byPath["mod.txt"]; d.Status != DiffModified || d.Added != 1 || d.Removed != 1 ||
		d.Unified != "--- a/mod.txt\n+++ b/mod.txt\n@@ -1,3 +1,3 @@\n one\n-two\n+TWO\n three\n" || !reflect.DeepEqual(d.Agents, []string{"a1"}) {
		t.Fatalf("mod.txt = %+v", d)
	}
	if d := byPath["add.txt"]; d.Status != DiffAdded || d.Unified != "--- /dev/null\n+++ b/add.txt\n@@ -0,0 +1 @@\n+hello\n" {
		t.Fatalf("add.txt = %+v", d)
	}
	if d := byPath["del.txt"]; d.Status != DiffDeleted || d.Unified != "--- a/del.txt\n+++ /dev/null\n@@ -1 +0,0 @@\n-bye\n" || !reflect.DeepEqual(d.Agents, []string{"a2"}) {
		t.Fatalf("del.txt = %+v", d)
	}
}

func TestDiffDescribesSymlinkAndTypeChanges(t *testing.T) {
	e := newEnv(t)
	e.write("t1", "x")
	e.write("t2", "y")
	if err := os.Symlink("t1", e.abs("link")); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	e.write("plain", "was a file")
	cp := e.s.Begin("p")
	if err := e.s.Before("a1", e.abs("link")); err != nil {
		t.Fatal(err)
	}
	os.Remove(e.abs("link"))
	if err := os.Symlink("t2", e.abs("link")); err != nil {
		t.Fatal(err)
	}
	if err := e.s.Before("a1", e.abs("plain")); err != nil {
		t.Fatal(err)
	}
	os.Remove(e.abs("plain"))
	if err := os.Symlink("t1", e.abs("plain")); err != nil {
		t.Fatal(err)
	}
	diffs, _ := e.s.Diff(cp)
	notes := map[string]string{}
	for _, d := range diffs {
		notes[d.Path] = d.Note
	}
	if !strings.Contains(notes["link"], "symlink target changed: t1 -> t2") {
		t.Fatalf("link note = %q", notes["link"])
	}
	if !strings.Contains(notes["plain"], "type changed: file -> symlink to t1") {
		t.Fatalf("plain note = %q", notes["plain"])
	}
}

// --- conflicts -------------------------------------------------------------

func TestConflictWhenSomeoneEditedAfterTheLastRecordedWriteWithAfter(t *testing.T) {
	e := newEnv(t)
	e.write("f.txt", "orig")
	cp := e.s.Begin("p")
	e.editAfter("a1", "f.txt", "agent version")
	// A human saves the file in their editor: no Before, no After.
	if err := os.WriteFile(e.abs("f.txt"), []byte("human edit"), 0o644); err != nil {
		t.Fatal(err)
	}

	rep := mustRestore(t, e.s, cp, RestoreOpts{})
	r := result(t, rep, "f.txt")
	if r.Outcome != OutcomeConflict || !strings.Contains(r.Detail, "someone else") {
		t.Fatalf("f.txt = %+v", r)
	}
	if e.read("f.txt") != "human edit" {
		t.Fatal("a conflicting file must not be touched")
	}
	if rep.OK() || rep.Rewound || !strings.Contains(rep.Summary(), "1 in conflict") {
		t.Fatalf("report = %s", rep.Summary())
	}
	if got := e.s.List()[0].Files; !reflect.DeepEqual(got, []string{"f.txt"}) {
		t.Fatal("the conflicting file must keep its record so Force can be used later")
	}

	rep = mustRestore(t, e.s, cp, RestoreOpts{Force: true})
	if !rep.OK() || !rep.Rewound || e.read("f.txt") != "orig" {
		t.Fatalf("Force: %s; f.txt=%q", rep.Summary(), e.read("f.txt"))
	}
}

func TestNoConflictWhenTheFileIsExactlyWhatTheAgentLeft(t *testing.T) {
	e := newEnv(t)
	e.write("f.txt", "orig")
	cp := e.s.Begin("p")
	e.editAfter("a1", "f.txt", "agent version")
	// Even with the mtime far in the future, a matching fingerprint means no one else touched it.
	future := time.Now().Add(time.Hour)
	if err := os.Chtimes(e.abs("f.txt"), future, future); err != nil {
		t.Fatal(err)
	}
	rep := mustRestore(t, e.s, cp, RestoreOpts{})
	if !rep.OK() || e.read("f.txt") != "orig" {
		t.Fatalf("report = %s", rep.Summary())
	}
}

func TestConflictFallsBackToMtimeWhenAfterWasNotCalled(t *testing.T) {
	e := newEnv(t)
	e.write("f.txt", "orig")
	cp := e.s.Begin("p")
	e.edit("a1", "f.txt", "agent version") // no After
	// Someone edits it later: same time as far as the store is concerned, but the mtime says so.
	if err := os.WriteFile(e.abs("f.txt"), []byte("human edit"), 0o644); err != nil {
		t.Fatal(err)
	}
	later := time.Now().Add(time.Hour)
	if err := os.Chtimes(e.abs("f.txt"), later, later); err != nil {
		t.Fatal(err)
	}
	rep := mustRestore(t, e.s, cp, RestoreOpts{})
	if r := result(t, rep, "f.txt"); r.Outcome != OutcomeConflict || !strings.Contains(r.Detail, "modification time") {
		t.Fatalf("f.txt = %+v", r)
	}
	if e.read("f.txt") != "human edit" {
		t.Fatal("conflicting file must be untouched")
	}
	if rep = mustRestore(t, e.s, cp, RestoreOpts{Force: true}); e.read("f.txt") != "orig" || !rep.OK() {
		t.Fatalf("Force: %s", rep.Summary())
	}
}

func TestEditingAgainAfterAnAfterInvalidatesTheOldFingerprint(t *testing.T) {
	e := newEnv(t)
	e.write("f.txt", "orig")
	cp := e.s.Begin("p")
	e.editAfter("a1", "f.txt", "first")
	e.edit("a2", "f.txt", "second") // announced and written, but After not (yet) called
	rep := mustRestore(t, e.s, cp, RestoreOpts{})
	if !rep.OK() || e.read("f.txt") != "orig" {
		t.Fatalf("a stale fingerprint must not cause a false conflict: %s", rep.Summary())
	}
}

func TestCreatedFileEditedByAHumanIsNotDeletedWithoutForce(t *testing.T) {
	e := newEnv(t)
	cp := e.s.Begin("p")
	e.editAfter("a1", "new.txt", "agent made this")
	if err := os.WriteFile(e.abs("new.txt"), []byte("now with human work in it"), 0o644); err != nil {
		t.Fatal(err)
	}
	rep := mustRestore(t, e.s, cp, RestoreOpts{})
	if r := result(t, rep, "new.txt"); r.Outcome != OutcomeConflict {
		t.Fatalf("new.txt = %+v", r)
	}
	if !e.exists("new.txt") {
		t.Fatal("file must not be deleted")
	}
	mustRestore(t, e.s, cp, RestoreOpts{Force: true})
	if e.exists("new.txt") {
		t.Fatal("Force should delete it")
	}
}

func TestDeletedBehindOurBackIsRecreatedWithoutConflict(t *testing.T) {
	e := newEnv(t)
	e.write("f.txt", "orig")
	cp := e.s.Begin("p")
	e.editAfter("a1", "f.txt", "agent version")
	if err := os.Remove(e.abs("f.txt")); err != nil { // someone deleted it: nothing left to destroy
		t.Fatal(err)
	}
	rep := mustRestore(t, e.s, cp, RestoreOpts{})
	if !rep.OK() || e.read("f.txt") != "orig" {
		t.Fatalf("report = %s", rep.Summary())
	}
}

func TestOnlyAgentRestoresItsOwnFilesAndFlagsSharedOnes(t *testing.T) {
	e := newEnv(t)
	for _, n := range []string{"only1.txt", "only2.txt", "shared.txt"} {
		e.write(n, "orig-"+n)
	}
	cp := e.s.Begin("p")
	e.edit("a1", "only1.txt", "a1 edit")
	e.edit("a1", "shared.txt", "a1 edit")
	e.edit("a2", "shared.txt", "a2 edit")
	e.edit("a2", "only2.txt", "a2 edit")

	rep := mustRestore(t, e.s, cp, RestoreOpts{OnlyAgent: "a1"})
	if r := result(t, rep, "only1.txt"); r.Outcome != OutcomeDone {
		t.Fatalf("only1 = %+v", r)
	}
	r := result(t, rep, "shared.txt")
	if r.Outcome != OutcomeConflict || !strings.Contains(r.Detail, "a2") {
		t.Fatalf("shared = %+v", r)
	}
	for _, f := range rep.Files {
		if f.Path == "only2.txt" {
			t.Fatalf("a2's private file is out of scope but was reported: %+v", f)
		}
	}
	if e.read("only1.txt") != "orig-only1.txt" || e.read("shared.txt") != "a2 edit" || e.read("only2.txt") != "a2 edit" {
		t.Fatal("wrong files were touched")
	}
	if rep.Rewound {
		t.Fatal("a filtered rewind never drops history")
	}
	// The agent-scope conflict is overridable.
	rep = mustRestore(t, e.s, cp, RestoreOpts{OnlyAgent: "a1", Force: true})
	if r := result(t, rep, "shared.txt"); r.Outcome != OutcomeDone {
		t.Fatalf("shared with Force = %+v", r)
	}
	if e.read("shared.txt") != "orig-shared.txt" || e.read("only2.txt") != "a2 edit" {
		t.Fatal("Force with OnlyAgent should still leave a2's private file alone")
	}
}

func TestOnlyPathsAndUnmatchedPaths(t *testing.T) {
	e := newEnv(t)
	for _, n := range []string{"a/one.txt", "a/deep/two.txt", "b/three.txt", "top.txt"} {
		e.write(n, "orig")
	}
	cp := e.s.Begin("p")
	for _, n := range []string{"a/one.txt", "a/deep/two.txt", "b/three.txt", "top.txt"} {
		e.edit("a1", n, "changed")
	}
	rep := mustRestore(t, e.s, cp, RestoreOpts{OnlyPaths: []string{"a", "top.txt", "typo.txt"}})
	if e.read("a/one.txt") != "orig" || e.read("a/deep/two.txt") != "orig" || e.read("top.txt") != "orig" {
		t.Fatal("selected paths should be restored (a directory covers everything under it)")
	}
	if e.read("b/three.txt") != "changed" {
		t.Fatal("b/three.txt was not selected")
	}
	if r := result(t, rep, "typo.txt"); r.Outcome != OutcomeUnchanged || !strings.Contains(r.Detail, "not modified") {
		t.Fatalf("typo.txt = %+v", r)
	}
	// The unselected file keeps its record; the selected ones are forgotten.
	if got := e.s.List()[0].Files; !reflect.DeepEqual(got, []string{"b/three.txt"}) {
		t.Fatalf("files still recorded = %v", got)
	}
	// "." selects everything inside the root; an absolute path works too.
	rep = mustRestore(t, e.s, cp, RestoreOpts{OnlyPaths: []string{"."}})
	if e.read("b/three.txt") != "orig" || rep.Rewound {
		t.Fatalf("OnlyPaths . : rewound=%v", rep.Rewound)
	}
}

func TestOnlyPathsAcceptsAbsolutePaths(t *testing.T) {
	e := newEnv(t)
	e.write("x.txt", "orig")
	e.write("y.txt", "orig")
	cp := e.s.Begin("p")
	e.edit("a1", "x.txt", "changed")
	e.edit("a1", "y.txt", "changed")
	mustRestore(t, e.s, cp, RestoreOpts{OnlyPaths: []string{e.abs("x.txt")}})
	if e.read("x.txt") != "orig" || e.read("y.txt") != "changed" {
		t.Fatal("absolute OnlyPaths entry not honoured")
	}
}

func TestDryRunChangesNothing(t *testing.T) {
	e := newEnv(t)
	e.write("a.txt", "orig")
	cp := e.s.Begin("p")
	e.edit("a1", "a.txt", "changed")
	e.edit("a1", "new/x.txt", "created")
	before := e.s.List()

	rep := mustRestore(t, e.s, cp, RestoreOpts{DryRun: true})
	if !rep.DryRun || rep.Rewound {
		t.Fatalf("report = %+v", rep)
	}
	if r := result(t, rep, "a.txt"); r.Outcome != OutcomePlanned || r.Action != ActionRestore {
		t.Fatalf("a.txt = %+v", r)
	}
	if r := result(t, rep, "new/x.txt"); r.Outcome != OutcomePlanned || r.Action != ActionDelete {
		t.Fatalf("new/x.txt = %+v", r)
	}
	if r := result(t, rep, "new"); r.Action != ActionRmdir || r.Outcome != OutcomePlanned {
		t.Fatalf("new = %+v", r)
	}
	if !strings.HasPrefix(rep.Summary(), "checkpoint "+cp+": would restore 2") {
		t.Fatalf("summary = %q", rep.Summary())
	}
	if e.read("a.txt") != "changed" || e.read("new/x.txt") != "created" {
		t.Fatal("dry run modified files")
	}
	if !sameInfos(e.s.List(), before) {
		t.Fatal("dry run modified the manifest")
	}
}

func TestDryRunReportsConflictsToo(t *testing.T) {
	e := newEnv(t)
	e.write("f.txt", "orig")
	cp := e.s.Begin("p")
	e.editAfter("a1", "f.txt", "agent")
	if err := os.WriteFile(e.abs("f.txt"), []byte("human"), 0o644); err != nil {
		t.Fatal(err)
	}
	rep := mustRestore(t, e.s, cp, RestoreOpts{DryRun: true})
	if r := result(t, rep, "f.txt"); r.Outcome != OutcomeConflict {
		t.Fatalf("f.txt = %+v", r)
	}
}

func TestRestoreIsIdempotent(t *testing.T) {
	e := newEnv(t)
	e.write("a.txt", "orig")
	cp := e.s.Begin("p")
	e.edit("a1", "a.txt", "changed")
	mustRestore(t, e.s, cp, RestoreOpts{})
	rep := mustRestore(t, e.s, cp, RestoreOpts{})
	if !rep.OK() || len(rep.Files) != 0 {
		t.Fatalf("second restore: %+v", rep)
	}
	if e.read("a.txt") != "orig" {
		t.Fatal("content changed")
	}
}

func TestFilesAlreadyBackInTheOriginalStateAreUnchanged(t *testing.T) {
	e := newEnv(t)
	e.write("a.txt", "orig")
	cp := e.s.Begin("p")
	e.edit("a1", "a.txt", "changed")
	e.write("a.txt", "orig") // the user manually put it back
	rep := mustRestore(t, e.s, cp, RestoreOpts{})
	if r := result(t, rep, "a.txt"); r.Outcome != OutcomeUnchanged || r.Action != ActionNone {
		t.Fatalf("a.txt = %+v", r)
	}
	if !rep.Rewound {
		t.Fatal("everything matches, so the rewind is complete")
	}
}

func TestRestoreWritesAtomicallyWithoutLeavingTemporaryFiles(t *testing.T) {
	e := newEnv(t)
	for i := 0; i < 5; i++ {
		e.write("d/f"+string(rune('a'+i)), "orig")
	}
	cp := e.s.Begin("p")
	for i := 0; i < 5; i++ {
		e.edit("a1", "d/f"+string(rune('a'+i)), "changed")
	}
	mustRestore(t, e.s, cp, RestoreOpts{})
	entries, err := os.ReadDir(e.abs("d"))
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 5 {
		var names []string
		for _, en := range entries {
			names = append(names, en.Name())
		}
		t.Fatalf("directory has %v: temporary files leaked", names)
	}
	manifest, _ := os.ReadDir(e.dir)
	for _, en := range manifest {
		if strings.HasPrefix(en.Name(), ".sleipnir-tmp-") {
			t.Fatalf("manifest dir has a leaked temp file %s", en.Name())
		}
	}
}

func TestRestoreExcludesConcurrentBefore(t *testing.T) {
	// A Before that arrives while a Restore is running must wait for it and then
	// record against the post-restore state, never interleave with it.
	e := newEnv(t)
	for i := 0; i < 30; i++ {
		e.write("f"+string(rune('A'+i%26))+string(rune('a'+i/26)), "orig")
	}
	cp := e.s.Begin("p")
	for i := 0; i < 30; i++ {
		e.edit("a1", "f"+string(rune('A'+i%26))+string(rune('a'+i/26)), "changed")
	}
	e.write("late.txt", "late-orig")
	done := make(chan struct{})
	go func() {
		defer close(done)
		for i := 0; i < 50; i++ {
			_ = e.s.Before("a2", e.abs("late.txt"))
		}
	}()
	rep := mustRestore(t, e.s, cp, RestoreOpts{})
	<-done
	if !rep.OK() {
		t.Fatalf("%s", rep.Summary())
	}
}

// A path that was a file, is deleted, and then becomes a directory holding a new
// file exercises the ordering rules: the new file goes first (deepest first),
// the now-empty directory gives way to the restored file, and the directory
// cleanup must not then delete the restored file that took its name.
func TestFileThatBecameADirectoryWithAFileInsideIsRestored(t *testing.T) {
	e := newEnv(t)
	e.write("x", "x was a file")
	cp := e.s.Begin("p")
	e.remove("a1", "x")
	e.edit("a1", "x/y.txt", "y lives in what used to be a file's name")

	rep := mustRestore(t, e.s, cp, RestoreOpts{})
	if !rep.OK() || !rep.Rewound {
		t.Fatalf("%s\n%+v", rep.Summary(), rep.Files)
	}
	if e.read("x") != "x was a file" {
		t.Fatal("x should be a file again with its original content")
	}
	fi, err := os.Lstat(e.abs("x"))
	if err != nil || fi.IsDir() {
		t.Fatalf("x = %v, %v", fi, err)
	}
}

// If Begin runs between a write and its After (the next prompt started while a
// tool call was finishing), the fingerprint still belongs to the checkpoint
// that announced the write; otherwise a later human edit would go unnoticed.
func TestAfterFindsItsRecordEvenIfANewCheckpointBeganMeanwhile(t *testing.T) {
	e := newEnv(t)
	e.write("f.txt", "orig")
	cp := e.s.Begin("first")
	e.edit("a1", "f.txt", "agent version") // announced in cp
	e.s.Begin("second")                    // the next prompt starts before After runs
	e.s.After("a1", e.abs("f.txt"))
	if err := os.WriteFile(e.abs("f.txt"), []byte("human edit"), 0o644); err != nil { // mtime stays within the grace window
		t.Fatal(err)
	}
	rep := mustRestore(t, e.s, cp, RestoreOpts{})
	if r := result(t, rep, "f.txt"); r.Outcome != OutcomeConflict {
		t.Fatalf("f.txt = %+v; the fingerprint must have been attached to the first checkpoint's record", r)
	}
	if e.read("f.txt") != "human edit" {
		t.Fatal("the human's edit was overwritten")
	}
}
