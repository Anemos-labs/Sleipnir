//go:build !(linux && (amd64 || arm64 || arm || 386 || riscv64 || loong64 || s390x))

package workspace

import "os"

// No reflink support on this platform (macOS's clonefile and Windows' block
// cloning are not wired up); the fallback is an ordinary copy.
func tryReflink(out, in *os.File) bool { return false }
