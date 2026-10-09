package reward

import (
	"testing"
	"time"

	"github.com/anemos-labs/sleipnir/internal/rl"
)

// Waste and group_ite apply to a passing verdict only, and the outcome of a json-score task is the improvement over the
// start's recorded score: a run that leaves the start as it found it, or improves on it without passing, is not charged
// for waste out of the partial credit the start already had. A pass is charged out of its outcome.
func TestWasteIsChargedToPassesOnlyWhateverTheBaseline(t *testing.T) {
	cfg := DefaultConfig()
	cfg.Weights[CompWaste] = 0.3
	task := &rl.Task{Verifier: rl.Verifier{Pass: "json-score", BaselineScore: 0.5}}
	for _, tc := range []struct {
		name  string
		score float64
		want  float64
	}{
		{"the start's own score", 0.5, 0},
		{"worse than the start", 0.25, 0},
		{"half of the remaining distance", 0.75, 0}, // progress without a pass
		{"a full pass", 1, -0.5},                    // 5 events of a cap of 10, times an outcome of 1
	} {
		t.Run(tc.name, func(t *testing.T) {
			ep := paced("a", 10*time.Second)
			ep.Signals[rl.SigStuckStops], ep.Signals[rl.SigRepeatedReads] = 1, 4
			ep.Outcome.Verifier = &rl.Verdict{Kind: "verifier", Pass: tc.score == 1, Score: tc.score}
			mustScore(t, ep, task, cfg, NoDiffs{})
			near(t, "waste component", ep.Reward.Components[CompWaste], tc.want)
		})
	}
}
