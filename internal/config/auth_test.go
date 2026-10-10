package config

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/anemos-labs/sleipnir/internal/harden"
)

// Stored keys reach harden.Provide at start: one the environment also names stays the environment's, one it does not is
// used from memory, and no stored key is put in the environment for a command to inherit.
func TestLoadStoredKeysKeepsTheKeyOfTheEnvironment(t *testing.T) {
	home := t.TempDir()
	const both, only = "CFGAUTH_T_BOTH_API_KEY", "CFGAUTH_T_ONLY_API_KEY"
	t.Setenv(both, "FROM-ENVIRONMENT")
	t.Setenv(only, "x")
	if err := os.Unsetenv(only); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { harden.Provide(both, ""); harden.Provide(only, "") })
	for name, v := range map[string]string{both: "FROM-STORED-FILE", only: " FROM-STORED-FILE \n"} {
		if err := SaveStoredKey(home, name, v); err != nil {
			t.Fatal(err)
		}
	}
	if err := LoadStoredKeys(home); err != nil {
		t.Fatal(err)
	}
	if got, src := harden.Secret(both), harden.SourceOf(both); got != "FROM-ENVIRONMENT" || src != harden.SourceEnvironment {
		t.Errorf("a key in both places: %q from %v, want the environment's", got, src)
	}
	if got, src := harden.Secret(only), harden.SourceOf(only); got != "FROM-STORED-FILE" || src != harden.SourceStored {
		t.Errorf("a key only in the file: %q from %v, want the stored one (trimmed)", got, src)
	}
	if v := os.Getenv(only); v != "" {
		t.Errorf("a stored key reached the environment: %q", v)
	}
}

func TestLoadStoredKeysWithoutAFileOrWithABadOne(t *testing.T) {
	home := t.TempDir()
	if err := LoadStoredKeys(home); err != nil {
		t.Errorf("no file holds no keys, and is no error: %v", err)
	}
	if err := os.MkdirAll(filepath.Dir(AuthPath(home)), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(AuthPath(home), []byte("[1, 2]"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := LoadStoredKeys(home); err == nil {
		t.Error("a file that is not an object of name to key should be reported")
	}
}
