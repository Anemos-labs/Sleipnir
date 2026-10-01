package agent_test

// The real agent loop against the native Anthropic adapter and the Anthropic
// dialect mock, which enforces preserved-thinking bindings and models explicit
// breakpoints. Nothing between the agent and the "endpoint" is faked: the agent
// renders with kv, the adapter builds wire JSON (cache_control, clear_at system
// messages, the betas), the mock signs thinking blocks against the prefix the
// wire carried and rejects a replayed block whose prefix changed.
//
// cxProv (cache_regress_test.go) reproduces the bindings in-process and is the
// place for adversarial cases; this file is the check that the same behaviour
// survives a real adapter: R1 (the hot tail no longer breaks preserved
// thinking), R2 (rebases strip it atomically) and the cache reads that follow.

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/anemos-labs/sleipnir/internal/agent"
	"github.com/anemos-labs/sleipnir/internal/core"
	"github.com/anemos-labs/sleipnir/internal/events"
	"github.com/anemos-labs/sleipnir/internal/kv"
	"github.com/anemos-labs/sleipnir/internal/provider"
	"github.com/anemos-labs/sleipnir/internal/provider/anthropic"
	"github.com/anemos-labs/sleipnir/internal/provider/mock"
	"github.com/anemos-labs/sleipnir/internal/tools"
)

const nativeModel = "claude-opus-5-5" // preserved thinking, 512-token minimum prefix

// nativeRig serves a model that reasons and calls the work tool `steps` times,
// then answers; compactor forks get a fixed valid patch. turnScoped says whether
// the endpoint (its profile) has clear_at system messages.
func nativeRig(t *testing.T, turnScoped bool, steps int) (*anthropic.Client, *mock.AnthropicServer, *[]anthropic.Warning) {
	t.Helper()
	var main atomic.Int32
	responder := func(c *mock.Call) mock.Reply {
		if task := c.LastUser(); strings.Contains(task, "<compactor-task>") {
			return mock.Reply{Text: nativePatch(task)}
		}
		n := int(main.Add(1)) - 1
		if n < steps {
			return mock.Reply{
				Reasoning: fmt.Sprintf("step %d: read the build output and pick the next check", n),
				Text:      fmt.Sprintf("step %d", n),
				ToolCalls: []mock.ToolCall{{ID: fmt.Sprintf("toolu_%02d", n), Name: "work", Args: fmt.Sprintf(`{"n":%d}`, n)}},
			}
		}
		return mock.Reply{Reasoning: "every check passes", Text: "all done"}
	}
	srv := mock.NewAnthropic(mock.AnthropicConfig{EnforceThinkingBinding: true}, responder)
	ts := srv.Start()
	t.Cleanup(ts.Close)
	prof := anthropic.DefaultProfile("native", ts.URL, nativeModel)
	prof.TurnScopedSystem = turnScoped
	var warned []anthropic.Warning
	c := anthropic.New(anthropic.Config{
		BaseURL: ts.URL, APIKey: "test-key", Model: nativeModel, Profile: &prof,
		OnWarnings: func(_ *provider.Request, ws []anthropic.Warning) { warned = append(warned, ws...) },
	})
	return c, srv, &warned
}

var nativeUnitLine = regexp.MustCompile(`(?m)^\s+t(\d+)(?:-t(\d+))? ·`)

// nativePatch answers a compactor task the way a model would: fold every unit
// the instruction lists as foldable into one spine line.
func nativePatch(task string) string {
	first, last := 0, 0
	for _, m := range nativeUnitLine.FindAllStringSubmatch(task, -1) {
		from, _ := strconv.Atoi(m[1])
		to := from
		if m[2] != "" {
			to, _ = strconv.Atoi(m[2])
		}
		if first == 0 {
			first = from
		}
		last = to
	}
	if first == 0 {
		return `{"keep_from":"t1","spine":[],"mask":[],"notes":[],"promote":[]}`
	}
	return fmt.Sprintf(`{"keep_from":"t%d","spine":[{"turns":"t%d-t%d","line":"explored the build"}],"mask":[],"notes":[],"promote":[]}`, last+1, first, last)
}

func nativeCached(s mock.AnthropicStat) int { return s.Read + s.Write5m + s.Write1h }

