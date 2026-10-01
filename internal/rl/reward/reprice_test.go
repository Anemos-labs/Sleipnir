package reward

import (
	"fmt"
	"math"
	"math/rand"
	"testing"
	"time"

	"github.com/anemos-labs/sleipnir/internal/core"
	"github.com/anemos-labs/sleipnir/internal/cost"
	"github.com/anemos-labs/sleipnir/internal/rl"
)

// handExample is the small episode whose bill is worked out by hand below.
//
// Target: Anthropic-like (read 0.1, write 1.25 for 5m entries and 2.0 for the
// 1h entries used for shared layers, output 5). Shared prefix "g" is 4000 tokens.
//
//	A.1  t=0s   prompt 5000 (4000 shared + 1000 private) out 100
//	     cold: writes 4000 (1h) + 1000 (5m)              4000*2 + 1000*1.25 + 100*5 = 9750
//	A.2  t=30s  prompt 6000 out 100
//	     reads 5000, writes the 1000 delta (5m)          5000*0.1 + 1000*1.25 + 500  = 2250
//	B.1  t=40s  prompt 4500 (4000 shared + 500 private) out 50
//	     reads the shared 4000, writes 500 (5m)          4000*0.1 + 500*1.25 + 250   = 1275
//	                                                                          total = 13275
func handExample(bAt time.Duration) *rl.Episode {
	a := mkAgent("A", "worker",
		mkStep("A.1", withAt(0), withPrompt(5000, "g"), withOut(100)),
		mkStep("A.2", withAt(30*time.Second), withPrompt(6000, "g"), withOut(100)),
	)
	b := mkAgent("B", "worker",
		mkStep("B.1", withAt(bAt), withPrompt(4500, "g"), withOut(50)),
	)
	return mkEpisode("t/0", a, b)
}

func TestRepriceHandComputedExample(t *testing.T) {
	ep := handExample(40 * time.Second)
	rep, err := RepriceWith(ep, anthropicLike(), RepriceOptions{SharedTokens: map[string]int{"g": 4000}})
	if err != nil {
		t.Fatal(err)
	}
	near(t, "ITE", rep.ITE, 13275)
	if rep.Requests != 3 {
		t.Errorf("requests = %d", rep.Requests)
	}
	if rep.Read != 9000 || rep.Write5m != 2500 || rep.Write1h != 4000 || rep.Uncached != 0 || rep.Output != 250 {
		t.Errorf("tokens: %+v", rep.Bill)
	}
	want := map[string]float64{"A.1": 9750, "A.2": 2250, "B.1": 1275}
	for i, id := range []string{"A.1", "A.2", "B.1"} {
		ai, si := []int{0, 0, 1}[i], []int{0, 1, 0}[i]
		sb, ok := rep.StepOf(ai, si)
		if !ok {
			t.Fatalf("no bill for %s", id)
		}
		near(t, id, sb.ITE, want[id])
	}
	// The split adds up.
	near(t, "split", rep.UncachedITE+rep.ReadITE+rep.WriteITE+rep.OutputITE, rep.ITE)
	near(t, "read ite", rep.ReadITE, 900)
	near(t, "write ite", rep.WriteITE, 4000*2+2500*1.25)
	near(t, "output ite", rep.OutputITE, 250*5)
	// Per-agent and per-role tallies add up to the total.
	near(t, "agent A", rep.PerAgent["A"].ITE, 9750+2250)
	near(t, "agent B", rep.PerAgent["B"].ITE, 1275)
	near(t, "role worker", rep.PerRole["worker"].ITE, 13275)
	near(t, "kind main", rep.PerKind["main"].ITE, 13275)
	// Replay order is by time across agents.
	if rep.Steps[0].Step != "A.1" || rep.Steps[1].Step != "A.2" || rep.Steps[2].Step != "B.1" {
		t.Errorf("replay order: %v", []string{rep.Steps[0].Step, rep.Steps[1].Step, rep.Steps[2].Step})
	}
	if rep.Steps[2].At != 40*time.Second {
		t.Errorf("At = %v", rep.Steps[2].At)
	}
	// Dollar price: 3 USD/M input-equivalent tokens... check via the token classes.
	usd := anthropicLike().Price.USD(core.Usage{CacheReadTokens: 9000, CacheWrite5mTokens: 2500, CacheWrite1hTokens: 4000, OutputTokens: 250})
	near(t, "usd", rep.USD, usd)
	if rep.Target != "test-anthropic" {
		t.Errorf("target = %q", rep.Target)
	}
}

