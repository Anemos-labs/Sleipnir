package reward

import (
	"math"
	"strings"
	"testing"

	"github.com/anemos-labs/sleipnir/internal/rl"
)

// TestOutcomeIsImprovementOverTheRecordedBaseline: a verifier that gives the untouched start partial credit must not
// pay a policy for doing nothing; the outcome is the share of the remaining distance to a full pass that was covered.
func TestOutcomeIsImprovementOverTheRecordedBaseline(t *testing.T) {
	tests := []struct {
		name     string
		baseline float64
		verdict  rl.Verdict
		want     float64
	}{
		{"the start's own score earns nothing", 0.25, rl.Verdict{Score: 0.25}, 0},
		{"half of the remaining distance", 0.25, rl.Verdict{Score: 0.625}, 0.5},
		{"a full pass is a full outcome", 0.25, rl.Verdict{Pass: true, Score: 1}, 1},
		{"worse than the start is zero, not negative", 0.25, rl.Verdict{Score: 0.1}, 0},
		{"a passing verdict with score zero is still a full pass", 0.25, rl.Verdict{Pass: true, Score: 0}, 1},
		{"no baseline leaves the score as it is", 0, rl.Verdict{Score: 0.6}, 0.6},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			ep := singleAgent()
			v := tc.verdict
			v.Kind = "verifier"
			ep.Outcome.Verifier = &v
			task := &rl.Task{Verifier: rl.Verifier{Pass: "json-score", BaselineScore: tc.baseline}}
			mustScore(t, ep, task, DefaultConfig(), nil)
			if got := ep.Reward.Components[CompOutcome]; math.Abs(got-tc.want) > 1e-9 {
				t.Errorf("outcome = %v, want %v", got, tc.want)
			}
			if ep.Outcome.Verifier.Score != tc.verdict.Score {
				t.Errorf("the recorded verdict changed: %v", ep.Outcome.Verifier.Score)
			}
			if tc.baseline > 0 && !strings.Contains(strings.Join(ep.Reward.Notes, "\n"), "baseline") {
				t.Errorf("notes do not say the outcome was calibrated: %q", ep.Reward.Notes)
			}
		})
	}
}
