package workspace

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/reee344/sleipnir/internal/gitx"
)

// ---- a queue after a crash -------------------------------------------------

// A session that died leaves its integration tree behind with a dead owner. A new
// queue for the same session, asked to resume, takes the branch's tip over and
// replaces the dead tree by itself: no Prune needed first.
func TestNewQueueTakesOverTheIntegrationTreeOfADeadRun(t *testing.T) {
	e := newQueueEnv(t, QueueOptions{})
	a := e.agent("a")
	edit(t, a, "a.txt", "a\n")
	if r := e.submit(a, "a"); !r.Merged() {
		t.Fatalf("a: %+v", r)
	}
	tip, base := e.q.Tip(), e.q.Base()
	orphan(t, e.q.tree) // the process is gone: nothing was closed
	orphan(t, a)

	fresh := &Manager{Repo: e.repo, Dir: e.m.Dir, Prefix: e.m.Prefix, Clock: testClock()}
	if _, err := NewQueue(tctx(t), fresh, QueueOptions{}); !errors.Is(err, ErrExists) {
		t.Fatalf("a queue that does not ask to resume must not take over work: %v", err)
	}
	q2, err := NewQueue(tctx(t), fresh, QueueOptions{Resume: true, VerifyCmd: "test -f a.txt"})
	if err != nil {
		t.Fatalf("NewQueue(Resume) over the dead run's tree: %v", err)
	}
	if q2.Tip() != tip || q2.Base() != base {
		t.Fatalf("resumed at %s from %s, want %s from %s", q2.Tip(), q2.Base(), tip, base)
	}
	if !exists(filepath.Join(q2.tree.Path, "a.txt")) {
		t.Fatal("the new integration tree does not hold the earlier work")
	}
	b := mustCreate(t, fresh, "b", CreateOptions{Base: q2.Tip()})
	edit(t, b, "b.txt", "b\n")
	r := mustSubmit(t, q2, Submission{Tree: b})
	if !r.Merged() || r.Before != tip || r.Verify == nil || !r.Verify.OK() {
		t.Fatalf("b through the resumed queue: %+v", r)
	}
	patch, err := q2.Finish(tctx(t))
	if err != nil || !strings.Contains(patch, "a.txt") || !strings.Contains(patch, "b.txt") {
		t.Fatalf("Finish after resume: %v\n%s", err, patch)
	}
}

// The integration tree of a session that is still running is not taken away.
func TestNewQueueLeavesTheIntegrationTreeOfARunningSessionAlone(t *testing.T) {
	e := newQueueEnv(t, QueueOptions{})
	a := e.agent("a")
	edit(t, a, "a.txt", "a\n")
	e.submit(a, "a")
	other := &Manager{Repo: e.repo, Dir: e.m.Dir, Prefix: e.m.Prefix, Clock: testClock()}
	_, err := NewQueue(tctx(t), other, QueueOptions{Resume: true})
	if !errors.Is(err, ErrExists) || !strings.Contains(err.Error(), "running session") {
		t.Fatalf("NewQueue over a live session's tree: %v", err)
	}
	e.integrationClean()
}

// When the user committed while the session was down, the manager's base is a
// newer commit than the integration branch started from. The queue resumes at the
// branch's tip and reports the base it really grew from.
func TestNewQueueResumesWhenTheUserCommittedInTheMeantime(t *testing.T) {
	e := newQueueEnv(t, QueueOptions{})
	a := e.agent("a")
	edit(t, a, "a.txt", "a\n")
	e.submit(a, "a")
	tip, base := e.q.Tip(), e.q.Base()
	orphan(t, e.q.tree)
	orphan(t, a)

	writeFile(t, filepath.Join(e.dir, "user.txt"), "the user's own commit\n")
	rawGit(t, e.dir, "add", "-A")
	rawGit(t, e.dir, "commit", "-qm", "user work")

	fresh := &Manager{Repo: openRepo(t, e.dir), Dir: e.m.Dir, Prefix: e.m.Prefix, Clock: testClock()}
	q2, err := NewQueue(tctx(t), fresh, QueueOptions{Resume: true})
	if err != nil {
		t.Fatal(err)
	}
	if q2.Tip() != tip || q2.Base() != base {
		t.Fatalf("resumed at %s from %s, want %s from %s (the merge base of the two histories)", q2.Tip(), q2.Base(), tip, base)
	}
	if fresh.BaseSHA() == base {
		t.Fatal("the fresh manager should have started from the user's newer commit")
	}
}

