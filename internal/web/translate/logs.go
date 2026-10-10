package translate

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/anemos-labs/sleipnir/internal/core"
	"github.com/anemos-labs/sleipnir/internal/events"
	"github.com/anemos-labs/sleipnir/internal/plan"
	"github.com/anemos-labs/sleipnir/internal/swarm"
	"github.com/anemos-labs/sleipnir/internal/tui/state"
	"github.com/anemos-labs/sleipnir/internal/web/wire"
)

// maxHistoryBytes is the largest events.jsonl whose history is translated when a session is resumed; a longer one is shown by its
// resumed row alone (its history is in Replay, VOCAB.md 12).
const maxHistoryBytes = 256 << 20

// errStopScan ends a scan early.
var errStopScan = errors.New("translate: stop")

// Attach follows a session's log until the returned function is called; the history of an existing log (VOCAB.md section 12) is
// translated first. It subscribes before it reads the file, so an event written meanwhile is in both and applied once (by its seq).
func (t *Translator) Attach(log *events.Log, dir string) (detach func()) {
	if log == nil {
		return func() {}
	}
	ch, cancel := log.Subscribe(subscriptionBuffer)
	_ = log.Flush()
	path := filepath.Join(dir, "events.jsonl")
	t.mu.Lock()
	if t.closed {
		t.mu.Unlock()
		cancel()
		return func() {}
	}
	if t.started.IsZero() {
		t.started = t.now()
	}
	t.d.logPath, t.d.logFlush = path, log.Flush
	t.readExisting(path)
	t.d.detach = cancel
	t.mu.Unlock()
	select {
	case t.attach <- attachReq{ch: ch, cancel: cancel}:
	default:
		// a previous attach the loop has not taken yet is replaced
		select {
		case old := <-t.attach:
			old.cancel()
		default:
		}
		t.attach <- attachReq{ch: ch, cancel: cancel}
	}
	return cancel
}

// Follow translates a log that another process writes (PARITY A7: a session watched read-only): every event comes from the file,
// which is polled; the run's start is its first event unless Config.StartedAt says otherwise. It returns the function that stops
// following.
func (t *Translator) Follow(path string) (stop func()) {
	t.follow(path)
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.d.stopFollow == nil {
		return func() {}
	}
	return t.d.stopFollow
}

// readExisting translates what the log holds already: the history of earlier runs when the log has one (a session.start is in it),
// else the start of this run.
func (t *Translator) readExisting(path string) {
	fi, err := os.Stat(path)
	if err != nil {
		return
	}
	resumed := false
	if fi.Size() <= maxHistoryBytes {
		resumed = hasSessionStart(path)
	} else {
		t.d.noGapCheck = true
		t.put(&wire.Say{Who: "sys", Glyph: "↺", Text: "↺ resumed: its history is too long to show here; Replay of the recorded session has it"}, 0, 0, nil)
		return
	}
	if resumed {
		t.d.history, t.d.logOnly = true, true
	}
	_ = events.Scan(path, func(e events.Event) error {
		t.applyLog(e)
		return nil
	})
	if resumed {
		t.finishHistory()
	}
	t.publishRoster()
}

// hasSessionStart reports whether the log at path records the start of a run (a session.start event): the log of a session that
// ran before, whose events are history to this run.
func hasSessionStart(path string) bool {
	f, err := os.Open(path)
	if err != nil {
		return false
	}
	defer f.Close()
	r := bufio.NewReaderSize(f, 1<<16)
	needle := []byte(`"type":"` + events.TypeSessionStart + `"`)
	for {
		l, err := r.ReadSlice('\n')
		if bytes.Contains(l, needle) {
			return true
		}
		switch {
		case err == nil:
		case errors.Is(err, bufio.ErrBufferFull):
			// a long line: the type comes early in the envelope, so the rest of it is skipped
			for errors.Is(err, bufio.ErrBufferFull) {
				_, err = r.ReadSlice('\n')
			}
			if err != nil && !errors.Is(err, io.EOF) {
				return false
			}
			if errors.Is(err, io.EOF) {
				return false
			}
		default:
			return false
		}
	}
}

