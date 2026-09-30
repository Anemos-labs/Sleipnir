//go:build unix

package fs

import (
	"os"
	"path/filepath"
	"syscall"
	"testing"
)

func TestReadNonRegularFile(t *testing.T) {
	env := testEnv(t)
	fifo := filepath.Join(env.Cwd, "pipe")
	if err := syscall.Mkfifo(fifo, 0o644); err != nil {
		t.Skip("mkfifo unavailable")
	}
	// Must return promptly instead of blocking on the pipe.
	contains(t, mustErr(t, run(t, Read{}, env, map[string]any{"path": "pipe"})), "not a regular file")
}

func TestAtomicWritePreservesOwnerWhenRoot(t *testing.T) {
	if os.Getuid() != 0 {
		t.Skip("needs root to chown")
	}
	dir := realTemp(t)
	p := filepath.Join(dir, "f.txt")
	writeFile(t, p, "old")
	if err := os.Chown(p, 12345, 23456); err != nil {
		t.Skip("chown not permitted here")
	}
	os.Chmod(p, 0o640)
	fi, _ := os.Stat(p)
	if err := atomicWrite(p, []byte("new"), fi); err != nil {
		t.Fatal(err)
	}
	after, _ := os.Stat(p)
	st := after.Sys().(*syscall.Stat_t)
	if st.Uid != 12345 || st.Gid != 23456 {
		t.Errorf("owner changed to %d:%d: a root-run harness would hand the developer's files to root", st.Uid, st.Gid)
	}
	if after.Mode().Perm() != 0o640 {
		t.Errorf("mode = %v", after.Mode().Perm())
	}
	if readFileT(t, p) != "new" {
		t.Errorf("content wrong")
	}
}
