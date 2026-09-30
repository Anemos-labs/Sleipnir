package agent_test

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/reee344/sleipnir/internal/events"
	"github.com/reee344/sleipnir/internal/provider"
)

// blockProv is a provider that never answers: it tells the test it was called and waits for its context.
type blockProv struct {
	called chan struct{}
}

func (blockProv) Profile() provider.Profile { return cxAnthropicProfile() }

func (p blockProv) Do(ctx context.Context, _ *provider.Request, _ func(provider.Event)) (*provider.Response, error) {
	close(p.called)
	<-ctx.Done()
	return nil, ctx.Err()
}

// A run that is cancelled (a person's Ctrl-C, a harness deadline) says so in the log, and says what it was doing: the
// friction report counts these, and a cancelled request is not a failed one.
func TestACancelledRunIsRecorded(t *testing.T) {
	prov := blockProv{called: make(chan struct{})}
	a, log := cxAgent(t, cxOpts{prov: prov, noCompct: true})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		_, err := a.Run(ctx, "do the work")
		done <- err
	}()
	<-prov.called // the request is out: the run is waiting for the model
	cancel()
	if err := <-done; err == nil {
		t.Fatal("a cancelled run must return its context's error")
	}
	got := log.OfType(events.TypeAgentCancel)
	if len(got) != 1 {
		t.Fatalf("%d agent.cancel events, want 1", len(got))
	}
	var d struct {
		Phase, Cause string
		Steps        int
	}
	if err := json.Unmarshal(got[0].Data, &d); err != nil {
		t.Fatal(err)
	}
	if d.Phase != "model" || d.Cause != "canceled" || d.Steps != 0 {
		t.Errorf("agent.cancel = %+v, want the model phase, cause canceled, 0 steps", d)
	}
}

func TestARunThatEndsNormallyRecordsNoCancel(t *testing.T) {
	prov := &cxProv{prof: cxAnthropicProfile()}
	prov.handle = cxWorkModel(2, nil)
	a, log := cxAgent(t, cxOpts{prov: prov, noCompct: true})
	if _, err := a.Run(context.Background(), "do the work"); err != nil {
		t.Fatal(err)
	}
	if n := len(log.OfType(events.TypeAgentCancel)); n != 0 {
		t.Errorf("%d agent.cancel events for a run nobody cancelled", n)
	}
}
