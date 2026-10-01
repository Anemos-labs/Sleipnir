package env

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/anemos-labs/sleipnir/internal/core"
	"github.com/anemos-labs/sleipnir/internal/events"
	"github.com/anemos-labs/sleipnir/internal/rl"
)

// This file provides a scripted Harness for tests, and a minimal Extractor, so
// the whole rollout path can be exercised (and other packages' tests can use
// the environment) without the real agent loop.

// FakeStep is one scripted action of a FakeHarness run.
type FakeStep struct {
	Op      string // write, append, delete, chmod, symlink, mkdir, exec, sleep, hang, hang-forever, panic, infra, agent-fail
	Path    string
	Data    string
	Mode    os.FileMode
	Dur     time.Duration
	Attempt int // when > 0 the step only runs on that attempt (1-based)
}

// Step constructors.
func FakeWrite(path, content string) FakeStep {
	return FakeStep{Op: "write", Path: path, Data: content, Mode: 0o644}
}
func FakeWriteMode(path, content string, mode os.FileMode) FakeStep {
	return FakeStep{Op: "write", Path: path, Data: content, Mode: mode}
}
func FakeAppend(path, content string) FakeStep {
	return FakeStep{Op: "append", Path: path, Data: content}
}
func FakeDelete(path string) FakeStep { return FakeStep{Op: "delete", Path: path} }
func FakeChmod(path string, mode os.FileMode) FakeStep {
	return FakeStep{Op: "chmod", Path: path, Mode: mode}
}
func FakeSymlink(path, target string) FakeStep {
	return FakeStep{Op: "symlink", Path: path, Data: target}
}
func FakeMkdir(path string) FakeStep     { return FakeStep{Op: "mkdir", Path: path} }
func FakeExec(cmd string) FakeStep       { return FakeStep{Op: "exec", Data: cmd} }
func FakeSleep(d time.Duration) FakeStep { return FakeStep{Op: "sleep", Dur: d} }
func FakeHang() FakeStep                 { return FakeStep{Op: "hang"} }
func FakeHangForever() FakeStep          { return FakeStep{Op: "hang-forever"} }
func FakePanic(msg string) FakeStep      { return FakeStep{Op: "panic", Data: msg} }
func FakeInfra(msg string) FakeStep      { return FakeStep{Op: "infra", Data: msg} }
func FakeAgentFail(msg string) FakeStep  { return FakeStep{Op: "agent-fail", Data: msg} }

// OnAttempt restricts a step to one attempt (1-based), which is how tests make
// a rollout fail on the first try and succeed on the retry.
func (s FakeStep) OnAttempt(n int) FakeStep { s.Attempt = n; return s }

// FakeScript is what one rollout does.
type FakeScript struct {
	Steps   []FakeStep
	Final   string
	Claimed string
}

// FakeCall records one Run invocation.
type FakeCall struct {
	Task      string
	Sample    int
	Attempt   int
	Workspace string
	RunDir    string
	Env       []string
	NetPrefix []string
	Seed      int64
	Group     string
	Budget    rl.Budget
}

// FakeHarness is a scripted Harness. Scripts are looked up by "task/sample",
// then "task", then "*", then Default. Paths in steps are relative to the
// workspace; absolute paths are used as they are (so a script can also misbehave
// outside it). Like a real harness it writes events.jsonl and blobs/ in RunDir
// and closes them before returning, even when it panics.
type FakeHarness struct {
	Scripts map[string]FakeScript
	Default FakeScript
	// Func, when set, replaces the script machinery entirely.
	Func func(ctx context.Context, spec RunSpec) (RunResult, error)
	// Release ends "hang-forever" steps; a test must close it before it returns.
	Release chan struct{}

	mu        sync.Mutex
	calls     []FakeCall
	active    int
	maxActive int
}

// Calls returns the recorded invocations in call order.
func (h *FakeHarness) Calls() []FakeCall {
	h.mu.Lock()
	defer h.mu.Unlock()
	return append([]FakeCall(nil), h.calls...)
}

// MaxConcurrent reports the highest number of simultaneous Run calls seen.
func (h *FakeHarness) MaxConcurrent() int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.maxActive
}

func (h *FakeHarness) script(task string, sample int) FakeScript {
	if s, ok := h.Scripts[fmt.Sprintf("%s/%d", task, sample)]; ok {
		return s
	}
	if s, ok := h.Scripts[task]; ok {
		return s
	}
	if s, ok := h.Scripts["*"]; ok {
		return s
	}
	return h.Default
}