// followEvent applies one event of the subscription; a gap in the seqs (the subscription dropped events) is filled from the file.
func (t *Translator) followEvent(e events.Event) {
	if e.Seq != 0 && e.Seq <= t.d.logSeq {
		return
	}
	if t.d.noGapCheck {
		t.d.noGapCheck = false
	} else if e.Seq > t.d.logSeq+1 && t.d.logPath != "" {
		t.rescan(t.d.logSeq, e.Seq)
	}
	t.applyLog(e)
	t.publishRoster()
}

// rescan applies the events of the file with a seq between from and to (both excluded), after flushing the log.
func (t *Translator) rescan(from, to uint64) {
	if t.d.logFlush != nil {
		_ = t.d.logFlush()
	}
	_ = events.Scan(t.d.logPath, func(e events.Event) error {
		switch {
		case e.Seq <= from:
			return nil
		case e.Seq >= to:
			return errStopScan
		}
		t.applyLog(e)
		return nil
	})
}

// logTime is the t and at of a log event: its session time, or for a history event 0 and its real time.
func (t *Translator) logTime(ts time.Time) (float64, int64) {
	if t.d.history {
		if ts.IsZero() {
			return 0, 0
		}
		return 0, ts.UnixMilli()
	}
	return t.sessT(ts), 0
}

// finishHistory journals the history of a resumed log: its events at t 0 (the newest History of them), then the state the whole
// log folds to (the agents' token tables, ratio series, layers and states, the tasks, the mail counts) at t 0, then the resumed
// row.
func (t *Translator) finishHistory() {
	t.flushPend(0, true)
	t.flushCkpts(0, true)
	t.d.history = false
	buf, dropped := t.d.histBuf, t.d.histDrop
	t.d.histBuf, t.d.histDrop = nil, 0
	if len(buf) > t.lim.History {
		dropped += len(buf) - t.lim.History
		buf = buf[len(buf)-t.lim.History:]
	}
	if dropped > 0 {
		t.put(&wire.Say{Who: "sys", Glyph: "◇", Text: fmt.Sprintf("◇ %d earlier events of the history are not shown; Replay of the recorded session has them", dropped)}, 0, 0, nil)
	}
	for _, h := range buf {
		if h.e != nil { // a tool row's place that its row never filled is skipped
			t.put(h.e, 0, h.at, h.pre)
		}
	}
	for _, hid := range t.st.AgentIDs() { // the constitution estimate of the whole log, before any layers are sent
		if a, ok := t.st.AgentLite(hid); ok && a.Stack.Unsectioned > 0 && (t.d.g0 == 0 || a.Stack.Unsectioned < t.d.g0) {
			t.d.g0 = a.Stack.Unsectioned
		}
	}
	for _, hid := range t.st.AgentIDs() {
		if nonAgent(hid) {
			continue
		}
		uid := uiID(hid)
		if t.agentOutOf(uid).service {
			continue
		}
		t.sendUse(hid, 0, true)
		t.sendLayers(hid, 0, true)
		for _, r := range t.st.Hits(hid).Ratios {
			t.put(&wire.Req{ID: uid, Ratio: r, Hist: true}, 0, 0, nil)
		}
		if a, ok := t.st.AgentLite(hid); ok {
			t.sendState(uid, t.agentOutOf(uid), t.stateOf(uid, a), 0)
		}
	}
	for _, id := range t.st.TaskIDs() {
		t.syncTask(id, 0, true)
	}
	t.syncMailStat(0)
	t.syncAlerts(nil, 0)
	t.put(&wire.Say{Who: "sys", Glyph: "↺", Text: t.resumedText()}, 0, 0, nil)
	t.d.logOnly = false
}

