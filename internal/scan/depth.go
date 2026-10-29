package scan

import (
	"fmt"
	"strings"
)

// Depth is how hard a scan looks. It does not set a time: the size of the work comes from the inventory (how many functions,
// how complex), and the depth sets how small the shards are, how many agents must confirm a lead, and from which severity up a
// lead must also be reproduced.
type Depth struct {
	ID      string
	Title   string
	Summary string
	// ShardFuncs and ShardLines bound a shard: one agent reads all of it in one sitting.
	ShardFuncs, ShardLines int
	// Verifiers is how many agents, none of them the filer, must confirm a lead.
	Verifiers int
	// ProveFrom is the lowest severity a confirmed lead must be reproduced at to be a finding; "" asks for no reproduction.
	ProveFrom string
	// Defender says one verifier argues against the lead (looks for the guard the filer missed) before it is confirmed.
	Defender bool
}

// Depths are the depths a scan can have.
var Depths = []Depth{
	{ID: "survey", Title: "Survey", Summary: "large shards, one verifier per lead, no reproduction required",
		ShardFuncs: 120, ShardLines: 3000, Verifiers: 1},
	{ID: "standard", Title: "Standard", Summary: "medium shards, two verifiers per lead, critical and high findings reproduced in the sandbox",
		ShardFuncs: 60, ShardLines: 1500, Verifiers: 2, ProveFrom: SevHigh, Defender: true},
	{ID: "deep", Title: "Deep", Summary: "small shards, three verifiers per lead with a defender, every finding from medium up reproduced in the sandbox",
		ShardFuncs: 30, ShardLines: 800, Verifiers: 3, ProveFrom: SevMedium, Defender: true},
}

// DefaultDepth is the depth a scan gets when none is named.
const DefaultDepth = "standard"

// DepthByID returns the depth with this id.
func DepthByID(id string) (Depth, bool) {
	for _, d := range Depths {
		if d.ID == id {
			return d, true
		}
	}
	return Depth{}, false
}

// DepthIDs lists the depths' ids.
func DepthIDs() []string {
	out := make([]string, len(Depths))
	for i, d := range Depths {
		out[i] = d.ID
	}
	return out
}

func errDepth(id string) error {
	return fmt.Errorf("unknown depth %q (%s)", id, strings.Join(DepthIDs(), ", "))
}

// DepthInfo is the run's depth.
func (r *Run) DepthInfo() Depth {
	if d, ok := DepthByID(r.Depth); ok {
		return d
	}
	d, _ := DepthByID(DefaultDepth)
	return d
}

// Team is the team the scan wants: enough agents that every shard is in someone's hands, at least 8 so that verifiers are
// never the filers, at most 32.
func (r *Run) Team() int {
	n := (len(r.Shards) + 1) / 2
	return min(max(n, 8), 32)
}

// needsProof reports whether a lead of this severity must be reproduced at this depth.
func (d Depth) needsProof(sev string) bool {
	if d.ProveFrom == "" {
		return false
	}
	return sevRank(sev) <= sevRank(d.ProveFrom)
}

// sevRank is a severity's place in Severities, worst first; an unknown one ranks last.
func sevRank(s string) int {
	for i, v := range Severities {
		if v == s {
			return i
		}
	}
	return len(Severities)
}
