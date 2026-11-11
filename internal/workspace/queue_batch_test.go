package workspace

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
)

// batchEnv holds the queue busy with a first submission whose verification waits for release, so that the next
// submissions are all waiting when it finishes, as they are when many agents finish while a long verifier runs.
type batchEnv struct {
	*queueEnv
	runs    atomic.Int32
	release chan struct{}
	once    sync.Once
}

func newBatchEnv(t *testing.T, o QueueOptions, fails func(dir string) bool) *batchEnv {
	b := &batchEnv{release: make(chan struct{})}
	o.VerifyCmd = "check"
	o.Verify = func(ctx context.Context, req VerifyRequest) VerifyResult {
		if b.runs.Add(1) == 1 {
			select {
			case <-b.release:
			case <-ctx.Done():
			}
		}
		if fails != nil && fails(req.Dir) {
			return VerifyResult{Cmd: req.Cmd, ExitCode: 1, Output: "broken"}
		}
		return VerifyResult{Cmd: req.Cmd, ExitCode: 0, Output: "fine"}
	}
	b.queueEnv = newQueueEnv(t, o)
	return b
}

// run submits n trees (agent i writes f<i>.txt, and bad.txt when bad[i]) concurrently after the blocking first one,
// releases the first once all of them wait, and returns their results by index.
func (b *batchEnv) run(n int, bad map[int]bool) []*Result {
	t := b.t
	first := b.agent("first")
	edit(t, first, "first.txt", "first\n")
	trees := make([]*Tree, n)
	for i := range trees {
		trees[i] = b.agent(fmt.Sprintf("a%d", i))
		edit(t, trees[i], fmt.Sprintf("f%d.txt", i), fmt.Sprintf("%d\n", i))
		if bad[i] {
			edit(t, trees[i], fmt.Sprintf("bad%d.txt", i), "bad\n")
		}
	}
	firstDone := make(chan *Result, 1)
	go func() { firstDone <- b.submit(first, "first") }()
	waitFor(t, func() bool { return b.runs.Load() == 1 })
	results := make([]*Result, n)
	var wg sync.WaitGroup
	for i := range trees {
		wg.Add(1)
		go func() {
			defer wg.Done()
			results[i] = b.submit(trees[i], fmt.Sprintf("task %d", i))
		}()
		// in order: the queue keeps arrival order
		waitFor(t, func() bool { return len(b.q.Status().Waiting) == i+1 })
	}
	close(b.release)
	if r := <-firstDone; !r.Merged() {
		t.Fatalf("first: %+v", r)
	}
	wg.Wait()
	b.integrationClean()
	return results
}

func TestQueueVerifiesWaitingSubmissionsTogether(t *testing.T) {
	b := newBatchEnv(t, QueueOptions{}, nil)
	results := b.run(6, nil)
	for i, r := range results {
		if !r.Merged() || r.Verify == nil || !r.Verify.OK() || r.After != b.q.Tip() {
			t.Fatalf("task %d: %+v", i, r)
		}
	}
	// one run for the first submission, one for the six that waited behind it
	if got := b.runs.Load(); got != 2 {
		t.Fatalf("verifier ran %d times, want 2", got)
	}
	for i := range 6 {
		if !exists(filepath.Join(b.q.tree.Path, fmt.Sprintf("f%d.txt", i))) {
			t.Fatalf("f%d.txt is not on the tip", i)
		}
	}
	st := b.q.Status()
	if st.Merged != 7 || len(st.Landed) != 7 || b.log.count(EventMerged) != 7 || b.log.count(EventBatch) != 1 {
		t.Fatalf("status %+v, merged events %d, batch events %d", st, b.log.count(EventMerged), b.log.count(EventBatch))
	}
	if len(b.q.Verifications("task 3")) != 1 {
		t.Fatal("a batched submission keeps the record of the verification that covered it")
	}
}

func TestQueueSplitsAFailedBatchAndLandsTheRest(t *testing.T) {
	fails := func(dir string) bool {
		_, err := os.Stat(filepath.Join(dir, "bad2.txt"))
		return err == nil
	}
	b := newBatchEnv(t, QueueOptions{}, fails)
	results := b.run(6, map[int]bool{2: true})
	for i, r := range results {
		if i == 2 {
			if r.Outcome != OutcomeVerifyFailed || !r.RolledBack {
				t.Fatalf("the bad submission: %+v", r)
			}
			continue
		}
		if !r.Merged() {
			t.Fatalf("task %d did not land with the bad one split off: %+v", i, r)
		}
	}
	if exists(filepath.Join(b.q.tree.Path, "bad2.txt")) {
		t.Fatal("the failing submission is on the tip")
	}
	// first; all six (fail); halves [0 1 2] (fail) and [3 4 5] (pass); [0] then [1 2] (fail); [1], [2] (fail): 8 runs,
	// against 7 one at a time, and the five good ones landed without waiting for the bad one's turn
	if got := b.runs.Load(); got > 8 {
		t.Fatalf("verifier ran %d times", got)
	}
	st := b.q.Status()
	if st.Merged != 6 || st.VerifyFailures != 1 || b.log.count(EventVerifyFailed) != 1 {
		t.Fatalf("a split batch counts what its parts came to: %+v, verify_failed events %d", st, b.log.count(EventVerifyFailed))
	}
}

