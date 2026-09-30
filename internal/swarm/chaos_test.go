package swarm

// Chaos, throttle and end-to-end tests (docs/reviews/swarm-concurrency.md): real
// agent runs over a fake provider while spawn/reuse/retire/mail race each other,
// the status throttle, and the real fs tools under stolen leases. TestConc_* that
// remain are repros of findings that live in other packages (they assert the
// correct behaviour and are gated behind SLEIPNIR_REVIEW=1); TestConcSound_* cover
// behaviour the review found sound.
//
//	SLEIPNIR_REVIEW=1 go test -race -count=1 -run 'TestConc_' ./internal/swarm

import (
	"context"
	"encoding/json"
	"fmt"
	"math/rand"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/reee344/sleipnir/internal/agent"
	"github.com/reee344/sleipnir/internal/core"
	"github.com/reee344/sleipnir/internal/events"
	"github.com/reee344/sleipnir/internal/kv"
	"github.com/reee344/sleipnir/internal/tools"
	"github.com/reee344/sleipnir/internal/tools/fs"
)

// Spawn, reuse, Retire, mail and hot rendering hammering a swarm of real agents
// for two seconds. At quiescence the roster, the board and the mailboxes must
// agree with each other.
func TestSwarmChaosLeavesConsistentState(t *testing.T) {
	r := newRVRig(t, Config{MaxAgents: 200, MaxWriters: 200,
		Router: RouterConfig{MaxPerMinute: 1 << 30, MaxPerPairPerMin: 1 << 30, MaxChars: 600, DedupeWindow: time.Nanosecond}},
		func(ctx context.Context, c *rvCall) rvReply {
			time.Sleep(time.Duration(rand.Intn(300)) * time.Microsecond)
			return rvReply{Text: "ok"}
		})
	r.sw.StartManager()
	stop := make(chan struct{})
	var wg sync.WaitGroup
	pick := func(rng *rand.Rand) string {
		ids := r.sw.roster()
		if len(ids) == 0 {
			return ""
		}
		return ids[rng.Intn(len(ids))]
	}
	spawn := func(seed int64) {
		defer wg.Done()
		rng := rand.New(rand.NewSource(seed))
		for i := 0; ; i++ {
			select {
			case <-stop:
				return
			default:
			}
			req := SpawnReq{Role: "reviewer", Title: fmt.Sprintf("job %d-%d", seed, i), By: "mgr"}
			if id := pick(rng); id != "" && id != "mgr" && rng.Intn(2) == 0 {
				req.Agent = id // reuse
			}
			_, _ = r.sw.Spawn(req)
		}
	}
	mail := func(seed int64) {
		defer wg.Done()
		rng := rand.New(rand.NewSource(seed))
		for i := 0; ; i++ {
			select {
			case <-stop:
				return
			default:
			}
			from, to := pick(rng), pick(rng)
			if from != "" && to != "" && from != to {
				_, _ = r.sw.Router.Send(from, to, "info", fmt.Sprintf("m %d %d", seed, i))
			}
		}
	}
	retire := func(seed int64) {
		defer wg.Done()
		rng := rand.New(rand.NewSource(seed))
		for {
			select {
			case <-stop:
				return
			default:
			}
			if id := pick(rng); id != "" && id != "mgr" {
				_ = r.sw.Retire(id)
			}
			time.Sleep(200 * time.Microsecond)
		}
	}
	read := func(seed int64) {
		defer wg.Done()
		rng := rand.New(rand.NewSource(seed))
		for {
			select {
			case <-stop:
				return
			default:
			}
			_ = r.sw.TotalCost()
			if id := pick(rng); id != "" {
				_ = RenderHot(r.sw.Board.Snapshot(), id, "reviewer", false, DefaultHotConfig(), r.sw.deps.Est)
			}
		}
	}
	for i := 0; i < 4; i++ {
		wg.Add(4)
		go spawn(int64(i))
		go mail(int64(100 + i))
		go retire(int64(200 + i))
		go read(int64(300 + i))
	}
	time.Sleep(2 * time.Second)
	close(stop)
	wg.Wait()
	rvWait(t, "all runs to finish", func() bool {
		for _, id := range r.sw.roster() {
			if r.running(id) {
				return false
			}
		}
		return true
	})
	time.Sleep(100 * time.Millisecond)

	snap := r.sw.Board.Snapshot()
	members := map[string]bool{}
	for _, id := range r.sw.roster() {
		members[id] = true
	}
	var ghosts, stuck, stranded, wrongState []string
	for _, a := range snap.Agents {
		if !members[a.ID] {
			ghosts = append(ghosts, a.ID)
		} else if a.State == "running" && !r.running(a.ID) && a.ID != "mgr" {
			wrongState = append(wrongState, a.ID)
		}
	}
	for _, tk := range snap.Tasks {
		if (tk.Status == StatusDoing || tk.Status == StatusBlocked) && (tk.Owner == "" || !members[tk.Owner]) {
			stuck = append(stuck, tk.ID+"/"+tk.Owner)
		}
	}
	for id := range members {
		if id == "mgr" {
			continue
		}
		if m := r.sw.get(id); m != nil && m.a.PendingInbox() > 0 {
			stranded = append(stranded, id)
		}
	}
	t.Logf("tasks=%d agents on board=%d members=%d", len(snap.Tasks), len(snap.Agents), len(members))
	if len(ghosts)+len(stuck)+len(stranded)+len(wrongState) > 0 {
		t.Fatalf("inconsistent at quiescence: %d ghost agents on the board but not in the swarm %v; %d tasks doing/blocked with a missing owner %v; %d idle agents with undelivered mail %v; %d agents shown running while idle %v",
			len(ghosts), head(ghosts), len(stuck), head(stuck), len(stranded), head(stranded), len(wrongState), head(wrongState))
	}
}

