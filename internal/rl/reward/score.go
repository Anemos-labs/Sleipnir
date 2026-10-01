package reward

import (
	"errors"
	"fmt"
	"math"
	"regexp"
	"strings"

	"github.com/reee344/sleipnir/internal/rl"
)

// SigBudgetBreaches is an optional signal (not part of rl's fixed vocabulary):
// how many budgets the run overran, when the harness counts them. Without it a
// budget_exceeded flag counts as one breach.
const SigBudgetBreaches = "budget_breaches"

// Score fills the reward side of an episode from the recorded run, the task
// definition and the blobs behind d:
//
//   - ep.Reward: Components (outcome, honest_done, cost, requests, time,
//     protocol as raw values, plus "role/<name>" aggregates), Total (their
//     weighted sum under cfg, clipped) and Notes;
//   - ep.Agents[i].Reward: that agent's role reward, with the components it was
//     built from, so it can be re-weighted offline;
//   - Step.Reward of compactor and mailman steps: the reward of that call under
//     its own role (main steps inherit their agent's reward);
//   - ep.Cost.ITE and ep.Cost.Target: the counterfactual cost under cfg's target;
//   - ep.Flags: hack:* flags for every detector that fired. Flags are only ever
//     added, never removed, and any hack:* flag on the episode forces the outcome
//     component to zero.
//
// Score is idempotent: it recomputes everything from the episode and replaces
// Reward, Agent.Reward and compactor/mailman Step.Reward, so calling it again
// with new weights re-scores a run. It never touches prompts, signals or other
// recorded data, and on error the episode is left exactly as it was.
//
// task may be nil (a session without a task: no budgets, no protected paths).
// d may be nil only when the episode has no diff blob; use NoDiffs to score
// without diff access on purpose.
func Score(ep *rl.Episode, task *rl.Task, cfg Config, d DiffSource) error {
	if ep == nil {
		return errors.New("reward: nil episode")
	}
	r, err := cfg.resolve()
	if err != nil {
		return err
	}
	if task == nil {
		task = &rl.Task{}
	}
	if err := checkPatterns("task.verifier.protected", task.Verifier.Protected); err != nil {
		return err
	}
	s := &scorer{ep: ep, task: task, cfg: r, diff: d, src: promptSource(cfg, d)}
	return s.run()
}

type scorer struct {
	ep   *rl.Episode
	task *rl.Task
	cfg  *resolved
	diff DiffSource
	src  PromptText

	notes []string
	rep   Repriced
	hack  bool
}

func (s *scorer) note(format string, args ...any) {
	s.notes = append(s.notes, fmt.Sprintf(format, args...))
}

// promptSource picks where fidelity probes read prompt text from.
func promptSource(cfg Config, d DiffSource) PromptText {
	var chain []PromptText
	if cfg.Prompts != nil {
		chain = append(chain, cfg.Prompts)
	}
	if ps, ok := d.(PromptSource); ok {
		chain = append(chain, ps.PromptText)
	}
	chain = append(chain, InlinePromptText)
	return func(ep *rl.Episode, st *rl.Step) (string, error) {
		// Report the most informative failure: the first source that actually
		// failed, not the trailing "prompt was not inlined" fallback.
		var first error
		for _, f := range chain {
			text, err := f(ep, st)
			if err == nil {
				return text, nil
			}
			if first == nil || (errors.Is(first, ErrNoPromptText) && !errors.Is(err, ErrNoPromptText)) {
				first = err
			}
		}
		return "", first
	}
}

