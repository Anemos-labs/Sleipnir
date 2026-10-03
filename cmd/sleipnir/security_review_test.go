package main

// Security review repro for docs/SECURITY.md (S45), now a regression
// test, with the cases the fix creates.

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/anemos-labs/sleipnir/internal/config"
)

// S45 (fixed): an environment variable named <PROVIDER>_BASE_URL used to redirect a built-in
// provider silently, and the provider's real API key was sent to whatever it named; plain
// http:// was accepted, so the Bearer token travelled in clear text. (Same for --base-url with
// --provider openai.) resolve now refuses to send a key to a host the environment chose unless
// the provider is known to use it or the user's own configuration lists it, and never over
// plain http to another machine unless the user allowed that.
func TestSec_S45_BaseURLOverrideCannotSendTheKeyToAnyHost(t *testing.T) {
	t.Setenv("OPENAI_API_KEY", "sk-real-key")
	t.Setenv("OPENAI_BASE_URL", "http://collector.attacker.example/v1") // e.g. from a repo's .envrc
	spec, key, err := providerFlags{provider: "openai", home: t.TempDir()}.resolve()
	t.Logf("resolved: baseURL=%s key-present=%v err=%v", spec.baseURL, key != "", err)
	if err == nil && key != "" && strings.HasPrefix(spec.baseURL, "http://") {
		t.Errorf("S45: provider key would be sent in clear text to %s", spec.baseURL)
	}
	// The refusal is an error, never a silent fallback, and it says how to lift it.
	if err == nil {
		t.Fatal("S45: the override was accepted")
	}
	for _, want := range []string{"collector.attacker.example", "OPENAI_API_KEY", "OPENAI_BASE_URL", "allow_hosts", "~/.sleipnir/config.json"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the refusal lacks %q:\n%s", want, err)
		}
	}
	if key != "" {
		t.Errorf("the key was returned together with a refusal")
	}
}

// userConfig writes the user's own configuration file under a private home.
func userConfig(t *testing.T, json string) (home string) {
	t.Helper()
	home = t.TempDir()
	path := config.UserConfigPath(home)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(json), 0o600); err != nil {
		t.Fatal(err)
	}
	return home
}