// resumedText is the resumed row: the session, the date of its last run and the permissions it gets back (the last perm.state of the
// log: a mode other than the default, bypass and yolo, and the allow rules).
func (t *Translator) resumedText() string {
	s := "↺ resumed " + firstNonEmpty(t.st.Session().ID, "a recorded session")
	if !t.d.lastTS.IsZero() {
		s += " · " + t.d.lastTS.Local().Format("2006-01-02 15:04")
	}
	var back []string
	if m := t.d.permMode; m != "" && m != "default" && m != "bypass" && m != "yolo" {
		back = append(back, "the mode "+m)
	}
	if n := t.d.permRules; n > 0 {
		word := "allow rules"
		if n == 1 {
			word = "allow rule"
		}
		back = append(back, fmt.Sprintf("%d %s", n, word))
	}
	if len(back) > 0 {
		s += ". Back as they were: " + strings.Join(back, " and ")
	}
	return line(s, capReason)
}

// applyLog folds one log event into the State and sends what it means (VOCAB.md 5, 6, 15, 16).
func (t *Translator) applyLog(e events.Event) {
	if e.Seq != 0 {
		if e.Seq <= t.d.logSeq {
			return
		}
		t.d.logSeq = e.Seq
	}
	if !e.TS.IsZero() && e.TS.After(t.d.lastTS) {
		t.d.lastTS = e.TS
	}
	ts, at := t.logTime(e.TS)
	isAlert := e.Type == events.TypeBoardOp && !t.d.history && alertOp(e.Data)
	var alerts []state.Alert
	if isAlert {
		alerts = t.st.Board().Alerts
	}
	t.chA, t.chT = t.chA[:0], t.chT[:0]
	t.st.Apply(e)
	agents, tasks := append([]string(nil), t.chA...), append([]string(nil), t.chT...)
	for _, id := range agents {
		t.seen(id)
	}
	t.publishRoster()
	t.derive(e, ts, at)
	if isAlert {
		t.syncAlerts(alerts, ts)
	}
	if t.d.history {
		return
	}
	for _, id := range tasks {
		t.syncTask(id, ts, false)
	}
	for _, id := range agents {
		if !nonAgent(id) {
			t.refreshAgent(id, ts, false)
		}
	}
	if len(tasks) > 0 || len(agents) > 0 {
		if h := t.harnessID("mgr"); !containsStr(agents, h) {
			t.refreshAgent(h, ts, false)
		}
	}
	if rosterEvent(e, t.d.ros.ents[uiID(e.Agent)]) {
		t.syncRoster()
	}
}

// rosterEvent reports whether an event can change the roster: an agent started or given work, the session's start, a board operation
// that moves a task between agents or changes its files, or the first request of an agent whose model the roster does not know. The
// roster is rebuilt only then, so that the work per event stays constant.
func rosterEvent(e events.Event, ent *wire.RosterEntry) bool {
	switch e.Type {
	case events.TypeAgentSpawn, "agent.assign", events.TypeSessionStart, events.TypeAgentRestore:
		return true
	case events.TypeModelRequest:
		return ent == nil || ent.Model == ""
	case events.TypeBoardOp:
		var p struct {
			Op string `json:"op"`
		}
		if json.Unmarshal(e.Data, &p) != nil {
			return false
		}
		switch p.Op {
		case "claim", "assign", "scope", "requeue", "finish", "block", "resume", "agent-remove":
			return true
		}
	}
	return false
}

// containsStr reports whether s holds x.
func containsStr(s []string, x string) bool {
	for _, v := range s {
		if v == x {
			return true
		}
	}
	return false
}

// alertOp reports whether a board.op payload is one of the alert operations.
func alertOp(data json.RawMessage) bool {
	return bytes.Contains(data, []byte(`"op":"alert`))
}

