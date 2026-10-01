package main

import (
	"encoding/json"
	"fmt"
	"sort"
	"time"
	"unicode/utf8"

	"github.com/anemos-labs/sleipnir/internal/tui/app"
)

// The time of a recorded chat is designed, not measured. `sleipnir chat-record` runs the real session as fast as the machine runs it
// and writes down what happened in the order it happened; this file says when. A model has a speed and a person has one, and a
// transcript that took its times from the clock of a busy machine would show that machine's afternoon (a recording once stretched to
// three minutes because the machine was) instead of a session. So the session has no latency at all while it is recorded, the order of
// everything is the session's own, and the time of each record follows from what the record is:
//
//   - a model answers after FirstToken, plus PrefillPer for every token of the prompt that the provider had not cached (so the first
//     answer of a session, and the one after the provider lost its cache, are slower than the others, as they are), and then gives a
//     piece of the reply every Decode, the arguments of a tool call included;
//   - a person types one key every Key, and the pauses of the person's script (reading a banner, a question, an answer) are the
//     script's;
//   - a tool takes the time the real one took, held between ToolMin and ToolMax (a command cannot take no time, and a machine that
//     stalled for a minute is not what the session was);
//   - the model call of a compaction, which runs beside the agent, answers Compactor after it was asked.
//
// Everything else is real: what is said, in what order, and the numbers the harness works out from it. The pace is a function of the
// records (paceChat), so the same session always has the same times.

// chatPace is how fast the model and the person of the recorded session are.
type chatPace struct {
	Attach     time.Duration // the session is made
	Key        time.Duration // between two keys
	FirstToken time.Duration // a model's answer begins this long after the request
	PrefillPer time.Duration // and this much more for every token of the prompt that was not read from the cache
	Decode     time.Duration // a piece of the reply comes every so often (24 characters of text, 16 of the arguments of a call)
	Compactor  time.Duration // the call of a compaction takes this long
	ToolMin    time.Duration // the real time of a tool is held between these
	ToolMax    time.Duration
}

// defaultChatPace is a fast model on a good day: the first token after 600 ms, 10k tokens of prompt a second to prefill, about 530
// characters of reply a second.
var defaultChatPace = chatPace{
	Attach: 320 * time.Millisecond, Key: 55 * time.Millisecond,
	FirstToken: 600 * time.Millisecond, PrefillPer: 100 * time.Microsecond, Decode: 45 * time.Millisecond, Compactor: 1400 * time.Millisecond,
	ToolMin: time.Millisecond, ToolMax: 2500 * time.Millisecond,
}

// argPiece is how many runes of the arguments of a call the endpoint sends in one chunk.
const argPiece = 16

// A rawRec is what the recorder has before it has a time: a record, or a pause of the person's script (a pause is no record).
type rawRec struct {
	rec   app.ChatRecord
	pause time.Duration
}

// A phase is one request of the main thread: from the event that says it was sent to the one that says it was answered.
type phase struct {
	ttft  time.Duration // from the request to the first piece of the answer
	after time.Duration // from the last piece of text to the end of the answer: the calls, which come after the words
}

// evInfo is what the pace reads of an event's payload.
type evInfo struct {
	Req      string `json:"req"`
	Kind     string `json:"kind"`
	Stage    string `json:"stage"`
	Decision string `json:"decision"`
	Usage    struct {
		Input int `json:"input_tokens"`
	} `json:"usage"`
}

func infoOf(r *app.ChatRecord) evInfo {
	var i evInfo
	if r.Event != nil {
		_ = json.Unmarshal(r.Event.Data, &i)
	}
	return i
}

