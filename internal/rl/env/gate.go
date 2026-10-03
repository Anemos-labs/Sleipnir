package env

import (
	"fmt"
	"math"
	"strconv"
	"strings"
)

// A Gate is a pass/fail rule on one metric of a Comparison: the change must not make that metric worse by more than a
// tolerance. It is what lets a nightly job or a pre-release check say "no regression" without a person reading a table.
//
// A gate fails only when the change is worse, worse by more than the tolerance, and the confidence interval of the
// paired difference excludes zero. A drop inside the noise does not fail it, because a gate that fires on noise gets
// switched off, and an interval that includes zero cannot tell a regression from a bad draw of the samples.
type Gate struct {
	Metric string
	// Tol is how much worse the point estimate may be. Absolute, in the metric's own units (0.05 is five points of
	// pass rate), or, when Relative, a fraction of A's value (0.25 is 25% more cost).
	Tol      float64
	Relative bool
}

// ParseGate reads "metric", "metric:0.05" or "metric:25%".
func ParseGate(s string) (Gate, error) {
	name, tol, has := strings.Cut(strings.TrimSpace(s), ":")
	g := Gate{Metric: strings.TrimSpace(name)}
	if g.Metric == "" {
		return g, fmt.Errorf("gate %q: a metric name is required (metric[:tolerance])", s)
	}
	if !has {
		return g, nil
	}
	tol = strings.TrimSpace(tol)
	if strings.HasSuffix(tol, "%") {
		g.Relative = true
		tol = strings.TrimSuffix(tol, "%")
	}
	v, err := strconv.ParseFloat(tol, 64)
	if err != nil || math.IsNaN(v) || math.IsInf(v, 0) || v < 0 {
		return g, fmt.Errorf("gate %q: the tolerance must be a number >= 0, optionally followed by %%", s)
	}
	if g.Relative {
		v /= 100
	}
	g.Tol = v
	return g, nil
}

// String formats an evaluation gate with optional absolute or percentage tolerance.
func (g Gate) String() string {
	switch {
	case g.Tol == 0:
		return g.Metric
	case g.Relative:
		return fmt.Sprintf("%s:%g%%", g.Metric, g.Tol*100)
	}
	return fmt.Sprintf("%s:%g", g.Metric, g.Tol)
}

// HigherIsBetter says which way a metric improves. The metrics of Compare are all either a quality (higher) or a cost
// or a fault rate (lower).
func HigherIsBetter(metric string) bool {
	switch {
	case metric == "pass_at_1", metric == "mean_score", metric == "mean_reward", strings.HasPrefix(metric, "pass_hat_"):
		return true
	}
	return false
}

// Verdict is how a metric moved: better, worse, or same (the interval includes zero, so the data cannot say).
func (m MetricDelta) Verdict() string {
	switch {
	case !m.Significant:
		return "same"
	case (m.Delta > 0) == HigherIsBetter(m.Name):
		return "better"
	}
	return "worse"
}

// Check applies the gate to a comparison. detail says why, in a line.
func (g Gate) Check(c Comparison) (ok bool, detail string) {
	m, found := c.Metric(g.Metric)
	if !found {
		names := make([]string, len(c.Metrics))
		for i, x := range c.Metrics {
			names[i] = x.Name
		}
		if c.Paired == 0 {
			return false, "no task was run by both, so nothing can be compared"
		}
		return false, fmt.Sprintf("the comparison has no metric %q (it has %s)", g.Metric, strings.Join(names, ", "))
	}
	worse := m.Delta
	if HigherIsBetter(g.Metric) {
		worse = -m.Delta
	}
	limit := g.Tol
	if g.Relative {
		limit = g.Tol * math.Abs(m.A)
	}
	switch {
	case worse <= 0:
		return true, fmt.Sprintf("%s did not get worse (%+.4g)", g.Metric, m.Delta)
	case worse <= limit:
		return true, fmt.Sprintf("%s is worse by %.4g, within the tolerance of %.4g", g.Metric, worse, limit)
	case !m.Significant:
		return true, fmt.Sprintf("%s is worse by %.4g, more than the tolerance of %.4g, but the interval [%+.4g, %+.4g] includes zero: within the noise", g.Metric, worse, limit, m.Low, m.High)
	}
	return false, fmt.Sprintf("%s got worse by %.4g (A %.4g, B %.4g), more than the tolerance of %.4g, and the interval [%+.4g, %+.4g] excludes zero", g.Metric, worse, m.A, m.B, limit, m.Low, m.High)
}
