package main

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/anemos-labs/sleipnir/internal/events"
	"github.com/anemos-labs/sleipnir/internal/session"
	"github.com/anemos-labs/sleipnir/internal/swarm"
	"github.com/anemos-labs/sleipnir/internal/trust"
	"github.com/anemos-labs/sleipnir/internal/web"
	"github.com/anemos-labs/sleipnir/internal/web/seam"
	"github.com/anemos-labs/sleipnir/internal/web/wire"
)

// teamDefaults are the server's defaults for a manager and two workers in project.
func teamDefaults(project string) webDefaults {
	return webDefaults{Cwd: project, Workers: 2, WorkersGiven: true, Mode: "accept-edits"}
}

// endReason is the reason the session.end event of a session directory records ("" when there is none).
func endReason(t *testing.T, dir string) string {
	t.Helper()
	reason := ""
	_ = events.Scan(eventsPath(dir), func(e events.Event) error {
		if e.Type == events.TypeSessionEnd {
			var d struct {
				Reason string `json:"reason"`
			}
			_ = json.Unmarshal(e.Data, &d)
			reason = d.Reason
		}
		return nil
	})
	return reason
}

// Model switches run beside the clock and the snapshots that read the tab's meta: nothing reads what a switch writes (run with -race).
func TestModelSwitchesDoNotRaceTheMeta(t *testing.T) {
	project := webWorld(t)
	_, base := newWebModel(t, sayScript("ok"))
	r := newWebRig(t, soloDefaults(project), base)
	tab := r.ready("")
	path := "/api/sessions/" + tab.ID
	stop := make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		for {
			select {
			case <-stop:
				return
			default:
			}
			if code, b := r.do("GET", path+"/snapshot", nil); code != 200 {
				t.Errorf("snapshot = %d %s", code, b)
				return
			}
			r.h.tab(tab.ID).publishMeta()
		}
	}()
	for range 15 {
		r.json("POST", path+"/model", wire.ModelRequest{Ref: "mock/mock-1"}, 200, nil)
	}
	close(stop)
	wg.Wait()
	var snap tabSnapshot
	r.json("GET", path+"/snapshot", nil, 200, &snap)
	if snap.Meta.Model == nil || *snap.Meta.Model != "mock/mock-1" {
		t.Errorf("the meta's model after the switches: %v", snap.Meta.Model)
	}
}

// A call that holds the session for a long time (a compaction, a model switch) does not hold the snapshots, the per-tab snapshot or
// the clock's meta: they read the tab's view of the session.
func TestALongSessionCallDoesNotHoldTheSnapshots(t *testing.T) {
	project := webWorld(t)
	_, base := newWebModel(t, sayScript("ok"))
	r := newWebRig(t, soloDefaults(project), base)
	tab := r.ready("")
	wt := r.h.tab(tab.ID)
	wt.sessMu.Lock() // what a compaction or a model switch holds for as long as its model call lasts
	defer wt.sessMu.Unlock()
	done := make(chan struct{})
	go func() {
		defer close(done)
		r.json("GET", "/api/snapshot", nil, 200, nil)
		r.json("GET", "/api/sessions/"+tab.ID+"/snapshot", nil, 200, nil)
		wt.publishMeta()
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("the snapshots wait for the session's call to end")
	}
}