// ---- queue outcomes that are not merges ------------------------------------

func TestQueueRecognizesWhatIsAlreadyIntegrated(t *testing.T) {
	e := newQueueEnv(t, QueueOptions{NoFF: true})
	a := e.agent("a")
	edit(t, a, "a.txt", "a\n")
	r := e.submit(a, "a")
	if !r.Merged() {
		t.Fatalf("a: %+v", r)
	}
	// with a merge commit on top, a's own commit is an ancestor of the tip, not the tip
	again := e.submit(a, "a again")
	if again.Outcome != OutcomeEmpty || !strings.Contains(again.Reason, "already integrated") {
		t.Fatalf("resubmission: %+v", again)
	}
	if e.q.Tip() != r.After {
		t.Fatal("the tip moved")
	}
	e.integrationClean()
}

// Two agents that made the same change: with the rebase strategy the second
// one's commit becomes empty and is dropped, which is reported as "nothing to
// integrate" rather than as an empty merge.
func TestQueueRebaseOfAnAlreadyMadeChangeChangesNothing(t *testing.T) {
	e := newQueueEnv(t, QueueOptions{Strategy: StrategyRebase})
	a, b := e.agent("a"), e.agent("b")
	edit(t, a, "same.txt", "the same change\n")
	edit(t, b, "same.txt", "the same change\n")
	if r := e.submit(a, "a"); !r.Merged() {
		t.Fatalf("a: %+v", r)
	}
	tip := e.q.Tip()
	r := e.submit(b, "b")
	if r.Outcome != OutcomeEmpty || !strings.Contains(r.Reason, "changed nothing") {
		t.Fatalf("b: %+v", r)
	}
	if e.q.Tip() != tip {
		t.Fatal("the tip moved")
	}
	e.integrationClean()
	if st := e.q.Status(); !st.Healthy || st.Empty != 1 {
		t.Fatalf("status: %+v", st)
	}
}

func TestFinishWithoutWorkIsEmptyAndFastForwardHasNothingToMove(t *testing.T) {
	e := newQueueEnv(t, QueueOptions{})
	if patch, err := e.q.Finish(tctx(t)); err != nil || patch != "" {
		t.Fatalf("Finish with no work: %q, %v", patch, err)
	}
	ff, err := e.q.FastForward(tctx(t))
	if err != nil || ff.Branch != "" || ff.From != ff.To {
		t.Fatalf("FastForward with no work: %+v, %v", ff, err)
	}
	a := e.agent("a")
	edit(t, a, "a.txt", "a\n")
	e.submit(a, "a")
	if ff, err = e.q.FastForward(tctx(t)); err != nil || ff.Branch != "main" || ff.To != e.q.Tip() {
		t.Fatalf("FastForward: %+v, %v", ff, err)
	}
	if ff, err = e.q.FastForward(tctx(t)); err != nil || ff.Branch != "" {
		t.Fatalf("a second FastForward has nothing to move: %+v, %v", ff, err)
	}
}