func TestRepriceSharedSizeInferredFromExpectedRead(t *testing.T) {
	ep := handExample(40 * time.Second)
	ep.Agents[1].Steps[0].Cache.ExpectedRead = 4000 // the planner expected the shared prefix warm
	rep, err := Reprice(ep, anthropicLike())
	if err != nil {
		t.Fatal(err)
	}
	near(t, "ITE with inferred shared size", rep.ITE, 13275)
	if rep.SharedTokens["g"] != 4000 {
		t.Errorf("shared tokens = %v", rep.SharedTokens)
	}
}

func TestRepriceSharedSizeInferredFromRecordedCacheRead(t *testing.T) {
	ep := handExample(40 * time.Second)
	ep.Agents[1].Steps[0].Usage = core.Usage{InputTokens: 500, CacheReadTokens: 4000}
	rep, err := Reprice(ep, anthropicLike())
	if err != nil {
		t.Fatal(err)
	}
	if rep.SharedTokens["g"] != 4000 {
		t.Errorf("shared tokens = %v", rep.SharedTokens)
	}
}

func TestRepriceSharedSizeFallbackWarns(t *testing.T) {
	ep := handExample(40 * time.Second)
	rep, err := Reprice(ep, anthropicLike())
	if err != nil {
		t.Fatal(err)
	}
	// No hint anywhere: the smallest first prompt (4500) is an upper bound.
	if rep.SharedTokens["g"] != 4500 {
		t.Errorf("shared tokens = %v", rep.SharedTokens)
	}
	if len(rep.Warnings) == 0 {
		t.Error("a guessed shared size must be reported")
	}
	// A prefix carried by one agent needs no size: nothing else can read it.
	solo := mkEpisode("t/1", mkAgent("A", "worker", mkStep("A.1", withAt(0), withPrompt(5000, "only-me"), withOut(10))))
	rep, _ = Reprice(solo, anthropicLike())
	if len(rep.SharedTokens) != 0 || len(rep.Warnings) != 0 {
		t.Errorf("single-agent prefix: %+v %v", rep.SharedTokens, rep.Warnings)
	}
}

func TestRepriceSharedSizeFromInlinedPrompt(t *testing.T) {
	ep := handExample(40 * time.Second)
	// Inline B's first prompt: tools + system + first message are the shared prefix.
	shared := core.Text(string(make([]byte, 0)) + fmt.Sprintf("%0*d", 900, 1)) // ~900 bytes
	ep.Agents[1].Steps[0].Inline = &core.Prompt{
		System: []core.Block{shared},
		Messages: []core.Message{
			{Role: core.RoleUser, Blocks: []core.Block{core.Text(fmt.Sprintf("%0*d", 100, 1))}},
		},
	}
	ep.Agents[1].Steps[0].Prompt.SharedMessages = 0 // only the system block is shared
	rep, err := Reprice(ep, anthropicLike())
	if err != nil {
		t.Fatal(err)
	}
	got := rep.SharedTokens["g"]
	if got <= 3500 || got >= 4500 {
		t.Errorf("inline-derived shared size = %d, want about 90%% of 4500", got)
	}
}

func TestRepriceConcurrentStartPaysColdWrites(t *testing.T) {
	// B starts 0.5s after A: A's write is not readable until its first byte, so B
	// writes the shared prefix again (first-byte readiness, exactly as in sim).
	ep := handExample(500 * time.Millisecond)
	rep, err := RepriceWith(ep, anthropicLike(), RepriceOptions{SharedTokens: map[string]int{"g": 4000}})
	if err != nil {
		t.Fatal(err)
	}
	var b StepBill
	for _, s := range rep.Steps {
		if s.Step == "B.1" {
			b = s
		}
	}
	near(t, "B cold", b.ITE, 4000*2+500*1.25+50*5)
	if b.Hit || b.Read != 0 {
		t.Errorf("B must not read: %+v", b)
	}
	// The warm gate (waiting for the first byte) is worth exactly the difference.
	warm, _ := RepriceWith(handExample(3*time.Second), anthropicLike(), RepriceOptions{SharedTokens: map[string]int{"g": 4000}})
	if warm.ITE >= rep.ITE {
		t.Errorf("waiting for the first byte must be cheaper: %v vs %v", warm.ITE, rep.ITE)
	}
}

