package env

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"time"

	"github.com/anemos-labs/sleipnir/internal/events"
	"github.com/anemos-labs/sleipnir/internal/rl"
)

// Branches of a long rollout (rl branch). A finished rollout that took
// savepoints (the trunk) is continued from some of them K times each, with
// different sampling seeds, for a limited horizon: K siblings per point that
// share everything up to the point, so their outcomes compare decisions made
// from the same state, without paying for the prefix again. A branch's sample
// directory starts as a copy of the trunk's log up to the savepoint plus an
// rl.fork event, the trunk's blobs (hard links) and a capture store that
// borrows the trunk's objects; the runner then resumes it as it resumes any
// rollout (savepoint_run.go). The siblings of one point form one GRPO group
// (rl.ForkGroup), and the steps before the fork are inherited: not trainable,
// left out of the training exports.

// DefaultBranchSavepointEvery is the savepoint interval of branches when the
// runner sets none: a branch is a resumed rollout and must be resumable.
const DefaultBranchSavepointEvery = 15 * time.Minute

// BranchRequest asks for branches of one rollout (Runner.Branch).
type BranchRequest struct {
	// Trunk is the sample directory of the rollout to branch
	// (<run>/<task>/<sample>). It must have finished (episode.json) and have
	// savepoints.
	Trunk string
	// Points are the trunk's savepoints to branch from.
	Points []int
	// Siblings is how many branches each point gets.
	Siblings int
	// Horizon is the agent time a branch gets after its fork; 0: the rest of
	// the trunk's budget.
	Horizon time.Duration
	// CompactFirst makes every branch compact its context before it goes on
	// (RunSpec.CompactFirst): K competing summaries of the point's state.
	CompactFirst bool
}

// forkFile is the branch record of a sample directory.
const forkFile = "fork.json"

// forkRecord is a branch's fork.json: rl.Fork and the agent time the trunk
// had used at the point, against which the horizon counts.
type forkRecord struct {
	rl.Fork
	ActiveWallMs int64 `json:"active_wall_ms"`
}

// TrunkSavepoints lists the consistent savepoints of a finished rollout, in
// order (the points rl branch can branch from).
func TrunkSavepoints(trunk string) ([]int, error) {
	recs, err := readSavepoints(trunk)
	if err != nil {
		return nil, err
	}
	var out []int
	for _, r := range recs {
		out = append(out, r.N)
	}
	return out, nil
}

// readSavepoints reads the consistent records of a sample's savepoint index.
func readSavepoints(dir string) ([]savepointRecord, error) {
	f, err := os.Open(filepath.Join(dir, savepointDir, savepointIndex))
	if err != nil {
		return nil, fmt.Errorf("%s has no savepoints: %w", dir, err)
	}
	defer f.Close()
	var out []savepointRecord
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 64<<10), 4<<20)
	for sc.Scan() {
		var r savepointRecord
		if json.Unmarshal(sc.Bytes(), &r) == nil && r.Consistent && r.Tree != "" {
			out = append(out, r)
		}
	}
	return out, sc.Err()
}

// Branch continues the trunk of req from each of its points Siblings times, as
// samples of task in Runner.Out: sample p*Siblings+k-1 is sibling k of point p.
// The branches run one after the other (they restore the trunk's workspace at
// its own path, which the agents' conversation names; use the trunk's work
// directory). A sample that has already been prepared is resumed or, when it
// has its episode, kept, so an interrupted Branch is rerun with the same
// arguments.
func (r *Runner) Branch(ctx context.Context, task rl.Task, req BranchRequest, opts RolloutOpts) (*Summary, error) {
	if req.Siblings < 1 || len(req.Points) == 0 {
		return nil, errors.New("branch: give at least one point and one sibling")
	}
	if _, err := os.Stat(filepath.Join(req.Trunk, "episode.json")); err != nil {
		return nil, fmt.Errorf("branch: %s has not finished (no episode.json): a running trunk uses the workspace its branches restore", req.Trunk)
	}
	recs, err := readSavepoints(req.Trunk)
	if err != nil {
		return nil, fmt.Errorf("branch: %w", err)
	}
	byN := map[int]savepointRecord{}
	for _, rec := range recs {
		byN[rec.N] = rec
	}
	rb := *r
	rb.Concurrency = 1
	if rb.SavepointEvery <= 0 {
		rb.SavepointEvery = DefaultBranchSavepointEvery
	}
	parent := trunkName(req.Trunk)
	for pi, n := range req.Points {
		rec, ok := byN[n]
		if !ok {
			return nil, fmt.Errorf("branch: %s has no consistent savepoint %d", req.Trunk, n)
		}
		if want := filepath.Join(rb.Workspaces.root, "ws", rec.Workspace.ID); rec.Workspace.Path != "" && rec.Workspace.Path != want {
			rb.logf("env: branch: the trunk worked in %s, its branches will work in %s: use the trunk's --work-dir so that the paths in its conversation hold", rec.Workspace.Path, want)
		}
		for k := 1; k <= req.Siblings; k++ {
			sample := pi*req.Siblings + k - 1
			dir := filepath.Join(rb.Out, task.ID, strconv.Itoa(sample))
			fork := forkRecord{Fork: rl.Fork{Parent: parent, Savepoint: n, Sibling: k, Siblings: req.Siblings, HorizonMs: req.Horizon.Milliseconds(),
				CompactFirst: req.CompactFirst},
				ActiveWallMs: rec.ActiveWallMs}
			if err := rb.prepareBranch(ctx, req.Trunk, dir, rec, fork, SampleSeed(opts.Seed, task.ID, sample), opts.Policy.Model); err != nil {
				return nil, fmt.Errorf("branch %d of savepoint %d: %w", k, n, err)
			}
		}
	}
	return rb.Rollout(ctx, []rl.Task{task}, len(req.Points)*req.Siblings, opts)
}

