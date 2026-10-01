package config

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"

	"github.com/anemos-labs/sleipnir/internal/harden"
)

// Stored keys. `sleipnir login` keeps a provider's API key in ~/.sleipnir/auth.json (mode 0600), the way opencode and pi keep theirs, so that
// a person pastes it once. The file maps the key's environment variable (HEIMDALL_API_KEY) to its value, so that a provider defined in
// the configuration with its own api_key_env is found the same way. At start the keys are handed to harden.Provide: they are held in the
// harness's memory, an environment variable of the same name wins, and no command a tool runs can see them.

// AuthPath is the file of stored keys under home.
func AuthPath(home string) string { return filepath.Join(home, ".sleipnir", "auth.json") }

// StoredKeys reads the file. A missing file holds none.
func StoredKeys(home string) (map[string]string, error) {
	b, err := os.ReadFile(AuthPath(home))
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var m map[string]string
	if err := json.Unmarshal(b, &m); err != nil {
		return nil, errors.New(AuthPath(home) + " is not a JSON object of name to key: fix or delete it")
	}
	return m, nil
}

// LoadStoredKeys hands every stored key to harden.Provide. A file that cannot be read is reported and skipped, never fatal.
func LoadStoredKeys(home string) error {
	m, err := StoredKeys(home)
	for name, v := range m {
		harden.Provide(name, strings.TrimSpace(v))
	}
	return err
}

// SaveStoredKey adds or replaces one key, atomically, readable by the user only; an empty value removes it.
func SaveStoredKey(home, name, value string) error {
	m, err := StoredKeys(home)
	if err != nil {
		return err
	}
	if m == nil {
		m = map[string]string{}
	}
	if value == "" {
		delete(m, name)
	} else {
		m[name] = strings.TrimSpace(value)
	}
	b, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return err
	}
	path := AuthPath(home)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".auth-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.Write(append(b, '\n')); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Chmod(tmp.Name(), 0o600); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}