func TestFastForwardRefusesADivergedBranchAndCopyMode(t *testing.T) {
	e := newQueueEnv(t, QueueOptions{})
	a := e.agent("a")
	edit(t, a, "a.txt", "a\n")
	e.submit(a, "a")
	writeFile(t, filepath.Join(e.dir, "user.txt"), "user\n")
	rawGit(t, e.dir, "add", "-A")
	rawGit(t, e.dir, "commit", "-qm", "the user moved on")
	if _, err := e.q.FastForward(tctx(t)); !errors.Is(err, ErrNotFastForward) {
		t.Fatalf("FastForward over a diverged branch: %v", err)
	}

	m := newCopyManager(t, newPlainDir(t))
	q := mustQueue(t, m, QueueOptions{})
	if _, err := q.FastForward(tctx(t)); !errors.Is(err, ErrUnsupported) {
		t.Fatalf("FastForward in copy mode: %v", err)
	}
}

// ---- manager -----------------------------------------------------------------

func TestManagerDefaultsAndMissingDir(t *testing.T) {
	dir := newRepo(t)
	repo := openRepo(t, dir)
	if _, err := (&Manager{Repo: repo}).Create(tctx(t), "a", CreateOptions{}); !errors.Is(err, ErrBadName) {
		t.Fatalf("a manager without Dir: %v", err)
	}
	m := &Manager{Repo: repo, Dir: filepath.Join(t.TempDir(), "trees"), Clock: testClock()} // no Prefix
	a := mustCreate(t, m, "a")
	if a.Branch != "sleipnir/a" {
		t.Fatalf("default prefix: branch %q", a.Branch)
	}
	if _, err := repo.BranchSHA(tctx(t), "sleipnir/a"); err != nil {
		t.Fatal(err)
	}
	if err := a.Remove(tctx(t), false); err != nil {
		t.Fatal(err)
	}
	if _, err := (&Manager{Repo: repo, Dir: t.TempDir(), Prefix: "a/../b"}).Create(tctx(t), "x", CreateOptions{}); !errors.Is(err, ErrBadName) {
		t.Fatalf("a bad prefix: %v", err)
	}
}

func TestListLabelsTheOwnerOfEachTree(t *testing.T) {
	dir := newRepo(t)
	repo := openRepo(t, dir)
	m := newManager(t, repo)
	live := mustCreate(t, m, "live")
	gone := mustCreate(t, m, "gone")
	elsewhere := mustCreate(t, m, "elsewhere")
	orphan(t, gone)
	mk, err := readMarker(elsewhere.repo.GitDir())
	if err != nil {
		t.Fatal(err)
	}
	mk.BootID = "another-boot"
	must(t, writeMarker(elsewhere.repo.GitDir(), *mk))

	infos, err := m.List(tctx(t))
	if err != nil {
		t.Fatal(err)
	}
	owners := map[string]string{}
	for _, in := range infos {
		owners[in.Agent] = in.Owner
	}
	if owners["live"] != "alive" || owners["gone"] != "dead" || owners["elsewhere"] != "unknown" || len(owners) != 3 {
		t.Fatalf("owners: %v", owners)
	}
	_ = live
}

func TestCreateRefusesNamesThatAreTakenInWaysItCannotClear(t *testing.T) {
	dir := newRepo(t)
	repo := openRepo(t, dir)
	m := newManager(t, repo)
	mustCreate(t, m, "warmup")

	// the branch is checked out in a worktree somebody else made
	theirs := filepath.Join(t.TempDir(), "their-tree")
	rawGit(t, dir, "worktree", "add", "-q", "-b", "sleipnir/s1/x", theirs)
	if _, err := m.Create(tctx(t), "x", CreateOptions{}); !errors.Is(err, ErrExists) || !strings.Contains(err.Error(), "checked out") {
		t.Fatalf("Create over a branch checked out elsewhere: %v", err)
	}
	if !exists(theirs) {
		t.Fatal("the other worktree was disturbed")
	}

	// a registration at our path whose directory is gone, made by somebody else
	// (no marker of ours): not ours to clear
	yPath := filepath.Join(m.TreesDir(), "y")
	rawGit(t, dir, "worktree", "add", "-q", "--detach", yPath)
	must(t, os.RemoveAll(yPath))
	if _, err := m.Create(tctx(t), "y", CreateOptions{}); !errors.Is(err, ErrExists) || !strings.Contains(err.Error(), "not ours") {
		t.Fatalf("Create over a foreign stale registration: %v", err)
	}
}