// derive sends the events one log event means, by its type.
func (t *Translator) derive(e events.Event, ts float64, at int64) {
	hid := e.Agent
	switch e.Type {
	case events.TypeUserInput:
		t.userInput(e, ts, at)
	case events.TypeTurnAppend:
		t.turnAppend(e, ts, at)
	case events.TypeToolCall:
		t.toolCall(e, ts, at)
	case events.TypeToolResult:
		t.toolResult(e, ts, at)
	case events.TypeModelRequest:
		var p struct {
			Req   string `json:"req"`
			Agent string `json:"agent"`
			Kind  string `json:"kind"`
		}
		if json.Unmarshal(e.Data, &p) == nil {
			who := firstNonEmpty(p.Agent, hid)
			t.setSide(who+"|"+p.Req, p.Kind != "" && p.Kind != "main")
			if t.d.logOnly {
				t.flushPendOf(uiID(who), ts)
			}
		}
	case events.TypeModelResponse:
		t.response(e, ts, at)
	case events.TypeModelError:
		t.modelError(e, ts)
	case "notice":
		var p struct {
			Level   string `json:"level"`
			Msg     string `json:"msg"`
			Message string `json:"message"`
		}
		if json.Unmarshal(e.Data, &p) == nil {
			uid := uiID(hid)
			if nonAgent(hid) {
				uid = "mgr"
			}
			t.noticeAt(uid, p.Level, firstNonEmpty(p.Msg, p.Message), ts, at, false)
		}
	case events.TypeLease:
		t.lease(e, ts, at)
	case events.TypeSwarmStall:
		t.stall(e, ts, at)
	case events.TypeSwarmHandover:
		t.handover(e, ts, at)
	case events.TypeAgentEnd:
		var p struct {
			ID       string `json:"id"`
			Evidence string `json:"evidence"`
		}
		if !t.d.history && json.Unmarshal(e.Data, &p) == nil && p.Evidence != "" {
			uid := uiID(firstNonEmpty(p.ID, hid))
			if !t.agentOutOf(uid).service && !nonAgent(uid) {
				var task string
				if a, ok := t.st.AgentLite(firstNonEmpty(p.ID, hid)); ok {
					task = a.Task
				}
				t.put(&wire.Note{ID: uid, G: "done", Text: line(p.Evidence, capNote), Task: task}, ts, at, nil)
			}
		}
		if t.d.logOnly {
			t.flushPendOf(uiID(hid), ts)
		}
	case events.TypeAgentCancel:
		if t.d.logOnly {
			t.flushPendOf(uiID(hid), ts)
		}
	case events.TypeMailSend, events.TypeMailDigest, events.TypeMailRoute, events.TypeMailDeliver, "mail.drop", events.TypeMailAck,
		events.TypeMailDirect, events.TypeMailBatch, events.TypeMailmanState:
		t.mail(e, ts, at)
	case events.TypeMergeQueued, events.TypeMergeMerged, events.TypeMergeConflict, events.TypeMergeVerifyFail, events.TypeMergeRejected,
		events.TypeTaskMerge:
		t.merge(e, ts, at)
	case "swarm.budget":
		var p struct {
			BudgetUSD float64 `json:"budget_usd"`
			SpentUSD  float64 `json:"spent_usd"`
		}
		if json.Unmarshal(e.Data, &p) == nil {
			t.sysRow("mgr", "⚠", fmt.Sprintf("⚠ budget reached: $%.2f of $%.2f spent; running workers were stopped", p.SpentUSD, p.BudgetUSD), "", "", ts, at)
		}
	case "agent.panic", "tool.panic", "supervisor.panic":
		var p struct {
			ID    string `json:"id"`
			Agent string `json:"agent"`
			Name  string `json:"name"`
			Panic string `json:"panic"`
		}
		if json.Unmarshal(e.Data, &p) == nil {
			who := uiID(firstNonEmpty(p.ID, p.Agent, hid))
			what := firstNonEmpty(who, "the swarm")
			if e.Type == "tool.panic" {
				what = "tool " + firstNonEmpty(p.Name, "a tool")
			}
			t.sysRow("mgr", "⚠", "⚠ "+what+" crashed: "+firstLine(p.Panic), who, "", ts, at)
		}
	case events.TypeCacheAnomaly:
		var p struct {
			Kind         string `json:"kind"`
			ExpectedRead int    `json:"expected_read"`
			ActualRead   int    `json:"actual_read"`
		}
		if json.Unmarshal(e.Data, &p) == nil && !nonAgent(hid) {
			kind := firstNonEmpty(p.Kind, "unknown")
			t.put(&wire.Break{ID: uiID(hid), Kind: line(kind, capID), Read: max(p.ActualRead, 0), Expected: max(p.ExpectedRead, 0),
				Why: line(anomalyWhy(kind), capWhy)}, ts, at, nil)
		}
	case events.TypeCompactCommit:
		t.compact(e, ts, at)
	case state.TypeGoalState:
		t.goalState(e, ts, at)
	case state.TypeGoalJudge:
		t.goalJudge(e, ts, at)
	case state.TypeCheckpoint:
		t.checkpoint(e, ts, at)
	case state.TypeCheckpointRestore:
		if t.d.logOnly {
			var p struct {
				ID    string   `json:"id"`
				Files []string `json:"files"`
			}
			if json.Unmarshal(e.Data, &p) == nil {
				t.sysRow("mgr", "↺", fmt.Sprintf("↺ restored %s: %d files", cidOf(p.ID), len(p.Files)), "", "", ts, at)
			}
		}
	case state.TypeWebAction:
		if t.d.logOnly {
			var p struct {
				Action string `json:"action"`
				Detail string `json:"detail"`
			}
			if json.Unmarshal(e.Data, &p) == nil {
				t.sysRow("mgr", "❯", strings.TrimSpace(p.Action+": "+p.Detail), "", "", ts, at)
			}
		}
	case events.TypeUserSteer:
		if t.d.logOnly {
			var p struct {
				Text string `json:"text"`
				To   string `json:"to"`
			}
			if json.Unmarshal(e.Data, &p) == nil && p.Text != "" {
				t.put(&wire.Steer{To: uiID(firstNonEmpty(p.To, "mgr")), Text: line(p.Text, capReason)}, ts, at, nil)
			}
		}
	case swarm.EventVerifyRun:
		var p struct {
			Cmd string `json:"cmd"`
		}
		if json.Unmarshal(e.Data, &p) == nil && p.Cmd != "" {
			t.d.queue.gate = line(p.Cmd, capArg)
		}
	case events.TypeSessionStart:
		t.d.ended = false
		var p struct {
			Root string `json:"root"`
			Cwd  string `json:"cwd"`
		}
		if t.d.root == "" && json.Unmarshal(e.Data, &p) == nil {
			t.d.root = firstNonEmpty(p.Root, p.Cwd)
		}
	case events.TypeSessionEnd:
		t.d.ended = true
	case events.TypePermState:
		var p struct {
			Mode  string   `json:"mode"`
			Allow []string `json:"allow"`
		}
		if json.Unmarshal(e.Data, &p) == nil {
			t.d.permMode, t.d.permRules = p.Mode, len(p.Allow)
		}
	}
}

