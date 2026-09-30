package fs

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"

	"github.com/reee344/sleipnir/internal/perm"
	"github.com/reee344/sleipnir/internal/tools"
)

func TestWriteCreatesFileAndParents(t *testing.T) {
	env := testEnv(t)
	res := run(t, Write{}, env, map[string]any{"path": "a/b/c/new.txt", "content": "hello\nworld\n"})
	text := mustOK(t, res)
	contains(t, text, "Created a/b/c/new.txt", "2 lines", "12 bytes")
	if got := readFileT(t, filepath.Join(env.Cwd, "a/b/c/new.txt")); got != "hello\nworld\n" {
		t.Errorf("content = %q", got)
	}
	// The writer has, by definition, seen what it wrote: no re-read needed.
	mustOK(t, run(t, Edit{}, env, map[string]any{"path": "a/b/c/new.txt", "old_string": "world", "new_string": "there"}))
}

func TestWriteDefaultMode(t *testing.T) {
	env := testEnv(t)
	mustOK(t, run(t, Write{}, env, map[string]any{"path": "f.txt", "content": "x"}))
	// A new file is created 0644 subject to the umask; compare with a file the
	// OS creates the same way.
	ref := filepath.Join(env.Cwd, "ref")
	f, err := os.OpenFile(ref, os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	f.Close()
	want, _ := os.Stat(ref)
	got, _ := os.Stat(filepath.Join(env.Cwd, "f.txt"))
	if got.Mode() != want.Mode() {
		t.Errorf("new file mode %v, want %v", got.Mode(), want.Mode())
	}
}

func TestWriteEmptyAndRequiredContent(t *testing.T) {
	env := testEnv(t)
	mustOK(t, run(t, Write{}, env, map[string]any{"path": "empty.txt", "content": ""}))
	if fi, err := os.Stat(filepath.Join(env.Cwd, "empty.txt")); err != nil || fi.Size() != 0 {
		t.Errorf("empty file not created: %v", err)
	}
	contains(t, mustErr(t, run(t, Write{}, env, map[string]any{"path": "x.txt"})), "content is required")
	contains(t, mustErr(t, run(t, Write{}, env, `{"path":"x.txt","content":5}`)), `argument "content" must be a string`)
	if exists(filepath.Join(env.Cwd, "x.txt")) {
		t.Errorf("a rejected call must not create the file")
	}
}

func TestWriteOverwriteRequiresRead(t *testing.T) {
	env := testEnv(t)
	p := filepath.Join(env.Cwd, "f.txt")
	writeFile(t, p, "original\n")

	text := mustErr(t, run(t, Write{}, env, map[string]any{"path": "f.txt", "content": "new\n"}))
	contains(t, text, "f.txt has not been read by you yet")
	if readFileT(t, p) != "original\n" {
		t.Fatalf("file modified despite the error")
	}

	mustOK(t, run(t, Read{}, env, map[string]any{"path": "f.txt"}))
	text = mustOK(t, run(t, Write{}, env, map[string]any{"path": "f.txt", "content": "new\n"}))
	contains(t, text, "Overwrote f.txt", "was 9")
	if readFileT(t, p) != "new\n" {
		t.Errorf("content = %q", readFileT(t, p))
	}
	// And the overwrite itself counts as a read: a second overwrite works.
	mustOK(t, run(t, Write{}, env, map[string]any{"path": "f.txt", "content": "newer\n"}))
}

func TestWriteStalenessBetweenAgents(t *testing.T) {
	a := testEnv(t)
	b := agentEnv(a, "b")
	p := filepath.Join(a.Cwd, "shared.txt")
	writeFile(t, p, "v1\n")

	mustOK(t, run(t, Read{}, a, map[string]any{"path": "shared.txt"}))
	mustOK(t, run(t, Read{}, b, map[string]any{"path": "shared.txt"}))
	mustOK(t, run(t, Write{}, b, map[string]any{"path": "shared.txt", "content": "v2 by b\n"}))

	text := mustErr(t, run(t, Write{}, a, map[string]any{"path": "shared.txt", "content": "v2 by a\n"}))
	contains(t, text, "shared.txt changed since you last read it", "modified by agent b", "read it again")
	if readFileT(t, p) != "v2 by b\n" {
		t.Errorf("the loser must not overwrite: %q", readFileT(t, p))
	}

	// After re-reading, a may write.
	mustOK(t, run(t, Read{}, a, map[string]any{"path": "shared.txt"}))
	mustOK(t, run(t, Write{}, a, map[string]any{"path": "shared.txt", "content": "v3 by a\n"}))
}

func TestWriteDetectsExternalChange(t *testing.T) {
	env := testEnv(t)
	p := filepath.Join(env.Cwd, "f.txt")
	writeFile(t, p, "v1\n")
	mustOK(t, run(t, Read{}, env, map[string]any{"path": "f.txt"}))
	writeFile(t, p, "changed behind our back\n")
	contains(t, mustErr(t, run(t, Write{}, env, map[string]any{"path": "f.txt", "content": "mine\n"})), "another process")
}

func TestWritePreservesModeAndStyle(t *testing.T) {
	tests := []struct {
		name     string
		existing string
		mode     os.FileMode
		content  string
		want     string
		note     string
	}{
		{"exec bit kept", "old\n", 0o755, "new\n", "new\n", ""},
		{"private mode kept", "old\n", 0o600, "new\n", "new\n", ""},
		{"crlf kept", "a\r\nb\r\n", 0o644, "x\ny\nz\n", "x\r\ny\r\nz\r\n", "kept CRLF"},
		{"bom kept", "\xef\xbb\xbfold\n", 0o644, "new\n", "\xef\xbb\xbfnew\n", "kept the UTF-8 BOM"},
		{"bom and crlf kept", "\xef\xbb\xbfa\r\nb\r\n", 0o644, "x\ny\n", "\xef\xbb\xbfx\r\ny\r\n", "BOM"},
		{"explicit crlf not doubled", "a\r\nb\r\n", 0o644, "x\r\ny\r\n", "x\r\ny\r\n", ""},
		{"lf file stays lf", "a\nb\n", 0o644, "x\ny\n", "x\ny\n", ""},
		{"mostly lf stays lf", "a\nb\nc\r\n", 0o644, "x\ny\n", "x\ny\n", ""},
		{"content has bom already", "\xef\xbb\xbfa\n", 0o644, "\xef\xbb\xbfb\n", "\xef\xbb\xbfb\n", ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			env := testEnv(t)
			p := filepath.Join(env.Cwd, "f.txt")
			writeFile(t, p, tc.existing)
			if err := os.Chmod(p, tc.mode); err != nil {
				t.Fatal(err)
			}
			mustOK(t, run(t, Read{}, env, map[string]any{"path": "f.txt"}))
			text := mustOK(t, run(t, Write{}, env, map[string]any{"path": "f.txt", "content": tc.content}))
			if got := readFileT(t, p); got != tc.want {
				t.Errorf("content = %q, want %q", got, tc.want)
			}
			if tc.note != "" {
				contains(t, text, tc.note)
			}
			fi, _ := os.Stat(p)
			if fi.Mode().Perm() != tc.mode {
				t.Errorf("mode = %v, want %v", fi.Mode().Perm(), tc.mode)
			}
		})
	}
}

