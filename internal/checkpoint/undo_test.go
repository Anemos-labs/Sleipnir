package checkpoint

import (
	"errors"
	"fmt"
	"math/rand"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"testing"

	"github.com/anemos-labs/sleipnir/internal/core"
)

// tree reads every regular file under the root (relative path -> content).
func (e *testEnv) tree() map[string]string {
	e.t.Helper()
	out := map[string]string{}
	err := filepath.Walk(e.root, func(p string, fi os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if fi.Mode().IsRegular() {
			b, err := os.ReadFile(p)
			if err != nil {
				return err
			}
			rel, _ := filepath.Rel(e.root, p)
			out[filepath.ToSlash(rel)] = string(b)
		}
		return nil
	})
	if err != nil {
		e.t.Fatal(err)
	}
	return out
}

func ids(list []Info) []string {
	var out []string
	for _, i := range list {
		out = append(out, fmt.Sprintf("%s%v", i.ID, i.Files))
	}
	return out
}

func TestRestorePreviewApplyUndo(t *testing.T) {
	e := newEnv(t)
	e.write("a.txt", "a0")
	e.write("gone.txt", "g0")
	cp1 := e.s.Begin("one")
	e.editAfter("w1", "a.txt", "a1")
	e.editAfter("w1", "new/n.txt", "n1")
	cp2 := e.s.Begin("two")
	e.editAfter("w2", "a.txt", "a2")
	e.remove("w2", "gone.txt")
	e.s.After("w2", e.abs("gone.txt"))
	before := e.tree()
	listBefore := ids(e.s.List())

	// The preview writes nothing.
	plan := mustRestore(t, e.s, cp1, RestoreOpts{DryRun: true})
	if !reflect.DeepEqual(e.tree(), before) || plan.Count(OutcomePlanned) == 0 {
		t.Fatalf("dry run changed files or planned nothing: %+v", plan)
	}

	rep, undo, err := e.s.RestoreWithUndo(cp1)
	if err != nil || undo == "" || !rep.Rewound {
		t.Fatalf("restore: %+v %q %v", rep, undo, err)
	}
	if got := e.tree(); got["a.txt"] != "a0" || got["gone.txt"] != "g0" || got["new/n.txt"] != "" {
		t.Fatalf("after restore: %v", got)
	}
	if e.exists("new") {
		t.Fatal("the directory the edit created must be gone")
	}
	if l := e.s.List(); len(l) != 1 || l[0].ID != cp1 {
		t.Fatalf("a full rewind drops the later checkpoints: %v", ids(l))
	}
	if u := e.s.Undos(); len(u) != 1 || u[0].ID != undo {
		t.Fatalf("Undos = %+v", u)
	}

	// Undo puts the files back and, nothing having changed since, the checkpoints too.
	urep, err := e.s.RestoreUndo(undo)
	if err != nil || !urep.OK() {
		t.Fatalf("undo: %+v %v", urep, err)
	}
	if got := e.tree(); !reflect.DeepEqual(got, before) {
		t.Fatalf("after undo: %v, want %v", got, before)
	}
	if got := ids(e.s.List()); !reflect.DeepEqual(got, listBefore) {
		t.Fatalf("checkpoints after undo: %v, want %v", got, listBefore)
	}
	if _, err := e.s.RestoreUndo(undo); !errors.Is(err, ErrUnknownUndo) {
		t.Fatalf("an undo is used once: %v", err)
	}
	// /rewind works on the reinstated history.
	if _, ok := mustContentAt(t, e.s, cp2, "gone.txt"); !ok {
		t.Fatal("cp2's records are back")
	}
	if rep := mustRestore(t, e.s, cp2, RestoreOpts{}); !rep.OK() || e.read("a.txt") != "a1" {
		t.Fatalf("rewind after undo: %+v", rep)
	}
}

