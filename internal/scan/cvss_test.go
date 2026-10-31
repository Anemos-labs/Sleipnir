package scan

import (
	"strings"
	"testing"
)

// The scores below are the ones FIRST's v3.1 calculator and NVD give for these vectors.
func TestCVSSScoreFollowsTheSpecification(t *testing.T) {
	for v, want := range map[string]float64{
		"AV:N/AC:L/PR:N/UI:N/S:U/C:H/I:H/A:H": 9.8,
		"AV:N/AC:L/PR:N/UI:N/S:C/C:H/I:H/A:H": 10.0,
		"AV:N/AC:L/PR:N/UI:R/S:C/C:L/I:L/A:N": 6.1,
		"AV:L/AC:L/PR:L/UI:N/S:U/C:H/I:H/A:H": 7.8,
		"AV:N/AC:H/PR:N/UI:N/S:U/C:H/I:N/A:N": 5.9,
		"AV:N/AC:L/PR:L/UI:N/S:U/C:H/I:N/A:N": 6.5,
		"AV:N/AC:L/PR:N/UI:N/S:U/C:N/I:N/A:H": 7.5,
		"AV:N/AC:L/PR:H/UI:N/S:U/C:H/I:H/A:H": 7.2,
		"AV:N/AC:L/PR:L/UI:N/S:C/C:L/I:L/A:N": 6.4,
		"AV:N/AC:L/PR:N/UI:N/S:C/C:L/I:N/A:N": 5.8,
		"AV:N/AC:L/PR:N/UI:N/S:C/C:L/I:L/A:N": 7.2,
		"AV:N/AC:L/PR:N/UI:N/S:U/C:L/I:N/A:N": 5.3,
		"AV:N/AC:L/PR:N/UI:R/S:U/C:H/I:H/A:H": 8.8,
		"AV:P/AC:L/PR:N/UI:N/S:U/C:H/I:H/A:H": 6.8,
		"AV:N/AC:H/PR:N/UI:N/S:U/C:H/I:H/A:H": 8.1,
		"AV:L/AC:H/PR:L/UI:N/S:U/C:H/I:H/A:H": 7.0,
		"AV:L/AC:L/PR:N/UI:N/S:U/C:N/I:N/A:N": 0.0,
	} {
		got, ok := CVSSScore(v)
		if !ok || got != want {
			t.Errorf("%s = %v (%v), want %v", v, got, ok, want)
		}
	}
}

func TestParseCVSSNormalizesAndRefuses(t *testing.T) {
	got, err := ParseCVSS(" a:h/i:h/c:h/s:u/ui:n/pr:n/ac:l/av:n ")
	if err != nil || got != "CVSS:3.1/AV:N/AC:L/PR:N/UI:N/S:U/C:H/I:H/A:H" {
		t.Fatalf("normalize: %q %v", got, err)
	}
	for v, want := range map[string]string{
		"": "no vector",
		"CVSS:3.0/AV:N/AC:L/PR:N/UI:N/S:U/C:H/I:H/A:H": "not supported",
		"CVSS:3.1/AV:N/AC:L/PR:N/UI:N/S:U/C:H/I:H":     "lacks A availability",
		"AV:X/AC:L/PR:N/UI:N/S:U/C:H/I:H/A:H":          "AV:X is not a value",
		"AV:N/AV:N/AC:L/PR:N/UI:N/S:U/C:H/I:H/A:H":     "AV is given twice",
		"AV:N/AC:L/PR:N/UI:N/S:U/C:H/I:H/A:H/E:P":      "E is not a base metric",
		"AV:N/AC:L/PR:N/UI:N/S:U/C:H/I:H/A":            "not a metric",
	} {
		if _, err := ParseCVSS(v); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%q: want an error with %q, got %v", v, want, err)
		}
	}
}

func TestCVSSBandIsTheRatingScale(t *testing.T) {
	for score, want := range map[float64]string{10: SevCritical, 9.0: SevCritical, 8.9: SevHigh, 7.0: SevHigh, 6.9: SevMedium, 4.0: SevMedium, 3.9: SevLow, 0.1: SevLow, 0: SevLow} {
		if got := CVSSBand(score); got != want {
			t.Errorf("%v = %s, want %s", score, got, want)
		}
	}
}

