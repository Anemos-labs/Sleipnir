package main

import (
	"context"
	"net/http"
	"sync"
	"time"

	"github.com/anemos-labs/sleipnir/internal/sandbox"
	"github.com/anemos-labs/sleipnir/internal/web/wire"
)

// sandboxProbeTTL is how long an answer about the container engine is kept: asking docker takes a moment, and the pages ask each
// time a dialog opens, while an engine installed or started meanwhile is seen soon enough.
const sandboxProbeTTL = 30 * time.Second

// sandboxProbe asks whether a session can have a sandbox here (sandbox.Available), at most once per sandboxProbeTTL. Its zero
// value is ready.
type sandboxProbe struct {
	mu   sync.Mutex
	at   time.Time
	last wire.SandboxStatus
	// check is sandbox.Available; a test sets another
	check func(context.Context) error
}

func (p *sandboxProbe) status(ctx context.Context) wire.SandboxStatus {
	p.mu.Lock()
	defer p.mu.Unlock()
	if !p.at.IsZero() && time.Since(p.at) < sandboxProbeTTL {
		return p.last
	}
	check := p.check
	if check == nil {
		check = sandbox.Available
	}
	if err := check(ctx); err != nil {
		p.last = wire.SandboxStatus{Why: err.Error()}
	} else {
		p.last = wire.SandboxStatus{Available: true}
	}
	p.at = time.Now()
	return p.last
}

// sandboxStatus is GET /api/sandbox: whether a session started here can run its commands in a sandbox (--sandbox docker), so
// that the pages say so before a session that asks for one fails to start.
func (h *webHostImpl) sandboxStatus(w http.ResponseWriter, r *http.Request) {
	ok(w, h.sandbox.status(r.Context()))
}