func TestUndoRefusesWhatChangedSince(t *testing.T) {
	e := newEnv(t)
	e.write("a.txt", "a0")
	e.write("b.txt", "b0")
	cp := e.s.Begin("one")
	e.editAfter("w", "a.txt", "a1")
	e.editAfter("w", "b.txt", "b1")
	_, undo, err := e.s.RestoreWithUndo(cp)
	if err != nil || undo == "" {
		t.Fatal(err)
	}
	// Someone edits a restored file afterwards: the undo must not destroy it.
	if err := os.WriteFile(e.abs("a.txt"), []byte("human"), 0o644); err != nil {
		t.Fatal(err)
	}
	rep, err := e.s.RestoreUndo(undo)
	if !errors.Is(err, ErrUndoConflict) || result(t, rep, "a.txt").Outcome != OutcomeConflict {
		t.Fatalf("undo over a later edit: %+v %v", rep, err)
	}
	if e.read("a.txt") != "human" || e.read("b.txt") != "b0" {
		t.Fatal("a refused undo writes nothing")
	}
	// The capture stays: putting the edit back as the restore left it makes it possible.
	if err := os.WriteFile(e.abs("a.txt"), []byte("a0"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := e.s.RestoreUndo(undo); err != nil || e.read("a.txt") != "a1" || e.read("b.txt") != "b1" {
		t.Fatalf("undo: %v %q %q", err, e.read("a.txt"), e.read("b.txt"))
	}
}

func TestUndoAfterNewWorkIsRecordedAsAWrite(t *testing.T) {
	e := newEnv(t)
	e.write("a.txt", "a0")
	cp := e.s.Begin("one")
	e.editAfter("w", "a.txt", "a1")
	_, undo, err := e.s.RestoreWithUndo(cp)
	if err != nil {
		t.Fatal(err)
	}
	// New work after the restore: the checkpoints cannot simply come back.
	e.editAfter("w", "other.txt", "o")
	if _, err := e.s.RestoreUndo(undo); err != nil {
		t.Fatal(err)
	}
	if e.read("a.txt") != "a1" || e.read("other.txt") != "o" {
		t.Fatal("the undo puts a.txt back and leaves the new work")
	}
	if a, _, _ := e.s.LastWriter("a.txt"); a != PersonAgent {
		t.Fatalf("the undo is a person's write: %q", a)
	}
	// and a rewind of the checkpoint undoes the undo as well
	mustRestore(t, e.s, cp, RestoreOpts{})
	if e.read("a.txt") != "a0" || e.exists("other.txt") {
		t.Fatalf("rewind after undo: %q", e.read("a.txt"))
	}
}

func TestWriteFileAndUndo(t *testing.T) {
	e := newEnv(t)
	e.s.EnableJournal(0)
	e.writeMode("f.sh", "#!/bin/sh\necho one\necho two\n", 0o755)
	e.s.Begin("p")
	e.editAfter("w", "f.sh", "#!/bin/sh\necho one\necho TWO\n")
	if err := os.Chmod(e.abs("f.sh"), 0o755); err != nil {
		t.Fatal(err)
	}
	cur := core.HashBytes([]byte("#!/bin/sh\necho one\necho TWO\n"))
	if _, err := e.s.WriteFile(PersonAgent, "f.sh", []byte("x"), core.HashBytes([]byte("stale"))); !errors.Is(err, ErrChanged) {
		t.Fatalf("a stale expectation must be refused: %v", err)
	}
	if _, err := e.s.WriteFile(PersonAgent, "../out.txt", []byte("x"), cur); !errors.Is(err, ErrOutsideRoot) {
		t.Fatalf("outside the root: %v", err)
	}
	undo, err := e.s.WriteFile(PersonAgent, "f.sh", []byte("#!/bin/sh\necho one\necho two\n"), cur)
	if err != nil {
		t.Fatal(err)
	}
	if e.read("f.sh") != "#!/bin/sh\necho one\necho two\n" || e.mode("f.sh").Perm() != 0o755 {
		t.Fatalf("write: %q %v", e.read("f.sh"), e.mode("f.sh"))
	}
	ws := e.s.Writes("f.sh")
	if ws[len(ws)-1].Agent != PersonAgent {
		t.Fatalf("the person's write is journaled: %+v", ws)
	}
	if _, err := e.s.RestoreUndo(undo); err != nil || e.read("f.sh") != "#!/bin/sh\necho one\necho TWO\n" {
		t.Fatalf("undo: %v %q", err, e.read("f.sh"))
	}
	// A second write, then the file changes: its undo is refused.
	cur = core.HashBytes([]byte(e.read("f.sh")))
	undo, err = e.s.WriteFile(PersonAgent, "f.sh", []byte("v3\n"), cur)
	if err != nil {
		t.Fatal(err)
	}
	e.editAfter("w", "f.sh", "agent again\n")
	if _, err := e.s.RestoreUndo(undo); !errors.Is(err, ErrUndoConflict) || e.read("f.sh") != "agent again\n" {
		t.Fatalf("undo over newer work: %v %q", err, e.read("f.sh"))
	}
}

func TestCaptureSealAndDrop(t *testing.T) {
	e := newEnv(t)
	e.write("a.txt", "a")
	e.write("b.txt", "b")
	id, err := e.s.CaptureUndo([]string{"a.txt", "b.txt", "absent.txt"})
	if err != nil {
		t.Fatal(err)
	}
	e.write("a.txt", "A")
	e.write("absent.txt", "now here")
	if err := e.s.SealUndo(id, []string{"a.txt", "absent.txt"}); err != nil {
		t.Fatal(err)
	}
	if u := e.s.Undos(); len(u) != 1 || !reflect.DeepEqual(u[0].Files, []string{"a.txt", "absent.txt"}) {
		t.Fatalf("sealed capture keeps what was written: %+v", u)
	}
	if _, err := e.s.RestoreUndo(id); err != nil {
		t.Fatal(err)
	}
	if e.read("a.txt") != "a" || e.exists("absent.txt") {
		t.Fatal("the capture's states are back")
	}
	id2, _ := e.s.CaptureUndo([]string{"a.txt"})
	if err := e.s.DropUndo(id2); err != nil || len(e.s.Undos()) != 0 {
		t.Fatalf("drop: %v", err)
	}
	for _, bad := range []string{"", "u_", "u_0", "cp_0001", "../u_0001", "u_99999999999"} {
		if _, err := e.s.RestoreUndo(bad); !errors.Is(err, ErrUnknownUndo) {
			t.Errorf("RestoreUndo(%q) = %v", bad, err)
		}
	}
}

func TestUndoCapturesAreBounded(t *testing.T) {
	e := newEnv(t)
	e.write("a.txt", "a")
	for range maxUndos + 5 {
		if _, err := e.s.CaptureUndo([]string{"a.txt"}); err != nil {
			t.Fatal(err)
		}
	}
	u := e.s.Undos()
	if len(u) != maxUndos || u[0].ID != undoID(6) {
		t.Fatalf("kept %d, oldest %s", len(u), u[0].ID)
	}
}

func TestUndoCaptureFromDiskIsVetted(t *testing.T) {
	e := newEnv(t)
	e.write("a.txt", "a")
	id, err := e.s.CaptureUndo([]string{"a.txt"})
	if err != nil {
		t.Fatal(err)
	}
	// A hostile capture names a path outside the root and a state with no checksum.
	bad := fmt.Sprintf(`{"v":1,"id":%q,"label":"x","files":[{"path":"../evil","pre":{"kind":"absent"}},{"path":"/etc/passwd","pre":{"kind":"absent"}},{"path":"a.txt","pre":{"kind":"file","blob":"zz"}}]}`, id)
	if err := os.WriteFile(filepath.Join(e.dir, "undo", id+".json"), []byte(bad), 0o600); err != nil {
		t.Fatal(err)
	}
	rep, err := e.s.RestoreUndo(id)
	if err != nil || len(rep.Files) != 0 || e.read("a.txt") != "a" {
		t.Fatalf("a vetted capture does nothing: %+v %v", rep, err)
	}
}

// Property: restore then undo returns the tree to what it was, for random histories.
func TestRestoreThenUndoProperty(t *testing.T) {
	rng := rand.New(rand.NewSource(11))
	for trial := range 25 {
		e := newEnv(t)
		names := []string{"a.txt", "d/b.txt", "d/e/c.txt", "z.txt"}
		for _, n := range names[:2] {
			e.write(n, "seed "+n)
		}
		var cps []string
		for k := range 4 {
			cps = append(cps, e.s.Begin(fmt.Sprintf("turn %d", k)))
			for range 1 + rng.Intn(4) {
				n := names[rng.Intn(len(names))]
				if rng.Intn(5) == 0 && e.exists(n) {
					e.remove("w", n)
					e.s.After("w", e.abs(n))
					continue
				}
				e.editAfter(fmt.Sprintf("w%d", rng.Intn(2)), n, fmt.Sprintf("t%d k%d %d", trial, k, rng.Intn(1000)))
			}
		}
		before := e.tree()
		target := cps[rng.Intn(len(cps))]
		rep, undo, err := e.s.RestoreWithUndo(target)
		if err != nil || !rep.OK() {
			t.Fatalf("trial %d restore: %+v %v", trial, rep, err)
		}
		if undo == "" {
			if !reflect.DeepEqual(e.tree(), before) {
				t.Fatalf("trial %d: nothing to undo but the tree changed", trial)
			}
			continue
		}
		if _, err := e.s.RestoreUndo(undo); err != nil {
			t.Fatalf("trial %d undo: %v", trial, err)
		}
		if got := e.tree(); !reflect.DeepEqual(got, before) {
			keys := func(m map[string]string) []string {
				var k []string
				for x := range m {
					k = append(k, x)
				}
				sort.Strings(k)
				return k
			}
			t.Fatalf("trial %d: tree %v, want %v", trial, keys(got), keys(before))
		}
	}
}
