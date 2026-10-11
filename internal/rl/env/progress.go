package env

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"time"

	"github.com/anemos-labs/sleipnir/internal/rl"
)

// Progress at savepoints (Runner.ScoreSavepoints). A long rollout's verdict
// comes once, at its end; the work tree of each savepoint shows how it got
// there. After a consistent savepoint the runner scores that tree with the
// task's own verifier, in a clean checkout like the final verification and
// invisible to the agents, in progress mode: the verifier's environment
// carries SUITE_SAMPLE and SUITE_BUDGET_S, with which a suite scorer runs a
// sample of each unit's tests under a deadline (a verifier that does not know
// them runs whole). The scores go to <sample>/progress.jsonl and into the
// episode (Episode.Progress). Scoring runs in the background, one tree at a
// time per run; a sample whose earlier tree is still waiting has it replaced by
// the newer one.

// Defaults of the progress mode.
const (
	DefaultProgressSample = 20
	DefaultProgressBudget = 5 * time.Minute
)

// progressFile is the progress record of a sample directory.
const progressFile = "progress.jsonl"

// progressJob is one savepoint's tree waiting to be scored.
type progressJob struct {
	dir  string
	task rl.Task
	rec  savepointRecord
}

// progressQueue scores savepoint trees one at a time, the newest of each
// sample only.
type progressQueue struct {
	rn *run

	mu      sync.Mutex
	cond    *sync.Cond
	pending map[string]*progressJob // by sample directory
	order   []string                // sample directories in the order their first pending job came
	running string                  // the sample directory being scored
	closed  bool
}

// newProgressQueue starts the scorer of a run; it ends with ctx.
func newProgressQueue(ctx context.Context, rn *run) *progressQueue {
	q := &progressQueue{rn: rn, pending: map[string]*progressJob{}}
	q.cond = sync.NewCond(&q.mu)
	context.AfterFunc(ctx, func() {
		q.mu.Lock()
		q.closed = true
		q.cond.Broadcast()
		q.mu.Unlock()
	})
	go q.loop(ctx)
	return q
}

// add queues a savepoint's tree, replacing a tree of the same sample that has
// not been scored yet.
func (q *progressQueue) add(job *progressJob) {
	q.mu.Lock()
	defer q.mu.Unlock()
	if q.closed {
		return
	}
	if _, ok := q.pending[job.dir]; !ok {
		q.order = append(q.order, job.dir)
	}
	q.pending[job.dir] = job
	q.cond.Broadcast()
}

// finish drops what a sample still has queued and waits while its tree is
// being scored: the attempt is ending and reads progress.jsonl next.
func (q *progressQueue) finish(dir string) {
	q.mu.Lock()
	defer q.mu.Unlock()
	delete(q.pending, dir)
	for q.running == dir && !q.closed {
		q.cond.Wait()
	}
}

// next waits for a job; nil once the run ends.
func (q *progressQueue) next() *progressJob {
	q.mu.Lock()
	defer q.mu.Unlock()
	for {
		if q.closed {
			return nil
		}
		for len(q.order) > 0 {
			dir := q.order[0]
			q.order = q.order[1:]
			if job, ok := q.pending[dir]; ok {
				delete(q.pending, dir)
				q.running = dir
				return job
			}
		}
		q.cond.Wait()
	}
}

// done marks the running job finished.
func (q *progressQueue) done() {
	q.mu.Lock()
	q.running = ""
	q.cond.Broadcast()
	q.mu.Unlock()
}

// loop scores jobs until the run ends.
func (q *progressQueue) loop(ctx context.Context) {
	for {
		job := q.next()
		if job == nil {
			return
		}
		if err := q.rn.scoreSavepoint(ctx, job); err != nil && ctx.Err() == nil {
			q.rn.r.logf("env: %s: scoring savepoint %d: %v", job.dir, job.rec.N, err)
		}
		q.done()
	}
}

// progressEnv is the verifier environment of the progress mode.
func (r *Runner) progressEnv() []string {
	n := r.ProgressSample
	if n <= 0 {
		n = DefaultProgressSample
	}
	return []string{"SUITE_SAMPLE=" + strconv.Itoa(n), "SUITE_BUDGET_S=" + strconv.Itoa(int(r.progressBudget().Seconds()))}
}

// progressBudget resolves Runner.ProgressBudget.
func (r *Runner) progressBudget() time.Duration {
	if r.ProgressBudget > 0 {
		return r.ProgressBudget
	}
	return DefaultProgressBudget
}

// scoreSavepoint verifies the tree of one savepoint and appends the score to
// the sample's progress.jsonl.
func (rn *run) scoreSavepoint(ctx context.Context, job *progressJob) error {
	m := rn.r.Workspaces
	patch, err := m.diffTrees(ctx, savepointStoreDir(job.dir), job.rec.Workspace.BaseTree, job.rec.Tree, maxRestoreBytes)
	if err != nil {
		return err
	}
	res, err := VerifyPatch(ctx, job.task, patch, VerifyOptions{
		Workspaces: m, HiddenBlobs: rn.r.HiddenBlobs, Env: rn.r.progressEnv(),
		// The deadline the progress mode is given, and a minute for the checkout and the scorer's own start.
		Timeout: rn.r.progressBudget() + time.Minute,
	})
	if err != nil {
		return err
	}
	p := rl.ProgressPoint{Savepoint: job.rec.N, Seq: job.rec.Seq, ActiveWallMs: job.rec.ActiveWallMs, At: job.rec.At, Score: res.Score, Pass: res.Pass,
		TimedOut: res.TimedOut, Ms: res.Ms}
	return appendJSONLine(filepath.Join(job.dir, progressFile), p)
}

// appendJSONLine appends one JSON line to a file (fsynced).
func appendJSONLine(path string, v any) error {
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	if _, err := f.Write(append(b, '\n')); err != nil {
		_ = f.Close()
		return err
	}
	if err := f.Sync(); err != nil {
		_ = f.Close()
		return err
	}
	return f.Close()
}

// readProgress reads a sample's progress.jsonl in savepoint order; unreadable
// lines are skipped. A point written without its time (by an earlier version)
// takes it from the savepoint index.
func readProgress(dir string) []rl.ProgressPoint {
	f, err := os.Open(filepath.Join(dir, progressFile))
	if err != nil {
		return nil
	}
	defer f.Close()
	var out []rl.ProgressPoint
	missing := false
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		var p rl.ProgressPoint
		if json.Unmarshal(sc.Bytes(), &p) == nil && p.Savepoint > 0 {
			out = append(out, p)
			missing = missing || p.At.IsZero()
		}
	}
	if missing {
		if recs, err := readSavepoints(dir); err == nil {
			at := map[int]time.Time{}
			for _, r := range recs {
				at[r.N] = r.At
			}
			for i := range out {
				if out[i].At.IsZero() {
					out[i].At = at[out[i].Savepoint]
				}
			}
		}
	}
	return out
}

// keepProgressUpTo rewrites a sample's progress.jsonl without the points of
// savepoints numbered after n (a rewind cut them off).
func keepProgressUpTo(dir string, n int) error {
	pts := readProgress(dir)
	if pts == nil {
		return nil
	}
	var b []byte
	for _, p := range pts {
		if p.Savepoint > n {
			continue
		}
		line, err := json.Marshal(p)
		if err != nil {
			return fmt.Errorf("progress point: %w", err)
		}
		b = append(append(b, line...), '\n')
	}
	return atomicWriteFile(filepath.Join(dir, progressFile), b, 0o600)
}