func TestSeverityComesFromTheScore(t *testing.T) {
	crit := "CVSS:3.1/AV:N/AC:L/PR:N/UI:N/S:U/C:H/I:H/A:H" // 9.8
	l := Lead{CVSS: crit, Impact: SevLow, Reach: ReachInternal, Exploit: ExploitDifficult}
	if l.Severity() != SevCritical {
		t.Fatalf("the vector decides, not the impact or the tiers: %s", l.Severity())
	}
	l.Reach = ReachUnreachable
	if l.Severity() != SevLow {
		t.Fatalf("an unreachable lead is low at most: %s", l.Severity())
	}
	f := FindingOf(&Run{}, &l)
	if !strings.Contains(ScoreLabel(f), "CVSS 9.8") || !strings.Contains(ScoreLabel(f), "capped at low") {
		t.Fatalf("the label says the score and the cap: %s", ScoreLabel(f))
	}
	if p := sarifProperties(f); p["security-severity"] != securitySeverity[SevLow] || p["cvssScore"] != 9.8 {
		t.Fatalf("a capped finding is not ranked above its severity in code scanning: %v", p)
	}
	l.Reach = ReachExternal
	if p := sarifProperties(FindingOf(&Run{}, &l)); p["security-severity"] != "9.8" {
		t.Fatalf("security-severity is the score: %v", p)
	}
}

func TestAVerifierCorrectsTheVectorOnlyDownward(t *testing.T) {
	crit := "CVSS:3.1/AV:N/AC:L/PR:N/UI:N/S:U/C:H/I:H/A:H"  // 9.8
	admin := "CVSS:3.1/AV:N/AC:L/PR:H/UI:N/S:U/C:H/I:H/A:H" // 7.2
	l := Lead{CVSS: crit, Reach: ReachExternal, Exploit: ExploitTrivial, Verdicts: []Verdict{{By: "a", Confirm: true, CVSS: admin}}}
	if l.Vector() != admin || l.Severity() != SevHigh {
		t.Fatalf("a confirming verifier's lower vector wins: %s %s", l.Vector(), l.Severity())
	}
	l = Lead{CVSS: admin, Reach: ReachExternal, Exploit: ExploitTrivial, Verdicts: []Verdict{{By: "a", Confirm: true, CVSS: crit}, {By: "b", Confirm: false, CVSS: "CVSS:3.1/AV:P/AC:H/PR:H/UI:R/S:U/C:L/I:N/A:N"}}}
	if l.Vector() != admin {
		t.Fatalf("a higher correction, or a rejecting verifier's, changes nothing: %s", l.Vector())
	}
	l = Lead{Impact: SevMedium, Reach: ReachExternal, Exploit: ExploitTrivial, Verdicts: []Verdict{{By: "a", Confirm: true, CVSS: crit}}}
	if l.Vector() != "" || l.Severity() != SevMedium {
		t.Fatalf("a lead filed without a vector keeps its impact rule: %q %s", l.Vector(), l.Severity())
	}
}

func TestFindingsRankByScoreWithinASeverity(t *testing.T) {
	fs := []Finding{}
	for _, c := range []struct{ id, v string }{{"L1", "AV:N/AC:L/PR:H/UI:N/S:U/C:H/I:H/A:H"}, {"L2", "AV:N/AC:L/PR:N/UI:R/S:U/C:H/I:H/A:H"}, {"L3", ""}, {"L4", "AV:N/AC:L/PR:N/UI:N/S:U/C:H/I:H/A:H"}} {
		l := Lead{ID: c.id, CVSS: c.v, Impact: SevHigh, Reach: ReachExternal, Exploit: ExploitTrivial}
		fs = append(fs, FindingOf(&Run{}, &l))
	}
	var ids []string
	for _, f := range bySeverity(fs) {
		ids = append(ids, f.ID+" "+SeverityLabel(f))
	}
	if got := strings.Join(ids, ", "); got != "L4 Critical 9.8, L2 High 8.8, L1 High 7.2, L3 High" {
		t.Fatalf("order: %s", got)
	}
}
