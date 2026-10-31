package scan

import "fmt"

// A scan ends. The work is sized by the inventory, and three rules keep the team from finding more to do forever:
//
//   - a budget: at most Depth.LeadsPerShard leads per shard are verified (Run.LeadBudget);
//   - diminishing returns: once every shard is hunted, only a few more leads are verified (Run.lateAllowance), and none once the
//     last lateYieldWindow of them that were ruled on were all rejected;
//   - a settled lead is final: it takes no more verdicts, and a lead that more verifiers than the depth asks for ruled on
//     without agreeing is disputed (Depth.disputeAfter), reported apart rather than argued over.
//
// A lead held back by the budget or by diminishing returns is deferred: it is kept in the ledger and listed in the report as
// not verified, and nothing more is needed for it. A critical lead is never deferred. When the ledger says done, the goal that
// drives the scan is met (the session judges a scan's goal from its ledger).

// minLeadBudget is the smallest lead budget, for a scan of a few shards.
const minLeadBudget = 24

// lateYieldWindow is how many late leads, the last ones ruled on, must all be rejected to close the intake of late leads.
const lateYieldWindow = 3

// LeadBudget is how many leads the scan verifies at most: LeadsPerShard per shard, and at least minLeadBudget.
func (r *Run) LeadBudget() int {
	per := r.DepthInfo().LeadsPerShard
	if per <= 0 {
		per = 6
	}
	return max(minLeadBudget, per*len(r.Shards))
}

// HuntClosed reports whether every shard has been hunted: a lead filed from then on is late.
func (r *Run) HuntClosed() bool {
	if len(r.Shards) == 0 {
		return false
	}
	for _, s := range r.Shards {
		if s.Hunted == "" {
			return false
		}
	}
	return true
}

// lateAllowance is how many late leads are verified, given how many leads the hunt itself filed: a tenth of them, at least 3.
func lateAllowance(hunted int) int { return max(3, hunted/10) }

// disputeAfter is how many agents' verdicts settle a lead they do not agree on: two more than the depth asks to confirm one.
func (d Depth) disputeAfter() int { return max(d.Verifiers, 1) + 2 }

// Intake is where the lead intake stands: how many leads are verified against the budget, and how many late ones against
// their allowance.
type Intake struct {
	Verified int `json:"verified"`
	Budget   int `json:"budget"`
	Late     int `json:"late"`
	// Allowance is the late leads verified at most; 0 until the hunt is closed.
	Allowance int `json:"allowance"`
	// Closed says no more late leads are verified (the allowance is spent, or the last ones were all rejected).
	Closed bool `json:"closed"`
	// Deferred counts the leads held back.
	Deferred int `json:"deferred"`
}

// Intake measures the lead intake.
func (r *Run) Intake() Intake {
	in := Intake{Budget: r.LeadBudget()}
	for i := range r.Leads {
		l := &r.Leads[i]
		switch {
		case l.Deferred != "":
			in.Deferred++
		case l.Late:
			in.Verified++
			in.Late++
		default:
			in.Verified++
		}
	}
	if r.HuntClosed() {
		in.Allowance = lateAllowance(in.Verified - in.Late)
		in.Closed = in.Late >= in.Allowance || r.lateYieldSpent()
	}
	return in
}

// lateYieldSpent reports whether the last lateYieldWindow late leads that were ruled on were all rejected: the hunt has stopped
// turning up holes.
func (r *Run) lateYieldSpent() bool {
	d := r.DepthInfo()
	n := 0
	for i := len(r.Leads) - 1; i >= 0 && n < lateYieldWindow; i-- {
		l := &r.Leads[i]
		if !l.Late || l.Deferred != "" {
			continue
		}
		switch l.Status(d) {
		case LeadOpen:
			continue
		case LeadRejected:
			n++
		default:
			return false
		}
	}
	return n == lateYieldWindow
}

// intake decides whether a new lead of severity sev is verified: it says whether the lead is late, and why it is deferred
// ("" when it is verified).
func (r *Run) intake(sev string) (late bool, deferred string) {
	late = r.HuntClosed()
	if sev == SevCritical {
		return late, ""
	}
	in := r.Intake()
	if in.Verified >= in.Budget {
		return late, fmt.Sprintf("the scan's budget of %d verified leads is spent", in.Budget)
	}
	if late && in.Closed {
		if in.Late >= in.Allowance {
			return late, fmt.Sprintf("every shard is hunted and the %d late leads the scan verifies are filed", in.Allowance)
		}
		return late, fmt.Sprintf("every shard is hunted and the last %d late leads ruled on were all rejected", lateYieldWindow)
	}
	return late, ""
}
