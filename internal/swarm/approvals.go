package swarm

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/anemos-labs/sleipnir/internal/core"
	"github.com/anemos-labs/sleipnir/internal/perm"
	"github.com/anemos-labs/sleipnir/internal/tools"
)

// The manager answers routine questions first.
//
// With Config.ManagerApproves, a worker's permission question that the engine marks routine (perm routine.go: it stays inside the
// workspace, reaches no network but this machine, and nothing guards it) is put to the manager before the person (DelegateApproval,
// the engine's Config.Delegate). The manager reads it as a message from the harness and answers with the approve tool: yes, no with
// an instruction (which the worker reads with the refusal, as a person's "no, and tell it what to do"), or escalate, which puts the
// question to the person. A question the manager has not answered within Config.ManagerApproveTimeout goes to the person too. What
// the engine does not mark routine (a protected path, an Ask rule, the network, a package install, anything high risk) never reaches
// the manager. The engine records the outcome as perm.decide with by "manager", and an answer of the manager is never remembered
// as a rule.

// ByManager is who decided a question the manager answered (perm.Audit.By, perm.decide's "by").
const ByManager = "manager"

// DefaultManagerApproveTimeout is how long a worker's question waits for the manager's answer before it goes to the person.
const DefaultManagerApproveTimeout = 60 * time.Second

// approvalAnswer is the manager's answer to one question.
type approvalAnswer struct {
	decision string // yes, no or escalate
	text     string // the instruction a no carries
}

// approvalQ is a question waiting for the manager.
type approvalQ struct {
	id, agent string
	ans       chan approvalAnswer // buffered 1, written once
}

// DelegateApproval puts a worker's routine question to the manager and waits for its answer (perm.Config.Delegate). It returns
// by "" when the question is not the manager's (manager approvals are off, there is no manager, the manager or a service agent asks,
// the asker is not a member) or when the manager escalated it, did not answer in time, or the swarm is closing.
func (s *Swarm) DelegateApproval(ctx context.Context, r perm.Request) (perm.Decision, string) {
	if !s.cfg.ManagerApproves {
		return perm.Decision{}, ""
	}
	return s.askManager(ctx, r, approvalText)
}

// FallbackApproval puts to the manager a worker's question that the person has left unanswered (perm.Config.Fallback, after
// perm.Config.FallbackAfter, for the kinds of question perm.Config.FallbackUpTo names). It returns by "" as DelegateApproval does:
// the question then goes on waiting for the person.
func (s *Swarm) FallbackApproval(ctx context.Context, r perm.Request) (perm.Decision, string) {
	return s.askManager(ctx, r, fallbackText)
}

// askManager puts a worker's question to the manager, worded by text, and waits for its answer (DelegateApproval).
func (s *Swarm) askManager(ctx context.Context, r perm.Request, text func(id string, r perm.Request, task string, ownTree bool, timeout time.Duration) string) (perm.Decision, string) {
	mgr := s.ManagerID()
	m := s.get(r.Agent)
	if mgr == "" || m == nil || m.manager || m.service || s.get(mgr) == nil {
		return perm.Decision{}, ""
	}
	q := &approvalQ{id: "A" + strconv.FormatInt(s.aseq.Add(1), 10), agent: r.Agent, ans: make(chan approvalAnswer, 1)}
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return perm.Decision{}, ""
	}
	if s.approvals == nil {
		s.approvals = map[string]*approvalQ{}
	}
	s.approvals[q.id] = q
	s.mu.Unlock()
	defer func() {
		s.mu.Lock()
		delete(s.approvals, q.id)
		s.mu.Unlock()
	}()
	timeout := s.cfg.ManagerApproveTimeout
	if timeout <= 0 {
		timeout = DefaultManagerApproveTimeout
	}
	s.notify(mgr, "request", text(q.id, r, currentTask(s.Board.Snapshot(), r.Agent), m.tree != nil, timeout))
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	select {
	case a := <-q.ans:
		switch a.decision {
		case "yes":
			return perm.Decision{Allow: true, Reason: "approved by the manager"}, ByManager
		case "no":
			return perm.Decision{Allow: false, Reason: perm.DeclinedByManager(a.text)}, ByManager
		}
		return perm.Decision{}, "" // escalated: the person decides
	case <-timer.C:
		s.notifyManager(q.id + " went to the person: no answer within " + timeout.String() + ".")
		return perm.Decision{}, ""
	case <-ctx.Done():
		return perm.Decision{}, ""
	case <-s.runCtx().Done():
		return perm.Decision{}, ""
	}
}

