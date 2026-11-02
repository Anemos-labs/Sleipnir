package main

import (
	"os"
	"path/filepath"
	"slices"
	"testing"
)

func TestServeDirs(t *testing.T) {
	home := t.TempDir()
	proj := filepath.Join(home, "proj")
	other := t.TempDir()
	// The start directory always exists; it must, for its symlinks (macOS's /var) to resolve as the roots' do.
	if err := os.Mkdir(proj, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name            string
		roots           []string
		cwd, here, home string
		wantRoots       []string
		wantDir         string
	}{
		{"nothing: home, started inside it", nil, "", proj, home, []string{home}, proj},
		{"nothing: home, started in /", nil, "", "/", home, []string{home}, ""},
		{"--cwd alone is the only root", nil, other, proj, home, []string{other}, other},
		{"--root, started inside", []string{home, other}, "", proj, home, []string{home, other}, proj},
		{"--root, started outside", []string{other}, "", proj, home, []string{other}, ""},
		{"--root and --cwd", []string{home}, proj, "/", home, []string{home}, proj},
		{"no home", nil, "", "/", "", nil, ""},
	} {
		roots, dir := serveDirs(tc.roots, tc.cwd, tc.here, tc.home)
		if !slices.Equal(roots, tc.wantRoots) || dir != tc.wantDir {
			t.Errorf("%s: roots %q dir %q; want %q %q", tc.name, roots, dir, tc.wantRoots, tc.wantDir)
		}
	}
}
