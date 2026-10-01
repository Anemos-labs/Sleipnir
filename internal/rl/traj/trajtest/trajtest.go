// Package trajtest builds synthetic recorded runs for tests of the RL pipeline.
//
// A Run writes events the way internal/agent and internal/swarm write them:
// model.request events carry a core.Manifest delta-encoded against the agent's
// previous main request, responses carry completion and token-trace blobs, and
// tool, mail, spawn, compaction and outcome events use the production payloads.
// Time is a fake clock, so runs are byte-for-byte reproducible, and the fake
// tokenizer is consistent by construction (a step's prompt ids start with the
// previous prompt ids, completion ids and observation ids) unless a test asks
// for a hot tail or a rebase, which break that property exactly as they do in
// real runs.
//
// The package deliberately does not import package traj, so tests of traj can use
// it: pass Events() and Blobs to traj.OpenWith.
package trajtest

import (
	"encoding/json"
	"fmt"
	"hash/fnv"
	"strings"
	"time"

	"github.com/anemos-labs/sleipnir/internal/core"
	"github.com/anemos-labs/sleipnir/internal/events"
)

// Run is a run being recorded.
type Run struct {
	Blobs *events.MemBlobs

	evs     []events.Event
	now     time.Time
	tick    time.Duration
	agents  map[string]*Agent
	mails   int
	tools   []core.ToolSpec
	system  string
	renderV string
}

// New returns an empty run whose clock starts at a fixed instant and advances 10ms
// per event.
func New() *Run {
	return &Run{
		Blobs:   events.NewMemBlobs(),
		now:     time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC),
		tick:    10 * time.Millisecond,
		agents:  map[string]*Agent{},
		system:  "You are Sleipnir, a careful coding agent.",
		renderV: "sleipnir-kv/1",
		tools: []core.ToolSpec{
			{Name: "edit", Description: "edit a file", InputSchema: json.RawMessage(`{"type":"object"}`)},
			{Name: "read", Description: "read a file", InputSchema: json.RawMessage(`{"type":"object"}`)},
		},
	}
}

// Events returns the events recorded so far.
func (r *Run) Events() []events.Event { return append([]events.Event(nil), r.evs...) }

// Advance moves the clock forward.
func (r *Run) Advance(d time.Duration) { r.now = r.now.Add(d) }

// Emit appends an event at the current time and returns its sequence number.
func (r *Run) Emit(agent, typ string, data any) uint64 {
	var raw json.RawMessage
	switch d := data.(type) {
	case nil:
	case json.RawMessage:
		raw = d
	default:
		b, err := json.Marshal(d)
		if err != nil {
			panic(err)
		}
		raw = b
	}
	r.now = r.now.Add(r.tick)
	e := events.Event{Seq: uint64(len(r.evs)) + 1, TS: r.now, Session: "trajtest", Agent: agent, Type: typ, Data: raw}
	r.evs = append(r.evs, e)
	return e.Seq
}

// Agent is one agent's thread and prompt state.
type Agent struct {
	run   *Run
	ID    string
	Role  string
	Model string

	preamble string
	turns    []core.Turn
	nextTurn core.TurnID
	reqN     int
	forkN    int
	man      core.ManifestState
	stream   []int32
	// Tokens turns token capture on: every response records a token trace.
	Tokens bool
	// Hot, when set, is appended (as an ephemeral block) to the last user message
	// of every main request, like the swarm's board view.
	Hot string
}

// Agent registers an agent without emitting an agent.spawn event (a single-agent
// run never has one).
func (r *Run) Agent(id, role, model string) *Agent {
	a := &Agent{run: r, ID: id, Role: role, Model: model, nextTurn: 1, preamble: "<shared-context>project</shared-context>"}
	r.agents[id] = a
	a.stream = Tokenize(a.preamble)
	return a
}

// Spawn registers a worker and emits agent.spawn the way swarm.Spawn does.
func (r *Run) Spawn(parent, id, role, model, task string) *Agent {
	a := r.Agent(id, role, model)
	r.Emit("swarm", events.TypeAgentSpawn, map[string]any{"id": id, "role": role, "task": task, "by": parent, "parent": parent, "model": model})
	return a
}

// SpawnRoot registers the manager, which is spawned without a parent.
func (r *Run) SpawnRoot(id, role, model string) *Agent {
	a := r.Agent(id, role, model)
	r.Emit("swarm", events.TypeAgentSpawn, map[string]any{"id": id, "role": role, "model": model})
	return a
}

// End emits agent.end.
func (r *Run) End(a *Agent, state string) {
	r.Emit("swarm", events.TypeAgentEnd, map[string]any{"id": a.ID, "state": state, "evidence": ""})
}