// setSide remembers whether a request was a side request, bounded.
func (t *Translator) setSide(key string, side bool) {
	if len(t.d.reqSide) >= 4096 {
		t.d.reqSide = map[string]bool{}
	}
	t.d.reqSide[key] = side
}

// isMain reports whether the agent is the one the person talks to.
func isMain(hid string) bool { return hid == "main" || hid == "mgr" }

// userInput is user.input: the person's line to the main agent (a say you row in a log-only translation; always a line of the
// composer's history) and, in a followed log, the start of a turn.
func (t *Translator) userInput(e events.Event, ts float64, at int64) {
	var p struct {
		Text   string `json:"text"`
		Origin string `json:"origin"`
	}
	if json.Unmarshal(e.Data, &p) != nil || (p.Origin != "" && p.Origin != "user") || !isMain(e.Agent) {
		return
	}
	t.d.addHist(text(p.Text, capEvent))
	if !t.d.logOnly {
		return
	}
	t.put(&wire.Say{Who: "you", Text: text(p.Text, capEvent)}, ts, at, nil)
	if !t.d.history {
		t.put(&wire.Turn{S: "start"}, ts, at, nil)
		t.d.turnOpen = true
	}
}

// turnAppend is turn.append: in a log-only translation, the main agent's answer (a say row) and the output of tool calls (the rows
// waiting for it).
func (t *Translator) turnAppend(e events.Event, ts float64, at int64) {
	if !t.d.logOnly {
		return
	}
	var turn core.Turn
	if len(e.Data) > 16<<20 || json.Unmarshal(e.Data, &turn) != nil {
		return
	}
	uid := uiID(e.Agent)
	switch turn.Role {
	case core.RoleAssistant:
		if !isMain(e.Agent) {
			return
		}
		var b strings.Builder
		calls := false
		for _, blk := range turn.Blocks {
			switch {
			case blk.Kind == core.BlockText && blk.Text != "":
				if b.Len() > 0 {
					b.WriteString("\n\n")
				}
				b.WriteString(blk.Text)
			case blk.Kind == core.BlockToolUse:
				calls = true
			}
		}
		t.sayMessage(b.String(), ts, at)
		if !calls {
			// an answer that calls no tool ends the turn
			t.put(&wire.Final{}, ts, at, nil)
			if !t.d.history && t.d.turnOpen {
				t.put(&wire.Turn{S: "end"}, ts, at, nil)
				t.d.turnOpen = false
			}
		}
	case core.RoleUser:
		for _, blk := range turn.Blocks {
			if blk.Kind != core.BlockToolResult {
				continue
			}
			if p := t.d.pend[blk.ToolID]; p != nil {
				var b strings.Builder
				for _, r := range blk.Result {
					if r.Kind == core.BlockText {
						b.WriteString(r.Text)
					}
					if b.Len() > 1<<16 {
						break
					}
				}
				p.text, p.hasText = b.String(), true
				if blk.IsError {
					p.isErr = true
				}
			}
		}
		t.flushPendOf(uid, ts)
	}
}