func TestRepriceForkReadsTheAgentsChain(t *testing.T) {
	a := mkAgent("A", "worker",
		mkStep("A.1", withAt(0), withPrompt(5000, "g"), withOut(100)),
		mkStep("A.2", withAt(30*time.Second), withPrompt(6000, "g"), withOut(100)),
		// A compactor call: the agent's prompt plus a 1500-token instruction.
		mkStep("A.c1", withAt(40*time.Second), withKind(rl.KindCompactor), withPrompt(7500, "g"), withOut(80)),
	)
	rep, err := RepriceWith(mkEpisode("t/0", a), anthropicLike(), RepriceOptions{SharedTokens: map[string]int{"g": 4000}})
	if err != nil {
		t.Fatal(err)
	}
	c, _ := rep.StepOf(0, 2)
	// reads the 6000-token chain, bills the instruction uncached, stores nothing.
	if c.Read != 6000 || c.Uncached != 1500 || c.Write5m != 0 || c.Write1h != 0 {
		t.Errorf("fork bill: %+v", c.Bill)
	}
	near(t, "fork ITE", c.ITE, 600+1500+400)
	if c.Kind != rl.KindCompactor || c.Role != rl.RoleCompactor {
		t.Errorf("kind/role = %s/%s", c.Kind, c.Role)
	}
	near(t, "per-role compactor", rep.PerRole[rl.RoleCompactor].ITE, 2500)
}

func TestRepriceForkIssuedBeforeTheMainWriteIsReadable(t *testing.T) {
	// The fork fires 100ms after the main request it forks from: that request's
	// entries are not readable yet, so the fork falls back to the previous chain
	// end and pays for the delta (the fixture's compactor reads one step behind).
	a := mkAgent("A", "worker",
		mkStep("A.1", withAt(0), withPrompt(5000, "g"), withOut(10)),
		mkStep("A.2", withAt(30*time.Second), withPrompt(6000, "g"), withOut(10)),
		mkStep("A.c1", withAt(30*time.Second+100*time.Millisecond), withKind(rl.KindCompactor), withPrompt(7000, "g"), withOut(10)),
	)
	rep, _ := RepriceWith(mkEpisode("t/0", a), anthropicLike(), RepriceOptions{SharedTokens: map[string]int{"g": 4000}})
	c, _ := rep.StepOf(0, 2)
	if c.Read != 5000 {
		t.Errorf("fork should read the older entry: %+v", c.Bill)
	}
	if c.Write5m != 1000 {
		t.Errorf("fork writes the main step's delta again: %+v", c.Bill)
	}
	if c.Uncached != 1000 {
		t.Errorf("instruction uncached: %+v", c.Bill)
	}
}

func TestRepriceRebaseKeepsTheSharedPrefixWarm(t *testing.T) {
	a := mkAgent("A", "worker",
		mkStep("A.1", withAt(0), withPrompt(5000, "g"), withOut(10)),
		mkStep("A.2", withAt(30*time.Second), withPrompt(9000, "g"), withOut(10)),
		// after a compaction commit: a new segment, a much smaller prompt
		mkStep("A.3", withAt(60*time.Second), withSeg(1, 1), withPrompt(6000, "g"), withOut(10)),
		mkStep("A.4", withAt(90*time.Second), withSeg(1, 1), withPrompt(6400, "g"), withOut(10)),
	)
	rep, err := RepriceWith(mkEpisode("t/0", a), anthropicLike(), RepriceOptions{SharedTokens: map[string]int{"g": 4000}})
	if err != nil {
		t.Fatal(err)
	}
	a3, _ := rep.StepOf(0, 2)
	// The shared 4000 is still cached; the 2000 of rewritten history is a new write.
	if a3.Read != 4000 || a3.Write5m != 2000 {
		t.Errorf("rebase step: %+v", a3.Bill)
	}
	a4, _ := rep.StepOf(0, 3)
	if a4.Read != 6000 || a4.Write5m != 400 {
		t.Errorf("step after rebase: %+v", a4.Bill)
	}
}

