package env

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/anemos-labs/sleipnir/internal/events"
	"github.com/anemos-labs/sleipnir/internal/rl"
)

// Savepoints of long rollouts (Runner.SavepointEvery). While the harness runs,
// the runner asks it for a savepoint at a fixed interval: the harness pauses the
// agents' writes for a moment, writes their recoverable snapshots, flushes the
// log, and lets the runner capture the work tree (Workspace.Capture) before
// writes go on. Each consistent savepoint is a line of savepoints/index.jsonl in
// the sample directory. When an attempt starts and the directory has one, the
// rollout resumes from the newest instead of starting over: the log is cut back
// to it (the cut tail is kept as events.rewound.N.jsonl), the workspace is
// restored at its stable path with the captured tree, and the harness continues
// the session (RunSpec.Resume).

// SavepointHook connects a rollout's runner to the harness that runs it. The
// harness binds a function that takes a savepoint once its session exists, and
// unbinds it before the session closes; the runner calls Take on its own
// schedule.
type SavepointHook struct {
	// Every is the interval the runner asks for savepoints at.
	Every time.Duration
	// Polite and Firm bound how long a savepoint waits for the agents' writes
	// to pause (session.Savepoint): first without holding anyone up, then while
	// new writes wait. A savepoint that does not get a pause is skipped.
	Polite, Firm time.Duration

	mu       sync.Mutex
	take     func(ctx context.Context, capture func(context.Context) error) (SavepointInfo, error)
	cancel   context.CancelFunc // ends the savepoint in progress
	inflight sync.WaitGroup
}

// SavepointInfo is what the harness reports about one savepoint
// (session.SavepointResult). N numbers the savepoints of the rollout.
type SavepointInfo struct {
	N          int               `json:"n"`
	Consistent bool              `json:"consistent"`
	LogBytes   int64             `json:"log_bytes"`
	Seq        uint64            `json:"seq"`
	Agents     map[string]string `json:"agents,omitempty"`
}

// Bind installs the harness's savepoint function. Bind(nil) unbinds it, ends a
// savepoint in progress and waits for it to return: after it, nothing touches
// the session.
func (h *SavepointHook) Bind(take func(ctx context.Context, capture func(context.Context) error) (SavepointInfo, error)) {
	h.mu.Lock()
	h.take = take
	cancel := h.cancel
	h.mu.Unlock()
	if take == nil {
		if cancel != nil {
			cancel()
		}
		h.inflight.Wait()
	}
}

// Take asks the bound harness for a savepoint; false when nothing is bound.
func (h *SavepointHook) Take(ctx context.Context, capture func(context.Context) error) (SavepointInfo, bool, error) {
	h.mu.Lock()
	take := h.take
	if take == nil {
		h.mu.Unlock()
		return SavepointInfo{}, false, nil
	}
	ctx, cancel := context.WithCancel(ctx)
	h.cancel = cancel
	h.inflight.Add(1)
	h.mu.Unlock()
	defer func() {
		h.mu.Lock()
		h.cancel = nil
		h.mu.Unlock()
		cancel()
		h.inflight.Done()
	}()
	info, err := take(ctx, capture)
	return info, true, err
}

// newSavepointHook is the hook of an attempt with savepoints every interval:
// a savepoint waits for a pause in writes for at most half an interval
// politely, then for at most an interval (30 s and 2 min at most).
func newSavepointHook(every time.Duration) *SavepointHook {
	return &SavepointHook{Every: every, Polite: min(every/2, 30*time.Second), Firm: min(every, 2*time.Minute)}
}

// ResumeNote is what the agent of a resumed rollout (a team's manager) is told
// when it continues.
const ResumeNote = "This run was interrupted and has now been resumed from a savepoint. The workspace is as it was at that " +
	"point: files changed after it are back as they were then, processes started before it (servers, background jobs) are " +
	"no longer running, and files git ignores (build outputs) may be gone. Continue the task from where you were."

