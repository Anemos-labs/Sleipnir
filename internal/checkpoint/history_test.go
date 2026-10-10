package checkpoint

import (
	"errors"
	"fmt"
	"math/rand"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"sync"
	"testing"
)

func mustContentAt(t *testing.T, s *Store, id, path string) (string, bool) {
	t.Helper()
	b, ok, err := s.ContentAt(id, path)
	if err != nil {
		t.Fatalf("ContentAt(%s, %s): %v", id, path, err)
	}
	return string(b), ok
}

func TestContentAt(t *testing.T) {
	e := newEnv(t)
	e.write("a.txt", "v1\n")
	e.write("c.txt", "doomed\n")
	e.write("untouched.txt", "same\n")
	cp1 := e.s.Begin("one")
	e.editAfter("x", "a.txt", "v2\n")
	e.editAfter("x", "b.txt", "born\n")
	cp2 := e.s.Begin("two")
	e.editAfter("y", "a.txt", "v3\n")
	e.remove("y", "c.txt")
	e.s.After("y", e.abs("c.txt"))
	cp3 := e.s.Begin("three")

	cases := []struct {
		id, path, want string
		exists         bool
	}{
		{cp1, "a.txt", "v1\n", true},
		{cp2, "a.txt", "v2\n", true},
		{cp3, "a.txt", "v3\n", true}, // no later record: the live file
		{cp1, "b.txt", "", false},    // created in cp1: absent when it began
		{cp2, "b.txt", "born\n", true},
		{cp1, "c.txt", "doomed\n", true},
		{cp2, "c.txt", "doomed\n", true},
		{cp3, "c.txt", "", false}, // deleted in cp2
		{cp1, "untouched.txt", "same\n", true},
		{"1", "a.txt", "v1\n", true}, // the id forms normalizeID accepts
		{"cp_2", "a.txt", "v2\n", true},
	}
	for _, c := range cases {
		got, ok := mustContentAt(t, e.s, c.id, c.path)
		if got != c.want || ok != c.exists {
			t.Errorf("ContentAt(%s, %s) = %q, %v; want %q, %v", c.id, c.path, got, ok, c.want, c.exists)
		}
	}
	if _, _, err := e.s.ContentAt("cp_0099", "a.txt"); !errors.Is(err, ErrUnknownCheckpoint) {
		t.Fatalf("unknown checkpoint: %v", err)
	}
	// An absolute path inside the root is the same file.
	if got, _ := mustContentAt(t, e.s, cp1, e.abs("a.txt")); got != "v1\n" {
		t.Fatalf("absolute path inside the root: %q", got)
	}
}

