package env

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/anemos-labs/sleipnir/internal/core"
	"github.com/anemos-labs/sleipnir/internal/events"
	"github.com/anemos-labs/sleipnir/internal/rl"
)

// Verification.
//
// The verifier never runs in the agent's workspace. Verify computes the agent's
// diff, builds a SEPARATE clean checkout from the cached snapshot (which no
// agent has ever touched), applies the diff to it with every protected path
// removed, writes the hidden files there and only then runs the task's command.
// Whatever the agent did to tests, build scripts or the verifier itself
// therefore has no effect, and is reported in Result.ProtectedTouched.
//
// The rule that keeps this honest for training: a failure of the machinery is
// an error (Result is then meaningless and the episode is an infra error); a
// failure the agent could have caused (tests that fail or hang, a diff that
// does not apply, a workspace that is too large) is a verdict with Pass false.
// Never the other way round, or the policy would learn to provoke "infra
// errors" to escape a bad reward.

// PassPolicy values for VerifyOptions.PassPolicy.
const (
	PassAll      = "all"      // every repeat must pass (default)
	PassMajority = "majority" // more than half
	PassAny      = "any"      // at least one
)

// VerifyOptions configures Verify, VerifyPatch and VerifyBaseline.
type VerifyOptions struct {
	// Workspaces builds the clean checkouts. Verify takes it from the workspace;
	// VerifyPatch and VerifyBaseline require it.
	Workspaces *Workspaces
	// HiddenBlobs resolves "blob:<hash>" hidden files. It is deliberately not the
	// run's own blob store: that one is written by the harness the agent runs in.
	HiddenBlobs events.Blobs
	// Store receives the raw diff and the verifier log as blobs; nil skips that.
	Store events.Blobs
	// OutDir, when set, receives diff.patch and verifier.log.
	OutDir string
	// Answer is the agent's final message, checked against Verifier.Expect.
	Answer string
	// Repeats runs the verifier that many times, each in a fresh checkout, to
	// measure flakiness (default 1). Score is the mean; Pass follows PassPolicy.
	Repeats    int
	PassPolicy string
	// Timeout overrides Verifier.TimeoutS when positive (default 10 min).
	Timeout time.Duration
	// MaxOutput bounds the output kept per stream of each run (default 2 MiB).
	MaxOutput int64
	// MaxDiffBytes bounds the agent's diff (default DefaultMaxDiffBytes).
	MaxDiffBytes int64
	// KeepCheckout leaves the last clean checkout on disk (Result.CheckoutDir).
	KeepCheckout bool
}

// VerifyRun is one execution of the verifier command.
type VerifyRun struct {
	ExitCode  int     `json:"exit_code"`
	Pass      bool    `json:"pass"`
	Score     float64 `json:"score"`
	TimedOut  bool    `json:"timed_out,omitempty"`
	Truncated bool    `json:"truncated,omitempty"`
	Signal    int     `json:"signal,omitempty"`
	Ms        int64   `json:"ms"`
	Note      string  `json:"note,omitempty"`
}

