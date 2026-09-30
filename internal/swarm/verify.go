package swarm

import (
	"context"
	"errors"
	"fmt"
	"time"
)

// verifyResult is the outcome of the harness's verification gate.
type verifyResult struct {
	ok    bool // the gate passed, or no verifier is configured
	ran   bool // the verifier ran to completion (pass or fail)
	infra bool // it could not run: an error, a timeout, an interrupted swarm
	code  int
	out   string
	err   error
}

// verify runs the configured verifier (Config.VerifyCmd through Config.Verify) in
// dir. The harness, not the model, decides whether work is finished: this is the
// gate a worker's done passes through, and again when the manager accepts.
//
// The run is bounded: at most Config.MaxVerifies verifiers run at once (fifty workers
// finishing together must not start fifty test suites in one tree), each has a
// deadline, and a verifier that ignores its context cannot hold the caller past it.
// A verifier that could not run at all is reported as such (infra) and never as a
// failed test.
func (s *Swarm) verify(ctx context.Context, dir string) verifyResult {
	if s.cfg.VerifyCmd == "" {
		return verifyResult{ok: true}
	}
	if s.cfg.Verify == nil {
		return verifyResult{infra: true, err: errors.New("no verification runner is installed")}
	}
	select {
	case s.verifySem <- struct{}{}:
		defer func() { <-s.verifySem }()
	case <-ctx.Done():
		return verifyResult{infra: true, err: ctx.Err()}
	}
	vctx, cancel := context.WithTimeout(ctx, s.cfg.VerifyTimeout)
	defer cancel()
	type outcome struct {
		out  string
		code int
		err  error
	}
	ch := make(chan outcome, 1)
	go func() {
		defer func() {
			if r := recover(); r != nil {
				ch <- outcome{err: fmt.Errorf("the verifier crashed: %v", r)}
			}
		}()
		out, code, err := s.cfg.Verify(vctx, dir, s.cfg.VerifyCmd)
		ch <- outcome{out, code, err}
	}()
	select {
	case r := <-ch:
		if err := ctx.Err(); err != nil { // interrupted: whatever it printed is not a verdict
			return verifyResult{infra: true, err: err}
		}
		if r.err != nil {
			return verifyResult{infra: true, err: r.err, out: r.out}
		}
		return verifyResult{ran: true, ok: r.code == 0, code: r.code, out: r.out}
	case <-vctx.Done():
		if err := ctx.Err(); err != nil {
			return verifyResult{infra: true, err: err}
		}
		return verifyResult{infra: true, err: fmt.Errorf("verification timed out after %s", s.cfg.VerifyTimeout.Round(time.Second))}
	}
}
