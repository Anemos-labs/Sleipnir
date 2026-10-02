package probe_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/anemos-labs/sleipnir/internal/provider/mock"
	"github.com/anemos-labs/sleipnir/internal/provider/openaichat"
	"github.com/anemos-labs/sleipnir/internal/provider/probe"
)

func TestProbeMeasuresMockEngine(t *testing.T) {
	srv := mock.New(mock.Config{
		FirstToken: 120 * time.Millisecond, // the window in which a parallel burst finds nothing: wide, so a loaded machine does not close it
		Engine:     mock.EngineConfig{BlockTokens: 16, MinCacheTokens: 64},
	}, func(c *mock.Call) mock.Reply {
		last := c.Messages[len(c.Messages)-1]
		switch {
		case len(c.Tools) > 0 && last.Role == "user" && strings.Contains(last.Content, "weather"):
			return mock.Reply{ToolCalls: []mock.ToolCall{{ID: "call_1", Name: "get_weather", Args: `{"city":"Paris"}`}}}
		case last.Role == "tool":
			return mock.Reply{Text: "It is 18C with light rain in Paris."}
		case strings.Contains(c.LastUser(), "17 * 23"):
			return mock.Reply{Text: "391", Reasoning: "17*23 = 17*20 + 17*3 = 340 + 51"}
		}
		return mock.Reply{Text: "ok"}
	})
	ts := srv.Start()
	defer ts.Close()

	var lastHdr = make(chan struct{}, 1)
	_ = lastHdr
	c := openaichat.New(openaichat.Config{Name: "mock", BaseURL: ts.URL, Options: openaichat.Options{SessionHeader: true}})
	var logs []string
	rep, err := probe.Run(context.Background(), probe.Config{
		Provider: c, Model: "mock-1", Deep: true, CacheKey: true,
		Log: func(s string) { logs = append(logs, s) },
	})
	if err != nil {
		t.Fatal(err)
	}
	f := rep.Findings
	if !f.Streaming || !f.UsageReported || !f.CostReported {
		t.Fatalf("basics: %+v", f)
	}
	if !f.Tools || !f.ToolRoundTrip {
		t.Fatalf("tools: %+v", f)
	}
	if !f.CacheWorks || !f.CachedTokensReported || f.CacheHitRatio < 0.9 {
		t.Fatalf("cache: %+v", f)
	}
	if f.CacheGranularity != 16 {
		t.Fatalf("granularity = %d, want 16 (the mock's block size)", f.CacheGranularity)
	}
	if f.MinCachePrefix < 64 || f.MinCachePrefix > 200 {
		t.Fatalf("min prefix = %d", f.MinCachePrefix)
	}
	if f.WarmupNeeded == nil || !*f.WarmupNeeded {
		t.Fatalf("the mock publishes at first token, so parallel cold bursts miss: %+v", f.WarmupNeeded)
	}
	if !f.ReasoningSeen {
		t.Fatalf("reasoning not detected: %+v", f)
	}
	txt := rep.Text()
	for _, want := range []string{"prefix cache works   yes", "warm-up before burst yes", "granularity"} {
		if !strings.Contains(txt, want) {
			t.Fatalf("report missing %q:\n%s", want, txt)
		}
	}
	prof := rep.Apply(c.Profile())
	if prof.Cache.Granularity != 16 || prof.Cache.MinPrefixTokens != f.MinCachePrefix {
		t.Fatalf("profile not refined: %+v", prof.Cache)
	}
}