// pendTool is a tool row of a log-only translation, waiting for the output its turn.append carries.
type pendTool struct {
	uid, tid, name string
	input          json.RawMessage
	ts             float64
	at             int64
	hasResult      bool
	isErr          bool
	refused        bool
	meta           map[string]any
	ms             int64
	text           string
	hasText        bool
	slot           int // the history place kept for the row, or -1
}

// toolCall is tool.call: the plan of the main agent (any translation), a tool row waiting for its result (log-only), and the end of
// a sink hold (the log has caught up with the tool start the sink showed).
func (t *Translator) toolCall(e events.Event, ts float64, at int64) {
	var p struct {
		ID    string          `json:"id"`
		Name  string          `json:"name"`
		As    string          `json:"as"`
		Input json.RawMessage `json:"input"`
	}
	if len(e.Data) > maxDecodedInput || json.Unmarshal(e.Data, &p) != nil {
		return
	}
	name := firstNonEmpty(p.As, p.Name)
	uid := uiID(e.Agent)
	if h, ok := t.d.hold[uid]; ok && (h.tid == "" || h.tid == p.ID) {
		delete(t.d.hold, uid)
	}
	if name == "plan" && isMain(e.Agent) {
		t.planCall(p.Input, ts, at)
	}
	if !t.d.logOnly || nonAgent(e.Agent) || p.ID == "" {
		return
	}
	if len(t.d.pend) >= maxPend {
		t.flushPend(ts, true)
	}
	pt := &pendTool{uid: uid, tid: p.ID, name: name, input: p.Input, ts: ts, at: at, slot: -1}
	if t.d.history {
		pt.slot = t.d.histPut(histEntry{}, t.lim.History) // the row's place: the transcript keeps the order of the calls
	}
	t.d.pend[p.ID] = pt
}

