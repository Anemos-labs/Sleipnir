package session

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/anemos-labs/sleipnir/internal/agent"
	"github.com/anemos-labs/sleipnir/internal/core"
	"github.com/anemos-labs/sleipnir/internal/cost"
	"github.com/anemos-labs/sleipnir/internal/events"
	"github.com/anemos-labs/sleipnir/internal/goal"
	"github.com/anemos-labs/sleipnir/internal/kv"
	"github.com/anemos-labs/sleipnir/internal/plan"
	"github.com/anemos-labs/sleipnir/internal/provider"
	"github.com/anemos-labs/sleipnir/internal/scan"
	"github.com/anemos-labs/sleipnir/internal/swarm"
	"github.com/anemos-labs/sleipnir/internal/tools"
)

// GoalPlan is the plan of the agent the person talks to: the requirements of a standing goal, as that agent wrote them.
func (s *Session) GoalPlan() []plan.Item {
	a := s.Main()
	if a == nil || s.plans == nil {
		return nil
	}
	return s.plans.Get(a.ID())
}

// ClearGoalPlan empties the plan of the agent the person talks to: a new goal starts with none, so that the steps of the one before are not shown
// as its requirements (to the judge, to the agent, and in /goal).
func (s *Session) ClearGoalPlan() {
	if a := s.Main(); a != nil && s.plans != nil {
		s.plans.Set(a.ID(), nil)
	}
}

// JudgeGoal checks completion against searchable durable tool evidence across
// workers and turns. Inspection is read-only and bounded; failures never certify
// completion. worked reports tool activity in the latest agent turn.
//
// The judge runs on the judge role's model (--role-model judge=M, else models.roles.judge) when one is named, else on the
// session's own. A "done" from a named judge whose model is not the session's is confirmed by a second, independent judgment on
// the session's model: the goal is met only when both find it met, and a confirmation that does not agree sends the agent on with
// what that judgment found missing. Two models that agree are more often right than either alone, and a false "done" ends the work
// while a false "continue" costs one turn, so only "done" pays for the second opinion.
func (s *Session) JudgeGoal(ctx context.Context, g *goal.State) (v goal.Verdict, worked bool, err error) {
	if g == nil || g.StartSeq == 0 {
		return goal.Verdict{Kind: goal.Continue}, false, fmt.Errorf("goal has no durable evidence boundary; save it before starting work")
	}
	a := s.Main()
	if a == nil {
		return goal.Verdict{Kind: goal.Continue}, false, fmt.Errorf("no agent to judge")
	}
	answer, worked := turnActivity(a.Stack().Thread.Turns)
	cutoff, err := s.goalEvidenceCutoff()
	if err != nil {
		return goal.Verdict{Kind: goal.Continue}, false, err
	}
	first := judgeRunner{p: s.Provider, m: s.Model, label: "goal:judge"}
	var second *judgeRunner
	if s.judgeP != nil {
		first = judgeRunner{p: s.judgeP, m: s.judgeModel, label: "goal:judge"}
		if s.judgeModel.ID != s.Model.ID || s.judgeRef != s.modelRef {
			second = &judgeRunner{p: s.Provider, m: s.Model, label: "goal:confirm"}
		}
	}
	v, err = s.judgeOn(ctx, g, first, answer, cutoff)
	if err != nil {
		return goal.Verdict{Kind: goal.Continue}, worked, err
	}
	v = g.Reconcile(v) // a goal with milestones is met only when this verdict confirms each one
	confirm := ""
	if v.Kind == goal.Done && second != nil {
		c, err := s.judgeOn(ctx, g, *second, answer, cutoff)
		if err != nil {
			return goal.Verdict{Kind: goal.Continue}, worked, fmt.Errorf("confirming the judge's verdict: %w", err)
		}
		c = g.Reconcile(c)
		confirm = c.Kind
		v = goal.Confirm(v, c)
	}
	if v.Kind == goal.Done && s.Swarm != nil {
		snapshot := s.Swarm.Board.Snapshot()
		for _, task := range snapshot.Tasks {
			if goalTaskUnfinished(task, snapshot) {
				v = goal.Verdict{Kind: goal.Continue, Reason: "the task board still contains unfinished work", Left: []string{"Repair or explicitly resolve existing task " + task.ID + " before completing the goal"}}
				break
			}
		}
	}
	left := v.Left
	if left == nil {
		left = []string{}
	}
	data := map[string]any{"verdict": v.Kind, "reason": v.Reason, "left": left}
	if len(g.Milestones) > 0 {
		data["milestones"] = g.Milestones
	}
	if s.judgeP != nil {
		data["judge"] = first.m.ID
	}
	if confirm != "" {
		data["confirm"] = map[string]any{"model": second.m.ID, "verdict": confirm}
	}
	s.Log.Emit("", judgeEvent, data)
	return v, worked, nil
}