// Result is the outcome of one verification.
type Result struct {
	Pass  bool    `json:"pass"`
	Score float64 `json:"score"` // in [0,1]
	// Version identifies the verifier configuration (command, pass mode, hidden
	// files, protected globs, expectations), so scores from different verifiers
	// are never mixed.
	Version string `json:"version"`
	Mode    string `json:"mode"` // exit0 | regex | json-score | expect

	Runs  []VerifyRun `json:"runs,omitempty"`
	Flaky bool        `json:"flaky,omitempty"` // repeats disagreed
	// ExitCode, TimedOut and Truncated describe the last run.
	ExitCode  int   `json:"exit_code"`
	TimedOut  bool  `json:"timed_out,omitempty"`
	Truncated bool  `json:"truncated,omitempty"`
	Ms        int64 `json:"ms"`

	// Rejected is set when the submission was refused before the verifier ran
	// (a diff over the size limit, a diff that does not apply). Pass is false.
	Rejected string `json:"rejected,omitempty"`

	// ProtectedTouched lists protected paths (and hidden-file paths) the agent's
	// diff changed; those changes were discarded.
	ProtectedTouched []string `json:"protected_touched,omitempty"`
	// VerifierTouched is the subset of ProtectedTouched that is part of the
	// verifier itself: a file its command names, or a hidden file.
	VerifierTouched []string      `json:"verifier_touched,omitempty"`
	Skipped         []SkippedPath `json:"skipped,omitempty"`
	HiddenWritten   []string      `json:"hidden_written,omitempty"`
	Warnings        []string      `json:"warnings,omitempty"`

	// FilesChanged, LinesAdded and LinesDeleted describe the applied diff.
	FilesChanged int `json:"files_changed"`
	LinesAdded   int `json:"lines_added"`
	LinesDeleted int `json:"lines_deleted"`

	BaseTree string `json:"base_tree,omitempty"`
	Tree     string `json:"tree,omitempty"`

	// Diff is the agent's raw diff against the starting state; DiffBlob and
	// LogBlob are set when a Store was given.
	Diff        []byte    `json:"-"`
	Log         string    `json:"-"`
	DiffBlob    core.Hash `json:"diff_blob,omitempty"`
	LogBlob     core.Hash `json:"log_blob,omitempty"`
	CheckoutDir string    `json:"checkout_dir,omitempty"`
}

// Verdict converts the result to the schema's Verdict.
func (r Result) Verdict() rl.Verdict {
	return rl.Verdict{Kind: "verifier", Pass: r.Pass, Score: r.Score, Version: r.Version, Detail: r.LogBlob, Ms: r.Ms}
}

// Sentinel errors of the task-quality checks.
var (
	// ErrBaselinePasses: the verifier passes on the untouched starting commit, so
	// the task would reward doing nothing.
	ErrBaselinePasses = errors.New("verifier passes on the untouched starting commit")
	// ErrBaselineBroken: the verifier did not fail cleanly on the starting commit
	// (it hung, was killed, or its command was not found), so its verdicts mean
	// nothing.
	ErrBaselineBroken = errors.New("verifier does not run cleanly on the starting commit")
	// ErrGoldFails: the verifier fails even with the reference solution applied.
	ErrGoldFails = errors.New("verifier fails with the reference solution applied")
)