// A restart that would resume another tab's session, a session that is not recorded or one that another process uses is refused
// before anything is closed: the tab keeps its session.
func TestARestartNeverResumesASessionItCannotHold(t *testing.T) {
	project := webWorld(t)
	_, base := newWebModel(t, sayScript("ok"))
	r := newWebRig(t, soloDefaults(project), base)
	a := r.ready("")
	zero := 0
	var created struct{ Tab wire.TabSummary }
	r.json("POST", "/api/sessions", wire.NewSessionRequest{Cwd: project, Swarm: &zero}, 201, &created)
	b := r.ready(created.Tab.ID)
	r.json("POST", "/api/sessions/"+b.ID+"/messages", wire.MessageRequest{Text: "one"}, 200, nil)
	r.waitEv(b.ID, "final", nil)

	path := "/api/sessions/" + a.ID + "/restart"
	for _, tc := range []struct {
		flags      []string
		status     int
		code, line string
	}{
		{[]string{"--resume", b.SID}, 409, "hosted", "/resume " + b.SID},
		{[]string{"--resume", "20200101-000000-aaaaaa"}, 404, "no_session", "/resume 20200101-000000-aaaaaa"},
		{[]string{"--continue"}, 409, "hosted", ""}, // the project's newest session is b's
	} {
		code, body := r.do("POST", path, wire.RestartRequest{Kind: "restart", Flags: tc.flags})
		var e struct{ Code string }
		if code != tc.status || json.Unmarshal(body, &e) != nil || e.Code != tc.code {
			t.Errorf("restart %v = %d %s, want %d %s", tc.flags, code, body, tc.status, tc.code)
		}
		if tc.line != "" {
			r.do("POST", "/api/sessions/"+a.ID+"/command", wire.CommandRequest{Line: tc.line})
		}
		if sum := r.h.tab(a.ID).Summary(); sum.SID != a.SID || sum.Gen != 1 {
			t.Fatalf("after a refused restart %v the tab is at gen %d with session %q, want gen 1 with %q", tc.flags, sum.Gen, sum.SID, a.SID)
		}
	}
	if s := r.h.tab(b.ID).session(); s == nil || s.ID != b.SID {
		t.Error("the other tab lost its session")
	}
}

// Ctrl-C does not end the process while a tab's close (an isolated team's work being applied) is still running: the host's close
// waits for it, and the session is closed whole.
func TestTheHostWaitsForATabThatIsClosing(t *testing.T) {
	project := webWorld(t)
	_, base := newWebModel(t, sayScript("ok"))
	r := newWebRig(t, soloDefaults(project), base)
	r.ready("")
	zero := 0
	var created struct{ Tab wire.TabSummary }
	r.json("POST", "/api/sessions", wire.NewSessionRequest{Cwd: project, Swarm: &zero}, 201, &created)
	b := r.ready(created.Tab.ID)
	r.json("POST", "/api/sessions/"+b.ID+"/messages", wire.MessageRequest{Text: "one"}, 200, nil) // a session that ran records its end
	r.waitEv(b.ID, "final", nil)
	dir := r.h.tab(b.ID).session().Dir

	release := make(chan struct{})
	entered := make(chan struct{})
	prev := webCloseSession
	webCloseSession = func(s *session.Session, reason string) *swarm.IntegrationReport {
		if s != nil && s.Dir == dir {
			close(entered)
			<-release // the minutes an isolated team's integration can take
		}
		return prev(s, reason)
	}
	t.Cleanup(func() { webCloseSession = prev })

	go func() { _, _ = r.do("DELETE", "/api/sessions/"+b.ID, nil) }()
	select {
	case <-entered:
	case <-time.After(webGuard):
		t.Fatal("the close did not start")
	}
	closed := make(chan struct{})
	go func() {
		r.stop() // Ctrl-C: the server stops, then the host closes
		close(closed)
	}()
	select {
	case <-closed:
		t.Fatal("the host closed while a tab was still closing")
	case <-time.After(500 * time.Millisecond):
	}
	close(release)
	select {
	case <-closed:
	case <-time.After(webGuard):
		t.Fatal("the host did not close")
	}
	if got := endReason(t, dir); got == "" {
		t.Error("the closing session was not closed")
	}
}

// A run that cannot bind its address (the port is taken) starts no session and leaves nothing in the state directory, even with a
// model configured and --continue.
func TestWebThatCannotListenStartsNoSession(t *testing.T) {
	m := startModel(t)
	w := newWorld(t, m.url())
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	res := w.run("", "web", "--addr", ln.Addr().String())
	assertRun(t, res, 1, []string{"!http://"}, []string{"already in use"})
	res = w.run("", "web", "--addr", ln.Addr().String(), "--continue")
	if res.code == 0 {
		t.Errorf("a run that cannot listen exited 0")
	}
	if ents, _ := os.ReadDir(filepath.Join(w.state, "sessions")); len(ents) != 0 {
		var names []string
		for _, e := range ents {
			names = append(names, e.Name())
		}
		t.Errorf("a run that could not listen left sessions behind: %v", names)
	}
}

