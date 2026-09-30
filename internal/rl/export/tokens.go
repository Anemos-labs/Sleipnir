package export

import (
	"math"
	"strconv"

	"github.com/reee344/sleipnir/internal/core"
	"github.com/reee344/sleipnir/internal/rl"
)

// tokenRecord is one line of the tokens format: a training sequence as token ids
// with a loss mask. A per-step record is one call (prompt ids, response ids); a
// packed record is one (agent, segment): prompt ids of the first call, then every
// completion and the observation tokens between them as one response, with the
// mask on completion tokens only.
type tokenRecord struct {
	Schema       string    `json:"schema"`
	ID           string    `json:"id"`
	TaskID       string    `json:"task_id"`
	GroupID      string    `json:"group_id"`
	Sample       int       `json:"sample"`
	Role         string    `json:"role"`
	Agent        string    `json:"agent"`
	Segment      int       `json:"segment"`
	Steps        []string  `json:"steps"`
	Packed       bool      `json:"packed"`
	PromptIDs    []int32   `json:"prompt_ids"`
	ResponseIDs  []int32   `json:"response_ids"`
	ResponseMask []int8    `json:"response_mask"`
	OldLogprobs  []float32 `json:"old_logprobs,omitempty"`
	Reward       float64   `json:"reward"`
	Advantage    *float64  `json:"advantage,omitempty"`
	Model        string    `json:"model"`
	Tokenizer    string    `json:"tokenizer,omitempty"`
	ModelVersion string    `json:"model_version,omitempty"`
	Split        string    `json:"split,omitempty"`
}

// tokCandidate is a step that passed every per-step test for the tokens format.
type tokCandidate struct {
	c      stepCtx
	tok    *core.TokenTrace
	reason string // why the step cannot be exported here; "" when it can
}

// Pack failure reasons (Stats.PackFailed).
const (
	packNotTrainable    = "not_trainable"
	packNoTrace         = "no_trace"
	packRedacted        = "redacted"
	packPrefixMismatch  = "prefix_mismatch"
	packLogprobsPartial = "logprobs_partial"
	packTokenizer       = "tokenizer_changed"
	packModelVersion    = "model_version_changed"
	packCreditVaries    = "credit_varies"
)

// traceUsable returns the step's token trace when it may be exported, or the
// reason it may not: a consistent trace with prompt ids and finite logprobs, and,
// when redaction is on, text that redaction leaves alone. Token ids encode the
// original text, so a redacted prompt cannot keep them: exporting them would leak
// the very secret the redactor removed, and re-tokenising would fabricate ids.
func (x *exporter) traceUsable(c stepCtx) (*core.TokenTrace, string) {
	tr := c.st.Tokens
	switch {
	case tr == nil || !tr.Consistent():
		return nil, "no_tokens"
	case len(tr.PromptIDs) == 0:
		return nil, "no_prompt_ids"
	}
	for _, lp := range tr.Logprobs {
		if math.IsNaN(float64(lp)) || math.IsInf(float64(lp), 0) {
			return nil, "bad_logprobs"
		}
	}
	if x.red != nil {
		p, err := x.promptFor(c.we, c.st)
		if err != nil {
			x.stats.warn(c.st.ID + ": " + err.Error())
			return nil, "no_prompt"
		}
		if _, changed := x.redactPrompt(p); changed {
			return nil, "redacted_tokens"
		}
		if _, changed := x.redactTurn(c.st.Completion.Turn); changed {
			return nil, "redacted_tokens"
		}
	}
	return tr, ""
}

// tokenVerdict tests one step for the tokens format: the generic step filters and
// a usable trace.
func (x *exporter) tokenVerdict(c stepCtx) tokCandidate {
	tc := tokCandidate{c: c}
	if r := x.stepVerdict(c); r != "" {
		tc.reason = r
		return tc
	}
	tc.tok, tc.reason = x.traceUsable(c)
	return tc
}

