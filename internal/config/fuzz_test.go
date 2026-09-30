package config

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"unicode/utf8"
)

// FuzzParseJSONC: the parser must never panic or hang, and for any input that
// is strict JSON it must agree with encoding/json.
func FuzzParseJSONC(f *testing.F) {
	for _, seed := range []string{
		`{}`, `[]`, `{"a": 1}`, `{"a": [1, 2, {"b": null}], "c": "xé\n"}`,
		"// c\n{\"a\": 1, /* x */ \"b\": [1,],}", `{"a": 12345678901234567890, "b": -0.5e+3}`,
		`{"a": "😀"}`, `{"a" 1}`, `{"a": 'x'}`, `{`, `[1 2]`, `"\`, "\xef\xbb\xbf{}",
		`{"a": "\ud83d"}`, `{"a":{"a":{"a":{"a":1}}}}`, `{"a": 1, "a": 2}`, `/* unterminated`, `{"a": tru}`,
	} {
		f.Add([]byte(seed))
	}
	f.Fuzz(func(t *testing.T, data []byte) {
		n, _, err := parseJSONC("fuzz.json", data)
		if err == nil {
			if _, merr := json.Marshal(toValue(n)); merr != nil {
				t.Fatalf("accepted a document whose value cannot be encoded: %v", merr)
			}
		}
		if json.Valid(data) && utf8.Valid(data) && !bytes.HasPrefix(data, []byte{0xEF, 0xBB, 0xBF}) {
			if err != nil {
				t.Fatalf("valid JSON rejected: %v\n%q", err, data)
			}
			var want any
			dec := json.NewDecoder(bytes.NewReader(data))
			dec.UseNumber()
			if derr := dec.Decode(&want); derr != nil {
				t.Skip()
			}
			if got := toValue(n); !reflect.DeepEqual(got, want) {
				t.Fatalf("value differs from encoding/json:\n got %#v\nwant %#v\ninput %q", got, want, data)
			}
		}
	})
}

// FuzzLoadFile: a hostile or corrupt config file must produce an error or a
// valid configuration, never a panic.
func FuzzLoadFile(f *testing.F) {
	for _, seed := range []string{
		`{}`, `{"cache": {"prewarm": false}}`, `{"providers": {"a": {"dialect": "x"}}}`, `{"swarm": {"max_agents": "x"}}`,
		`{"hooks": {"a": null}, "mcp": {"b": {"c": [1,2]}}}`, `{"models": {"roles": {"a": "b/c"}}}`, `[]`, `null`, `"str"`,
		`{"permissions": {"allow": ["Bash(", "Read", ""]}}`, `{"$schema": 1, "zzz": {"a": [null]}}`,
	} {
		f.Add([]byte(seed))
	}
	f.Fuzz(func(t *testing.T, data []byte) {
		dir := t.TempDir()
		home := filepath.Join(dir, "home")
		root := filepath.Join(dir, "root")
		if err := os.MkdirAll(filepath.Join(home, ".sleipnir"), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.MkdirAll(root, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(UserConfigPath(home), data, 0o600); err != nil {
			t.Fatal(err)
		}
		cfg, rep, err := Load(LoadOpts{Home: home, Root: root, Environ: func() []string { return nil }})
		if rep == nil {
			t.Fatal("Load must always return a report")
		}
		if (cfg == nil) == (err == nil) {
			t.Fatalf("exactly one of config and error must be set: cfg=%v err=%v", cfg != nil, err)
		}
		if err == nil {
			if issues := cfg.Validate(); Errors(issues) != nil {
				t.Fatalf("Load accepted a configuration that fails Validate: %v", issues)
			}
			if _, merr := json.Marshal(cfg); merr != nil {
				t.Fatalf("loaded config cannot be marshalled: %v", merr)
			}
		}
	})
}