func TestRepriceShrinkWithoutDeclaredRebaseStartsANewChain(t *testing.T) {
	a := mkAgent("A", "worker",
		mkStep("A.1", withAt(0), withPrompt(9000, ""), withOut(1)),
		mkStep("A.2", withAt(30*time.Second), withPrompt(5000, ""), withOut(1)), // smaller, same segment
	)
	rep, _ := Reprice(mkEpisode("t/0", a), anthropicLike())
	a2, _ := rep.StepOf(0, 1)
	if a2.Read != 0 || a2.Write5m != 5000 {
		t.Errorf("an undeclared shrink cannot hit the old chain: %+v", a2.Bill)
	}
}

func TestRepriceTTLExpiryKeepsOnlyTheLongLivedSharedLayer(t *testing.T) {
	a := mkAgent("A", "worker",
		mkStep("A.1", withAt(0), withPrompt(5000, "g"), withOut(10)),
		mkStep("A.2", withAt(10*time.Minute), withPrompt(5500, "g"), withOut(10)),
	)
	rep, _ := RepriceWith(mkEpisode("t/0", a), anthropicLike(), RepriceOptions{SharedTokens: map[string]int{"g": 4000}})
	a2, _ := rep.StepOf(0, 1)
	if a2.Read != 4000 || a2.Write5m != 1500 {
		t.Errorf("after 10 idle minutes only the 1h shared entry survives: %+v", a2.Bill)
	}
	// With a provider that has no longer TTL, everything is cold again.
	m := anthropicLike()
	m.Cache.TTLs = m.Cache.TTLs[:1]
	rep, _ = RepriceWith(mkEpisode("t/0", a), m, RepriceOptions{SharedTokens: map[string]int{"g": 4000}})
	a2, _ = rep.StepOf(0, 1)
	if a2.Read != 0 {
		t.Errorf("5m TTL only: %+v", a2.Bill)
	}
}

func TestRepriceTargetsOrderAsExpected(t *testing.T) {
	// A long single-agent chain: caching matters. No cache must cost the most.
	var steps []rl.Step
	p := 8000
	for i := 0; i < 30; i++ {
		steps = append(steps, mkStep(fmt.Sprintf("A.%d", i), withAt(time.Duration(i)*20*time.Second), withPrompt(p, ""), withOut(200)))
		p += 1500
	}
	ep := mkEpisode("t/0", mkAgent("A", "worker", steps...))
	ite := map[string]float64{}
	for _, name := range []string{"anthropic-sonnet", "openai", "marketplace", "no-cache"} {
		m, _ := LookupTarget(name)
		rep, err := Reprice(ep, m)
		if err != nil {
			t.Fatal(err)
		}
		ite[name] = rep.ITE
	}
	if !(ite["no-cache"] > ite["openai"] && ite["no-cache"] > ite["anthropic-sonnet"] && ite["no-cache"] > ite["marketplace"]) {
		t.Errorf("no cache should cost the most: %v", ite)
	}
	// With a growing chain and 5-minute-alive entries, every provider reads almost
	// everything: ITE is roughly read-weight times the prompt bytes plus outputs.
	m, _ := LookupTarget("anthropic-sonnet")
	rep, _ := Reprice(ep, m)
	// Only outputs were recorded (30 steps x 200 tokens x weight 5).
	if rep.Read == 0 {
		t.Errorf("a growing chain must read: %+v", rep.Bill)
	}
	near(t, "recorded ITE", rep.Recorded.ITE, 30*200*5)
	total := 0
	for _, s := range steps {
		total += s.Prompt.Tokens
	}
	if got := rep.Input(); got != int64(total) {
		t.Errorf("the input tokens must partition the recorded prompts: %d vs %d", got, total)
	}
}

