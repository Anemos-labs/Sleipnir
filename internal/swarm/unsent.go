package swarm

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/anemos-labs/sleipnir/internal/core"
	"github.com/anemos-labs/sleipnir/internal/tools"
)

// What an agent tried to send and the harness refused.
//
// A refused mail, task or note comes back to the model as a tool error, once, in the middle of a turn. Models read past it:
// a worker whose 30,000-character mail was refused went on as if the recipient had it, a manager whose task create was
// refused for its description spawned the task id it expected, and nobody noticed until the work that depended on it never
// came. The refusal is in the log, and the person reading the log sees what the model missed.
//
// So a refusal of something the agent meant to deliver (a mail, a task it creates, updates or hands in, a note, a spawn,
// a pool proposal) is kept and repeated at the top of the agent's live view, as "NOT SENT", until the agent does the same
// kind of thing successfully (a mail to the same recipient, a create, a done of the same task) or the record is old. The
// view is the one thing every request of the agent carries, so the refusal cannot be lost behind later tool output.

// unsentTTL is how long a refusal stays in the live view without a successful retry.
const unsentTTL = 20 * time.Minute

// maxUnsent is how many refusals one agent's view shows (the newest).
const maxUnsent = 4

type unsentRec struct {
	key, what, why string
	at             time.Time
}

type unsentBook struct {
	mu  sync.Mutex
	by  map[string][]unsentRec // agent -> its open refusals, oldest first
	now func() time.Time
}

func (b *unsentBook) record(agent, key, what, why string) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.by == nil {
		b.by = map[string][]unsentRec{}
	}
	recs := b.by[agent][:0:0]
	for _, r := range b.by[agent] {
		if r.key != key {
			recs = append(recs, r)
		}
	}
	recs = append(recs, unsentRec{key: key, what: what, why: why, at: b.now()})
	if len(recs) > maxUnsent {
		recs = recs[len(recs)-maxUnsent:]
	}
	b.by[agent] = recs
}

func (b *unsentBook) clear(agent, key string) {
	b.mu.Lock()
	defer b.mu.Unlock()
	recs := b.by[agent]
	out := recs[:0]
	for _, r := range recs {
		if r.key != key {
			out = append(out, r)
		}
	}
	if len(out) == 0 {
		delete(b.by, agent)
		return
	}
	b.by[agent] = out
}

// lines is the live-view block of agent's open refusals; empty when there are none.
func (b *unsentBook) lines(agent string) []string {
	b.mu.Lock()
	defer b.mu.Unlock()
	now := b.now()
	var keep []unsentRec
	for _, r := range b.by[agent] {
		if now.Sub(r.at) < unsentTTL {
			keep = append(keep, r)
		}
	}
	if len(keep) == 0 {
		delete(b.by, agent)
		return nil
	}
	b.by[agent] = keep
	out := []string{"NOT SENT: the harness refused these calls and nothing happened: nobody has what they carried until you send it again in a form it accepts:"}
	for _, r := range keep {
		out = append(out, fmt.Sprintf("- %s, %s ago: %s", r.what, now.Sub(r.at).Round(time.Minute).String(), r.why))
	}
	return out
}

// unsentKey says what kind of delivery a swarm tool call is, for the refusal record: the key a later success of the same
// kind clears, and how the record names it. ok false: the call delivers nothing (a read, a list, a wait).
func unsentKey(tool string, input json.RawMessage) (key, what string, ok bool) {
	var in struct {
		Action, ID, To, Read, Task, Agent string
		Withdraw                          int
	}
	_ = json.Unmarshal(input, &in)
	id := cleanText(strings.TrimSpace(in.ID), 20)
	switch tool {
	case "mail":
		if strings.TrimSpace(in.Read) != "" {
			return "", "", false
		}
		to := cleanText(strings.TrimSpace(in.To), 40)
		return "mail:" + to, "your mail to " + to, true
	case "note":
		if in.Withdraw != 0 {
			return "", "", false
		}
		return "note", "your note", true
	case "spawn":
		task := cleanText(strings.TrimSpace(in.Task), 60)
		return "spawn:" + task, "your spawn of " + task, true
	case "task":
		switch in.Action {
		case "create":
			return "task:create", "your task create", true
		case "deliver":
			return "task:deliver", "your task deliver", true
		case "done", "update", "block", "handover", "revise":
			return "task:" + in.Action + ":" + id, "your task " + in.Action + " of " + id, true
		}
	case "pool":
		switch in.Action {
		case "propose", "split", "reconcile":
			return "pool:" + in.Action + ":" + id, "your pool " + in.Action + " on " + id, true
		}
	}
	return "", "", false
}

// unsentTool records the refusals of a swarm tool's deliveries (see the top of this file).
type unsentTool struct {
	inner tools.Tool
	s     *Swarm
}

// Spec is the wrapped tool's.
func (t unsentTool) Spec() core.ToolSpec { return t.inner.Spec() }

// Run runs the wrapped tool and keeps or clears the agent's refusal record for the call's kind of delivery.
func (t unsentTool) Run(ctx context.Context, c *tools.Call) (*tools.Result, error) {
	res, err := t.inner.Run(ctx, c)
	if err != nil || res == nil || c == nil || c.Env == nil || c.Env.Agent == "" {
		return res, err
	}
	key, what, ok := unsentKey(t.inner.Spec().Name, c.Input)
	if !ok {
		return res, err
	}
	if res.IsError {
		t.s.unsent.record(c.Env.Agent, key, what, cleanText(res.Text, maxReasonRunes))
	} else {
		t.s.unsent.clear(c.Env.Agent, key)
	}
	return res, err
}
