package env

import (
	"reflect"
	"testing"
	"time"

	"github.com/anemos-labs/sleipnir/internal/rl"
)

// The swarm board's closure counts reach the summary and the report next to the efficiency
// means, summed over completed rollouts only: a skipped or failed rollout closed nothing.
func TestSummaryAndReportSumTheBoardClosures(t *testing.T) {
	key := &rl.RankKey{Verified: true, Score: 1}
	results := []RolloutResult{
		{Task: "a", Sample: 0, Status: StatusOK, Pass: true, Rank: key, Closures: map[string]int{"done:verified": 2, "handed_off": 1}},
		{Task: "a", Sample: 1, Status: StatusOK, Rank: key, Closures: map[string]int{"done:verified": 1, "failed:superseded": 1}},
		{Task: "a", Sample: 2, Status: StatusInfra, Closures: map[string]int{"done:verified": 9}},
		{Task: "b", Sample: 0, Status: StatusSkipped},
		{Task: "c", Sample: 0, Status: StatusOK, Rank: key}, // a single agent: no board
	}
	tasks := []rl.Task{{ID: "a"}, {ID: "b"}, {ID: "c"}}
	want := map[string]int{"done:verified": 3, "handed_off": 1, "failed:superseded": 1}

	s := buildSummary("r", time.Unix(0, 0), time.Unix(1, 0), tasks, 3, results, nil, false)
	if !reflect.DeepEqual(s.Closures, want) {
		t.Errorf("summary closures = %v, want %v", s.Closures, want)
	}
	if rep := BuildReport(tasks, results, 3); !reflect.DeepEqual(rep.Closures, want) {
		t.Errorf("report closures = %v, want %v", rep.Closures, want)
	}
	if got := closuresOf(results[3:]); got != nil {
		t.Errorf("a run with no board closures reports %v, want none", got)
	}
}

// A rollout's closures are its episode's, copied: the result is written to the summary and must
// not share the map of an episode that may be kept and exported afterwards.
func TestAResultCarriesACopyOfTheEpisodesClosures(t *testing.T) {
	ep := &rl.Episode{TaskID: "t", Sample: 1, Outcome: rl.Outcome{Closures: map[string]int{"done:verified": 2}}}
	res := ResultFromEpisode(ep, nil)
	if !reflect.DeepEqual(res.Closures, map[string]int{"done:verified": 2}) {
		t.Fatalf("closures = %v", res.Closures)
	}
	res.Closures["done:verified"] = 7
	if ep.Outcome.Closures["done:verified"] != 2 {
		t.Errorf("the episode's closures changed through the result: %v", ep.Outcome.Closures)
	}
	if res := ResultFromEpisode(&rl.Episode{TaskID: "t"}, nil); res.Closures != nil {
		t.Errorf("an episode without closures gives %v", res.Closures)
	}
}