func TestRepriceRecordedUsageIsPricedAtTargetWeights(t *testing.T) {
	a := mkAgent("A", "worker",
		mkStep("A.1", withAt(0), withPrompt(2960, ""), withUsage(2960, 0, 0, 9)),
		mkStep("A.2", withAt(time.Minute), withPrompt(3914, ""), withUsage(954, 2960, 0, 9)),
	)
	rep, _ := Reprice(mkEpisode("t/0", a), anthropicLike())
	near(t, "recorded", rep.Recorded.ITE, 2960+954+0.1*2960+2*9*5)
	if rep.Recorded.Requests != 2 {
		t.Errorf("recorded requests = %d", rep.Recorded.Requests)
	}
}

func TestRepriceEmptyAndDegenerateEpisodes(t *testing.T) {
	if _, err := Reprice(nil, anthropicLike()); err == nil {
		t.Error("nil episode must be an error")
	}
	rep, err := Reprice(&rl.Episode{}, anthropicLike())
	if err != nil || rep.ITE != 0 || rep.Requests != 0 {
		t.Errorf("empty episode: %+v %v", rep, err)
	}
	// An agent with no steps, a step with no sizes at all.
	ep := mkEpisode("t/0", mkAgent("A", "worker"), mkAgent("B", "worker", mkStep("B.1")))
	rep, err = Reprice(ep, anthropicLike())
	if err != nil || rep.Requests != 1 || rep.ITE != 0 {
		t.Errorf("no sizes: %+v %v", rep.Bill, err)
	}
	if len(rep.Warnings) == 0 {
		t.Error("unknown prompt sizes must produce a warning")
	}
	// Invalid targets are refused, not silently priced at NaN.
	bad := anthropicLike()
	bad.Price.CacheReadPerM = -1
	if _, err := Reprice(ep, bad); err == nil {
		t.Error("negative price accepted")
	}
	nan := anthropicLike()
	nan.Price.OutputPerM = math.NaN()
	if _, err := Reprice(ep, nan); err == nil {
		t.Error("NaN price accepted")
	}
	// A zero model is a provider without a cache: every prompt token is uncached.
	ep = mkEpisode("t/0", mkAgent("A", "worker", mkStep("A.1", withPrompt(1000, ""), withOut(10)), mkStep("A.2", withAt(time.Minute), withPrompt(1200, ""), withOut(10))))
	rep, _ = Reprice(ep, cost.Model{})
	if rep.Uncached != 2200 || rep.Read != 0 {
		t.Errorf("zero model: %+v", rep.Bill)
	}
}

func TestRepriceEstimatesOutputWhenUsageIsMissing(t *testing.T) {
	st := mkStep("A.1", withPrompt(1000, ""), withText("hello world, this is a completion of a reasonable length"))
	rep, _ := Reprice(mkEpisode("t/0", mkAgent("A", "worker", st)), anthropicLike())
	if rep.Output == 0 {
		t.Error("an unrecorded output must be estimated from the completion")
	}
	// Recorded usage that says zero output is respected.
	st = mkStep("A.1", withPrompt(1000, ""), withUsage(1000, 0, 0, 0), withText("hello world"))
	rep, _ = Reprice(mkEpisode("t/0", mkAgent("A", "worker", st)), anthropicLike())
	if rep.Output != 0 {
		t.Errorf("recorded zero output overridden: %d", rep.Output)
	}
}