func nativeHasBeta(s mock.AnthropicStat, beta string) bool {
	for _, b := range s.Betas {
		if b == beta {
			return true
		}
	}
	return false
}

// nativeAllOK fails on any request the endpoint did not accept.
func nativeAllOK(t *testing.T, st []mock.AnthropicStat) {
	t.Helper()
	for i, s := range st {
		if s.Status != 200 || s.Err != "" {
			t.Fatalf("request %d failed: status %d %s", i+1, s.Status, s.Err)
		}
	}
}

// With a board that changes on every request, an agent on a preserved-thinking
// route never has a thinking block rejected, replays every one of them, and
// re-reads what it sent before, whichever way the route delivers the board.
func TestCacheEcon_NativeAdapterAgentKeepsThinkingAndTheCache(t *testing.T) {
	for _, tc := range []struct {
		name       string
		turnScoped bool
		hotMode    string
	}{
		{"turn-scoped board (clear_at system messages)", true, "turn-scoped"},
		{"persisted board (endpoint without clear_at)", false, "persist"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			const steps = 8
			c, srv, warned := nativeRig(t, tc.turnScoped, steps)
			a, log := cxAgent(t, cxOpts{prov: c, hot: cxBoard(), noCompct: true, model: nativeModel})
			res, err := a.Run(context.Background(), "find out why the build fails and fix it")
			if err != nil || res.Steps != steps+1 {
				t.Fatalf("run: steps=%v err=%v", res, err)
			}
			if n := cxCount(log, events.TypeModelResponse, `"hot_mode":"`+tc.hotMode+`"`); n != steps+1 {
				t.Fatalf("every request must record hot_mode %q, got %d of %d", tc.hotMode, n, steps+1)
			}
			if n := cxCount(log, events.TypeCacheAnomaly, ""); n != 0 {
				t.Fatalf("no anomaly expected on an append-only run, got %d", n)
			}
			if len(*warned) != 0 {
				t.Fatalf("the adapter had to adjust what kv rendered: %+v", *warned)
			}

			st := srv.AnthropicStats()
			if len(st) != steps+1 {
				t.Fatalf("the endpoint served %d requests, want %d", len(st), steps+1)
			}
			nativeAllOK(t, st)
			for i, s := range st {
				if got := nativeHasBeta(s, anthropic.BetaTurnScopedSystem); got != tc.turnScoped {
					t.Errorf("request %d: turn-scoped beta present=%v, want %v (%v)", i+1, got, tc.turnScoped, s.Betas)
				}
			}
			// The prefix is append-only from one request to the next, so what the
			// previous request cached is read in full (the only slack is the block
			// the rolling marker could not reach, a few hundred tokens).
			for i := 1; i < len(st); i++ {
				prev, cur := st[i-1], st[i]
				if cur.Read < nativeCached(prev)*9/10 {
					t.Errorf("request %d read %d of the %d tokens request %d cached", i+1, cur.Read, nativeCached(prev), i)
				}
			}
			// The last request must have replayed every thinking block it was given.
			reqs := a.Thread().Snapshot()
			think := 0
			for _, tr := range reqs.Turns {
				for _, b := range tr.Blocks {
					if b.Kind == core.BlockThinking {
						think++
					}
				}
			}
			if think != steps+1 {
				t.Errorf("the thread holds %d thinking blocks, want %d (none stripped by a healthy run)", think, steps+1)
			}
		})
	}
}