// /roles with several roles restarts the team once, with every role (the manager's own included), as the terminal does.
func TestRolesLineRestartsOnceWithEveryRole(t *testing.T) {
	project := webWorld(t)
	_, base := newWebModel(t, sayScript("ok"))
	r := newWebRig(t, teamDefaults(project), base)
	tab := r.ready("")
	if r.h.tab(tab.ID).session().Swarm == nil {
		t.Fatal("the tab is not a team")
	}
	var res wire.CommandResult
	r.json("POST", "/api/sessions/"+tab.ID+"/command", wire.CommandRequest{Line: "/roles tester=mock/mock-1 manager=mock/mock-1 backend=mock/mock-1"}, 200, &res)
	if res.Output != "" {
		t.Fatalf("/roles said %q", res.Output)
	}
	r.waitForSID(tab.ID, 2)
	wt := r.h.tab(tab.ID)
	wt.mu.Lock()
	got := append([]string(nil), wt.args...)
	gen := wt.gen
	wt.mu.Unlock()
	for _, want := range []string{"manager=mock/mock-1", "tester=mock/mock-1", "backend=mock/mock-1"} {
		if i := slices.Index(got, want); i < 1 || got[i-1] != "--role-model" {
			t.Errorf("the restart's arguments %q lack --role-model %s", got, want)
		}
	}
	if gen != 2 {
		t.Errorf("one /roles line made %d generations", gen)
	}
}

// "budget off" is refused for a team whose configuration sets swarm.budget_usd: the team would start with that budget again.
func TestTeamBudgetOffIsRefusedWhenTheConfigurationSetsOne(t *testing.T) {
	project := webWorld(t)
	_, base := newWebModel(t, sayScript("ok"))
	cfg := *base.Config
	cfg.Swarm.BudgetUSD = 1.5
	base.Config = &cfg
	r := newWebRig(t, teamDefaults(project), base)
	tab := r.ready("")
	path := "/api/sessions/" + tab.ID
	code, body := r.do("POST", path+"/budget", wire.BudgetRequest{Off: true})
	var e struct{ Code string }
	if code != 409 || json.Unmarshal(body, &e) != nil || e.Code != "budget_configured" {
		t.Fatalf("budget off for a team with swarm.budget_usd = %d %s", code, body)
	}
	r.json("POST", path+"/budget", wire.BudgetRequest{USD: 3}, 200, nil)
	r.json("POST", path+"/restart", wire.RestartRequest{Kind: "restart"}, 202, nil)
	r.waitForSID(tab.ID, 2)
	var snap tabSnapshot
	r.json("GET", path+"/snapshot", nil, 200, &snap)
	if snap.Meta.Budget == nil || *snap.Meta.Budget != 3 {
		t.Errorf("the team's budget after the restart: %v, want 3", snap.Meta.Budget)
	}
	if got := configuredTeamBudget(r.h.tab(tab.ID).session()); got != 1.5 {
		t.Errorf("configuredTeamBudget = %v", got)
	}
}

// Requests with one client id that overlap make one session: the repeats wait for the first and return its answer.
func TestOverlappingRepeatsOfOneClientIDMakeOneSession(t *testing.T) {
	project := webWorld(t)
	_, base := newWebModel(t, sayScript("ok"))
	r := newWebRig(t, soloDefaults(project), base)
	r.ready("")
	zero := 0
	for round := range 3 {
		before := len(r.h.tabList())
		var wg sync.WaitGroup
		ids := make([]string, 3)
		for i := range ids {
			wg.Add(1)
			go func() {
				defer wg.Done()
				var created struct{ Tab wire.TabSummary }
				r.json("POST", "/api/sessions", newSessionRequest{NewSessionRequest: wire.NewSessionRequest{Cwd: project, Swarm: &zero}, ClientID: fmt.Sprintf("c-%d", round)}, 201, &created)
				ids[i] = created.Tab.ID
			}()
		}
		wg.Wait()
		if n := len(r.h.tabList()) - before; n != 1 || ids[0] != ids[1] || ids[1] != ids[2] {
			t.Fatalf("round %d: three overlapping requests with one client id made %d tabs (%v)", round, n, ids)
		}
	}
}

