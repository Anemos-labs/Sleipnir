//go:build unix

package perm

import (
	"os"
	"path/filepath"
	"runtime"
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
	// the file and every directory above it: one that someone else may write lets them put another tree under the interpreter's
	// name. A sticky directory (/tmp) lets no one rename what others own in it, so it counts as safe above an owned one.
	for p, first := target, true; ; p, first = filepath.Dir(p), false {
		fi, err := os.Stat(p)
		if err != nil {
			return false
		}
		st, ok := fi.Sys().(*syscall.Stat_t)
		if !ok || st.Uid != 0 && st.Uid != uid {
			return false
		}
		w := fi.Mode().Perm() & 0o022
		if w&0o020 != 0 && rootGroup(st.Gid) {
			w &^= 0o020 // a group whose members can act as root anyway (Homebrew's admin-writable /opt/homebrew)
		}
		if w != 0 && (first || fi.Mode()&os.ModeSticky == 0) {
			return false
		}
		if d := filepath.Dir(p); d == p {
			return true
		}
	}
}

// rootGroup reports whether a group's members can already act as root, so that its write to a path gives no one a right they do
// not have: root's own group, and on macOS admin (gid 80), whose members sudo by default and own Homebrew's tree.
func rootGroup(gid uint32) bool {
	return gid == 0 || runtime.GOOS == "darwin" && gid == 80
}
