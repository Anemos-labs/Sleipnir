package swarm

import (
	"fmt"
	"slices"
	"strings"
)

// Where a pool stands and what the reader does next.
//
// A pool's state reaches its members as mail, one wake per step, and the protocol's rules come in the kickoff mail. In a run with
// four-member pools, every member opened turn after turn by working out its place again ("I'm fe-2, a member of pool T10 led by
// fe-1; I should check whether fe-1 has proposed"), inspected the pool to find out, called actions the pool did not have, and agreed
// to revisions it had not read; leaders gave the whole of a large task to themselves. So each member's live view carries one line
// per open pool it is in: the phase, the revision, who has voted, and the one action that is the reader's to take now, worked out
// from the board. The view is the one thing every request carries; the line changes only when the pool does.

// poolStepLines is the reader's line for each open pool it is a member of; nil when it is in none.
func poolStepLines(sn *Snapshot, agent string) []string {
	var out []string
	for _, t := range sn.Tasks {
		if t.Pool == nil || t.Status == StatusDone || t.Status == StatusFailed || !slices.Contains(t.Pool.Members, agent) {
			continue
		}
		out = append(out, poolStepLine(sn, t, agent))
	}
	return out
}

func poolStepLine(sn *Snapshot, t Task, agent string) string {
	p := *t.Pool
	lead := t.Owner == agent
	who := "you lead it"
	if !lead {
		who = "led by " + t.Owner + ", you are a member"
	}
	head := fmt.Sprintf("Pool %s (%s; members %s), %s", t.ID, who, strings.Join(p.Members, ", "), p.Phase)
	if p.Revision > 0 {
		head += fmt.Sprintf(" revision %d", p.Revision)
	}
	missing := missingVotes(p)
	voted := !slices.Contains(missing, agent)
	var next string
	switch p.Phase {
	case "planning":
		switch {
		case p.Agreement == "" && lead:
			next = fmt.Sprintf("propose the plan (pool propose id=%s text=<the shares of the work: for each, its files and the member who does it>). "+
				"Give each member a share when the work divides; keep it as one child of your own only when it is small (one owner, a handful of files)", t.ID)
		case p.Agreement == "":
			next = fmt.Sprintf("nothing is proposed yet, and only %s proposes. If the plan must account for something, mail %s once; then wait. Do not propose, agree or inspect until the proposal arrives", t.Owner, t.Owner)
		case len(missing) == 0 && lead:
			next = fmt.Sprintf("every member agreed: pool execute id=%s, then create the children and assign them", t.ID)
		case len(missing) == 0:
			next = fmt.Sprintf("every member agreed; %s executes it next. Wait", t.Owner)
		case voted:
			next = fmt.Sprintf("you agreed; waiting for %s. Wait", strings.Join(missing, ", "))
		case lead:
			next = fmt.Sprintf("agree to your own proposal (pool agree id=%s revision=%d); waiting for %s", t.ID, p.Revision, strings.Join(missing, ", "))
		default:
			next = fmt.Sprintf("read revision %d (task get id=%s), then agree to it (pool agree id=%s revision=%d) or mail %s what should change", p.Revision, t.ID, t.ID, p.Revision, t.Owner)
		}
	case "executing":
		var mine, open, unassigned []string
		children := 0
		for _, c := range sn.Tasks {
			if c.Parent != t.ID {
				continue
			}
			children++
			switch {
			case c.Owner == agent && c.Status != StatusDone && c.Status != StatusFailed:
				mine = append(mine, c.ID+" "+string(c.Status))
			case c.Owner == "" && c.Status == StatusTodo:
				unassigned = append(unassigned, c.ID)
			}
			if c.Status != StatusDone && c.Status != StatusFailed {
				open = append(open, c.ID)
			}
		}
		switch {
		case lead && children == 0:
			next = fmt.Sprintf("create one child per share of the plan (task create parent=%s role=%s with its files), claim yours (task claim) and assign each member its own (spawn agent=MEMBER task=CHILD)", t.ID, t.Role)
		case lead && len(unassigned) > 0:
			next = fmt.Sprintf("assign %s (spawn agent=MEMBER task=CHILD, or task claim for your own)", joinWords(unassigned, "and"))
		case len(mine) > 0:
			next = "work on your child " + joinWords(mine, "and") + ", and finish it with task done"
		case lead && len(open) == 0:
			next = fmt.Sprintf("every child is finished: accept those in review, then pool reconcile id=%s", t.ID)
		case lead:
			next = fmt.Sprintf("children still open: %s. Wait for them", joinWords(open, "and"))
		default:
			next = fmt.Sprintf("you have no open child; %s assigns the children. If you have no share and the plan gave you one, mail %s; otherwise wait", t.Owner, t.Owner)
		}
	case "reconciling":
		switch {
		case len(missing) == 0 && lead:
			next = fmt.Sprintf("every member confirmed: submit it (task done id=%s)", t.ID)
		case voted:
			next = fmt.Sprintf("you confirmed; waiting for %s. Wait", strings.Join(missing, ", "))
		case !slices.Contains(p.Inspected, agent):
			next = fmt.Sprintf("bring in the combined result (pool inspect id=%s), check it, then pool confirm id=%s revision=%d", t.ID, t.ID, p.Revision)
		default:
			next = fmt.Sprintf("check the combined result, then pool confirm id=%s revision=%d, or mail %s what is wrong", t.ID, p.Revision, t.Owner)
		}
	}
	if next == "" {
		return head
	}
	return head + ". Your next step: " + next + "."
}