func TestRepriceTimelineForUnstampedAgents(t *testing.T) {
	// No timestamps at all: agents are laid out one after the other, so a worker
	// finds what the manager wrote warm (the warm gate's effect).
	mgr := mkAgent("M", "manager", mkStep("M.1", withPrompt(5000, "g"), withOut(10)))
	w := mkAgent("W", "worker", mkStep("W.1", withPrompt(4600, "g"), withOut(10)))
	rep, err := RepriceWith(mkEpisode("t/0", mgr, w), anthropicLike(), RepriceOptions{SharedTokens: map[string]int{"g": 4000}})
	if err != nil {
		t.Fatal(err)
	}
	w1, _ := rep.StepOf(1, 0)
	if w1.Read != 4000 {
		t.Errorf("worker after the manager should read the shared prefix: %+v", w1.Bill)
	}
	if w1.At <= 0 {
		t.Errorf("synthesised time should follow the manager: %v", w1.At)
	}
	// Steps of one agent are spaced by more than the time to first byte, so each
	// step can read its predecessor's write.
	a := mkAgent("A", "worker", mkStep("A.1", withPrompt(5000, ""), withOut(1)), mkStep("A.2", withPrompt(5500, ""), withOut(1)))
	rep, _ = Reprice(mkEpisode("t/0", a), anthropicLike())
	if s, _ := rep.StepOf(0, 1); s.Read != 5000 {
		t.Errorf("unstamped consecutive steps should chain: %+v", s.Bill)
	}
	// Partially stamped agents: the missing times are filled around the known ones.
	a = mkAgent("A", "worker",
		mkStep("A.1", withPrompt(5000, ""), withOut(1)),
		mkStep("A.2", withAt(3*time.Minute), withPrompt(5500, ""), withOut(1)),
		mkStep("A.3", withPrompt(6000, ""), withOut(1)),
	)
	rep, _ = Reprice(mkEpisode("t/0", a), anthropicLike())
	s1, _ := rep.StepOf(0, 0)
	s2, _ := rep.StepOf(0, 1)
	s3, _ := rep.StepOf(0, 2)
	// The one stamped step anchors the timeline (offset 0); the unstamped ones are
	// filled around it and never go backwards.
	if !(s1.At <= s2.At && s2.At < s3.At) {
		t.Errorf("times: %v %v %v", s1.At, s2.At, s3.At)
	}
}

func TestRepriceReplaysAtRecordedLatency(t *testing.T) {
	// A fast local endpoint: the first byte arrives in 3ms, so the next request 5ms
	// later can read the entry (the recorded fixture behaves like this).
	a := mkAgent("A", "worker",
		mkStep("A.1", withAt(0), withLatency(3), withPrompt(3000, ""), withOut(9)),
		mkStep("A.2", withAt(5*time.Millisecond), withLatency(3), withPrompt(3500, ""), withOut(9)),
	)
	rep, _ := Reprice(mkEpisode("t/0", a), anthropicLike())
	if s, _ := rep.StepOf(0, 1); s.Read != 3000 {
		t.Errorf("recorded latency bounds the first-byte delay: %+v", s.Bill)
	}
	// The same timing with no recorded latency uses the model's first-byte delay.
	a.Steps[0].LatencyMs, a.Steps[1].LatencyMs = 0, 0
	rep, _ = Reprice(mkEpisode("t/0", a), anthropicLike())
	if s, _ := rep.StepOf(0, 1); s.Read != 0 {
		t.Errorf("without latency the model's ttfb applies: %+v", s.Bill)
	}
}

func TestRepriceIsOrderIndependent(t *testing.T) {
	build := func(order []int) *rl.Episode {
		agents := []rl.Agent{
			mkAgent("A", "worker", mkStep("A.1", withAt(0), withPrompt(5000, "g"), withOut(10)), mkStep("A.2", withAt(30*time.Second), withPrompt(5600, "g"), withOut(10))),
			mkAgent("B", "worker", mkStep("B.1", withAt(10*time.Second), withPrompt(4700, "g"), withOut(10))),
			mkAgent("C", "worker", mkStep("C.1", withAt(50*time.Second), withPrompt(4800, "g"), withOut(10))),
		}
		var out []rl.Agent
		for _, i := range order {
			out = append(out, agents[i])
		}
		return mkEpisode("t/0", out...)
	}
	opts := RepriceOptions{SharedTokens: map[string]int{"g": 4000}}
	var first float64
	for i, order := range [][]int{{0, 1, 2}, {2, 1, 0}, {1, 2, 0}, {0, 2, 1}} {
		rep, err := RepriceWith(build(order), anthropicLike(), opts)
		if err != nil {
			t.Fatal(err)
		}
		if i == 0 {
			first = rep.ITE
			continue
		}
		near(t, fmt.Sprintf("order %v", order), rep.ITE, first)
	}
}

