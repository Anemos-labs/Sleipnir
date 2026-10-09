package env

import (
	"testing"
	"time"

	"github.com/anemos-labs/sleipnir/internal/rl"
)

func TestSummaryNamesTheBestOfEachGroup(t *testing.T) {
	key := func(verified bool, ite, waste float64) *rl.RankKey {
		return &rl.RankKey{Verified: verified, Score: map[bool]float64{true: 1}[verified], ITE: ite, Waste: waste}
	}
	results := []RolloutResult{
		{Task: "a", Sample: 0, Status: StatusOK, Pass: true, ToolCalls: 4, Rank: key(true, 900, 0)},
		{Task: "a", Sample: 1, Status: StatusOK, Pass: true, ToolCalls: 4, Rank: key(true, 400, 1), RepeatedReads: 1},
		{Task: "a", Sample: 2, Status: StatusOK, ToolCalls: 12, StuckStops: 1, StuckWarnings: 1, Rank: key(false, 100, 9)},
		{Task: "a", Sample: 3, Status: StatusInfra},
		{Task: "b", Sample: 0, Status: StatusInfra},
	}
	tasks := []rl.Task{{ID: "a"}, {ID: "b"}}
	s := buildSummary("r", time.Unix(0, 0), time.Unix(1, 0), tasks, 4, results, nil, false)
	if b := s.PerTask[0].Best; b == nil || b.Sample != 1 || b.Key.ITE != 400 {
		t.Fatalf("best of a = %+v, want sample 1", b)
	}
	if s.PerTask[1].Best != nil {
		t.Errorf("a task with no completed rollout has no best: %+v", s.PerTask[1].Best)
	}
	e := s.Efficiency
	if e.ToolCalls != 20.0/3 || e.StuckStops != 1.0/3 || e.LoopRate != 1.0/3 || e.Waste != 10.0/3 || e.RepeatedReads != 1.0/3 {
		t.Errorf("efficiency over completed rollouts = %+v", e)
	}
}