// planCall is a call of the plan tool by the main agent: the whole plan, normalized as the tool does.
func (t *Translator) planCall(input json.RawMessage, ts float64, at int64) {
	var in struct {
		Items []plan.Item `json:"items"`
	}
	if json.Unmarshal(input, &in) != nil {
		return
	}
	items, err := plan.Normalize(in.Items)
	if err != nil {
		return
	}
	ev := &wire.Plan{Steps: make([]string, 0, len(items)), St: make([]string, 0, len(items))}
	for _, it := range items {
		ev.Steps = append(ev.Steps, line(it.Step, capPlan))
		st := "pending"
		switch it.Status {
		case plan.Doing:
			st = "act"
		case plan.Done:
			st = "done"
		}
		ev.St = append(ev.St, st)
	}
	t.put(ev, ts, at, nil)
}

// toolResult is tool.result: in a log-only translation, the outcome of a waiting tool row.
func (t *Translator) toolResult(e events.Event, ts float64, at int64) {
	if !t.d.logOnly {
		return
	}
	var p struct {
		ID      string         `json:"id"`
		Error   bool           `json:"error"`
		Refused bool           `json:"refused"`
		Ms      int64          `json:"ms"`
		Meta    map[string]any `json:"meta"`
	}
	if json.Unmarshal(e.Data, &p) != nil {
		return
	}
	if pt := t.d.pend[p.ID]; pt != nil {
		pt.hasResult, pt.isErr, pt.refused, pt.meta, pt.ms = true, p.Error, p.Refused, p.Meta, p.Ms
	}
}

// flushPendOf sends the agent's tool rows that have their result.
func (t *Translator) flushPendOf(uid string, ts float64) {
	var ready []*pendTool
	for id, p := range t.d.pend {
		if p.uid == uid && p.hasResult {
			delete(t.d.pend, id)
			ready = append(ready, p)
		}
	}
	sortPend(ready)
	for _, p := range ready {
		t.sendPend(p)
	}
}

// flushPend sends the waiting tool rows that have their result and have waited two seconds (all of them, with all).
func (t *Translator) flushPend(ts float64, all bool) {
	var ready []*pendTool
	for id, p := range t.d.pend {
		if all || (p.hasResult && ts-p.ts >= 2) {
			delete(t.d.pend, id)
			if p.hasResult {
				ready = append(ready, p)
			}
		}
	}
	sortPend(ready)
	for _, p := range ready {
		t.sendPend(p)
	}
}

// sortPend orders tool rows by their time, then their call id.
func sortPend(ps []*pendTool) {
	for i := 1; i < len(ps); i++ {
		for j := i; j > 0 && (ps[j].ts < ps[j-1].ts || (ps[j].ts == ps[j-1].ts && ps[j].tid < ps[j-1].tid)); j-- {
			ps[j], ps[j-1] = ps[j-1], ps[j]
		}
	}
}

// sendPend sends a log-only tool row, at the time of its call.
func (t *Translator) sendPend(p *pendTool) {
	in := decodeInput(p.input)
	it := sinkItem{op: opToolEnd, tid: p.tid, tool: p.name, isErr: p.isErr, took: time.Duration(p.ms) * time.Millisecond}
	if p.name == "spawn" && !p.isErr {
		it.spawned = spawnedIn(p.text)
	}
	it.arg = argOf(t.root(), p.name, &in, it.spawned)
	resultDetails(&it, t.root(), p.name, &in, p.text, p.meta)
	if p.refused && p.isErr {
		it.refused = true
	}
	if t.d.history && p.slot >= 0 {
		t.d.histSlot = p.slot
	}
	t.toolEnd(p.uid, &it, p.ts, p.at)
	t.d.histSlot = -1
}
