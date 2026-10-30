//go:build !unix

package sched

// alive is not used where the test that needs it is skipped.
func alive(int) bool { return false }
