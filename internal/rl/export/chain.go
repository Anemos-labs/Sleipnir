package export

import (
	"bytes"
	"encoding/json"
	"fmt"

	"github.com/reee344/sleipnir/internal/core"
	"github.com/reee344/sleipnir/internal/rl"
)

// chainView is a run of steps of one agent and segment rendered as a single
// conversation: the wire messages of the last step's prompt followed by its
// completion. Because prompts inside a segment are append-only, every earlier
// completion sits inside that list, and at[j] says where; those are the turns the
// model produced and the ones a trainer may learn from.
type chainView struct {
	steps []stepCtx
	msgs  []json.RawMessage
	tools json.RawMessage
	at    []int // at[j]: index in msgs of step j's completion, -1 if it could not be located
	// wire is the completion as rendered for training (per the Reasoning option);
	// wire[j] is msgs[at[j]] unless reasoning handling replaced it.
	changed bool // redaction altered any prompt or completion
}

// renderChain renders steps (one segment of one agent, in order) as a chainView.
// Completions are located by exact comparison with the assistant message the
// later prompts replay; a completion that cannot be located is not marked, so it
// stays context rather than becoming a wrongly placed training target.
func (x *exporter) renderChain(steps []stepCtx, objectArgs bool) (*chainView, error) {
	cv := &chainView{steps: steps, at: make([]int, len(steps))}
	comps := make([]json.RawMessage, len(steps))
	raws := make([]json.RawMessage, len(steps))
	counts := make([]int, len(steps))
	for j, c := range steps {
		p, err := x.promptFor(c.we, c.st)
		if err != nil {
			return nil, err
		}
		rp, pc := x.redactPrompt(p)
		wp, err := x.renderPrompt(rp, objectArgs)
		if err != nil {
			return nil, err
		}
		turn, tc := x.redactTurn(c.st.Completion.Turn)
		cv.changed = cv.changed || pc || tc
		counts[j] = len(wp.Messages)
		if comps[j], err = x.renderCompletion(turn, c.st.Model, objectArgs); err != nil {
			return nil, err
		}
		if raws[j], err = x.renderCompletionWire(turn, c.st.Model, objectArgs); err != nil {
			return nil, err
		}
		if j == len(steps)-1 {
			cv.msgs = append(append([]json.RawMessage(nil), wp.Messages...), comps[j])
			cv.tools = wp.Tools
		}
	}
	last := len(steps) - 1
	for j := range steps {
		cv.at[j] = -1
		switch {
		case j == last:
			cv.at[j] = len(cv.msgs) - 1
		case counts[j] < len(cv.msgs) && sameJSON(cv.msgs[counts[j]], raws[j]):
			cv.at[j] = counts[j]
			cv.msgs[counts[j]] = comps[j] // the trained turn follows the Reasoning option
		default:
			x.stats.drop("step:choice_misaligned")
		}
	}
	return cv, nil
}

// renderCompletionWire renders a completion exactly as the wire replays it,
// before the Reasoning option is applied.
func (x *exporter) renderCompletionWire(t core.Turn, model string, objectArgs bool) (json.RawMessage, error) {
	saved := x.o.Reasoning
	x.o.Reasoning = "keep"
	defer func() { x.o.Reasoning = saved }()
	return x.renderCompletion(t, model, objectArgs)
}

func sameJSON(a, b json.RawMessage) bool {
	if bytes.Equal(a, b) {
		return true
	}
	var ca, cb bytes.Buffer
	if json.Compact(&ca, a) != nil || json.Compact(&cb, b) != nil {
		return false
	}
	return bytes.Equal(ca.Bytes(), cb.Bytes())
}

// splitChains groups an agent's steps for conversation-level formats: each maximal
// run of main steps of one segment is a chain, and every other step is its own
// unit. Units come back in step order of their first step.
func splitChains(we *workEpisode, ag *rl.Agent, ai int) [][]stepCtx {
	var out [][]stepCtx
	byKey := map[int]int{} // segment -> index into out of its main chain
	for si := range ag.Steps {
		c := stepCtx{we: we, ag: ag, ai: ai, st: &ag.Steps[si], si: si}
		if c.st.Kind != rl.KindMain {
			out = append(out, []stepCtx{c})
			continue
		}
		if k, ok := byKey[c.st.Segment]; ok {
			out[k] = append(out[k], c)
			continue
		}
		byKey[c.st.Segment] = len(out)
		out = append(out, []stepCtx{c})
	}
	return out
}

// withWeight sets "weight" on assistant messages of an OpenAI SFT record.
func withWeight(msgs []json.RawMessage, weights []int) ([]json.RawMessage, error) {
	out := make([]json.RawMessage, len(msgs))
	for i, m := range msgs {
		var obj map[string]json.RawMessage
		if err := json.Unmarshal(m, &obj); err != nil {
			return nil, fmt.Errorf("message %d: %w", i, err)
		}
		if string(obj["role"]) != `"assistant"` {
			out[i] = m
			continue
		}
		w, _ := marshalStable(weights[i])
		obj["weight"] = w
		b, err := marshalStable(obj)
		if err != nil {
			return nil, err
		}
		out[i] = b
	}
	return out, nil
}