// Run implements Harness.
func (h *FakeHarness) Run(ctx context.Context, spec RunSpec) (RunResult, error) {
	h.mu.Lock()
	h.calls = append(h.calls, FakeCall{
		Task: spec.Task.ID, Sample: spec.Sample, Attempt: spec.Attempt, Workspace: spec.Workspace, RunDir: spec.RunDir,
		Env: spec.Env, NetPrefix: spec.NetPrefix, Seed: spec.Seed, Group: spec.Group, Budget: spec.Budget,
	})
	h.active++
	h.maxActive = max(h.maxActive, h.active)
	h.mu.Unlock()
	defer func() { h.mu.Lock(); h.active--; h.mu.Unlock() }()

	if h.Func != nil {
		return h.Func(ctx, spec)
	}
	sc := h.script(spec.Task.ID, spec.Sample)

	session := fmt.Sprintf("fake-%s-%d", spec.Task.ID, spec.Sample)
	log, err := events.Open(spec.RunDir, session)
	if err != nil {
		return RunResult{}, Infra("fake harness", err)
	}
	defer func() { _ = log.Close() }() // runs on panic too
	blobs, err := events.NewDirBlobs(filepath.Join(spec.RunDir, "blobs"))
	if err != nil {
		return RunResult{}, Infra("fake harness", err)
	}
	emit := func(typ string, data any) {
		_, _ = log.Emit("worker", typ, data)
	}
	emit(events.TypeSessionStart, map[string]any{"task": spec.Task.ID, "sample": spec.Sample, "attempt": spec.Attempt})
	emit(events.TypeAgentSpawn, map[string]any{"role": rl.RoleWorker, "model": spec.Policy.Model})

	res := RunResult{FinalMessage: sc.Final, Claimed: sc.Claimed}
	for i, st := range sc.Steps {
		if st.Attempt > 0 && st.Attempt != spec.Attempt {
			continue
		}
		emit(events.TypeModelRequest, map[string]any{"id": fmt.Sprintf("worker.%d", i+1), "kind": rl.KindMain, "role": rl.RoleWorker})
		desc := fmt.Sprintf("%s %s", st.Op, st.Path)
		hash, _ := blobs.Put([]byte(desc))
		emit(events.TypeToolCall, map[string]any{"name": st.Op, "path": st.Path, "blob": hash})
		out, err := h.doStep(ctx, spec, st)
		if err != nil {
			// A step that returns an error decides how the run ends.
			switch e := err.(type) {
			case *fakeEnd:
				emit(events.TypeSessionEnd, map[string]any{"reason": e.reason})
				return e.result(res), e.err
			}
			out = "error: " + err.Error()
		}
		emit(events.TypeToolResult, map[string]any{"name": st.Op, "output": out})
		emit(events.TypeModelResponse, map[string]any{"id": fmt.Sprintf("worker.%d", i+1)})
	}
	emit(events.TypeSessionEnd, map[string]any{"reason": "done"})
	if res.Claimed == "" {
		res.Claimed = "done"
	}
	return res, nil
}

// fakeEnd is returned by a step that ends the run.
type fakeEnd struct {
	reason string
	res    *RunResult
	err    error
}

func (e *fakeEnd) Error() string { return e.reason }
func (e *fakeEnd) result(base RunResult) RunResult {
	if e.res != nil {
		return *e.res
	}
	return base
}

