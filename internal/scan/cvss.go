package scan

import (
	"fmt"
	"math"
	"strings"
)

// CVSS v3.1 base scoring (https://www.first.org/cvss/v3.1/specification-document). A lead carries a base vector; the harness
// checks it and computes its score with the specification's formula, so two agents that agree on the metrics always get the
// same score, and the severity band comes from that score.

// cvssPrefix is the version label every stored vector starts with.
const cvssPrefix = "CVSS:3.1"

// cvssMetric is one base metric: its key, its name for an error message, and its values from the worst.
type cvssMetric struct {
	key, name string
	values    []string
}

// cvssMetrics are the eight base metrics, in the order the specification writes them.
var cvssMetrics = []cvssMetric{
	{"AV", "attack vector (N network, A adjacent, L local, P physical)", []string{"N", "A", "L", "P"}},
	{"AC", "attack complexity (L low, H high)", []string{"L", "H"}},
	{"PR", "privileges required (N none, L low, H high)", []string{"N", "L", "H"}},
	{"UI", "user interaction (N none, R required)", []string{"N", "R"}},
	{"S", "scope (U unchanged, C changed: the harm reaches past the vulnerable component)", []string{"U", "C"}},
	{"C", "confidentiality impact (H high, L low, N none)", []string{"H", "L", "N"}},
	{"I", "integrity impact (H high, L low, N none)", []string{"H", "L", "N"}},
	{"A", "availability impact (H high, L low, N none)", []string{"H", "L", "N"}},
}

// CVSSHelp is how a vector is written, for a tool description or an error.
const CVSSHelp = "a CVSS v3.1 base vector, CVSS:3.1/AV:_/AC:_/PR:_/UI:_/S:_/C:_/I:_/A:_ " +
	"(AV N network, A adjacent, L local, P physical; AC L low, H high; PR N none, L low, H high; UI N none, R required; " +
	"S U unchanged, C changed; C, I and A H high, L low, N none)"

// ParseCVSS checks a CVSS v3.1 base vector and returns it in the specification's form: the CVSS:3.1 prefix (optional on input),
// then the eight base metrics in order, each exactly once. Keys and values may be in any case and any order on input; temporal
// and environmental metrics are refused, since the scan scores the hole itself, not a deployment of it.
func ParseCVSS(v string) (string, error) {
	s := strings.ToUpper(strings.TrimSpace(v))
	if s == "" {
		return "", fmt.Errorf("no vector: give %s", CVSSHelp)
	}
	parts := strings.Split(s, "/")
	if strings.HasPrefix(parts[0], "CVSS:") {
		if parts[0] != cvssPrefix {
			return "", fmt.Errorf("%s is not supported: give %s", parts[0], CVSSHelp)
		}
		parts = parts[1:]
	}
	got := map[string]string{}
	for _, p := range parts {
		k, val, ok := strings.Cut(p, ":")
		if !ok {
			return "", fmt.Errorf("%q is not a metric (KEY:VALUE): give %s", p, CVSSHelp)
		}
		m := cvssMetricOf(k)
		if m == nil {
			return "", fmt.Errorf("%s is not a base metric: give only the eight base metrics, %s", k, CVSSHelp)
		}
		if _, dup := got[k]; dup {
			return "", fmt.Errorf("%s is given twice", k)
		}
		if !oneOf(val, m.values) {
			return "", fmt.Errorf("%s:%s is not a value of %s", k, val, m.name)
		}
		got[k] = val
	}
	var missing []string
	out := []string{cvssPrefix}
	for _, m := range cvssMetrics {
		val, ok := got[m.key]
		if !ok {
			missing = append(missing, m.key+" "+m.name)
			continue
		}
		out = append(out, m.key+":"+val)
	}
	if len(missing) > 0 {
		return "", fmt.Errorf("the vector lacks %s", strings.Join(missing, "; "))
	}
	return strings.Join(out, "/"), nil
}

func cvssMetricOf(k string) *cvssMetric {
	for i := range cvssMetrics {
		if cvssMetrics[i].key == k {
			return &cvssMetrics[i]
		}
	}
	return nil
}

// CVSSScore is the base score of a vector ParseCVSS accepts, from 0.0 to 10.0, by the v3.1 formula; false when the vector is
// not one it accepts.
func CVSSScore(v string) (float64, bool) {
	norm, err := ParseCVSS(v)
	if err != nil {
		return 0, false
	}
	m := map[string]string{}
	for _, p := range strings.Split(norm, "/")[1:] {
		k, val, _ := strings.Cut(p, ":")
		m[k] = val
	}
	changed := m["S"] == "C"
	av := map[string]float64{"N": 0.85, "A": 0.62, "L": 0.55, "P": 0.2}[m["AV"]]
	ac := map[string]float64{"L": 0.77, "H": 0.44}[m["AC"]]
	pr := map[string]float64{"N": 0.85, "L": 0.62, "H": 0.27}[m["PR"]]
	if changed {
		pr = map[string]float64{"N": 0.85, "L": 0.68, "H": 0.5}[m["PR"]]
	}
	ui := map[string]float64{"N": 0.85, "R": 0.62}[m["UI"]]
	cia := map[string]float64{"H": 0.56, "L": 0.22, "N": 0}
	iss := 1 - (1-cia[m["C"]])*(1-cia[m["I"]])*(1-cia[m["A"]])
	impact := 6.42 * iss
	if changed {
		impact = 7.52*(iss-0.029) - 3.25*math.Pow(iss-0.02, 15)
	}
	if impact <= 0 {
		return 0, true
	}
	exploitability := 8.22 * av * ac * pr * ui
	if changed {
		return roundUp(math.Min(1.08*(impact+exploitability), 10)), true
	}
	return roundUp(math.Min(impact+exploitability, 10)), true
}

// roundUp is the specification's Roundup: the smallest number with one decimal that is at least x, computed on integers so
// that floating-point error does not push a score up a tenth.
func roundUp(x float64) float64 {
	i := int64(math.Round(x * 100000))
	if i%10000 == 0 {
		return float64(i) / 100000
	}
	return float64(i/10000+1) / 10
}

// CVSSBand is the qualitative severity of a base score in the specification's rating scale: critical from 9.0, high from 7.0,
// medium from 4.0, low below. A score of 0.0 is rated none there; the scan reports it as low, the least of its severities.
func CVSSBand(score float64) string {
	switch {
	case score >= 9.0:
		return SevCritical
	case score >= 7.0:
		return SevHigh
	case score >= 4.0:
		return SevMedium
	}
	return SevLow
}

// CVSSCalculator is the address of FIRST's calculator showing a vector, for a reader who wants to see or adjust its metrics.
func CVSSCalculator(v string) string {
	return "https://www.first.org/cvss/calculator/3.1#" + v
}
