package swarm

import (
	"context"
	"encoding/json"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/anemos-labs/sleipnir/internal/core"
	"github.com/anemos-labs/sleipnir/internal/events"
	"github.com/anemos-labs/sleipnir/internal/tools"
)

// Seeded-regression tests for the stall sweep: each named stall is seeded and must be
// found (once, with its nudge), must clear when its condition ends, and must not be
// found in the healthy version of the same situation. Sweeps are run by hand
// (SuperviseEvery is an hour), so what a sweep sees is what the test set up.

// stallCfg turns the turn-based kinds on at three turns and the supervisor off.
func stallCfg() Config { return Config{StallTurns: 3, SuperviseEvery: time.Hour, StuckAfter: -1} }

// stallEvent is a decoded swarm.stall event.
type stallEv struct{ Action, Kind, Task, Agent, Notify, Detail string }

// stallEvents returns the swarm.stall events of the log, in order.
func stallEvents(r *rvRig) []stallEv {
	var out []stallEv
	for _, e := range r.log.OfType(events.TypeSwarmStall) {
		var d stallEv
		_ = json.Unmarshal(e.Data, &d)
		out = append(out, d)
	}
	return out
}

// raised counts the raise events of a kind.
func raised(r *rvRig, kind StallKind) int {
	n := 0
	for _, e := range stallEvents(r) {
		if e.Action == "raise" && e.Kind == string(kind) {
			n++
		}
	}
	return n
}

// active reports whether a finding of the kind holds now.
func active(r *rvRig, kind StallKind) (Stall, bool) {
	for _, f := range r.sw.Stalls() {
		if f.Kind == kind {
			return f, true
		}
	}
	return Stall{}, false
}

// nudged reports whether the harness mailed the agent a stall check.
func nudged(r *rvRig, to string) bool {
	for _, e := range r.log.OfType(events.TypeMailSend) {
		var m struct{ From, To, Text string }
		_ = json.Unmarshal(e.Data, &m)
		if m.From == harnessSender && m.To == to && strings.HasPrefix(m.Text, "Stall check:") {
			return true
		}
	}
	return false
}

// gatedWorkers scripts workers: each request of an agent gets the next reply from its
// script; past the end of it the request blocks until the test ends.
func gatedWorkers(scripts map[string][]rvReply) func(ctx context.Context, c *rvCall) rvReply {
	return func(ctx context.Context, c *rvCall) rvReply {
		if s, ok := scripts[c.Agent]; ok && c.Assistants < len(s) {
			return s[c.Assistants]
		}
		<-ctx.Done()
		return rvReply{}
	}
}

func repeat(n int, r rvReply) []rvReply {
	out := make([]rvReply, n)
	for i := range out {
		out[i] = r
	}
	return out
}

var (
	readReply = rvReply{Tools: []rvToolCall{{Name: "read", Args: map[string]any{"path": "api/users.go"}}}}
	editReply = rvReply{Tools: []rvToolCall{{Name: "edit", Args: map[string]any{"path": "api/users.go"}}}}
)

// spawnWorker starts a backend worker on a new task and waits until it has made want
// requests (the last of which blocks).
func spawnWorker(t *testing.T, r *rvRig, title string, want int) (string, Task) {
	t.Helper()
	id, err := r.sw.Spawn(SpawnReq{Role: "backend", Title: title, By: "mgr"})
	if err != nil {
		t.Fatal(err)
	}
	rvWait(t, id+" to reach its blocked request", func() bool { return r.prov.callsFor(id) >= want })
	task, _ := r.sw.Board.Snapshot().Task(currentTask(r.sw.Board.Snapshot(), id))
	return id, task
}

