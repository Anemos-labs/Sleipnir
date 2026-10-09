package swarm

import (
	"encoding/json"
	"fmt"
	"sort"

	"github.com/anemos-labs/sleipnir/internal/events"
)

// ReplayBoard rebuilds the board from the board.op events of a session log: tasks
// (every field), the agent roster and the pending notes. Alerts are transient (they
// expire) and are not rebuilt. The events must be in log order; ops of other types
// are ignored. It is how "the log is the truth" is checked: the snapshot it returns
// equals the live board's, alerts aside.
func ReplayBoard(evs []events.Event) (*Snapshot, error) {
	snap := &Snapshot{}
	taskAt := map[string]int{}
	upsert := func(t Task) {
		if i, ok := taskAt[t.ID]; ok {
			snap.Tasks[i] = t
			return
		}
		taskAt[t.ID] = len(snap.Tasks)
		snap.Tasks = append(snap.Tasks, t)
	}
	for _, e := range evs {
		if e.Type != events.TypeBoardOp {
			continue
		}
		var m struct {
			Op         string            `json:"op"`
			Version    uint64            `json:"version"`
			Task       string            `json:"task"`
			Status     string            `json:"status"`
			Owner      string            `json:"owner"`
			Line       string            `json:"line"`
			Result     string            `json:"result"`
			Evid       string            `json:"evidence"`
			Agreement  string            `json:"agreement"`
			Agreements []TaskAgreement   `json:"agreements"`
			Att        int               `json:"attempts"`
			Verify     int               `json:"verification_failures"`
			Rev        uint64            `json:"rev"`
			Files      []string          `json:"files"`
			Title      string            `json:"title"`
			Desc       string            `json:"desc"`
			Role       string            `json:"role"`
			Kind       string            `json:"kind"`
			Deps       []string          `json:"deps"`
			Agent      string            `json:"agent"`
			State      string            `json:"state"`
			Ctx        int               `json:"ctx_tokens"`
			Cost       float64           `json:"cost_usd"`
			Note       int               `json:"note"`
			Text       string            `json:"text"`
			Scope      string            `json:"scope"`
			Evicted    []int             `json:"evicted"`
			Notes      []int             `json:"notes"`
			Tasks      []string          `json:"tasks"`
			Owners     map[string]string `json:"owners"`
			Closure    Closure           `json:"closure"`
			BlockedOn  string            `json:"blocked_on"`
		}
		if err := json.Unmarshal(e.Data, &m); err != nil {
			return nil, fmt.Errorf("board.op #%d: %w", e.Seq, err)
		}
		snap.Version = m.Version
		switch m.Op {
		case "create", "claim", "assign", "update", "scope", "finish", "block", "resume", "requeue":
			if m.Task == "" {
				// RequeueOwned names the tasks it returned; the per-task state follows in
				// the event of the individual requeue when there is one.
				if m.Op == "requeue" {
					for _, id := range m.Tasks {
						if i, ok := taskAt[id]; ok {
							t := snap.Tasks[i]
							t.Status, t.Owner, t.Line, t.Rev = StatusTodo, m.Owners[id], m.Line, m.Version
							t.BlockedOn = ""
							snap.Tasks[i] = t
						}
					}
					continue
				}
				return nil, fmt.Errorf("board.op #%d (%s) names no task", e.Seq, m.Op)
			}
			var t Task
			if i, ok := taskAt[m.Task]; ok {
				t = snap.Tasks[i]
			}
			t.ID, t.Status, t.Owner, t.Line, t.Result, t.Evidence = m.Task, TaskStatus(m.Status), m.Owner, m.Line, m.Result, m.Evid
			t.Attempts, t.Rev, t.Files = m.Att, m.Rev, m.Files
			t.VerificationFailures = m.Verify
			t.Agreement, t.Agreements = m.Agreement, m.Agreements
			t.Closure, t.BlockedOn = m.Closure, m.BlockedOn
			if m.Op == "create" || (m.Op == "assign" && m.Title != "") {
				t.Title, t.Desc, t.Role, t.Deps = m.Title, m.Desc, m.Role, m.Deps
				t.Kind = m.Kind
			}
			upsert(t)
		case "agent":
			info := AgentInfo{ID: m.Agent, Role: m.Role, State: m.State, Task: m.Task, Line: m.Line, CtxTokens: m.Ctx, CostUSD: m.Cost}
			replaced := false
			for i := range snap.Agents {
				if snap.Agents[i].ID == info.ID {
					snap.Agents[i], replaced = info, true
				}
			}
			if !replaced {
				snap.Agents = append(snap.Agents, info)
				sort.Slice(snap.Agents, func(i, j int) bool { return snap.Agents[i].ID < snap.Agents[j].ID })
			}
		case "agent-remove":
			keep := snap.Agents[:0:0]
			for _, a := range snap.Agents {
				if a.ID != m.Agent {
					keep = append(keep, a)
				}
			}
			snap.Agents = keep
		case "note":
			drop := map[int]bool{}
			for _, id := range m.Evicted {
				drop[id] = true
			}
			keep := snap.Notes[:0:0]
			for _, n := range snap.Notes {
				if !drop[n.ID] {
					keep = append(keep, n)
				}
			}
			snap.Notes = append(keep, Note{ID: m.Note, From: e.Agent, Scope: m.Scope, Role: m.Role, Text: m.Text})
		case "notes-take":
			take := map[int]bool{}
			for _, id := range m.Notes {
				take[id] = true
			}
			keep := snap.Notes[:0:0]
			for _, n := range snap.Notes {
				if !take[n.ID] {
					keep = append(keep, n)
				}
			}
			snap.Notes = keep
		}
	}
	return snap, nil
}