func TestWriteNoChange(t *testing.T) {
	env := testEnv(t)
	p := filepath.Join(env.Cwd, "f.txt")
	writeFile(t, p, "same\n")
	h := &hooks{}
	withHooks(env, h)
	mustOK(t, run(t, Read{}, env, map[string]any{"path": "f.txt"}))
	before, _ := os.Stat(p)
	text := mustOK(t, run(t, Write{}, env, map[string]any{"path": "f.txt", "content": "same\n"}))
	contains(t, text, "No change")
	after, _ := os.Stat(p)
	if !after.ModTime().Equal(before.ModTime()) {
		t.Errorf("an identical write must not touch the file")
	}
	for _, l := range h.Log() {
		if strings.HasPrefix(l, "guard.") || strings.HasPrefix(l, "snap:") {
			t.Errorf("guard/snapshot must not run for a no-op: %v", h.Log())
		}
	}
}

func TestWriteGuardOrder(t *testing.T) {
	env := testEnv(t)
	h := &hooks{}
	withHooks(env, h)
	writeFile(t, filepath.Join(env.Cwd, "old.txt"), "old\n")
	mustOK(t, run(t, Read{}, env, map[string]any{"path": "old.txt"}))

	mustOK(t, run(t, Write{}, env, map[string]any{"path": "old.txt", "content": "new\n"}))
	want := []string{"perm:read", "perm:write", "guard.before:old.txt", "snap:old.txt", "guard.after:old.txt"}
	if got := h.Log(); !reflect.DeepEqual(got, want) {
		t.Errorf("order = %v, want %v", got, want)
	}

	// A file that is created (no prior state) goes through the same sequence.
	h.log = nil
	mustOK(t, run(t, Write{}, env, map[string]any{"path": "fresh.txt", "content": "x"}))
	want = []string{"perm:write", "guard.before:fresh.txt", "snap:fresh.txt", "guard.after:fresh.txt"}
	if got := h.Log(); !reflect.DeepEqual(got, want) {
		t.Errorf("order = %v, want %v", got, want)
	}
}

