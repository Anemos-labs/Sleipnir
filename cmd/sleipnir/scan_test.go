package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/anemos-labs/sleipnir/internal/scan"
)

func TestScanDiffFlagIsChecked(t *testing.T) {
	dir := t.TempDir()
	for _, c := range []struct {
		args []string
		want string
	}{
		{[]string{"--cwd", dir, "--start", "--diff", ""}, "--diff needs a BASE"},
		{[]string{"--cwd", dir, "--diff", ""}, "--diff goes with --start"},
		{[]string{"--cwd", dir, "--start", "--diff", "main", "sub"}, "leave DIR out"},
	} {
		if err := cmdScan(context.Background(), c.args); err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("scan %q: %v, want %q", c.args, err, c.want)
		}
	}
}

// The scan a rerun came from is read from the project's scan.json, so the listing prints it without terminal controls.
func TestScanListShowsPreviousSafely(t *testing.T) {
	root := t.TempDir()
	st := scan.NewStore(root)
	r, err := st.Create(strings.Repeat("ab", 20), "survey", "", scan.Inventory{Files: []scan.File{{Path: "a.go", Lang: "go", Lines: 1}}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.Update(r.Slug, func(r *scan.Run) error { r.Previous = "old\x1b]0;owned\x07\nscan"; return nil }); err != nil {
		t.Fatal(err)
	}
	var out strings.Builder
	if err := scanListCLI(&out, root, false); err != nil {
		t.Fatal(err)
	}
	if s := out.String(); strings.ContainsAny(s, "\x1b\x07") || !strings.Contains(s, "old") {
		t.Fatalf("listing:\n%q", s)
	}
}

func TestScanPluginOffIsSaid(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	dir := t.TempDir()
	if msg := scanPluginOff(dir); !strings.Contains(msg, "sleipnir plugins enable scan") {
		t.Fatalf("the scan plugin is off by default and nothing said so: %q", msg)
	}
	if err := os.MkdirAll(filepath.Join(home, ".sleipnir"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, ".sleipnir", "config.json"), []byte(`{"plugins":{"enabled":{"scan":true}}}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if msg := scanPluginOff(dir); msg != "" {
		t.Fatalf("the scan plugin is on and a warning was given: %q", msg)
	}
}
