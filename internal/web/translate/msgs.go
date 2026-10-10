package translate

import (
	"strconv"
	"strings"
	"time"

	"github.com/anemos-labs/sleipnir/internal/tui/state"
	"github.com/anemos-labs/sleipnir/internal/web/wire"
)

// A message is an answer being streamed (say for the manager, stream for a worker) or a file's content shown as it is written (a
// code stream). Its first event carries mid = "m" + its seq; the rest of its text follows as more events, at most one per message
// per 100 ms, cut where a secret cannot be split; end closes it. A message is capped at 64 KiB: past it, end is sent and the rest is
// dropped.
type message struct {
	uid       string
	mid       string
	opened    bool
	worker    bool
	code      bool
	pending   strings.Builder
	waitSince float64 // when the oldest pending text arrived
	lastFlush float64
	sent      int
	capped    bool
}

// moreGap is the least time between two more events of a message.
const moreGap = 0.1

// textDelta adds a delta to the agent's open message, opening one if needed.
func (t *Translator) textDelta(uid, delta string, ts float64) {
	m := t.d.msgs[uid]
	if m == nil {
		m = &message{uid: uid, worker: uid != "mgr", lastFlush: ts}
		t.d.msgs[uid] = m
	}
	if m.capped {
		return
	}
	if m.pending.Len() == 0 {
		m.waitSince = ts
	}
	m.pending.WriteString(delta)
	if !m.opened {
		t.flushMsg(m, ts, false) // the first words open it at once
	}
}

// flushMsg sends a message's pending text: the opening event, or a more. force sends all of it (the message ends); otherwise text
// after the last white space waits for the next flush, unless it has waited a second.
func (t *Translator) flushMsg(m *message, ts float64, force bool) {
	p := m.pending.String()
	if p == "" {
		return
	}
	ready, rest := safeSplit(p, force || ts-m.waitSince >= 1)
	if ready == "" {
		return
	}
	m.pending.Reset()
	m.pending.WriteString(rest)
	m.waitSince = ts
	m.lastFlush = ts
	for ready != "" {
		chunk := cutBytes(ready, capEvent)
		if chunk == "" {
			chunk = ready
		}
		ready = ready[len(chunk):]
		if m.sent+len(chunk) > capMessage {
			chunk = cutBytes(chunk, max(capMessage-m.sent, 0))
			ready = ""
			m.capped = true
		}
		out := text(chunk, capEvent)
		m.sent += len(chunk)
		if !m.opened {
			t.openMsg(m, out, ts)
		} else if out != "" {
			t.put(&wire.More{Mid: m.mid, Text: out}, ts, 0, nil)
		}
		if m.capped {
			t.put(&wire.More{Mid: m.mid, End: true}, ts, 0, nil)
			m.pending.Reset()
			return
		}
	}
}

// openMsg sends a message's first event: say for the manager, stream for a worker.
func (t *Translator) openMsg(m *message, txt string, ts float64) {
	m.opened = true
	if !m.worker {
		e := &wire.Say{Who: "mgr", Text: txt, Stream: true, Rate: 240}
		t.put(e, ts, 0, func(seq uint64) {
			m.mid = "m" + strconv.FormatUint(seq, 10)
			e.Mid = m.mid
		})
		return
	}
	e := &wire.Stream{ID: m.uid, Text: txt, Rate: 240}
	t.put(e, ts, 0, func(seq uint64) {
		m.mid = "m" + strconv.FormatUint(seq, 10)
		e.Mid = m.mid
	})
}

// endMsg ends the agent's open prose message: what is pending is sent, then end (with reset: a retried request; the pending text is
// dropped).
func (t *Translator) endMsg(uid string, ts float64, reset bool) {
	m := t.d.msgs[uid]
	if m == nil {
		return
	}
	delete(t.d.msgs, uid)
	t.closeMsg(m, ts, reset)
}