func (s *scorer) run() error {
	ep := s.ep
	hits, dnotes, err := detectHacks(ep, s.task, s.cfg, s.diff)
	if err != nil {
		return err
	}
	rep, err := RepriceWith(ep, s.cfg.target, s.cfg.reprice)
	if err != nil {
		return err
	}
	s.rep = rep
	for _, w := range rep.Warnings {
		s.note("reprice: %s", w)
	}

	var newFlags []string
	for _, h := range hits {
		newFlags = append(newFlags, h.flag)
		s.note("%s: %s", h.flag, h.note)
	}
	for _, n := range dnotes {
		s.note("%s", n)
	}
	s.hack = hasHackFlag(ep.Flags) || len(hits) > 0
	if s.hack && len(hits) == 0 {
		s.note("outcome is zero because the episode already carries a hack flag")
	}

	comps := s.episodeComponents()
	epTotal := s.cfg.clipTotal(s.cfg.total(comps))

	agents := make([]rl.Reward, len(ep.Agents))
	stepRewards := map[[2]int]float64{}
	events := s.compactionEvents(epTotal)
	mail := s.mailComponents()

	byRole := map[string][]float64{} // reward samples per role, for the informational aggregates
	for ai := range ep.Agents {
		a := &ep.Agents[ai]
		class := roleClass(a.Role)
		ac := copyComps(comps)
		switch class {
		case rl.RoleCompactor:
			ac = map[string]float64{CompDownstream: epTotal}
		case rl.RoleMailman:
			ac = map[string]float64{CompDownstream: epTotal}
			for k, v := range mail {
				ac[k] = v
			}
		case rl.RoleWorker:
			s.workerTerms(ai, ac)
		case rl.RoleManager:
			s.managerTerms(ai, ac)
		}

		// Per-event rewards of this agent's compactor and mailman steps. The
		// per-compaction components are kept under "compactor/<step>/<name>" so a
		// run can be re-weighted offline.
		var compTotals, mailTotals []float64
		for _, ev := range events {
			if ev.ai != ai {
				continue
			}
			for k, v := range ev.comps {
				ac[fmt.Sprintf("compactor/%s/%s", ev.stepID, k)] = v
			}
			stepRewards[[2]int{ai, ev.si}] = ev.total
			compTotals = append(compTotals, ev.total)
		}
		for si := range a.Steps {
			if stepRole(a, &a.Steps[si]) != rl.RoleMailman {
				continue
			}
			mc := map[string]float64{CompDownstream: epTotal}
			for k, v := range mail {
				mc[k] = v
			}
			t := s.cfg.clipTotal(s.cfg.total(mc))
			stepRewards[[2]int{ai, si}] = t
			mailTotals = append(mailTotals, t)
		}
		if len(compTotals) > 0 {
			ac["role/"+rl.RoleCompactor] = mean(compTotals)
			byRole[rl.RoleCompactor] = append(byRole[rl.RoleCompactor], compTotals...)
		}
		if len(mailTotals) > 0 {
			ac["role/"+rl.RoleMailman] = mean(mailTotals)
			byRole[rl.RoleMailman] = append(byRole[rl.RoleMailman], mailTotals...)
		}
		total := s.cfg.clipTotal(s.cfg.total(ac))
		switch {
		case class == rl.RoleCompactor && len(compTotals) > 0:
			total = mean(compTotals)
		case class == rl.RoleMailman && len(mailTotals) > 0:
			total = mean(mailTotals)
		}
		agents[ai] = rl.Reward{Total: total, Components: ac}
		if class != rl.RoleCompactor && class != rl.RoleMailman {
			byRole[class] = append(byRole[class], total)
		}
	}
	for _, class := range sortedKeys(byRole) {
		comps["role/"+class] = mean(byRole[class])
	}

	// ---- commit: nothing above touched the episode ----
	for _, f := range newFlags {
		ep.AddFlag(f)
	}
	ep.Reward = rl.Reward{Total: epTotal, Components: comps, Notes: s.finalNotes()}
	ep.Cost.ITE = s.rep.ITE
	ep.Cost.Target = s.rep.Target
	for ai := range ep.Agents {
		ep.Agents[ai].Reward = agents[ai]
		for si := range ep.Agents[ai].Steps {
			st := &ep.Agents[ai].Steps[si]
			if v, ok := stepRewards[[2]int{ai, si}]; ok {
				st.Reward = v
			} else if stepRole(&ep.Agents[ai], st) == rl.RoleCompactor || stepRole(&ep.Agents[ai], st) == rl.RoleMailman {
				st.Reward = 0
			}
		}
	}
	return nil
}

func (s *scorer) finalNotes() []string {
	limit := int(s.cfg.cap(CapMaxNotes))
	if limit <= 0 {
		limit = int(defaultCaps()[CapMaxNotes])
	}
	notes := s.notes
	if len(notes) <= limit {
		return notes
	}
	kept := append([]string(nil), notes[:limit-1]...)
	return append(kept, fmt.Sprintf("(+%d more notes)", len(notes)-limit+1))
}

func copyComps(m map[string]float64) map[string]float64 {
	out := make(map[string]float64, len(m))
	for k, v := range m {
		out[k] = v
	}
	return out
}

func mean(v []float64) float64 {
	if len(v) == 0 {
		return 0
	}
	sum := 0.0
	for _, x := range v {
		sum += x
	}
	return sum / float64(len(v))
}

// ---- episode components ---------------------------------------------------------------

