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
	if rep.Count(OutcomeDone) != 0 {
		t.Fatalf("the restore reported the swapped path as rewound: %s", rep.Summary())
	}
}

// The confined operations themselves: a parent swapped for a symlink after the
// key was resolved (so resolveKey's check has passed) does not lead a write, a
// chmod or a mkdir outside the project.
func TestConfinedOperationsDoNotFollowAParentSwappedAfterResolving(t *testing.T) {
	e := newEnv(t)
	e.write("sub/x.txt", "orig")
	outside := e.base + "/outside"
	if err := os.MkdirAll(outside, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(outside+"/x.txt", []byte("host"), 0o600); err != nil {
		t.Fatal(err)
	}
	abs, err := e.s.resolveKey("sub/x.txt")
	if err != nil {
		t.Fatal(err)
	}
	c, err := e.s.confine("sub/x.txt", abs)
	if err != nil {
		t.Fatal(err)
	}
	defer c.close()
	if err := os.RemoveAll(e.abs("sub")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, e.abs("sub")); err != nil {
		t.Fatal(err)
	}
	if err := c.writeFile([]byte("new"), 0o644, nil); err == nil {
		t.Error("a write through the swapped parent succeeded")
	}
	if err := c.chmod(0o777); err == nil {
		t.Error("a chmod through the swapped parent succeeded")
	}
	if err := c.remove(); err == nil {
		t.Error("a removal through the swapped parent succeeded")
	}
	if err := c.mkdirAll(); err == nil {
		t.Error("a mkdir through the swapped parent succeeded")
	}
	b, _ := os.ReadFile(outside + "/x.txt")
	fi, _ := os.Stat(outside + "/x.txt")
	if string(b) != "host" || fi == nil || fi.Mode().Perm() != 0o600 {
		t.Fatalf("the file outside the project changed: %q %v", b, fi)
	}
}

// A directory keeps its recorded set-group-ID bit, applied through a descriptor of the directory itself; a file at the path does
// not get it.
func TestConfinedChmodKeepsSetIDOnlyOnTheDirectory(t *testing.T) {
	root := t.TempDir()
	if err := os.Mkdir(root+"/d", 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(root+"/f", []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	r, err := os.OpenRoot(root)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	for _, c := range []struct {
		rel   string
		setID bool
	}{{"d", true}, {"f", false}} {
		cf := &confined{root: r, rel: c.rel, abs: root + "/" + c.rel}
		if err := cf.chmod(0o755 | fs.ModeSetgid); err != nil {
			t.Fatalf("%s: %v", c.rel, err)
		}
		fi, err := os.Stat(root + "/" + c.rel)
		if err != nil {
			t.Fatal(err)
		}
		if got := fi.Mode()&fs.ModeSetgid != 0; got && !c.setID { // a filesystem may drop the bit on the directory too
			t.Errorf("%s: set-group-ID = %v, want %v", c.rel, got, c.setID)
		}
	}
}
