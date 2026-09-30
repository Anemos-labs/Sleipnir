package demo

import (
	"bytes"
	"context"
	"strings"
	"testing"
	"time"
)

func TestDemoRunsATeamToCompletionAndShowsTheCacheAtWork(t *testing.T) {
	var out bytes.Buffer
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	rep, err := Run(ctx, Options{Topics: 8, Dir: t.TempDir(), Out: &out})
	if err != nil {
		t.Fatalf("%v\n%s", err, out.String())
	}
	t.Log("\n" + out.String())
	if rep.Tasks == 0 || rep.TasksDone != rep.Tasks {
		t.Fatalf("%d of %d tasks accepted", rep.TasksDone, rep.Tasks)
	}
	if len(rep.Summaries) != 8 {
		t.Errorf("summaries: %v", rep.Summaries)
	}
	if rep.Agents < 10 {
		t.Errorf("only %d agents took part", rep.Agents)
	}
	if rep.HitRatio < 0.6 {
		t.Errorf("hit ratio %.2f: the shared prefix is not being shared", rep.HitRatio)
	}
	if rep.FirstRequests == 0 || rep.FirstReads*2 < rep.FirstRequests {
		t.Errorf("only %d of %d workers' first requests were served from the cache", rep.FirstReads, rep.FirstRequests)
	}
	if rep.CostUSD <= 0 || rep.CostUSD >= rep.NoShareUSD || rep.NoShareUSD >= rep.NoCacheUSD {
		t.Errorf("cost %.4f, %.4f with private caches, %.4f with no cache: not ordered as the design claims", rep.CostUSD, rep.NoShareUSD, rep.NoCacheUSD)
	}
	for _, want := range []string{"agents", "tasks", "hit ratio", "private cache", "no caching"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("report lacks %q", want)
		}
	}
}