// Mail sends a message the way the swarm router does (mail.send from the sender,
// then mail.deliver attributed to the recipient) and returns its id. The text is
// also queued into the recipient's next user turn by DeliverMail.
func (r *Run) Mail(from, to, kind, text string) string {
	r.mails++
	id := fmt.Sprintf("m%d", r.mails)
	r.Emit(from, events.TypeMailSend, map[string]any{"id": id, "from": from, "to": to, "kind": kind, "text": text})
	r.Emit(to, events.TypeMailDeliver, map[string]any{"id": id, "from": from})
	return id
}

// EpochCommit emits the swarm's shared-layer epoch.
func (r *Run) EpochCommit(reason string) {
	r.Emit("swarm", events.TypeLayerCommit, map[string]any{"scope": "shared-epoch", "reason": reason, "hash": "abc123def456"})
}

// Outcome emits an outcome event.
func (r *Run) Outcome(kind string, pass bool, score float64) {
	r.Emit("", events.TypeOutcome, map[string]any{"kind": kind, "pass": pass, "score": score, "version": "v1", "ms": 42})
}

// Tokenize is the fake tokenizer: one id per whitespace-separated word and one
// per newline.
func Tokenize(s string) []int32 {
	var out []int32
	for _, line := range strings.Split(s, "\n") {
		for _, w := range strings.Fields(line) {
			h := fnv.New32a()
			h.Write([]byte(w))
			out = append(out, int32(1000+h.Sum32()%30000))
		}
		out = append(out, 7) // newline
	}
	return out
}

// User appends a user turn (a typed instruction) and emits its events.
func (a *Agent) User(text string) {
	t := a.push(core.Turn{Role: core.RoleUser, Origin: core.OriginUser, Blocks: []core.Block{core.Text(text)}})
	_ = t
	a.run.Emit(a.ID, events.TypeUserInput, map[string]any{"text": text})
	a.stream = append(a.stream, Tokenize(text)...)
}

func (a *Agent) push(t core.Turn) core.Turn {
	t.ID = a.nextTurn
	a.nextTurn++
	t.At = a.run.now
	a.turns = append(a.turns, t)
	a.run.Emit(a.ID, events.TypeTurnAppend, t)
	return t
}

// Call describes one main-thread step: a model call, and when Tool is set the tool
// execution that follows it.
type Call struct {
	Text    string
	Tool    string // "" ends the turn with a final answer
	ToolID  string // default call_<n>
	Input   string // JSON arguments (default {})
	Invalid string // marks the tool_use as unparseable (cut-off arguments)
	Result  string // what the tool returned to the model
	IsError bool
	Meta    map[string]any
	Stop    core.StopReason
	Usage   *core.Usage
	// Model overrides the agent's model for this call (a teacher).
	Model string
	// Fail makes the request error out: no response, a model.error instead.
	Fail bool
	// NoTokens skips the token trace for this call even when capture is on.
	NoTokens bool
	// BadTokens records a token trace that is not internally consistent.
	BadTokens bool
	// Mail texts are appended as text blocks to the tool-result turn, as the
	// swarm delivers mail.
	Mail []string
	// Thinking adds a reasoning block before the text; ReasoningWire, when set, is
	// its provider-native JSON (what the wire replays as reasoning_details).
	Thinking      string
	ReasoningWire string
}