// ResumeNoteTeam is ResumeNote for a team's manager, whose workers did not
// survive the interruption.
const ResumeNoteTeam = ResumeNote + " The workers that were running were stopped: the tasks they had not finished are " +
	"back on the board as to do, their changes up to the savepoint are in the workspace, and they need workers again."

// savepointRecord is one line of savepoints/index.jsonl.
type savepointRecord struct {
	SavepointInfo
	Tree      string        `json:"tree"`
	Workspace WorkspaceInfo `json:"workspace"`
	// Policy and Seed identify the rollout; a resume refuses a record of
	// another one.
	Policy string `json:"policy"`
	Seed   int64  `json:"seed"`
	// ActiveWallMs is the agent time the rollout had used at the savepoint,
	// across earlier resumes: a resume gets the rest of the wall budget.
	ActiveWallMs int64     `json:"active_wall_ms"`
	At           time.Time `json:"at"`
}

// Paths in a sample directory.
const (
	savepointDir   = "savepoints"
	savepointIndex = "index.jsonl"
	savepointStore = "git"
	recordedFile   = "recorded.json" // log bytes whose spend the ledger already has
)

// savepointStoreDir is the object store the sample's captures go to.
func savepointStoreDir(dir string) string { return filepath.Join(dir, savepointDir, savepointStore) }

// appendSavepoint adds a record to the sample's index (fsynced).
func appendSavepoint(dir string, rec savepointRecord) error {
	if err := os.MkdirAll(filepath.Join(dir, savepointDir), 0o700); err != nil {
		return err
	}
	b, err := json.Marshal(rec)
	if err != nil {
		return err
	}
	f, err := os.OpenFile(filepath.Join(dir, savepointDir, savepointIndex), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
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

// lastSavepoint is the newest consistent record of the sample's index that
// belongs to this rollout (same policy and seed) and whose log prefix still
// exists, or nil.
func lastSavepoint(dir, policy string, seed int64) *savepointRecord {
	fi, err := os.Stat(filepath.Join(dir, "events.jsonl"))
	if err != nil {
		return nil
	}
	f, err := os.Open(filepath.Join(dir, savepointDir, savepointIndex))
	if err != nil {
		return nil
	}
	defer f.Close()
	var last *savepointRecord
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 64<<10), 4<<20)
	for sc.Scan() {
		var r savepointRecord
		if json.Unmarshal(sc.Bytes(), &r) != nil || !r.Consistent || r.Tree == "" || r.Policy != policy || r.Seed != seed ||
			r.LogBytes <= 0 || r.LogBytes > fi.Size() {
			continue
		}
		rr := r
		last = &rr
	}
	return last
}

// rewindTo cuts the sample's log back to rec: the tail after it is moved to
// events.rewound.N.jsonl, and what the previous attempt left besides its log
// (verification output, a half-written episode) is removed. It returns the
// spend recorded in the tail that the ledger does not have yet, for a
// "rewound" ledger entry, and marks the kept prefix as recorded.
func rewindTo(dir string, rec *savepointRecord) (unrecorded spend, err error) {
	logPath := filepath.Join(dir, "events.jsonl")
	fi, err := os.Lstat(logPath)
	if err != nil {
		return spend{}, err
	}
	if !fi.Mode().IsRegular() { // it is cut in place: a link would cut whatever it names
		return spend{}, fmt.Errorf("%s is not a regular file", logPath)
	}
	from := recordedBytes(dir)
	unrecorded = spendOfRange(logPath, min(from, fi.Size()), fi.Size())
	if fi.Size() > rec.LogBytes {
		var n int
		for n = 1; ; n++ {
			if _, err := os.Lstat(filepath.Join(dir, fmt.Sprintf("events.rewound.%d.jsonl", n))); errors.Is(err, os.ErrNotExist) {
				break
			}
		}
		if err := copyRange(logPath, filepath.Join(dir, fmt.Sprintf("events.rewound.%d.jsonl", n)), rec.LogBytes, fi.Size()); err != nil {
			return spend{}, err
		}
		if err := os.Truncate(logPath, rec.LogBytes); err != nil {
			return spend{}, err
		}
	}
	for _, name := range []string{"episode.json", "diff.patch", "verifier.log"} {
		if err := os.Remove(filepath.Join(dir, name)); err != nil && !errors.Is(err, os.ErrNotExist) {
			return spend{}, err
		}
	}
	// The savepoints after rec belong to the log that was cut off: once the log
	// grows past their offsets again, they would point into other events.
	if err := keepSavepointsUpTo(dir, rec.N); err != nil {
		return spend{}, err
	}
	if err := keepProgressUpTo(dir, rec.N); err != nil {
		return spend{}, err
	}
	return unrecorded, setRecordedBytes(dir, rec.LogBytes)
}