func TestContentAtRefusesUnsafePaths(t *testing.T) {
	e := newEnv(t)
	e.write("ok.txt", "fine")
	outside := filepath.Join(e.base, "outside")
	if err := os.MkdirAll(outside, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(outside, "secret"), []byte("s3cret"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, e.abs("door")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(outside, "secret"), e.abs("link.txt")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("loop2", e.abs("loop1")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("loop1", e.abs("loop2")); err != nil {
		t.Fatal(err)
	}
	e.s.Begin("p")
	for _, p := range []string{"../outside/secret", filepath.Join(outside, "secret"), "door/secret", "/etc/passwd", "a\x00b", "", strings.Repeat("x/", 3000)} {
		if _, _, err := e.s.ContentAt("", p); !errors.Is(err, ErrOutsideRoot) {
			t.Errorf("ContentAt(%.40q) = %v, want ErrOutsideRoot", p, err)
		}
		if _, _, err := e.s.CurrentContent(p); !errors.Is(err, ErrOutsideRoot) {
			t.Errorf("CurrentContent(%.40q) = %v, want ErrOutsideRoot", p, err)
		}
		if _, err := e.s.CaptureUndo([]string{p}); err == nil {
			t.Errorf("CaptureUndo(%.40q) accepted", p)
		}
	}
	// A symlink at the end of the path is not followed, even inside the root.
	for _, p := range []string{"link.txt", "loop1"} {
		b, ok, err := e.s.CurrentContent(p)
		if !errors.Is(err, ErrNotRegular) || len(b) != 0 || !ok {
			t.Errorf("CurrentContent(%s) = %q %v %v, want ErrNotRegular", p, b, ok, err)
		}
	}
	if runtime.GOOS != "windows" {
		if err := mkfifo(e.abs("fifo"), 0o644); err == nil {
			if _, _, err := e.s.CurrentContent("fifo"); !errors.Is(err, ErrNotRegular) {
				t.Errorf("a FIFO must be refused without blocking: %v", err)
			}
		}
	}
	// Unicode names are ordinary names.
	e.write("dür/ñame ✓.txt", "ok")
	if b, ok, err := e.s.CurrentContent("dür/ñame ✓.txt"); err != nil || !ok || string(b) != "ok" {
		t.Fatalf("unicode path: %q %v %v", b, ok, err)
	}
}

func TestChangesPerCheckpoint(t *testing.T) {
	e := newEnv(t)
	e.write("mod.txt", "one\ntwo\nthree\n")
	e.write("del.txt", "bye\n")
	e.write("same.txt", "keep\n")
	cp1 := e.s.Begin("one")
	e.editAfter("a1", "mod.txt", "one\nTWO\nthree\nfour\n")
	e.editAfter("a2", "add.txt", "hello\nworld\n")
	e.remove("a2", "del.txt")
	e.editAfter("a1", "same.txt", "changed\n")
	e.editAfter("a3", "same.txt", "keep\n") // back as it began: not a change
	e.writeMode("bin.dat", "x", 0o644)
	e.editAfter("a3", "bin.dat", "\x00\x01\x02")
	cp2 := e.s.Begin("two")
	e.editAfter("a2", "mod.txt", "one\nTWO\n")

	ch, err := e.s.Changes(cp1)
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]Change{}
	var order []string
	for _, c := range ch {
		got[c.Path] = c
		order = append(order, c.Path)
	}
	if want := []string{"add.txt", "bin.dat", "del.txt", "mod.txt"}; !reflect.DeepEqual(order, want) {
		t.Fatalf("paths = %v, want %v", order, want)
	}
	check := func(path, status string, added, removed int, agents []string, binary bool) {
		t.Helper()
		c := got[path]
		if c.Status != status || c.Added != added || c.Removed != removed || !reflect.DeepEqual(c.Agents, agents) || c.Binary != binary {
			t.Errorf("%s = %+v, want %s +%d -%d %v binary=%v", path, c, status, added, removed, agents, binary)
		}
	}
	// mod.txt's change set ends where cp2 began, not at the live file.
	check("mod.txt", "modified", 2, 1, []string{"a1"}, false)
	check("add.txt", "added", 2, 0, []string{"a2"}, false)
	check("del.txt", "deleted", 0, 1, []string{"a2"}, false)
	check("bin.dat", "modified", 0, 0, []string{"a3"}, true)

	ch2, err := e.s.Changes(cp2)
	if err != nil || len(ch2) != 1 || ch2[0].Added != 0 || ch2[0].Removed != 2 {
		t.Fatalf("cp2 changes = %+v, %v", ch2, err)
	}
	list := e.s.List()
	if list[0].Added != 4 || list[0].Removed != 2 || list[1].Removed != 2 {
		t.Fatalf("List counts = %+v", list)
	}
	if _, err := e.s.Changes("nope"); !errors.Is(err, ErrUnknownCheckpoint) {
		t.Fatalf("unknown id: %v", err)
	}
}

func TestWriteJournal(t *testing.T) {
	e := newEnv(t)
	e.s.EnableJournal(0)
	e.write("f.txt", "a\n")
	cp1 := e.s.Begin("one")
	e.editAfter("w1", "f.txt", "a\nb\n")
	e.editAfter("w2", "f.txt", "a\nb\nc\n")
	cp2 := e.s.Begin("two")
	e.editAfter("w1", "f.txt", "a\nb\nc\nd\n")

	ws := e.s.Writes("f.txt")
	if len(ws) != 3 {
		t.Fatalf("writes = %+v", ws)
	}
	for i, w := range []struct{ cp, agent, content string }{{cp1, "w1", "a\nb\n"}, {cp1, "w2", "a\nb\nc\n"}, {cp2, "w1", "a\nb\nc\nd\n"}} {
		got := ws[i]
		b, err := e.blobs.Get(got.Blob)
		if got.Checkpoint != w.cp || got.Agent != w.agent || err != nil || string(b) != w.content || got.Gap {
			t.Errorf("write %d = %+v (%q, %v), want %+v", i, got, b, err, w)
		}
	}
	if a, cp, ok := e.s.LastWriter("f.txt"); !ok || a != "w1" || cp != cp2 {
		t.Fatalf("LastWriter = %q %q %v", a, cp, ok)
	}
	// Someone who does not record changes the file between two writes: the next
	// entry says so.
	if err := os.WriteFile(e.abs("f.txt"), []byte("a\nb\nc\nd\nshell\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	e.editAfter("w2", "f.txt", "a\nb\nc\nd\nshell\ne\n")
	ws = e.s.Writes("f.txt")
	if last := ws[len(ws)-1]; !last.Gap || last.Agent != "w2" {
		t.Fatalf("the write after a shell edit must be marked: %+v", last)
	}
	// The journal is in the manifest: a restarted process sees it.
	if again := e.reopen().Writes("f.txt"); !reflect.DeepEqual(stripTimes(again), stripTimes(ws)) {
		t.Fatalf("after reopen: %+v, want %+v", again, ws)
	}
	if e.s.Writes("../x") != nil || e.s.Writes("never.txt") != nil {
		t.Fatal("no journal for paths outside the root or never written")
	}
}

func stripTimes(ws []Write) []Write {
	out := append([]Write(nil), ws...)
	for i := range out {
		out[i].At = out[i].At.UTC().Round(0)
	}
	return out
}

func TestJournalSkipsLargeContentButKeepsTheWriter(t *testing.T) {
	e := newEnv(t)
	e.s.EnableJournal(0)
	e.s.Begin("p")
	big := strings.Repeat("x", maxJournalBytes+1)
	e.editAfter("w", "big.txt", big)
	ws := e.s.Writes("big.txt")
	if len(ws) != 1 || ws[0].Blob != "" || ws[0].Sum == "" || ws[0].Agent != "w" {
		t.Fatalf("writes = %+v", ws)
	}
}

func authors(a Authorship) []string {
	out := make([]string, len(a.Lines))
	for i, l := range a.Lines {
		out[i] = l.Agent
		if out[i] == "" {
			out[i] = "-"
		}
	}
	return out
}

func TestBlameExactAndInexact(t *testing.T) {
	e := newEnv(t)
	e.s.EnableJournal(0)
	e.write("f.go", "package f\n\nfunc A() {}\n")
	cp1 := e.s.Begin("one")
	e.editAfter("be-1", "f.go", "package f\n\nfunc A() {}\n\nfunc B() {}\n")
	e.editAfter("be-2", "f.go", "package f\n\nfunc A() { b() }\n\nfunc B() {}\n")
	cp2 := e.s.Begin("two")
	e.editAfter("ts-1", "f.go", "package f\n\nfunc A() { b() }\n\nfunc B() {}\nfunc C() {}\n")

	a, err := e.s.AuthorshipAt("", "f.go")
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"-", "-", "be-2", "be-1", "be-1", "ts-1"}; !a.Exact || !reflect.DeepEqual(authors(a), want) {
		t.Fatalf("live: exact=%v authors=%v, want %v", a.Exact, authors(a), want)
	}
	if a.Lines[5].Checkpoint != cp2 || a.Lines[2].Checkpoint != cp1 {
		t.Fatalf("checkpoints: %+v", a.Lines)
	}
	// As it was when cp2 began.
	b, err := e.s.AuthorshipAt(cp2, "f.go")
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"-", "-", "be-2", "be-1", "be-1"}; !b.Exact || !reflect.DeepEqual(authors(b), want) || !strings.HasSuffix(string(b.Content), "func B() {}\n") {
		t.Fatalf("at cp2: exact=%v authors=%v content=%q", b.Exact, authors(b), b.Content)
	}
	// A shell edit: its line has no author and the result is not exact.
	if err := os.WriteFile(e.abs("f.go"), []byte("package f\n\nfunc A() { b() }\n\nfunc B() {}\nfunc C() {}\n// shell\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	c, err := e.s.AuthorshipAt("", "f.go")
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"-", "-", "be-2", "be-1", "be-1", "ts-1", "-"}; c.Exact || !reflect.DeepEqual(authors(c), want) {
		t.Fatalf("after a shell edit: exact=%v authors=%v", c.Exact, authors(c))
	}
	// A file the session never touched: every line predates it, exactly.
	e.write("old.txt", "x\ny\n")
	d, err := e.s.AuthorshipAt("", "old.txt")
	if err != nil || !d.Exact || !reflect.DeepEqual(authors(d), []string{"-", "-"}) {
		t.Fatalf("untouched: %+v %v", d, err)
	}
	// Binary content has no lines.
	e.editAfter("x", "b.bin", "a\x00b")
	if bb, err := e.s.AuthorshipAt("", "b.bin"); err != nil || !bb.Binary || len(bb.Lines) != 0 {
		t.Fatalf("binary: %+v %v", bb, err)
	}
}

func TestBlameAcrossAGapBetweenCheckpoints(t *testing.T) {
	e := newEnv(t)
	e.s.EnableJournal(0)
	e.write("f.txt", "1\n")
	e.s.Begin("one")
	e.editAfter("a", "f.txt", "1\n2\n")
	// a person edits between the checkpoints
	if err := os.WriteFile(e.abs("f.txt"), []byte("1\n2\nhuman\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	e.s.Begin("two")
	e.editAfter("b", "f.txt", "1\n2\nhuman\n3\n")
	a, err := e.s.AuthorshipAt("", "f.txt")
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"-", "a", "-", "b"}; a.Exact || !reflect.DeepEqual(authors(a), want) {
		t.Fatalf("exact=%v authors=%v want %v", a.Exact, authors(a), want)
	}
}

// Property: for random edit sequences by random agents, an exact authorship has one
// author per line of the live file, and every line an agent's write introduced and
// nobody changed since is attributed to that agent.
func TestBlameProperty(t *testing.T) {
	rng := rand.New(rand.NewSource(7))
	for trial := range 30 {
		e := newEnv(t)
		e.s.EnableJournal(0)
		lines := []string{"base0", "base1", "base2"}
		e.write("f.txt", strings.Join(lines, "\n")+"\n")
		owner := []string{"-", "-", "-"}
		e.s.Begin("p")
		for step := range 12 {
			agent := fmt.Sprintf("ag%d", rng.Intn(3))
			if step%5 == 4 {
				e.s.Begin(fmt.Sprintf("p%d", step))
			}
			switch op := rng.Intn(3); {
			case op == 0 || len(lines) == 0: // insert a unique line
				at := rng.Intn(len(lines) + 1)
				l := fmt.Sprintf("t%d-s%d", trial, step)
				lines = append(lines[:at], append([]string{l}, lines[at:]...)...)
				owner = append(owner[:at], append([]string{agent}, owner[at:]...)...)
			case op == 1: // delete a line
				at := rng.Intn(len(lines))
				lines = append(lines[:at], lines[at+1:]...)
				owner = append(owner[:at], owner[at+1:]...)
			default: // replace a line
				at := rng.Intn(len(lines))
				lines[at] = fmt.Sprintf("r%d-s%d", trial, step)
				owner[at] = agent
			}
			content := ""
			if len(lines) > 0 {
				content = strings.Join(lines, "\n") + "\n"
			}
			e.editAfter(agent, "f.txt", content)
		}
		a, err := e.s.AuthorshipAt("", "f.txt")
		if err != nil {
			t.Fatal(err)
		}
		if !a.Exact || len(a.Lines) != len(lines) {
			t.Fatalf("trial %d: exact=%v lines=%d want %d", trial, a.Exact, len(a.Lines), len(lines))
		}
		if got := authors(a); !reflect.DeepEqual(got, owner) {
			t.Fatalf("trial %d: authors %v, want %v (lines %q)", trial, got, owner, lines)
		}
	}
}

func TestOnChange(t *testing.T) {
	e := newEnv(t)
	var mu sync.Mutex
	var got []string
	e.s.OnChange(func(i Info) {
		mu.Lock()
		got = append(got, fmt.Sprintf("%s:%d", i.ID, len(i.Files)))
		mu.Unlock()
	})
	e.write("a.txt", "x")
	cp := e.s.Begin("one")
	e.editAfter("w", "a.txt", "y")
	e.editAfter("w", "a.txt", "z") // same file again: the file set does not change
	e.editAfter("w", "b.txt", "new")
	mustRestore(t, e.s, cp, RestoreOpts{})
	mu.Lock()
	defer mu.Unlock()
	want := []string{"cp_0001:0", "cp_0001:1", "cp_0001:2", "cp_0001:0"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("notifications %v, want %v", got, want)
	}
}

func TestDiffLinesReverseAppliesToTheOriginal(t *testing.T) {
	rng := rand.New(rand.NewSource(3))
	words := []string{"alpha", "beta", "gamma", "delta", "eps", "", "zeta"}
	gen := func() string {
		var b strings.Builder
		for range rng.Intn(40) {
			b.WriteString(words[rng.Intn(len(words))] + "\n")
		}
		if rng.Intn(4) == 0 {
			b.WriteString("no newline")
		}
		return b.String()
	}
	for range 300 {
		a := gen()
		b := mutate(rng, a)
		d := DiffLines([]byte(a), []byte(b))
		if got := applyHunks(splitLines(a), d.Hunks, false); trimJoin(got) != trimJoin(splitLines(b)) {
			t.Fatalf("forward:\n%q\n%q\n%+v", a, b, d.Hunks)
		}
		if got := applyHunks(splitLines(b), d.Hunks, true); trimJoin(got) != trimJoin(splitLines(a)) {
			t.Fatalf("reverse:\n%q\n%q\n%+v", a, b, d.Hunks)
		}
		// Reverting each hunk on its own also lands on the original's lines there.
		for i := range d.Hunks {
			one := applyHunks(splitLines(b), d.Hunks[i:i+1], true)
			if len(one) != len(splitLines(b))-d.Hunks[i].NewLines+d.Hunks[i].OldLines {
				t.Fatalf("single hunk revert has the wrong length")
			}
		}
	}
}

func mutate(rng *rand.Rand, s string) string {
	l := splitLines(s)
	for range rng.Intn(6) {
		switch {
		case len(l) == 0 || rng.Intn(3) == 0:
			at := rng.Intn(len(l) + 1)
			l = append(l[:at], append([]string{fmt.Sprintf("new %d\n", rng.Intn(100))}, l[at:]...)...)
		case rng.Intn(2) == 0:
			at := rng.Intn(len(l))
			l = append(l[:at], l[at+1:]...)
		default:
			l[rng.Intn(len(l))] = fmt.Sprintf("changed %d\n", rng.Intn(100))
		}
	}
	return strings.Join(l, "")
}

func trimJoin(lines []string) string {
	var out []string
	for _, l := range lines {
		out = append(out, strings.TrimSuffix(l, "\n"))
	}
	return strings.Join(out, "\n")
}

// applyHunks applies hunks to lines (forward: old -> new; reverse: new -> old), using
// the line numbers of the side it starts from.
func applyHunks(lines []string, hunks []Hunk, reverse bool) []string {
	var out []string
	pos := 0
	for _, h := range hunks {
		start, count := h.OldStart, h.OldLines
		if reverse {
			start, count = h.NewStart, h.NewLines
		}
		begin := start - 1
		if count == 0 {
			begin = start
		}
		out = append(out, lines[pos:begin]...)
		for _, l := range h.Lines {
			keep := l.Op == ' ' || (!reverse && l.Op == '+') || (reverse && l.Op == '-')
			if keep {
				out = append(out, l.Text+"\n")
			}
		}
		pos = begin + count
	}
	return append(out, lines[pos:]...)
}
