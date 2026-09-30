package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"

	"github.com/reee344/sleipnir/internal/kv/sim"
)

func init() { extraCommands["sim"] = cmdSim }

// cmdSim runs the cache-policy simulator: what layering buys, and where it stops
// paying. Every number is an assumption made explicit in the printed workload;
// the event log of a real run records the quantities the simulator consumes, so
// calibrate before quoting absolute savings.
func cmdSim(_ context.Context, args []string) error {
	fs := flag.NewFlagSet("sim", flag.ExitOnError)
	agents := fs.Int("agents", 20, "concurrent workers")
	provider := fs.String("provider", "anthropic", "cache model: anthropic (explicit breakpoints, 5m TTL, 1.25x writes) | marketplace (automatic prefix cache, no write premium)")
	engines := fs.Int("engines", 3, "engines behind a marketplace provider")
	mode := fs.String("mode", "compare", "compare | scenarios | pins | agents")
	seed := fs.Int64("seed", 1, "workload seed")
	asJSON := fs.Bool("json", false, "print machine-readable results")
	fs.Usage = func() {
		fmt.Fprint(os.Stderr, `usage: sleipnir sim [flags]

Replays one synthetic swarm workload under a plain harness (whole history, and
with summary compaction) and under Sleipnir's layered policy, pricing every
request against an explicit model of the provider's cache.

modes:
  compare    the policies and Sleipnir's ablations on one workload
  scenarios  the standard sensitivity cases (short/long tasks, cold launches, small repo, bloated pins, ...)
  pins       sweep the pin size against a fixed orientation budget: where does pinning stop paying?
  agents     how the advantage scales with swarm size

flags:
`)
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		return err
	}
	var p sim.Provider
	switch *provider {
	case "anthropic":
		p = sim.Anthropic()
	case "marketplace", "openai":
		p = sim.Marketplace(*engines)
	default:
		return fmt.Errorf("sim: unknown --provider %q", *provider)
	}
	w := sim.DefaultWorkload(*agents)
	w.Seed = *seed

	var out any
	var text string
	switch *mode {
	case "compare":
		c := sim.Compare(w, p)
		out, text = c.Results, c.Table()
	case "scenarios":
		t, rows := sim.ScenarioTable(*agents, p)
		out, text = rows, t
	case "pins":
		t, pts := sim.PinSweep(*agents, p, []int{4000, 9000, 18000, 30000, 45000, 70000, 100000})
		out, text = pts, t
	case "agents":
		text = sim.AgentSweep(p, []int{1, 2, 5, 10, 20, 50, 100})
		out = text
	default:
		return fmt.Errorf("sim: unknown --mode %q", *mode)
	}
	if *asJSON {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(out)
	}
	fmt.Print(text)
	return nil
}
