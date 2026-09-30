package checkpoint_test

import (
	"github.com/reee344/sleipnir/internal/checkpoint"
	"github.com/reee344/sleipnir/internal/tools"
)

// The store satisfies tools.Snapshotter structurally; this package must not
// import internal/tools itself, so the assertion lives in an external test.
var _ tools.Snapshotter = (*checkpoint.Store)(nil)

// After has the same shape as tools.Guard.AfterWrite, so wiring it in is one line.
var _ interface{ AfterWrite(agent, path string) } = afterAdapter{}

type afterAdapter struct{ s *checkpoint.Store }

func (a afterAdapter) AfterWrite(agent, path string) { a.s.After(agent, path) }