// A compaction commit in the middle of a run is a declared rebase: the retained
// tail is stripped of the thinking that was bound to the folded prefix, in the
// same step, so the next request is accepted and the constitution and shared
// layers are still read from the cache.
func TestCacheEcon_NativeAdapterAgentSurvivesACompactionCommit(t *testing.T) {
	for _, turnScoped := range []bool{true, false} {
		t.Run(fmt.Sprintf("turn-scoped=%v", turnScoped), func(t *testing.T) {
			const steps = 14
			c, srv, _ := nativeRig(t, turnScoped, steps)
			pl := kv.DefaultPlanner()
			pl.SoftThreadTokens, pl.HardThreadTokens, pl.MinThreadTokens = 500, 900, 300
			a, log := cxAgent(t, cxOpts{prov: c, hot: cxBoard(), planner: pl, model: nativeModel})
			res, err := a.Run(context.Background(), "find out why the build fails and fix it")
			if err != nil || res.Steps != steps+1 {
				t.Fatalf("run: steps=%v err=%v", res, err)
			}
			if len(log.OfType(events.TypeCompactCommit)) == 0 {
				t.Fatalf("setup: nothing was committed (plans %d, rejects %d)", len(log.OfType(events.TypeCompactPlan)), len(log.OfType(events.TypeCompactReject)))
			}
			// The compactor fork itself must be a request the endpoint accepts: on a
			// turn-scoped route the parent's board view (a system message) sits at the
			// tail of the thread, and the instruction is a user turn.
			for _, e := range log.OfType(events.TypeCompactReject) {
				if strings.Contains(string(e.Data), `"stage":"model_patch"`) {
					t.Fatalf("the model patch failed and fell back to the mechanical one: %s", e.Data)
				}
			}
			st := srv.AnthropicStats()
			nativeAllOK(t, st)
			// After the last commit the run still reads the layers in front of the
			// thread: some request served from cache far more than the tools alone.
			best := 0
			for _, s := range st {
				if s.Read > best {
					best = s.Read
				}
			}
			if best < 1000 {
				t.Errorf("no request read more than %d tokens from the cache", best)
			}
		})
	}
}

// A route that binds signatures without being flagged in the model table (a
// gateway) is learned from its first rejection: through the real adapter the
// rejection arrives as provider.ErrThinkingBinding, the agent strips, retries and
// persists the board from then on.
func TestCacheEcon_NativeAdapterUnflaggedRouteIsLearnedFromOneRejection(t *testing.T) {
	const steps = 5
	c, srv, _ := nativeRig(t, false, steps)
	a, log := cxAgent(t, cxOpts{prov: c, hot: cxBoard(), noCompct: true, model: "claude-opus-5"}) // not flagged PreservedThinking
	res, err := a.Run(context.Background(), "find out why the build fails and fix it")
	if err != nil || res.Steps != steps+1 {
		t.Fatalf("run: steps=%v err=%v", res, err)
	}
	if n := cxCount(log, events.TypeCacheAnomaly, `"kind":"thinking_binding"`); n != 1 {
		t.Fatalf("want exactly one binding rejection, got %d", n)
	}
	rejected := 0
	for _, s := range srv.AnthropicStats() {
		if s.Status != 200 {
			rejected++
		}
	}
	if rejected != 1 {
		t.Fatalf("the endpoint rejected %d requests, want 1", rejected)
	}
}

// A shared-layer epoch in the middle of a run rewrites everything below the shared
// pin, so every thinking block produced before it is bound to a prefix that no
// longer exists. SyncShared strips them in the same step; the next request is
// accepted by an endpoint that enforces the bindings.
func TestCacheEcon_NativeAdapterAgentSurvivesASharedEpoch(t *testing.T) {
	const steps = 6
	c, srv, _ := nativeRig(t, true, steps)
	newShared := kv.NewLayer("shared", kv.KindShared, 2, []kv.Segment{{Key: "project", Text: strings.Repeat("The repo moved to a monorepo. ", 100), Vol: kv.VolEpoch}})
	var a *agent.Agent
	calls := 0
	a, log := cxAgent(t, cxOpts{prov: c, hot: cxBoard(), noCompct: true, model: nativeModel, toolOut: func(json.RawMessage) *tools.Result {
		calls++
		if calls == 3 {
			a.SyncShared(newShared, nil, "the project moved") // between two requests, like swarm.SetShared
		}
		return &tools.Result{Text: strings.Repeat("build output line with some detail\n", 30)}
	}})
	res, err := a.Run(context.Background(), "find out why the build fails and fix it")
	if err != nil || res.Steps != steps+1 {
		t.Fatalf("run: steps=%v err=%v", res, err)
	}
	nativeAllOK(t, srv.AnthropicStats())
	if n := cxCount(log, events.TypeLayerCommit, `"thinking_stripped":true`); n != 1 {
		t.Fatalf("the epoch must strip thinking in the same step, got %d such commits", n)
	}
	if n := cxCount(log, events.TypeCacheAnomaly, ""); n != 0 {
		t.Fatalf("a declared epoch is not an anomaly, got %d", n)
	}
}