func head(s []string) []string {
	if len(s) > 4 {
		return s[:4]
	}
	return s
}

// A line change inside the 750ms throttle window is not dropped: the newest line is
// flushed when the window ends, so a long-running tool is shown for what it is.
func TestStatusThrottleFlushesTheTrailingLine(t *testing.T) {
	r := newRVRig(t, Config{MaxWriters: 4}, func(ctx context.Context, c *rvCall) rvReply { return rvReply{Text: "ok"} })
	r.sw.StartManager()
	id, err := r.sw.Spawn(SpawnReq{Role: "backend", Title: "work", By: "mgr"})
	if err != nil {
		t.Fatal(err)
	}
	rvWait(t, "worker idle", func() bool { return r.idle(id) })
	m := r.sw.get(id)
	k := &memberSink{Sink: agent.NopSink{}, s: r.sw, m: m, ev: m.ev}
	k.ToolStart(id, core.ToolUse("1", "edit", json.RawMessage(`{"path":"api/a.go"}`)))
	k.ToolStart(id, core.ToolUse("2", "bash", json.RawMessage(`{"command":"go test ./..."}`))) // five minutes of tests
	rvWait(t, "the trailing status line to be published", func() bool {
		a, _ := r.sw.Board.Snapshot().Agent(id)
		return a.Line == "running a command"
	})
	a, _ := r.sw.Board.Snapshot().Agent(id)
	if strings.Contains(a.Line, "go test") {
		t.Fatalf("the status line carries the command: %q", a.Line)
	}
}

// State transitions are never throttled: the final "idle" always lands.
func TestConcSound_ThrottleNeverDropsAStateChange(t *testing.T) {
	r := newRVRig(t, Config{MaxWriters: 4, MaxAgents: 100}, func(ctx context.Context, c *rvCall) rvReply {
		return rvReply{Tools: nil, Text: "ok"}
	})
	r.sw.StartManager()
	for i := 0; i < 8; i++ {
		id, err := r.sw.Spawn(SpawnReq{Role: "reviewer", Title: fmt.Sprintf("j%d", i), By: "mgr"})
		if err != nil {
			t.Fatal(err)
		}
		rvWait(t, "idle", func() bool { return r.idle(id) })
		// finishRun flips running=false first and publishes "idle" a few steps later, so
		// poll: the claim is that the transition is never dropped, not that it is instant.
		deadline := time.Now().Add(3 * time.Second)
		for {
			a, _ := r.sw.Board.Snapshot().Agent(id)
			if a.State == "idle" {
				break
			}
			if time.Now().After(deadline) {
				t.Fatalf("%s finished but the board still says %q 3s later", id, a.State)
			}
			time.Sleep(2 * time.Millisecond)
		}
	}
}