// trunkName names a trunk's sample by its last three path elements
// (<run>/<task>/<sample>).
func trunkName(dir string) string {
	abs, err := filepath.Abs(dir)
	if err != nil {
		abs = dir
	}
	sample := filepath.Base(abs)
	task := filepath.Base(filepath.Dir(abs))
	run := filepath.Base(filepath.Dir(filepath.Dir(abs)))
	return run + "/" + task + "/" + sample
}

// prepareBranch makes dir a copy of the trunk up to rec plus an rl.fork event,
// unless it was prepared before.
func (r *Runner) prepareBranch(ctx context.Context, trunk, dir string, rec savepointRecord, fork forkRecord, seed int64, policy string) error {
	if _, err := os.Stat(filepath.Join(dir, forkFile)); err == nil {
		return nil // prepared by an earlier run: the runner resumes or keeps it
	}
	if err := removeAllNoFollow(dir); err != nil {
		return err
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	logPath := filepath.Join(dir, "events.jsonl")
	if err := copyRange(filepath.Join(trunk, "events.jsonl"), logPath, 0, rec.LogBytes); err != nil {
		return err
	}
	if err := linkTree(filepath.Join(trunk, "blobs"), filepath.Join(dir, "blobs")); err != nil {
		return err
	}
	session, err := firstSession(logPath)
	if err != nil {
		return err
	}
	log, err := events.Open(dir, session)
	if err != nil {
		return err
	}
	seq, err := log.Emit("", events.TypeRLFork, fork.Fork)
	if err != nil {
		_ = log.Close()
		return err
	}
	size, err := log.FlushedSize()
	if cerr := log.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return err
	}
	fork.Seq = seq
	// The capture store borrows the trunk's objects (the tree of the point among them).
	store := savepointStoreDir(dir)
	if err := r.Workspaces.git.initBare(ctx, store); err != nil {
		return err
	}
	trunkObjects, err := filepath.Abs(filepath.Join(savepointStoreDir(trunk), "objects"))
	if err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(store, "objects", "info", "alternates"), []byte(trunkObjects+"\n"), 0o644); err != nil {
		return err
	}
	rec.Policy, rec.Seed, rec.LogBytes, rec.Seq = policy, seed, size, seq
	if err := appendSavepoint(dir, rec); err != nil {
		return err
	}
	if err := setRecordedBytes(dir, size); err != nil { // the prefix's spend is the trunk's
		return err
	}
	return writeJSONAtomic(filepath.Join(dir, forkFile), fork)
}

// readFork reads a sample's fork.json; nil when the sample is no branch.
func readFork(dir string) *forkRecord {
	b, err := os.ReadFile(filepath.Join(dir, forkFile))
	if err != nil {
		return nil
	}
	var f forkRecord
	if json.Unmarshal(b, &f) != nil {
		return nil
	}
	return &f
}

// firstSession is the session id of a log's first event.
func firstSession(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	var e struct {
		Session string `json:"session"`
	}
	if err := json.NewDecoder(io.LimitReader(f, 1<<20)).Decode(&e); err != nil {
		return "", fmt.Errorf("reading the trunk's log: %w", err)
	}
	if e.Session == "" {
		return "", errors.New("the trunk's log names no session")
	}
	return e.Session, nil
}

// linkTree recreates the files under src in dst as hard links (copies where
// links cannot be made); blobs are content-addressed, so sharing them is safe.
func linkTree(src, dst string) error {
	return filepath.WalkDir(src, func(p string, d os.DirEntry, err error) error {
		if err != nil {
			if errors.Is(err, os.ErrNotExist) && p == src {
				return nil
			}
			return err
		}
		rel, err := filepath.Rel(src, p)
		if err != nil {
			return err
		}
		target := filepath.Join(dst, rel)
		switch {
		case d.IsDir():
			return os.MkdirAll(target, 0o700)
		case !d.Type().IsRegular():
			return nil
		}
		if err := os.Link(p, target); err == nil || errors.Is(err, os.ErrExist) {
			return nil
		}
		fi, err := d.Info()
		if err != nil {
			return err
		}
		return copyRange(p, target, 0, fi.Size())
	})
}
