//go:build unix

package fs

import (
	iofs "io/fs"
	"os"
	"syscall"
)

// copyOwner gives f the owner and group of the file it is about to replace.
//
// A rename-based write creates a new inode owned by the running user. In the
// container setups this harness lives in (root editing a bind-mounted checkout
// owned by uid 1000) that would silently hand the developer's files to root.
// Failure is ignored: an unprivileged process cannot chown, and then the file
// is already owned by that process anyway.
func copyOwner(f *os.File, existing iofs.FileInfo) {
	st, ok := existing.Sys().(*syscall.Stat_t)
	if !ok {
		return
	}
	if int(st.Uid) == os.Getuid() && int(st.Gid) == os.Getgid() {
		return
	}
	_ = f.Chown(int(st.Uid), int(st.Gid))
}