// scanPhases finds the requests of the main thread and works out how long each answer takes, from what the answer was: its prompt
// that the cache did not hold, and the calls that came after its words. The result is by the index of the request's event.
func scanPhases(raw []rawRec, p chatPace) map[int]*phase {
	open := map[string]int{} // request id -> index of its request event
	out := map[int]*phase{}
	for i := range raw {
		r := &raw[i].rec
		if raw[i].pause != 0 || r.Kind != app.RecEvent {
			continue
		}
		switch in := infoOf(r); r.Event.Type {
		case "model.request":
			if in.Kind == "main" {
				open[in.Req] = i
				out[i] = &phase{ttft: p.FirstToken} // a request that is never answered (a cancelled turn) still has a first token
			}
		case "model.response":
			start, ok := open[in.Req]
			if !ok {
				continue // the answer of a request that is not the main thread's
			}
			ph := out[start]
			ph.ttft = p.FirstToken + time.Duration(in.Usage.Input)*p.PrefillPer
			// After the last piece of text the endpoint pauses once, and then sends each call in a chunk of its own and the pieces of its
			// arguments, a chunk each.
			emits := 1
			for j := i + 1; j < len(raw) && raw[j].pause == 0; j++ {
				k := &raw[j].rec
				if k.Kind == app.RecToolStart {
					emits += 1 + (utf8.RuneCount(k.Call.Input)+argPiece-1)/argPiece
					continue
				}
				if k.Kind != app.RecEvent && k.Kind != app.RecResponse && k.Kind != app.RecNotice {
					break
				}
			}
			ph.after = time.Duration(emits) * p.Decode
		}
	}
	return out
}

// pacer is paceChat's state.
type pacer struct {
	p      chatPace
	phases map[int]*phase
	out    []app.ChatRecord
	last   int64         // the time of the last record written, in ms: times never go backwards
	t      time.Duration // now
	starts map[string]time.Duration

	cur      *phase        // the request that is open: sent, and not yet answered in full
	t0       time.Duration // when it was sent
	first    bool          // its first piece has been placed
	answered bool          // its end has been placed

	// events of a compaction's model call that wait for the time the call answers, ordered by it
	held   []heldRec
	bgPlan time.Duration // when the compaction was planned
	bgReq  string        // the id of its model call
}

type heldRec struct {
	rec app.ChatRecord
	due time.Duration
}

// place writes a record at a time, after the records that were waiting for an earlier one.
func (z *pacer) place(rec app.ChatRecord, at time.Duration) {
	for len(z.held) > 0 && z.held[0].due <= at {
		h := z.held[0]
		z.held = z.held[1:]
		z.write(h.rec, h.due)
	}
	z.write(rec, at)
}

func (z *pacer) write(rec app.ChatRecord, at time.Duration) {
	rec.T = max(at.Milliseconds(), z.last)
	z.last = rec.T
	z.out = append(z.out, rec)
}

func (z *pacer) hold(rec app.ChatRecord, due time.Duration) {
	z.held = append(z.held, heldRec{rec, due})
	sort.SliceStable(z.held, func(i, j int) bool { return z.held[i].due < z.held[j].due })
}

// endAnswer places the end of the answer of the open request, if it has not been: the first piece of it if no words came, and what the
// endpoint sends after the last of them.
func (z *pacer) endAnswer() {
	if z.cur == nil || z.answered {
		return
	}
	if !z.first {
		z.t = z.t0 + z.cur.ttft
		z.first = true
	}
	z.t += z.cur.after
	z.answered = true
}

