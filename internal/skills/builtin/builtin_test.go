package builtin

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/anemos-labs/sleipnir/internal/core"
	"github.com/anemos-labs/sleipnir/internal/skills"
)

// discover is a catalog of the built-in skills alone, from a fresh state directory.
func discover(t *testing.T, extraHome string) (*skills.Catalog, []skills.Warning) {
	t.Helper()
	e, err := Extra(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return skills.Discover(skills.Opts{Home: extraHome, Extra: []skills.Extra{e}})
}

func TestEverySkillLoadsCleanly(t *testing.T) {
	cat, warns := discover(t, t.TempDir())
	if len(warns) != 0 {
		t.Fatalf("warnings: %v", warns)
	}
	names := Names()
	if len(names) == 0 || cat.Len() != len(names) {
		t.Fatalf("catalog has %d skills, the tree %d (%v)", cat.Len(), len(names), names)
	}
	for _, n := range names {
		sk, ok := cat.Get(n)
		if !ok {
			t.Fatalf("%s: not in the catalog", n)
		}
		// The listing shortens every description once any exceeds the first rung; keep built-ins under it.
		if l := utf8.RuneCountInString(sk.Summary()); l > 240 {
			t.Errorf("%s: description is %d characters, more than 240", n, l)
		}
		if sk.Path != Label+"/"+n+"/"+skills.FileName {
			t.Errorf("%s: path %q", n, sk.Path)
		}
		l, err := cat.LoadForModel(n, "")
		if err != nil || strings.TrimSpace(l.Body) == "" {
			t.Errorf("%s: load: %v", n, err)
		}
	}
	listing := cat.Listing(skills.DefaultListingTokens, core.NewBytesEstimator())
	// Every request of every agent reads the listing: the built-ins leave most of its budget to the user's skills.
	if n := core.NewBytesEstimator().Tokens(listing); n > skills.DefaultListingTokens/2 {
		t.Errorf("the built-in skills take %d tokens of the %d-token listing; shorten their descriptions", n, skills.DefaultListingTokens)
	}
	for _, n := range names {
		if !strings.Contains(listing, n+": ") {
			t.Errorf("listing lacks %s with its description:\n%s", n, listing)
		}
	}
}

func TestMaterializeIsStableAndRepairsTampering(t *testing.T) {
	state := t.TempDir()
	dir, err := Materialize(state)
	if err != nil {
		t.Fatal(err)
	}
	if filepath.Base(dir) != Label {
		t.Fatalf("dir %q is not named %q", dir, Label)
	}
	again, err := Materialize(state)
	if err != nil || again != dir {
		t.Fatalf("second materialize: %q, %v; want %q", again, err, dir)
	}

	// A skill planted next to the built-ins, and a changed body, are both replaced on the next start.
	planted := filepath.Join(dir, "planted", skills.FileName)
	if err := os.MkdirAll(filepath.Dir(planted), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(planted, []byte("---\nname: planted\ndescription: x\n---\nx\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	first := filepath.Join(dir, filepath.FromSlash(paths()[0]))
	if err := os.WriteFile(first, []byte("changed"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := Materialize(state); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(planted); !os.IsNotExist(err) {
		t.Errorf("the planted skill survived: %v", err)
	}
	if !complete(dir) {
		t.Error("the tree was not restored")
	}
}

func TestUserSkillReplacesBuiltin(t *testing.T) {
	home := t.TempDir()
	name := Names()[0]
	p := filepath.Join(home, ".sleipnir", "skills", name, skills.FileName)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte("---\nname: "+name+"\ndescription: Mine.\n---\nmine\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	cat, _ := discover(t, home)
	sk, ok := cat.Get(name)
	if !ok || sk.Description != "Mine." || sk.Scope != skills.ScopeUser {
		t.Errorf("%s: %+v", name, sk)
	}
}

func TestDigestCoversContent(t *testing.T) {
	if d := Digest(); len(d) != 64 || d != Digest() {
		t.Errorf("digest %q", d)
	}
}