func TestStallClaimedNoProgress(t *testing.T) {
	t.Run("seeded: a worker that only reads", func(t *testing.T) {
		r := newRVRig(t, stallCfg(), gatedWorkers(map[string][]rvReply{"be-1": repeat(3, readReply)}))
		r.sw.StartManager()
		id, task := spawnWorker(t, r, "paginate users", 4)
		r.sw.sweepStalls()
		r.sw.sweepStalls() // idempotent: the finding is raised once
		f, ok := active(r, StallClaimedNoProgress)
		if !ok || f.Task != task.ID || f.Agent != id || f.Notify != id {
			t.Fatalf("finding = %+v, %v; stalls %+v", f, ok, r.sw.Stalls())
		}
		if n := raised(r, StallClaimedNoProgress); n != 1 {
			t.Fatalf("raised %d times", n)
		}
		if !nudged(r, id) {
			t.Error("the worker was not told")
		}
		if r.sw.StallCounts()[StallClaimedNoProgress] != 1 {
			t.Errorf("counts = %v", r.sw.StallCounts())
		}
		// The condition ends when the worker stops running: the finding clears itself.
		r.sw.stopRun(r.sw.get(id), "test over", false)
		rvWait(t, "the worker to stop", func() bool { return r.idle(id) })
		r.sw.sweepStalls()
		if _, ok := active(r, StallClaimedNoProgress); ok {
			t.Fatal("the finding outlived its condition")
		}
		evs := stallEvents(r)
		if last := evs[len(evs)-1]; last.Action != "clear" || last.Kind != string(StallClaimedNoProgress) {
			t.Fatalf("events = %+v", evs)
		}
	})
	t.Run("healthy: a worker that edits", func(t *testing.T) {
		r := newRVRig(t, stallCfg(), gatedWorkers(map[string][]rvReply{"be-1": {readReply, editReply, readReply, editReply}}))
		r.sw.StartManager()
		spawnWorker(t, r, "paginate users", 5)
		r.sw.sweepStalls()
		if n := raised(r, StallClaimedNoProgress); n != 0 {
			t.Fatalf("a worker that makes progress was found stalled: %+v", stallEvents(r))
		}
	})
}

// A worker that reads the code through the shell (cat, sed -n, grep, chained with && and echo banners, as one ten-hour run did
// 975 times in 1,147 commands without one stall finding) is reading: the sweep finds it as it finds a worker that reads with the
// read tool. A test run is progress (a tester's work, a writer's check), and so is a command that may change files.
func TestStallShellReadsAreNotProgress(t *testing.T) {
	bash := func(cmd string) rvReply {
		return rvReply{Tools: []rvToolCall{{Name: "bash", Args: map[string]any{"command": cmd}}}}
	}
	look := bash(`cd web && echo "===== Incidents.tsx =====" && cat -n src/pages/Incidents.tsx && sed -n '1,80p' src/lib/types.ts | head -40 2>/dev/null`)
	rig := func(t *testing.T, script []rvReply) *rvRig {
		r := newRVRig(t, stallCfg(), gatedWorkers(map[string][]rvReply{"be-1": script}))
		r.sw.deps.Registry.Register(rvFakeTool{name: "bash", run: func(context.Context, *tools.Call) *tools.Result {
			return &tools.Result{Text: "ok\n[exit code 0]", Meta: map[string]any{"exit_code": 0}}
		}})
		r.sw.StartManager()
		return r
	}
	t.Run("seeded: a worker that reads with shell commands", func(t *testing.T) {
		r := rig(t, repeat(3, look))
		id, task := spawnWorker(t, r, "incidents page", 4)
		r.sw.sweepStalls()
		f, ok := active(r, StallClaimedNoProgress)
		if !ok || f.Task != task.ID || f.Agent != id {
			t.Fatalf("a worker that only read through the shell for 3 turns was not found: %+v, %v; events %+v", f, ok, stallEvents(r))
		}
		if !nudged(r, id) {
			t.Error("the worker was not told")
		}
	})
	t.Run("healthy: a worker that runs the tests", func(t *testing.T) {
		r := rig(t, []rvReply{look, bash("go test ./api/..."), look, bash("cd web && npm test"), look})
		spawnWorker(t, r, "incidents page", 6)
		r.sw.sweepStalls()
		if n := raised(r, StallClaimedNoProgress); n != 0 {
			t.Fatalf("a worker that runs the tests was found stalled: %+v", stallEvents(r))
		}
	})
	t.Run("healthy: a worker that changes files with the shell", func(t *testing.T) {
		r := rig(t, []rvReply{look, bash("gofmt -w api/users.go"), look, bash("mkdir -p web/src/pages && cat src/a.ts"), look})
		spawnWorker(t, r, "incidents page", 6)
		r.sw.sweepStalls()
		if n := raised(r, StallClaimedNoProgress); n != 0 {
			t.Fatalf("a worker whose commands change files was found stalled: %+v", stallEvents(r))
		}
	})
}

