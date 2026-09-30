package env

import (
	"fmt"
	"strings"
	"testing"
)

func TestParseGate(t *testing.T) {
	for _, tc := range []struct {
		in      string
		want    Gate
		wantErr string
	}{
		{in: "pass_at_1", want: Gate{Metric: "pass_at_1"}},
		{in: " pass_at_1 : 0.05 ", want: Gate{Metric: "pass_at_1", Tol: 0.05}},
		{in: "mean_usd:25%", want: Gate{Metric: "mean_usd", Tol: 0.25, Relative: true}},
		{in: "", wantErr: "metric name"},
		{in: ":0.1", wantErr: "metric name"},
		{in: "pass_at_1:-1", wantErr: "number >= 0"},
		{in: "pass_at_1:lots", wantErr: "number >= 0"},
		{in: "pass_at_1:NaN", wantErr: "number >= 0"},
		{in: "pass_at_1:", wantErr: "number >= 0"},
	} {
		got, err := ParseGate(tc.in)
		if tc.wantErr != "" {
			if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Errorf("ParseGate(%q) error %v, want one containing %q", tc.in, err, tc.wantErr)
			}
			continue
		}
		if err != nil || got != tc.want {
			t.Errorf("ParseGate(%q) = %+v, %v; want %+v", tc.in, got, err, tc.want)
		}
		if again, err := ParseGate(got.String()); err != nil || again != got {
			t.Errorf("%+v does not survive String and ParseGate: %q -> %+v, %v", got, got.String(), again, err)
		}
	}
}

func cmp(m ...MetricDelta) Comparison { return Comparison{Paired: 10, Metrics: m} }

func delta(name string, a, b, low, high float64) MetricDelta {
	return MetricDelta{Name: name, A: a, B: b, Delta: b - a, Low: low, High: high, Significant: low > 0 || high < 0, N: 10}
}

func TestGateFailsOnlyOnASignificantWorsening(t *testing.T) {
	for _, tc := range []struct {
		name string
		g    Gate
		c    Comparison
		ok   bool
		why  string
	}{
		{"a significant drop beyond the tolerance", Gate{Metric: "pass_at_1", Tol: 0.05}, cmp(delta("pass_at_1", 0.70, 0.60, -0.15, -0.05)), false, "excludes zero"},
		{"a drop within the tolerance", Gate{Metric: "pass_at_1", Tol: 0.15}, cmp(delta("pass_at_1", 0.70, 0.60, -0.15, -0.05)), true, "within the tolerance"},
		{"a drop that could be noise", Gate{Metric: "pass_at_1", Tol: 0.05}, cmp(delta("pass_at_1", 0.70, 0.60, -0.25, 0.04)), true, "within the noise"},
		{"an improvement", Gate{Metric: "pass_at_1"}, cmp(delta("pass_at_1", 0.60, 0.70, 0.02, 0.2)), true, "did not get worse"},
		{"no tolerance means any significant worsening", Gate{Metric: "pass_at_1"}, cmp(delta("pass_at_1", 0.70, 0.69, -0.02, -0.001)), false, ""},
		{"lower is better for cost: 40% more", Gate{Metric: "mean_usd", Tol: 0.25, Relative: true}, cmp(delta("mean_usd", 0.010, 0.014, 0.002, 0.006)), false, "got worse"},
		{"lower is better for cost: 40% more is fine at 50%", Gate{Metric: "mean_usd", Tol: 0.5, Relative: true}, cmp(delta("mean_usd", 0.010, 0.014, 0.002, 0.006)), true, "within the tolerance"},
		{"lower is better for cost: cheaper", Gate{Metric: "mean_usd"}, cmp(delta("mean_usd", 0.014, 0.010, -0.006, -0.002)), true, "did not get worse"},
		{"pass^k is a quality too", Gate{Metric: "pass_hat_3"}, cmp(delta("pass_hat_3", 0.5, 0.3, -0.3, -0.1)), false, ""},
		{"a fault rate going up", Gate{Metric: "hack_rate"}, cmp(delta("hack_rate", 0, 0.1, 0.02, 0.2)), false, ""},
		{"a metric the comparison lacks", Gate{Metric: "mean_ite2"}, cmp(delta("pass_at_1", 1, 1, 0, 0)), false, "no metric"},
		{"nothing paired", Gate{Metric: "pass_at_1"}, Comparison{}, false, "no task was run by both"},
	} {
		ok, why := tc.g.Check(tc.c)
		if ok != tc.ok || !strings.Contains(why, tc.why) {
			t.Errorf("%s: Check = %v %q, want %v containing %q", tc.name, ok, why, tc.ok, tc.why)
		}
	}
}

func TestVerdictReadsTheDirectionOfTheMetric(t *testing.T) {
	for _, tc := range []struct {
		m    MetricDelta
		want string
	}{
		{delta("pass_at_1", 0.5, 0.6, 0.01, 0.2), "better"},
		{delta("pass_at_1", 0.5, 0.4, -0.2, -0.01), "worse"},
		{delta("pass_at_1", 0.5, 0.4, -0.2, 0.1), "same"},
		{delta("mean_usd", 1, 2, 0.5, 1.5), "worse"},
		{delta("mean_usd", 2, 1, -1.5, -0.5), "better"},
		{delta("mean_wall_ms", 2, 1, -1.5, 0.5), "same"},
	} {
		if got := tc.m.Verdict(); got != tc.want {
			t.Errorf("%s %v: %s, want %s", tc.m.Name, tc.m, got, tc.want)
		}
	}
}

// The gate on real Compare output: a run against itself passes every gate, a clearly worse one fails the quality gate.
func TestGatesOnAComparisonOfReports(t *testing.T) {
	var a, worse []TaskResult
	for i := 0; i < 20; i++ {
		id := fmt.Sprintf("t%02d", i)
		a = append(a, row(id, 3, 3, 10))
		worse = append(worse, row(id, 3, 0, 10))
	}
	ra, rw := reportOf("m", a...), reportOf("m", worse...)
	g := Gate{Metric: "pass_at_1", Tol: 0.05}
	if ok, why := g.Check(Compare(ra, ra)); !ok {
		t.Errorf("a report against itself must pass: %s", why)
	}
	if ok, why := g.Check(Compare(ra, rw)); ok {
		t.Errorf("every task going from 3/3 to 0/3 must fail the gate: %s", why)
	}
}
