package workspace

import (
	"context"
	"errors"
	"fmt"
	"math/rand"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/reee344/sleipnir/internal/gitx"
)

func skipWithoutUnix(t testing.TB) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("needs a unix shell")
	}
}

const coreV1 = "package core\n\nfunc Name() string { return \"core\" }\n\nfunc Version() int { return 1 }\n\nfunc Extra() int { return 0 }\n"

// queueEnv is a repository, a manager and a queue with an event log.
type queueEnv struct {
	t    *testing.T
	dir  string
	repo *gitx.Repo
	m    *Manager
	q    *Queue
	log  *eventLog
}

func newQueueEnv(t *testing.T, o QueueOptions) *queueEnv {
	t.Helper()
	skipWithoutUnix(t)
	dir := newRepo(t)
	repo := openRepo(t, dir)
	m := newManager(t, repo)
	log := &eventLog{}
	m.OnEvent = log.fn() // the queue defaults to the manager's sink
	q := mustQueue(t, m, o)
	return &queueEnv{t: t, dir: dir, repo: repo, m: m, q: q, log: log}
}

// agent creates a tree from the queue's current tip.
func (e *queueEnv) agent(id string) *Tree {
	e.t.Helper()
	return mustCreate(e.t, e.m, id, CreateOptions{Base: e.q.Tip()})
}

func (e *queueEnv) submit(tree *Tree, task string) *Result {
	e.t.Helper()
	return mustSubmit(e.t, e.q, Submission{Tree: tree, Task: task})
}

// integrationClean asserts the integration tree is exactly at the queue's tip,
// with no leftovers, no merge state and no conflict markers.
func (e *queueEnv) integrationClean() {
	e.t.Helper()
	repo := e.q.tree.repo
	head, err := repo.Head(tctx(e.t))
	if err != nil || head != e.q.Tip() {
		e.t.Fatalf("integration tree at %s (err %v), want the tip %s", head, err, e.q.Tip())
	}
	st, err := repo.Status(tctx(e.t))
	if err != nil || !st.Clean() {
		e.t.Fatalf("integration tree is dirty: %+v (err %v)", st, err)
	}
	if repo.InProgress() != "" {
		e.t.Fatalf("integration tree is in the middle of a %s", repo.InProgress())
	}
	if sha, err := e.repo.BranchSHA(tctx(e.t), e.q.Branch()); err != nil || sha != e.q.Tip() {
		e.t.Fatalf("integration branch at %s (err %v), want %s", sha, err, e.q.Tip())
	}
	files := projectFiles(e.t, e.q.tree.Path)
	for name, c := range files {
		if hasMarkers([]byte(c)) {
			e.t.Fatalf("conflict markers in the integration tree: %s", name)
		}
	}
}

func TestQueueMergesSeveralAgentsSerially(t *testing.T) {
	e := newQueueEnv(t, QueueOptions{VerifyCmd: "test -f go.mod && test -f README.md"})
	a, b, c := e.agent("be-1"), e.agent("fe-1"), e.agent("dc-1")
	edit(t, a, "internal/util/util.go", "package util\n\nfunc Add(a, b int) int { return a + b }\n\nfunc Sub(a, b int) int { return a - b }\n")
	edit(t, b, "cmd/app/main.go", "package main\n\nfunc main() { println(\"hi\") }\n")
	edit(t, c, "docs/guide.md", "# guide\n\nline one\nline two\nline three\nline four\nline five\nline six\n")
	base := e.q.Tip()

	r1 := e.submit(a, "T1 util")
	r2 := e.submit(b, "T2 app")
	r3 := e.submit(c, "T3 docs")
	for i, r := range []*Result{r1, r2, r3} {
		if !r.Merged() || r.Err() != nil || r.Verify == nil || !r.Verify.OK() {
			t.Fatalf("result %d: %+v", i, r)
		}
	}
	if r1.Before != base || r1.After != r2.Before || r2.After != r3.Before || r3.After != e.q.Tip() || r1.After == r1.Before {
		t.Fatalf("tips do not chain: %s→%s, %s→%s, %s→%s", r1.Before, r1.After, r2.Before, r2.After, r3.Before, r3.After)
	}
	if strings.Join(r1.Files, ",") != "internal/util/util.go" || r1.Commits != 1 {
		t.Fatalf("r1 files/commits: %v %d", r1.Files, r1.Commits)
	}
	e.integrationClean()
	// everything landed
	for path, want := range map[string]string{
		"internal/util/util.go": "func Sub",
		"cmd/app/main.go":       "println",
		"docs/guide.md":         "line six",
	} {
		if got := readFile(t, filepath.Join(e.q.tree.Path, path)); !strings.Contains(got, want) {
			t.Errorf("%s lacks %q", path, want)
		}
	}
	st := e.q.Status()
	if st.Merged != 3 || len(st.Landed) != 3 || st.Landed[0].Agent != "be-1" || st.Landed[2].Task != "T3 docs" || !st.Healthy || st.Tip != e.q.Tip() || st.Base != base ||
		st.Branch != "sleipnir/s1/_integration" || st.Path != e.q.tree.Path || st.Active != nil || len(st.Waiting) != 0 {
		t.Fatalf("status: %+v", st)
	}
	// events: queued, commit, merged for each submission in order
	var queueEvents []string
	for _, ty := range e.log.types() {
		if strings.HasPrefix(ty, "merge.") || ty == EventCommit {
			queueEvents = append(queueEvents, ty)
		}
	}
	wantEv := []string{EventQueued, EventCommit, EventMerged, EventQueued, EventCommit, EventMerged, EventQueued, EventCommit, EventMerged}
	if strings.Join(queueEvents, ",") != strings.Join(wantEv, ",") {
		t.Fatalf("events:\n got %v\nwant %v", queueEvents, wantEv)
	}
	ev, _ := e.log.first(EventMerged)
	if ev.Agent != "be-1" || ev.Task != "T1 util" || ev.Data["before"] != base || ev.Data["after"] != r1.After || ev.Data["strategy"] != "merge" || ev.Data["verified"] != true {
		t.Fatalf("merged event: %+v", ev)
	}
	// the merge commits are attributed to the queue, the work to the agents
	log, _ := e.repo.Log(tctx(t), gitx.LogOptions{Rev: e.q.Branch(), Max: 20})
	authors := map[string]bool{}
	for _, c := range log {
		authors[c.Author.Name] = true
	}
	for _, want := range []string{"be-1", "fe-1", "dc-1"} {
		if !authors[want] {
			t.Errorf("no commit by %s in the integration history: %v", want, authors)
		}
	}
}

func TestQueueServesSubmissionsInArrivalOrder(t *testing.T) {
	e := newQueueEnv(t, QueueOptions{})
	gate := filepath.Join(t.TempDir(), "gate")
	holder := e.agent("holder")
	edit(t, holder, "held.txt", "h\n")

	const n = 6
	trees := make([]*Tree, n)
	for i := range trees {
		trees[i] = e.agent(fmt.Sprintf("w-%d", i))
		edit(t, trees[i], fmt.Sprintf("work/w-%d.txt", i), "w\n")
	}
	results := make([]*Result, n+1)
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		// this verification blocks until the gate file appears, holding the turn
		results[0] = mustSubmit(t, e.q, Submission{Tree: holder, Task: "holder", VerifyCmd: fmt.Sprintf("while [ ! -f %q ]; do sleep 0.02; done", gate)})
	}()
	waitFor(t, func() bool { st := e.q.Status(); return st.Active != nil && st.Active.Phase == "verifying" })
	for i := 0; i < n; i++ {
		i := i
		wg.Add(1)
		go func() {
			defer wg.Done()
			results[i+1] = mustSubmit(t, e.q, Submission{Tree: trees[i], Task: fmt.Sprintf("w-%d", i)})
		}()
		// arrival order is made deterministic by waiting until each is in line
		waitFor(t, func() bool { return len(e.q.Status().Waiting) == i+1 })
	}
	st := e.q.Status()
	if st.Active.Agent != "holder" || len(st.Waiting) != n || st.Waiting[0].Agent != "w-0" || st.Waiting[n-1].Agent != fmt.Sprintf("w-%d", n-1) || st.Waiting[0].Phase != "queued" {
		t.Fatalf("status while blocked: %+v", st)
	}
	if err := os.WriteFile(gate, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	wg.Wait()
	for i, r := range results {
		if r == nil || !r.Merged() {
			t.Fatalf("result %d: %+v", i, r)
		}
	}
	landed := e.q.Status().Landed
	var order []string
	for _, l := range landed {
		order = append(order, l.Agent)
	}
	want := "holder,w-0,w-1,w-2,w-3,w-4,w-5"
	if strings.Join(order, ",") != want {
		t.Fatalf("landing order %v, want %s", order, want)
	}
	e.integrationClean()
}

