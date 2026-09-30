package traj

import (
	"encoding/json"
	"sort"

	"github.com/reee344/sleipnir/internal/core"
	"github.com/reee344/sleipnir/internal/rl"
)

// toolRun is one tool call of a kept main step with whatever came back. Signals
// are derived from these records.
type toolRun struct {
	agent   *agentInfo
	step    *stepInfo
	id      string
	name    string
	input   json.RawMessage
	invalid bool // the model's arguments were flagged unusable when the call was parsed
	has     bool // a result was found (event or block)
	isErr   bool
	text    string // exactly what the model saw
	meta    map[string]any
	ms      int64
	seq     uint64 // position in the log: the call event, else the step's response
}

// observations attaches each main step's tool results to it.
//
// A step's results live in the window between its response and the agent's next
// main request: the assistant turn, the tool.call / tool.result events and the
// turn that carries the tool_result blocks all fall there, and nothing of another
// step does. Within the window results are matched to the completion's tool_use
// blocks by tool id, in order, so a model that reuses ids across turns still
// matches correctly. The text of an observation is the tool_result block as it
// was appended to the thread, i.e. what the next prompt contains; a call that was
// never answered (a killed agent) gets no observation rather than an invented
// one.
func (b *builder) observations(a *agentInfo) {
	turns, calls, results := b.v.turns[a.id], b.v.toolCalls[a.id], b.v.toolResults[a.id]
	for _, st := range a.main {
		uses := st.turn.ToolCalls()
		if len(uses) == 0 {
			continue
		}
		lo, hi := st.p.seq, b.nextMainRequestSeq(a, st.q)
		callQ := map[string][]toolCallEv{}
		for k := sort.Search(len(calls), func(i int) bool { return calls[i].seq > lo }); k < len(calls) && calls[k].seq < hi; k++ {
			callQ[calls[k].id] = append(callQ[calls[k].id], calls[k])
		}
		resQ := map[string][]toolResultEv{}
		for k := sort.Search(len(results), func(i int) bool { return results[i].seq > lo }); k < len(results) && results[k].seq < hi; k++ {
			resQ[results[k].id] = append(resQ[results[k].id], results[k])
		}
		blkQ := map[string][]core.Block{}
		for k := sort.Search(len(turns), func(i int) bool { return turns[i].seq > lo }); k < len(turns) && turns[k].seq < hi; k++ {
			if turns[k].turn.Role != core.RoleUser {
				continue
			}
			for _, blk := range turns[k].turn.Blocks {
				if blk.Kind == core.BlockToolResult {
					blkQ[blk.ToolID] = append(blkQ[blk.ToolID], blk)
				}
			}
		}
		for _, u := range uses {
			run := toolRun{agent: a, step: st, id: u.ToolID, name: u.ToolName, input: u.Input, invalid: u.Invalid != "", seq: lo}
			obs := rl.Observation{ToolID: u.ToolID, Name: u.ToolName, Input: sanitizeInput(u.Input)}
			if q := callQ[u.ToolID]; len(q) > 0 {
				run.seq = q[0].seq
				callQ[u.ToolID] = q[1:]
			}
			if q := resQ[u.ToolID]; len(q) > 0 {
				res := q[0]
				resQ[u.ToolID] = q[1:]
				obs.Ms, obs.IsError, obs.Full = res.ms, res.isErr, res.full
				run.has, run.isErr, run.meta, run.ms = true, res.isErr, res.meta, res.ms
			}
			if q := blkQ[u.ToolID]; len(q) > 0 {
				blk := q[0]
				blkQ[u.ToolID] = q[1:]
				obs.Output = blk.PlainText()
				obs.IsError = obs.IsError || blk.IsError
				run.has, run.isErr, run.text = true, run.isErr || blk.IsError, obs.Output
			}
			b.runs = append(b.runs, run)
			if run.has {
				st.step.Observations = append(st.step.Observations, obs)
			}
		}
	}
}

// nextMainRequestSeq is the sequence number of the agent's first main request
// after q, kept or not; a step's tool results cannot outlive it.
func (b *builder) nextMainRequestSeq(a *agentInfo, q *reqInfo) uint64 {
	for _, x := range a.reqs {
		if x.kind == rl.KindMain && x.seq > q.seq {
			return x.seq
		}
	}
	return ^uint64(0)
}

// sanitizeInput keeps tool arguments that are JSON as they were and wraps
// anything else (arguments cut off by the output limit) as a JSON string, so an
// episode always marshals.
func sanitizeInput(raw json.RawMessage) json.RawMessage {
	if len(raw) == 0 {
		return nil
	}
	if json.Valid(raw) {
		return raw
	}
	b, _ := json.Marshal(string(raw))
	return b
}