// closeMsg ends a message.
func (t *Translator) closeMsg(m *message, ts float64, reset bool) {
	if !reset {
		t.flushMsg(m, ts, true)
	}
	if !m.opened || m.capped {
		return
	}
	t.put(&wire.More{Mid: m.mid, End: true, Reset: reset}, ts, 0, nil)
}

// endAll ends every open message.
func (t *Translator) endAll(ts float64) {
	for _, uid := range sortedKeys(t.d.msgs) {
		t.endMsg(uid, ts, false)
	}
	for _, key := range sortedKeys(t.d.codes) {
		m := t.d.codes[key]
		delete(t.d.codes, key)
		t.closeMsg(m, ts, false)
	}
}

// flushMessages sends the pending text of the messages whose gap has passed.
func (t *Translator) flushMessages(ts float64) {
	for _, uid := range sortedKeys(t.d.msgs) {
		if m := t.d.msgs[uid]; m.pending.Len() > 0 && ts-m.lastFlush >= moreGap {
			t.flushMsg(m, ts, false)
		}
	}
}

// sayMessage sends a whole message at once (a history answer of the manager): say with the first 16 KiB, more events with the rest
// up to 64 KiB, and end when there was more than one event.
func (t *Translator) sayMessage(txt string, ts float64, at int64) {
	txt = text(txt, capMessage)
	if strings.TrimSpace(txt) == "" {
		return
	}
	first := cutBytes(txt, capEvent)
	rest := txt[len(first):]
	say := &wire.Say{Who: "mgr", Text: first, Stream: false}
	var mid string
	t.put(say, ts, at, func(seq uint64) {
		if rest != "" {
			mid = "m" + strconv.FormatUint(seq, 10)
			say.Mid = mid
		}
	})
	if rest == "" {
		return
	}
	// The seq of a history event is given when the history is journalled, so the continuation names the message through the same
	// closure.
	for rest != "" {
		chunk := cutBytes(rest, capEvent)
		rest = rest[len(chunk):]
		more := &wire.More{Text: chunk}
		t.put(more, ts, at, func(uint64) { more.Mid = mid })
	}
	end := &wire.More{End: true}
	t.put(end, ts, at, func(uint64) { end.Mid = mid })
}

// toolRun is a tool call seen starting through the sink: when, and how long it has waited for an answer to a question.
type toolRun struct {
	agent  string
	start  time.Time
	waited time.Duration
}

// applySink translates one queued sink call.
func (t *Translator) applySink(it *sinkItem) {
	ts := t.sessT(it.at)
	uid := t.seen(it.agent)
	if uid == "" {
		uid = "mgr" // a notice of the session itself
	}
	t.publishRoster()
	switch it.op {
	case opText:
		if t.agentOutOf(uid).service {
			return
		}
		t.textDelta(uid, it.text, ts)
	case opResponse:
		t.endMsg(uid, ts, false)
	case opReset:
		t.endMsg(uid, ts, true)
	case opNotice:
		t.notice(uid, it.level, it.text, ts, true)
	case opToolStart:
		t.toolStart(uid, it, ts)
	case opToolEnd:
		t.toolEnd(uid, it, ts, 0)
	}
	t.publishRoster()
}

