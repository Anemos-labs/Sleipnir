package demo

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/anemos-labs/sleipnir/internal/config"
	"github.com/anemos-labs/sleipnir/internal/cost"
	"github.com/anemos-labs/sleipnir/internal/events"
	"github.com/anemos-labs/sleipnir/internal/perm"
	"github.com/anemos-labs/sleipnir/internal/provider/mock"
	"github.com/anemos-labs/sleipnir/internal/provider/openaichat"
	"github.com/anemos-labs/sleipnir/internal/session"
	"github.com/anemos-labs/sleipnir/internal/swarm"
)

// The shop scenario's cache lifetime. The real lifetimes are minutes; a demo that lasts one minute needs a cache that cools in
// seconds. The planner treats a request as being at "a cold moment" when it is within 20 seconds of the end of the entry's life, so
// the lifetime is those 20 seconds plus 5 seconds of silence at the demo's own pace: a worker that says nothing for longer than that
// is at a cold moment, and one that keeps talking is not. The 5 seconds stretch and squeeze with --scale, like everything else the
// script does, so that a squeezed run (the tests) has the same shape as the real one.
const (
	shopColdMargin = 20 * time.Second
	shopSilence    = 5 * time.Second
)

// shopTTL is the lifetime of a cache entry in the shop scenario at a scale.
func shopTTL(scale float64) time.Duration {
	return shopColdMargin + time.Duration(float64(shopSilence)*scale)
}

// shopModel is the model of the shop scenario: the handbook's prices, a short cache lifetime and a small context window, so that a
// worker who reads a lot has a thread worth folding.
func shopModel(scale float64) cost.Model {
	m := model
	m.ContextTokens = 16_000
	cache := cost.OpenAICacheModel()
	cache.TTLs = []time.Duration{shopTTL(scale)}
	m.Cache = cache
	return m
}

// runShop runs the shop scenario: a manager, three scouts, four writers in worktrees of their own and a reviewer, against the
// cache-faithful mock endpoint, with latency.
func runShop(ctx context.Context, o Options) (*Report, error) {
	scale := o.Scale
	if scale <= 0 {
		scale = 1
	}
	if o.Timeout <= 0 {
		o.Timeout = 5 * time.Minute
	}
	out := o.Out
	if out == nil {
		out = io.Discard
	}
	dir := o.Dir
	if dir == "" {
		d, err := os.MkdirTemp("", "sleipnir-demo-")
		if err != nil {
			return nil, err
		}
		dir = d
	}
	ws := filepath.Join(dir, "shop")
	if err := os.MkdirAll(ws, 0o755); err != nil {
		return nil, err
	}
	if err := writeShop(ws); err != nil {
		return nil, fmt.Errorf("demo: the shop project needs git and sh: %w", err)
	}

	script := newShopScript(scale)
	m := shopModel(scale)
	srv := mock.New(mock.Config{
		Engine:     mock.EngineConfig{BlockTokens: 16, MinCacheTokens: 64, TTL: shopTTL(scale)},
		Price:      prices,
		FirstToken: time.Duration(float64(450*time.Millisecond) * scale),
		PrefillPer: time.Duration(float64(35*time.Microsecond) * scale),
		DecodePer:  time.Duration(float64(6*time.Millisecond) * scale),
	}, script.respond)
	script.srv = srv
	ts := srv.Start()
	defer ts.Close()
	prof := openaichat.DefaultProfile("demo", ts.URL)
	client := openaichat.New(openaichat.Config{Name: "demo", BaseURL: ts.URL, Profile: &prof,
		Options: openaichat.Options{SessionHeader: true, CacheKeyBody: true}})

	cfg := config.Defaults()
	cfg.Cache.ThreadSoftLimitTokens = 2_500
	cfg.Cache.CompactThresholdTokens = 9_000

	sdir := filepath.Join(dir, "session")
	s, err := session.New(ctx, session.Options{
		Cwd: ws, Root: ws, Home: filepath.Join(dir, "home"), Dir: sdir, Config: cfg,
		Provider: client, ModelInfo: &m, Model: m.ID, ContextWindow: m.ContextTokens,
		Mode: perm.ModeBypass, NoWeb: true, TrustProject: true, Offline: true,
		Swarm: true, MaxAgents: 12, Isolation: config.IsolationWorktree, Verify: "sh verify.sh",
	})
	if err != nil {
		return nil, err
	}
	defer s.Close()

	fmt.Fprintf(out, "Sleipnir demo: a team builds a small shop, against a cache-faithful mock endpoint (about twenty seconds).\n")
	fmt.Fprintf(out, "Everything but the model is real: git worktrees, the merge queue and its checks, mail, the cache planner, the governor, accounting.\n\n")

	rctx, cancel := context.WithTimeout(ctx, o.Timeout)
	defer cancel()
	start := time.Now()
	res, err := s.Run(rctx, shopGoal)
	if err != nil {
		return nil, fmt.Errorf("demo run: %w", err)
	}
	rep := &Report{Dir: sdir, Workspace: ws, Elapsed: time.Since(start), Scale: scale}
	snap := s.Swarm.Board.Snapshot()
	rep.Tasks = len(snap.Tasks)
	for _, t := range snap.Tasks {
		if t.Status == swarm.StatusDone {
			rep.TasksDone++
		}
	}
	if err := s.Close(); err != nil {
		return nil, err
	}
	if err := analyse(sdir, rep); err != nil {
		return nil, err
	}
	sc, err := shopCounts(sdir)
	if err != nil {
		return nil, err
	}
	rep.printShop(out, res.Text, sc)
	return rep, nil
}

