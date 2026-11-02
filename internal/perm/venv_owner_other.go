//go:build !unix

package perm

// trustedInterpreter is true where file ownership is not checked this way.
func trustedInterpreter(string) bool { return true }
