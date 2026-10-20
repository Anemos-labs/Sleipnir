package memory

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Files lists every place Load reads, in Load's order, whether a file is there or not; what Load finds is among them.
func TestFilesListsThePlacesLoadReadsInItsOrder(t *testing.T) {
	w := newWorld(t)
	tree(t, w.root, map[string]string{"AGENTS.md": "root", "sub/CLAUDE.md": "sub", "SLEIPNIR.local.md": "local"})
	tree(t, w.home, map[string]string{".sleipnir/SLEIPNIR.md": "user"})
	cwd := filepath.Join(w.root, "sub")
	fs, err := Files(Opts{Root: w.root, Cwd: cwd, Home: w.home})
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, f := range fs {
		got = append(got, f.Path+"|"+f.Scope)
	}
	want := "~/.sleipnir/SLEIPNIR.md|user ~/.sleipnir/MEMORY.md|user AGENTS.md|project CLAUDE.md|project SLEIPNIR.md|project .sleipnir/SLEIPNIR.md|project " +
		"sub/AGENTS.md|dir sub/CLAUDE.md|dir sub/SLEIPNIR.md|dir sub/.sleipnir/SLEIPNIR.md|dir " +
		"SLEIPNIR.local.md|local .sleipnir/SLEIPNIR.local.md|local .sleipnir/MEMORY.local.md|local sub/SLEIPNIR.local.md|local sub/.sleipnir/SLEIPNIR.local.md|local"
	if strings.Join(got, " ") != want {
		t.Fatalf("Files =\n%s\nwant\n%s", strings.Join(got, " "), want)
	}
	places := map[string]File{}
	for _, f := range fs {
		places[f.Path] = f
	}
	srcs, err := Load(Opts{Root: w.root, Cwd: cwd, Home: w.home})
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range srcs {
		f, ok := places[s.Path]
		if !ok {
			t.Errorf("Load read %s, which Files does not list", s.Path)
			continue
		}
		if b, err := os.ReadFile(f.Abs()); err != nil || string(b) != s.Text {
			t.Errorf("%s: Abs %s holds %q (%v), Load read %q", s.Path, f.Abs(), b, err, s.Text)
		}
	}
	if f := places["~/.sleipnir/SLEIPNIR.md"]; !f.User || f.Rel != "SLEIPNIR.md" {
		t.Errorf("the user's file = %+v", f)
	}
	if f := places["sub/CLAUDE.md"]; f.User || f.Rel != "sub/CLAUDE.md" {
		t.Errorf("a folder's file = %+v", f)
	}
}

// With NoProject only the user's files are listed and loaded, whatever Root and Cwd say.
func TestNoProjectReadsOnlyTheUsersFiles(t *testing.T) {
	w := newWorld(t)
	tree(t, w.root, map[string]string{"AGENTS.md": "root"})
	tree(t, w.home, map[string]string{".sleipnir/SLEIPNIR.md": "user"})
	fs, err := Files(Opts{Root: w.root, Home: w.home, NoNotes: true, NoProject: true})
	if err != nil || len(fs) != 1 || fs[0].Path != "~/.sleipnir/SLEIPNIR.md" {
		t.Fatalf("Files = %+v, %v", fs, err)
	}
	srcs, err := Load(Opts{Root: w.root, Home: w.home, NoProject: true})
	if err != nil || len(srcs) != 1 || srcs[0].Text != "user" {
		t.Fatalf("Load = %+v, %v", srcs, err)
	}
}
