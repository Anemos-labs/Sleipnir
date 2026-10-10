package translate

import (
	"encoding/json"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/anemos-labs/sleipnir/internal/agent"
	"github.com/anemos-labs/sleipnir/internal/core"
	"github.com/anemos-labs/sleipnir/internal/perm"
	"github.com/anemos-labs/sleipnir/internal/provider"
	"github.com/anemos-labs/sleipnir/internal/tools"
	"github.com/anemos-labs/sleipnir/internal/tui/state"
)

// The sink side: agent.Sink calls arrive on the agents' goroutines and must never block (internal/agent agent.go). Each call does a
// bounded amount of work to keep what the page needs of it (a tool result's first line, its counts; a write's content up to the code
// stream's cap) and puts an item in the tab's bounded queue; the translator's goroutine drains the queue.

// sinkOp is what a queued sink call was.
type sinkOp uint8

const (
	opText sinkOp = iota + 1
	opToolStart
	opToolEnd
	opResponse
	opNotice
	opReset
)

// sinkItem is one queued sink call, reduced to what the translation needs.
type sinkItem struct {
	op    sinkOp
	agent string
	at    time.Time
	text  string // a text delta, a notice's message
	level string // a notice's level

	// a tool call
	tid, tool string
	summary   string // state.ToolSummary of the call
	arg       string // the row's argument, raw
	file      string // the file of a write, edit or apply_patch, project-relative
	files     []string
	content   *string // a write's content when it fits the code stream

	// a tool result
	isErr          bool
	out            string // the first line, or "<n> lines"
	reason         string // the start of an error's text
	refused        bool   // a refusal (permission, lease, scope, hook, too many calls)
	nobody         bool   // a refusal for want of anyone to answer
	add, del       int
	created, plain bool // write: the file was new; the result carried counts
	took           time.Duration
	spawned        string
}

// sinkQueue is the bounded queue of sink items: at most maxItems items and maxBytes bytes of text. Text deltas of one agent that
// follow each other are merged; when it is full, the oldest text items are dropped first, then the oldest items, and the loss is
// counted.
type sinkQueue struct {
	mu       sync.Mutex
	items    []sinkItem
	bytes    int
	maxItems int
	maxBytes int
	lost     int
	closed   bool
	wake     chan struct{}
}

// newSinkQueue returns a queue with the bounds; wake is signalled (without blocking) when an item is added.
func newSinkQueue(maxItems, maxBytes int, wake chan struct{}) *sinkQueue {
	return &sinkQueue{maxItems: maxItems, maxBytes: maxBytes, wake: wake}
}

// itemBytes is what an item counts against the byte bound.
func itemBytes(it *sinkItem) int {
	n := len(it.text) + len(it.out) + len(it.reason) + len(it.arg) + len(it.summary) + 64
	if it.content != nil {
		n += len(*it.content)
	}
	return n
}

// push queues an item without blocking.
func (q *sinkQueue) push(it sinkItem) {
	q.mu.Lock()
	if q.closed {
		q.mu.Unlock()
		return
	}
	if it.op == opText {
		if n := len(q.items); n > 0 {
			last := &q.items[n-1]
			if last.op == opText && last.agent == it.agent && len(last.text) < capEvent {
				last.text += it.text
				q.bytes += len(it.text)
				q.trim()
				q.mu.Unlock()
				q.signal()
				return
			}
		}
	}
	q.items = append(q.items, it)
	q.bytes += itemBytes(&it)
	q.trim()
	q.mu.Unlock()
	q.signal()
}

// trim drops items while a bound is exceeded, down to seven eighths of it so that a full queue costs constant time per push on
// average: the oldest text deltas first, then the oldest items.
func (q *sinkQueue) trim() {
	if len(q.items) <= q.maxItems && (q.bytes <= q.maxBytes || len(q.items) <= 1) {
		return
	}
	targetN, targetB := q.maxItems*7/8, q.maxBytes*7/8
	n, b := len(q.items), q.bytes
	need := func() bool { return n > targetN || (b > targetB && n > 1) }
	drop := make([]bool, len(q.items))
	for pass := 0; pass < 2 && need(); pass++ {
		for i := range q.items {
			if !need() {
				break
			}
			if drop[i] || (pass == 0 && q.items[i].op != opText) {
				continue
			}
			drop[i] = true
			n--
			b -= itemBytes(&q.items[i])
		}
	}
	kept := q.items[:0]
	for i := range q.items {
		if drop[i] {
			q.lost++
			continue
		}
		kept = append(kept, q.items[i])
	}
	for i := len(kept); i < len(q.items); i++ {
		q.items[i] = sinkItem{}
	}
	q.items, q.bytes = kept, b
}

// signal wakes the drain without blocking.
func (q *sinkQueue) signal() {
	select {
	case q.wake <- struct{}{}:
	default:
	}
}

// take returns the queued items and how many were lost since the last take, and empties the queue.
func (q *sinkQueue) take() ([]sinkItem, int) {
	q.mu.Lock()
	defer q.mu.Unlock()
	items, lost := q.items, q.lost
	q.items, q.bytes, q.lost = nil, 0, 0
	return items, lost
}

// close makes later pushes do nothing.
func (q *sinkQueue) close() {
	q.mu.Lock()
	q.closed = true
	q.items, q.bytes = nil, 0
	q.mu.Unlock()
}

// sink is the agent.Sink (and agent.Resetter) of one tab generation; fixed, when set, is the agent every call is attributed to (the
// per-agent sinks of NewSink).
type sink struct {
	t     *Translator
	fixed string
}

var (
	_ agent.Sink     = (*sink)(nil)
	_ agent.Resetter = (*sink)(nil)
)

