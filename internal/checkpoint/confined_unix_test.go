//go:build unix

package checkpoint

import (
	"io/fs"
	"os"
	"testing"
)

// A recorded set-user-ID or set-group-ID bit is not restored: the manifest is not
// authenticated, and a privileged rewind would otherwise install recorded content
// as a program that runs with its recorded owner's rights.
func TestRestoreDropsSetIDBits(t *testing.T) {
	e := newEnv(t)
	e.writeMode("tool", "#!/bin/sh\n", 0o755|fs.ModeSetuid|fs.ModeSetgid)
	fi, err := os.Stat(e.abs("tool"))
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode()&fs.ModeSetuid == 0 {
		t.Skip("this filesystem does not keep the set-user-ID bit")
	}
	cp := e.s.Begin("p")
	e.edit("a1", "tool", "changed\n")
	mustRestore(t, e.s, cp, RestoreOpts{})
	fi, err = os.Stat(e.abs("tool"))
	if err != nil {
		t.Fatal(err)
	}
	if e.read("tool") != "#!/bin/sh\n" {
		t.Fatalf("content = %q", e.read("tool"))
	}
	if fi.Mode()&(fs.ModeSetuid|fs.ModeSetgid) != 0 || fi.Mode().Perm() != 0o755 {
		t.Fatalf("mode = %v, want 0755 without set-ID bits", fi.Mode())
	}
}

// A parent directory replaced by a symlink to somewhere outside the project
// between planning and writing does not lead the write there.
func TestRestoreDoesNotWriteThroughAParentSwappedForASymlink(t *testing.T) {
	e := newEnv(t)
	e.write("sub/x.txt", "orig")
	cp := e.s.Begin("p")
	e.edit("a1", "sub/x.txt", "new")
	outside := e.base + "/outside"
	if err := os.MkdirAll(outside, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.RemoveAll(e.abs("sub")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, e.abs("sub")); err != nil {
		t.Fatal(err)
	}
	rep, _ := e.s.Restore(cp, RestoreOpts{})
	if _, err := os.Stat(outside + "/x.txt"); err == nil {
		t.Fatalf("the restore wrote outside the project: %s", rep.Summary())
	}
}
