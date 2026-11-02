package catalog

import (
	"testing"

	"github.com/anemos-labs/sleipnir/internal/config"
)

// The default of a role is kept under the key a start reads, in the user's file, and read back with the layer that set it.
func TestSaveDefaultWritesTheKeyAStartReads(t *testing.T) {
	home := t.TempDir()
	t.Setenv("SLEIPNIR_MODEL", "")
	for role, want := range map[string]string{"": "models.default", "default": "models.default", "manager": "models.default", "judge": "models.roles.judge"} {
		if got := DefaultKey(role); got != want {
			t.Errorf("DefaultKey(%q) = %q, want %q", role, got, want)
		}
	}
	for _, bad := range []string{"Judge", "a b", "../x", "-x"} {
		if ValidRole(bad) {
			t.Errorf("%q is taken for a role", bad)
		}
	}
	if _, err := SaveDefault("", "", "acme/a"); err == nil {
		t.Error("no home: a relative .sleipnir/config.json would be written in the working directory")
	}
	if _, err := SaveDefault(home, "", "nonsense"); err == nil {
		t.Error("a reference without a provider is kept")
	}
	path, err := SaveDefault(home, "", "acme/a")
	if err != nil || path != config.UserConfigPath(home) {
		t.Fatalf("%q %v", path, err)
	}
	if _, err := SaveDefault(home, "backend", "acme/b"); err != nil {
		t.Fatal(err)
	}
	cfg, rep, err := config.Load(config.LoadOpts{Home: home, Cwd: home, Environ: func() []string { return nil }})
	if err != nil {
		t.Fatal(err)
	}
	if d := DefaultOf(cfg, rep, ""); d.Ref != "acme/a" || d.Kind != "user" || d.Source != path {
		t.Errorf("default: %+v", d)
	}
	if d := DefaultOf(cfg, rep, "backend"); d.Ref != "acme/b" || d.Key != "models.roles.backend" {
		t.Errorf("backend: %+v", d)
	}
	if d := DefaultOf(cfg, rep, "frontend"); d.Ref != "" || d.Kind != "" {
		t.Errorf("a role with nothing saved: %+v", d)
	}
}
