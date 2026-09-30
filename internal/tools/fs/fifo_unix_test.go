//go:build unix

package fs

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

// A FIFO with no writer makes os.Open wait for ever and nothing can cancel it. The tools stat a path
// before opening it, but the model has a shell: it can turn a path into a FIFO in between, or leave
// one where a tool does not expect it (a .gitignore). Every open of a user-controlled path must
// therefore fail at once instead of waiting.

func mkfifo(t *testing.T, path string) {
	t.Helper()
	if err := syscall.Mkfifo(path, 0o644); err != nil {
		t.Skipf("mkfifo unavailable: %v", err)
	}
}

// within fails the test if fn does not return in time (a blocked open cannot be interrupted, so the
// goroutine is abandoned).
func within(t *testing.T, d time.Duration, what string, fn func()) {
	t.Helper()
	done := make(chan struct{})
	go func() {
		defer close(done)
		fn()
	}()
	select {
	case <-done:
	case <-time.After(d):
		t.Fatalf("%s did not return within %v: an open is waiting on a FIFO", what, d)
	}
}

func TestOpenRegularNeverWaitsAndOnlyOpensRegularFiles(t *testing.T) {
	dir := realTemp(t)
	writeFile(t, filepath.Join(dir, "file.txt"), "hello")
	if err := os.Symlink("file.txt", filepath.Join(dir, "link")); err != nil {
		t.Skip("symlinks unavailable")
	}
	mkfifo(t, filepath.Join(dir, "pipe"))
	if err := os.Symlink("pipe", filepath.Join(dir, "linkpipe")); err != nil {
		t.Skip("symlinks unavailable")
	}
	if err := os.Mkdir(filepath.Join(dir, "sub"), 0o755); err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		name string
		path string
		want error // nil: opens; otherwise errors.Is
	}{
		{"regular file", "file.txt", nil},
		{"symlink to a regular file", "link", nil},
		{"FIFO", "pipe", errNotRegular},
		{"symlink to a FIFO", "linkpipe", errNotRegular},
		{"directory", "sub", errNotRegular},
		{"missing", "nope", os.ErrNotExist},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			within(t, 3*time.Second, "openRegular", func() {
				f, fi, err := openRegular(filepath.Join(dir, tt.path))
				if f != nil {
					defer f.Close()
				}
				switch {
				case tt.want == nil && (err != nil || f == nil || !fi.Mode().IsRegular()):
					t.Errorf("openRegular = %v, %v", fi, err)
				case tt.want != nil && !errors.Is(err, tt.want):
					t.Errorf("err = %v, want %v", err, tt.want)
				case tt.want != nil && f != nil:
					t.Errorf("returned an open file for %s", tt.path)
				}
			})
		})
	}
	// A device is not a regular file either.
	if _, _, err := openRegular("/dev/null"); !errors.Is(err, errNotRegular) {
		t.Errorf("/dev/null: %v", err)
	}
	// What was read is what the file holds.
	f, _, err := openRegular(filepath.Join(dir, "link"))
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	buf := make([]byte, 16)
	if n, _ := f.Read(buf); string(buf[:n]) != "hello" {
		t.Errorf("read %q", buf[:n])
	}
}

// The walk that reads .gitignore files must not stall on one that is a FIFO.
func TestLoadIgnoreFileSkipsAFIFO(t *testing.T) {
	dir := realTemp(t)
	mkfifo(t, filepath.Join(dir, ".gitignore"))
	within(t, 3*time.Second, "loadIgnoreFile", func() {
		if f := loadIgnoreFile(filepath.Join(dir, ".gitignore"), 0); f != nil {
			t.Errorf("a FIFO produced ignore rules: %+v", f)
		}
	})
}