func TestResolveKeyDestination(t *testing.T) {
	type env map[string]string
	for _, tc := range []struct {
		name   string
		flags  providerFlags
		env    env
		config string
		want   string   // resolved base URL when allowed
		bad    []string // substrings of the refusal; nil means allowed
	}{
		{"the built-in default", providerFlags{provider: "openai"}, env{"OPENAI_API_KEY": "k"}, "", "https://api.openai.com/v1", nil},
		{"auto-detected from the key", providerFlags{}, env{"OPENROUTER_API_KEY": "k"}, "", "https://openrouter.ai/api/v1", nil},

		// The environment override.
		{"env: same host, other path", providerFlags{provider: "openai"}, env{"OPENAI_API_KEY": "k", "OPENAI_BASE_URL": "https://api.openai.com/v2"}, "", "https://api.openai.com/v2", nil},
		{"env: https to a stranger", providerFlags{provider: "openai"}, env{"OPENAI_API_KEY": "k", "OPENAI_BASE_URL": "https://collector.attacker.example/v1"}, "", "",
			[]string{"collector.attacker.example", "not a host this provider is known to use", "api.openai.com", `"allow_hosts":["collector.attacker.example"]`, "unset OPENAI_BASE_URL"}},
		{"env: plain http to a stranger", providerFlags{provider: "openai"}, env{"OPENAI_API_KEY": "k", "OPENAI_BASE_URL": "http://collector.attacker.example/v1"}, "", "",
			[]string{"collector.attacker.example"}},
		{"env: the real host over plain http", providerFlags{provider: "openai"}, env{"OPENAI_API_KEY": "k", "OPENAI_BASE_URL": "http://api.openai.com/v1"}, "", "",
			[]string{"plain http to api.openai.com", "allow_insecure_http"}},
		{"env: a local server", providerFlags{provider: "openai"}, env{"OPENAI_API_KEY": "k", "OPENAI_BASE_URL": "http://localhost:8000/v1"}, "", "http://localhost:8000/v1", nil},
		{"env: 127.0.0.1", providerFlags{provider: "openai"}, env{"OPENAI_API_KEY": "k", "OPENAI_BASE_URL": "http://127.0.0.1:11434/v1"}, "", "http://127.0.0.1:11434/v1", nil},
		{"env: not a URL", providerFlags{provider: "openai"}, env{"OPENAI_API_KEY": "k", "OPENAI_BASE_URL": "gateway.example.com"}, "", "", []string{"not an absolute http(s) URL", "OPENAI_BASE_URL"}},
		{"env: blank is unset", providerFlags{provider: "openai"}, env{"OPENAI_API_KEY": "k", "OPENAI_BASE_URL": "   "}, "", "https://api.openai.com/v1", nil},
		{"env: no key, nothing to protect", providerFlags{provider: "openai"}, env{"OPENAI_BASE_URL": "http://collector.attacker.example/v1"}, "", "http://collector.attacker.example/v1", nil},

		// What only the user can say.
		{"user config lists the host", providerFlags{provider: "openai"}, env{"OPENAI_API_KEY": "k", "OPENAI_BASE_URL": "https://proxy.corp.example/v1"},
			`{"providers":{"openai":{"allow_hosts":["proxy.corp.example"]}}}`, "https://proxy.corp.example/v1", nil},
		{"user config lists another provider's host", providerFlags{provider: "openai"}, env{"OPENAI_API_KEY": "k", "OPENAI_BASE_URL": "https://proxy.corp.example/v1"},
			`{"providers":{"openrouter":{"allow_hosts":["proxy.corp.example"]}}}`, "", []string{"proxy.corp.example"}},
		{"a listed host over plain http still needs its own permission", providerFlags{provider: "openai"}, env{"OPENAI_API_KEY": "k", "OPENAI_BASE_URL": "http://proxy.corp.example/v1"},
			`{"providers":{"openai":{"allow_hosts":["proxy.corp.example"]}}}`, "", []string{"plain http", "allow_insecure_http"}},
		{"a listed host over plain http, permitted", providerFlags{provider: "openai"}, env{"OPENAI_API_KEY": "k", "OPENAI_BASE_URL": "http://proxy.corp.example/v1"},
			`{"providers":{"openai":{"allow_hosts":["proxy.corp.example"],"allow_insecure_http":true}}}`, "http://proxy.corp.example/v1", nil},

		// A command-line flag is the user's own word.
		{"flag: https anywhere", providerFlags{provider: "openai", baseURL: "https://my-proxy.example/v1"}, env{"OPENAI_API_KEY": "k"}, "", "https://my-proxy.example/v1", nil},
		{"flag: wins over the environment", providerFlags{provider: "openai", baseURL: "https://my-proxy.example/v1"}, env{"OPENAI_API_KEY": "k", "OPENAI_BASE_URL": "http://collector.attacker.example/v1"}, "", "https://my-proxy.example/v1", nil},
		{"flag: plain http to another machine", providerFlags{provider: "openai", baseURL: "http://gpu-box.lan:8000/v1"}, env{"OPENAI_API_KEY": "k"}, "", "",
			[]string{"plain http to gpu-box.lan:8000", "allow_insecure_http", `"openai"`}},
		{"flag: plain http to another machine, permitted", providerFlags{provider: "openai", baseURL: "http://gpu-box.lan:8000/v1"}, env{"OPENAI_API_KEY": "k"},
			`{"providers":{"openai":{"allow_insecure_http":true}}}`, "http://gpu-box.lan:8000/v1", nil},
		{"flag: loopback over http", providerFlags{provider: "openai", baseURL: "http://127.0.0.1:8000/v1"}, env{"OPENAI_API_KEY": "k"}, "", "http://127.0.0.1:8000/v1", nil},

		// A custom endpoint has nothing it is known to use.
		{"custom: flag with a key over https", providerFlags{baseURL: "https://llm.example/v1", keyEnv: "MY_KEY"}, env{"MY_KEY": "k"}, "", "https://llm.example/v1", nil},
		{"custom: env-only URL with a key", providerFlags{provider: "custom", keyEnv: "MY_KEY"}, env{"MY_KEY": "k", "CUSTOM_BASE_URL": "https://llm.example/v1"}, "", "",
			[]string{"llm.example", "no default host", `"custom"`}},
		{"custom: env-only URL without a key", providerFlags{provider: "custom"}, env{"CUSTOM_BASE_URL": "http://llm.lan:8000/v1"}, "", "http://llm.lan:8000/v1", nil},
		{"custom: env-only URL listed by the user", providerFlags{provider: "custom", keyEnv: "MY_KEY"}, env{"MY_KEY": "k", "CUSTOM_BASE_URL": "https://llm.example/v1"},
			`{"providers":{"custom":{"allow_hosts":["llm.example"]}}}`, "https://llm.example/v1", nil},
		{"custom: nothing to reach", providerFlags{provider: "custom"}, env{}, "", "", []string{"needs --base-url"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			for _, k := range []string{"HEIMDALL_API_KEY", "OPENROUTER_API_KEY", "OPENAI_API_KEY", "OPENAI_BASE_URL", "CUSTOM_BASE_URL", "MY_KEY", "HOME"} {
				t.Setenv(k, "")
			}
			for k, v := range tc.env {
				t.Setenv(k, v)
			}
			pf := tc.flags
			pf.home = userConfig(t, "{}")
			if tc.config != "" {
				pf.home = userConfig(t, tc.config)
			}
			spec, key, err := pf.resolve()
			if tc.bad == nil {
				if err != nil {
					t.Fatalf("refused: %v", err)
				}
				if spec.baseURL != tc.want {
					t.Fatalf("baseURL = %q, want %q", spec.baseURL, tc.want)
				}
				if tc.env["OPENAI_API_KEY"] != "" && spec.name == "openai" && key != tc.env["OPENAI_API_KEY"] {
					t.Fatalf("key = %q", key)
				}
				return
			}
			if err == nil {
				t.Fatalf("allowed: %+v", spec)
			}
			if key != "" {
				t.Error("a key came back with the refusal")
			}
			for _, want := range tc.bad {
				if !strings.Contains(err.Error(), want) {
					t.Errorf("the refusal lacks %q:\n%s", want, err)
				}
			}
		})
	}
}

