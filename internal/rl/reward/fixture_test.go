package reward

import (
	"bufio"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/reee344/sleipnir/internal/core"
	"github.com/reee344/sleipnir/internal/rl"
)

// The recorded run in internal/rl/traj/testdata/agent_run is real harness
// output: one agent, 27 model calls, four compactions (three of which fell back
// to the mechanical patch). traj turns it into an Episode; this package must not
// depend on traj, so the test decodes the events itself, keeping only what
// scoring consumes. It anchors the tests to reality: the numbers here come from
// a run of the actual harness, not from hand-built episodes.

type fxEvent struct {
	Seq   int             `json:"seq"`
	TS    time.Time       `json:"ts"`
	Agent string          `json:"agent"`
	Type  string          `json:"type"`
	Data  json.RawMessage `json:"data"`
}

func fixtureDir(t *testing.T) string {
	t.Helper()
	dir := filepath.Join("..", "traj", "testdata", "agent_run")
	if _, err := os.Stat(filepath.Join(dir, "events.jsonl")); err != nil {
		t.Skipf("fixture not available: %v", err)
	}
	return dir
}

func fxBlob(t *testing.T, dir string, h string) []byte {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(dir, "blobs", h[:2], h[2:4], h))
	if err != nil {
		t.Fatalf("blob %s: %v", h, err)
	}
	return b
}

func loadFixtureEpisode(t *testing.T) *rl.Episode {
	t.Helper()
	dir := fixtureDir(t)
	f, err := os.Open(filepath.Join(dir, "events.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	var events []fxEvent
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 1<<20), 8<<20)
	for sc.Scan() {
		var e fxEvent
		if err := json.Unmarshal(sc.Bytes(), &e); err != nil {
			t.Fatal(err)
		}
		events = append(events, e)
	}
	if err := sc.Err(); err != nil {
		t.Fatal(err)
	}

	outputs := map[string]string{} // tool call id -> output text the model saw
	for _, e := range events {
		if e.Type != "turn.append" {
			continue
		}
		var turn core.Turn
		if err := json.Unmarshal(e.Data, &turn); err != nil {
			t.Fatal(err)
		}
		for _, b := range turn.Blocks {
			if b.Kind == core.BlockToolResult {
				outputs[b.ToolID] = b.PlainText()
			}
		}
	}

	ep := &rl.Episode{Schema: rl.SchemaEpisode, ID: "fixture/0", TaskID: "fixture", Group: "fixture@mock"}
	agent := rl.Agent{ID: "be-1", Role: "backend"}
	segment := 0
	idx := map[string]int{} // req id -> step index
	var lastMain = -1
	commits, rejects := 0, 0
	for _, e := range events {
		switch e.Type {
		case "model.request":
			var d struct {
				Req  string `json:"req"`
				Kind string `json:"kind"`
				Role string `json:"role"`
				Wire string `json:"wire_hash"`
			}
			if err := json.Unmarshal(e.Data, &d); err != nil {
				t.Fatal(err)
			}
			st := rl.Step{ID: d.Req, Kind: d.Kind, Role: d.Role, Model: "mock-1", Segment: segment, Epoch: segment, At: e.TS, Trainable: true}
			st.Prompt = rl.PromptRef{Req: d.Req, WireHash: core.Hash(d.Wire)}
			if d.Kind == rl.KindCompactor {
				st.Segment, st.Epoch = 0, 0 // set below from the previous main step
				if lastMain >= 0 {
					st.Segment, st.Epoch = agent.Steps[lastMain].Segment, agent.Steps[lastMain].Epoch
				}
				st.Role = rl.RoleCompactor
			} else {
				st.Role = rl.RoleWorker
				lastMain = len(agent.Steps)
			}
			idx[d.Req] = len(agent.Steps)
			agent.Steps = append(agent.Steps, st)
		case "model.response":
			var d struct {
				Req        string     `json:"req"`
				Stop       string     `json:"stop"`
				Completion string     `json:"completion"`
				TotalMs    int64      `json:"total_ms"`
				HitRatio   float64    `json:"hit_ratio"`
				Expected   int        `json:"expected_read"`
				Usage      core.Usage `json:"usage"`
			}
			if err := json.Unmarshal(e.Data, &d); err != nil {
				t.Fatal(err)
			}
			st := &agent.Steps[idx[d.Req]]
			st.Usage = d.Usage
			st.Prompt.Tokens = d.Usage.TotalInput()
			st.LatencyMs = d.TotalMs
			st.Cache = rl.CacheInfo{HitRatio: d.HitRatio, ExpectedRead: d.Expected}
			st.Completion.Stop = core.StopReason(d.Stop)
			if d.Completion != "" {
				var turn core.Turn
				if err := json.Unmarshal(fxBlob(t, fixtureDir(t), d.Completion), &turn); err != nil {
					t.Fatal(err)
				}
				st.Completion.Turn = turn
				for _, c := range turn.ToolCalls() {
					st.Observations = append(st.Observations, rl.Observation{ToolID: c.ToolID, Name: c.ToolName, Input: c.Input, Output: outputs[c.ToolID]})
				}
			}
		case "compact.commit":
			commits++
			segment++
		case "compact.reject":
			rejects++
		}
	}
	ep.Agents = []rl.Agent{agent}
	ep.Signals = map[string]float64{
		rl.SigRequests: float64(len(agent.Steps)), rl.SigCompactions: float64(commits), rl.SigCompactRejects: float64(rejects),
	}
	ep.Outcome = rl.Outcome{Verifier: &rl.Verdict{Kind: "verifier", Pass: true, Score: 1}, Claimed: "done"}
	return ep
}

