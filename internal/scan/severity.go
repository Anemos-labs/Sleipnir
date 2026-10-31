package scan

// Verdict roles.
const (
	RoleVerifier = "verifier" // checks the trace step by step and rules
	RoleDefender = "defender" // argues the lead is false: looks for the guard, the caller that cannot pass the input, the dead path
)

// reachCaution and exploitCaution order the tiers from the least to the most cautious.
var (
	reachCaution   = []string{ReachExternal, ReachInternal, ReachUnreachable}
	exploitCaution = []string{ExploitTrivial, ExploitModerate, ExploitDifficult, ExploitTheoretical}
)

func caution(order []string, v string) int {
	for i, x := range order {
		if x == v {
			return i
		}
	}
	return 0
}

// Reachability is the lead's reachability: the filer's, or a confirming verifier's correction when that is more cautious.
func (l *Lead) Reachability() string {
	v := l.Reach
	for _, vd := range l.latestVerdicts() {
		if vd.Confirm && vd.Reach != "" && caution(reachCaution, vd.Reach) > caution(reachCaution, v) {
			v = vd.Reach
		}
	}
	return v
}

// Exploitability is the lead's exploitability, corrected like Reachability.
func (l *Lead) Exploitability() string {
	v := l.Exploit
	for _, vd := range l.latestVerdicts() {
		if vd.Confirm && vd.Exploit != "" && caution(exploitCaution, vd.Exploit) > caution(exploitCaution, v) {
			v = vd.Exploit
		}
	}
	return v
}

// Vector is the lead's CVSS v3.1 base vector: the filer's, or a confirming verifier's correction when that scores lower. It is
// empty for a lead filed before leads carried vectors.
func (l *Lead) Vector() string {
	v := l.CVSS
	best, ok := CVSSScore(v)
	if !ok {
		v = ""
	}
	for _, vd := range l.latestVerdicts() {
		if !vd.Confirm || vd.CVSS == "" {
			continue
		}
		if s, good := CVSSScore(vd.CVSS); good && (v == "" || s < best) {
			v, best = vd.CVSS, s
		}
	}
	return v
}

// Score is the base score of the lead's vector, and false when it has none.
func (l *Lead) Score() (float64, bool) {
	v := l.Vector()
	if v == "" {
		return 0, false
	}
	return CVSSScore(v)
}

// Severity is computed, never chosen. With a CVSS vector it is the band of the vector's base score; without one (a lead filed
// before leads carried vectors) it is the impact, one level lower when only an insider reaches the code and one lower again when
// exploiting it is difficult. Either way it is low at most when no path reaches the code or no practical exploit was shown,
// since a base score assumes the code is reached.
func (l *Lead) Severity() string {
	reach, exploit := l.Reachability(), l.Exploitability()
	var rank int
	if score, ok := l.Score(); ok {
		rank = sevRank(CVSSBand(score))
	} else {
		rank = sevRank(l.Impact)
		if rank >= len(Severities) {
			rank = len(Severities) - 1
		}
		if reach == ReachInternal {
			rank++
		}
		if exploit == ExploitDifficult {
			rank++
		}
	}
	if reach == ReachUnreachable || exploit == ExploitTheoretical {
		rank = len(Severities) - 1
	}
	return Severities[min(rank, len(Severities)-1)]
}

// latestVerdicts is each agent's latest verdict: an agent that changed its mind counts once, as it ruled last.
func (l *Lead) latestVerdicts() []Verdict {
	seen := map[string]int{}
	var out []Verdict
	for _, v := range l.Verdicts {
		if i, ok := seen[v.By]; ok {
			out[i] = v
			continue
		}
		seen[v.By] = len(out)
		out = append(out, v)
	}
	return out
}

// Tally counts the latest verdicts.
func (l *Lead) Tally() (confirms, rejects int, defended bool) {
	for _, v := range l.latestVerdicts() {
		if v.Confirm {
			confirms++
			if v.Role == RoleDefender {
				defended = true
			}
		} else {
			rejects++
		}
	}
	return
}

// Reproduced reports whether a proof reproduced the lead. Only a reproducer written by an agent other than the filer counts:
// the one who believes in a lead is the last one to test it.
func (l *Lead) Reproduced() bool {
	for _, p := range l.Proofs {
		if p.Outcome == ProofReproduced && p.By != l.By {
			return true
		}
	}
	return false
}

// Attempts counts the reproductions that count: those by agents other than the filer.
func (l *Lead) Attempts() int {
	n := 0
	for _, p := range l.Proofs {
		if p.By != l.By {
			n++
		}
	}
	return n
}

// Status is where the lead stands at depth d. A deferred lead stays deferred. It is rejected once as many verifiers reject it
// as the depth asks to confirm it, or once more reject than confirm after that many have ruled; it is confirmed once enough
// confirm, more than reject, a defender among them when the depth has one; a confirmed lead the depth wants reproduced is
// unproven until a proof reproduces it. A lead that is none of these after Depth.disputeAfter agents ruled on it is disputed;
// anything else is open.
func (l *Lead) Status(d Depth) string {
	if l.Deferred != "" {
		return LeadDeferred
	}
	c, r, defended := l.Tally()
	need := max(d.Verifiers, 1)
	if r >= need || (c+r >= need && r > c) {
		return LeadRejected
	}
	if c < need || c <= r || (d.Defender && !defended) {
		if c+r >= d.disputeAfter() {
			return LeadDisputed
		}
		return LeadOpen
	}
	if d.needsProof(l.Severity()) && !l.Reproduced() {
		return LeadUnproven
	}
	return LeadConfirmed
}

// RejectReason is the reason most of the rejecting verifiers gave, for a rejected lead.
func (l *Lead) RejectReason() string {
	n := map[string]int{}
	best := ""
	for _, v := range l.latestVerdicts() {
		if !v.Confirm {
			n[v.Reason]++
			if n[v.Reason] > n[best] {
				best = v.Reason
			}
		}
	}
	return best
}