func (h *FakeHarness) doStep(ctx context.Context, spec RunSpec, st FakeStep) (string, error) {
	target := func(p string) string {
		if filepath.IsAbs(p) {
			return p
		}
		return filepath.Join(spec.Workspace, p)
	}
	switch st.Op {
	case "write":
		p := target(st.Path)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			return "", err
		}
		mode := st.Mode
		if mode == 0 {
			mode = 0o644
		}
		if err := os.WriteFile(p, []byte(st.Data), mode); err != nil {
			return "", err
		}
		return "wrote " + st.Path, os.Chmod(p, mode)
	case "append":
		f, err := os.OpenFile(target(st.Path), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
		if err != nil {
			return "", err
		}
		defer func() { _ = f.Close() }()
		_, err = f.WriteString(st.Data)
		return "appended", err
	case "delete":
		return "deleted", os.RemoveAll(target(st.Path))
	case "chmod":
		return "chmod", os.Chmod(target(st.Path), st.Mode)
	case "symlink":
		p := target(st.Path)
		_ = os.MkdirAll(filepath.Dir(p), 0o755)
		_ = os.Remove(p)
		return "symlinked", os.Symlink(st.Data, p)
	case "mkdir":
		return "mkdir", os.MkdirAll(target(st.Path), 0o755)
	case "exec":
		cmd := exec.CommandContext(ctx, "sh", "-c", st.Data)
		cmd.Dir = spec.Workspace
		cmd.Env = append([]string{}, spec.Env...)
		configureProc(cmd)
		out, err := cmd.CombinedOutput()
		return strings.TrimSpace(string(out)), err
	case "sleep":
		select {
		case <-time.After(st.Dur):
		case <-ctx.Done():
			return "", &fakeEnd{reason: "cancelled", res: &RunResult{Claimed: "budget", Err: ctx.Err()}}
		}
		return "slept", nil
	case "hang":
		<-ctx.Done()
		return "", &fakeEnd{reason: "cancelled", res: &RunResult{Claimed: "budget", Err: ctx.Err()}}
	case "hang-forever":
		if h.Release != nil {
			<-h.Release
		} else {
			select {} // a harness that ignores its context, for good
		}
		return "", &fakeEnd{reason: "released", res: &RunResult{Claimed: "gave_up"}}
	case "panic":
		panic(st.Data)
	case "infra":
		return "", &fakeEnd{reason: "infra", err: Infra("fake provider", errors.New(st.Data))}
	case "agent-fail":
		return "", &fakeEnd{reason: "agent failure", res: &RunResult{Claimed: "gave_up", Err: errors.New(st.Data)}}
	}
	return "", fmt.Errorf("unknown fake step %q", st.Op)
}

// MinimalExtractor is an Extractor that needs nothing but the event log: it
// builds an Episode with one agent per distinct agent id seen on model.request
// events, one step per request, and the outcome recorded by Runner. It is the
// default when Runner.Extract is nil and is meant for tests and for using the
// environment before the real trajectory extractor is wired in; it does not
// reconstruct prompts.
func MinimalExtractor(runDir string, task rl.Task, sample int, group string) (*rl.Episode, error) {
	ep := &rl.Episode{
		Schema: rl.SchemaEpisode, ID: fmt.Sprintf("%s/%d", task.ID, sample), TaskID: task.ID, Group: group, Sample: sample,
		Provenance: rl.Provenance{License: task.Repo.License},
	}
	type agentAcc struct{ steps []rl.Step }
	agents := map[string]*agentAcc{}
	var order []string
	requests := 0
	sawEnd := false
	err := events.Scan(filepath.Join(runDir, "events.jsonl"), func(e events.Event) error {
		switch e.Type {
		case events.TypeModelRequest:
			requests++
			id := e.Agent
			if id == "" {
				id = "main"
			}
			a := agents[id]
			if a == nil {
				a = &agentAcc{}
				agents[id] = a
				order = append(order, id)
			}
			a.steps = append(a.steps, rl.Step{
				ID: fmt.Sprintf("%s.%d", id, len(a.steps)+1), Kind: rl.KindMain, Role: rl.RoleWorker, Trainable: true,
			})
		case events.TypeSessionEnd:
			sawEnd = true
		case events.TypeOutcome:
			var p struct {
				Kind    string    `json:"kind"`
				Pass    bool      `json:"pass"`
				Score   float64   `json:"score"`
				Version string    `json:"version"`
				Detail  core.Hash `json:"detail"`
				Ms      int64     `json:"ms"`
				Diff    core.Hash `json:"diff"`
				Claimed string    `json:"claimed"`
			}
			if err := json.Unmarshal(e.Data, &p); err != nil {
				return err
			}
			if p.Kind == "verifier" {
				ep.Outcome.Verifier = &rl.Verdict{Kind: "verifier", Pass: p.Pass, Score: p.Score, Version: p.Version, Detail: p.Detail, Ms: p.Ms}
				ep.Outcome.Claimed = p.Claimed
				ep.Outcome.Diff = p.Diff
			}
		}
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("reading events.jsonl: %w", err)
	}
	for _, id := range order {
		ep.Agents = append(ep.Agents, rl.Agent{ID: id, Role: rl.RoleWorker, Steps: agents[id].steps})
	}
	ep.Cost.Requests = requests
	ep.Signals = map[string]float64{rl.SigRequests: float64(requests), rl.SigSteps: float64(requests)}
	if !sawEnd && requests > 0 {
		ep.AddFlag(rl.FlagTruncated)
	}
	return ep, nil
}