func (x *exporter) writeTokens(work []*workEpisode) error {
	for _, we := range work {
		for ai := range we.ep.Agents {
			ag := &we.ep.Agents[ai]
			for si := 0; si < len(ag.Steps); {
				st := &ag.Steps[si]
				c := stepCtx{we: we, ag: ag, ai: ai, st: st, si: si}
				if st.Kind != rl.KindMain {
					if err := x.tokenSingle(x.tokenVerdict(c)); err != nil {
						return err
					}
					si++
					continue
				}
				// The chain: consecutive main steps of one segment. Side calls that
				// interleave (compactor forks) are handled where they sit.
				end := si
				var chain []tokCandidate
				var side []tokCandidate
				for ; end < len(ag.Steps); end++ {
					s := &ag.Steps[end]
					cc := stepCtx{we: we, ag: ag, ai: ai, st: s, si: end}
					if s.Kind == rl.KindMain {
						if s.Segment != st.Segment {
							break
						}
						chain = append(chain, x.tokenVerdict(cc))
					} else if s.Segment == st.Segment {
						side = append(side, x.tokenVerdict(cc))
					} else {
						break
					}
				}
				if err := x.tokenChain(chain); err != nil {
					return err
				}
				for _, sc := range side {
					if err := x.tokenSingle(sc); err != nil {
						return err
					}
				}
				si = end
			}
		}
	}
	return nil
}

// tokenSingle exports one step as its own record, or records why not.
func (x *exporter) tokenSingle(tc tokCandidate) error {
	if tc.reason != "" {
		x.noteDrop(tc.c, tc.reason)
		return nil
	}
	c, tr := tc.c, tc.tok
	reward, _ := rewardOf(c.we.ep, c.ag, c.st)
	mask := make([]int8, len(tr.CompletionIDs))
	for i := range mask {
		mask[i] = 1
	}
	split := x.splitOf(c.we.ep)
	rec := tokenRecord{
		Schema: SchemaTokens, ID: recordID(c.we.ep, c.st), TaskID: c.we.ep.TaskID, GroupID: c.we.ep.Group, Sample: c.we.ep.Sample,
		Role: c.st.Role, Agent: c.ag.ID, Segment: c.st.Segment, Steps: []string{c.st.ID}, Packed: false,
		PromptIDs: tr.PromptIDs, ResponseIDs: tr.CompletionIDs, ResponseMask: mask, OldLogprobs: tr.Logprobs,
		Reward: reward, Advantage: x.advantageOf(c.st), Model: c.st.Model, Tokenizer: tr.Tokenizer, ModelVersion: tr.ModelVersion, Split: split,
	}
	if err := x.emit(rec, c.st.Role, "step", split); err != nil {
		return err
	}
	c.we.contributed = true
	x.countTeacher(c.st)
	x.stats.PromptTokens += len(tr.PromptIDs)
	x.stats.ResponseTokens += len(tr.CompletionIDs)
	x.stats.TrainedTokens += len(tr.CompletionIDs)
	return nil
}

// tokenChain exports a segment's main steps: packed when asked and possible,
// otherwise one record per step.
func (x *exporter) tokenChain(chain []tokCandidate) error {
	if len(chain) == 0 {
		return nil
	}
	if x.o.PackSegments && chain[0].reason != "role_filtered" {
		rec, reason := x.pack(chain)
		if rec != nil {
			if err := x.emit(*rec, rec.Role, "segment", rec.Split); err != nil {
				return err
			}
			c := chain[0].c
			c.we.contributed = true
			for _, tc := range chain {
				x.countTeacher(tc.c.st)
			}
			x.stats.Packed++
			x.stats.PackedSteps += len(chain)
			x.stats.PromptTokens += len(rec.PromptIDs)
			x.stats.ResponseTokens += len(rec.ResponseIDs)
			for _, m := range rec.ResponseMask {
				x.stats.TrainedTokens += int(m)
			}
			return nil
		}
		bump(&x.stats.PackFailed, reason, 1)
	}
	for _, tc := range chain {
		if err := x.tokenSingle(tc); err != nil {
			return err
		}
	}
	return nil
}