// The warm gate sits IN FRONT of the governor. When a cold prefix elects a
// worker-priority primer that then queues behind other worker traffic, a
// higher-priority follower (the manager) must wait for that primer even though the
// governor would have admitted it first.
func TestConc_ColdPrefixGateInvertsPriority(t *testing.T) {
	concGate(t)
	scenario := func(cold bool) time.Duration {
		gov := NewGovernor(GovernorConfig{MaxConcurrent: 1})
		gate := NewWarmGate(time.Minute, 10*time.Second)
		ctx := context.Background()
		if !cold {
			f, _ := gate.Enter(ctx, "prefix")
			f(true)
		}
		f, _ := gate.Enter(ctx, "other")
		f(true)
		request := func(prio int, hold time.Duration) (waited time.Duration) {
			start := time.Now()
			started, _ := gate.Enter(ctx, "prefix") // as agent.requestOnce: gate first ...
			rel, _ := gov.Acquire(ctx, prio)        // ... then the governor
			waited = time.Since(start)
			started(true)
			time.Sleep(hold)
			rel(nil, nil)
			return
		}
		hog := func() {
			started, _ := gate.Enter(ctx, "other")
			rel, _ := gov.Acquire(ctx, agent.PrioWorker)
			started(true)
			time.Sleep(100 * time.Millisecond)
			rel(nil, nil)
		}
		var wg sync.WaitGroup
		for i := 0; i < 5; i++ { // five worker requests ahead in the queue
			wg.Add(1)
			go func() { defer wg.Done(); hog() }()
			time.Sleep(2 * time.Millisecond)
		}
		wg.Add(1)
		go func() { defer wg.Done(); request(agent.PrioWorker, 10*time.Millisecond) }() // the primer
		time.Sleep(20 * time.Millisecond)
		var mgr time.Duration
		wg.Add(1)
		go func() { defer wg.Done(); mgr = request(agent.PrioInteractive, time.Millisecond) }()
		wg.Wait()
		return mgr
	}
	warm, cold := scenario(false), scenario(true)
	t.Logf("manager time to admission: warm prefix %v, cold prefix %v", warm.Round(time.Millisecond), cold.Round(time.Millisecond))
	if cold > warm+150*time.Millisecond {
		t.Fatalf("manager (priority 0) waited %v on a cold prefix versus %v on a warm one: it sat behind a worker-priority primer that was itself queued behind five workers", cold.Round(time.Millisecond), warm.Round(time.Millisecond))
	}
}

// A verifier that ignores its context cannot wedge Shutdown: the harness stops
// waiting for it at its own deadline.
func TestShutdownIsNotHeldByAVerifierThatIgnoresItsContext(t *testing.T) {
	release := make(chan struct{})
	cfg := Config{MaxWriters: 4, ShutdownGrace: 2 * time.Second, VerifyCmd: "go test ./...", Verify: func(ctx context.Context, dir, cmd string) (string, int, error) {
		<-release // ignores ctx
		return "ok", 0, nil
	}}
	r := newRVRig(t, cfg, func(ctx context.Context, c *rvCall) rvReply {
		if c.Role == "backend" && c.Assistants == 0 {
			return rvReply{Tools: []rvToolCall{{"task", map[string]any{"action": "done", "id": "T1", "text": "x"}}}}
		}
		return rvReply{Text: "summary"}
	})
	t.Cleanup(func() { close(release) })
	r.sw.StartManager()
	if _, err := r.sw.Spawn(SpawnReq{Role: "backend", Title: "work", By: "mgr"}); err != nil {
		t.Fatal(err)
	}
	rvWait(t, "worker to be inside the verifier", func() bool { return r.running("be-1") })
	time.Sleep(50 * time.Millisecond)
	done := make(chan struct{})
	go func() { r.sw.Shutdown(); close(done) }()
	select {
	case <-done:
	case <-time.After(1500 * time.Millisecond):
		t.Fatal("Shutdown is still blocked 1.5s after cancellation")
	}
}

