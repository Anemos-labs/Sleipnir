//go:build unix

package checkpoint

import (
	"io/fs"
	"os"
	"syscall"
)

// ownerOf reads the owning user and group from file info.
func ownerOf(fi fs.FileInfo) *owner {
	if st, ok := fi.Sys().(*syscall.Stat_t); ok {
		return &owner{UID: int(st.Uid), GID: int(st.Gid)}
	}
	return nil
}

// chownPath restores ownership best effort. Only a privileged process can give
// a file to someone else, so failure is the normal case for everyone else, and
// not worth failing a rewind over: the content and mode are what matter.
func chownPath(path string, o *owner) {
	if o != nil {
		_ = os.Lchown(path, o.UID, o.GID)
	}
}