// A cache that hits on some requests and not on others is not "working" or "broken": the
// verdict counts every repeat request of the cache step. Without affinity the mock spreads
// requests over three engines, so the first request each engine sees misses and the rest hit
// (one sample, the identical repeat, would have said the cache does not work).
func TestProbeCountsEveryRepeatRequest(t *testing.T) {
	srv := mock.New(mock.Config{Engines: 3, Engine: mock.EngineConfig{BlockTokens: 16, MinCacheTokens: 64}},
		func(c *mock.Call) mock.Reply { return mock.Reply{Text: "ok"} })
	ts := srv.Start()
	defer ts.Close()

	c := openaichat.New(openaichat.Config{Name: "mock", BaseURL: ts.URL, Options: openaichat.Options{SessionHeader: true}})
	rep, err := probe.Run(context.Background(), probe.Config{Provider: c, Model: "mock-1", CacheKey: false})
	if err != nil {
		t.Fatal(err)
	}
	f := rep.Findings
	if f.CacheRepeats != 9 || f.CacheRepeatHits != 7 {
		t.Fatalf("%d of %d repeat requests hit, want 7 of 9 (the second and third request each engine had not seen miss): %+v", f.CacheRepeatHits, f.CacheRepeats, f)
	}
	if !f.CacheWorks || !f.CachedTokensReported {
		t.Fatalf("a cache that hits on most requests works: %+v", f)
	}
	if f.CacheHitRatio < 0.5 || f.CacheHitRatio > 0.9 {
		t.Fatalf("hit ratio %.2f: the two misses cost a share of the prompt tokens", f.CacheHitRatio)
	}
	txt := rep.Text()
	if !strings.Contains(txt, "prefix cache works   partly (7 of 9 repeat requests hit") {
		t.Fatalf("the report must say the hits were partial:\n%s", txt)
	}
	noted := false
	for _, n := range f.Notes {
		noted = noted || strings.Contains(n, "7 of 9 repeat requests hit the cache: hits are erratic")
	}
	if !noted {
		t.Fatalf("no note about erratic hits: %v", f.Notes)
	}

	// With the conversation key sent, one engine serves every request and all of them hit.
	pinned, err := probe.Run(context.Background(), probe.Config{Provider: c, Model: "mock-1", CacheKey: true})
	if err != nil {
		t.Fatal(err)
	}
	if g := pinned.Findings; g.CacheRepeats != 9 || g.CacheRepeatHits != 9 || !strings.Contains(pinned.Text(), "prefix cache works   yes (9 of 9") {
		t.Fatalf("with affinity every repeat request hits: %+v\n%s", g, pinned.Text())
	}
}

// An endpoint that never reports cached tokens is told so plainly.
func TestProbeSaysWhenNothingIsCached(t *testing.T) {
	srv := mock.New(mock.Config{Engine: mock.EngineConfig{BlockTokens: 16, MinCacheTokens: 1 << 20}},
		func(c *mock.Call) mock.Reply { return mock.Reply{Text: "ok"} })
	ts := srv.Start()
	defer ts.Close()

	c := openaichat.New(openaichat.Config{Name: "mock", BaseURL: ts.URL})
	rep, err := probe.Run(context.Background(), probe.Config{Provider: c, Model: "mock-1", CacheKey: true})
	if err != nil {
		t.Fatal(err)
	}
	f := rep.Findings
	if f.CacheWorks || f.CachedTokensReported || f.CacheRepeatHits != 0 || f.CacheRepeats != 9 {
		t.Fatalf("nothing was cached: %+v", f)
	}
	if !strings.Contains(rep.Text(), "prefix cache works   NO (0 of 9 repeat requests hit") {
		t.Fatalf("report:\n%s", rep.Text())
	}
	noted := false
	for _, n := range f.Notes {
		noted = noted || strings.Contains(n, "no repeat request hit the cache")
	}
	if !noted {
		t.Fatalf("no note: %v", f.Notes)
	}
}