func TestWritePermissionRequest(t *testing.T) {
	env := testEnv(t)
	h := &hooks{}
	withHooks(env, h)
	mustOK(t, run(t, Write{}, env, map[string]any{"path": "sub/n.txt", "content": "x"}))
	r := h.Requests()[0]
	if r.Tool != "write" || !r.Writes || len(r.Paths) != 1 || r.Paths[0] != filepath.Join(env.Cwd, "sub/n.txt") || r.Risk < perm.RiskMedium {
		t.Errorf("bad request: %+v", r)
	}
}

func TestWriteVetoesLeaveNoTrace(t *testing.T) {
	tests := []struct {
		name  string
		setup func(h *hooks)
		want  string
	}{
		{"permission", func(h *hooks) { h.denyPerm = func(perm.Request) string { return "read-only mode" } }, "permission denied (write f.txt): read-only mode"},
		{"guard verbatim", func(h *hooks) { h.denyGuard = func(string) error { return errors.New("f.txt is leased to agent b") } }, "f.txt is leased to agent b"},
		{"snapshot", func(h *hooks) { h.failSnap = func(string) error { return errors.New("disk full") } }, "cannot checkpoint f.txt"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			env := testEnv(t)
			h := &hooks{}
			tc.setup(h)
			withHooks(env, h)
			text := mustErr(t, run(t, Write{}, env, map[string]any{"path": "f.txt", "content": "x"}))
			if !strings.Contains(text, tc.want) {
				t.Errorf("got %q, want it to contain %q", text, tc.want)
			}
			if exists(filepath.Join(env.Cwd, "f.txt")) {
				t.Errorf("file was created despite the veto")
			}
			if entries, _ := os.ReadDir(env.Cwd); len(entries) != 0 {
				t.Errorf("leftover files: %v", entries)
			}
			for _, l := range h.Log() {
				if strings.HasPrefix(l, "guard.after") {
					t.Errorf("AfterWrite must not run after a veto")
				}
			}
		})
	}
}

func TestWriteStalenessRejectedBeforeGuardAndSnapshot(t *testing.T) {
	env := testEnv(t)
	h := &hooks{}
	withHooks(env, h)
	writeFile(t, filepath.Join(env.Cwd, "f.txt"), "x\n")
	mustErr(t, run(t, Write{}, env, map[string]any{"path": "f.txt", "content": "y"}))
	for _, l := range h.Log() {
		if strings.HasPrefix(l, "guard.") || strings.HasPrefix(l, "snap:") {
			t.Errorf("no checkpoint should be taken for a write that will be refused: %v", h.Log())
		}
	}
}

func TestWriteLeavesNoTempFiles(t *testing.T) {
	env := testEnv(t)
	for i := 0; i < 20; i++ {
		mustOK(t, run(t, Write{}, env, map[string]any{"path": "d/f.txt", "content": strings.Repeat("x", i*100)}))
		mustOK(t, run(t, Read{}, env, map[string]any{"path": "d/f.txt"}))
	}
	entries, _ := os.ReadDir(filepath.Join(env.Cwd, "d"))
	if len(entries) != 1 {
		t.Errorf("expected only f.txt, found %v", entries)
	}
}

func TestWriteErrors(t *testing.T) {
	env := testEnv(t)
	writeFile(t, filepath.Join(env.Cwd, "file"), "x")
	if err := os.Mkdir(filepath.Join(env.Cwd, "dir"), 0o755); err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name  string
		input any
		want  string
	}{
		{"directory", map[string]any{"path": "dir", "content": "x"}, "is a directory"},
		{"parent is a file", map[string]any{"path": "file/child.txt", "content": "x"}, "not a directory"},
		{"empty path", map[string]any{"path": "", "content": "x"}, "path is required"},
		{"nul path", map[string]any{"path": "a\x00b", "content": "x"}, "NUL"},
		{"long path", map[string]any{"path": strings.Repeat("d/", 20000) + "f", "content": "x"}, "too long"},
		{"very long name", map[string]any{"path": strings.Repeat("n", 300), "content": "x"}, "file name too long"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			contains(t, mustErr(t, run(t, Write{}, env, tc.input)), tc.want)
		})
	}
}

