// Package statetest holds the helpers that tests of the terminal UI share: a builder of hand-made event sequences, and the
// recording of a real session, made by the repository's own scripted team (internal/demo) against its mock provider. The
// packages that draw from a state (render, widget, the screens) use them to get events in a known condition without a model and
// without a clock.
//
// It imports only internal/events, and in particular not the state package, so that the tests of that package can use it.
package statetest

import (
	"encoding/json"
	"time"

	"github.com/reee344/sleipnir/internal/events"
)

// Epoch is the time a Builder starts at unless told otherwise.
var Epoch = time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)

// Builder makes a sequence of events by hand: consecutive seq numbers from 1, and timestamps that only move when the test says so
// (At, Advance), so that every duration and every time in an assertion is written out by the test and not measured.
type Builder struct {
	Session string
	seq     uint64
	t       time.Time
	evs     []events.Event
}

// NewBuilder returns a Builder whose clock is Epoch.
func NewBuilder() *Builder { return &Builder{Session: "test", t: Epoch} }

// At sets the time of the next events.
func (b *Builder) At(t time.Time) *Builder { b.t = t; return b }

// Advance moves the time of the next events forward.
func (b *Builder) Advance(d time.Duration) *Builder { b.t = b.t.Add(d); return b }

// Now is the time the next event will carry.
func (b *Builder) Now() time.Time { return b.t }

// Seq is the seq of the last event made (0 before any).
func (b *Builder) Seq() uint64 { return b.seq }

// Events returns the events made so far.
func (b *Builder) Events() []events.Event { return append([]events.Event(nil), b.evs...) }

// Emit makes an event of the type for the agent with data marshalled as its payload (nil: no payload), adds it to the sequence and
// returns it.
func (b *Builder) Emit(agent, typ string, data any) events.Event {
	var raw json.RawMessage
	if data != nil {
		var err error
		if raw, err = json.Marshal(data); err != nil {
			panic("statetest: " + err.Error())
		}
	}
	return b.add(agent, typ, raw)
}

// Raw is Emit with the payload given as text, valid JSON or not: for events that are garbage on purpose.
func (b *Builder) Raw(agent, typ, data string) events.Event {
	return b.add(agent, typ, json.RawMessage(data))
}

func (b *Builder) add(agent, typ string, raw json.RawMessage) events.Event {
	b.seq++
	e := events.Event{Seq: b.seq, TS: b.t, Session: b.Session, Agent: agent, Type: typ, Data: raw}
	b.evs = append(b.evs, e)
	return e
}

// Sec is one section of a request.
type Sec struct {
	Name   string
	Tokens int
	BP     bool
	// Hash is the layer's hash; empty makes one from the name, so two requests with a section of the same name share a layer.
	Hash string
}

// Request makes a model.request for the agent: a main request for the model, with the sections given (the harness writes shared,
// role, notes and spine), carrying the prefix key.
func (b *Builder) Request(agent, req, model, prefixKey string, secs ...Sec) events.Event {
	return b.Emit(agent, events.TypeModelRequest, RequestPayload(agent, req, model, prefixKey, secs...))
}

// RequestPayload is the payload of a main model.request.
func RequestPayload(agent, req, model, prefixKey string, secs ...Sec) map[string]any {
	var ss []map[string]any
	for _, s := range secs {
		h := s.Hash
		if h == "" {
			h = s.Name + "-hash-0123456789abcdef"
		}
		m := map[string]any{"name": s.Name, "hash": h, "tokens": s.Tokens}
		if s.BP {
			m["bp"] = true
		}
		ss = append(ss, m)
	}
	return map[string]any{"req": req, "agent": agent, "kind": "main", "model": model, "provider": "test", "dialect": "openai-chat",
		"sections": ss, "thread_from": 1, "thread_to": 3, "prefix_key": prefixKey, "cache_key": "sl:test:" + prefixKey, "tools": 17,
		"shared_blocks": 0, "shared_tokens": 0, "renderer": "sleipnir-kv/2"}
}

// Response makes a model.response for the agent: the usage, in tokens, and the cost the provider reported.
func (b *Builder) Response(agent, req, model string, in, read, write, out int, costUSD float64) events.Event {
	return b.Emit(agent, events.TypeModelResponse, ResponsePayload(req, model, in, read, write, out, costUSD))
}

// ResponsePayload is the payload of a main model.response that stopped for a tool call.
func ResponsePayload(req, model string, in, read, write, out int, costUSD float64) map[string]any {
	return map[string]any{"req": req, "model": model, "provider": "test", "stop": "tool_use", "cost_usd": costUSD, "hit_ratio": 0,
		"usage":         map[string]any{"input_tokens": in, "cache_read_tokens": read, "cache_write_5m_tokens": write, "cache_write_1h_tokens": 0, "output_tokens": out},
		"expected_read": 0, "missed": 0, "anomaly": false}
}

// Call makes a tool.call for the agent.
func (b *Builder) Call(agent, id, name string, input map[string]any) events.Event {
	return b.Emit(agent, events.TypeToolCall, map[string]any{"id": id, "name": name, "input": input})
}

// Result makes the tool.result that answers a call.
func (b *Builder) Result(agent, id, name string, failed bool, ms int) events.Event {
	return b.Emit(agent, events.TypeToolResult, map[string]any{"id": id, "name": name, "error": failed, "chars": 10, "ms": ms})
}

// Spawn makes an agent.spawn.
func (b *Builder) Spawn(id, role, task, parent string) events.Event {
	return b.Emit("swarm", events.TypeAgentSpawn, map[string]any{"id": id, "role": role, "task": task, "by": parent, "parent": parent, "model": "m"})
}

// PermAsk makes the perm.ask the permission engine's audit writes (internal/session permaudit.go) when it has to put a question: the
// tool, why it asks, the command of a shell call, the paths (at most five) and the role. Like the producer it leaves out what is
// empty (command, paths, role) and never writes an id: a question and its answer are told together by who, tool, command and paths.
func (b *Builder) PermAsk(agent, role, tool, command, reason string, paths ...string) events.Event {
	return b.Emit(agent, events.TypePermAsk, PermPayload(role, tool, command, reason, paths))
}

// PermDecide makes the perm.decide that settles a question, or that refuses a request without one (by "policy"): the fields of the
// question, and allow, by ("user", "no one", "policy" or "canceled") and, for an answer that is kept as a rule, remember ("session"
// or "project").
func (b *Builder) PermDecide(agent, role, tool, command, reason string, allow bool, by, remember string, paths ...string) events.Event {
	d := PermPayload(role, tool, command, reason, paths)
	d["allow"], d["by"] = allow, by
	if remember != "" {
		d["remember"] = remember
	}
	return b.Emit(agent, events.TypePermDecide, d)
}

// PermPayload is the payload of a perm.ask (and the part of a perm.decide's that is the same).
func PermPayload(role, tool, command, reason string, paths []string) map[string]any {
	d := map[string]any{"tool": tool, "reason": reason}
	if command != "" {
		d["command"] = command
	}
	if len(paths) > 0 {
		d["paths"] = paths
	}
	if role != "" {
		d["role"] = role
	}
	return d
}

// Cancel makes the agent.cancel that an agent's run writes as its last act when its context was cancelled (internal/agent agent.go):
// phase is "model", "tools" or "between" (what the run was doing), cause "canceled" or "deadline", steps the model answers it had
// finished.
func (b *Builder) Cancel(agent, phase, cause string, steps int) events.Event {
	return b.Emit(agent, events.TypeAgentCancel, map[string]any{"phase": phase, "cause": cause, "steps": steps})
}