// Meta frames reach the pages in the order of what they describe: a page that applies them in order ends with the tab's meta, and
// their versions rise one by one.
func TestMetaFramesArriveInOrder(t *testing.T) {
	hub := web.NewHub(web.HubConfig{})
	defer hub.Close()
	st, err := hub.Subscribe(seam.Topic, web.SubscribeOpts{})
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	h := &webHostImpl{hub: hub, logf: t.Logf, d: webDefaults{Cwd: t.TempDir()}}
	tab := &webTab{h: h, id: "t1", closedDone: make(chan struct{})}
	page := ""
	var lastV uint64
	set := func(v string) {
		tab.mu.Lock()
		tab.staged.Verify = &v
		tab.mu.Unlock()
	}
	for i := range 2000 {
		var wg sync.WaitGroup
		set(fmt.Sprintf("a%d", i))
		wg.Add(1)
		go func() { defer wg.Done(); tab.publishMeta() }()
		set(fmt.Sprintf("b%d", i))
		wg.Add(1)
		go func() { defer wg.Done(); tab.publishMeta() }()
		wg.Wait()
		last := hub.LastID(seam.Topic)
		for got := uint64(0); got < last; {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			ev, err := st.Next(ctx)
			cancel()
			if err != nil {
				t.Fatal(err)
			}
			got = ev.ID
			var f struct {
				Patch wire.MetaPatch `json:"patch"`
				V     uint64         `json:"v"`
			}
			if json.Unmarshal(ev.Data, &f) != nil || f.V != lastV+1 {
				t.Fatalf("trial %d: a meta frame of version %d after %d: %s", i, f.V, lastV, ev.Data)
			}
			lastV = f.V
			if f.Patch.Verify != nil {
				page = *f.Patch.Verify
			}
		}
		if want := fmt.Sprintf("b%d", i); page != want {
			t.Fatalf("trial %d: the page shows %q, the tab's meta is %q", i, page, want)
		}
	}
}

// A snapshot says which versions of the meta and roster frames it holds: a frame sent after it has a higher version.
func TestSnapshotsCarryTheVersionsOfTheirMetaAndRoster(t *testing.T) {
	project := webWorld(t)
	_, base := newWebModel(t, sayScript("ok"))
	r := newWebRig(t, soloDefaults(project), base)
	tab := r.ready("")
	path := "/api/sessions/" + tab.ID
	var snap tabSnapshot
	r.json("GET", path+"/snapshot", nil, 200, &snap)
	if snap.MetaV == 0 {
		t.Fatalf("the snapshot has no meta version: %+v", snap.MetaV)
	}
	r.json("POST", path+"/effort", wire.EffortRequest{Level: "high"}, 200, nil)
	f := r.waitFor("a meta frame with the effort", func(f frame) bool {
		return f.event == "meta" && strings.Contains(string(f.data), `"effort":"high"`)
	})
	var m struct{ V uint64 }
	if json.Unmarshal(f.data, &m) != nil || m.V <= snap.MetaV {
		t.Errorf("the frame after the snapshot has version %d, the snapshot %d", m.V, snap.MetaV)
	}
	var again tabSnapshot
	r.json("GET", path+"/snapshot", nil, 200, &again)
	if again.MetaV < m.V || again.Meta.Effort == nil || *again.Meta.Effort != "high" {
		t.Errorf("a later snapshot: version %d (the frame's %d), effort %v", again.MetaV, m.V, again.Meta.Effort)
	}
	var all struct{ Tabs []tabSnapshot }
	r.json("GET", "/api/snapshot", nil, 200, &all)
	if len(all.Tabs) != 1 || all.Tabs[0].MetaV < m.V {
		t.Errorf("/api/snapshot versions: %+v", all.Tabs)
	}
}

