package scan

import (
	"fmt"
	"strings"
)

// Phases of a scan, in order.
const (
	PhaseMap    = "map"    // every shard read and mapped: what it does, its points, its edges
	PhaseHunt   = "hunt"   // every shard investigated for holes, leads filed with their traces
	PhaseVerify = "verify" // every lead ruled on, and reproduced where the depth asks
	PhaseReport = "report" // the report written from the ledger as it ends
	PhaseDone   = "done"
)

// Phases lists the phases in order.
var Phases = []string{PhaseMap, PhaseHunt, PhaseVerify, PhaseReport, PhaseDone}

// Problem is one thing a scan still needs.
type Problem struct {
	Phase string `json:"phase"`
	Ref   string `json:"ref,omitempty"`
	Text  string `json:"text"`
}

// Counts are a scan's numbers.
type Counts struct {
	Files      int            `json:"files"`
	Funcs      int            `json:"funcs"`
	Lines      int            `json:"lines"`
	Complexity int            `json:"complexity"`
	Shards     int            `json:"shards"`
	Mapped     int            `json:"mapped"`
	Hunted     int            `json:"hunted"`
	Points     int            `json:"points"`
	Lanes      map[string]int `json:"lanes"`
	Edges      int            `json:"edges"`
	Inferred   int            `json:"inferred"`
	Leads      int            `json:"leads"`
	Open       int            `json:"open"`
	Confirmed  int            `json:"confirmed"`
	Unproven   int            `json:"unproven"`
	Rejected   int            `json:"rejected"`
	Proofs     int            `json:"proofs"`
	Reproduced int            `json:"reproduced"`
	// BySeverity counts the confirmed findings.
	BySeverity map[string]int `json:"by_severity"`
}

// Report is a scan measured: its phase, what each phase still needs, and the numbers.
type Report struct {
	Phase    string    `json:"phase"`
	Problems []Problem `json:"problems"`
	Warnings []Problem `json:"warnings"`
	Counts   Counts    `json:"counts"`
}

// Blocking is how many problems stand between the scan and done.
func (rep Report) Blocking() int { return len(rep.Problems) }

// Check measures a scan. The phase is the first one with a problem; a phase can be worked before the one before it is done
// (a shard is hunted as soon as it is mapped), but the scan is only as far as its least finished phase.
func Check(r *Run) Report {
	d := r.DepthInfo()
	rep := Report{Counts: Counts{Lanes: map[string]int{}, BySeverity: map[string]int{}}}
	c := &rep.Counts
	add := func(phase, ref, format string, args ...any) {
		rep.Problems = append(rep.Problems, Problem{Phase: phase, Ref: ref, Text: fmt.Sprintf(format, args...)})
	}
	warn := func(phase, ref, format string, args ...any) {
		rep.Warnings = append(rep.Warnings, Problem{Phase: phase, Ref: ref, Text: fmt.Sprintf(format, args...)})
	}
	c.Files, c.Funcs, c.Shards = len(r.Inventory.Files), len(r.Inventory.Functions), len(r.Shards)
	for _, f := range r.Inventory.Files {
		c.Lines += f.Lines
		c.Complexity += f.Complexity
	}
	for _, s := range r.Shards {
		if s.Mapped != "" {
			c.Mapped++
		} else {
			add(PhaseMap, s.ID, "%s is not mapped (%d files, %d functions: %s)", s.ID, len(s.Files), s.Funcs, shardSpan(s))
		}
		if s.Hunted != "" {
			c.Hunted++
		} else {
			add(PhaseHunt, s.ID, "%s is not hunted (%s)", s.ID, shardSpan(s))
		}
	}
	c.Points, c.Edges = len(r.Points), len(r.Edges)
	for _, p := range r.Points {
		c.Lanes[p.Lane]++
	}
	for _, e := range r.Edges {
		if !e.Extracted() {
			c.Inferred++
		}
	}
	if c.Mapped == c.Shards {
		if c.Lanes[LaneEntry] == 0 {
			warn(PhaseMap, "", "no entry point is recorded: if the code takes input (routes, handlers, CLI arguments, files, messages), record where")
		}
		if c.Lanes[LaneSink] == 0 {
			warn(PhaseMap, "", "no sink is recorded: queries, commands, file paths, templates and outbound requests are sinks")
		}
	}
	c.Leads = len(r.Leads)
	for i := range r.Leads {
		l := &r.Leads[i]
		c.Proofs += len(l.Proofs)
		if l.Reproduced() {
			c.Reproduced++
		}
		conf, rej, defended := l.Tally()
		switch l.Status(d) {
		case LeadOpen:
			c.Open++
			var need []string
			if more := d.Verifiers - conf; more > 0 && rej <= conf {
				need = append(need, fmt.Sprintf("%d more %s", more, plural(more, "verdict", "verdicts")))
			} else if rej >= conf {
				need = append(need, "a verdict to break the tie")
			}
			if d.Defender && !defended {
				need = append(need, "a defender's verdict")
			}
			add(PhaseVerify, l.ID, "%s (%s, %s) needs %s, from agents other than %s", l.ID, l.Severity(), clip(l.Title, 60), strings.Join(need, " and "), l.By)
		case LeadUnproven:
			c.Unproven++
			if l.Attempts() == 0 {
				add(PhaseVerify, l.ID, "%s (%s, %s) is confirmed and must be reproduced in the sandbox at %s depth, by an agent other than %s: scan prove", l.ID, l.Severity(), clip(l.Title, 60), d.ID, l.By)
			} else {
				warn(PhaseVerify, l.ID, "%s (%s) was not reproduced in %d %s: it is reported as unproven", l.ID, l.Severity(), l.Attempts(), plural(l.Attempts(), "attempt", "attempts"))
			}
		case LeadConfirmed:
			c.Confirmed++
			c.BySeverity[l.Severity()]++
		case LeadRejected:
			c.Rejected++
		}
	}
	if !blocked(rep, PhaseVerify) && (r.Reported == nil || r.Reported.Version != r.Version) {
		add(PhaseReport, "", "the report is older than the ledger: scan report writes it")
	}
	rep.Phase = PhaseDone
	for _, ph := range Phases {
		if blocked(rep, ph) {
			rep.Phase = ph
			break
		}
	}
	return rep
}

// blocked reports whether a phase, or one before it, has a problem.
func blocked(rep Report, phase string) bool {
	for _, ph := range Phases {
		for _, p := range rep.Problems {
			if p.Phase == ph {
				return true
			}
		}
		if ph == phase {
			return false
		}
	}
	return false
}

// shardSpan names a shard's files briefly: the first and how many more.
func shardSpan(s Shard) string {
	if len(s.Files) == 0 {
		return "no files"
	}
	if len(s.Files) == 1 {
		return s.Files[0]
	}
	return fmt.Sprintf("%s and %d more", s.Files[0], len(s.Files)-1)
}

func plural(n int, one, many string) string {
	if n == 1 {
		return one
	}
	return many
}