// ShopCounts are the things the shop scenario exists to put in the log, counted from it.
type ShopCounts struct {
	Merged, Bounced int // the merge queue: integrations, and the ones sent back
	Mail            int
	Anomalies       int // cache anomalies
	Compactions     int
	Nudges          int // times the repetition guard told an agent it was repeating itself
}

// shopCounts reads the session's log and counts what the scenario is built to show.
func shopCounts(dir string) (ShopCounts, error) {
	var c ShopCounts
	err := events.Scan(filepath.Join(dir, "events.jsonl"), func(e events.Event) error {
		switch e.Type {
		case events.TypeMergeMerged, events.TypeMergeFastFwd:
			c.Merged++
		case events.TypeMergeVerifyFail, events.TypeMergeConflict, events.TypeMergeRejected:
			c.Bounced++
		case events.TypeMailSend:
			c.Mail++
		case events.TypeCacheAnomaly:
			c.Anomalies++
		case events.TypeCompactCommit:
			c.Compactions++
		case events.TypeAgentStuck:
			var p struct {
				Phase string `json:"phase"`
			}
			if json.Unmarshal(e.Data, &p) == nil && p.Phase == "nudge" {
				c.Nudges++
			}
		}
		return nil
	})
	return c, err
}

// CountShop is shopCounts for callers outside the package (the command's report and the tests of the recorded session).
func CountShop(dir string) (ShopCounts, error) { return shopCounts(dir) }

func (r *Report) printShop(w io.Writer, final string, c ShopCounts) {
	fmt.Fprintf(w, "manager: %s\n\n", strings.TrimSpace(final))
	fmt.Fprintf(w, "  agents             %d (1 manager, %d workers)\n", r.Agents, r.Agents-1)
	fmt.Fprintf(w, "  tasks              %d of %d accepted by the harness after its own gate\n", r.TasksDone, r.Tasks)
	fmt.Fprintf(w, "  merge queue        %d merged, %d sent back (each merge was checked on the merged result)\n", c.Merged, c.Bounced)
	fmt.Fprintf(w, "  mail               %d messages between agents\n", c.Mail)
	fmt.Fprintf(w, "  model requests     %d in %s\n", r.Requests, r.Elapsed.Round(time.Millisecond))
	fmt.Fprintf(w, "  input tokens       %d read from cache, %d written, %d uncached (hit ratio %.0f%%)\n", r.Usage.CacheRead, r.Usage.CacheWrite, r.Usage.Input, 100*r.HitRatio)
	fmt.Fprintf(w, "  cache              %d compactions, %d cache anomalies (the provider dropped its cache once, on purpose), %d nudge to an agent that repeated itself\n", c.Compactions, c.Anomalies, c.Nudges)
	fmt.Fprintf(w, "  cost               $%.4f\n", r.CostUSD)
	fmt.Fprintf(w, "    if every agent kept a private cache   $%.4f (%.0f%% less with the shared prefix)\n", r.NoShareUSD, pctLess(r.CostUSD, r.NoShareUSD))
	fmt.Fprintf(w, "    with no caching at all                $%.4f (%.0f%% less)\n", r.NoCacheUSD, pctLess(r.CostUSD, r.NoCacheUSD))
	fmt.Fprintf(w, "\nPrices are round demo numbers ($%.2f/M input, $%.3f/M cached, $%.2f/M output) and the cache lives %s here, not minutes, so that you can watch it cool; the model is a script.\n",
		prices.InputPerM, prices.CacheReadPerM, prices.OutputPerM, shopTTL(r.Scale))
	fmt.Fprintf(w, "Workspace: %s\nRecorded session: %s\nSee it again: sleipnir replay %s\n", r.Workspace, r.Dir, r.Dir)
}