// approvalText is the message that puts a question to the manager. The command and the reason are the worker's request, which the
// manager reads as data.
func approvalText(id string, r perm.Request, task string, ownTree bool, timeout time.Duration) string {
	what := r.Command
	if what == "" {
		what = r.Summary
		if what == "" {
			what = r.Tool
		}
	}
	where := "in the project"
	if ownTree {
		where = "in its own tree"
	}
	who := r.Agent
	if task != "" {
		who += " (task " + task + ")"
	}
	reach := "It stays inside the workspace and reaches no network but this machine; nothing protected is involved. "
	if r.PackageIndex {
		reach = "It installs Python packages into a virtual environment inside the workspace: it reaches the package index, and the " +
			"install runs the packages' build code; nothing else leaves the workspace and nothing protected is involved. "
	}
	return fmt.Sprintf("Approval %s: %s asks to run %q %s. The permission engine asks because: %s. %s"+
		"Answer with approve id=%s decision=yes, decision=no with text= saying what to do instead, or decision=escalate to put it to the person. "+
		"With no answer within %s it goes to the person.",
		id, who, cleanText(what, 400), where, cleanText(r.Why, 300), reach, id, timeout)
}

// fallbackText is the message that puts to the manager a question the person left unanswered. Unlike a routine question it may
// reach beyond the workspace, so it says so, and that a no is the safe answer when in doubt.
func fallbackText(id string, r perm.Request, task string, ownTree bool, timeout time.Duration) string {
	what := r.Command
	if what == "" {
		what = r.Summary
		if what == "" {
			what = r.Tool
		}
	}
	where := "in the project"
	if ownTree {
		where = "in its own tree"
	}
	who := r.Agent
	if task != "" {
		who += " (task " + task + ")"
	}
	return fmt.Sprintf("Approval %s: the person has not answered a question for a while, so it is yours to decide for them. %s asks to "+
		"run %q %s. The permission engine asks because: %s. This is not a routine question: it may reach outside the workspace or the "+
		"network, so approve only what the goal needs and nothing you would not defend to the person; when in doubt, say no and say "+
		"what to do instead. Answer with approve id=%s decision=yes, decision=no with text=, or decision=escalate to leave it with the "+
		"person. With no answer within %s it stays with the person.",
		id, who, cleanText(what, 400), where, cleanText(r.Why, 300), id, timeout)
}

// answerApproval delivers the manager's answer to a waiting question.
func (s *Swarm) answerApproval(id string, a approvalAnswer) error {
	s.mu.Lock()
	q := s.approvals[id]
	if q != nil {
		delete(s.approvals, id)
	}
	s.mu.Unlock()
	if q == nil {
		return fmt.Errorf("no question %s is waiting (it was answered, went to the person, or its worker stopped asking)", cleanText(id, 20))
	}
	q.ans <- a
	return nil
}

// ---- approve ------------------------------------------------------------------

type approveTool struct{ s *Swarm }

// Spec declares the manager's answer to a worker's permission question.
func (t *approveTool) Spec() core.ToolSpec {
	return core.ToolSpec{
		Name:         "approve",
		NoTreeWrites: true, // the answer lets a worker's own action through; it writes nothing itself
		Description: "Manager only. Answer a worker's permission question that the harness put to you (\"Approval A3: ...\"): id, decision " +
			"(yes, no or escalate), text (with no: what the worker should do instead). Say yes to what the task needs inside the worker's tree; " +
			"escalate what you cannot judge, so the person decides.",
		InputSchema: json.RawMessage(`{"type":"object","properties":{"id":{"type":"string"},"decision":{"type":"string","enum":["yes","no","escalate"]},` +
			`"text":{"type":"string"}},"required":["id","decision"]}`),
	}
}

// Run delivers the manager's answer; any other role is refused.
func (t *approveTool) Run(_ context.Context, c *tools.Call) (*tools.Result, error) {
	if c.Env.Role != "manager" {
		return tools.Errorf("only the manager answers permission questions; your own questions go to the manager or the person"), nil
	}
	var in struct{ ID, Decision, Text string }
	if r := decode(c.Input, &in); r != nil {
		return r, nil
	}
	in.ID, in.Decision, in.Text = strings.TrimSpace(in.ID), strings.TrimSpace(in.Decision), strings.TrimSpace(in.Text)
	switch in.Decision {
	case "yes", "no", "escalate":
	default:
		return tools.Errorf("decision is yes, no or escalate"), nil
	}
	if in.Decision != "no" {
		in.Text = ""
	}
	t.s.noteCoordination(c.Env.Agent, "approve", "")
	if err := t.s.answerApproval(in.ID, approvalAnswer{decision: in.Decision, text: cleanText(in.Text, 1000)}); err != nil {
		return tools.Errorf("%v", err), nil
	}
	switch in.Decision {
	case "yes":
		return text("%s approved", in.ID), nil
	case "no":
		return text("%s refused; the worker reads your instruction", in.ID), nil
	}
	return text("%s goes to the person", in.ID), nil
}
