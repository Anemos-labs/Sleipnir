//go:build unix

package perm

import (
	"os"
	"path/filepath"
	"strings"
	"syscall"
)

// trustedInterpreter reports whether the installed interpreter at target (a resolved path) is one that only its owner, who is root or
// this user, can change: the file and its directory are owned so and not writable by others. A python another local user put in /tmp
// or in a directory everyone may write passes every other check of ownInterpreter, and would run with this user's rights.
func trustedInterpreter(target string) bool {
	if strings.Contains(target, "..") {
		return false // a resolved path has no ".." segment; a name that holds the two dots is not judged
	}
	uid := uint32(os.Getuid())
	for _, p := range []string{target, filepath.Dir(target)} {
		fi, err := os.Stat(p)
		if err != nil {
			return false
		}
		st, ok := fi.Sys().(*syscall.Stat_t)
		if !ok || st.Uid != 0 && st.Uid != uid || fi.Mode().Perm()&0o022 != 0 {
			return false
		}
	}
	return true
}