// progressCall judges a shell command by what it can do: reading and printing is not progress, a test run, a check or anything
// that may write is; a failed call never is.
func TestProgressCallShell(t *testing.T) {
	s := &Swarm{}
	call := func(cmd string) core.Block {
		in, _ := json.Marshal(map[string]any{"command": cmd})
		return core.Block{ToolName: "bash", Input: in}
	}
	ok := &tools.Result{Text: "[exit code 0]", Meta: map[string]any{"exit_code": 0}}
	for cmd, want := range map[string]bool{
		`cat -n src/a.ts`: false,
		`cd /repo/web && echo "== a ==" && sed -n '64,115p' src/a.ts && grep -n "x" src/b.ts`: false,
		`find web -type f 2>/dev/null | sort || echo "none"`:                                  false,
		`git log --oneline -5 && git diff --stat`:                                             false,
		`go test ./...`:                                    true,
		`go vet ./internal/swarm`:                          true,
		`cd web && npm test -- --run Incidents`:            true,
		`echo "== tests ==" && go test ./api/ 2>&1 | tail`: true,
		`npx tsc --noEmit -p tsconfig.json`:                true,
		`sed -i 's/a/b/' x.go`:                             true,
		`cat > notes.md <<'EOF'\nx\nEOF`:                   true,
		`python3 scripts/gen.py`:                           true,
	} {
		if got := s.progressCall(call(cmd), ok); got != want {
			t.Errorf("progressCall(%q) = %v, want %v", cmd, got, want)
		}
	}
	if s.progressCall(call("go test ./..."), &tools.Result{IsError: true, Text: "denied"}) {
		t.Error("a refused command is progress")
	}
}

func TestStallManagerWaitingOnIdle(t *testing.T) {
	t.Run("seeded: the manager waits while nobody works", func(t *testing.T) {
		r := newRVRig(t, stallCfg(), gatedWorkers(nil))
		r.sw.StartManager()
		task, _ := r.sw.Board.CreateTask("mgr", TaskSpec{Title: "paginate users"})
		ctx := context.Background()
		if res := r.callTool(ctx, "wait", "mgr", "manager", map[string]any{"until": []string{task.ID}}); !res.IsError {
			t.Fatalf("wait: %s", res.Text)
		}
		f, ok := active(r, StallManagerWaitingOnIdle)
		if !ok || f.Agent != "mgr" || !strings.Contains(f.Detail, task.ID) {
			t.Fatalf("finding = %+v, %v", f, ok)
		}
		r.callTool(ctx, "wait", "mgr", "manager", map[string]any{"until": []string{task.ID}}) // still the same stall
		r.sw.sweepStalls()
		if n := raised(r, StallManagerWaitingOnIdle); n != 1 {
			t.Fatalf("raised %d times", n)
		}
		// The manager acts: the condition ends.
		r.callTool(ctx, "task", "mgr", "manager", map[string]any{"action": "list"})
		r.sw.sweepStalls()
		if _, ok := active(r, StallManagerWaitingOnIdle); ok {
			t.Fatal("the finding outlived its condition")
		}
	})
	t.Run("healthy: the manager waits while a worker works", func(t *testing.T) {
		r := newRVRig(t, stallCfg(), gatedWorkers(nil))
		r.sw.StartManager()
		_, task := spawnWorker(t, r, "paginate users", 1)
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		res := r.callTool(ctx, "wait", "mgr", "manager", map[string]any{"until": []string{task.ID}, "timeout_sec": 3})
		if res.IsError {
			t.Fatalf("wait: %s", res.Text)
		}
		r.sw.sweepStalls()
		if n := raised(r, StallManagerWaitingOnIdle); n != 0 {
			t.Fatalf("a manager waiting on a running worker was found stalled: %+v", stallEvents(r))
		}
	})
}