func TestProbeChecksTokenCapture(t *testing.T) {
	srv := mock.New(mock.Config{Engine: mock.EngineConfig{BlockTokens: 16, MinCacheTokens: 64}},
		func(c *mock.Call) mock.Reply { return mock.Reply{Text: "one two three"} })
	ts := srv.Start()
	defer ts.Close()

	prof := openaichat.DefaultProfile("mock", ts.URL)
	prof.CaptureTokens = true
	c := openaichat.New(openaichat.Config{Name: "mock", BaseURL: ts.URL, Profile: &prof})
	rep, err := probe.Run(context.Background(), probe.Config{Provider: c, Model: "mock-1", Capture: true})
	if err != nil {
		t.Fatal(err)
	}
	f := rep.Findings
	if !f.TokenIDs || !f.TokenLogprobs || !f.TokenPrefixStable {
		t.Fatalf("a capturing endpoint must be recognised: %+v", f)
	}
	if !strings.Contains(rep.Text(), "token ids") {
		t.Fatalf("report should mention token ids:\n%s", rep.Text())
	}

	// An endpoint whose profile cannot capture yields a note, not a failure.
	plain := openaichat.New(openaichat.Config{Name: "mock", BaseURL: ts.URL})
	rep, err = probe.Run(context.Background(), probe.Config{Provider: plain, Model: "mock-1", Capture: true})
	if err != nil || rep.Findings.TokenIDs {
		t.Fatalf("no capture expected: %v %+v", err, rep.Findings)
	}
	noted := false
	for _, n := range rep.Findings.Notes {
		noted = noted || strings.Contains(n, "no token ids")
	}
	if !noted {
		t.Fatalf("expected an explanatory note, got %v", rep.Findings.Notes)
	}
}

func TestReportFailureIsAnEndpointThatAnsweredNothing(t *testing.T) {
	ok := func(name string) probe.Step { return probe.Step{Name: name, OK: true} }
	bad := func(name, why string) probe.Step { return probe.Step{Name: name, Detail: why} }
	for _, tc := range []struct {
		name  string
		steps []probe.Step
		want  string // "" means no failure
	}{
		{"everything answered", []probe.Step{ok("basic"), ok("tools"), ok("cache-cold")}, ""},
		{"a capability that is missing is a finding, not a failure", []probe.Step{ok("basic"), bad("tools", "400"), bad("reasoning", "400")}, ""},
		{"the plain request failed", []probe.Step{bad("basic", "connection refused"), bad("tools", "connection refused")}, "the endpoint did not answer a basic request: connection refused"},
		{"nothing was asked", nil, "the probe made no request"},
	} {
		err := (&probe.Report{Steps: tc.steps}).Failure()
		switch {
		case tc.want == "" && err != nil:
			t.Errorf("%s: Failure() = %v, want nil", tc.name, err)
		case tc.want != "" && (err == nil || err.Error() != tc.want):
			t.Errorf("%s: Failure() = %v, want %q", tc.name, err, tc.want)
		}
	}
}

// A model that answers the tools request without calling the tool is a request that worked and a capability that is missing: the live log says
// both, so that a line saying ✓ is not followed by a summary saying NO with nothing in between.
func TestProbeSaysWhenTheModelDidNotCallTheTool(t *testing.T) {
	srv := mock.New(mock.Config{}, func(c *mock.Call) mock.Reply { return mock.Reply{Text: "ok"} })
	ts := srv.Start()
	defer ts.Close()

	var logs []string
	c := openaichat.New(openaichat.Config{Name: "mock", BaseURL: ts.URL})
	rep, err := probe.Run(context.Background(), probe.Config{Provider: c, Model: "mock-1", CacheKey: true, Log: func(s string) { logs = append(logs, s) }})
	if err != nil {
		t.Fatal(err)
	}
	if rep.Findings.Tools || !strings.Contains(strings.Join(logs, "\n"), "did not call the offered tool") {
		t.Fatalf("tools=%v, log:\n%s", rep.Findings.Tools, strings.Join(logs, "\n"))
	}
}

// An endpoint that does not answer the plain request is said so once: the other probes would repeat the same error four times.
func TestProbeStopsWhenTheEndpointDoesNotAnswerAtAll(t *testing.T) {
	srv := mock.New(mock.Config{}, func(c *mock.Call) mock.Reply { return mock.Reply{Text: "ok"} })
	ts := srv.Start()
	ts.Close() // nothing listens any more
	c := openaichat.New(openaichat.Config{Name: "mock", BaseURL: ts.URL})
	rep, err := probe.Run(context.Background(), probe.Config{Provider: c, Model: "mock-1", CacheKey: true})
	if err != nil {
		t.Fatal(err)
	}
	if len(rep.Steps) != 1 || rep.Failure() == nil {
		t.Fatalf("%d steps ran against a dead endpoint: %+v", len(rep.Steps), rep.Steps)
	}
}