// The advertised safety net: with every lease stolen (TTL of one nanosecond, so
// the guard never excludes anyone) FileState + the fs tools' per-path lock must
// still prevent lost updates. Expected to PASS.
func TestConcSound_StolenLeasesStillCannotLoseAnEdit(t *testing.T) {
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "shared.txt")
	if err := os.WriteFile(path, []byte("header\n<<END>>\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	reg := tools.NewRegistry()
	fs.Register(reg)
	read, _ := reg.Get("read")
	edit, _ := reg.Get("edit")
	files := tools.NewFileState()
	guard := NewLeases(time.Nanosecond, NewBoard(nil))
	blobs := events.NewMemBlobs()
	const agents, per = 6, 8
	var wg sync.WaitGroup
	var ok atomic.Int32
	var mu sync.Mutex
	var wrote []string
	for a := 0; a < agents; a++ {
		wg.Add(1)
		go func(a int) {
			defer wg.Done()
			env := (&tools.Env{Agent: fmt.Sprintf("a%d", a), Cwd: dir, Root: dir, Files: files, Guard: guard, Blobs: blobs}).Defaults()
			for n := 0; n < per; {
				in, _ := json.Marshal(map[string]any{"path": path})
				if res, _ := read.Run(context.Background(), &tools.Call{Input: in, Env: env}); res.IsError {
					t.Errorf("read: %s", res.Text)
					return
				}
				line := fmt.Sprintf("line from a%d #%d", a, n)
				in, _ = json.Marshal(map[string]any{"path": path, "old_string": "<<END>>", "new_string": line + "\n<<END>>"})
				res, _ := edit.Run(context.Background(), &tools.Call{Input: in, Env: env})
				if res.IsError {
					continue // stale: re-read and retry
				}
				ok.Add(1)
				mu.Lock()
				wrote = append(wrote, line)
				mu.Unlock()
				n++
			}
		}(a)
	}
	wg.Wait()
	final, _ := os.ReadFile(path)
	for _, l := range wrote {
		if !strings.Contains(string(final), l+"\n") {
			t.Fatalf("lost update: %q was reported written but is not in the file", l)
		}
	}
	if int(ok.Load()) != agents*per {
		t.Fatalf("expected %d successful edits, got %d", agents*per, ok.Load())
	}
}

// Whoever publishes last reads the newest state, so two racing updates cannot leave
// the board disagreeing with the member.
func TestSetStateNeverLeavesTheBoardStale(t *testing.T) {
	r := newRVRig(t, Config{MaxWriters: 4}, func(ctx context.Context, c *rvCall) rvReply { return rvReply{Text: "ok"} })
	r.sw.StartManager()
	id, err := r.sw.Spawn(SpawnReq{Role: "backend", Title: "work", By: "mgr"})
	if err != nil {
		t.Fatal(err)
	}
	rvWait(t, "worker idle", func() bool { return r.idle(id) })
	m := r.sw.get(id)
	var flag, done atomic.Int32
	quit := make(chan struct{})
	go func() { // second caller, released together with the first
		for {
			for flag.Load() == 0 {
				select {
				case <-quit:
					return
				default:
					runtime.Gosched()
				}
			}
			flag.Store(0)
			m.setState(r.sw, "idle", "")
			done.Add(1)
		}
	}()
	defer close(quit)
	deadline := time.Now().Add(3 * time.Second)
	for i := int32(1); time.Now().Before(deadline); i++ {
		flag.Store(1)
		m.setState(r.sw, "running", "step")
		for done.Load() != i {
			runtime.Gosched()
		}
		m.mu.Lock()
		want := m.state
		m.mu.Unlock()
		if got, _ := r.sw.Board.Snapshot().Agent(id); got.State != want {
			t.Fatalf("after %d racing update pairs the member says %q but the board (every agent's view) says %q", i, want, got.State)
		}
	}
}

// Sound: the wait tool wakes on a board change and on mail well inside its poll.
func TestConcSound_WaitWakesPromptlyOnBoardChangeAndOnMail(t *testing.T) {
	for _, name := range []string{"board", "mail"} {
		t.Run(name, func(t *testing.T) {
			r := newRVRig(t, Config{MaxWriters: 4}, func(ctx context.Context, c *rvCall) rvReply { return rvReply{Text: "ok"} })
			r.sw.StartManager()
			r.sw.Board.CreateTask("mgr", TaskSpec{Title: "t"})
			r.sw.Board.Assign("mgr", "be-1", "T1")
			res := make(chan string, 1)
			go func() {
				res <- r.callTool(context.Background(), "wait", "mgr", "manager", map[string]any{"timeout_sec": 30}).Text
			}()
			time.Sleep(100 * time.Millisecond)
			start := time.Now()
			if name == "board" {
				r.sw.Board.Finish("be-1", "T1", StatusReview, "done")
			} else {
				r.sw.Router.Send("be-1", "manager", "blocker", "need a decision")
			}
			select {
			case <-res:
				if d := time.Since(start); d > 700*time.Millisecond {
					t.Fatalf("wait took %v to notice a %s event", d, name)
				}
			case <-time.After(5 * time.Second):
				t.Fatalf("wait did not wake on a %s event", name)
			}
		})
	}
}

// nullBlobs keeps nothing, so the heap growth measured below is the archive's
// in-memory index alone.
type rvNullBlobs struct{}

func (rvNullBlobs) Put(d []byte) (core.Hash, error) { return core.HashBytes(d), nil }
func (rvNullBlobs) Get(core.Hash) ([]byte, error)   { return nil, events.ErrBlobNotFound }
func (rvNullBlobs) Has(core.Hash) bool              { return false }

// Archive.Put builds the search preview with `pv = pv[:previewCap]`, which keeps
// the ENTIRE lower-cased turn text alive (a substring shares its backing array), so
// the "capped 4KB preview" costs as much memory as the turn itself, forever: there
// is no eviction and no per-agent release, so Retire cannot give any of it back.
func TestConc_ArchiveIndexRetainsWholeTurnsForeverAndSurvivesRetirement(t *testing.T) {
	concGate(t)
	a := kv.NewArchive(rvNullBlobs{})
	body := strings.Repeat("build output line with details and more words\n", 1000) // ~46KB tool result; previews cap at 4KB
	var before, after runtime.MemStats
	runtime.GC()
	runtime.ReadMemStats(&before)
	const agents, turns = 4, 250
	for ag := 0; ag < agents; ag++ {
		for i := 1; i <= turns; i++ {
			_ = a.Put(fmt.Sprintf("w-%d", ag), core.Turn{ID: core.TurnID(i), Role: core.RoleUser,
				Blocks: []core.Block{core.Text(fmt.Sprintf("turn %d %s", i, body))}})
		}
	}
	runtime.GC()
	runtime.ReadMemStats(&after)
	held := int64(after.HeapAlloc) - int64(before.HeapAlloc)
	perTurn := held / (agents * turns)
	t.Logf("%d turns of ~%dKB archived (preview cap 4KB); index retains %d MB = %d bytes/turn; agents 'retired', Len(w-0)=%d", agents*turns, len(body)>>10, held>>20, perTurn, a.Len("w-0"))
	if perTurn > 8*1024 {
		t.Fatalf("index retains ~%dKB per archived turn against a 4KB preview cap (pv[:cap] pins the whole string), with no eviction: 50 workers x 2000 turns of 20KB = ~%d MB", perTurn>>10, (50*2000*20)>>10)
	}
}

// Evidence recovers a command's exit status by matching "[exit code N]" at the very
// END of the result text. Env.Finish appends its "[full output saved as ...]" note
// after truncating any output over MaxOutputChars, so a failing `go test` with a
// long failure log is recorded as exit 0, and the task result the manager reads
// says the last test PASSED. (The exit code is available in Result.Meta.)
func TestEvidenceNeverCallsAFailingTestPassedWhenItsOutputWasTruncated(t *testing.T) {
	env := (&tools.Env{Agent: "be-1", Blobs: events.NewMemBlobs()}).Defaults()
	failLog := strings.Repeat("--- FAIL: TestSomething (0.00s)\n    expected 10 got 11\n", 2000) // ~100KB
	res := env.Finish(failLog+"[exit code 1]", false)
	res.Meta = map[string]any{"exit_code": 1}
	ev := NewEvidence()
	ev.Observe(core.ToolUse("1", "bash", json.RawMessage(`{"command":"go test ./..."}`)), res, time.Now())
	if strings.Contains(ev.Summary(), "passed") {
		t.Fatalf("the command exited 1 (Meta says so) but the evidence line the manager sees is %q; result text tail: %q", ev.Summary(), res.Text[len(res.Text)-90:])
	}
}

// rvBlockEmit stalls the first layer.commit event after arm(), i.e. the first agent
// that SetShared syncs, while that agent's lock is held.
type rvBlockEmit struct {
	events.Emitter
	mu           sync.Mutex
	hit, release chan struct{}
}

func (b *rvBlockEmit) arm() (hit, release chan struct{}) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.hit, b.release = make(chan struct{}), make(chan struct{})
	return b.hit, b.release
}