// /trust shows the trust of the session's project, not of the directory the server process runs in.
func TestTrustCommandShowsTheSessionsProject(t *testing.T) {
	project := webWorld(t)
	other := untrustedProject(t, project, "shop")
	_, base := newWebModel(t, sayScript("ok"))
	d := soloDefaults(project)
	d.Projects = []string{other}
	r := newWebRig(t, d, base)
	r.ready("")
	zero := 0
	var created struct{ Tab wire.TabSummary }
	r.json("POST", "/api/sessions", wire.NewSessionRequest{Cwd: other, Swarm: &zero}, 201, &created)
	tab := r.ready(created.Tab.ID)
	var res wire.CommandResult
	r.json("POST", "/api/sessions/"+tab.ID+"/command", wire.CommandRequest{Line: "/trust"}, 200, &res)
	if first, _, _ := strings.Cut(res.Output, "\n"); first != trust.Show(other) || !strings.Contains(res.Output, "AGENTS.md") {
		t.Errorf("/trust in a session of %s said:\n%s", other, res.Output)
	}
}

// Closing the sessions at Ctrl-C records them as interrupted, as the terminal does; closing a tab records an exit.
func TestCtrlCRecordsTheSessionsAsInterrupted(t *testing.T) {
	project := webWorld(t)
	_, base := newWebModel(t, sayScript("ok"))
	r := newWebRig(t, soloDefaults(project), base)
	a := r.ready("")
	zero := 0
	var created struct{ Tab wire.TabSummary }
	r.json("POST", "/api/sessions", wire.NewSessionRequest{Cwd: project, Swarm: &zero}, 201, &created)
	b := r.ready(created.Tab.ID)
	for _, id := range []string{a.ID, b.ID} { // a session that ran records its end
		r.json("POST", "/api/sessions/"+id+"/messages", wire.MessageRequest{Text: "one"}, 200, nil)
		r.waitEv(id, "final", nil)
	}
	aDir, bDir := r.h.tab(a.ID).session().Dir, r.h.tab(b.ID).session().Dir
	r.json("DELETE", "/api/sessions/"+b.ID, nil, 200, nil)
	if got := endReason(t, bDir); got != session.EndExit {
		t.Errorf("a closed tab's session ended %q, want %q", got, session.EndExit)
	}
	r.stop()
	if got := endReason(t, aDir); got != session.EndInterrupted {
		t.Errorf("a session closed at Ctrl-C ended %q, want %q", got, session.EndInterrupted)
	}
}

// When the server stops, every page's stream delivers bye, after what it held, before it ends.
func TestTheStreamSaysByeBeforeItEnds(t *testing.T) {
	project := webWorld(t)
	_, base := newWebModel(t, sayScript("ok"))
	r := newWebRig(t, soloDefaults(project), base)
	r.ready("")
	r.stop()
	r.waitFor("bye", func(f frame) bool { return f.event == "bye" })
}

// The real command: a page's stream gets bye when the server is interrupted.
func TestWebSaysByeOnInterrupt(t *testing.T) {
	w := newWorld(t, "")
	run := w.startWeb(t)
	req, _ := http.NewRequest("GET", run.base+"/api/stream?after=0", nil)
	req.Header.Set("Authorization", "Bearer "+run.token)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	events := make(chan string, 64)
	go func() {
		sc := bufio.NewScanner(resp.Body)
		for sc.Scan() {
			if ev, ok := strings.CutPrefix(sc.Text(), "event: "); ok {
				events <- ev
			}
		}
		close(events)
	}()
	time.Sleep(200 * time.Millisecond) // the stream is open
	run.stop()
	for ev := range events {
		if ev == "bye" {
			return
		}
	}
	t.Error("the stream ended without bye")
}

// The goal route says when the action waits to run: a goal set on an idle tab runs as its next item.
func TestTheGoalRouteSaysWhenTheActionWaits(t *testing.T) {
	project := webWorld(t)
	_, base := newWebModel(t, sayScript("ok"))
	r := newWebRig(t, soloDefaults(project), base)
	tab := r.ready("")
	var res struct {
		OK     bool
		State  string
		Queued bool
	}
	r.json("POST", "/api/sessions/"+tab.ID+"/goal", wire.GoalRequest{Action: "set", Text: "make it so"}, 200, &res)
	if !res.OK || (!res.Queued && res.State == "none") {
		t.Errorf("goal set answered %+v: neither the new state nor that it waits", res)
	}
}

