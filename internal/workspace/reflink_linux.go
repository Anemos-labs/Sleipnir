//go:build linux && (amd64 || arm64 || arm || 386 || riscv64 || loong64 || s390x)

package workspace

import (
	"os"
	"syscall"
)

// ficlone is the ioctl number of FICLONE (_IOW(0x94, 9, int)) on the
// architectures that share the generic ioctl encoding.
const ficlone = 0x40049409

// tryReflink asks the filesystem to share in's extents with out (btrfs, XFS with
// reflink, bcachefs, ...). It reports success; on any error (different
// filesystem, no support, file not empty) nothing was written and the caller
// falls back to copying.
func tryReflink(out, in *os.File) bool {
	_, _, errno := syscall.Syscall(syscall.SYS_IOCTL, out.Fd(), ficlone, in.Fd())
	return errno == 0
}