func TestWriteThroughSymlinks(t *testing.T) {
	env := testEnv(t)
	real := filepath.Join(env.Cwd, "real.txt")
	writeFile(t, real, "orig\n")
	link := filepath.Join(env.Cwd, "link.txt")
	if err := os.Symlink("real.txt", link); err != nil {
		t.Skip("symlinks unsupported")
	}
	mustOK(t, run(t, Read{}, env, map[string]any{"path": "link.txt"}))
	mustOK(t, run(t, Write{}, env, map[string]any{"path": "link.txt", "content": "via link\n"}))
	if fi, _ := os.Lstat(link); fi.Mode()&os.ModeSymlink == 0 {
		t.Errorf("the symlink itself must survive a write")
	}
	if readFileT(t, real) != "via link\n" {
		t.Errorf("target = %q", readFileT(t, real))
	}
	// The link and its target are one file for staleness purposes.
	other := agentEnv(env, "b")
	mustOK(t, run(t, Read{}, other, map[string]any{"path": "real.txt"}))
	mustOK(t, run(t, Write{}, other, map[string]any{"path": "real.txt", "content": "via target\n"}))
	contains(t, mustErr(t, run(t, Write{}, env, map[string]any{"path": "link.txt", "content": "stale\n"})), "modified by agent b")

	// A dangling link is written through: the target is created.
	if err := os.Symlink("created_later.txt", filepath.Join(env.Cwd, "dangling")); err != nil {
		t.Fatal(err)
	}
	mustOK(t, run(t, Write{}, env, map[string]any{"path": "dangling", "content": "hi"}))
	if readFileT(t, filepath.Join(env.Cwd, "created_later.txt")) != "hi" {
		t.Errorf("dangling symlink target not created")
	}
	if fi, _ := os.Lstat(filepath.Join(env.Cwd, "dangling")); fi.Mode()&os.ModeSymlink == 0 {
		t.Errorf("dangling symlink replaced by a regular file")
	}
}

func TestWriteHugeContent(t *testing.T) {
	if testing.Short() {
		t.Skip("large input; skipped in -short mode")
	}
	env := testEnv(t)
	big := strings.Repeat("0123456789abcdef\n", 400_000) // ~6.8MB
	mustOK(t, run(t, Write{}, env, map[string]any{"path": "big.txt", "content": big}))
	if readFileT(t, filepath.Join(env.Cwd, "big.txt")) != big {
		t.Errorf("large content corrupted")
	}
}

func TestWriteUnicodeAndBinarySafeContent(t *testing.T) {
	env := testEnv(t)
	content := "héllo 日本語 🎉\x00nul inside\x01\ttab\n"
	mustOK(t, run(t, Write{}, env, map[string]any{"path": "u.txt", "content": content}))
	if got := readFileT(t, filepath.Join(env.Cwd, "u.txt")); got != content {
		t.Errorf("content changed: %q", got)
	}
}

func TestWriteRefusesFilesTooBigToCheck(t *testing.T) {
	env := testEnv(t)
	p := filepath.Join(env.Cwd, "huge.bin")
	f, _ := os.Create(p)
	if err := f.Truncate(maxFileBytes + 1); err != nil {
		t.Skip("no sparse files")
	}
	f.Close()
	contains(t, mustErr(t, run(t, Write{}, env, map[string]any{"path": "huge.bin", "content": "x"})), "larger than")
}

type racer struct {
	env *tools.Env
	res *tools.Result
}

func TestWriteSerialisesConcurrentWritersOfOneFile(t *testing.T) {
	// N agents all read version 0 and then race to overwrite it. Exactly one may
	// win; every loser must be told the file changed, and the file must hold the
	// winner's content, never a mix.
	base := testEnv(t)
	p := filepath.Join(base.Cwd, "race.txt")
	writeFile(t, p, "v0\n")
	const n = 16
	racers := make([]*racer, n)
	for i := range racers {
		racers[i] = &racer{env: agentEnv(base, "agent"+string(rune('A'+i)))}
		mustOK(t, run(t, Read{}, racers[i].env, map[string]any{"path": "race.txt"}))
	}
	var wg sync.WaitGroup
	start := make(chan struct{})
	for i := range racers {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			racers[i].res = run(t, Write{}, racers[i].env, map[string]any{
				"path": "race.txt", "content": strings.Repeat(string(rune('A'+i)), 1000) + "\n"})
		}(i)
	}
	close(start)
	wg.Wait()

	winners, winner := 0, -1
	for i, r := range racers {
		if !r.res.IsError {
			winners++
			winner = i
			continue
		}
		contains(t, r.res.Text, "changed since you last read it", "modified by agent agent")
	}
	if winners != 1 {
		t.Fatalf("expected exactly one winner, got %d", winners)
	}
	want := strings.Repeat(string(rune('A'+winner)), 1000) + "\n"
	if got := readFileT(t, p); got != want {
		t.Errorf("file does not hold the winner's content")
	}
}
