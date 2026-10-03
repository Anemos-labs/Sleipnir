package env

import (
	"context"
	"errors"
	"fmt"
)

// ErrInfra marks a failure of the environment machinery itself: a setup
// command that could not run, a git error on data the harness owns, a
// missing hidden-file blob, a sandbox that would not start.
//
// The distinction matters for training. An infrastructure failure says nothing
// about the policy, so the episode is flagged infra_error and dropped by the
// exporters; anything the policy can influence (a slow test, a giant diff, a
// broken repository it left behind) must NEVER be classified this way, or the
// policy would learn that provoking an "infra error" is a way to avoid a
// negative reward.
var ErrInfra = errors.New("infrastructure error")

// InfraError is the concrete type behind ErrInfra.
type InfraError struct {
	Op  string // what the environment was doing ("checkout", "apply", ...)
	Err error
}

// Error formats an infrastructure operation failure and uses a generic description when no cause
// exists.
func (e *InfraError) Error() string {
	if e.Err == nil {
		return "env: " + e.Op + ": infrastructure error"
	}
	return "env: " + e.Op + ": " + e.Err.Error()
}

// Unwrap exposes the infrastructure error's underlying cause.
func (e *InfraError) Unwrap() error { return e.Err }

// Is makes errors.Is(err, ErrInfra) true for every InfraError.
func (e *InfraError) Is(target error) bool { return target == ErrInfra }

// Infra wraps err as an infrastructure failure of operation op. It is
// exported so a Harness implementation can mark its own outages (provider
// down, log disk full) the same way; Runner treats them like any other.
// Context cancellation is never infrastructure and passes through untouched.
func Infra(op string, err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return err
	}
	var ie *InfraError
	if errors.As(err, &ie) {
		return err
	}
	return &InfraError{Op: op, Err: err}
}

// Infraf is Infra with a formatted message.
func Infraf(op, format string, args ...any) error {
	return &InfraError{Op: op, Err: fmt.Errorf(format, args...)}
}

// IsInfra reports whether err is an infrastructure failure.
func IsInfra(err error) bool { return errors.Is(err, ErrInfra) }