func TestRepriceStepsSumToTotalOnRandomEpisodes(t *testing.T) {
	rng := rand.New(rand.NewSource(3))
	for trial := 0; trial < 40; trial++ {
		var agents []rl.Agent
		for a := 0; a < 1+rng.Intn(4); a++ {
			var steps []rl.Step
			p := 3000 + rng.Intn(3000)
			at := time.Duration(rng.Intn(120)) * time.Second
			for s := 0; s < 1+rng.Intn(25); s++ {
				opts := []stepOpt{withAt(at), withPrompt(p, []string{"", "g"}[rng.Intn(2)]), withOut(rng.Intn(400))}
				switch rng.Intn(9) {
				case 0:
					opts = append(opts, withKind(rl.KindCompactor))
				case 1:
					opts = append(opts, withSeg(s, 0))
					p = 2000 + rng.Intn(3000)
				}
				steps = append(steps, mkStep(fmt.Sprintf("a%d.%d", a, s), opts...))
				p += rng.Intn(1500)
				at += time.Duration(5+rng.Intn(600)) * time.Second
			}
			agents = append(agents, mkAgent(fmt.Sprintf("a%d", a), "worker", steps...))
		}
		ep := mkEpisode("t/0", agents...)
		for _, name := range []string{"anthropic-sonnet", "openai", "marketplace", "no-cache"} {
			m, _ := LookupTarget(name)
			rep, err := Reprice(ep, m)
			if err != nil {
				t.Fatal(err)
			}
			var sum float64
			var in int64
			for _, s := range rep.Steps {
				sum += s.ITE
				in += s.Input()
			}
			near(t, "sum of steps", sum, rep.ITE)
			var agentSum float64
			for _, b := range rep.PerAgent {
				agentSum += b.ITE
			}
			near(t, "sum of agents", agentSum, rep.ITE)
			want := int64(0)
			for _, s := range rep.Steps {
				want += int64(s.Prompt)
			}
			if in != want {
				t.Fatalf("trial %d %s: input tokens %d do not partition the prompts %d", trial, name, in, want)
			}
			if rep.ITE < 0 || !finite(rep.ITE) {
				t.Fatalf("bad ITE %v", rep.ITE)
			}
			// A rerun is bit-identical.
			again, _ := Reprice(ep, m)
			if again.ITE != rep.ITE {
				t.Fatal("not deterministic")
			}
		}
	}
}

func TestRepriceLongEpisodeIsFast(t *testing.T) {
	var steps []rl.Step
	p := 5000
	for i := 0; i < 20000; i++ {
		steps = append(steps, mkStep(fmt.Sprintf("A.%d", i), withAt(time.Duration(i)*10*time.Second), withPrompt(p, ""), withOut(5)))
		p += 30
	}
	ep := mkEpisode("t/0", mkAgent("A", "worker", steps...))
	start := time.Now()
	rep, err := Reprice(ep, anthropicLike())
	if err != nil {
		t.Fatal(err)
	}
	if d := time.Since(start); d > time.Minute {
		t.Errorf("20k-step chain took %v", d)
	}
	if rep.Requests != 20000 || rep.Read == 0 {
		t.Errorf("%+v", rep.Bill)
	}
}

func TestRepriceMarketplaceEnginesRouteBySharedPrefix(t *testing.T) {
	m, _ := LookupTarget("marketplace")
	// Several workers share a prefix; with 4 engines and routing keys they all land
	// on the engine holding it, so reads match the single-engine case.
	build := func() *rl.Episode {
		var agents []rl.Agent
		for i := 0; i < 6; i++ {
			agents = append(agents, mkAgent(fmt.Sprintf("w%d", i), "worker",
				mkStep(fmt.Sprintf("w%d.1", i), withAt(time.Duration(10+i*20)*time.Second), withPrompt(5000+i*10, "g"), withOut(10))))
		}
		return mkEpisode("t/0", agents...)
	}
	one, _ := RepriceWith(build(), m, RepriceOptions{Engines: 1, SharedTokens: map[string]int{"g": 4000}})
	four, _ := RepriceWith(build(), m, RepriceOptions{Engines: 4, SharedTokens: map[string]int{"g": 4000}})
	near(t, "keyed routing keeps the prefix on one engine", four.ITE, one.ITE)
	// Without a routing key the same workload pays extra cold prefills.
	nokey := m
	nokey.Cache.KeyRouting = false
	scattered, _ := RepriceWith(build(), nokey, RepriceOptions{Engines: 4, SharedTokens: map[string]int{"g": 4000}})
	if scattered.ITE <= one.ITE {
		t.Errorf("unkeyed requests over 4 engines must cost more: %v vs %v", scattered.ITE, one.ITE)
	}
}