// ---- removal -------------------------------------------------------------------

// `git worktree lock` can be run by any process in the tree, and makes git refuse
// to remove it. It must not make the tree unremovable for the manager that owns it
// (a clean, merged tree goes away quietly; a forced removal always works).
func TestATreeLockedByItsAgentCanStillBeRemoved(t *testing.T) {
	dir := newRepo(t)
	repo := openRepo(t, dir)
	m := newManager(t, repo)
	a := mustCreate(t, m, "a")
	b := mustCreate(t, m, "b")
	for _, tr := range []*Tree{a, b} {
		rawGit(t, tr.Path, "worktree", "lock", "--reason", "the agent says so", ".")
	}
	if err := a.Remove(tctx(t), false); err != nil {
		t.Fatalf("Remove of a clean locked tree: %v", err)
	}
	if err := b.Remove(tctx(t), true); err != nil {
		t.Fatalf("forced Remove of a locked tree: %v", err)
	}
	for _, tr := range []*Tree{a, b} {
		if exists(tr.Path) {
			t.Fatalf("%s survived", tr.Path)
		}
	}
	wts, _ := repo.Worktrees(tctx(t))
	if len(wts) != 1 {
		t.Fatalf("registrations left: %+v", wts)
	}
	admins, _ := os.ReadDir(filepath.Join(repo.CommonDir(), "worktrees"))
	if len(admins) != 0 {
		t.Fatalf("administrative directories left: %v", admins)
	}
	// and a locked, abandoned one is pruned like any other
	c := mustCreate(t, m, "c")
	rawGit(t, c.Path, "worktree", "lock", ".")
	orphan(t, c)
	fresh := &Manager{Repo: repo, Dir: m.Dir, Prefix: "sleipnir/s1", Clock: testClock()}
	rep, err := fresh.Prune(tctx(t), PruneOptions{})
	if err != nil || exists(c.Path) || len(rep.Removed) != 1 {
		t.Fatalf("Prune of a locked stale tree: %+v, %v", rep, err)
	}
}

// ---- Prune's refusals --------------------------------------------------------

func TestPruneKeepsWhatItCannotSalvageSafely(t *testing.T) {
	dir := newRepo(t)
	repo := openRepo(t, dir)
	m := newManager(t, repo)
	m.MaxFileBytes = 2000

	big := mustCreate(t, m, "big")
	edit(t, big, "dump.bin", strings.Repeat("x", 5000))
	detached := mustCreate(t, m, "detached", CreateOptions{Detach: true})
	edit(t, detached, "wip.txt", "wip\n")
	orphan(t, big)
	orphan(t, detached)

	fresh := &Manager{Repo: repo, Dir: m.Dir, Prefix: "sleipnir/s1", Clock: testClock(), MaxFileBytes: 2000}
	dry, err := fresh.Prune(tctx(t), PruneOptions{Salvage: true, DryRun: true})
	if err != nil {
		t.Fatal(err)
	}
	if len(dry.Removed) == 0 || !strings.Contains(dry.Removed[0].Reason, "would salvage") {
		t.Fatalf("dry run: %+v", dry)
	}
	rep, err := fresh.Prune(tctx(t), PruneOptions{Salvage: true})
	if err != nil {
		t.Fatal(err)
	}
	reasons := map[string]string{}
	for _, k := range rep.Kept {
		reasons[k.Agent] = k.Reason
	}
	if !strings.Contains(reasons["big"], "salvage failed") || !strings.Contains(reasons["detached"], "detached") {
		t.Fatalf("Kept: %+v", rep.Kept)
	}
	if !exists(filepath.Join(big.Path, "dump.bin")) || !exists(filepath.Join(detached.Path, "wip.txt")) {
		t.Fatal("work that could not be salvaged was deleted")
	}
}