// VerifierVersion fingerprints everything that decides a verdict.
func VerifierVersion(t rl.Task) string {
	v := t.Verifier
	h := sha256.New()
	w := func(s string) { h.Write([]byte(s)); h.Write([]byte{0}) }
	w(v.Cmd)
	w(v.Pass)
	w(fmt.Sprint(v.TimeoutS))
	w(string(v.Expect))
	prot := append([]string(nil), v.Protected...)
	sort.Strings(prot)
	for _, p := range prot {
		w("p:" + p)
	}
	names := make([]string, 0, len(v.Hidden))
	for n := range v.Hidden {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, n := range names {
		ref := v.Hidden[n]
		if strings.HasPrefix(ref, "text:") {
			ref = "text-sha:" + string(core.HashString(ref))
		}
		w("h:" + n + "=" + ref)
	}
	return "v1-" + hex.EncodeToString(h.Sum(nil))[:12]
}

func needsCommand(t rl.Task) bool { return strings.TrimSpace(t.Verifier.Cmd) != "" }

// Verify judges the agent's work in ws. See the section comment above.
func Verify(ctx context.Context, task rl.Task, ws *Workspace, opts VerifyOptions) (Result, error) {
	if err := ValidateTask(task); err != nil {
		return Result{}, Infra("verify", err)
	}
	if !needsCommand(task) {
		// A recall task: only the final answer is judged, no checkout is needed.
		return verifyAnswerOnly(task, opts)
	}
	if ws == nil {
		return Result{}, Infra("verify", errors.New("a workspace is required to verify a task with a command"))
	}
	m := ws.mgr
	d, err := ws.Diff(ctx, opts.MaxDiffBytes)
	if err != nil {
		if IsAgentLimit(err) && ctx.Err() == nil {
			res := Result{Version: VerifierVersion(task), Mode: modeName(task), Rejected: err.Error(), BaseTree: ws.BaseTree}
			return finishRejected(res, opts)
		}
		return Result{}, err
	}
	return m.verify(ctx, verifyInput{
		task: task, snap: ws.snap, patch: d.Patch, files: d.Files, skipped: d.Skipped,
		baseTree: d.BaseTree, tree: d.Tree, opts: opts,
	})
}

// VerifyPatch judges an explicit patch (as produced by Workspace.Diff or by
// `git diff`) applied to the task's starting state: taskgen uses it to try a
// reference solution, and a stored diff.patch can be re-verified with it.
func VerifyPatch(ctx context.Context, task rl.Task, patch []byte, opts VerifyOptions) (Result, error) {
	if err := ValidateTask(task); err != nil {
		return Result{}, Infra("verify", err)
	}
	if !needsCommand(task) {
		return verifyAnswerOnly(task, opts)
	}
	m := opts.Workspaces
	if m == nil {
		return Result{}, Infra("verify", errors.New("VerifyOptions.Workspaces is required"))
	}
	snap, err := m.snapshotFor(ctx, task)
	if err != nil {
		return Result{}, err
	}
	files, err := m.parsePatch(ctx, patch)
	if err != nil {
		return Result{}, Infra("parse patch", err)
	}
	return m.verify(ctx, verifyInput{task: task, snap: snap, patch: patch, files: files, baseTree: snap.meta.BaseTree, opts: opts})
}

// VerifyBaseline runs the verifier on the untouched starting state (with hidden
// files in place). A sound task fails there: it returns the Result and nil.
// ErrBaselinePasses means the task rewards doing nothing; ErrBaselineBroken
// that the verifier does not run cleanly; both make the task invalid.
func VerifyBaseline(ctx context.Context, task rl.Task, opts VerifyOptions) (Result, error) {
	res, err := VerifyPatch(ctx, task, nil, opts)
	if err != nil {
		return res, err
	}
	if res.Pass {
		return res, ErrBaselinePasses
	}
	if res.Rejected != "" || res.TimedOut || (needsCommand(task) && brokenExit(res)) {
		return res, fmt.Errorf("%w: %s", ErrBaselineBroken, describeBroken(res))
	}
	return res, nil
}

func brokenExit(r Result) bool {
	if len(r.Runs) == 0 {
		return true
	}
	last := r.Runs[len(r.Runs)-1]
	return last.Signal != 0 || last.ExitCode == 126 || last.ExitCode == 127
}

func describeBroken(r Result) string {
	switch {
	case r.Rejected != "":
		return r.Rejected
	case r.TimedOut:
		return "timed out"
	case len(r.Runs) > 0 && r.Runs[len(r.Runs)-1].Signal != 0:
		return fmt.Sprintf("killed by signal %d", r.Runs[len(r.Runs)-1].Signal)
	default:
		return fmt.Sprintf("exit status %d (command not found or not executable?)", r.ExitCode)
	}
}

// CheckReport is the outcome of CheckTask.
type CheckReport struct {
	Baseline Result
	Gold     Result
}

// CheckTask proves a task is sound: the verifier fails on the starting state
// and passes with the reference solution (gold, a patch) applied. It is what
// taskgen runs before it emits a task.
//
// With Repeats above one a task is held to every run, whatever PassPolicy the rollouts use: the starting state must fail
// each time (one pass is a task that rewards doing nothing some of the time) and the reference solution must pass each
// time (a verifier that fails the solution now and then scores the same correct work differently).
func CheckTask(ctx context.Context, task rl.Task, gold []byte, opts VerifyOptions) (CheckReport, error) {
	var rep CheckReport
	var err error
	baseOpts, goldOpts := opts, opts
	if opts.Repeats > 1 {
		baseOpts.PassPolicy, goldOpts.PassPolicy = PassAny, PassAll
	}
	if rep.Baseline, err = VerifyBaseline(ctx, task, baseOpts); err != nil {
		return rep, err
	}
	if rep.Gold, err = VerifyPatch(ctx, task, gold, goldOpts); err != nil {
		return rep, err
	}
	if !rep.Gold.Pass {
		return rep, fmt.Errorf("%w: %s", ErrGoldFails, goldFailure(rep.Gold))
	}
	return rep, nil
}

func goldFailure(r Result) string {
	if r.Rejected != "" {
		return r.Rejected
	}
	out := r.Log
	if len(out) > 800 {
		out = "..." + out[len(out)-800:]
	}
	return fmt.Sprintf("exit status %d\n%s", r.ExitCode, strings.TrimSpace(out))
}

// ---- the core ----

type verifyInput struct {
	task     rl.Task
	snap     *snapshot
	patch    []byte
	files    []PatchFile
	skipped  []SkippedPath
	baseTree string
	tree     string
	opts     VerifyOptions
}

func modeName(t rl.Task) string {
	if !needsCommand(t) {
		return "expect"
	}
	pm, _ := parsePassMode(t.Verifier.Pass)
	return pm.String()
}

func (m *Workspaces) verify(ctx context.Context, in verifyInput) (Result, error) {
	task, opts := in.task, in.opts
	res := Result{Version: VerifierVersion(task), Mode: modeName(task), BaseTree: in.baseTree, Tree: in.tree, Diff: in.patch}

	// Verification checkouts come from the cached snapshot, so the snapshot is
	// re-checked (and rebuilt if it was modified while the rollout ran) rather than
	// trusted because the workspace once was cloned from it. The diff is only
	// meaningful against the baseline it was computed from: if the rebuilt
	// snapshot has a different baseline (a non-deterministic setup), the verdict
	// cannot be trusted and the episode is an infra error, never a score.
	snap, err := m.snapshotFor(ctx, task)
	if err != nil {
		return res, err
	}
	if in.baseTree != "" && snap.meta.BaseTree != in.baseTree {
		return res, Infraf("verify", "the starting snapshot changed while the rollout ran (baseline %s, now %s): is the task's setup deterministic?",
			in.baseTree[:min(12, len(in.baseTree))], snap.meta.BaseTree[:12])
	}
	in.snap = snap
	pm, err := parsePassMode(task.Verifier.Pass)
	if err != nil {
		return res, Infra("verify", err)
	}
	prot, err := CompileGlobs(task.Verifier.Protected)
	if err != nil {
		return res, Infra("verify", err)
	}
	hiddenSet := make(map[string]bool, len(task.Verifier.Hidden))
	for p := range task.Verifier.Hidden {
		hiddenSet[foldPath(p)] = true
	}

	fr := filterPatch(in.files, prot, hiddenSet)
	res.ProtectedTouched = fr.Protected
	res.VerifierTouched = verifierFilesTouched(task, fr.Protected)
	res.Skipped = append(append([]SkippedPath(nil), in.skipped...), fr.Skipped...)
	res.FilesChanged = len(fr.Applied)
	for _, f := range fr.Applied {
		res.LinesAdded += f.Added
		res.LinesDeleted += f.Deleted
	}

	repeats := max(opts.Repeats, 1)
	timeout := opts.Timeout
	if timeout <= 0 {
		timeout = time.Duration(task.Verifier.TimeoutS) * time.Second
	}
	if timeout <= 0 {
		timeout = 10 * time.Minute
	}
	maxOut := opts.MaxOutput
	if maxOut <= 0 {
		maxOut = 2 << 20
	}
	expect, hasExpect := Expect{}, HasExpect(task.Verifier)
	if hasExpect {
		if expect, err = ParseExpect(task.Verifier.Expect); err != nil {
			return res, Infra("verify", err)
		}
	}
	expectScore, expectPass := 1.0, true
	if hasExpect {
		expectScore, expectPass = judgeExpect(expect, opts.Answer)
	}

	var log strings.Builder
	started := time.Now()
	for i := 0; i < repeats; i++ {
		if err := ctx.Err(); err != nil {
			return res, err
		}
		rr, out, rej, err := m.runVerifierOnce(ctx, in, fr, pm, timeout, maxOut, i, &res)
		if err != nil {
			return res, err
		}
		if rej != "" {
			res.Rejected = rej
			res.Runs = nil
			break
		}
		if hasExpect {
			rr.Score = math.Min(rr.Score, expectScore)
			rr.Pass = rr.Pass && expectPass
			if !expectPass {
				rr.Note = strings.TrimSpace(rr.Note + " answer does not contain the expected text")
			}
		}
		res.Runs = append(res.Runs, rr)
		fmt.Fprintf(&log, "===== verifier run %d/%d: exit=%d pass=%v score=%.3f duration=%dms timed_out=%v truncated=%v %s\n%s\n",
			i+1, repeats, rr.ExitCode, rr.Pass, rr.Score, rr.Ms, rr.TimedOut, rr.Truncated, rr.Note, out)
	}
	res.Ms = time.Since(started).Milliseconds()

	if res.Rejected != "" {
		res.Pass, res.Score = false, 0
	} else {
		res.Pass, res.Score, res.Flaky = combineRuns(res.Runs, opts.PassPolicy)
		last := res.Runs[len(res.Runs)-1]
		res.ExitCode, res.TimedOut, res.Truncated = last.ExitCode, last.TimedOut, last.Truncated
	}
	header := m.logHeader(task, res)
	res.Log = header + log.String()
	return finishResult(res, opts)
}

// runVerifierOnce prepares a fresh clean checkout, applies the filtered diff,
// writes the hidden files and runs the verifier command once.
func (m *Workspaces) runVerifierOnce(ctx context.Context, in verifyInput, fr filterResult, pm PassMode,
	timeout time.Duration, maxOut int64, idx int, res *Result) (rr VerifyRun, output, rejected string, err error) {
	task, opts := in.task, in.opts
	co, err := m.newCheckout(ctx, in.snap, task, fmt.Sprint(idx))
	if err != nil {
		return rr, "", "", err
	}
	if opts.KeepCheckout {
		res.CheckoutDir = co.dir
	} else {
		defer m.removeCheckout(co)
	}
	if err := m.applyPatch(ctx, co.tree, fr.Patch); err != nil {
		var rej *errPatchRejected
		if errors.As(err, &rej) {
			return rr, "", rej.msg, nil
		}
		return rr, "", "", err
	}
	written, err := m.writeHidden(co.tree, task.Verifier.Hidden, opts.HiddenBlobs)
	if err != nil {
		return rr, "", "", err
	}
	res.HiddenWritten = written

	pol := ExecPolicy{
		Network: task.Network, MaxOutput: maxOut, Marker: co.id,
		Limits: m.opts.Limits, Grace: m.opts.KillGrace,
	}
	env := m.commandEnv(co.home, co.tmp, task.Network, co.id)
	defer sweepMarker(co.id)
	er, err := m.sandbox.Exec(WithExecPolicy(ctx, pol), co.dirIn, env, task.Verifier.Cmd, timeout)
	if err != nil {
		if ctx.Err() != nil {
			return rr, "", "", ctx.Err()
		}
		return rr, "", "", Infra("run verifier", err)
	}
	if er.Canceled {
		if cerr := ctx.Err(); cerr != nil {
			return rr, "", "", cerr
		}
	}
	for _, w := range er.Warnings {
		if !slices.Contains(res.Warnings, w) {
			res.Warnings = append(res.Warnings, w)
		}
	}
	rr = judgeRun(pm, er)
	return rr, er.Output, "", nil
}

// judgeRun applies the pass mode to one execution. Nothing here is an error:
// a hung, killed or garbled verifier is a failed verdict.
func judgeRun(pm PassMode, er ExecResult) VerifyRun {
	rr := VerifyRun{ExitCode: er.ExitCode, TimedOut: er.TimedOut, Truncated: er.Truncated, Signal: er.Signal, Ms: er.Duration.Milliseconds()}
	switch {
	case er.TimedOut:
		rr.Note = "verifier timed out"
		return rr
	case er.OutputKilled:
		rr.Note = "verifier was killed for printing too much output"
		return rr
	case er.Signal != 0:
		rr.Note = fmt.Sprintf("verifier was killed by signal %d", er.Signal)
		return rr
	}
	switch pm.Kind {
	case PassRegex:
		if pm.Regex.MatchString(er.Stdout) {
			rr.Pass, rr.Score = true, 1
		} else {
			rr.Note = "stdout does not match the pass pattern"
			if er.Truncated {
				rr.Note += " (output was truncated)"
			}
		}
	case PassJSONScore:
		score, ok := lastLineScore(er.Stdout)
		switch {
		case !ok:
			rr.Note = "the last line of stdout is not a {\"score\": x} object"
		default:
			rr.Score = score
			rr.Pass = score >= 1-1e-9
		}
	default:
		switch er.ExitCode {
		case 0:
			rr.Pass, rr.Score = true, 1
		case 126, 127:
			rr.Note = "command not found or not executable: check the verifier command and toolchain"
		}
	}
	return rr
}

// lastLineScore parses the last non-empty line of stdout as {"score": x}. The
// score is clamped to [0,1].
func lastLineScore(stdout string) (float64, bool) {
	lines := strings.Split(strings.TrimRight(stdout, "\r\n \t"), "\n")
	last := strings.TrimSpace(lines[len(lines)-1])
	if last == "" {
		return 0, false
	}
	var obj struct {
		Score *float64 `json:"score"`
	}
	if err := json.Unmarshal([]byte(last), &obj); err != nil || obj.Score == nil {
		return 0, false
	}
	s := *obj.Score
	if math.IsNaN(s) {
		return 0, false
	}
	return math.Max(0, math.Min(1, s)), true
}

func judgeExpect(e Expect, answer string) (score float64, pass bool) {
	hay := answer
	if e.Fold {
		hay = strings.ToLower(hay)
	}
	found := 0
	for _, c := range e.Contains {
		needle := c
		if e.Fold {
			needle = strings.ToLower(needle)
		}
		if strings.Contains(hay, needle) {
			found++
		}
	}
	return float64(found) / float64(len(e.Contains)), found == len(e.Contains)
}

// combineRuns folds repeated runs into one verdict. The score is the mean, so a
// verifier that passes two runs out of three reports 0.667 whatever the policy.
func combineRuns(runs []VerifyRun, policy string) (pass bool, score float64, flaky bool) {
	if len(runs) == 0 {
		return false, 0, false
	}
	passes := 0
	for _, r := range runs {
		score += r.Score
		if r.Pass {
			passes++
		}
	}
	score /= float64(len(runs))
	switch policy {
	case PassAny:
		pass = passes > 0
	case PassMajority:
		pass = passes*2 > len(runs)
	default:
		pass = passes == len(runs)
	}
	return pass, score, passes != 0 && passes != len(runs)
}

// verifyAnswerOnly judges a task that has no command: only the final answer.
func verifyAnswerOnly(task rl.Task, opts VerifyOptions) (Result, error) {
	res := Result{Version: VerifierVersion(task), Mode: "expect"}
	e, err := ParseExpect(task.Verifier.Expect)
	if err != nil {
		return res, Infra("verify", err)
	}
	score, pass := judgeExpect(e, opts.Answer)
	res.Pass, res.Score = pass, score
	res.Runs = []VerifyRun{{Pass: pass, Score: score}}
	res.Log = fmt.Sprintf("===== answer check: pass=%v score=%.3f (%d expected strings)\n", pass, score, len(e.Contains))
	if !pass {
		res.Runs[0].Note = "answer does not contain the expected text"
	}
	return finishResult(res, opts)
}

// verifierFilesTouched picks out of the touched protected paths those that are
// part of the verifier: named in its command line, or hidden files.
func verifierFilesTouched(task rl.Task, touched []string) []string {
	if len(touched) == 0 {
		return nil
	}
	tokens := map[string]bool{}
	for _, tok := range strings.FieldsFunc(task.Verifier.Cmd, func(r rune) bool {
		return strings.ContainsRune(" \t\r\n;&|<>()\"'`$=", r)
	}) {
		tokens[foldPath(strings.TrimPrefix(tok, "./"))] = true
	}
	var out []string
	for _, p := range touched {
		fp := foldPath(p)
		if tokens[fp] {
			out = append(out, p)
			continue
		}
		for h := range task.Verifier.Hidden {
			if foldPath(h) == fp {
				out = append(out, p)
				break
			}
		}
	}
	return out
}

// writeHidden writes the hidden files into the clean checkout, and only there.
// Blob content is checked against its hash on the way in: the store is
// content-addressed, so a mismatch means someone edited the store (an agent
// that found it, say) and the verifier must not trust the file.
func (m *Workspaces) writeHidden(dir string, hidden map[string]string, blobs events.Blobs) ([]string, error) {
	if len(hidden) == 0 {
		return nil, nil
	}
	names := make([]string, 0, len(hidden))
	for n := range hidden {
		names = append(names, n)
	}
	sort.Strings(names)
	root, err := os.OpenRoot(dir)
	if err != nil {
		return nil, Infra("hidden files", err)
	}
	defer func() { _ = root.Close() }()
	for _, name := range names {
		ref := hidden[name]
		var data []byte
		switch {
		case strings.HasPrefix(ref, "text:"):
			data = []byte(strings.TrimPrefix(ref, "text:"))
		case strings.HasPrefix(ref, "blob:"):
			if blobs == nil {
				return nil, Infraf("hidden files", "%s refers to %s but no hidden blob store was configured", name, ref)
			}
			h := core.Hash(strings.TrimPrefix(ref, "blob:"))
			data, err = blobs.Get(h)
			if err != nil {
				return nil, Infra("hidden files", fmt.Errorf("%s: %w", name, err))
			}
			if core.HashBytes(data) != h {
				return nil, Infraf("hidden files", "blob %s for %s does not match its hash: the blob store was modified", h.Short(), name)
			}
		default:
			return nil, Infraf("hidden files", "%s: unsupported reference %q", name, ref)
		}
		mode := os.FileMode(0o644)
		if strings.HasPrefix(string(data), "#!") {
			mode = 0o755
		}
		if err := writeFileIn(root, name, data, mode); err != nil {
			return nil, Infra("hidden files", err)
		}
	}
	return names, nil
}

func (m *Workspaces) logHeader(task rl.Task, r Result) string {
	var b strings.Builder
	fmt.Fprintf(&b, "task: %s\nverifier: %s (%s)\ncommand: %s\n", task.ID, r.Version, r.Mode, task.Verifier.Cmd)
	fmt.Fprintf(&b, "result: pass=%v score=%.3f runs=%d flaky=%v\n", r.Pass, r.Score, len(r.Runs), r.Flaky)
	if r.Rejected != "" {
		fmt.Fprintf(&b, "rejected: %s\n", r.Rejected)
	}
	if len(r.ProtectedTouched) > 0 {
		fmt.Fprintf(&b, "protected paths touched by the agent (changes discarded): %s\n", strings.Join(r.ProtectedTouched, ", "))
	}
	for _, s := range r.Skipped {
		fmt.Fprintf(&b, "skipped %s: %s\n", s.Path, s.Reason)
	}
	for _, w := range r.Warnings {
		fmt.Fprintf(&b, "warning: %s\n", w)
	}
	b.WriteString("\n")
	return b.String()
}

func finishRejected(res Result, opts VerifyOptions) (Result, error) {
	res.Log = fmt.Sprintf("rejected: %s\n", res.Rejected)
	return finishResult(res, opts)
}

// finishResult stores the diff and log where the caller asked.
func finishResult(res Result, opts VerifyOptions) (Result, error) {
	if opts.OutDir != "" {
		if err := os.MkdirAll(opts.OutDir, 0o755); err != nil {
			return res, Infra("write results", err)
		}
		if err := atomicWriteFile(filepath.Join(opts.OutDir, "verifier.log"), []byte(res.Log), 0o644); err != nil {
			return res, Infra("write results", err)
		}
		// diff.patch always exists, empty when the agent changed nothing: a missing
		// file would be indistinguishable from a run that never got verified.
		if err := atomicWriteFile(filepath.Join(opts.OutDir, "diff.patch"), res.Diff, 0o644); err != nil {
			return res, Infra("write results", err)
		}
	}
	if opts.Store != nil {
		var err error
		if res.LogBlob, err = opts.Store.Put([]byte(res.Log)); err != nil {
			return res, Infra("store log", err)
		}
		if res.DiffBlob, err = opts.Store.Put(res.Diff); err != nil {
			return res, Infra("store diff", err)
		}
	}
	return res, nil
}
