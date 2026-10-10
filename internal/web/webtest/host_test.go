package webtest

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/anemos-labs/sleipnir/internal/session"
	"github.com/anemos-labs/sleipnir/internal/web/seam"
	"github.com/anemos-labs/sleipnir/internal/web/wire"
)

var ctx = context.Background()

// sessionT is the harness session the seam hands to the routes.
type sessionT = session.Session

// kindsOf lists the event kinds a tab journaled after seq from.
func kindsOf(t testing.TB, tab *Tab, from uint64) []string {
	t.Helper()
	snap, err := tab.Snapshot(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, e := range snap.Events {
		var h wire.Base
		if err := json.Unmarshal(e, &h); err != nil {
			t.Fatal(err)
		}
		if h.Seq > from {
			out = append(out, h.K)
		}
	}
	return out
}

// code returns the code of a *wire.Error ("" for other errors and nil).
func code(err error) string {
	var we *wire.Error
	if errors.As(err, &we) {
		return we.Code
	}
	return ""
}

// status returns the status of a *wire.Error.
func status(err error) int {
	var we *wire.Error
	if errors.As(err, &we) {
		return we.Status
	}
	return 0
}

func TestTheShopHostIsASeamHost(t *testing.T) {
	var h seam.Host = NewShopHost()
	if tabs := h.Tabs(); len(tabs) != 1 || tabs[0].ID != TabID || tabs[0].SID != SID || tabs[0].Cwd != Cwd || h.Active() != TabID {
		t.Fatalf("tabs = %+v active %q", tabs, h.Active())
	}
	if _, ok := h.Tab("nope"); ok {
		t.Error("found a tab that is not there")
	}
	tab, ok := h.Tab(TabID)
	if !ok {
		t.Fatal("no shop tab")
	}
	snap, err := tab.Snapshot(ctx)
	if err != nil {
		t.Fatal(err)
	}
	sc := Shop()
	if snap.Seq != uint64(len(sc.History)) || len(snap.Events) != len(sc.History) || snap.Now != Now || snap.Gen != 1 || len(snap.Roster) != 5 || len(snap.Keyframe) != len(sc.Keyframe) {
		t.Errorf("snapshot: seq %d events %d now %v gen %d roster %d keyframe %d", snap.Seq, len(snap.Events), snap.Now, snap.Gen, len(snap.Roster), len(snap.Keyframe))
	}
	if len(snap.Questions) != 1 || snap.Questions[0].ID != ShopQuestion().ID {
		t.Errorf("questions = %+v", snap.Questions)
	}
	if qs := h.Questions(); len(qs) != 1 || qs[0].Tab != TabID || qs[0].Q.ID != ShopQuestion().ID || qs[0].T0 != 33 {
		t.Errorf("host questions = %+v", qs)
	}
	if m := snap.Meta; m.Mode == nil || *m.Mode != "default" || m.Swarm == nil || *m.Swarm != 4 || m.Queued == nil || m.Rules == nil || len(*m.Rules) != 2 || m.Running == nil || !*m.Running {
		t.Errorf("meta = %+v", m)
	}
	if ps := h.Projects(ctx); len(ps) != 3 || !ps[0].Default || ps[0].Dir != Cwd {
		t.Errorf("projects = %+v", ps)
	}
	// Seq numbers are one apart from 1, and the tab reports the last in its snapshot.
	for i, e := range snap.Events {
		var b wire.Base
		_ = json.Unmarshal(e, &b)
		if b.Seq != uint64(i+1) {
			t.Fatalf("event %d has seq %d", i, b.Seq)
		}
	}
	// The journal is the host's: no frame was published by loading it.
	if n := len(NewShopHost().Frames()); n != 0 {
		t.Errorf("%d frames published by loading the history", n)
	}
}

func TestSendRunsATurnOrQueuesBehindTheRunningOne(t *testing.T) {
	h := NewShopHost()
	tab := h.FakeTab(TabID)
	// The canned meta says a run is in progress: a message then queues, as it would behind a real turn.
	res, err := tab.Send(ctx, wire.MessageRequest{Text: "hello"})
	if err != nil || !res.Queued || res.Position != 1 || res.ID == "" {
		t.Fatalf("send behind the running turn = %+v %v", res, err)
	}
	meta := tab.metaFull()
	if meta.Queued == nil || len(*meta.Queued) != 1 || (*meta.Queued)[0].Text != "hello" {
		t.Errorf("queue = %+v", meta.Queued)
	}
	if got := h.FramesOf("meta"); len(got) == 0 {
		t.Error("the queue change was not published")
	}

	// An idle tab runs a short turn at once.
	h2 := NewHost()
	t2, err := h2.AddTab("scratch", Cwd)
	if err != nil {
		t.Fatal(err)
	}
	before := len(kindsOf(t, t2, 0))
	res, err = t2.Send(ctx, wire.MessageRequest{Text: "/goal x", Display: "[pasted 3 lines]"})
	if err != nil || res.Queued {
		t.Fatalf("send = %+v %v", res, err)
	}
	got := kindsOf(t, t2, 0)[before:]
	want := []string{"say", "turn", "state", "say", "state", "final", "turn"}
	if strings.Join(got, " ") != strings.Join(want, " ") {
		t.Errorf("a turn journals %v, want %v", got, want)
	}
	snap, _ := t2.Snapshot(ctx)
	var echo wire.Say
	_ = json.Unmarshal(snap.Events[before], &echo)
	if echo.Who != "you" || echo.Text != "[pasted 3 lines]" {
		t.Errorf("the transcript shows %+v: Display is what the transcript shows", echo)
	}
	if t2.Busy() {
		t.Error("the turn did not end")
	}
	// Errors.
	if _, err := t2.Send(ctx, wire.MessageRequest{Text: "  "}); code(err) != "empty" || status(err) != 400 {
		t.Errorf("empty message: %v", err)
	}
}

func TestAHeldTurnLetsTheQueueSteerAndInterruptBeExercised(t *testing.T) {
	h := NewHost()
	tab, _ := h.AddTab("scratch", Cwd)
	if err := tab.Steer(ctx, "now"); code(err) != "idle" || status(err) != 409 {
		t.Errorf("steer while idle: %v", err)
	}
	if err := tab.Interrupt(ctx, "turn"); code(err) != "idle" {
		t.Errorf("interrupt while idle: %v", err)
	}
	tab.HoldTurns(true)
	if res, err := tab.Send(ctx, wire.MessageRequest{Text: "first"}); err != nil || res.Queued || !tab.Busy() {
		t.Fatalf("first = %+v %v busy %v", res, err, tab.Busy())
	}
	if res, _ := tab.Send(ctx, wire.MessageRequest{Text: "second"}); !res.Queued || res.Position != 1 {
		t.Errorf("second = %+v", res)
	}
	if res, _ := tab.Send(ctx, wire.MessageRequest{Text: "third"}); !res.Queued || res.Position != 2 {
		t.Errorf("third = %+v", res)
	}
	if err := tab.Steer(ctx, ""); code(err) != "empty" {
		t.Errorf("empty steer: %v", err)
	}
	if err := tab.Steer(ctx, "use the pager"); err != nil {
		t.Fatal(err)
	}
	tab.ReleaseTurn() // the first ends; the second starts and is held
	if !tab.Busy() || len(*tab.metaFull().Queued) != 1 {
		t.Errorf("after release: busy %v queue %+v", tab.Busy(), tab.metaFull().Queued)
	}
	if err := tab.Interrupt(ctx, "turn"); err != nil {
		t.Fatal(err)
	}
	if tab.Busy() {
		t.Error("interrupt left the turn running")
	}
	kinds := strings.Join(kindsOf(t, tab, 0), " ")
	for _, want := range []string{"steer", "interrupt", "turn"} {
		if !strings.Contains(kinds, want) {
			t.Errorf("journal lacks %s: %s", want, kinds)
		}
	}
}

func TestSettingsAreAcknowledgedAndRefusedWithTheContractsCodes(t *testing.T) {
	h := NewHost()
	tab, _ := h.AddTab("scratch", Cwd)
	meta := func() wire.MetaPatch { return tab.metaFull() }
	lastSys := func() string {
		snap, _ := tab.Snapshot(ctx)
		var s wire.Say
		_ = json.Unmarshal(snap.Events[len(snap.Events)-1], &s)
		return s.Text
	}
	if err := tab.SetMode(ctx, wire.ModeRequest{Mode: "plan"}); err != nil || *meta().Mode != "plan" || lastSys() != "mode: plan (read-only)" {
		t.Errorf("mode plan: %v %q", err, lastSys())
	}
	if err := tab.SetMode(ctx, wire.ModeRequest{Mode: "yolo"}); err != nil || !strings.HasPrefix(lastSys(), "mode: yolo (dangerous") {
		t.Errorf("mode yolo: %v %q", err, lastSys())
	}
	if err := tab.SetMode(ctx, wire.ModeRequest{Mode: "chaos"}); code(err) != "bad_mode" || status(err) != 400 {
		t.Errorf("mode chaos: %v", err)
	}
	if err := tab.SetModel(ctx, wire.ModelRequest{Ref: "heimdall/x"}); err != nil || *meta().Model != "heimdall/x" || !strings.Contains(lastSys(), "the prompt cache starts over") {
		t.Errorf("model: %v %q", err, lastSys())
	}
	if err := tab.SetModel(ctx, wire.ModelRequest{Ref: "backend", Role: "scout"}); code(err) != "model" || status(err) != 422 {
		t.Errorf("bad model: %v", err)
	}
	if err := tab.SetModel(ctx, wire.ModelRequest{Ref: "heimdall/small", Role: "scout"}); err != nil || (*meta().RoleModels)["scout"] != "heimdall/small" {
		t.Errorf("role model: %v %+v", err, meta().RoleModels)
	}
	if res, err := tab.SetEffort(ctx, wire.EffortRequest{Level: "high"}); err != nil || res.Applied != "high" || *meta().Effort != "high" {
		t.Errorf("effort: %+v %v", res, err)
	}
	if _, err := tab.SetEffort(ctx, wire.EffortRequest{Level: "ludicrous"}); code(err) != "bad_level" {
		t.Errorf("bad effort: %v", err)
	}
	if err := tab.SetBudget(ctx, wire.BudgetRequest{USD: 5}); err != nil || *meta().Budget != 5 || lastSys() != "budget: $5.00 for the turns from now on" {
		t.Errorf("budget: %v %q", err, lastSys())
	}
	if err := tab.SetBudget(ctx, wire.BudgetRequest{Off: true}); err != nil || *meta().Budget != 0 || lastSys() != "budget: off" {
		t.Errorf("budget off: %v %q", err, lastSys())
	}
	if err := tab.SetBudget(ctx, wire.BudgetRequest{USD: -1}); code(err) != "bad_budget" {
		t.Errorf("bad budget: %v", err)
	}
	iso, bad := "worktree", "docker"
	if err := tab.StageLaunch(ctx, wire.LaunchPatch{Isolation: &iso}); err != nil || *meta().Isolation != "worktree" || !strings.Contains(lastSys(), "applies when the team starts again") {
		t.Errorf("launch: %v %q", err, lastSys())
	}
	if err := tab.StageLaunch(ctx, wire.LaunchPatch{Isolation: &bad}); code(err) != "bad_isolation" {
		t.Errorf("bad isolation: %v", err)
	}
	if err := tab.Compact(ctx, wire.CompactRequest{}); err != nil || !strings.Contains(strings.Join(kindsOf(t, tab, 0), " "), "compact") {
		t.Errorf("compact: %v", err)
	}
	if _, err := tab.Command(ctx, wire.CommandRequest{Line: "/review x"}); err != nil {
		t.Errorf("custom command: %v", err)
	}
	if _, err := tab.Command(ctx, wire.CommandRequest{Line: "/nope"}); code(err) != "unknown_command" || status(err) != 404 {
		t.Errorf("unknown command: %v", err)
	}
	for _, bad := range []string{"", strings.Repeat("x", 61), "a\nb"} {
		if err := tab.Rename(ctx, bad); code(err) != "bad_name" {
			t.Errorf("rename %q: %v", bad, err)
		}
	}
	if err := tab.Rename(ctx, "shop two"); err != nil || tab.Summary().Name != "shop two" {
		t.Errorf("rename: %v", err)
	}
	if f := h.FramesOf("tab"); len(f) == 0 {
		t.Error("the rename was not published")
	}
}

func TestRulesAreAddedRemovedAndFixedOnesRefused(t *testing.T) {
	h := NewShopHost()
	tab := h.FakeTab(TabID)
	res, err := tab.AddRule(ctx, wire.RuleRequest{Rule: "Bash(make:*)"})
	if err != nil || res.Added != 1 {
		t.Fatalf("add: %+v %v", res, err)
	}
	res, err = tab.AddRule(ctx, wire.RuleRequest{Rule: "tests"})
	if err != nil || res.Added != len(TestsPreset()) {
		t.Errorf("tests preset: %+v %v", res, err)
	}
	if _, err := tab.AddRule(ctx, wire.RuleRequest{Rule: ""}); code(err) != "bad_rule" {
		t.Errorf("empty rule: %v", err)
	}
	if _, err := tab.AddRule(ctx, wire.RuleRequest{Rule: "x", Effect: "maybe"}); code(err) != "bad_rule" {
		t.Errorf("bad effect: %v", err)
	}
	if _, err := tab.RemoveRule(ctx, wire.RuleRequest{Rule: "Bash(make:*)"}); err != nil {
		t.Errorf("remove: %v", err)
	}
	if _, err := tab.RemoveRule(ctx, wire.RuleRequest{Rule: "Bash(make:*)"}); code(err) != "no_rule" || status(err) != 404 {
		t.Errorf("remove again: %v", err)
	}
	if _, err := tab.RemoveRule(ctx, wire.RuleRequest{Rule: "Read(./.env)"}); code(err) != "fixed" || status(err) != 409 {
		t.Errorf("remove a file's rule: %v", err)
	}
	rules, _ := tab.Rules(ctx)
	if len(rules) != 2+len(TestsPreset()) {
		t.Errorf("rules = %d", len(rules))
	}
}

func TestGoalFollowsTheContractsStates(t *testing.T) {
	h := NewHost()
	tab, _ := h.AddTab("scratch", Cwd)
	for action, want := range map[string]string{"pause": "no_goal", "resume": "no_goal", "clear": "no_goal"} {
		if err := tab.Goal(ctx, wire.GoalRequest{Action: action}); code(err) != want || status(err) != 409 {
			t.Errorf("%s without a goal: %v", action, err)
		}
	}
	if err := tab.Goal(ctx, wire.GoalRequest{Action: "set"}); code(err) != "empty" {
		t.Errorf("set without text: %v", err)
	}
	if err := tab.Goal(ctx, wire.GoalRequest{Action: "set", Text: "ship it"}); err != nil {
		t.Fatal(err)
	}
	if err := tab.Goal(ctx, wire.GoalRequest{Action: "resume"}); code(err) != "not_paused" {
		t.Errorf("resume while active: %v", err)
	}
	if err := tab.Goal(ctx, wire.GoalRequest{Action: "pause"}); err != nil {
		t.Fatal(err)
	}
	if err := tab.Goal(ctx, wire.GoalRequest{Action: "pause"}); code(err) != "not_active" {
		t.Errorf("pause twice: %v", err)
	}
	if err := tab.Goal(ctx, wire.GoalRequest{Action: "resume"}); err != nil {
		t.Fatal(err)
	}
	if err := tab.Goal(ctx, wire.GoalRequest{Action: "clear"}); err != nil {
		t.Fatal(err)
	}
	if tab.goalState() != "cleared" {
		t.Errorf("goal = %q", tab.goalState())
	}
	if err := tab.Goal(ctx, wire.GoalRequest{Action: "stop"}); err == nil {
		t.Error("an unknown action was accepted")
	}
}

func TestQuestionsHaveAQuietPeriodAndSingleUseAnswers(t *testing.T) {
	h := NewShopHost()
	now := time.Date(2026, 10, 9, 22, 0, 0, 0, time.UTC)
	h.SetClock(func() time.Time { return now })
	tab := h.FakeTab(TabID)
	qid := ShopQuestion().ID

	if _, err := tab.Answer("q_zzzzzzzzzzzzzzzzzzzzzzzzzz", wire.AnswerRequest{Choice: 1}); code(err) != "no_question" {
		t.Errorf("unknown question: %v", err)
	}
	for _, bad := range []wire.AnswerRequest{{Choice: 0}, {Choice: 4}, {Choice: 1, Note: "why"}, {Choice: 3, Note: strings.Repeat("x", 2001)}} {
		if _, err := tab.Answer(qid, bad); code(err) != "bad_choice" || status(err) != 400 {
			t.Errorf("answer %+v: %v", bad, err)
		}
	}
	// Questions of the history can be answered at once; one asked now has to wait 350 ms.
	if res, err := tab.Answer(qid, wire.AnswerRequest{Choice: 2}); err != nil || res.Rule != ShopQuestion().Rule {
		t.Fatalf("answer = %+v %v", res, err)
	}
	if _, err := tab.Answer(qid, wire.AnswerRequest{Choice: 1}); code(err) != "no_question" {
		t.Errorf("answering twice: %v", err)
	}
	if !tab.wasAnswered(qid) {
		t.Error("the answer is not in the journal")
	}
	kinds := kindsOf(t, tab, uint64(len(Shop().History)))
	if strings.Join(kinds, " ") != "answer state" {
		t.Errorf("an answer journals %v", kinds)
	}
	rules, _ := tab.Rules(ctx)
	if rules[len(rules)-1].Rule != ShopQuestion().Rule || rules[len(rules)-1].Origin != "don't ask again" {
		t.Errorf("choice 2 did not add the rule: %+v", rules)
	}

	tab.Ask()
	tab.Ask() // only once
	later := LaterQuestion().ID
	_, err := tab.Answer(later, wire.AnswerRequest{Choice: 1})
	var we *wire.Error
	if !errors.As(err, &we) || we.Code != "too_soon" || we.Status != 409 {
		t.Fatalf("an immediate answer: %v", err)
	}
	detail, _ := we.Detail.(map[string]int64)
	if detail["retryAfterMs"] < 1 || detail["retryAfterMs"] > 351 {
		t.Errorf("retryAfterMs = %v", we.Detail)
	}
	now = now.Add(400 * time.Millisecond)
	if _, err := tab.Answer(later, wire.AnswerRequest{Choice: 3, Note: "not now"}); err != nil {
		t.Errorf("after the quiet period: %v", err)
	}
	if qs := h.Questions(); len(qs) != 0 {
		t.Errorf("open questions: %+v", qs)
	}
}

func TestRestartsStartAGenerationAndPublishIt(t *testing.T) {
	h := NewShopHost()
	tab := h.FakeTab(TabID)
	two := 2
	if err := tab.Restart(ctx, wire.RestartRequest{Kind: "swarm", Swarm: &two}); err != nil {
		t.Fatal(err)
	}
	s := tab.Summary()
	if s.Gen != 2 || s.SID == SID {
		t.Errorf("summary after a fresh restart: %+v", s)
	}
	snap, _ := tab.Snapshot(ctx)
	if len(snap.Roster) != 3 || *snap.Meta.Swarm != 2 || len(snap.Questions) != 0 {
		t.Errorf("snapshot after: roster %d swarm %v questions %d", len(snap.Roster), snap.Meta.Swarm, len(snap.Questions))
	}
	if snap.Seq != 1 || len(snap.Events) != 1 {
		t.Errorf("a fresh generation starts its journal again: seq %d events %d", snap.Seq, len(snap.Events))
	}
	var types []string
	for _, f := range h.Frames() {
		types = append(types, f.Type)
	}
	if got := strings.Join(types, " "); !strings.Contains(got, "reset") || !strings.Contains(got, "roster") || !strings.Contains(got, "tab") {
		t.Errorf("frames: %s", got)
	}
	// A restart that carries the conversation keeps the journal.
	if err := tab.Restart(ctx, wire.RestartRequest{Kind: "model", Model: "heimdall/x"}); err != nil {
		t.Fatal(err)
	}
	if snap2, _ := tab.Snapshot(ctx); snap2.Seq < snap.Seq || snap2.Gen != 3 {
		t.Errorf("carried restart: seq %d gen %d", snap2.Seq, snap2.Gen)
	}
	if err := tab.Restart(ctx, wire.RestartRequest{Kind: "swarm"}); code(err) != "bad_flags" {
		t.Errorf("swarm without a count: %v", err)
	}
	if err := tab.Restart(ctx, wire.RestartRequest{Kind: "explode"}); code(err) != "bad_flags" {
		t.Errorf("unknown kind: %v", err)
	}
}

func TestTabsComeAndGoWithinTheLimits(t *testing.T) {
	h := NewShopHost()
	if err := h.CloseTab(TabID); code(err) != "last" || status(err) != 409 {
		t.Errorf("closing the only tab: %v", err)
	}
	if err := h.CloseTab("nope"); code(err) != "no_session" {
		t.Errorf("closing a tab that is not there: %v", err)
	}
	var ids []string
	for i := 1; i < maxTabs; i++ {
		tab, err := h.AddTab("", Cwd)
		if err != nil {
			t.Fatalf("tab %d: %v", i, err)
		}
		ids = append(ids, tab.Summary().ID)
	}
	if _, err := h.AddTab("one too many", Cwd); code(err) != "limit" || status(err) != 409 {
		t.Errorf("the 17th tab: %v", err)
	}
	seen := map[string]bool{}
	for _, tab := range h.Tabs() {
		if seen[tab.ID] || !validTabID(tab.ID) {
			t.Errorf("tab id %q is a duplicate or not valid", tab.ID)
		}
		seen[tab.ID] = true
	}
	if err := h.CloseTab(ids[0]); err != nil {
		t.Fatal(err)
	}
	if len(h.Tabs()) != maxTabs-1 {
		t.Errorf("%d tabs", len(h.Tabs()))
	}
	if tabs := h.FramesOf("tab"); len(tabs) != maxTabs {
		t.Errorf("%d tab frames, want 15 adds and a remove", len(tabs))
	}
	// Closing the active tab moves the active one.
	if err := h.CloseTab(TabID); err != nil || h.Active() == TabID || h.Active() == "" {
		t.Errorf("closing the active tab: %v, active %q", err, h.Active())
	}
}

func TestCallsAreRecordedAndFailuresInjected(t *testing.T) {
	tab := NewShopHost().FakeTab(TabID)
	boom := errors.New("boom")
	tab.Fail("SetMode", boom)
	if err := tab.SetMode(ctx, wire.ModeRequest{Mode: "plan"}); !errors.Is(err, boom) {
		t.Errorf("injected failure: %v", err)
	}
	tab.Fail("SetMode", nil)
	if err := tab.SetMode(ctx, wire.ModeRequest{Mode: "plan"}); err != nil {
		t.Errorf("after clearing the failure: %v", err)
	}
	calls := tab.Calls()
	if len(calls) != 2 || calls[0].Method != "SetMode" || calls[1].Arg.(wire.ModeRequest).Mode != "plan" {
		t.Errorf("calls = %+v", calls)
	}
}

func TestSessionAccessOfAFakeTab(t *testing.T) {
	tab := NewShopHost().FakeTab(TabID)
	var a seam.SessionAccess = tab.Access()
	if a.TabID() != TabID || a.Session() != nil || !a.Busy() {
		t.Errorf("access: %q %v busy %v", a.TabID(), a.Session(), a.Busy())
	}
	if err := a.Exclusive(ctx, func(*sessionT) error { return nil }); code(err) != "busy" {
		t.Errorf("Exclusive while a turn runs: %v", err)
	}
	idle := NewHost()
	it, _ := idle.AddTab("x", Cwd)
	ran := false
	if err := it.Access().Exclusive(ctx, func(*sessionT) error { ran = true; return nil }); err != nil || !ran {
		t.Errorf("Exclusive while idle: %v ran %v", err, ran)
	}
	n := len(it.Access().(*Tab).journal)
	it.Access().Emit(&wire.Sys{Ch: "mgr", Glyph: "◇", Text: "checkpoint c02"})
	it.Access().Notify("rewound")
	run := false
	it.Access().Meta(wire.MetaPatch{Running: &run})
	if len(it.journal) != n+1 || it.Calls()[len(it.Calls())-1].Method != "Notify" {
		t.Errorf("emit/notify: journal %d calls %+v", len(it.journal), it.Calls())
	}
}
