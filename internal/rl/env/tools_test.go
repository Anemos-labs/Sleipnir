package env

import (
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"testing"

	"github.com/anemos-labs/sleipnir/internal/rl"
)

// fakeTool puts an executable called name into dir, the way this platform names executables.
func fakeTool(t *testing.T, dir, name string) {
	t.Helper()
	if runtime.GOOS == "windows" {
		name += ".exe"
	}
	if err := os.WriteFile(filepath.Join(dir, name), []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
}

func TestMissingToolsLooksAtTheCommandsPath(t *testing.T) {
	bin := t.TempDir()
	fakeTool(t, bin, "ruby")
	m := newManager(t, func(o *WorkspaceOptions) { o.SetEnv["PATH"] = bin })
	task := rl.Task{Requires: []string{"ruby", "cargo", "node"}}
	if got := m.MissingTools(task); !reflect.DeepEqual(got, []string{"cargo", "node"}) {
		t.Errorf("MissingTools = %q, want cargo and node, in the task's order", got)
	}
	if got := m.MissingTools(rl.Task{}); got != nil {
		t.Errorf("a task without requirements misses nothing, got %q", got)
	}
}

func TestOnPath(t *testing.T) {
	bin := t.TempDir()
	if err := os.WriteFile(filepath.Join(bin, "ruby"), []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(bin, "data"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(bin, "node.CMD"), []byte("@echo off\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(bin, "go"), 0o755); err != nil {
		t.Fatal(err)
	}
	list := "" + string(os.PathListSeparator) + bin
	if runtime.GOOS != "windows" {
		if !onPath("ruby", list, "", "linux") {
			t.Error("an executable file is found")
		}
		if onPath("data", list, "", "linux") {
			t.Error("a file without an execute bit is not a tool")
		}
	}
	if onPath("go", list, "", runtime.GOOS) {
		t.Error("a directory is not a tool")
	}
	// Windows: a bare name is tried with each PATHEXT extension, in any case.
	if !onPath("node", list, ".COM;.EXE;.BAT;.CMD", "windows") {
		t.Error("node.CMD is node on Windows")
	}
	if onPath("ruby", list, ".COM;.EXE", "windows") {
		t.Error("an extensionless file is not run by name on Windows")
	}
}
