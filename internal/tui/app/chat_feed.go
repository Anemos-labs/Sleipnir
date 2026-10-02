package app

// What the session says while a turn runs (text, tool calls, notices) and how the answer reaches the scrollback.

import (
	"strings"

	"github.com/anemos-labs/sleipnir/internal/agent"
	"github.com/anemos-labs/sleipnir/internal/tui/cell"
)

// ---- what the session says while it works ----

func (m *chatModel) isMain(agent string) bool { return m.c.MainAgent == "" || agent == m.c.MainAgent }

func (m *chatModel) msg(x chatMsg) {
	switch x.kind {
	case mText:
		if m.isMain(x.agent) {
			m.stream.md.Append(x.text)
		}
	case mReset:
		if m.isMain(x.agent) {
			m.stream.md.Reset() // what was final stays in the scrollback; the attempt that failed leaves no more
			m.stream.printed = 0
		}
	case mResponse:
		if m.isMain(x.agent) {
			m.endStream()
		}
	case mToolStart:
		m.toolStart(x)
	case mToolEnd:
		m.toolEnd(x)
	case mNotice:
		m.notice(x)
	case mQuestion:
		m.ask(x.q)
	case mQuestionGone:
		m.dropQuestion(x.q)
	}
}

// settle takes everything the run said and logged before it returned. The run's goroutine sends a message, or emits an event,
// before it reports that it is over, and both are in their channels by then; but the program chooses among the channels that are
// ready, and could take the end first, and show a tool that has finished as one that never did.
func (m *chatModel) settle() {
	// What the run sent is in the channel already, and at most as much as it holds: a bound keeps a producer that never stops (an
	// agent of a swarm that is still talking) from keeping the program here.
	for i := 0; i < 2*linkBuffer; i++ {
		select {
		case x := <-m.c.Link.msgs:
			m.msg(x)
			continue
		default:
		}
		break
	}
	for i := 0; i < 2*eventBuffer && m.events != nil; i++ {
		select {
		case e, ok := <-m.events:
			if !ok {
				m.events = nil
				return
			}
			m.event(e)
			continue
		default:
		}
		break
	}
}

// eventBuffer is how many events the session's log holds for a subscriber before it drops one (cmd/sleipnir subscribes with it).
const eventBuffer = 4096

// drainMsgs takes what the session has said that is already waiting, so that a burst of it costs one frame.
func (m *chatModel) drainMsgs() {
	for i := 0; i < 128 && !m.over; i++ {
		select {
		case x := <-m.c.Link.msgs:
			m.msg(x)
		default:
			return
		}
	}
}

func (m *chatModel) drainEvents() {
	for i := 0; i < 512; i++ {
		select {
		case e, ok := <-m.events:
			if !ok {
				m.events = nil
				return
			}
			m.event(e)
		default:
			return
		}
	}
}

func (m *chatModel) notice(x chatMsg) {
	if strings.EqualFold(x.level, "info") && !m.c.Verbose {
		return
	}
	if strings.HasPrefix(x.text, agent.CacheMissNoticePrefix) {
		return // the log's cache.anomaly is drawn as a break (anomalyLines), with the same numbers and the cause: twice is noise
	}
	m.syncStream()
	m.block(bkNote, m.k.noticeLines(x.agent, x.level, x.text, m.cols, m.isMain(x.agent) || x.agent == ""))
}

func toolKey(agent, id string) string { return agent + "\x00" + id }

func (m *chatModel) toolStart(x chatMsg) {
	key := toolKey(x.agent, x.call.ToolID)
	t := &toolRun{key: key, id: x.call.ToolID, agent: x.agent, name: x.call.ToolName, input: x.call.Input, since: m.clock()}
	if _, dup := m.tools[key]; !dup {
		m.toolSeq = append(m.toolSeq, key)
	}
	m.tools[key] = t
}

func (m *chatModel) toolEnd(x chatMsg) {
	key := toolKey(x.agent, x.call.ToolID)
	if t, ok := m.tools[key]; ok {
		x.took = max(x.took-t.waited, 0) // what it took to do the work: the time a person was asked is not the tool's
		delete(m.tools, key)
		for i, k := range m.toolSeq {
			if k == key {
				m.toolSeq = append(m.toolSeq[:i:i], m.toolSeq[i+1:]...)
				break
			}
		}
	}
	m.printTool(doneTool{agent: x.agent, call: x.call, res: x.res, took: x.took, cwd: m.info.Cwd, worker: !m.isMain(x.agent)})
}

// printTool writes a finished call into the scrollback.
func (m *chatModel) printTool(t doneTool) {
	m.syncStream()
	lines, exp := m.k.toolLines(t, m.cols)
	if exp != nil {
		m.expand = append(m.expand, exp)
		if len(m.expand) > 16 {
			m.expand = m.expand[1:]
		}
	}
	kind := bkTool
	if len(lines) > 1 {
		kind = bkToolBody
	}
	m.block(kind, lines)
}

// flushTools writes the calls of the turn's own agent that never ended (the turn was cancelled under them) as cancelled, so that the
// scrollback does not lose them. The calls of a swarm's workers go on: a worker outlives the manager's turn, and says itself when its
// call has ended.
func (m *chatModel) flushTools(cancelled bool) {
	kept := m.toolSeq[:0:0]
	for _, key := range m.toolSeq {
		t := m.tools[key]
		if t == nil {
			continue
		}
		if !m.isMain(t.agent) {
			kept = append(kept, key)
			continue
		}
		res := toolResult{Failed: true, IsError: true, Text: "cancelled"}
		if !cancelled {
			res = toolResult{Text: ""}
		}
		m.printTool(doneTool{agent: t.agent, call: callOf(t), res: res, took: m.since(t.since), cwd: m.info.Cwd})
		delete(m.tools, key)
	}
	m.toolSeq = kept
}

// ---- the answer ----

// syncStream prints what of the answer is final and returns the rest, to be drawn in the live region.
func (m *chatModel) syncStream() []cell.Line {
	if m.stream.md.Len() == 0 {
		return nil
	}
	stable, tail := m.stream.md.Render(max(m.cols-2, 1), m.k.Theme)
	if len(stable) > m.stream.printed {
		m.printAnswer(m.k.answerLines(stable, m.stream.printed))
		m.stream.printed = len(stable)
	}
	if len(tail) == 0 {
		return nil
	}
	all := append(stable[:len(stable):len(stable)], tail...)
	return m.k.answerLines(all, m.stream.printed)
}

// endStream prints the rest of the answer: the message is over.
func (m *chatModel) endStream() {
	if m.stream.md.Len() == 0 {
		m.stream.printed = 0
		return
	}
	stable, tail := m.stream.md.Render(max(m.cols-2, 1), m.k.Theme)
	all := append(stable[:len(stable):len(stable)], tail...)
	lines := m.k.answerLines(all, m.stream.printed)
	for len(lines) > 0 && len(lines[len(lines)-1]) == 0 {
		lines = lines[:len(lines)-1]
	}
	m.printAnswer(lines)
	m.stream.md.Reset()
	m.stream.printed = 0
}

// printAnswer prints lines of the answer: a blank line before the first of a message, none inside it.
func (m *chatModel) printAnswer(lines []cell.Line) {
	if m.stream.printed > 0 {
		m.print(bkText, lines)
		return
	}
	m.block(bkText, lines)
}