// A repository cannot allow itself anything: its config file's allow_hosts and
// allow_insecure_http are ignored (config.UserOnlyPaths), so the environment override
// it plants (a .envrc) stays refused.
func TestAProjectConfigCannotAllowItsOwnBaseURL(t *testing.T) {
	repo := t.TempDir()
	if err := os.MkdirAll(filepath.Join(repo, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	cfgDir := filepath.Join(repo, ".sleipnir")
	if err := os.MkdirAll(cfgDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(cfgDir, "config.json"),
		[]byte(`{"providers":{"openai":{"allow_hosts":["collector.attacker.example"],"allow_insecure_http":true}}}`), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Chdir(repo)
	t.Setenv("OPENAI_API_KEY", "k")
	t.Setenv("OPENAI_BASE_URL", "https://collector.attacker.example/v1")
	_, _, err := providerFlags{provider: "openai", home: t.TempDir()}.resolve()
	if err == nil || !strings.Contains(err.Error(), "collector.attacker.example") {
		t.Fatalf("a repository's own config must not lift the refusal: %v", err)
	}
}

func TestAMalformedUserConfigAllowsNothing(t *testing.T) {
	t.Setenv("OPENAI_API_KEY", "k")
	t.Setenv("OPENAI_BASE_URL", "https://proxy.corp.example/v1")
	home := userConfig(t, `{"providers": {"openai": {"allow_hosts": ["proxy.corp.example"]`) // truncated
	if _, _, err := (providerFlags{provider: "openai", home: home}).resolve(); err == nil {
		t.Fatal("an unreadable configuration must not allow an override")
	}
}

func TestAllowInsecureReachesTheClient(t *testing.T) {
	t.Setenv("OPENAI_API_KEY", "k")
	pf := providerFlags{provider: "openai", baseURL: "http://gpu-box.lan:8000/v1", home: userConfig(t, `{"providers":{"openai":{"allow_insecure_http":true}}}`)}
	spec, key, err := pf.resolve()
	if err != nil || !spec.allowInsecure {
		t.Fatalf("%v %+v", err, spec)
	}
	if c := newClient(spec, key, nil); c == nil {
		t.Fatal("no client")
	}
}
