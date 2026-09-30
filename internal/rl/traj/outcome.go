package traj

import (
	"fmt"
	"sort"
	"time"

	"github.com/reee344/sleipnir/internal/core"
	"github.com/reee344/sleipnir/internal/rl"
)

// root is the agent whose final word is the team's: the manager, or the first
// agent that has no parent (the sole agent of a single-agent run).
func (b *builder) root() *agentInfo {
	for _, a := range b.order {
		if a.role == rl.RoleManager {
			return a
		}
	}
	for _, a := range b.order {
		if a.parent == "" {
			return a
		}
	}
	if len(b.order) > 0 {
		return b.order[0]
	}
	return nil
}

// outcome gathers verdicts, what the team claimed and the human-facing labels.
//
// Verdicts come from outcome events: the last verifier verdict is the result; all
// review, human and protocol verdicts are kept in order. An "infra" outcome is a
// failure of the environment, not a verdict, and is reported through the
// infra_error flag instead. A verdict without a score gets 1 when it passed and 0
// when it did not.
func (b *builder) outcome() rl.Outcome {
	var out rl.Outcome
	for _, o := range b.v.outcomes {
		if o.kind == "infra" {
			continue
		}
		vd := rl.Verdict{Kind: o.kind, Pass: o.pass, Score: o.score, Version: o.version, Detail: o.detail, Ms: o.ms}
		if !o.hasScore {
			vd.Score = 0
			if o.pass {
				vd.Score = 1
			}
		}
		if o.kind == "verifier" {
			v := vd
			out.Verifier = &v
			continue
		}
		out.Reviews = append(out.Reviews, vd)
	}
	out.Claimed = b.claimed()
	if b.v.denials > 0 {
		out.Labels = append(out.Labels, fmt.Sprintf("perm_denied:%d", b.v.denials))
	}
	if b.v.steers > 0 {
		out.Labels = append(out.Labels, fmt.Sprintf("user_steer:%d", b.v.steers))
	}
	sort.Strings(out.Labels)
	return out
}

// claimed says what the team said at the end: "done" when the root agent ended
// with a final answer (or its last done claim went through), "blocked" when the
// last thing it did on the board was block a task, "gave_up" when its last call
// was cut off (max_tokens, refusal), "budget" when a budget stopped it, and ""
// when the log does not say.
func (b *builder) claimed() string {
	root := b.root()
	if root == nil {
		return ""
	}
	if b.v.budgetHit || root.ended && root.state == "budget" {
		return "budget"
	}
	var lastAction string
	for _, t := range b.runs {
		if t.agent == root && t.name == "task" && t.has && !t.isErr {
			if act, _ := parseObject(t.input)["action"].(string); act == "done" || act == "block" || act == "resume" {
				lastAction = act
			}
		}
	}
	switch {
	case lastAction == "block":
		return "blocked"
	case cleanFinish(root), lastAction == "done":
		return "done"
	}
	if n := len(root.main); n > 0 {
		switch root.main[n-1].p.stop {
		case core.StopMaxTokens, core.StopRefusal, core.StopOther:
			return "gave_up"
		}
	}
	return ""
}

// cost totals what the run consumed. Usage and USD cover every response,
// including compactor calls; Requests counts every call attempted (retries
// included) because requests per minute, not tokens, is the scarce resource.
func (b *builder) cost() rl.CostRef {
	var c rl.CostRef
	for _, p := range b.run.respOrder {
		c.Usage = c.Usage.Add(p.usage)
		c.USD += p.costUSD
	}
	c.Requests = int(b.signals[rl.SigRequests])
	if n := len(b.run.evs); n > 0 {
		c.WallMs = b.run.evs[n-1].TS.Sub(b.run.evs[0].TS).Milliseconds()
		if c.WallMs < 0 {
			c.WallMs = 0
		}
	}
	return c
}

// span is the episode's wall-clock extent: session.start/end when logged, else the
// first and last event.
func (b *builder) span() (time.Time, time.Time) {
	evs := b.run.evs
	if len(evs) == 0 {
		return time.Time{}, time.Time{}
	}
	start, end := evs[0].TS, evs[len(evs)-1].TS
	if b.v.sessionStart != nil {
		start = *b.v.sessionStart
	}
	if b.v.sessionEnd != nil {
		end = *b.v.sessionEnd
	}
	return start, end
}

// provenance records which models produced the completions. Every model other
// than the policy is a teacher, whatever role it played.
func (b *builder) provenance(ep *rl.Episode) {
	if b.task != nil {
		ep.Provenance.License = b.task.Repo.License
	}
	seen := map[string]bool{}
	for _, a := range b.order {
		for _, st := range a.steps {
			if !st.step.Trainable && st.step.Model != "" && !seen[st.step.Model] {
				seen[st.step.Model] = true
				ep.Provenance.TeacherModels = append(ep.Provenance.TeacherModels, st.step.Model)
			}
		}
	}
	sort.Strings(ep.Provenance.TeacherModels)
	ep.Provenance.Teacher = len(ep.Provenance.TeacherModels) > 0
}

// flags sets the episode flags: infra_error, truncated, replay_mismatch,
// weak_label, token_mismatch and budget_exceeded, sorted for stable output.
func (b *builder) flags(ep *rl.Episode) {
	for _, o := range b.v.outcomes {
		if o.kind == "infra" && !o.pass {
			ep.AddFlag(rl.FlagInfraError)
		}
	}
	if b.v.sessionEnd == nil && !b.finished() {
		ep.AddFlag(rl.FlagTruncated)
	}
	if b.replayBroken() {
		ep.AddFlag(rl.FlagReplayMismatch)
	}
	if ep.Outcome.Verifier == nil {
		ep.AddFlag(rl.FlagWeakLabel)
	}
	if b.tokenMismatch {
		ep.AddFlag(rl.FlagTokenMismatch)
	}
	if b.v.budgetHit {
		ep.AddFlag(rl.FlagBudgetExceeded)
	}
	sort.Strings(ep.Flags)
}

// finished reports a clean end without session.end: the root agent logged its
// end, or its last main step is a final answer. Raw agent runs (no session
// layer) end that way.
func (b *builder) finished() bool {
	root := b.root()
	return root != nil && (root.ended || cleanFinish(root))
}

// replayBroken reports whether the replay check found anything that touches the
// exported data: log damage, an orphan response, or a problem with a request that
// got a response. Problems confined to requests that never got a response are
// reported by Verify but do not disqualify the episode: nothing of them is
// exported.
func (b *builder) replayBroken() bool {
	for _, m := range b.run.Verify() {
		if m.Req == "" || m.Kind == KindResponse {
			return true
		}
		if _, answered := b.run.resps[m.Req]; answered {
			return true
		}
		if _, isReq := b.run.reqs[m.Req]; !isReq {
			return true
		}
	}
	return false
}