func (b *rvBlockEmit) Emit(agentID, typ string, data any, opts ...events.Opt) (uint64, error) {
	if typ == events.TypeLayerCommit {
		b.mu.Lock()
		hit, rel := b.hit, b.release
		b.hit, b.release = nil, nil
		b.mu.Unlock()
		if hit != nil {
			close(hit)
			<-rel
		}
	}
	return b.Emitter.Emit(agentID, typ, data, opts...)
}

// SetShared publishes s.shared under the lock but syncs the members OUTSIDE it, so
// two overlapping calls can finish in the opposite order from the one in which
// they published: agents end on an epoch the swarm no longer considers current
// (each divergent agent is also a separate cache prefix) and nothing reconciles
// them until the next epoch. Forced by stalling the first call inside its first
// agent's sync while a second, later call runs to the point where it must wait for
// that agent; whether the overwrite reaches an agent depends on map iteration
// order, so up to 8 attempts are made (each fails with probability ~95%). A worker
// built across an epoch can miss it the same way (buildAgent reads s.shared once
// and registers later); that window was not reproduced.
func TestConcurrentSetSharedLeavesAgentsOnTheSameEpoch(t *testing.T) {
	be := &rvBlockEmit{Emitter: events.NewMemLog()}
	r := newRVRigWith(t, Config{MaxAgents: 100, MaxWriters: 100}, func(ctx context.Context, c *rvCall) rvReply { return rvReply{Text: "ok"} },
		func(d *Deps) { d.Events = be })
	r.sw.StartManager()
	for i := 0; i < 24; i++ {
		if _, err := r.sw.Spawn(SpawnReq{Role: "reviewer", Title: fmt.Sprintf("j%d", i), By: "mgr"}); err != nil {
			t.Fatal(err)
		}
	}
	layer := func(v string) *kv.Layer {
		return kv.NewLayer("shared", kv.KindShared, 1, []kv.Segment{{Key: "project", Text: "project knowledge, " + v, Vol: kv.VolEpoch}})
	}
	for attempt := 0; attempt < 8; attempt++ {
		hit, release := be.arm()
		first, second := make(chan struct{}), make(chan struct{})
		go func() { r.sw.SetShared(layer(fmt.Sprintf("epoch A%d", attempt)), "A"); close(first) }()
		<-hit // call A is inside its first sync, holding that agent's lock
		go func() { r.sw.SetShared(layer(fmt.Sprintf("epoch B%d", attempt)), "B"); close(second) }()
		time.Sleep(100 * time.Millisecond) // call B published B and synced every agent it could reach
		close(release)
		<-first
		<-second
		cur := r.sw.currentShared()
		stale, total := 0, 0
		for _, id := range r.sw.roster() {
			if m := r.sw.get(id); m != nil {
				total++
				if m.a.Stack().Shared.Hash() != cur.Hash() {
					stale++
				}
			}
		}
		if stale > 0 {
			t.Fatalf("attempt %d: %d of %d agents carry an older shared layer than the swarm's current one (A's late sync overwrote B on the agents B had already reached)", attempt+1, stale, total)
		}
	}
}