// canonicalOrder is raw with one race taken out of it. When the planner decides to start a compaction (a compact.plan event, from the
// main thread), the compaction goes on in a goroutine of its own, which emits its request to the model and the patch's request; and the
// main thread goes on to build its next request. Which gets into the log first is up to the scheduler, and on a busy machine the
// goroutine can be so late that the main thread has begun to stream its answer. Nothing a person sees depends on it, but a transcript
// that said one thing on one run and another on the next would not be the same story twice. So the compaction's request and its
// patch's come directly after the plan that started it, wherever they landed, which is where they belong in the story.
func canonicalOrder(raw []rawRec) []rawRec {
	isEvent := func(r rawRec, typ string) bool {
		return r.pause == 0 && r.rec.Kind == app.RecEvent && r.rec.Event.Type == typ
	}
	isPlan := func(r rawRec) bool { return isEvent(r, "compact.plan") && infoOf(&r.rec).Decision == "start" }
	isCompactorRequest := func(r rawRec) bool { return isEvent(r, "model.request") && infoOf(&r.rec).Kind == "compactor" }
	isPatchRequest := func(r rawRec) bool { return isEvent(r, "compact.patch") && infoOf(&r.rec).Stage == "request" }

	follow := map[int][]int{} // a plan, and the events that belong right after it
	moved := map[int]bool{}   // the events that are somewhere else than where they were
	for i := range raw {
		if !isPlan(raw[i]) {
			continue
		}
		request, patch := -1, -1 // the first of each, up to the next plan that starts a compaction
		for j := i + 1; j < len(raw) && !isPlan(raw[j]); j++ {
			switch {
			case request < 0 && isCompactorRequest(raw[j]):
				request = j
			case patch < 0 && isPatchRequest(raw[j]):
				patch = j
			}
		}
		for _, j := range []int{request, patch} {
			if j >= 0 {
				follow[i] = append(follow[i], j)
				moved[j] = true
			}
		}
	}
	out := make([]rawRec, 0, len(raw))
	for i := range raw {
		if moved[i] {
			continue
		}
		out = append(out, raw[i])
		for _, j := range follow[i] {
			out = append(out, raw[j])
		}
	}
	return out
}

// paceChat gives the records of a recorded session their times. raw is the session in the order it happened; the result is the records
// in the order of their times, as a transcript holds them. An event of a compaction's model call is held back to the time the call
// answers (and so comes later than the events that follow it in raw); the events of its start are put right after its plan
// (canonicalOrder); nothing else changes its place.
func paceChat(raw []rawRec, p chatPace) ([]app.ChatRecord, error) {
	raw = canonicalOrder(raw)
	z := &pacer{p: p, phases: scanPhases(raw, p), starts: map[string]time.Duration{}}
	for i := range raw {
		if raw[i].pause != 0 {
			z.t += raw[i].pause
			continue
		}
		rec := raw[i].rec
		switch rec.Kind {
		case app.RecAttach:
			z.t = p.Attach
		case app.RecKey:
			z.t += p.Key
		case app.RecText:
			switch {
			case z.cur != nil && !z.first:
				z.t = z.t0 + z.cur.ttft
				z.first = true
			default:
				z.t += p.Decode
			}
		case app.RecEvent:
			in := infoOf(&rec)
			switch {
			case rec.Event.Type == "model.request" && in.Kind == "main":
				z.cur, z.t0, z.first, z.answered = z.phases[i], z.t, false, false
			case rec.Event.Type == "model.request" && in.Kind == "compactor":
				z.bgReq = in.Req
			case rec.Event.Type == "compact.plan" && in.Decision == "start":
				z.bgPlan = z.t
			case rec.Event.Type == "compact.patch" && in.Stage == "request":
			case rec.Event.Type == "compact.patch" && in.Stage == "ready", rec.Event.Type == "model.response" && z.bgReq != "" && in.Req == z.bgReq:
				z.hold(rec, z.bgPlan+p.Compactor) // the call of the compaction answers when it answers, beside the agent
				continue
			default:
				z.endAnswer() // anything else that the session says comes after the answer it is in
			}
		case app.RecToolStart:
			z.endAnswer()
			z.starts[rec.Call.ID] = z.t
		case app.RecToolEnd:
			z.t += min(max(time.Duration(rec.TookMS)*time.Millisecond, p.ToolMin), p.ToolMax)
			rec.TookMS = (z.t - z.starts[rec.Call.ID]).Milliseconds()
		case app.RecResponse, app.RecNotice, app.RecAsk, app.RecTurn, app.RecTurnEnd, app.RecReset:
			z.endAnswer()
		default:
			return nil, fmt.Errorf("a record of kind %q cannot be paced", rec.Kind)
		}
		z.place(rec, z.t)
	}
	for len(z.held) > 0 {
		h := z.held[0]
		z.held = z.held[1:]
		z.write(h.rec, h.due)
	}
	return z.out, nil
}
