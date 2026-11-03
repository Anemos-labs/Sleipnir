//go:build !unix

package perm

// trustedInterpreter is true where file ownership is not checked this way.
func trustedInterpreter(string) bool { return true }

// rootGroup is never asked off Unix, where ownership is not checked.
func rootGroup(uint32) bool { return false }