// pack builds the packed record of a segment, or names the first reason it cannot
// be built. Every step must be exportable and carry a trace, the traces must chain
// (each prompt starts with the previous prompt and completion, token for token),
// and one reward and advantage must describe the whole sequence. Nothing is
// repaired: observation tokens are taken from the next prompt as recorded.
func (x *exporter) pack(chain []tokCandidate) (*tokenRecord, string) {
	for _, tc := range chain {
		switch tc.reason {
		case "":
		case "no_tokens", "no_prompt_ids", "bad_logprobs":
			return nil, packNoTrace
		case "redacted_tokens":
			return nil, packRedacted
		case "not_trainable", "teacher_not_ok":
			return nil, packNotTrainable
		default:
			return nil, tc.reason
		}
	}
	first, last := chain[0].c, chain[len(chain)-1].c
	haveLP := len(chain[0].tok.Logprobs) > 0
	rw0, _ := rewardOf(first.we.ep, first.ag, first.st)
	for i, tc := range chain {
		tr := tc.tok
		if i > 0 {
			prev := chain[i-1].tok
			if tr.Tokenizer != prev.Tokenizer {
				return nil, packTokenizer
			}
			if tr.ModelVersion != prev.ModelVersion {
				return nil, packModelVersion
			}
			if !chainsOnto(prev, tr) {
				return nil, packPrefixMismatch
			}
		}
		if (len(tr.Logprobs) > 0) != haveLP {
			return nil, packLogprobsPartial
		}
		rw, _ := rewardOf(tc.c.we.ep, tc.c.ag, tc.c.st)
		if math.Abs(rw-rw0) > 1e-12 || math.Abs(tc.c.st.Advantage-first.st.Advantage) > 1e-12 {
			return nil, packCreditVaries
		}
	}
	rec := &tokenRecord{
		Schema: SchemaTokens, ID: first.we.ep.ID + "#" + first.ag.ID + ".seg" + strconv.Itoa(first.st.Segment), TaskID: first.we.ep.TaskID,
		GroupID: first.we.ep.Group, Sample: first.we.ep.Sample, Role: first.st.Role, Agent: first.ag.ID, Segment: first.st.Segment,
		Packed: true, PromptIDs: chain[0].tok.PromptIDs, Reward: rw0, Advantage: x.advantageOf(last.st), Model: first.st.Model,
		Tokenizer: chain[0].tok.Tokenizer, ModelVersion: chain[0].tok.ModelVersion, Split: x.splitOf(first.we.ep),
	}
	for i, tc := range chain {
		tr := tc.tok
		rec.Steps = append(rec.Steps, tc.c.st.ID)
		rec.ResponseIDs = append(rec.ResponseIDs, tr.CompletionIDs...)
		for range tr.CompletionIDs {
			rec.ResponseMask = append(rec.ResponseMask, 1)
		}
		if haveLP {
			rec.OldLogprobs = append(rec.OldLogprobs, tr.Logprobs...)
		}
		if i+1 < len(chain) {
			next := chain[i+1].tok
			obs := next.PromptIDs[len(tr.PromptIDs)+len(tr.CompletionIDs):]
			rec.ResponseIDs = append(rec.ResponseIDs, obs...)
			for range obs {
				rec.ResponseMask = append(rec.ResponseMask, 0)
				if haveLP {
					rec.OldLogprobs = append(rec.OldLogprobs, 0) // masked: never a training target
				}
			}
		}
	}
	return rec, ""
}

// chainsOnto reports whether next's prompt is prev's prompt, then prev's
// completion, then anything: the strict prefix property that lets two calls share
// one forward pass.
func chainsOnto(prev, next *core.TokenTrace) bool {
	n := len(prev.PromptIDs) + len(prev.CompletionIDs)
	if len(next.PromptIDs) < n {
		return false
	}
	for i, id := range prev.PromptIDs {
		if next.PromptIDs[i] != id {
			return false
		}
	}
	for i, id := range prev.CompletionIDs {
		if next.PromptIDs[len(prev.PromptIDs)+i] != id {
			return false
		}
	}
	return true
}