// judgeRunner is one model a goal is judged on: its client, its description and the label of its requests.
// judgeAttempts is how many times one judge request is sent when the failure is one that repeating may cure (a network error,
// a timeout, a 5xx or a rate limit). A judgment is many requests long; one dropped connection near its end paused the goal and threw
// away every evidence request before it.
const judgeAttempts = 4

// judgeDo sends one judge request, again while it fails in a way that repeating may cure, after the longer of a growing wait and
// the wait the server asked for, capped at agent.MaxRetryAfter. Each retry is a model.error in the log, as an agent's is.
func (s *Session) judgeDo(ctx context.Context, j judgeRunner, req *provider.Request) (*provider.Response, error) {
	for attempt := 0; ; attempt++ {
		resp, err := j.p.Do(ctx, req, nil)
		pe, ok := provider.AsError(err)
		if err == nil || !ok || !pe.Retryable() || attempt == judgeAttempts-1 || ctx.Err() != nil {
			return resp, err
		}
		delay := agent.RetryBase << attempt
		if after := min(pe.RetryAfter, agent.MaxRetryAfter); after > delay {
			delay = after // a server that says how long to wait is believed, up to the cap an agent's retries use
		}
		s.Log.Emit("", events.TypeModelError, map[string]any{
			"req": req.Label, "kind": pe.Kind.String(), "status": pe.Status, "attempt": attempt + 1, "delay_ms": delay.Milliseconds(),
		})
		select {
		case <-time.After(delay):
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
}

type judgeRunner struct {
	p     provider.Provider
	m     cost.Model
	label string
}

// judgeOn asks one judge for its verdict on goal g: the goal, the plan, the agent's last answer and the first page of the evidence
// archive, then as many read-only evidence requests as it makes (bounded by rounds, its context window and the session's budget).
// Its cost is the session's. An answer that is neither an evidence request nor a verdict is asked for again a few times; a judge
// that cannot be asked or read is an error, never a verdict.
func (s *Session) judgeOn(ctx context.Context, g *goal.State, j judgeRunner, answer string, cutoff uint64) (goal.Verdict, error) {
	evidence, err := s.goalEvidence(ctx, g, cutoff, evidenceRequest{})
	if err != nil {
		return goal.Verdict{Kind: goal.Continue}, err
	}
	prompt := &core.Prompt{
		Model:    j.m.ID,
		System:   []core.Block{core.Text(goal.JudgeSystem)},
		Params:   core.Params{MaxTokens: judgeMaxTokens},
		Messages: []core.Message{{Role: core.RoleUser, Blocks: []core.Block{core.Text(goal.JudgePrompt(g, s.GoalPlan(), answer, []string{evidence}))}}},
	}
	roster, err := s.goalEvidence(ctx, g, cutoff, evidenceRequest{Record: cutoff})
	if err != nil {
		return goal.Verdict{Kind: goal.Continue}, err
	}
	prompt.Messages[0].Blocks = append(prompt.Messages[0].Blocks, core.Text("Current harness roster (requested model identities, not proof of task correctness):\n"+roster))
	if s.Swarm != nil {
		var board strings.Builder
		for _, task := range s.Swarm.Board.Snapshot().Tasks {
			fmt.Fprintf(&board, "%s %s closure=%s target=%s %s\n", task.ID, task.Status, task.Closure.Kind(), task.Closure.Target(), task.Title)
		}
		prompt.Messages[0].Blocks = append(prompt.Messages[0].Blocks, core.Text("Current task board (status only, not proof of correctness):\n"+board.String()))
	}
	est := core.NewBytesEstimator()
	unread := 0  // answers in a row that were neither an evidence request nor a verdict
	empties := 0 // answers in a row with no text at all
	served := 0  // evidence requests answered
	var asked judgeAsked
	pressed := false // a verdict of continue before any evidence request was sent back once to search first
	for round := 0; round < judgeRounds+judgeRereads+2; round++ {
		if window := j.m.ContextTokens; window > 0 {
			tokens := est.Tokens(goal.JudgeSystem) + judgeMaxTokens
			for _, message := range prompt.Messages {
				for _, block := range message.Blocks {
					tokens += est.Tokens(block.PlainText()) + 8
				}
			}
			if tokens > window*3/4 {
				return goal.Verdict{Kind: goal.Continue}, fmt.Errorf("evidence inspection reached the model context budget")
			}
		}
		if budget := s.Budget(); budget > 0 && s.Cost() >= budget {
			return goal.Verdict{Kind: goal.Continue}, fmt.Errorf("session budget reached during evidence inspection")
		}
		resp, e := s.judgeDo(ctx, j, &provider.Request{Prompt: prompt, Label: j.label, NoStream: true})
		if e != nil {
			return goal.Verdict{Kind: goal.Continue}, e
		}
		usd := j.m.Price.USD(resp.Usage)
		if resp.CostUSD != nil {
			usd = *resp.CostUSD
		}
		s.mu.Lock()
		s.judgeUSD += usd
		s.judgeListUSD += j.m.ListUSD(resp.Usage)
		s.judgePlan = s.judgePlan || j.m.Plan
		s.mu.Unlock()
		response := resp.Turn.PlainText()
		req, inspect := judgeInspection(response)
		if !inspect {
			if pv, ok := goal.ParseVerdict(response); ok {
				if pv.Kind == goal.Continue && served == 0 && !pressed {
					pressed = true
					prompt.Messages = append(prompt.Messages,
						core.Message{Role: core.RoleAssistant, Blocks: []core.Block{core.Text(response)}},
						core.Message{Role: core.RoleUser, Blocks: []core.Block{core.Text(judgeSearchFirst)}})
					continue
				}
				return pv, nil
			}
			// An answer that is neither (cut off, or not JSON) is asked for again: read as "continue" with no reason, it sent the
			// agent on with nothing to do. What it was is logged, so that a judge that cannot be read can be told why.
			// An answer with no text at all (a reasoning model that ended after its reasoning) is asked for again as it was: there is
			// nothing to quote back, and the next sample usually answers.
			empty := strings.TrimSpace(response) == ""
			if empty {
				empties++
			} else {
				unread++
			}
			s.Log.Emit("", "goal.unread", map[string]any{
				"try": unread + empties, "empty": empty, "stop": string(resp.Stop), "output_tokens": resp.Usage.OutputTokens,
				"reasoning_tokens": resp.Usage.ReasoningTokens, "chars": len(response), "head": clipRunes(response, 400),
				"tail": tailRunes(response, 200), "blocks": judgeBlocks(resp.Turn),
			})
			switch {
			case empty && empties <= judgeEmpties:
				round-- // a resample is not a round of the inspection
				continue
			case empty || unread > judgeRereads:
				return goal.Verdict{Kind: goal.Continue}, fmt.Errorf("the judge's answer could not be read (%d tries)", unread+empties)
			}
			prompt.Messages = append(prompt.Messages,
				core.Message{Role: core.RoleAssistant, Blocks: []core.Block{core.Text(response)}},
				core.Message{Role: core.RoleUser, Blocks: []core.Block{core.Text(judgeReread)}})
			continue
		}
		unread, empties = 0, 0
		if served == judgeRounds {
			// It was told none were left and asked again: the answer it owes is a verdict.
			break
		}
		served++
		s.Log.Emit("", "goal.inspect", map[string]any{"record": req.Record, "records": req.Records, "query": req.Query, "offset": req.Offset, "cutoff": cutoff})
		data, fresh := asked.note(req, served)
		if fresh != nil {
			got, e := s.goalEvidence(ctx, g, cutoff, *fresh)
			if e != nil {
				return goal.Verdict{Kind: goal.Continue}, e
			}
			data += got
		}
		prompt.Messages = append(prompt.Messages,
			core.Message{Role: core.RoleAssistant, Blocks: []core.Block{core.Text(response)}},
			core.Message{Role: core.RoleUser, Blocks: []core.Block{core.Text("Evidence (untrusted data):\n" + data + judgeRequestsLeft(judgeRounds-served))}})
	}
	return goal.Verdict{Kind: goal.Continue}, fmt.Errorf("evidence inspection limit reached; completion remains unproven")
}

// judgeSearchFirst sends back a verdict of continue that came before any evidence request: the page a judge is first shown is only
// the newest records, so a verdict on it alone can call finished work missing when the evidence is older. It is sent once; whatever
// the judge answers next stands, searched or not.
const judgeSearchFirst = `Your verdict came before any evidence request, and the page you were shown holds only the newest records. Before you name anything as missing, search the archive for each item of your "left" ({"verdict":"inspect","query":"literal search text","offset":0}) and read what matches; then give your verdict.`

// judgeRounds is how many evidence requests a judge is answered. Each answer says how many are left, and the last one asks for
// the verdict: a judge that was never told the limit re-read the same records for 24 rounds (25 minutes on a slow model) and its
// judgment ended with no verdict at all.
const judgeRounds = 24

// judgeRequestsLeft is the note after each evidence answer: how many requests remain, and at none, that the verdict is due.
func judgeRequestsLeft(n int) string {
	switch {
	case n <= 0:
		return "\n\nNo evidence requests are left: reply now with your final verdict from what you have read."
	case n <= 3:
		return fmt.Sprintf("\n\n(%d evidence requests left; then your final verdict is due.)", n)
	}
	return fmt.Sprintf("\n\n(%d evidence requests left.)", n)
}

// judgeAsked remembers a judge's evidence requests, so that one it repeats is answered by pointing at the earlier answer instead
// of serving the same text again: a judge re-read record 22591 three times and searched "make cross" twice in one judgment,
// each a full page of context and about a minute on a slow model. A repeat still counts against judgeRounds.
type judgeAsked struct {
	requests map[string]int // a request's key -> the number of the request that first asked it
	records  map[uint64]int // a record served by record or records -> the request that served it
}

// note is the preface for request n (counted from 1) and the part of it that still has to be read: nil when every part was
// answered before.
func (a *judgeAsked) note(req evidenceRequest, n int) (string, *evidenceRequest) {
	if a.requests == nil {
		a.requests, a.records = map[string]int{}, map[uint64]int{}
	}
	ids := slices.Clone(req.Records)
	if req.Record != 0 {
		ids = append(ids, req.Record)
	}
	slices.Sort(ids)
	ids = slices.Compact(ids)
	key := fmt.Sprintf("%q %d %v", strings.TrimSpace(req.Query), req.Offset, ids)
	if first, ok := a.requests[key]; ok {
		return fmt.Sprintf("You made this same request as request %d; its answer is above and the archive has not changed since. Ask for something else or give your verdict.", first), nil
	}
	a.requests[key] = n
	if len(ids) == 0 || strings.TrimSpace(req.Query) != "" {
		return "", &req
	}
	var shown []string
	var unread []uint64
	for _, id := range ids {
		if first, ok := a.records[id]; ok {
			shown = append(shown, fmt.Sprintf("%d (request %d)", id, first))
			continue
		}
		a.records[id] = n
		unread = append(unread, id)
	}
	if len(shown) == 0 {
		return "", &req
	}
	preface := "Already shown above, not repeated: " + strings.Join(shown, ", ") + "."
	if len(unread) == 0 {
		return preface + " Ask for something else or give your verdict.", nil
	}
	fresh := req
	fresh.Record, fresh.Records = 0, unread
	if len(unread) == 1 {
		fresh.Record, fresh.Records = unread[0], nil
	}
	return preface + "\n", &fresh
}

// judgeMaxTokens bounds a judge's answer. A model that reasons before it answers spends part of it on that: at 1200 a reasoning
// judge could be cut off before its JSON, which read as "continue" with no reason.
const judgeMaxTokens = 4000

// judgeRereads is how many answers in a row the judge may give that are neither an evidence request nor a verdict before the
// judgment fails (and the goal pauses); judgeReread is what it is told each time.
const (
	judgeRereads = 2
	// judgeEmpties is how many answers in a row with no text are asked for again unchanged, beside judgeRereads.
	judgeEmpties = 3
	judgeReread  = `Your answer could not be read. Reply with one JSON object only: an evidence request {"verdict":"inspect",...} or a final verdict {"verdict":"done|continue|blocked","reason":"...","left":[...]}.`
)

// judgeInspection reads an evidence request out of a judge's answer, which may have words around it or a code fence, as a verdict
// may (goal.ParseVerdict).
func judgeInspection(text string) (req evidenceRequest, ok bool) {
	goal.EachObject(text, func(o json.RawMessage) bool {
		var r evidenceRequest
		if json.Unmarshal(o, &r) == nil && strings.EqualFold(strings.TrimSpace(r.Verdict), "inspect") {
			req, ok = r, true
			return false
		}
		return true
	})
	return req, ok
}

// goalTaskUnfinished distinguishes unresolved work from explicit dispositions.
// Superseded chains must terminate in a completed replacement; cycles, missing
// targets, blocked work and exhausted verification remain incomplete.
func goalTaskUnfinished(task swarm.Task, snapshot *swarm.Snapshot) bool {
	seen := map[string]bool{}
	following := false
	for {
		if seen[task.ID] {
			return true
		}
		seen[task.ID] = true
		if task.Status == swarm.StatusDone {
			return false
		}
		if task.Status != swarm.StatusFailed {
			return true
		}
		switch task.Closure.Kind() {
		case swarm.CloseCanceled, swarm.CloseDenied:
			return following
		case swarm.CloseSuperseded:
			following = true
			var ok bool
			task, ok = snapshot.Task(task.Closure.Target())
			if !ok {
				return true
			}
		default:
			return true
		}
	}
}

// judgeEvent is the log record of a judge's verdict: its kind, its reason and what it found missing, so that the history of a
// session shows the verdicts a standing goal had.
const judgeEvent = "goal.judge"

// turnActivity returns the latest answer and whether the latest user turn did
// tool work. Completion evidence itself comes from the durable log.
func turnActivity(turns []core.Turn) (answer string, worked bool) {
	for _, t := range turns {
		if t.Role == core.RoleUser && (t.Origin == "" || t.Origin == core.OriginUser) && strings.TrimSpace(kv.AnswerText(t)) != "" {
			worked = false
		}
		for _, b := range t.Blocks {
			if b.Kind == core.BlockToolResult {
				worked = true
			}
		}
		if t.Role == core.RoleAssistant {
			if txt := strings.TrimSpace(kv.AnswerText(t)); txt != "" {
				answer = txt
			}
		}
	}
	return answer, worked
}

// GoalTurn is what a standing goal does after a turn ended with turnErr: it judges the turn and returns a note for the person and, when the goal
// is not met, the message that sends the agent on. A turn that was cancelled or failed pauses the goal, and so does a judge that cannot be
// asked (the person resumes it or drops it). g is changed.
func (s *Session) GoalTurn(ctx context.Context, g *goal.State, turnErr error) (note, next string) {
	note, next, _ = s.GoalJudged(ctx, g, turnErr)
	return note, next
}

// GoalJudgment is what the judge of a standing goal said after a turn: whether it was asked and answered (Judged), its verdict,
// and the error when it could not be asked.
type GoalJudgment struct {
	Judged  bool
	Verdict goal.Verdict
	Err     error
}

// GoalJudged is GoalTurn with the judgment it was based on, for a host that shows the verdict as well as the note (the web
// interface's plan box). Judged is false when no judge was asked: no goal, a goal that is paused or met, a turn that was cancelled
// or failed.
func (s *Session) GoalJudged(ctx context.Context, g *goal.State, turnErr error) (note, next string, j GoalJudgment) {
	if g == nil || g.Paused != "" || g.Done {
		return "", "", j
	}
	// Each of these pauses leaves the turn unjudged: resuming judges it first (GoalResume).
	pause := func(why string) (string, string, GoalJudgment) {
		g.Paused, g.Unjudged = why, true
		return "goal paused: " + why + ". /goal resume goes on, /goal clear drops it", "", j
	}
	switch {
	case errors.Is(turnErr, context.Canceled):
		return pause("you interrupted it")
	case turnErr != nil:
		return pause("the turn failed")
	}
	v, worked, err := s.JudgeGoal(ctx, g)
	if err != nil {
		j.Err = err
		return pause("the judge could not be asked (" + short(tools.SanitizeForTerminal(err.Error()), 100) + ")")
	}
	j.Judged, j.Verdict = true, v
	reason := tools.SanitizeForTerminal(v.Reason)
	g.NoteVerdict(v)
	action := g.Apply(v, worked)
	if won, outcome, ok := goalOutcome(g, action); ok {
		s.goalEnded(ctx, g, won, outcome)
	}
	switch action {
	case goal.Stop:
		return "goal met: " + reason, "", j
	case goal.Pause:
		return "goal paused: " + g.Paused + ". /goal resume goes on, /goal clear drops it", "", j
	}
	if reason == "" {
		reason = "the judge's answer could not be read"
	}
	return fmt.Sprintf("goal not met yet: %s (continuation %d of %d)", reason, g.Turns, g.Max), s.GoalContinuation(g, v), j
}

// GoalResume takes the pause off g and returns a note for the person and the message that sends the agent on ("" when there is
// none). A goal paused on a turn that was not judged (it was interrupted or failed, the chat was restarted, or the judge could not be
// asked) is judged first: the judge's last verdict predates that turn, and its "not met yet" would send the agent to redo work it may
// have done since; j is that judgment (zero when there was none). While the team's board has unfinished tasks the judge is not asked,
// since it could not find the goal met, and the continuation names those tasks instead of the stale verdict. g is changed.
func (s *Session) GoalResume(ctx context.Context, g *goal.State) (note, next string, j GoalJudgment) {
	unjudged := g.Unjudged
	g.Paused, g.Repeats, g.Unjudged = "", 0, false
	g.Max = g.Turns + goal.MaxTurns
	if unjudged {
		if open := s.unfinishedGoalTasks(); len(open) > 0 {
			// The judge's verdict could only be "not met": a goal is not met while its board has unfinished work (JudgeGoal). Asking
			// it costs minutes of evidence reads before the team goes on; the last verdict is stale, so the continuation leaves it out.
			g.Turns++
			why := "the board still has unfinished tasks (" + strings.Join(open, ", ") + ")"
			return "goal resumed without judging the interrupted turn: " + why, s.GoalContinuation(g, goal.Verdict{Kind: goal.Continue, Reason: why}), j
		}
		if s.Swarm != nil && len(s.Swarm.Board.Snapshot().Tasks) == 0 {
			// The team was interrupted before its manager handed out any work (a restart during the first plan): there is nothing for
			// the judge to read yet, and its evidence reads would hold the team for minutes. The next turn's end is judged as usual.
			g.Turns++
			why := "the team has no tasks yet: the manager's plan was interrupted"
			return "goal resumed without judging the interrupted turn: " + why, s.GoalContinuation(g, goal.Verdict{Kind: goal.Continue, Reason: why}), j
		}
		return s.GoalJudged(ctx, g, nil)
	}
	g.Turns++
	return "", s.GoalContinuation(g, goal.Verdict{Kind: goal.Continue, Reason: g.Reason}), j
}

// unfinishedGoalTasks are the ids of the team's tasks that keep a goal from being met (goalTaskUnfinished), at most maxListed of them
// and then "+N more"; nil without a team.
func (s *Session) unfinishedGoalTasks() []string {
	if s.Swarm == nil {
		return nil
	}
	const maxListed = 5
	snapshot := s.Swarm.Board.Snapshot()
	var ids []string
	n := 0
	for _, task := range snapshot.Tasks {
		if goalTaskUnfinished(task, snapshot) {
			if n++; n <= maxListed {
				ids = append(ids, task.ID)
			}
		}
	}
	if n > maxListed {
		ids = append(ids, fmt.Sprintf("+%d more", n-maxListed))
	}
	return ids
}

// GoalContinuation is the message that sends the agent on after verdict v (goal.Continuation), with, for the manager of a team, the
// workers that are free (swarm.FreeCapacity).
func (s *Session) GoalContinuation(g *goal.State, v goal.Verdict) string {
	msg := goal.Continuation(g, s.GoalPlan(), v)
	if s.Swarm != nil {
		if line := s.Swarm.FreeCapacity(); line != "" {
			msg += "\n\n" + line
		}
	}
	return msg
}

// short retains at most n runes including an ellipsis when truncated; n must be positive.
func short(s string, n int) string {
	if r := []rune(s); len(r) > n {
		return string(r[:n-1]) + "…"
	}
	return s
}

// noteObjective remembers the objective of g (a cleared goal keeps the last one: the
// work it named is still what the session's branch holds).
func (s *Session) noteObjective(g *goal.State) {
	if g == nil || strings.TrimSpace(g.Objective) == "" {
		return
	}
	s.mu.Lock()
	s.objective = g.Objective
	s.mu.Unlock()
	if slug := scan.ObjectiveSlug(g.Objective); slug != "" && s.scan != nil {
		if _, err := s.scan.store.Bind(slug); err != nil {
			s.notice("", "scan: "+err.Error())
		}
	}
}

// goalObjective is the objective of the session's standing goal, "" when it never had one.
func (s *Session) goalObjective() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.objective
}

// goalEvent is the log record of a standing goal; a nil goal says it was cleared or met.
const goalEvent = "goal.state"

// SaveGoal records a resumable goal. Call it before its first turn to establish
// the objective boundary; JudgeGoal refuses a goal whose boundary was not saved.
func (s *Session) SaveGoal(g *goal.State) {
	if g != nil && g.StartSeq == 0 {
		g.StartSeq, _ = s.Log.Emit("", "goal.start", map[string]any{"objective": g.Objective})
	}
	if _, err := s.Log.Emit("", goalEvent, map[string]any{"goal": g}); err == nil {
		s.noteObjective(g)
	}
}

// LoadGoal is the goal this session had when its log last said so, made paused ("the chat was restarted"): nothing runs until the person
// says /goal resume. nil when there was none, or it was cleared or met.
func (s *Session) LoadGoal() *goal.State {
	if !s.Resumed() {
		return nil
	}
	_ = s.Log.Flush()
	var last *goal.State
	_ = events.Scan(filepath.Join(s.Dir, "events.jsonl"), func(e events.Event) error {
		if e.Type != goalEvent {
			return nil
		}
		var d struct {
			Goal *goal.State `json:"goal"`
		}
		if json.Unmarshal(e.Data, &d) == nil {
			if d.Goal != nil && d.Goal.StartSeq == 0 {
				d.Goal.StartSeq = e.Seq
				if last != nil && !last.Done && last.Objective == d.Goal.Objective {
					d.Goal.StartSeq = last.StartSeq
				}
			}
			last = d.Goal
		}
		return nil
	})
	if last != nil && last.Objective != "" && !last.Done {
		s.noteObjective(last)
		if last.Paused == "" {
			last.Paused, last.Unjudged = "the chat was restarted", true // a turn may have been cut short, and was not judged
		}
		return last
	}
	return nil
}

// judgeBlocks names the blocks of a judge's answer, a tool call with its name and the start of its input, for the log of an answer
// that could not be read.
func judgeBlocks(t core.Turn) []string {
	var out []string
	for _, b := range t.Blocks {
		switch b.Kind {
		case core.BlockToolUse:
			out = append(out, string(b.Kind)+" "+b.ToolName+" "+clipRunes(string(b.Input), 200))
		case core.BlockThinking:
			out = append(out, string(b.Kind)+" "+tailRunes(b.Text, 200))
		default:
			out = append(out, string(b.Kind))
		}
	}
	return out
}