func TestQueueBatchOfOneIsUnchangedAndMaxBatchOneTurnsBatchingOff(t *testing.T) {
	b := newBatchEnv(t, QueueOptions{MaxBatch: 1}, nil)
	results := b.run(4, nil)
	for i, r := range results {
		if !r.Merged() {
			t.Fatalf("task %d: %+v", i, r)
		}
	}
	if got := b.runs.Load(); got != 5 {
		t.Fatalf("verifier ran %d times, want one per submission", got)
	}
	if b.log.count(EventBatch) != 0 {
		t.Fatal("no batch without batching")
	}
}

func TestQueueBatchesOnlyCompatibleVerifyCommands(t *testing.T) {
	var cmds []string
	var mu sync.Mutex
	release := make(chan struct{})
	var runs atomic.Int32
	verify := func(ctx context.Context, req VerifyRequest) VerifyResult {
		if runs.Add(1) == 1 {
			<-release
		}
		mu.Lock()
		cmds = append(cmds, req.Cmd)
		mu.Unlock()
		return VerifyResult{Cmd: req.Cmd}
	}
	combine := func(dir string, subs []Submission) (string, bool) { return "union", true }
	for _, tc := range []struct {
		name    string
		combine func(string, []Submission) (string, bool)
		want    int
	}{{"no combiner", nil, 4}, {"combiner", combine, 2}} {
		t.Run(tc.name, func(t *testing.T) {
			cmds, release = nil, make(chan struct{})
			runs.Store(0)
			e := newQueueEnv(t, QueueOptions{VerifyCmd: "x", Verify: verify, CombineVerify: tc.combine})
			first := e.agent("first")
			edit(t, first, "first.txt", "1\n")
			done := make(chan struct{})
			go func() { e.submit(first, "first"); close(done) }()
			waitFor(t, func() bool { return runs.Load() == 1 })
			var wg sync.WaitGroup
			for i, cmd := range []string{"go test ./a", "go test ./b", "go test ./a"} {
				tr := e.agent(fmt.Sprintf("a%d", i))
				edit(t, tr, fmt.Sprintf("f%d.txt", i), "x\n")
				wg.Add(1)
				go func() {
					defer wg.Done()
					if r := mustSubmit(t, e.q, Submission{Tree: tr, Task: fmt.Sprint(i), VerifyCmd: cmd}); !r.Merged() {
						t.Errorf("%d: %+v", i, r)
					}
				}()
				waitFor(t, func() bool { return len(e.q.Status().Waiting) == i+1 })
			}
			close(release)
			<-done
			wg.Wait()
			if got := int(runs.Load()); got != tc.want {
				t.Fatalf("verifier ran %d times (%q), want %d", got, cmds, tc.want)
			}
			if tc.combine != nil && cmds[1] != "union" {
				t.Fatalf("a batch of different commands runs the combined one: %q", cmds)
			}
		})
	}
}

func TestQueueBatchMemberThatGivesUpIsLeftOut(t *testing.T) {
	b := newBatchEnv(t, QueueOptions{}, nil)
	first := b.agent("first")
	edit(t, first, "first.txt", "first\n")
	go b.submit(first, "first")
	waitFor(t, func() bool { return b.runs.Load() == 1 })
	ctx, cancel := context.WithCancel(tctx(t))
	quitter := b.agent("quitter")
	edit(t, quitter, "q.txt", "q\n")
	stayer := b.agent("stayer")
	edit(t, stayer, "s.txt", "s\n")
	gaveUp := make(chan error, 1)
	go func() {
		_, err := b.q.Submit(ctx, Submission{Tree: quitter, Task: "quitter"})
		gaveUp <- err
	}()
	waitFor(t, func() bool { return len(b.q.Status().Waiting) == 1 })
	stayed := make(chan *Result, 1)
	go func() { stayed <- b.submit(stayer, "stayer") }()
	waitFor(t, func() bool { return len(b.q.Status().Waiting) == 2 })
	cancel()
	if err := <-gaveUp; err == nil {
		t.Fatal("a submission whose caller gave up has no result")
	}
	close(b.release)
	if r := <-stayed; !r.Merged() {
		t.Fatalf("stayer: %+v", r)
	}
	if exists(filepath.Join(b.q.tree.Path, "q.txt")) {
		t.Fatal("the work of a caller that gave up landed")
	}
	b.integrationClean()
}