// verdictScore reads the verifier's score in [0, 1].
func verdictScore(v *rl.Verdict) (float64, string) {
	switch {
	case v == nil:
		return 0, "no verifier verdict: outcome is 0"
	case !finite(v.Score):
		return 0, "verifier score is not a finite number: outcome is 0"
	}
	sc, note := v.Score, ""
	if v.Pass && sc == 0 {
		// A passing verdict with a zero score is contradictory; the exit-code
		// pass mode is the likely producer, and scoring it 0 would erase every pass.
		sc, note = 1, "verifier passed with score 0: scored as 1"
	}
	if sc < 0 || sc > 1 {
		sc = clamp01(sc)
		note = "verifier score outside [0, 1]: clamped"
	}
	return sc, note
}

func claimedDone(ep *rl.Episode) bool {
	// A run the budget ended did not say it was done. The harness used to default the claim to "done" for a run the wall clock
	// cut off, so episodes written before it stopped carry both; the flag is what the runner saw, the claim was a default.
	if ep.Outcome.Claimed == "done" && ep.Has(rl.FlagBudgetExceeded) {
		return false
	}
	switch ep.Outcome.Claimed {
	case "done":
		return true
	case "":
		return sigOr(ep, rl.SigDoneAccepted, 0) > 0 || sigOr(ep, rl.SigDoneClaims, 0) > 0
	}
	return false
}

func (s *scorer) episodeComponents() map[string]float64 {
	ep, t := s.ep, s.task
	c := map[string]float64{}

	// outcome
	score, note := verdictScore(ep.Outcome.Verifier)
	if note != "" {
		s.note("%s", note)
	}
	if s.hack {
		score = 0
	}
	c[CompOutcome] = score

	// honest_done
	v := ep.Outcome.Verifier
	switch {
	case v == nil:
		c[CompHonestDone] = 0
	case claimedDone(ep) && v.Pass && !s.hack:
		c[CompHonestDone] = 1
	case claimedDone(ep):
		c[CompHonestDone] = -1
	default:
		c[CompHonestDone] = 0
	}

	// cost, requests, time: relative to the task's budgets; no budget, no term.
	if t.Budget.ITE > 0 {
		c[CompCost] = -clamp01(s.rep.ITE / t.Budget.ITE)
	} else {
		c[CompCost] = 0
	}
	stepsTotal := 0
	for ai := range ep.Agents {
		stepsTotal += len(ep.Agents[ai].Steps)
	}
	requests := math.Max(sigOr(ep, rl.SigRequests, 0), float64(stepsTotal))
	if t.Budget.Requests > 0 {
		c[CompRequests] = -clamp01(requests / float64(t.Budget.Requests))
	} else {
		c[CompRequests] = 0
	}
	if t.Budget.Steps > 0 {
		c[CompTime] = -clamp01(s.criticalPathSteps() / float64(t.Budget.Steps))
	} else {
		c[CompTime] = 0
	}

	// protocol
	c[CompProtocol] = -s.cfg.frac(s.protocolEvents(), CapProtocol)
	return c
}

// criticalPathSteps prefers the harness's own signal and falls back to the
// episode's causal DAG.
func (s *scorer) criticalPathSteps() float64 {
	if v, ok := sig(s.ep, rl.SigCriticalPath); ok && v > 0 {
		return v
	}
	cp, ok := criticalPath(s.ep)
	if !ok {
		s.note("edges contain a cycle: critical path is the longest single-agent chain")
	}
	return float64(cp)
}

// protocolEvents counts rule violations: invalid tool calls, rejected patches,
// lease and scope violations, stale writes and budget breaches.
func (s *scorer) protocolEvents() float64 {
	ep := s.ep
	invalid, ok := sig(ep, rl.SigInvalidToolCalls)
	if !ok {
		for ai := range ep.Agents {
			for si := range ep.Agents[ai].Steps {
				for _, b := range ep.Agents[ai].Steps[si].Completion.Turn.ToolCalls() {
					if b.Invalid != "" {
						invalid++
					}
				}
			}
		}
	}
	breaches := sigOr(ep, SigBudgetBreaches, 0)
	if ep.Has(rl.FlagBudgetExceeded) && breaches < 1 {
		breaches = 1
	}
	return invalid + sigOr(ep, rl.SigCompactRejects, 0) + sigOr(ep, rl.SigLeaseConflicts, 0) +
		sigOr(ep, rl.SigScopeViolations, 0) + sigOr(ep, rl.SigStaleWrites, 0) + breaches
}

// ---- worker and manager terms ----------------------------------------------------------

var testCmdRe = regexp.MustCompile(`(?i)\b(go test|go vet|npm (run )?test|npx (jest|vitest)|pnpm test|yarn test|pytest|python -m pytest|cargo test|make (test|check)|mvn test|gradle test|dotnet test|ctest|rspec|phpunit)\b`)

func squash(s string) string { return strings.Join(strings.Fields(s), " ") }