func TestStallOrphanedTask(t *testing.T) {
	t.Run("seeded: a task owned by an agent that is not on the team", func(t *testing.T) {
		r := newRVRig(t, stallCfg(), gatedWorkers(nil))
		r.sw.StartManager()
		task, _ := r.sw.Board.CreateTask("mgr", TaskSpec{Title: "paginate users"})
		if err := r.sw.Board.Assign("mgr", "be-9", task.ID); err != nil {
			t.Fatal(err)
		}
		r.sw.sweepStalls()
		if _, ok := active(r, StallOrphanedTask); ok {
			t.Fatal("raised on the first sight: a spawn in progress looks the same")
		}
		r.sw.sweepStalls()
		f, ok := active(r, StallOrphanedTask)
		if !ok || f.Task != task.ID || f.Agent != "be-9" || f.Notify != "mgr" {
			t.Fatalf("finding = %+v, %v", f, ok)
		}
		if !nudged(r, "mgr") {
			t.Error("the manager was not told")
		}
		if err := r.sw.Board.Fail("mgr", task.ID, closeAs(CloseCanceled), "nobody owns it"); err != nil {
			t.Fatal(err)
		}
		r.sw.sweepStalls()
		if _, ok := active(r, StallOrphanedTask); ok {
			t.Fatal("the finding outlived its condition")
		}
	})
	t.Run("healthy: a spawn in progress is never one, however many sweeps see it", func(t *testing.T) {
		r := newRVRig(t, stallCfg(), gatedWorkers(nil))
		r.sw.StartManager()
		task, _ := r.sw.Board.CreateTask("mgr", TaskSpec{Title: "paginate users"})
		done := r.sw.arrive("be-9") // as Spawn does between the assignment and the registration
		if err := r.sw.Board.Assign("mgr", "be-9", task.ID); err != nil {
			t.Fatal(err)
		}
		for i := 0; i < 5; i++ {
			r.sw.sweepStalls()
		}
		if n := raised(r, StallOrphanedTask); n != 0 {
			t.Fatalf("a task of an agent that is arriving was found orphaned: %+v", stallEvents(r))
		}
		done() // the spawn failed or finished without registering: now it is one, on the second sweep
		r.sw.sweepStalls()
		r.sw.sweepStalls()
		if _, ok := active(r, StallOrphanedTask); !ok {
			t.Fatal("the mark outlived the spawn")
		}
	})
	t.Run("healthy: a task owned by a running worker", func(t *testing.T) {
		r := newRVRig(t, stallCfg(), gatedWorkers(nil))
		r.sw.StartManager()
		spawnWorker(t, r, "paginate users", 1)
		r.sw.sweepStalls()
		r.sw.sweepStalls()
		if n := raised(r, StallOrphanedTask); n != 0 {
			t.Fatalf("found an orphan: %+v", stallEvents(r))
		}
	})
}

func TestStallBlockedCycle(t *testing.T) {
	setup := func(t *testing.T) (*rvRig, []Task) {
		r := newRVRig(t, stallCfg(), gatedWorkers(nil))
		r.sw.StartManager()
		var tasks []Task
		for _, title := range []string{"api", "client", "docs"} {
			_, task := spawnWorker(t, r, title, 1)
			tasks = append(tasks, task)
		}
		return r, tasks
	}
	t.Run("seeded: tasks blocked on each other", func(t *testing.T) {
		r, ts := setup(t)
		b := r.sw.Board
		must := func(err error) {
			t.Helper()
			if err != nil {
				t.Fatal(err)
			}
		}
		// T3 waits for T2, T2 for T1, T1 for T2: one cycle (T1, T2) with a tail.
		must(b.BlockOn(ts[2].Owner, ts[2].ID, ts[1].ID, "needs the client"))
		must(b.BlockOn(ts[1].Owner, ts[1].ID, ts[0].ID, "needs the api"))
		must(b.BlockOn(ts[0].Owner, ts[0].ID, ts[1].ID, "needs the client"))
		r.sw.sweepStalls()
		r.sw.sweepStalls()
		var cycles []Stall
		for _, f := range r.sw.Stalls() {
			if f.Kind == StallBlockedCycle {
				cycles = append(cycles, f)
			}
		}
		if len(cycles) != 1 || cycles[0].Task != "T1" || cycles[0].Detail != "blocked on each other: T1 -> T2 -> T1" || cycles[0].Notify != "mgr" {
			t.Fatalf("cycles = %+v", cycles)
		}
		if raised(r, StallBlockedCycle) != 1 || !nudged(r, "mgr") {
			t.Fatalf("events %+v", stallEvents(r))
		}
		must(b.Unblock("mgr", ts[1].ID))
		r.sw.sweepStalls()
		if _, ok := active(r, StallBlockedCycle); ok {
			t.Fatal("the finding outlived its condition")
		}
	})
	t.Run("healthy: a chain that ends in work", func(t *testing.T) {
		r, ts := setup(t)
		if err := r.sw.Board.BlockOn(ts[0].Owner, ts[0].ID, ts[1].ID, "needs the client"); err != nil {
			t.Fatal(err)
		}
		if err := r.sw.Board.BlockOn(ts[2].Owner, ts[2].ID, ts[0].ID, "needs the api"); err != nil {
			t.Fatal(err)
		}
		r.sw.sweepStalls()
		if n := raised(r, StallBlockedCycle); n != 0 {
			t.Fatalf("found a cycle: %+v", stallEvents(r))
		}
	})
}