// who is the agent a call is about.
func (s *sink) who(agentID string) string {
	if s.fixed != "" && agentID == "" {
		return s.fixed
	}
	return agentID
}

// Text queues a delta of an answer's text.
func (s *sink) Text(agentID, delta string) {
	if delta == "" {
		return
	}
	s.t.sq.push(sinkItem{op: opText, agent: s.who(agentID), at: s.t.now(), text: delta})
}

// Thinking ignores reasoning deltas: the vocabulary has no event for them.
func (s *sink) Thinking(string, string) {}

// ToolStart queues the start of a tool call with its summary, its row's argument and, for a write, the content shown as code.
func (s *sink) ToolStart(agentID string, call core.Block) {
	it := sinkItem{op: opToolStart, agent: s.who(agentID), at: s.t.now(), tid: call.ToolID, tool: call.ToolName}
	in := decodeInput(call.Input)
	root := s.t.cfg.Root
	it.summary = state.ToolSummary(call.ToolName, call.Input)
	it.arg = argOf(root, call.ToolName, &in, "")
	switch call.ToolName {
	case "write":
		it.file = pathOf(root, &in)
		if in.Content != nil && len(*in.Content) <= capCode {
			c := *in.Content
			it.content = &c
		}
	case "edit":
		it.file = pathOf(root, &in)
	case "apply_patch":
		it.files = patchFiles(in.Patch, in.Input)
	}
	s.t.sq.push(it)
}

// ToolEnd queues a finished tool call: its outcome, the first line of its output, its counts, and whether it was refused.
func (s *sink) ToolEnd(agentID string, call core.Block, res *tools.Result, took time.Duration) {
	it := sinkItem{op: opToolEnd, agent: s.who(agentID), at: s.t.now(), tid: call.ToolID, tool: call.ToolName, took: took}
	in := decodeInput(call.Input)
	root := s.t.cfg.Root
	var txt string
	var meta map[string]any
	if res != nil {
		txt, meta, it.isErr = res.Text, res.Meta, res.IsError
	}
	if call.ToolName == "spawn" && !it.isErr {
		it.spawned = spawnedIn(txt)
	}
	it.arg = argOf(root, call.ToolName, &in, it.spawned)
	resultDetails(&it, root, call.ToolName, &in, txt, meta)
	s.t.sq.push(it)
}

// resultDetails fills a tool item from the call and its result: the output line, the refusal, the file and its counts.
func resultDetails(it *sinkItem, root, tool string, in *callInput, txt string, meta map[string]any) {
	kind, _ := meta["error_kind"].(string)
	if it.isErr {
		switch {
		case perm.IsNoOneToAsk(txt) || strings.Contains(txt, "nobody answered within"):
			it.refused, it.nobody = true, true
		case kind == "permission", kind == "lease", kind == "scope", kind == "hook", kind == "too_many_calls":
			it.refused = true
		case meta["refused"] == true:
			it.refused = true
		}
		it.reason = cutBytes(txt, 2*capReason+64)
	}
	switch n := countLines(txt); {
	case n > 1 && !it.isErr:
		it.out = itoa(n) + " lines"
	default:
		it.out = cutBytes(firstLine(txt), 2*capOut+64)
	}
	switch tool {
	case "edit":
		it.file = relPath(root, firstNonEmpty(str(meta["path"]), pathOf(root, in)))
		it.add, it.del, it.plain = num(meta["added"]), num(meta["removed"]), true
	case "write":
		it.file = relPath(root, firstNonEmpty(str(meta["path"]), pathOf(root, in)))
		if in.Content != nil {
			it.add = countLines(*in.Content)
		}
		it.created, _ = meta["created"].(bool)
		it.plain = true
	case "apply_patch":
		if fs, ok := meta["files"].([]any); ok {
			for _, f := range fs {
				if s := str(f); s != "" {
					it.files = append(it.files, relPath(root, s))
				}
			}
		} else if fs, ok := meta["files"].([]string); ok {
			for _, f := range fs {
				it.files = append(it.files, relPath(root, f))
			}
		}
		if len(it.files) == 0 {
			it.files = patchFiles(in.Patch, in.Input)
		}
		if len(it.files) > 0 {
			it.file = it.files[0]
		}
		it.add, it.del, it.plain = num(meta["added"]), num(meta["removed"]), true
	}
}

// Response ends the open message of the agent's answer.
func (s *sink) Response(agentID string, _ *provider.Response, _ float64) {
	s.t.sq.push(sinkItem{op: opResponse, agent: s.who(agentID), at: s.t.now()})
}

// Notice queues a notice of the harness.
func (s *sink) Notice(agentID, level, msg string) {
	if msg == "" {
		return
	}
	s.t.sq.push(sinkItem{op: opNotice, agent: s.who(agentID), at: s.t.now(), level: level, text: cutBytes(msg, 4*capReason)})
}

// Reset says a request of the agent is being sent again: the open message ends where it is.
func (s *sink) Reset(agentID string) {
	s.t.sq.push(sinkItem{op: opReset, agent: s.who(agentID), at: s.t.now()})
}

// itoa writes a non-negative count.
func itoa(n int) string { return strconv.Itoa(n) }

// str is v when it is a string.
func str(v any) string {
	s, _ := v.(string)
	return s
}

// num is v as an int when it is a number (a JSON number decodes as float64; a Go caller may put an int).
func num(v any) int {
	switch n := v.(type) {
	case float64:
		if n > 0 && n < 1<<31 {
			return int(n)
		}
	case int:
		if n > 0 {
			return n
		}
	case int64:
		if n > 0 && n < 1<<31 {
			return int(n)
		}
	case json.Number:
		if i, err := n.Int64(); err == nil && i > 0 && i < 1<<31 {
			return int(i)
		}
	}
	return 0
}