func TestFixtureEpisodeShape(t *testing.T) {
	ep := loadFixtureEpisode(t)
	a := ep.Agents[0]
	main, comp := 0, 0
	for _, s := range a.Steps {
		if s.Kind == rl.KindCompactor {
			comp++
		} else {
			main++
		}
	}
	if main != 23 || comp != 4 {
		t.Fatalf("fixture shape: %d main, %d compactor steps", main, comp)
	}
	if ep.Signals[rl.SigCompactions] != 4 || ep.Signals[rl.SigCompactRejects] != 3 {
		t.Fatalf("signals: %v", ep.Signals)
	}
}

func TestScoreOnTheRecordedRun(t *testing.T) {
	ep := loadFixtureEpisode(t)
	task := &rl.Task{ID: "fixture", Kind: rl.TaskFix, Budget: rl.Budget{ITE: 400_000, Steps: 40, Requests: 60}}
	cfg := DefaultConfig()
	cfg.Probes = false
	mustScore(t, ep, task, cfg, nil)

	if ep.Cost.ITE <= 0 || !finite(ep.Cost.ITE) {
		t.Fatalf("ITE = %v", ep.Cost.ITE)
	}
	// 27 requests against a budget of 60, 23 steps against 40.
	near(t, "requests", ep.Reward.Components[CompRequests], -27.0/60)
	near(t, "time", ep.Reward.Components[CompTime], -23.0/40)
	// Three rejects out of four compactions, each unrepeatable per call: the
	// mechanical fallback replaced three patches, so each patch is valid with
	// probability 1/4; the JSON replies themselves are syntactically fine.
	valid := 0.0
	events := 0
	for k, v := range ep.Agents[0].Reward.Components {
		if strings.HasSuffix(k, "/valid") {
			near(t, k, v, 0.25)
			valid += v
			events++
		}
	}
	if events != 4 {
		t.Fatalf("compaction events scored: %d", events)
	}
	// The rewards are finite and the compactor steps carry their own.
	compRewards := 0
	for _, s := range ep.Agents[0].Steps {
		if s.Kind == rl.KindCompactor {
			if s.Reward == 0 || !finite(s.Reward) {
				t.Errorf("compactor step %s reward %v", s.ID, s.Reward)
			}
			compRewards++
		} else if s.Reward != 0 {
			t.Errorf("main step %s has a step reward", s.ID)
		}
	}
	if compRewards != 4 {
		t.Errorf("compactor steps = %d", compRewards)
	}
	if !finite(ep.Reward.Total) || !finite(ep.Agents[0].Reward.Total) {
		t.Errorf("totals: %v %v", ep.Reward.Total, ep.Agents[0].Reward.Total)
	}
}

func TestRepriceMatchesTheRecordedCacheBehaviour(t *testing.T) {
	// The recorded provider cached the growing prompt of one agent: each request
	// read the previous one and paid for the new tail. Replayed under an
	// Anthropic-like target the model must reproduce that structure: reads within
	// a few percent of what was recorded, and the rebases (new segments) paying a
	// rewrite. This validates the model against real harness output, not against
	// itself.
	ep := loadFixtureEpisode(t)
	m := anthropicLike()
	m.Cache.MinPrefixTokens = 512
	rep, err := Reprice(ep, m)
	if err != nil {
		t.Fatal(err)
	}
	if rep.Requests != 27 {
		t.Fatalf("requests = %d", rep.Requests)
	}
	recorded := rep.Recorded
	ratio := float64(rep.Read) / float64(recorded.Read)
	if ratio < 0.75 || ratio > 1.25 {
		t.Errorf("replayed reads %d vs recorded %d (ratio %.2f): the model diverges from the real cache behaviour", rep.Read, recorded.Read, ratio)
	}
	// Same tokens either way: uncached + read + write partitions the prompts.
	var prompts int64
	for _, a := range ep.Agents {
		for _, s := range a.Steps {
			prompts += int64(s.Prompt.Tokens)
		}
	}
	if rep.Input() != prompts {
		t.Errorf("input tokens %d vs prompts %d", rep.Input(), prompts)
	}
	// The four commits are rebases: the first request of each new segment writes.
	rebases := 0
	for ai, a := range ep.Agents {
		for si, s := range a.Steps {
			if s.Kind != rl.KindCompactor && s.Segment > 0 && (si == 0 || a.Steps[si-1].Segment != s.Segment && a.Steps[si-1].Kind != rl.KindCompactor || a.Steps[si-1].Segment != s.Segment) {
				if sb, ok := rep.StepOf(ai, si); ok && sb.Write5m+sb.Write1h > 1000 {
					rebases++
				}
			}
		}
	}
	if rebases < 3 {
		t.Errorf("expected the rebases to pay a rewrite, saw %d", rebases)
	}
	// Recorded output tokens are priced identically.
	if rep.Output != recorded.Output {
		t.Errorf("output tokens %d vs %d", rep.Output, recorded.Output)
	}
	// Under an OpenAI-like target (no write premium) the same run is cheaper than
	// under the Anthropic-like one only if its cache reads are cheaper; at least
	// the bill is finite and different.
	oa, _ := LookupTarget("openai")
	other, err := Reprice(ep, oa)
	if err != nil || other.ITE == rep.ITE || !finite(other.ITE) {
		t.Errorf("repricing under another target: %v %v", other.ITE, err)
	}
}