func TestStallReviewStarved(t *testing.T) {
	setup := func(t *testing.T) (*rvRig, *member, Task) {
		r := newRVRig(t, stallCfg(), gatedWorkers(nil))
		if _, err := r.sw.StartManager(); err != nil {
			t.Fatal(err)
		}
		b := r.sw.Board
		task, _ := b.CreateTask("mgr", TaskSpec{Title: "paginate users"})
		if err := b.Assign("mgr", "be-1", task.ID); err != nil {
			t.Fatal(err)
		}
		if err := b.Submit("be-1", task.ID, "paginated", "edited api/users.go"); err != nil {
			t.Fatal(err)
		}
		r.sw.sweepStalls() // the review clock starts
		return r, r.sw.get("mgr"), task
	}
	t.Run("seeded: the manager works on other things", func(t *testing.T) {
		r, mgr, task := setup(t)
		mgr.turns.Add(3) // three manager turns that do not look at the task
		r.callTool(context.Background(), "task", "mgr", "manager", map[string]any{"action": "list"})
		r.sw.sweepStalls()
		f, ok := active(r, StallReviewStarved)
		if !ok || f.Task != task.ID || f.Notify != "mgr" {
			t.Fatalf("finding = %+v, %v", f, ok)
		}
		if !nudged(r, "mgr") {
			t.Error("the manager was not told")
		}
		if err := r.sw.Board.Accept("mgr", task.ID, ""); err != nil {
			t.Fatal(err)
		}
		r.sw.sweepStalls()
		if _, ok := active(r, StallReviewStarved); ok {
			t.Fatal("the finding outlived its condition")
		}
	})
	t.Run("healthy: the manager reads the submission", func(t *testing.T) {
		r, mgr, task := setup(t)
		mgr.turns.Add(2)
		r.callTool(context.Background(), "task", "mgr", "manager", map[string]any{"action": "get", "id": task.ID})
		r.sw.sweepStalls()
		mgr.turns.Add(2)
		r.sw.sweepStalls()
		if n := raised(r, StallReviewStarved); n != 0 {
			t.Fatalf("a submission the manager is reading was found starved: %+v", stallEvents(r))
		}
	})
}

// A whole healthy run, swept continuously, names no stall at all.
func TestAHealthyRunHasNoStalls(t *testing.T) {
	var turn atomic.Int32
	cfg := stallCfg()
	cfg.SuperviseEvery = 2 * time.Millisecond
	r := newRVRig(t, cfg, func(ctx context.Context, c *rvCall) rvReply {
		switch c.Role {
		case "manager":
			switch turn.Add(1) {
			case 1:
				return rvReply{Tools: []rvToolCall{
					{Name: "task", Args: map[string]any{"action": "create", "title": "paginate users", "description": "cursor pagination", "files": []string{"api/**"}}},
					{Name: "spawn", Args: map[string]any{"role": "backend", "task": "T1"}},
				}}
			case 2:
				return rvReply{Tools: []rvToolCall{{Name: "wait", Args: map[string]any{"until": []string{"T1"}, "timeout_sec": 20}}}}
			case 3:
				return rvReply{Tools: []rvToolCall{{Name: "task", Args: map[string]any{"action": "accept", "id": "T1"}}}}
			}
			return rvReply{Text: "users are paginated"}
		case "backend":
			switch c.Assistants {
			case 0:
				return rvReply{Tools: []rvToolCall{{Name: "read", Args: map[string]any{"path": "api/users.go"}}, {Name: "edit", Args: map[string]any{"path": "api/users.go"}}}}
			case 1:
				return rvReply{Tools: []rvToolCall{{Name: "task", Args: map[string]any{"action": "done", "id": "T1", "text": "paginated"}}}}
			}
			return rvReply{Text: "T1 is paginated"}
		}
		return rvReply{Text: "ok"}
	})
	if _, err := r.sw.RunManager(context.Background(), "paginate the users list"); err != nil {
		t.Fatal(err)
	}
	if tk, _ := r.sw.Board.Snapshot().Task("T1"); tk.Status != StatusDone {
		t.Fatalf("T1 = %+v", tk)
	}
	r.sw.sweepStalls()
	if evs := stallEvents(r); len(evs) != 0 {
		t.Fatalf("a healthy run named stalls: %+v", evs)
	}
}