// Cancelling the swarm while a worker is inside the verifier settles the task instead
// of leaving it "doing" under an idle owner, whichever way the verifier returns (an
// interrupted swarm is never a verdict): the task returns to todo, nothing failed.
func TestVerifierCancelledMidVerificationSettlesTheTask(t *testing.T) {
	for _, tc := range []struct {
		name string
		code int
	}{
		{"killed", 137},
		{"finished first", 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var r *rvRig
			inside := make(chan struct{}, 1)
			cfg := Config{MaxWriters: 4, VerifyCmd: "go test ./...", Verify: func(ctx context.Context, dir, cmd string) (string, int, error) {
				inside <- struct{}{}
				r.sw.cancel() // Shutdown / user interrupt while the verifier runs
				<-ctx.Done()
				return "output", tc.code, nil
			}}
			r = newRVRig(t, cfg, func(ctx context.Context, c *rvCall) rvReply {
				if c.Role == "backend" && c.Assistants == 0 {
					return rvReply{Tools: []rvToolCall{{"task", map[string]any{"action": "done", "id": "T1", "text": "x"}}}}
				}
				return rvReply{Text: "summary"}
			})
			r.sw.StartManager()
			id, err := r.sw.Spawn(SpawnReq{Role: "backend", Title: "work", By: "mgr"})
			if err != nil {
				t.Fatal(err)
			}
			<-inside
			rvWait(t, "worker idle", func() bool { return r.idle(id) })
			time.Sleep(50 * time.Millisecond)
			tk, _ := r.sw.Board.Snapshot().Task("T1")
			if tk.Status != StatusTodo || tk.Owner != "" || tk.Attempts != 0 {
				t.Fatalf("T1 ended %s/%s attempts=%d (%s); want todo, unowned", tk.Status, tk.Owner, tk.Attempts, tk.Result)
			}
			if a, _ := r.sw.Board.Snapshot().Agent(id); a.State != "idle" {
				t.Fatalf("worker state %q", a.State)
			}
		})
	}
}