// keepSavepointsUpTo rewrites the sample's savepoint index without the records
// numbered after n.
func keepSavepointsUpTo(dir string, n int) error {
	path := filepath.Join(dir, savepointDir, savepointIndex)
	b, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	var kept []byte
	for _, ln := range bytes.SplitAfter(b, []byte("\n")) {
		var r struct {
			N int `json:"n"`
		}
		if len(bytes.TrimSpace(ln)) == 0 || json.Unmarshal(ln, &r) != nil || r.N > n {
			continue
		}
		kept = append(kept, ln...)
	}
	return atomicWriteFile(path, kept, 0o600)
}

// copyRange copies bytes [from, to) of src to a new file dst.
func copyRange(src, dst string, from, to int64) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, io.NewSectionReader(in, from, to-from)); err != nil {
		_ = out.Close()
		return err
	}
	if err := out.Sync(); err != nil {
		_ = out.Close()
		return err
	}
	return out.Close()
}

// spend is what a stretch of a log spent at the endpoint; main counts the
// responses to the single agent (its steps).
type spend struct {
	usd                     float64
	requests, retries, main int
}

// zero reports whether the stretch spent nothing.
func (s spend) zero() bool { return s.usd == 0 && s.requests == 0 && s.retries == 0 }

// spendOfRange is spendOf for the lines of a log that start in [from, to).
func spendOfRange(path string, from, to int64) spend {
	var s spend
	f, err := os.Open(path)
	if err != nil || to <= from {
		return s
	}
	defer f.Close()
	r := bufio.NewReaderSize(io.NewSectionReader(f, from, to-from), 1<<20)
	for {
		line, err := r.ReadBytes('\n')
		if len(line) > 0 {
			var e events.Event
			if json.Unmarshal(line, &e) == nil {
				switch e.Type {
				case events.TypeModelResponse:
					s.requests++
					if e.Agent == "main" {
						s.main++
					}
					var d struct {
						CostUSD float64 `json:"cost_usd"`
					}
					if json.Unmarshal(e.Data, &d) == nil {
						s.usd += cleanCost(d.CostUSD)
					}
				case events.TypeModelError:
					s.retries++
				}
			}
		}
		if err != nil {
			return s
		}
	}
}

// recordedBytes is how much of the sample's log the ledger already accounts
// for (0 when nothing says so).
func recordedBytes(dir string) int64 {
	b, err := os.ReadFile(filepath.Join(dir, savepointDir, recordedFile))
	if err != nil {
		return 0
	}
	var r struct {
		Bytes int64 `json:"bytes"`
	}
	if json.Unmarshal(b, &r) != nil || r.Bytes < 0 {
		return 0
	}
	return r.Bytes
}

// setRecordedBytes records how much of the log the ledger accounts for.
func setRecordedBytes(dir string, n int64) error {
	if err := os.MkdirAll(filepath.Join(dir, savepointDir), 0o700); err != nil {
		return err
	}
	return writeJSONAtomic(filepath.Join(dir, savepointDir, recordedFile), map[string]int64{"bytes": n})
}