// A reviewer whose run outlasts the work it reviews is told to write its report: in one run a reviewer spent twenty requests
// of two to three minutes each re-walking four small modules that took fifteen minutes to build, and the run hit its time
// limit without a report. The bound follows the work (the unreviewed merges' durations summed, at least ten minutes), so a
// review within it is not a finding, and the finding clears when the review ends.
func TestStallReviewOverrun(t *testing.T) {
	cfg := stallCfg()
	cfg.StallTurns, cfg.Team, cfg.MaxWorkers = 100, []string{"backend", "reviewer"}, 8
	r := newRVRig(t, cfg, gatedWorkers(map[string][]rvReply{"rv-1": repeat(2, readReply)}))
	r.sw.StartManager()
	now := r.sw.deps.Now()
	work := Task{ID: "T1", Role: "backend", Rev: 1}
	r.sw.review.noteBegan(work.ID, work.Rev, now.Add(-19*time.Minute))
	r.sw.noteAccepted(work)
	if took, ids := r.sw.review.unreviewedWork(); took.Round(time.Minute) != 19*time.Minute || len(ids) != 1 || ids[0] != "T1" {
		t.Fatalf("unreviewed work = %v %v", took, ids)
	}
	id, err := r.sw.Spawn(SpawnReq{Role: ReviewerRole, Title: "review T1", By: "mgr"})
	if err != nil {
		t.Fatal(err)
	}
	rvWait(t, id+" to reach its blocked request", func() bool { return r.prov.callsFor(id) >= 3 })
	task := currentTask(r.sw.Board.Snapshot(), id)
	backdate := func(d time.Duration) {
		m := r.sw.get(id)
		m.mu.Lock()
		m.run.started = r.sw.deps.Now().Add(-d)
		m.mu.Unlock()
	}
	backdate(12 * time.Minute)
	r.sw.sweepStalls()
	if _, ok := active(r, StallReviewOverrun); ok {
		t.Fatal("a review within the work's own duration was found overrun")
	}
	backdate(25 * time.Minute)
	r.sw.sweepStalls()
	r.sw.sweepStalls()
	f, ok := active(r, StallReviewOverrun)
	if !ok || f.Task != task || f.Agent != id || f.Notify != id {
		t.Fatalf("finding = %+v, %v; stalls %+v", f, ok, r.sw.Stalls())
	}
	if want := id + " has reviewed " + task + " for 25 minutes; the work it reviews (T1) took 19 minutes to build"; f.Detail != want {
		t.Errorf("detail = %q, want %q", f.Detail, want)
	}
	if n := raised(r, StallReviewOverrun); n != 1 {
		t.Errorf("raised %d times", n)
	}
	if !nudged(r, id) {
		t.Error("the reviewer was not told")
	}
	r.sw.stopRun(r.sw.get(id), "test over", false)
	rvWait(t, "the reviewer to stop", func() bool { return r.idle(id) })
	r.sw.sweepStalls()
	if _, ok := active(r, StallReviewOverrun); ok {
		t.Fatal("the finding outlived the review")
	}
}

// With nothing unreviewed, the floor bounds a review: ten minutes.
func TestStallReviewOverrunFloor(t *testing.T) {
	cfg := stallCfg()
	cfg.StallTurns, cfg.Team, cfg.MaxWorkers = 100, []string{"backend", "reviewer"}, 8
	r := newRVRig(t, cfg, gatedWorkers(map[string][]rvReply{"rv-1": repeat(2, readReply)}))
	r.sw.StartManager()
	id, err := r.sw.Spawn(SpawnReq{Role: ReviewerRole, Title: "review the tree", By: "mgr"})
	if err != nil {
		t.Fatal(err)
	}
	rvWait(t, id+" to reach its blocked request", func() bool { return r.prov.callsFor(id) >= 3 })
	m := r.sw.get(id)
	m.mu.Lock()
	m.run.started = r.sw.deps.Now().Add(-9 * time.Minute)
	m.mu.Unlock()
	r.sw.sweepStalls()
	if _, ok := active(r, StallReviewOverrun); ok {
		t.Fatal("a nine-minute review was found overrun")
	}
	m.mu.Lock()
	m.run.started = r.sw.deps.Now().Add(-11 * time.Minute)
	m.mu.Unlock()
	r.sw.sweepStalls()
	f, ok := active(r, StallReviewOverrun)
	if !ok || !strings.HasSuffix(f.Detail, "the work it reviews (no merge) took 0 minutes to build") {
		t.Fatalf("finding = %+v, %v", f, ok)
	}
	r.sw.stopRun(m, "test over", false)
	rvWait(t, "the reviewer to stop", func() bool { return r.idle(id) })
}
