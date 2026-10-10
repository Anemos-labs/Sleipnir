//go:build !unix

package tools

import "testing"

// holdSession is not available here: the tests that need a held session skip.
func holdSession(t *testing.T, _ string) func() {
	t.Skip("session locks are probed with flock in these tests")
	return func() {}
}