// Step performs one call and returns the request id.
func (a *Agent) Step(c Call) string {
	r := a.run
	model := c.Model
	if model == "" {
		model = a.Model
	}
	a.reqN++
	req := fmt.Sprintf("%s.%d", a.ID, a.reqN)
	p := a.render(model, a.Hot)
	a.recordRequest(req, p, "main", a.Role, true)

	if c.Fail {
		r.Emit(a.ID, events.TypeModelError, map[string]any{"req": req, "error": "boom"})
		return req
	}
	blocks := []core.Block{}
	if c.Thinking != "" {
		tb := core.Block{Kind: core.BlockThinking, Text: c.Thinking}
		if c.ReasoningWire != "" {
			tb.Wire, tb.WireFormat = json.RawMessage(c.ReasoningWire), "openai-chat"
		}
		blocks = append(blocks, tb)
	}
	if c.Text != "" {
		blocks = append(blocks, core.Text(c.Text))
	}
	stop := c.Stop
	if c.Tool != "" {
		id := c.ToolID
		if id == "" {
			id = fmt.Sprintf("call_%s_%d", a.ID, a.reqN)
		}
		in := c.Input
		if in == "" {
			in = "{}"
		}
		// Like the provider adapter: arguments that are not JSON are kept as a JSON
		// string and the block is marked Invalid.
		input := json.RawMessage(in)
		if c.Invalid != "" {
			input = json.RawMessage(mustJSON(in))
		}
		blk := core.ToolUse(id, c.Tool, input)
		blk.Invalid = c.Invalid
		blk.Wire = json.RawMessage(fmt.Sprintf(`{"function":{"arguments":%s,"name":%q},"id":%q,"type":"function"}`, mustJSON(in), c.Tool, id))
		blk.WireFormat = "openai-chat"
		blocks = append(blocks, blk)
		if stop == "" {
			stop = core.StopToolUse
		}
	} else if stop == "" {
		stop = core.StopEnd
	}
	turn := core.Turn{Role: core.RoleAssistant, Blocks: blocks, Origin: core.OriginModel, Model: model}
	usage := core.Usage{InputTokens: len(a.stream) + len(Tokenize(a.Hot)), OutputTokens: len(Tokenize(c.Text)) + 2}
	if c.Usage != nil {
		usage = *c.Usage
	}
	compl := completionTurn(turn)
	extra := map[string]any{}
	if a.Tokens && !c.NoTokens {
		prompt := append([]int32(nil), a.stream...)
		if a.Hot != "" {
			prompt = append(prompt, Tokenize(a.Hot)...)
		}
		ids := Tokenize(c.Text + " " + c.Tool + " " + c.Input)
		lp := make([]float32, len(ids))
		for i := range lp {
			lp[i] = -0.25 - float32(i%5)*0.1
		}
		if c.BadTokens {
			lp = lp[:len(lp)-1]
		}
		tr := core.TokenTrace{Tokenizer: "fake", ModelVersion: model, PromptIDs: prompt, CompletionIDs: ids, Logprobs: lp}
		extra["tokens"] = a.putJSON(tr)
		a.stream = append(a.stream, ids...)
	}
	r.Emit(a.ID, events.TypeModelResponse, a.response(req, model, usage, stop, a.putJSON(compl), extra, false))
	a.push(withUsage(turn, usage))

	if c.Tool != "" {
		id := blocks[len(blocks)-1].ToolID
		r.Emit(a.ID, events.TypeToolCall, map[string]any{"id": id, "name": c.Tool, "input": blocks[len(blocks)-1].Input})
		r.Emit(a.ID, events.TypeToolResult, map[string]any{"id": id, "name": c.Tool, "error": c.IsError, "chars": len(c.Result), "truncated": false, "handle": "", "ms": 5, "meta": c.Meta})
		res := []core.Block{core.ToolResult(id, c.IsError, core.Text(c.Result))}
		for _, m := range c.Mail {
			res = append(res, core.Text(m))
		}
		a.push(core.Turn{Role: core.RoleUser, Origin: core.OriginTool, Blocks: res})
		a.stream = append(a.stream, Tokenize(c.Result)...)
		for _, m := range c.Mail {
			a.stream = append(a.stream, Tokenize(m)...)
		}
	}
	return req
}

// Fork records a compactor call: a side request forked from the agent's current
// prompt, answered with patch. It does not change the agent's thread.
func (a *Agent) Fork(patch string, model string) string {
	r := a.run
	if model == "" {
		model = a.Model
	}
	a.forkN++
	req := fmt.Sprintf("%s.c%d", a.ID, a.forkN)
	p := a.render(model, "")
	last := len(p.Messages) - 1
	p.Messages[last].Blocks = append(append([]core.Block(nil), p.Messages[last].Blocks...), core.Text("<compactor-task>fold</compactor-task>"))
	a.recordRequest(req, p, "compactor", "compactor", false)
	turn := core.Turn{Role: core.RoleAssistant, Blocks: []core.Block{core.Text(patch)}, Origin: core.OriginModel, Model: model}
	usage := core.Usage{InputTokens: len(a.stream) + 10, OutputTokens: len(Tokenize(patch))}
	extra := map[string]any{}
	if a.Tokens {
		prompt := append(append([]int32(nil), a.stream...), Tokenize("<compactor-task>fold</compactor-task>")...)
		ids := Tokenize(patch)
		lp := make([]float32, len(ids))
		for i := range lp {
			lp[i] = -0.5
		}
		extra["tokens"] = a.putJSON(core.TokenTrace{Tokenizer: "fake", ModelVersion: model, PromptIDs: prompt, CompletionIDs: ids, Logprobs: lp})
	}
	r.Emit(a.ID, events.TypeModelResponse, a.response(req, model, usage, core.StopEnd, a.putJSON(completionTurn(turn)), extra, true))
	return req
}

