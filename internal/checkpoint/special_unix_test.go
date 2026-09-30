//go:build unix

package checkpoint

import (
	"os"
	"reflect"
	"strings"
	"syscall"
	"testing"
	"time"
)

func TestSpecialFilesAreRecordedButNeverTouched(t *testing.T) {
	e := newEnv(t)
	if err := syscall.Mkfifo(e.abs("pipe"), 0o644); err != nil {
		t.Skipf("cannot create a fifo: %v", err)
	}
	e.write("plain.txt", "orig")
	cp := e.s.Begin("p")

	// Opening a FIFO for reading would block forever: Before must not try.
	done := make(chan error, 1)
	go func() { done <- e.s.Before("a1", e.abs("pipe")) }()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Before on a fifo: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Before blocked on a FIFO")
	}
	e.edit("a1", "plain.txt", "changed")

	if got := e.s.List()[0].Unsaved; !reflect.DeepEqual(got, []string{"pipe"}) {
		t.Fatalf("Unsaved = %v", got)
	}
	// Remove the fifo behind the store's back so its state differs from the record.
	if err := os.Remove(e.abs("pipe")); err != nil {
		t.Fatal(err)
	}
	rep := mustRestore(t, e.s, cp, RestoreOpts{})
	if r := result(t, rep, "pipe"); r.Outcome != OutcomeUnrestorable || !strings.Contains(r.Detail, "cannot restore") {
		t.Fatalf("pipe = %+v", r)
	}
	if e.read("plain.txt") != "orig" {
		t.Fatal("regular files are still restored")
	}
}

func ownerOfPath(t *testing.T, path string) (uid, gid int) {
	t.Helper()
	fi, err := os.Lstat(path)
	if err != nil {
		t.Fatal(err)
	}
	st := fi.Sys().(*syscall.Stat_t)
	return int(st.Uid), int(st.Gid)
}

// A tool that replaces a file (atomic rename) gives the new inode to whoever the
// process runs as. When the harness runs as root over a user's files, a rewind
// must hand them back rather than leave the user's files owned by root.
func TestOwnershipIsRestoredWhenThePrivilegeAllowsIt(t *testing.T) {
	if os.Geteuid() != 0 {
		t.Skip("only a privileged process can give files to another user")
	}
	e := newEnv(t)
	e.write("f.txt", "orig")
	if err := os.MkdirAll(e.abs("d"), 0o755); err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{"f.txt", "d"} {
		if err := os.Chown(e.abs(p), 4242, 4343); err != nil {
			t.Skipf("cannot chown here: %v", err)
		}
	}
	cp := e.s.Begin("p")
	// Replace the file the way an atomic-write tool does: new inode, owned by us.
	if err := e.s.Before("a1", e.abs("f.txt")); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(e.abs("f.txt")); err != nil {
		t.Fatal(err)
	}
	e.write("f.txt", "new")
	if uid, _ := ownerOfPath(t, e.abs("f.txt")); uid != 0 {
		t.Fatalf("test setup: the replacement should be owned by root, got %d", uid)
	}
	// And delete a directory that someone else owned.
	if err := e.s.Before("a1", e.abs("d")); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(e.abs("d")); err != nil {
		t.Fatal(err)
	}

	rep := mustRestore(t, e.s, cp, RestoreOpts{})
	if !rep.OK() {
		t.Fatalf("%s\n%+v", rep.Summary(), rep.Files)
	}
	if e.read("f.txt") != "orig" {
		t.Fatal("content not restored")
	}
	for _, p := range []string{"f.txt", "d"} {
		if uid, gid := ownerOfPath(t, e.abs(p)); uid != 4242 || gid != 4343 {
			t.Errorf("%s is owned by %d:%d after the rewind, want 4242:4343", p, uid, gid)
		}
	}
}