// The context of an item ends with the item.
func TestAnItemsContextEndsWithIt(t *testing.T) {
	project := webWorld(t)
	_, base := newWebModel(t, sayScript("ok"))
	r := newWebRig(t, soloDefaults(project), base)
	tab := r.ready("")
	wt := r.h.tab(tab.ID)
	got := make(chan context.Context, 1)
	if _, _, _, err := wt.enqueue(queued{text: "probe", op: func(ctx context.Context, _ *session.Session) { got <- ctx }}); err != nil {
		t.Fatal(err)
	}
	ctx := <-got
	deadline := time.Now().Add(webGuard)
	for ctx.Err() == nil && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if ctx.Err() == nil {
		t.Error("the context of an item that ended is still live")
	}
}

// A new session that resumes a recorded session another process uses is refused with 409 locked, as the resume route refuses it.
func TestANewSessionDoesNotResumeALockedOne(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("the lock table is Linux's")
	}
	project := webWorld(t)
	_, base := newWebModel(t, sayScript("ok"))
	r := newWebRig(t, soloDefaults(project), base)
	r.ready("")
	zero := 0
	var created struct{ Tab wire.TabSummary }
	r.json("POST", "/api/sessions", wire.NewSessionRequest{Cwd: project, Swarm: &zero}, 201, &created)
	b := r.ready(created.Tab.ID)
	r.json("POST", "/api/sessions/"+b.ID+"/messages", wire.MessageRequest{Text: "one"}, 200, nil)
	r.waitEv(b.ID, "final", nil)
	dir := r.h.tab(b.ID).session().Dir
	r.json("DELETE", "/api/sessions/"+b.ID, nil, 200, nil)
	f, err := os.OpenFile(filepath.Join(dir, ".lock"), os.O_RDWR|os.O_CREATE, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if err := holdLock(f); err != nil {
		t.Fatal(err)
	}
	before := len(r.h.tabList())
	code, body := r.do("POST", "/api/sessions", newSessionRequest{NewSessionRequest: wire.NewSessionRequest{Cwd: project, Swarm: &zero}, Resume: b.SID})
	var e struct{ Code string }
	if code != 409 || json.Unmarshal(body, &e) != nil || e.Code != "locked" || len(r.h.tabList()) != before {
		t.Errorf("resuming a locked session = %d %s", code, body)
	}
}

// /goal pause typed while a turn runs stops the turn at once and pauses the goal; it does not wait behind the turn to interrupt itself.
func TestGoalPauseTypedDuringATurnActsAtOnce(t *testing.T) {
	project := webWorld(t)
	m, base := newWebModel(t, sayScript("ok"))
	r := newWebRig(t, soloDefaults(project), base)
	tab := r.ready("")
	path := "/api/sessions/" + tab.ID
	m.hold("make it so")
	r.json("POST", path+"/goal", wire.GoalRequest{Action: "set", Text: "make it so"}, 200, nil)
	r.waitEv(tab.ID, "turn", map[string]any{"s": "start"})
	var res wire.CommandResult
	r.json("POST", path+"/command", wire.CommandRequest{Line: "/goal pause"}, 200, &res)
	if strings.HasPrefix(res.Output, "queued") {
		t.Fatalf("/goal pause waits behind the turn: %q", res.Output)
	}
	m.release("make it so")
	deadline := time.Now().Add(webGuard)
	for r.h.tab(tab.ID).goalState() != "paused" && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	if s := r.h.tab(tab.ID).goalState(); s != "paused" {
		t.Errorf("the goal is %q", s)
	}
	n := 0
	r.mu.Lock()
	for _, f := range r.frames {
		if e := f.ev(); e != nil && e["k"] == "interrupt" && e["_tab"] == tab.ID {
			n++
		}
	}
	r.mu.Unlock()
	if n != 1 {
		t.Errorf("%d interrupt events, want 1", n)
	}
}