func waitFor(t testing.TB, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(20 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatal("condition not reached in time")
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func TestQueueConflictLeavesNoTraceAndIsActionable(t *testing.T) {
	e := newQueueEnv(t, QueueOptions{VerifyCmd: "true"})
	a, b, x := e.agent("be-1"), e.agent("be-2"), e.agent("be-3")
	edit(t, a, "internal/core/core.go", strings.Replace(coreV1, "return 1", "return 100", 1))
	edit(t, b, "internal/core/core.go", strings.Replace(coreV1, "return 1", "return 200", 1))
	edit(t, b, "docs/guide.md", "# guide\n\nline one\nline two\nline three\nline four\nline five\nline six by be-2\n")

	r1 := e.submit(a, "T1")
	if !r1.Merged() {
		t.Fatalf("first: %+v", r1)
	}
	tipBefore := e.q.Tip()
	r2 := e.submit(b, "T2")
	if r2.Outcome != OutcomeConflict || r2.Conflict == nil {
		t.Fatalf("second: %+v", r2)
	}
	c := r2.Conflict
	if strings.Join(c.Files, ",") != "internal/core/core.go" || len(c.Details) != 1 || c.Details[0].Kind != "content" || c.Details[0].HunkCount != 1 {
		t.Fatalf("conflict: %+v", c)
	}
	if len(c.Hunks) != 1 {
		t.Fatalf("hunks: %+v", c.Hunks)
	}
	h := c.Hunks[0]
	if h.File != "internal/core/core.go" || !strings.Contains(h.Ours, "return 100") || !strings.Contains(h.Theirs, "return 200") || !strings.Contains(h.Base, "return 1") || h.Line <= 0 || h.Truncated {
		t.Fatalf("hunk: %+v", h)
	}
	if c.Agent != "be-2" || c.Task != "T2" || c.Tip != tipBefore || c.Theirs == "" || c.MergeBase == "" {
		t.Fatalf("conflict metadata: %+v", c)
	}
	if strings.Join(c.Details[0].With, ",") != "be-1" {
		t.Fatalf("who landed the competing change: %v", c.Details[0].With)
	}
	for _, want := range []string{"be-2", "internal/core/core.go", "landed by be-1", tipBefore[:8], "resubmit", "be-1"} {
		if !strings.Contains(c.Suggest, want) {
			t.Errorf("Suggest lacks %q:\n%s", want, c.Suggest)
		}
	}
	var asErr *Conflict
	if !errors.As(r2.Err(), &asErr) || asErr != c {
		t.Fatal("Result.Err() should return the *Conflict")
	}
	// the integration tree is exactly where it was, and the branch did not move
	if e.q.Tip() != tipBefore {
		t.Fatal("a conflict moved the tip")
	}
	e.integrationClean()
	if got := readFile(t, filepath.Join(e.q.tree.Path, "docs", "guide.md")); strings.Contains(got, "be-2") {
		t.Fatal("part of the conflicting submission leaked into the integration tree")
	}
	if e.log.count(EventConflict) != 1 {
		t.Fatalf("events: %v", e.log.types())
	}
	ev, _ := e.log.first(EventConflict)
	if ev.Agent != "be-2" || ev.Data["tip"] != tipBefore {
		t.Fatalf("conflict event: %+v", ev)
	}
	if st := e.q.Status(); st.Conflicts != 1 || st.Merged != 1 || !st.Healthy {
		t.Fatalf("status: %+v", st)
	}
	// the queue carries on: an unrelated agent still merges
	d := e.agent("dc-1")
	edit(t, d, "README.md", "# project (docs agent)\n")
	if r := e.submit(d, "T3"); !r.Merged() {
		t.Fatalf("submission after a conflict: %+v", r)
	}
	e.integrationClean()

	// The loser resolves it against the tip in its own tree (Update leaves markers
	// for its ordinary tools) and resubmits.
	upd, err := b.Update(tctx(t), e.q.Tip())
	if err != nil || upd == nil || len(upd.Files) != 1 || upd.Files[0] != "internal/core/core.go" {
		t.Fatalf("Update: %+v, %v", upd, err)
	}
	body := readFile(t, filepath.Join(b.Path, "internal/core/core.go"))
	if !hasMarkers([]byte(body)) || !strings.Contains(body, "return 100") || !strings.Contains(body, "return 200") {
		t.Fatalf("Update did not leave markers for the agent:\n%s", body)
	}
	// resubmitting before resolving is refused, not merged with markers in it
	rr := e.submit(b, "T2 again")
	if rr.Outcome != OutcomeRejected || !strings.Contains(rr.Reason, "conflict") {
		t.Fatalf("submission with unresolved markers: %+v", rr)
	}
	edit(t, b, "internal/core/core.go", strings.Replace(coreV1, "return 1", "return 300 // reconciled with be-1", 1))
	rr = e.submit(b, "T2 resolved")
	if !rr.Merged() {
		t.Fatalf("resolved submission: %+v", rr)
	}
	if got := readFile(t, filepath.Join(e.q.tree.Path, "internal/core/core.go")); !strings.Contains(got, "reconciled") {
		t.Fatalf("integration content: %s", got)
	}
	if got := readFile(t, filepath.Join(e.q.tree.Path, "docs/guide.md")); !strings.Contains(got, "by be-2") {
		t.Fatal("be-2's other change did not land")
	}
	e.integrationClean()
	// AbortUpdate abandons an Update (x started before all this landed)
	edit(t, x, "internal/core/core.go", strings.Replace(coreV1, "return 1", "return 7", 1))
	if cf, err := x.Update(tctx(t), e.q.Tip()); err != nil || cf == nil {
		t.Fatalf("Update: %v", err)
	}
	if err := x.AbortUpdate(tctx(t)); err != nil {
		t.Fatal(err)
	}
	if body := readFile(t, filepath.Join(x.Path, "internal/core/core.go")); hasMarkers([]byte(body)) || !strings.Contains(body, "return 7") {
		t.Fatalf("AbortUpdate:\n%s", body)
	}
}

func TestQueueConflictKinds(t *testing.T) {
	type scenario struct {
		name     string
		setup    func(t *testing.T, dir string)
		first    func(t *testing.T, tr *Tree)
		second   func(t *testing.T, tr *Tree)
		wantKind string
		binary   bool
		hunks    bool
	}
	scenarios := []scenario{
		{name: "deleted by us (modify/delete)", wantKind: "deleted-by-us",
			first:  func(t *testing.T, tr *Tree) { must(t, os.Remove(filepath.Join(tr.Path, "docs/guide.md"))) },
			second: func(t *testing.T, tr *Tree) { edit(t, tr, "docs/guide.md", "# guide edited\n") }},
		{name: "deleted by them (delete/modify)", wantKind: "deleted-by-them",
			first:  func(t *testing.T, tr *Tree) { edit(t, tr, "docs/guide.md", "# guide edited\n") },
			second: func(t *testing.T, tr *Tree) { must(t, os.Remove(filepath.Join(tr.Path, "docs/guide.md"))) }},
		{name: "add/add", wantKind: "add/add", hunks: true,
			first:  func(t *testing.T, tr *Tree) { edit(t, tr, "docs/new.md", "first version\nshared\n") },
			second: func(t *testing.T, tr *Tree) { edit(t, tr, "docs/new.md", "second version\nshared\n") }},
		{name: "binary", wantKind: "binary", binary: true,
			setup: func(t *testing.T, dir string) {
				must(t, os.WriteFile(filepath.Join(dir, "asset.bin"), []byte{0, 1, 2, 3, 0, 5}, 0o644))
				rawGit(t, dir, "add", "-A")
				rawGit(t, dir, "commit", "-qm", "asset")
			},
			first: func(t *testing.T, tr *Tree) {
				must(t, os.WriteFile(filepath.Join(tr.Path, "asset.bin"), []byte{0, 9, 9, 9, 0}, 0o644))
			},
			second: func(t *testing.T, tr *Tree) {
				must(t, os.WriteFile(filepath.Join(tr.Path, "asset.bin"), []byte{0, 7, 7, 7, 0}, 0o644))
			}},
		{name: "same lines edited", wantKind: "content", hunks: true,
			first: func(t *testing.T, tr *Tree) {
				edit(t, tr, "docs/guide.md", "# guide\n\nline one\nFIRST\nline three\nline four\nline five\n")
			},
			second: func(t *testing.T, tr *Tree) {
				edit(t, tr, "docs/guide.md", "# guide\n\nline one\nSECOND\nline three\nline four\nline five\n")
			}},
	}
	for _, sc := range scenarios {
		t.Run(sc.name, func(t *testing.T) {
			skipWithoutUnix(t)
			dir := newRepo(t)
			if sc.setup != nil {
				sc.setup(t, dir)
			}
			m := newManager(t, openRepo(t, dir))
			q := mustQueue(t, m, QueueOptions{})
			e := &queueEnv{t: t, dir: dir, repo: m.Repository(), m: m, q: q, log: &eventLog{}}
			a, b := e.agent("first"), e.agent("second")
			sc.first(t, a)
			sc.second(t, b)
			if r := e.submit(a, "one"); !r.Merged() {
				t.Fatalf("first: %+v", r)
			}
			r := e.submit(b, "two")
			if r.Outcome != OutcomeConflict {
				t.Fatalf("second: %+v", r)
			}
			d := r.Conflict.Details[0]
			if d.Kind != sc.wantKind || d.Binary != sc.binary || (len(r.Conflict.Hunks) > 0) != sc.hunks {
				t.Fatalf("details %+v hunks %d", d, len(r.Conflict.Hunks))
			}
			if d.Note == "" && !sc.hunks {
				t.Fatalf("no note for a conflict without hunks: %+v", d)
			}
			if !strings.Contains(r.Conflict.Suggest, "first") {
				t.Fatalf("Suggest does not name the agent that landed first:\n%s", r.Conflict.Suggest)
			}
			e.integrationClean()
		})
	}
}

func must(t testing.TB, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

func TestQueueConflictHunksAreCapped(t *testing.T) {
	skipWithoutUnix(t)
	dir := newRepo(t)
	// a file with many separate regions, edited differently by two agents
	var base, a1, a2 strings.Builder
	for i := 0; i < 80; i++ {
		fmt.Fprintf(&base, "unchanged filler line %d\nregion %d original\n", i, i)
		fmt.Fprintf(&a1, "unchanged filler line %d\nregion %d by first %s\n", i, i, strings.Repeat("x", 3000))
		fmt.Fprintf(&a2, "unchanged filler line %d\nregion %d by second\n", i, i)
	}
	writeFile(t, filepath.Join(dir, "big.txt"), base.String())
	rawGit(t, dir, "add", "-A")
	rawGit(t, dir, "commit", "-qm", "big")
	m := newManager(t, openRepo(t, dir))
	q := mustQueue(t, m, QueueOptions{})
	a := mustCreate(t, m, "first", CreateOptions{Base: q.Tip()})
	b := mustCreate(t, m, "second", CreateOptions{Base: q.Tip()})
	edit(t, a, "big.txt", a1.String())
	edit(t, b, "big.txt", a2.String())
	if r := mustSubmit(t, q, Submission{Tree: a}); !r.Merged() {
		t.Fatalf("first: %+v", r)
	}
	r := mustSubmit(t, q, Submission{Tree: b})
	if r.Outcome != OutcomeConflict {
		t.Fatalf("second: %+v", r)
	}
	c := r.Conflict
	if c.Details[0].HunkCount != 80 || len(c.Hunks) != maxHunksPerFile || !c.Truncated {
		t.Fatalf("HunkCount=%d kept=%d truncated=%v", c.Details[0].HunkCount, len(c.Hunks), c.Truncated)
	}
	for _, h := range c.Hunks {
		if len(h.Ours) > maxSnippetBytes || !h.Truncated {
			t.Fatalf("snippet not capped: %d bytes truncated=%v", len(h.Ours), h.Truncated)
		}
	}
	if len(c.Suggest) > 2000 {
		t.Fatalf("Suggest is %d bytes", len(c.Suggest))
	}
}

func TestQueueRollsBackAFailedVerification(t *testing.T) {
	e := newQueueEnv(t, QueueOptions{VerifyCmd: `echo "checking"; test ! -f bad.txt || { echo "bad.txt must not exist" >&2; exit 3; }`})
	a, b := e.agent("be-1"), e.agent("fe-1")
	edit(t, a, "bad.txt", "this breaks the build\n")
	edit(t, a, "internal/util/extra.go", "package util\n")
	edit(t, b, "docs/guide.md", "# guide\n\nline one\nline two\nline three\nline four\nline five\nby fe-1\n")
	tip0 := e.q.Tip()

	r := e.submit(a, "T1 bad")
	if r.Outcome != OutcomeVerifyFailed || !r.RolledBack || r.Verify == nil || r.Verify.OK() || r.Verify.ExitCode != 3 {
		t.Fatalf("result: %+v verify: %+v", r, r.Verify)
	}
	if !strings.Contains(r.Verify.Output, "checking") || !strings.Contains(r.Verify.Output, "bad.txt must not exist") {
		t.Fatalf("verifier output lost: %q", r.Verify.Output)
	}
	if r.After != tip0 || r.Before != tip0 || len(r.Files) != 0 {
		t.Fatalf("tips: %s → %s files %v", r.Before, r.After, r.Files)
	}
	var ve *VerifyError
	if !errors.As(r.Err(), &ve) || ve.Result.ExitCode != 3 || !strings.Contains(ve.Error(), "exit 3") {
		t.Fatalf("Err(): %v", r.Err())
	}
	if e.q.Tip() != tip0 {
		t.Fatal("tip moved after a failed verification")
	}
	e.integrationClean()
	if exists(filepath.Join(e.q.tree.Path, "bad.txt")) || exists(filepath.Join(e.q.tree.Path, "internal/util/extra.go")) {
		t.Fatal("rolled-back files remain in the integration tree")
	}
	// the submitter's own tree is untouched: it can fix and resubmit
	if !exists(filepath.Join(a.Path, "bad.txt")) {
		t.Fatal("the failed agent's tree was modified")
	}
	// events: verify_failed then rolled_back, with the output
	types := e.log.types()
	iv, ir := indexOf(types, EventVerifyFailed), indexOf(types, EventRolledBack)
	if iv < 0 || ir < iv || e.log.count(EventMerged) != 0 {
		t.Fatalf("events: %v", types)
	}
	ev, _ := e.log.first(EventVerifyFailed)
	if ev.Data["exit_code"] != 3 || !strings.Contains(ev.Data["output"].(string), "bad.txt must not exist") || ev.Data["timed_out"] != false {
		t.Fatalf("verify_failed event: %+v", ev)
	}
	if st := e.q.Status(); st.VerifyFailures != 1 || st.RolledBack != 1 || st.Merged != 0 {
		t.Fatalf("status: %+v", st)
	}
	// a good submission afterwards merges cleanly on the untouched tip
	if r2 := e.submit(b, "T2"); !r2.Merged() || r2.Before != tip0 {
		t.Fatalf("after rollback: %+v", r2)
	}
	e.integrationClean()
	// the agent fixes its tree and lands
	must(t, os.Remove(filepath.Join(a.Path, "bad.txt")))
	if r3 := e.submit(a, "T1 fixed"); !r3.Merged() {
		t.Fatalf("fixed: %+v", r3)
	}
	e.integrationClean()
}

func indexOf(list []string, s string) int {
	for i, x := range list {
		if x == s {
			return i
		}
	}
	return -1
}

func TestQueueCleansWhatTheVerifierLeavesBehind(t *testing.T) {
	// The verifier builds things (untracked files, edits to tracked files). A failure
	// rolls all of it back; a success must not poison the next merge either.
	e := newQueueEnv(t, QueueOptions{})
	a, b := e.agent("a"), e.agent("b")
	edit(t, a, "a.txt", "a\n")
	edit(t, b, "artifact.txt", "b's real file\n")

	r := mustSubmit(t, e.q, Submission{Tree: a, VerifyCmd: "touch artifact.txt built.o; echo scribble >> README.md; exit 1"})
	if r.Outcome != OutcomeVerifyFailed {
		t.Fatalf("a: %+v", r)
	}
	e.integrationClean()
	if exists(filepath.Join(e.q.tree.Path, "artifact.txt")) || exists(filepath.Join(e.q.tree.Path, "built.o")) {
		t.Fatal("verifier's untracked files survived the rollback")
	}
	if readFile(t, filepath.Join(e.q.tree.Path, "README.md")) != "# project\n" {
		t.Fatal("verifier's edit to a tracked file survived the rollback")
	}
	// success that leaves an untracked artifact which the next submission wants to add
	r = mustSubmit(t, e.q, Submission{Tree: a, VerifyCmd: "touch artifact.txt"})
	if !r.Merged() {
		t.Fatalf("a again: %+v", r)
	}
	r = e.submit(b, "b")
	if !r.Merged() {
		t.Fatalf("an untracked leftover from the previous verification blocked the next merge: %+v", r)
	}
	if readFile(t, filepath.Join(e.q.tree.Path, "artifact.txt")) != "b's real file\n" {
		t.Fatal("wrong artifact content")
	}
	e.integrationClean()
}

func TestQueueVerifierTimeoutOutputCapAndProcessGroup(t *testing.T) {
	skipWithoutUnix(t)
	e := newQueueEnv(t, QueueOptions{VerifyTimeout: 500 * time.Millisecond, MaxVerifyOutput: 4096})
	a := e.agent("a")
	edit(t, a, "a.txt", "a\n")
	pidFile := filepath.Join(t.TempDir(), "child.pid")

	start := time.Now()
	r := mustSubmit(t, e.q, Submission{Tree: a, Task: "hang",
		VerifyCmd: fmt.Sprintf(`echo START; (sleep 60 & echo $! > %q; wait) & sleep 60`, pidFile)})
	if r.Outcome != OutcomeVerifyFailed || r.Verify == nil || !r.Verify.TimedOut || r.Verify.OK() {
		t.Fatalf("timeout not reported: %+v %+v", r, r.Verify)
	}
	if took := time.Since(start); took > 15*time.Second {
		t.Fatalf("a hung verifier held the queue for %s", took)
	}
	if !strings.Contains(r.Verify.Summary(), "timed out") {
		t.Fatalf("summary: %s", r.Verify.Summary())
	}
	e.integrationClean()
	// no descendant survives: the whole process group was killed
	pidText := strings.TrimSpace(readFile(t, pidFile))
	waitFor(t, func() bool { return !processAlive(pidText) })

	// output cap: head and tail kept, the middle elided, the run still judged by its exit status
	r = mustSubmit(t, e.q, Submission{Tree: a, Task: "noisy",
		VerifyCmd: `echo START-OF-OUTPUT; i=0; while [ $i -lt 3000 ]; do echo "noise line $i xxxxxxxxxxxxxxxxxxxxxxxxxxxxxx"; i=$((i+1)); done; echo END-OF-OUTPUT; exit 2`})
	if r.Outcome != OutcomeVerifyFailed || r.Verify.ExitCode != 2 || !r.Verify.Truncated {
		t.Fatalf("noisy: %+v %+v", r, r.Verify)
	}
	if len(r.Verify.Output) > 4096+200 || !strings.Contains(r.Verify.Output, "START-OF-OUTPUT") || !strings.Contains(r.Verify.Output, "END-OF-OUTPUT") || !strings.Contains(r.Verify.Output, "bytes omitted") {
		t.Fatalf("output cap: %d bytes\n%.300s ... %s", len(r.Verify.Output), r.Verify.Output, r.Verify.Output[max(0, len(r.Verify.Output)-100):])
	}
	// a background job left running by a *passing* verification is reaped too
	pidFile2 := filepath.Join(t.TempDir(), "bg.pid")
	r = mustSubmit(t, e.q, Submission{Tree: a, VerifyCmd: fmt.Sprintf(`sleep 60 & echo $! > %q`, pidFile2)})
	if !r.Merged() {
		t.Fatalf("passing verification with a background job: %+v", r)
	}
	waitFor(t, func() bool { return !processAlive(strings.TrimSpace(readFile(t, pidFile2))) })
}

func processAlive(pidText string) bool {
	pid, err := strconv.Atoi(pidText)
	if err != nil || pid <= 0 {
		return false
	}
	return pidExists(pid)
}

func TestQueueVerifierEnvironment(t *testing.T) {
	skipWithoutUnix(t)
	t.Setenv("GIT_DIR", "/nonexistent")
	t.Setenv("GIT_WORK_TREE", "/nonexistent")
	t.Setenv("GITHUB_TOKEN", "ghp_verysecret")
	t.Setenv("MY_API_KEY", "sk-verysecret")
	t.Setenv("DATABASE_PASSWORD", "pw")
	t.Setenv("HARMLESS_SETTING", "keep-me")
	e := newQueueEnv(t, QueueOptions{VerifyEnv: []string{"EXTRA_FROM_HARNESS=1"}})
	a := e.agent("be-1")
	edit(t, a, "a.txt", "a\n")
	r := mustSubmit(t, e.q, Submission{Tree: a, Task: "T-42 env check",
		VerifyCmd: `env | sort; echo "cwd=$(pwd)"; echo "toplevel=$(git rev-parse --show-toplevel)"; test -f go.mod; exit 9`})
	out := r.Verify.Output
	for _, bad := range []string{"GIT_DIR=", "GIT_WORK_TREE=", "ghp_verysecret", "sk-verysecret", "DATABASE_PASSWORD", "GITHUB_TOKEN", "MY_API_KEY"} {
		if strings.Contains(out, bad) {
			t.Errorf("verifier environment leaks %q", bad)
		}
	}
	for _, want := range []string{"HARMLESS_SETTING=keep-me", "EXTRA_FROM_HARNESS=1", "SLEIPNIR_AGENT=be-1", "SLEIPNIR_TASK=T-42 env check", "SLEIPNIR_WORKSPACE=" + e.q.tree.Path,
		"cwd=" + e.q.tree.Path, "toplevel=" + e.q.tree.Path, "GIT_TERMINAL_PROMPT=0"} {
		if !strings.Contains(out, want) {
			t.Errorf("verifier environment lacks %q", want)
		}
	}
	if r.Verify.ExitCode != 9 {
		t.Fatalf("exit code %d, output:\n%s", r.Verify.ExitCode, out)
	}
}

func TestQueueUsesAnInjectedVerifier(t *testing.T) {
	var got []VerifyRequest
	var seenContent string
	fake := func(ctx context.Context, req VerifyRequest) VerifyResult {
		got = append(got, req)
		b, _ := os.ReadFile(filepath.Join(req.Dir, "a.txt"))
		seenContent = string(b)
		if strings.Contains(req.Task, "reject") {
			return VerifyResult{Cmd: req.Cmd, ExitCode: 1, Output: "no thanks"}
		}
		return VerifyResult{Cmd: req.Cmd, ExitCode: 0, Output: "fine"}
	}
	e := newQueueEnv(t, QueueOptions{VerifyCmd: "default-cmd", Verify: fake})
	a := e.agent("a")
	edit(t, a, "a.txt", "content under test\n")
	r := e.submit(a, "accept me")
	if !r.Merged() || r.Verify.Output != "fine" {
		t.Fatalf("result: %+v", r)
	}
	if len(got) != 1 || got[0].Dir != e.q.tree.Path || got[0].Cmd != "default-cmd" || got[0].Agent != "a" || got[0].Task != "accept me" ||
		got[0].Timeout != 10*time.Minute || got[0].MaxOutput != 64<<10 || seenContent != "content under test\n" {
		t.Fatalf("request: %+v (verifier saw %q)", got, seenContent)
	}
	// a per-submission command overrides the default
	b := e.agent("b")
	edit(t, b, "b.txt", "b\n")
	r = mustSubmit(t, e.q, Submission{Tree: b, Task: "reject me", VerifyCmd: "special-cmd"})
	if r.Outcome != OutcomeVerifyFailed || got[1].Cmd != "special-cmd" {
		t.Fatalf("second: %+v / %+v", r, got)
	}
	e.integrationClean()
	// a verifier that could not run at all is a failed verification, not a merge
	e2 := newQueueEnv(t, QueueOptions{VerifyCmd: "x", Verify: func(ctx context.Context, req VerifyRequest) VerifyResult {
		return VerifyResult{Cmd: req.Cmd, ExitCode: -1, Err: errors.New("sh: not found")}
	}})
	c := e2.agent("c")
	edit(t, c, "c.txt", "c\n")
	if r := e2.submit(c, "c"); r.Outcome != OutcomeVerifyFailed || !strings.Contains(r.Verify.Summary(), "could not run") {
		t.Fatalf("unrunnable verifier: %+v", r)
	}
	e2.integrationClean()
}

func TestQueueSemanticConflictIsCaughtByVerification(t *testing.T) {
	// Two changes that merge cleanly as text but break an invariant together.
	e := newQueueEnv(t, QueueOptions{VerifyCmd: `[ "$(ls locks 2>/dev/null | wc -l)" -le 1 ]`})
	a, b := e.agent("a"), e.agent("b")
	edit(t, a, "locks/LOCK-a", "a\n")
	edit(t, b, "locks/LOCK-b", "b\n")
	if r := e.submit(a, "a"); !r.Merged() {
		t.Fatalf("a: %+v", r)
	}
	r := e.submit(b, "b")
	if r.Outcome != OutcomeVerifyFailed || !r.RolledBack {
		t.Fatalf("b must fail verification on the merged stack: %+v", r)
	}
	if exists(filepath.Join(e.q.tree.Path, "locks", "LOCK-b")) {
		t.Fatal("rolled back file present")
	}
	e.integrationClean()
}

func TestQueueRebaseStrategy(t *testing.T) {
	e := newQueueEnv(t, QueueOptions{Strategy: StrategyRebase, VerifyCmd: "true"})
	a, b := e.agent("a"), e.agent("b")
	edit(t, a, "a1.txt", "a1\n")
	if _, err := a.Commit(tctx(t), "a: first"); err != nil {
		t.Fatal(err)
	}
	edit(t, a, "a2.txt", "a2\n")
	edit(t, b, "b1.txt", "b1\n")
	if r := e.submit(a, "a"); !r.Merged() || r.Commits != 2 {
		t.Fatalf("a: %+v", r)
	}
	if r := e.submit(b, "b"); !r.Merged() {
		t.Fatalf("b: %+v", r)
	}
	// history is linear: no merge commits, every commit replayed once
	merges, _ := e.q.tree.repo.Git(tctx(t), "rev-list", "--merges", e.q.Base()+".."+e.q.Tip())
	if strings.TrimSpace(merges.Stdout) != "" {
		t.Fatalf("rebase strategy produced merge commits: %s", merges.Stdout)
	}
	log, _ := e.repo.Log(tctx(t), gitx.LogOptions{Range: e.q.Base() + ".." + e.q.Tip()})
	var subjects []string
	for _, c := range log {
		subjects = append(subjects, c.Subject)
	}
	if len(log) != 3 || !strings.Contains(strings.Join(subjects, "|"), "a: first") {
		t.Fatalf("history: %v", subjects)
	}
	e.integrationClean()
	// a conflicting rebase aborts cleanly too
	x, y := e.agent("x"), e.agent("y")
	edit(t, x, "docs/guide.md", "# guide\n\nline one\nX\nline three\nline four\nline five\n")
	edit(t, y, "docs/guide.md", "# guide\n\nline one\nY\nline three\nline four\nline five\n")
	if r := e.submit(x, "x"); !r.Merged() {
		t.Fatalf("x: %+v", r)
	}
	tip := e.q.Tip()
	r := e.submit(y, "y")
	if r.Outcome != OutcomeConflict || len(r.Conflict.Hunks) != 1 || !strings.Contains(r.Conflict.Hunks[0].Ours, "X") || !strings.Contains(r.Conflict.Hunks[0].Theirs, "Y") {
		t.Fatalf("y: %+v", r)
	}
	if e.q.Tip() != tip {
		t.Fatal("tip moved")
	}
	e.integrationClean()
}

func TestQueueNoFFAndFastForward(t *testing.T) {
	for _, noff := range []bool{false, true} {
		t.Run(fmt.Sprintf("NoFF=%v", noff), func(t *testing.T) {
			e := newQueueEnv(t, QueueOptions{NoFF: noff})
			a := e.agent("a")
			edit(t, a, "a.txt", "a\n")
			r := e.submit(a, "a")
			if !r.Merged() {
				t.Fatalf("%+v", r)
			}
			head, _ := a.Head(tctx(t))
			c, _ := e.repo.CommitInfo(tctx(t), e.q.Tip())
			if noff {
				if r.After == head || len(c.Parents) != 2 || !strings.HasPrefix(c.Subject, "Merge a") {
					t.Fatalf("NoFF must create a merge commit: tip %s agent head %s parents %v subject %q", r.After, head, c.Parents, c.Subject)
				}
			} else if r.After != head {
				t.Fatalf("an unmoved tip should fast-forward: %s vs %s", r.After, head)
			}
		})
	}
}

func TestQueueEmptyAndRejectedSubmissions(t *testing.T) {
	e := newQueueEnv(t, QueueOptions{})
	a := e.agent("a")
	r := e.submit(a, "nothing")
	if r.Outcome != OutcomeEmpty || r.Err() != nil || r.Reason == "" || e.q.Tip() != e.q.Base() {
		t.Fatalf("empty tree: %+v", r)
	}
	edit(t, a, "a.txt", "a\n")
	if r := e.submit(a, "a"); !r.Merged() {
		t.Fatalf("%+v", r)
	}
	if r := e.submit(a, "a again"); r.Outcome != OutcomeEmpty || r.Reason == "" {
		t.Fatalf("resubmission: %+v", r)
	}
	// a tree that merged the tip in and added nothing of its own has nothing to land
	b := e.agent("b")
	if _, err := b.Update(tctx(t), e.q.Tip()); err != nil {
		t.Fatal(err)
	}
	if r := e.submit(b, "b"); r.Outcome != OutcomeEmpty {
		t.Fatalf("no-op tree: %+v", r)
	}
	if st := e.q.Status(); st.Empty != 3 || st.Merged != 1 {
		t.Fatalf("status: %+v", st)
	}

	// rejections
	strict := newQueueEnv(t, QueueOptions{NoAutoCommit: true})
	c := strict.agent("c")
	edit(t, c, "c.txt", "c\n")
	if r := strict.submit(c, "c"); r.Outcome != OutcomeRejected || !strings.Contains(r.Reason, "uncommitted") {
		t.Fatalf("NoAutoCommit: %+v", r)
	}
	if _, err := c.Commit(tctx(t), "c"); err != nil {
		t.Fatal(err)
	}
	if r := strict.submit(c, "c"); !r.Merged() {
		t.Fatalf("committed tree: %+v", r)
	}
	if st := strict.q.Status(); st.Rejected != 1 {
		t.Fatalf("rejected count: %+v", st)
	}
	strict.integrationClean()

	// oversize files and scope violations are refused when the queue commits for the agent
	auto := newQueueEnv(t, QueueOptions{})
	auto.m.MaxFileBytes = 500
	d := auto.agent("d")
	edit(t, d, "huge.bin", strings.Repeat("x", 4000))
	if r := auto.submit(d, "d"); r.Outcome != OutcomeRejected || !strings.Contains(r.Reason, "huge.bin") {
		t.Fatalf("oversize: %+v", r)
	}
	f := auto.agent("f")
	edit(t, f, "internal/core/core.go", "package core\n")
	edit(t, f, "docs/other.md", "x\n")
	edit(t, f, "cmd/app/main.go", "package main\n")
	rr := mustSubmit(t, auto.q, Submission{Tree: f, Task: "f", Scope: []string{"internal/core/**"}, EnforceScope: true, Message: "f work"})
	if rr.Outcome != OutcomeRejected || !strings.Contains(rr.Reason, "docs/other.md") || !strings.Contains(rr.Reason, "cmd/app/main.go") || strings.Contains(rr.Reason, "internal/core") {
		t.Fatalf("scope: %+v", rr)
	}
	if strings.Join(rr.Files, ",") != "cmd/app/main.go,docs/other.md" {
		t.Fatalf("out-of-scope files: %v", rr.Files)
	}
	// without EnforceScope the scope is advisory
	if r := mustSubmit(t, auto.q, Submission{Tree: f, Scope: []string{"internal/core/**"}}); !r.Merged() {
		t.Fatalf("advisory scope: %+v", r)
	}
	if st := auto.q.Status(); st.Rejected != 2 {
		t.Fatalf("rejected count: %+v", st)
	}
	if auto.log.count(EventRejected) < 2 {
		t.Fatalf("events: %v", auto.log.types())
	}
	auto.integrationClean()
}

func TestQueueScopeJudgesOnlyTheSubmissionsOwnChanges(t *testing.T) {
	e := newQueueEnv(t, QueueOptions{})
	a, b := e.agent("a"), e.agent("b")
	edit(t, a, "docs/a-only.md", "a\n")
	if r := e.submit(a, "a"); !r.Merged() {
		t.Fatalf("a: %+v", r)
	}
	// b brings a's work into its tree, then does its own work inside its scope
	if _, err := b.Update(tctx(t), e.q.Tip()); err != nil {
		t.Fatal(err)
	}
	edit(t, b, "internal/util/b.go", "package util\n")
	r := mustSubmit(t, e.q, Submission{Tree: b, Task: "b", Scope: []string{"internal/util/**"}, EnforceScope: true})
	if !r.Merged() {
		t.Fatalf("a's files (merged into b's tree) must not count against b's scope: %+v", r)
	}
	// Assert on the tree itself sees the merged-in files (relative to its base) - documented difference
	out, _ := Assert(tctx(t), b, []string{"internal/util/**"})
	if len(out) != 1 || out[0] != "docs/a-only.md" {
		t.Logf("Assert on a tree that merged others' work: %v", out)
	}
}

func TestQueueContextCancellation(t *testing.T) {
	e := newQueueEnv(t, QueueOptions{})
	gate := filepath.Join(t.TempDir(), "gate")
	holder, waiter := e.agent("holder"), e.agent("waiter")
	edit(t, holder, "h.txt", "h\n")
	edit(t, waiter, "w.txt", "w\n")

	// A submission that is waiting in line can be abandoned.
	var wg sync.WaitGroup
	var hr *Result
	wg.Add(1)
	go func() {
		defer wg.Done()
		hr = mustSubmit(t, e.q, Submission{Tree: holder, VerifyCmd: fmt.Sprintf("while [ ! -f %q ]; do sleep 0.02; done", gate)})
	}()
	waitFor(t, func() bool { return e.q.Status().Active != nil })
	ctx, cancel := context.WithCancel(context.Background())
	errc := make(chan error, 1)
	go func() {
		_, err := e.q.Submit(ctx, Submission{Tree: waiter})
		errc <- err
	}()
	waitFor(t, func() bool { return len(e.q.Status().Waiting) == 1 })
	cancel()
	if err := <-errc; !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled waiter: %v", err)
	}
	if n := len(e.q.Status().Waiting); n != 0 {
		t.Fatalf("canceled submission still waiting (%d)", n)
	}
	must(t, os.WriteFile(gate, nil, 0o600))
	wg.Wait()
	if !hr.Merged() {
		t.Fatalf("holder: %+v", hr)
	}
	// the abandoned tree was untouched by the queue and can still be submitted
	if r := e.submit(waiter, "waiter"); !r.Merged() {
		t.Fatalf("waiter: %+v", r)
	}

	// Cancelling in the middle of a verification rolls the integration tree back
	// even though the caller's context is gone.
	x := e.agent("x")
	edit(t, x, "x.txt", "x\n")
	tip := e.q.Tip()
	ctx2, cancel2 := context.WithCancel(context.Background())
	go func() { time.Sleep(300 * time.Millisecond); cancel2() }()
	start := time.Now()
	_, err := e.q.Submit(ctx2, Submission{Tree: x, VerifyCmd: "sleep 30"})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled verification: %v", err)
	}
	if time.Since(start) > 10*time.Second {
		t.Fatal("cancellation was not prompt")
	}
	if e.q.Tip() != tip {
		t.Fatal("tip moved")
	}
	e.integrationClean()
	if st := e.q.Status(); !st.Healthy || st.Active != nil {
		t.Fatalf("status after cancel: %+v", st)
	}
	if r := e.submit(x, "x again"); !r.Merged() {
		t.Fatalf("x after a canceled attempt: %+v", r)
	}
}

func TestQueueSurvivesRandomCancellations(t *testing.T) {
	e := newQueueEnv(t, QueueOptions{})
	const n = 30
	trees := make([]*Tree, n)
	for i := range trees {
		trees[i] = e.agent(fmt.Sprintf("s-%02d", i))
		edit(t, trees[i], fmt.Sprintf("stress/s-%02d.txt", i), "s\n")
	}
	rng := rand.New(rand.NewSource(3))
	var wg sync.WaitGroup
	var mu sync.Mutex
	merged := 0
	for i := 0; i < n; i++ {
		delay := time.Duration(rng.Intn(400)) * time.Millisecond
		ctx, cancel := context.WithTimeout(context.Background(), delay+time.Duration(rng.Intn(50))*time.Millisecond)
		wg.Add(1)
		go func() {
			defer wg.Done()
			defer cancel()
			r, err := e.q.Submit(ctx, Submission{Tree: trees[i]})
			if err == nil && r.Merged() {
				mu.Lock()
				merged++
				mu.Unlock()
			}
		}()
	}
	wg.Wait()
	// whatever was canceled or landed, the queue is idle, healthy and consistent
	st := e.q.Status()
	if st.Active != nil || len(st.Waiting) != 0 || !st.Healthy || st.Merged != merged {
		t.Fatalf("status after the storm: %+v (merged counted %d)", st, merged)
	}
	e.integrationClean()
	// and a fresh submission still goes through
	z := e.agent("z")
	edit(t, z, "z.txt", "z\n")
	if r := e.submit(z, "z"); !r.Merged() {
		t.Fatalf("after the storm: %+v", r)
	}
	e.integrationClean()
}

func TestQueueRecoversAFromDamageToItsTree(t *testing.T) {
	e := newQueueEnv(t, QueueOptions{VerifyCmd: "true"})
	a, b, c := e.agent("a"), e.agent("b"), e.agent("c")
	edit(t, a, "a.txt", "a\n")
	edit(t, b, "b.txt", "b\n")
	edit(t, c, "c.txt", "c\n")
	if r := e.submit(a, "a"); !r.Merged() {
		t.Fatal("a")
	}
	// someone scribbles in the integration tree
	writeFile(t, filepath.Join(e.q.tree.Path, "junk.txt"), "junk\n")
	writeFile(t, filepath.Join(e.q.tree.Path, "README.md"), "# scribble\n")
	if r := e.submit(b, "b"); !r.Merged() {
		t.Fatalf("b after scribbles: %+v", r)
	}
	e.integrationClean()
	if exists(filepath.Join(e.q.tree.Path, "junk.txt")) {
		t.Fatal("junk survived")
	}
	// the integration tree directory is deleted outright
	oldPath := e.q.tree.Path
	must(t, os.RemoveAll(oldPath))
	r := e.submit(c, "c")
	if !r.Merged() {
		t.Fatalf("c after the integration tree vanished: %+v", r)
	}
	e.integrationClean()
	if e.q.tree.Path != oldPath || !exists(filepath.Join(oldPath, "c.txt")) || !exists(filepath.Join(oldPath, "a.txt")) {
		t.Fatal("integration tree was not rebuilt at the same place with all the work")
	}
	// a queue marked broken repairs itself before accepting more
	e.q.mu.Lock()
	e.q.broken = errors.New("simulated failure")
	e.q.mu.Unlock()
	if st := e.q.Status(); st.Healthy || !strings.Contains(st.Broken, "simulated") {
		t.Fatalf("status: %+v", st)
	}
	d := e.agent("d")
	edit(t, d, "d.txt", "d\n")
	if r := e.submit(d, "d"); !r.Merged() {
		t.Fatalf("d after repair: %+v", r)
	}
	if !e.q.Status().Healthy {
		t.Fatal("queue still unhealthy")
	}
	must(t, e.q.Repair(tctx(t)))
	e.integrationClean()
}

func TestQueueBranchMovedUnderneath(t *testing.T) {
	e := newQueueEnv(t, QueueOptions{})
	a := e.agent("a")
	edit(t, a, "a.txt", "a\n")
	// somebody moves the integration branch behind the queue's back
	head, _ := e.repo.Head(tctx(t))
	rawGit(t, e.dir, "update-ref", "refs/heads/"+e.q.Branch(), head, e.q.Tip())
	other := e.repo
	_ = other
	tip := e.q.Tip()
	rawGit(t, e.dir, "update-ref", "refs/heads/"+e.q.Branch(), rawGit(t, e.dir, "commit-tree", "-p", head, "-m", "meddling", head+"^{tree}"))
	_, err := e.q.Submit(tctx(t), Submission{Tree: a})
	if err == nil || !strings.Contains(err.Error(), "moved underneath") {
		t.Fatalf("want a CAS failure, got %v", err)
	}
	if e.q.Tip() != tip {
		t.Fatal("the queue's tip moved although publishing failed")
	}
	if h, _ := e.q.tree.repo.Head(tctx(t)); h != tip {
		t.Fatal("integration tree not restored after a failed publish")
	}
}

func TestQueueFinishAndFastForward(t *testing.T) {
	e := newQueueEnv(t, QueueOptions{})
	if p, err := e.q.Finish(tctx(t)); err != nil || p != "" {
		t.Fatalf("Finish with nothing merged: %q, %v", p, err)
	}
	a := e.agent("a")
	edit(t, a, "docs/guide.md", "# guide\n\nline one\nline two\nline three\nline four\nline five\nline six\n")
	if r := e.submit(a, "a"); !r.Merged() {
		t.Fatalf("%+v", r)
	}
	// dirty user checkout: refused, nothing changes
	writeFile(t, filepath.Join(e.dir, "README.md"), "# user is editing\n")
	headBefore, _ := e.repo.Head(tctx(t))
	if _, err := e.q.FastForward(tctx(t)); !errors.Is(err, ErrDirty) {
		t.Fatalf("dirty checkout: %v", err)
	}
	if h, _ := e.repo.Head(tctx(t)); h != headBefore || readFile(t, filepath.Join(e.dir, "README.md")) != "# user is editing\n" {
		t.Fatal("a refused fast-forward changed the checkout")
	}
	// an untracked file counts as dirty as well
	rawGit(t, e.dir, "checkout", "-q", "--", "README.md")
	writeFile(t, filepath.Join(e.dir, "untracked.txt"), "u\n")
	if _, err := e.q.FastForward(tctx(t)); !errors.Is(err, ErrDirty) {
		t.Fatalf("untracked file: %v", err)
	}
	must(t, os.Remove(filepath.Join(e.dir, "untracked.txt")))
	// diverged user branch: refused
	writeFile(t, filepath.Join(e.dir, "user.txt"), "user commit\n")
	rawGit(t, e.dir, "add", "-A")
	rawGit(t, e.dir, "commit", "-qm", "user commit after the session started")
	if _, err := e.q.FastForward(tctx(t)); !errors.Is(err, ErrNotFastForward) {
		t.Fatalf("diverged branch: %v", err)
	}
	rawGit(t, e.dir, "reset", "-q", "--hard", headBefore)
	// clean and behind: fast-forwards, and the working tree follows
	ff, err := e.q.FastForward(tctx(t))
	if err != nil || ff.Branch != "main" || ff.From != headBefore || ff.To != e.q.Tip() {
		t.Fatalf("FastForward: %+v, %v", ff, err)
	}
	if h, _ := e.repo.Head(tctx(t)); h != e.q.Tip() || !strings.Contains(readFile(t, filepath.Join(e.dir, "docs/guide.md")), "line six") {
		t.Fatal("the checkout did not follow")
	}
	if ok, _ := e.repo.IsClean(tctx(t)); !ok {
		t.Fatal("checkout dirty after fast-forward")
	}
	// again: nothing to do
	if ff2, err := e.q.FastForward(tctx(t)); err != nil || ff2.Branch != "" || ff2.From != ff2.To {
		t.Fatalf("second FastForward: %+v, %v", ff2, err)
	}
}

func TestQueueFinishPatchOnADirtyCheckout(t *testing.T) {
	// The user keeps editing while agents work. With Snapshot the agents start from
	// the user's state, and Finish's patch (only what agents added) applies on top
	// of the user's uncommitted edits.
	dir := newRepo(t)
	repo := openRepo(t, dir)
	writeFile(t, filepath.Join(dir, "README.md"), "# user edit in progress\n")
	writeFile(t, filepath.Join(dir, "wip.txt"), "wip\n")
	m := newManager(t, repo)
	m.Snapshot = true
	q := mustQueue(t, m, QueueOptions{})
	a := mustCreate(t, m, "a", CreateOptions{Base: q.Tip()})
	if readFile(t, filepath.Join(a.Path, "wip.txt")) != "wip\n" {
		t.Fatal("agent does not see the user's work in progress")
	}
	edit(t, a, "docs/guide.md", "# guide\n\nline one\nline two\nline three\nline four\nline five\nagent line\n")
	if r := mustSubmit(t, q, Submission{Tree: a}); !r.Merged() {
		t.Fatalf("%+v", r)
	}
	patch, err := q.Finish(tctx(t))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(patch, "user edit in progress") || strings.Contains(patch, "wip.txt") || !strings.Contains(patch, "agent line") {
		t.Fatalf("the patch must contain only the agents' work:\n%s", patch)
	}
	if err := repo.Apply(tctx(t), patch, false); err != nil {
		t.Fatalf("patch does not apply on top of the user's uncommitted work: %v", err)
	}
	if readFile(t, filepath.Join(dir, "README.md")) != "# user edit in progress\n" || !strings.Contains(readFile(t, filepath.Join(dir, "docs/guide.md")), "agent line") {
		t.Fatal("result after applying")
	}
	// fast-forward is refused (the checkout is dirty) and points at the patch
	if _, err := q.FastForward(tctx(t)); !errors.Is(err, ErrDirty) {
		t.Fatalf("FastForward on a dirty checkout: %v", err)
	}
}

func TestQueueFastForwardIgnoresOurOwnDirectory(t *testing.T) {
	skipWithoutUnix(t)
	dir := newRepo(t)
	repo := openRepo(t, dir)
	// the trees live inside the repository (and are not git-ignored)
	m := &Manager{Repo: repo, Dir: filepath.Join(dir, ".sleipnir", "trees"), Prefix: "sleipnir/s1", Clock: testClock()}
	q := mustQueue(t, m, QueueOptions{})
	a := mustCreate(t, m, "a", CreateOptions{Base: q.Tip()})
	edit(t, a, "a.txt", "a\n")
	if r := mustSubmit(t, q, Submission{Tree: a}); !r.Merged() {
		t.Fatalf("%+v", r)
	}
	st, _ := repo.Status(tctx(t))
	if len(st.Untracked) == 0 {
		t.Fatal("test setup: expected the trees directory to show up as untracked")
	}
	if _, err := q.FastForward(tctx(t)); err != nil {
		t.Fatalf("our own untracked directory blocked the fast-forward: %v", err)
	}
	if !exists(filepath.Join(dir, "a.txt")) {
		t.Fatal("not applied")
	}
}

func TestQueueRacesUnderLoad(t *testing.T) {
	e := newQueueEnv(t, QueueOptions{VerifyCmd: "test -f go.mod"})
	const n = 12
	trees := make([]*Tree, n)
	for i := range trees {
		trees[i] = e.agent(fmt.Sprintf("r-%02d", i))
		edit(t, trees[i], fmt.Sprintf("load/r-%02d.txt", i), fmt.Sprintf("r %d\n", i))
	}
	stop := make(chan struct{})
	var pollers sync.WaitGroup
	for i := 0; i < 3; i++ {
		pollers.Add(1)
		go func() {
			defer pollers.Done()
			for {
				select {
				case <-stop:
					return
				default:
					st := e.q.Status()
					_ = e.q.Tip()
					if len(st.Landed) > n {
						t.Error("more landed than submitted")
					}
					time.Sleep(time.Millisecond)
				}
			}
		}()
	}
	var wg sync.WaitGroup
	results := make([]*Result, n)
	for i := range trees {
		wg.Add(1)
		go func() {
			defer wg.Done()
			results[i], _ = e.q.Submit(tctx(t), Submission{Tree: trees[i], Task: fmt.Sprintf("r-%d", i)})
		}()
	}
	wg.Wait()
	close(stop)
	pollers.Wait()
	prev := e.q.Base()
	for i, r := range results {
		if r == nil || !r.Merged() {
			t.Fatalf("result %d: %+v", i, r)
		}
	}
	// the ledger is a strict chain of ancestors: every tip descends from the previous
	for _, l := range e.q.Status().Landed {
		if l.Before != prev {
			t.Fatalf("ledger does not chain: %s after %s (before=%s)", l.Commit, prev, l.Before)
		}
		if ok, err := e.repo.IsAncestor(tctx(t), l.Before, l.Commit); err != nil || !ok {
			t.Fatalf("%s is not a descendant of %s", l.Commit, l.Before)
		}
		prev = l.Commit
	}
	if prev != e.q.Tip() {
		t.Fatal("ledger end != tip")
	}
	files := projectFiles(t, e.q.tree.Path)
	for i := 0; i < n; i++ {
		if _, ok := files[fmt.Sprintf("load/r-%02d.txt", i)]; !ok {
			t.Errorf("work of r-%02d missing", i)
		}
	}
	e.integrationClean()
}

func TestQueueRefusesForeignAndIntegrationTrees(t *testing.T) {
	e := newQueueEnv(t, QueueOptions{})
	other := newManager(t, e.repo)
	other.Prefix = "sleipnir/s5"
	x := mustCreate(t, other, "x")
	if _, err := e.q.Submit(tctx(t), Submission{Tree: x}); !errors.Is(err, ErrForeign) {
		t.Fatalf("tree of another manager: %v", err)
	}
	if _, err := e.q.Submit(tctx(t), Submission{Tree: e.q.tree}); !errors.Is(err, ErrForeign) {
		t.Fatalf("the integration tree itself: %v", err)
	}
	if _, err := e.q.Submit(tctx(t), Submission{}); err == nil {
		t.Fatal("nil tree accepted")
	}
	a := e.agent("a")
	must(t, a.Remove(tctx(t), true))
	if _, err := e.q.Submit(tctx(t), Submission{Tree: a}); !errors.Is(err, ErrRemoved) {
		t.Fatalf("removed tree: %v", err)
	}
	// Close: the integration branch (the result) stays, the tree goes, submissions stop
	b := e.agent("b")
	edit(t, b, "b.txt", "b\n")
	if r := e.submit(b, "b"); !r.Merged() {
		t.Fatal("b")
	}
	path := e.q.tree.Path
	must(t, e.q.Close(tctx(t)))
	if exists(path) {
		t.Fatal("integration tree survived Close")
	}
	if sha, err := e.repo.BranchSHA(tctx(t), e.q.Branch()); err != nil || sha != e.q.Tip() {
		t.Fatal("integration branch lost on Close")
	}
	if _, err := e.q.Submit(tctx(t), Submission{Tree: b}); !errors.Is(err, ErrClosed) {
		t.Fatalf("Submit after Close: %v", err)
	}
	if e.q.Status().Healthy {
		t.Fatal("a closed queue must not report healthy")
	}
	must(t, e.q.Close(tctx(t))) // idempotent
	// a second queue on the same manager cannot start over a branch that holds work
	if _, err := NewQueue(tctx(t), e.m, QueueOptions{}); !errors.Is(err, ErrExists) {
		t.Fatalf("second queue: %v", err)
	}
}

// The integration tree belongs to the queue. What a swarm iterates over to look
// after its agents (Trees, Get) never hands it out, List labels it, and shutting
// the agents down with Manager.Close does not pull it from under the queue.
func TestTheIntegrationTreeBelongsToTheQueueNotToTheAgents(t *testing.T) {
	e := newQueueEnv(t, QueueOptions{})
	a := e.agent("a")
	if ts := e.m.Trees(); len(ts) != 1 || ts[0] != a {
		t.Fatalf("Trees: %v", ts)
	}
	if got, ok := e.m.Get(integrationName); ok {
		t.Fatalf("Get(%q) handed out the integration tree: %v", integrationName, got)
	}
	infos, err := e.m.List(tctx(t))
	if err != nil {
		t.Fatal(err)
	}
	integration := 0
	for _, in := range infos {
		if in.Integration {
			integration++
			if in.Path != e.q.tree.Path {
				t.Fatalf("List: the integration entry is %s, the queue's tree is %s", in.Path, e.q.tree.Path)
			}
		}
	}
	if len(infos) != 2 || integration != 1 {
		t.Fatalf("List: want the agent tree and the integration tree, got %+v", infos)
	}
	must(t, e.m.Close(tctx(t), true))
	if !exists(e.q.tree.Path) {
		t.Fatal("Manager.Close removed the integration tree")
	}
	b := e.agent("b")
	edit(t, b, "b.txt", "b\n")
	if r := e.submit(b, "b"); !r.Merged() {
		t.Fatalf("the queue after Manager.Close: %+v", r)
	}
	e.integrationClean()
}

// A git repository inside an agent's tree (a clone made for reference, say) cannot
// be recorded: git would store a pointer to a commit that no clone of the project
// can fetch. It is refused with a reason the agent can act on, like an oversized
// file, instead of becoming history; ignoring it, or removing it, lets the rest in.
func TestNestedRepositoriesAreRefusedNotRecordedAsBrokenPointers(t *testing.T) {
	e := newQueueEnv(t, QueueOptions{})
	a := e.agent("a")
	edit(t, a, "keep.txt", "keep\n")
	nested := filepath.Join(a.Path, "deps", "lib")
	must(t, os.MkdirAll(nested, 0o755))
	rawGit(t, nested, "init", "-q", "-b", "main", ".")
	writeFile(t, filepath.Join(nested, "lib.go"), "package lib\n")
	rawGit(t, nested, "add", "-A")
	rawGit(t, nested, "commit", "-q", "-m", "lib")
	head, _ := a.Head(tctx(t))

	_, err := a.Commit(tctx(t), "work")
	var nr *NestedRepoError
	if !errors.Is(err, ErrNestedRepo) || !errors.As(err, &nr) || len(nr.Paths) != 1 || nr.Paths[0] != "deps/lib" {
		t.Fatalf("Commit with a nested repository: %v", err)
	}
	if after, _ := a.Head(tctx(t)); after != head {
		t.Fatal("a refused commit still moved the branch")
	}
	r := e.submit(a, "a")
	if r.Outcome != OutcomeRejected || !strings.Contains(r.Reason, "git repositories of their own") || !strings.Contains(r.Reason, "deps/lib") {
		t.Fatalf("submission with a nested repository: %+v", r)
	}
	if e.q.Tip() != r.Before {
		t.Fatal("the tip moved on a rejected submission")
	}

	// listed in .gitignore, the repository is not part of the tree's content
	edit(t, a, ".gitignore", "deps/\n")
	r = e.submit(a, "a again")
	if !r.Merged() {
		t.Fatalf("after ignoring it: %+v", r)
	}
	ls := rawGit(t, e.q.tree.Path, "ls-files", "-s")
	if strings.Contains(ls, "160000") || strings.Contains(ls, "deps") || !strings.Contains(ls, "keep.txt") {
		t.Fatalf("what was integrated:\n%s", ls)
	}
	e.integrationClean()

	// and a plain removal works as well
	b := e.agent("b")
	edit(t, b, "b.txt", "b\n")
	inner := filepath.Join(b.Path, "clone")
	must(t, os.MkdirAll(inner, 0o755))
	rawGit(t, inner, "init", "-q", "-b", "main", ".")
	if r := e.submit(b, "b"); r.Outcome != OutcomeRejected {
		t.Fatalf("b: %+v", r)
	}
	must(t, os.RemoveAll(inner))
	if r := e.submit(b, "b again"); !r.Merged() {
		t.Fatalf("b after removing the clone: %+v", r)
	}
}