// ---- conflicts -------------------------------------------------------------------

func TestQueueConflictFileListIsCapped(t *testing.T) {
	e := newQueueEnv(t, QueueOptions{})
	const n = maxConflictFiles + 5
	seed := e.agent("seed")
	for i := 0; i < n; i++ {
		edit(t, seed, fmt.Sprintf("many/f%02d.txt", i), "original\n")
	}
	if r := e.submit(seed, "seed"); !r.Merged() {
		t.Fatalf("seed: %+v", r)
	}
	a, b := e.agent("a"), e.agent("b")
	for i := 0; i < n; i++ {
		edit(t, a, fmt.Sprintf("many/f%02d.txt", i), "first\n")
		edit(t, b, fmt.Sprintf("many/f%02d.txt", i), "second\n")
	}
	if r := e.submit(a, "a"); !r.Merged() {
		t.Fatalf("a: %+v", r)
	}
	r := e.submit(b, "b")
	if r.Outcome != OutcomeConflict {
		t.Fatalf("b: %+v", r)
	}
	c := r.Conflict
	if len(c.Files) != maxConflictFiles || len(c.Details) != maxConflictFiles || !c.Truncated {
		t.Fatalf("files=%d details=%d truncated=%v", len(c.Files), len(c.Details), c.Truncated)
	}
	e.integrationClean()
}

// A merge driver named by the repository is never run. Where it would have been
// consulted, git cannot merge, so the file comes back as a conflict that says why,
// instead of being merged by a driver we did not run or by a text merge the
// repository asked not to have.
func TestQueueConflictNamesADisabledMergeDriver(t *testing.T) {
	skipWithoutUnix(t)
	dir := newRepo(t)
	base := "one\ntwo\nthree\nfour\nfive\nsix\nseven\neight\nnine\n"
	writeFile(t, filepath.Join(dir, "table.dat"), base)
	writeFile(t, filepath.Join(dir, ".gitattributes"), "*.dat merge=custom\n")
	rawGit(t, dir, "add", "-A")
	rawGit(t, dir, "commit", "-qm", "data with a custom merge driver")
	rawGit(t, dir, "config", "merge.custom.driver", "echo would-run-a-program %O %A %B; exit 0")

	m := newManager(t, openRepo(t, dir))
	q := mustQueue(t, m, QueueOptions{})
	a := mustCreate(t, m, "a", CreateOptions{Base: q.Tip()})
	b := mustCreate(t, m, "b", CreateOptions{Base: q.Tip()})
	// different regions: a plain text merge would be clean
	edit(t, a, "table.dat", strings.Replace(base, "one", "ONE", 1))
	edit(t, b, "table.dat", strings.Replace(base, "nine", "NINE", 1))
	if r := mustSubmit(t, q, Submission{Tree: a}); !r.Merged() {
		t.Fatalf("a: %+v", r)
	}
	r := mustSubmit(t, q, Submission{Tree: b})
	if r.Outcome != OutcomeConflict || r.Conflict == nil {
		t.Fatalf("b: %+v", r)
	}
	d := r.Conflict.Details
	if len(d) != 1 || d[0].Path != "table.dat" || !strings.Contains(d[0].Note, "merge driver") {
		t.Fatalf("details: %+v", d)
	}
}

// ---- trees -----------------------------------------------------------------------