// toolStart ends the agent's prose, shows the agent at the tool at once, and opens the code stream of a write.
func (t *Translator) toolStart(uid string, it *sinkItem, ts float64) {
	t.endMsg(uid, ts, false)
	if it.tid != "" {
		if len(t.d.runs) >= maxPend {
			for k := range t.d.runs {
				delete(t.d.runs, k)
				break
			}
		}
		t.d.runs[callKey(uid, it.tid)] = &toolRun{agent: uid, start: it.at}
	}
	if t.agentOutOf(uid).service {
		return
	}
	s := "tool"
	switch state.ToolStatus(it.tool) {
	case state.StatusEditing:
		s = "edit"
	case state.StatusWaiting:
		s = "wait"
	}
	doing := strings.TrimSpace(displayName(it.tool) + " " + t.summaryLine(it.summary))
	if s == "wait" {
		if a, ok := t.st.AgentLite(t.harnessID(uid)); ok && a.Line != "" {
			doing = a.Line
		} else {
			doing = "waits"
		}
	}
	st := &StateX{State: wire.State{ID: uid, S: s, Doing: line(doing, capDoing)}}
	if a, ok := t.st.AgentLite(t.harnessID(uid)); ok && a.Task != "" {
		task := a.Task
		st.Task = &task
	}
	if q := t.openQuestionOf(uid); q != nil {
		st.S, st.Doing = "ask", line("wants to run `"+q.Cmd+"`", capDoing)
	}
	t.offerState(uid, st, ts, false)
	t.d.hold[uid] = holdInfo{tid: it.tid, since: ts}
	if it.tool == "write" && it.content != nil && it.file != "" {
		m := &message{uid: uid, worker: true, code: true, lastFlush: ts}
		content := text(*it.content, capCode)
		e := &wire.Stream{ID: uid, Text: content, Rate: 400, Code: true, File: line(it.file, capPath)}
		t.put(e, ts, 0, func(seq uint64) {
			m.mid = "m" + strconv.FormatUint(seq, 10)
			e.Mid = m.mid
		})
		m.opened, m.sent = true, len(content)
		if it.tid != "" {
			t.d.codes[callKey(uid, it.tid)] = m
		} else {
			t.closeMsg(m, ts, false)
		}
	}
}

// toolEnd sends a finished tool call: a tool row (or a refuse row when nobody could answer its question), the diff of each file a
// write, edit or patch completed, and the end of its code stream. at is the real time of a history row.
func (t *Translator) toolEnd(uid string, it *sinkItem, ts float64, at int64) {
	if it.op == opToolEnd && !t.d.logOnly {
		// the state the tool's start gave is over: one still held back is not sent, and the log says what comes next
		if a := t.d.ags[uid]; a != nil && a.pend != nil && (a.pend.S == "tool" || a.pend.S == "edit" || a.pend.S == "wait") {
			a.pend = nil
		}
		if h, ok := t.d.hold[uid]; ok && h.tid == it.tid {
			delete(t.d.hold, uid)
		}
	}
	var ms, waited int64
	key := callKey(uid, it.tid)
	if r := t.d.runs[key]; r != nil && it.tid != "" {
		delete(t.d.runs, key)
		waited = r.waited.Milliseconds()
	}
	if it.took > 0 {
		ms = max(it.took.Milliseconds()-waited, 0)
	}
	if m := t.d.codes[key]; m != nil && it.tid != "" {
		delete(t.d.codes, key)
		t.closeMsg(m, ts, false)
	}
	if t.agentOutOf(uid).service {
		return
	}
	name := displayName(it.tool)
	arg := line(it.arg, capArg)
	var task string
	if a, ok := t.st.AgentLite(t.harnessID(uid)); ok {
		task = a.Task
	}
	if it.nobody {
		t.put(&wire.Refuse{ID: uid, Name: name, Arg: arg, Reason: line(it.reason, capReason)}, ts, at, nil)
		return
	}
	row := &ToolX{Tool: wire.Tool{ID: uid, Name: name, Arg: arg, Out: line(it.out, capOut), OK: !it.isErr, Task: task, TID: line(it.tid, capID)},
		Ms: ms, Waited: waited}
	if it.refused {
		row.Refused, row.Reason, row.Out = true, line(it.reason, capReason), ""
	}
	if !it.isErr && it.file != "" {
		row.File, row.Add, row.Del = line(it.file, capPath), it.add, it.del
		if it.tool == "write" && !it.created {
			row.Del = 0 // the previous line count is not known
		}
	}
	t.put(row, ts, at, nil)
	if it.isErr || t.d.logOnly {
		return
	}
	switch it.tool {
	case "write", "edit":
		if it.file != "" {
			t.put(&wire.Diff{File: line(it.file, capPath), Done: true}, ts, 0, nil)
		}
	case "apply_patch":
		for _, f := range it.files {
			t.put(&wire.Diff{File: line(f, capPath), Done: true}, ts, 0, nil)
		}
	}
}