// End to end: glob, ls and grep over a tree with a FIFO as .gitignore (and one named like a source
// file) finish, and still find the real files. (Ripgrep, which the grep tool prefers, waits on a
// FIFO named .gitignore itself until the tool's timeout kills it and the Go engine takes over, so
// that case is only run against the Go engine; a FIFO named like a source file is harmless to both.)
func TestSearchToolsSurviveFIFOsInTheTree(t *testing.T) {
	env := testEnv(t)
	writeFile(t, filepath.Join(env.Cwd, "src", "a.go"), "package a // needle\n")
	mkfifo(t, filepath.Join(env.Cwd, ".gitignore"))
	mkfifo(t, filepath.Join(env.Cwd, "src", ".gitignore"))
	mkfifo(t, filepath.Join(env.Cwd, "src", "b.go"))

	within(t, 10*time.Second, "glob over a tree with FIFOs", func() {
		out := mustOK(t, run(t, Glob{}, env, map[string]any{"pattern": "**/*.go"}))
		contains(t, out, "a.go")
		notContains(t, out, "b.go")
	})
	within(t, 10*time.Second, "grep (Go engine) over a tree with FIFOs", func() {
		out := mustOK(t, run(t, Grep{DisableRipgrep: true}, env, map[string]any{"pattern": "needle", "output_mode": "content"}))
		contains(t, out, "a.go")
	})
	within(t, 10*time.Second, "ls over a tree with FIFOs", func() {
		mustOK(t, run(t, LS{}, env, map[string]any{"path": "src"}))
	})

	env2 := testEnv(t)
	writeFile(t, filepath.Join(env2.Cwd, "src", "a.go"), "package a // needle\n")
	mkfifo(t, filepath.Join(env2.Cwd, "src", "b.go"))
	within(t, 10*time.Second, "grep (default engine) over a FIFO named like a source file", func() {
		out := mustOK(t, run(t, Grep{}, env2, map[string]any{"pattern": "needle", "output_mode": "content"}))
		contains(t, out, "a.go")
	})
}

// The in-place fallback of a write no longer truncates before it knows what it is opening, and it
// does not wait on a FIFO.
func TestWriteInPlace(t *testing.T) {
	dir := realTemp(t)
	p := filepath.Join(dir, "f.txt")
	writeFile(t, p, "a much longer old content")
	if err := writeInPlace(p, []byte("new")); err != nil {
		t.Fatal(err)
	}
	if got := readFileT(t, p); got != "new" {
		t.Fatalf("content = %q, want the old bytes gone", got)
	}
	if err := writeInPlace(p, nil); err != nil || readFileT(t, p) != "" {
		t.Fatalf("writing nothing must empty the file: %v %q", err, readFileT(t, p))
	}

	mkfifo(t, filepath.Join(dir, "pipe"))
	within(t, 3*time.Second, "writeInPlace on a FIFO", func() {
		if err := writeInPlace(filepath.Join(dir, "pipe"), []byte("x")); err == nil {
			t.Error("writing into a FIFO through the file tool succeeded")
		}
	})
	if err := writeInPlace(filepath.Join(dir, "missing"), []byte("x")); err == nil || !errors.Is(err, os.ErrNotExist) {
		t.Errorf("missing file: %v", err)
	}
	if err := writeInPlace("/dev/null", []byte("x")); err == nil || !strings.Contains(err.Error(), "not a regular file") {
		t.Errorf("/dev/null: %v", err)
	}
}

// Reading a file that turns out not to be regular when it is opened says so instead of hanging
// (the stat in front of it is the fast path; this is the descriptor-level check behind it).
func TestReadFileReportsWhatItFoundAtOpen(t *testing.T) {
	env := testEnv(t)
	k := begin(nil, nil, "read")
	k.env = env
	dir := env.Cwd
	mkfifo(t, filepath.Join(dir, "pipe"))
	within(t, 3*time.Second, "readFile", func() {
		if _, _, res := k.readFile(filepath.Join(dir, "pipe"), "pipe", 1<<20); res == nil || !res.IsError || !strings.Contains(res.Text, "not a regular file") {
			t.Errorf("readFile(FIFO) = %+v", res)
		}
	})
}
