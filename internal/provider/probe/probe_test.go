package probe_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/reee344/sleipnir/internal/provider/mock"
	"github.com/reee344/sleipnir/internal/provider/openaichat"
	"github.com/reee344/sleipnir/internal/provider/probe"
)

func TestProbeMeasuresMockEngine(t *testing.T) {
	srv := mock.New(mock.Config{
		FirstToken: 40 * time.Millisecond,
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