// runTag identifies a run's output directory in stable workspace ids.
func runTag(out string) string {
	abs, err := filepath.Abs(out)
	if err != nil {
		abs = out
	}
	h := sha256.Sum256([]byte(abs))
	return hex.EncodeToString(h[:4])
}

// savepointLoop asks the harness for a savepoint every hook.Every until ctx
// ends, capturing ws into the sample's store and appending each consistent one
// to the index. active is the agent time the rollout has used so far, earlier
// attempts included. It returns how many savepoints it recorded.
func (rn *run) savepointLoop(ctx context.Context, sp *savepointRun, active func() time.Duration) int {
	hook, ws, dir := sp.hook, sp.ws, sp.dir
	t := time.NewTicker(hook.Every)
	defer t.Stop()
	n := 0
	for {
		select {
		case <-ctx.Done():
			return n
		case <-t.C:
		}
		var tree string
		info, bound, err := hook.Take(ctx, func(cctx context.Context) error {
			var cerr error
			tree, _, cerr = ws.Capture(cctx, savepointStoreDir(dir))
			return cerr
		})
		switch {
		case !bound || ctx.Err() != nil:
			continue
		case err != nil:
			rn.r.logf("env: %s: savepoint: %v", dir, err)
			continue
		case !info.Consistent || tree == "":
			continue
		}
		info.N += sp.base
		rec := savepointRecord{SavepointInfo: info, Tree: tree, Workspace: ws.Info(), Policy: sp.policy, Seed: sp.seed,
			ActiveWallMs: active().Milliseconds(), At: rn.r.now().UTC()}
		if err := appendSavepoint(dir, rec); err != nil {
			rn.r.logf("env: %s: recording savepoint %d: %v", dir, info.N, err)
			continue
		}
		n++
		if rn.progress != nil {
			rn.progress.add(&progressJob{dir: dir, task: sp.task, rec: rec})
		}
	}
}

// maxResumes bounds how many attempts of one rollout may resume from a
// savepoint of their own beyond the infra retries: each made progress, but a
// rollout that keeps failing after progress must still end.
const maxResumes = 20

// savepointRun is what runHarness needs to take an attempt's savepoints.
type savepointRun struct {
	hook   *SavepointHook
	ws     *Workspace
	dir    string
	policy string
	seed   int64
	prior  time.Duration // agent time used before this attempt (resumed from a savepoint)
	// base is the number of the savepoint the attempt resumed from: the
	// session numbers its own from 1, the index continues the rollout's.
	base int
	task rl.Task // scored at each savepoint (Runner.ScoreSavepoints)
}

// remainingBudget is what is left of b after the kept part of a resumed log
// spent prior: the request budget less the responses in it and, for a single
// agent, the step budget less its steps (a team's members count steps per run
// of their own). A budget that is used up stays at one, so that the resumed run
// ends on it at once instead of turning it off.
func remainingBudget(b rl.Budget, prior spend, single bool) rl.Budget {
	if b.Requests > 0 {
		b.Requests = max(b.Requests-prior.requests, 1)
	}
	if single && b.Steps > 0 {
		b.Steps = max(b.Steps-prior.main, 1)
	}
	return b
}

// recordRewound writes the ledger entry for the part of a log that a resume
// cut off and that no entry has counted yet (the process died before its
// attempt ended): money spent on work that was thrown away.
func (rn *run) recordRewound(j job, attempt int, lost spend) {
	if rn.ledger == nil || lost.zero() {
		return
	}
	e := LedgerEntry{Time: rn.r.now().UTC(), Run: rn.id, Task: j.task.ID, Sample: j.sample, Attempt: attempt, Status: StatusRewound,
		CostUSD: lost.usd, Requests: lost.requests, Retries: lost.retries}
	if err := rn.ledger.add(e); err != nil {
		rn.r.logf("env: %s/%d: writing the ledger: %v", j.task.ID, j.sample, err)
	}
}
