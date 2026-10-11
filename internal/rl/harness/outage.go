package harness

import (
	"context"
	"fmt"
	"sync"

	"github.com/anemos-labs/sleipnir/internal/events"
	"github.com/anemos-labs/sleipnir/internal/provider"
	"github.com/anemos-labs/sleipnir/internal/rl/env"
)

// outageWatch tells the runner's clock (env.WallClock) when the endpoints of a rollout go down and come back, and records both in
// the session's log (events.TypeRLOutage). An outage begins at the first failure that is one (provider.Error.Outage: a status of
// 500 or more or 429, or nothing answered) and ends at the next answer from any model of the rollout. One watch serves every
// provider of the rollout, the policy's and the roles' alike: a team's requests share one clock.
type outageWatch struct {
	clock *env.WallClock

	mu  sync.Mutex
	log *events.Log // nil until the session exists, and again once it is closing
}

// wrap puts p behind the watch. Without a clock there is nothing to tell, and p is returned as it is.
func (w *outageWatch) wrap(p provider.Provider) provider.Provider {
	if w == nil || w.clock == nil || p == nil {
		return p
	}
	return &watched{Provider: p, w: w}
}

// attach sets the log the watch records outages in (nil: none).
func (w *outageWatch) attach(l *events.Log) {
	w.mu.Lock()
	w.log = l
	w.mu.Unlock()
}

// down reports a failure that is an outage, and records the outage when it is the one that began it.
func (w *outageWatch) down(pe *provider.Error) {
	what := pe.Kind.String()
	if pe.Status != 0 {
		what += fmt.Sprintf(" (http %d)", pe.Status)
	}
	if m := provider.SanitizeText(pe.Message, 160); m != "" {
		what += ": " + m
	}
	if !w.clock.Down(what) {
		return
	}
	w.record(map[string]any{
		"phase": "start", "kind": pe.Kind.String(), "status": pe.Status, "class": pe.Outage().String(), "error": what,
		"active_ms": w.clock.Active().Milliseconds(), "paused_ms": w.clock.Paused().Milliseconds(),
	})
}

// up reports an answer, and records the end of the outage it ends, if any.
func (w *outageWatch) up() {
	lasted, ended := w.clock.Up()
	if !ended {
		return
	}
	w.record(map[string]any{
		"phase": "end", "outage_ms": lasted.Milliseconds(),
		"active_ms": w.clock.Active().Milliseconds(), "paused_ms": w.clock.Paused().Milliseconds(),
	})
}

// record writes an rl.outage event to the attached log; a failure to write loses the record, never the run.
func (w *outageWatch) record(data map[string]any) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.log != nil {
		_, _ = w.log.Emit("", events.TypeRLOutage, data)
	}
}

// watched is a provider whose outcomes an outageWatch reads.
type watched struct {
	provider.Provider
	w *outageWatch
}

// EffortLevels is the wrapped adapter's range of reasoning efforts for model (provider.EffortSetting asks a provider for it), or
// the model table's when the adapter has none: the watch must not change what a request asks for.
func (p *watched) EffortLevels(model string) ([]string, bool) {
	if s, ok := p.Provider.(interface{ EffortLevels(string) ([]string, bool) }); ok {
		return s.EffortLevels(model)
	}
	return provider.ModelEfforts(model)
}

// Do implements provider.Provider. A call the caller stopped says nothing about the endpoint. A failure with a status that is no
// outage (a 400) is still an answer: the endpoint is up.
func (p *watched) Do(ctx context.Context, req *provider.Request, on func(provider.Event)) (*provider.Response, error) {
	resp, err := p.Provider.Do(ctx, req, on)
	if ctx.Err() != nil {
		return resp, err
	}
	if err == nil {
		p.w.up()
		return resp, nil
	}
	if pe, ok := provider.AsError(err); ok {
		switch {
		case pe.Outage() != provider.NotOutage:
			p.w.down(pe)
		case pe.Status != 0:
			p.w.up()
		}
	}
	return resp, err
}