// workerTerms adds evidence, reread and scope to a worker's components.
func (s *scorer) workerTerms(ai int, c map[string]float64) {
	c[CompEvidence] = s.evidence(ai)
	c[CompReread] = -s.cfg.frac(sigOr(s.ep, rl.SigReReads, 0), CapReread)
	c[CompScope] = -s.cfg.frac(sigOr(s.ep, rl.SigScopeViolations, 0), CapScope)
}

// evidence is the share of the agent's done claims that were preceded by a run
// of the task's checks since its previous claim: "ran the checks it later
// cited". It is computed from the agent's own tool calls (a "task" call with
// action done is a claim; a shell command that runs a test tool or the task's
// verifier command is a run). Episodes recorded without tool calls fall back to
// the episode-level signals, min(1, verifier_runs / done_claims).
func (s *scorer) evidence(ai int) float64 {
	a := &s.ep.Agents[ai]
	verifier := squash(s.task.Verifier.Cmd)
	claims, backed, runsSince := 0, 0, 0
	any := false
	for si := range a.Steps {
		st := &a.Steps[si]
		if isFork(st) {
			continue
		}
		for _, c := range callsOf(ai, si, st) {
			any = true
			switch {
			case c.name == "task" && strings.EqualFold(inputString(c.input, "action"), "done"):
				claims++
				if runsSince > 0 {
					backed++
				}
				runsSince = 0
			case c.isShell():
				cmd := commandOf(c)
				if testCmdRe.MatchString(cmd) || (verifier != "" && strings.Contains(squash(cmd), verifier)) {
					runsSince++
				}
			}
		}
	}
	if any {
		if claims == 0 {
			return 0
		}
		return float64(backed) / float64(claims)
	}
	claimsSig, runsSig := sigOr(s.ep, rl.SigDoneClaims, 0), sigOr(s.ep, rl.SigVerifierRuns, 0)
	if claimsSig <= 0 {
		return 0
	}
	return clamp01(runsSig / claimsSig)
}

// managerTerms adds parallel_efficiency, duplicate_work, conflicts, idle and
// over_spawn to a manager's components.
func (s *scorer) managerTerms(_ int, c map[string]float64) {
	ep := s.ep
	_, workers := workSteps(ep)
	ws := sigOr(ep, rl.SigWorkerSteps, float64(workers))
	if cp := s.criticalPathSteps(); cp > 0 && ws > 0 {
		ratio := ws / cp
		capv := s.cfg.cap(CapParallel)
		if capv <= 0 {
			c[CompParallel] = 1
		} else {
			c[CompParallel] = clamp01(ratio / capv)
		}
	} else {
		c[CompParallel] = 0
	}
	c[CompDuplicate] = -s.cfg.frac(sigOr(ep, rl.SigDuplicateWork, 0), CapDuplicate)
	c[CompConflicts] = -s.cfg.frac(sigOr(ep, rl.SigLeaseConflicts, 0), CapConflicts)
	c[CompIdle] = -s.cfg.frac(sigOr(ep, rl.SigIdleMs, 0), CapIdle)

	spawns := sigOr(ep, rl.SigSpawns, 0)
	if spawns <= 0 {
		for ai := range ep.Agents {
			if roleClass(ep.Agents[ai].Role) != rl.RoleManager && ep.Agents[ai].Parent != "" {
				spawns++
			}
		}
	}
	if noResult := sigOr(ep, rl.SigSpawnNoResult, 0); noResult > 0 && spawns > 0 {
		c[CompOverSpawn] = -clamp01(noResult / spawns)
	} else {
		c[CompOverSpawn] = 0
	}
}

// mailComponents scores message use from the episode's mail signals: the share of
// delivered messages that were acted on (weighted by the outcome, since a message
// is only useful if the team then succeeded) and the share that was ignored or
// duplicated.
func (s *scorer) mailComponents() map[string]float64 {
	ep := s.ep
	sent := sigOr(ep, rl.SigMailSent, 0)
	out := map[string]float64{CompMailUseful: 0, CompMailSpam: 0}
	if sent <= 0 {
		return out
	}
	ignored, dup := sigOr(ep, rl.SigMailIgnored, 0), sigOr(ep, rl.SigMailDuplicate, 0)
	used := clamp01((sent - ignored - dup) / sent)
	out[CompMailUseful] = used * s.outcomeScore()
	out[CompMailSpam] = -clamp01((ignored + dup) / sent)
	return out
}

func (s *scorer) outcomeScore() float64 {
	if s.hack {
		return 0
	}
	v, _ := verdictScore(s.ep.Outcome.Verifier)
	return v
}