func TestUpdateRecordsUnsavedWorkBeforeMerging(t *testing.T) {
	e := newQueueEnv(t, QueueOptions{})
	a, b := e.agent("a"), e.agent("b")
	edit(t, b, "b.txt", "b\n")
	if r := e.submit(b, "b"); !r.Merged() {
		t.Fatalf("b: %+v", r)
	}
	edit(t, a, "a.txt", "unsaved work of a\n") // never committed
	before, _ := a.Head(tctx(t))
	cf, err := a.Update(tctx(t), e.q.Tip())
	if err != nil || cf != nil {
		t.Fatalf("Update: %+v, %v", cf, err)
	}
	after, _ := a.Head(tctx(t))
	if after == before {
		t.Fatal("Update did not move the tree")
	}
	if !exists(filepath.Join(a.Path, "a.txt")) || !exists(filepath.Join(a.Path, "b.txt")) {
		t.Fatal("after Update the tree lacks its own work or the merged work")
	}
	if dirty, _ := a.Dirty(tctx(t)); dirty {
		t.Fatal("the tree is dirty after a clean Update")
	}
	// and Update refuses to bring conflict markers into a commit later
	if err := a.AbortUpdate(tctx(t)); err != nil {
		t.Fatalf("AbortUpdate with nothing to abort: %v", err)
	}
}

func TestDiffRefusesToReturnACutPatch(t *testing.T) {
	dir := newRepo(t)
	m := newManager(t, openRepo(t, dir))
	m.MaxDiffBytes = 200
	a := mustCreate(t, m, "a")
	edit(t, a, "big.txt", strings.Repeat("a long line of text\n", 100))
	if _, err := a.Diff(tctx(t)); !errors.Is(err, ErrTooLarge) {
		t.Fatalf("Diff over the cap: %v", err)
	}
	d, err := a.DiffWith(tctx(t), gitx.DiffOptions{MaxPatchBytes: 200})
	if err != nil || !d.Truncated {
		t.Fatalf("DiffWith reports the cut: %+v, %v", d, err)
	}
}

// ---- copy mode ---------------------------------------------------------------------

func TestCopyModeRefusesWhatIsNotItsToUse(t *testing.T) {
	skipWithoutUnix(t)
	plain := newPlainDir(t)

	// the source is a file, not a directory
	file := filepath.Join(t.TempDir(), "afile")
	writeFile(t, file, "x")
	if _, err := newCopyManager(t, file).Create(tctx(t), "a", CreateOptions{}); err == nil || !strings.Contains(err.Error(), "not a directory") {
		t.Fatalf("copy mode over a file: %v", err)
	}
	if _, err := (&Manager{Dir: filepath.Join(t.TempDir(), "t")}).Create(tctx(t), "a", CreateOptions{}); !errors.Is(err, ErrBadName) {
		t.Fatalf("copy mode without a source: %v", err)
	}

	// a snapshot made for one source is not reused for another
	m := newCopyManager(t, plain)
	mustCreate(t, m, "a")
	other := newPlainDir(t)
	m2 := &Manager{Source: other, Dir: m.Dir, Prefix: "sleipnir/s1", Clock: testClock()}
	if _, err := m2.Create(tctx(t), "b", CreateOptions{}); !errors.Is(err, ErrExists) {
		t.Fatalf("a snapshot of another directory: %v", err)
	}

	// a directory that merely has the snapshot's name is not ours
	stray := filepath.Join(t.TempDir(), "trees")
	must(t, os.MkdirAll(filepath.Join(stray, shadowName, "keep"), 0o755))
	writeFile(t, filepath.Join(stray, shadowName, "keep", "precious.txt"), "do not touch\n")
	m3 := &Manager{Source: plain, Dir: stray, Prefix: "sleipnir/s1", Clock: testClock()}
	if _, err := m3.Create(tctx(t), "a", CreateOptions{}); err == nil {
		t.Fatal("copy mode used a directory that is not its snapshot")
	}
	if readFile(t, filepath.Join(stray, shadowName, "keep", "precious.txt")) != "do not touch\n" {
		t.Fatal("copy mode touched a directory that is not its own")
	}
	// Close with force removes our snapshot, and only ours
	if err := m.Close(tctx(t), true); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if exists(filepath.Join(m.TreesDir(), shadowName)) {
		t.Fatal("Close(force) left the snapshot")
	}
	if err := m3.Close(tctx(t), true); err == nil || !exists(filepath.Join(stray, shadowName, "keep")) {
		t.Fatalf("Close removed something that is not a snapshot of ours: %v", err)
	}
}