// Commit applies a compaction: it emits compact.commit and layer.commit, replaces
// the thread's older turns by a one-line spine in the preamble and keeps the
// turns from keepFrom on. The next request is a declared rebase (a new segment).
func (a *Agent) Commit(keepFrom core.TurnID, fallback bool, reason string) {
	r := a.run
	var kept []core.Turn
	for _, t := range a.turns {
		if t.ID >= keepFrom {
			kept = append(kept, t)
		}
	}
	a.turns = kept
	a.preamble = a.preamble + "\n<history>folded</history>"
	r.Emit(a.ID, events.TypeCompactCommit, map[string]any{"reason": reason, "keep_from": keepFrom, "removed_turns": 3, "fallback": fallback})
	r.Emit(a.ID, events.TypeLayerCommit, map[string]any{"scope": "agent", "spine": "s1", "spine_version": 1})
	a.stream = Tokenize(a.preamble)
	for _, t := range a.turns {
		a.stream = append(a.stream, Tokenize(plain(t))...)
	}
}

// SyncShared emits the agent-side pickup of a shared epoch (a rebase without a
// compaction).
func (a *Agent) SyncShared(shared string) {
	a.preamble = "<shared-context>" + shared + "</shared-context>"
	a.run.Emit(a.ID, events.TypeLayerCommit, map[string]any{"scope": "shared-sync", "reason": "test", "shared": "x", "role": "y"})
	a.stream = Tokenize(a.preamble)
	for _, t := range a.turns {
		a.stream = append(a.stream, Tokenize(plain(t))...)
	}
}

func plain(t core.Turn) string {
	var sb strings.Builder
	for _, b := range t.Blocks {
		sb.WriteString(b.PlainText())
		sb.WriteByte(' ')
	}
	return sb.String()
}

func (a *Agent) render(model, hot string) *core.Prompt {
	p := &core.Prompt{Model: model, Tools: a.run.tools, System: []core.Block{core.Text(a.run.system)}}
	pre := core.Message{Role: core.RoleUser, Blocks: []core.Block{core.Text(a.preamble)}}
	msgs := []core.Message{pre}
	i := 0
	if len(a.turns) > 0 && a.turns[0].Role == core.RoleUser {
		msgs[0].Blocks = append(msgs[0].Blocks, a.turns[0].Blocks...)
		i = 1
	}
	for ; i < len(a.turns); i++ {
		msgs = append(msgs, core.Message{Role: a.turns[i].Role, Blocks: append([]core.Block(nil), a.turns[i].Blocks...), Turn: a.turns[i].ID})
	}
	if hot != "" {
		blk := core.Text(hot)
		blk.Ephemeral = true
		last := len(msgs) - 1
		if msgs[last].Role == core.RoleUser {
			msgs[last].Blocks = append(msgs[last].Blocks, blk)
		} else {
			msgs = append(msgs, core.Message{Role: core.RoleUser, Blocks: []core.Block{blk}})
		}
	}
	p.Messages = msgs
	return p
}

func (a *Agent) recordRequest(req string, p *core.Prompt, kind, role string, advance bool) {
	man, next, err := core.BuildManifest(p, a.man, req, func(b []byte) (core.Hash, error) { return a.run.Blobs.Put(b) })
	if err != nil {
		panic(err)
	}
	if advance {
		a.man = next
	}
	hot := core.Hash("")
	if a.Hot != "" && kind == "main" {
		h, _ := a.run.Blobs.Put([]byte(a.Hot + "\n"))
		hot = h
	}
	a.run.Emit(a.ID, events.TypeModelRequest, map[string]any{
		"req": req, "agent": a.ID, "role": role, "kind": kind, "model": p.Model, "provider": "fake", "dialect": "openai-chat",
		"hot": hot, "params": map[string]any{"max_tokens": 512}, "renderer": a.run.renderV, "manifest": man, "wire_hash": man.Wire,
		"tools": len(p.Tools),
	})
}

func (a *Agent) response(req, model string, u core.Usage, stop core.StopReason, completion core.Hash, extra map[string]any, side bool) map[string]any {
	m := map[string]any{
		"req": req, "id": "gen-" + req, "model": model, "provider": "fake", "usage": u, "cost_usd": float64(u.InputTokens+u.OutputTokens) / 1e6,
		"hit_ratio": u.HitRatio(), "stop": stop, "total_ms": 12, "completion": completion,
	}
	if side {
		m["side"] = true
		delete(m, "total_ms")
	}
	for k, v := range extra {
		m[k] = v
	}
	return m
}

// completionTurn is what the agent stores in the completion blob: the provider's
// turn as returned, before the thread stamps ids and usage on it.
func completionTurn(t core.Turn) core.Turn { return t }

func withUsage(t core.Turn, u core.Usage) core.Turn {
	t.Usage = &u
	return t
}

func (a *Agent) putJSON(v any) core.Hash {
	b, err := core.MarshalStable(v)
	if err != nil {
		panic(err)
	}
	h, _ := a.run.Blobs.Put(b)
	return h
}

func mustJSON(s string) string {
	b, _ := json.Marshal(s)
	return string(b)
}
