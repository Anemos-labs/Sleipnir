package main

import (
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"strings"
	"testing"

	"github.com/anemos-labs/sleipnir/internal/config"
	"github.com/anemos-labs/sleipnir/internal/harden"
)

// main starts with harden.Process(harden.MoveKeys()): the provider keys leave the environment, so no
// command the harness starts (git, a hook, a verifier, an MCP server) inherits them. That is only
// safe if every reader of a key goes through harden.Secret. This test runs the real code paths in a
// child process that has done exactly what main does, and checks that the key is gone from the
// environment and still found by everything that needs it.

const moveKeysHelperEnv = "SLEIPNIR_TEST_MOVEKEYS"

// The child of TestKeysLeaveTheEnvironmentButStillReachTheirReaders. It does nothing otherwise.
func TestMoveKeysHelper(t *testing.T) {
	if os.Getenv(moveKeysHelperEnv) != "1" {
		t.Skip("helper process of TestKeysLeaveTheEnvironmentButStillReachTheirReaders")
	}
	harden.Process(harden.MoveKeys())

	out := map[string]any{}
	out["environ_has_key"] = os.Getenv("HEIMDALL_API_KEY") != ""
	// A command started the way the harness starts git and hooks: nil Env, so it inherits.
	printed, _ := exec.Command("printenv", "HEIMDALL_API_KEY").Output()
	out["child_sees_key"] = strings.TrimSpace(string(printed)) != ""

	spec, key, err := providerFlags{home: t.TempDir()}.resolve()
	out["resolved_provider"] = spec.name
	out["resolved_key"] = key
	if err != nil {
		out["resolve_error"] = err.Error()
	}
	out["config_key"] = config.Provider{APIKeyEnv: "HEIMDALL_API_KEY"}.APIKey()
	out["custom_config_key"] = config.Provider{APIKeyEnv: "MYCO_SERVICE_TOKEN"}.APIKey()
	out["custom_environ_has_key"] = os.Getenv("MYCO_SERVICE_TOKEN") != ""

	b, _ := json.Marshal(out)
	os.Stdout.WriteString("\nMOVEKEYS-RESULT " + string(b) + "\n")
}

func TestKeysLeaveTheEnvironmentButStillReachTheirReaders(t *testing.T) {
	if _, err := exec.LookPath("printenv"); err != nil {
		t.Skip("printenv unavailable")
	}
	// Assembled, not written out: nothing here is a credential.
	key := "canary-" + "provider-" + "value"
	custom := "canary-" + "custom-" + "value"

	cmd := exec.Command(os.Args[0], "-test.run=^TestMoveKeysHelper$", "-test.v")
	cmd.Env = []string{
		"PATH=" + os.Getenv("PATH"), "HOME=" + t.TempDir(),
		moveKeysHelperEnv + "=1",
		"HEIMDALL_API_KEY=" + key,
		"HEIMDALL_BASE_URL=http://127.0.0.1:9/v1", // loopback: a key may go there over plain http
		"MYCO_SERVICE_TOKEN=" + custom,
	}
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil {
		t.Fatalf("helper: %v\n%s\n%s", err, stdout.String(), stderr.String())
	}
	_, res, ok := strings.Cut(stdout.String(), "MOVEKEYS-RESULT ")
	if !ok {
		t.Fatalf("no result from the helper:\n%s\n%s", stdout.String(), stderr.String())
	}
	var got map[string]any
	if err := json.Unmarshal([]byte(strings.TrimSpace(strings.SplitN(res, "\n", 2)[0])), &got); err != nil {
		t.Fatalf("result: %v\n%s", err, res)
	}
	if got["environ_has_key"] != false || got["child_sees_key"] != false {
		t.Errorf("the key is still in the environment (os.Getenv: %v, inherited by a child: %v)", got["environ_has_key"], got["child_sees_key"])
	}
	if got["resolve_error"] != nil || got["resolved_provider"] != "heimdall" || got["resolved_key"] != key {
		t.Errorf("provider resolution lost the key: provider=%v key-found=%v error=%v", got["resolved_provider"], got["resolved_key"] == key, got["resolve_error"])
	}
	if got["config_key"] != key {
		t.Errorf("config.Provider.APIKey did not find the moved key")
	}
	if got["custom_config_key"] != custom || got["custom_environ_has_key"] != false {
		t.Errorf("a key the configuration names must be read through Secret (and so moved too): found=%v, still in the environment=%v", got["custom_config_key"] == custom, got["custom_environ_has_key"])
	}
}

const storedKeysHelperEnv = "SLEIPNIR_TEST_STOREDKEYS"

// The child of TestAStoredKeyDoesNotReplaceTheKeyOfTheEnvironment: the first two statements of main, then
// what each key resolves to and where it came from.
func TestStoredKeysHelper(t *testing.T) {
	if os.Getenv(storedKeysHelperEnv) != "1" {
		t.Skip("helper process of TestAStoredKeyDoesNotReplaceTheKeyOfTheEnvironment")
	}
	harden.Process(harden.MoveKeys("HF_TOKEN"))
	if err := config.LoadStoredKeys(userHome()); err != nil {
		t.Fatal(err)
	}
	out := map[string]string{}
	for _, name := range []string{"HEIMDALL_API_KEY", "OPENROUTER_API_KEY", "OPENAI_API_KEY", "HF_TOKEN"} {
		out[name] = harden.Secret(name)
		out[name+"/source"] = harden.SourceOf(name).String()
		out[name+"/environ"] = os.Getenv(name)
	}
	b, _ := json.Marshal(out)
	os.Stdout.WriteString("\nSTOREDKEYS-RESULT " + string(b) + "\n")
}

// The order of main: the keys of the environment are moved into memory, then the stored keys are loaded. The
// environment wins over the file, though the variable is gone from os.Getenv by then; a stored key is used where
// the environment has none; and neither is left in the environment for a command to inherit.
func TestAStoredKeyDoesNotReplaceTheKeyOfTheEnvironment(t *testing.T) {
	home := t.TempDir()
	// Assembled, not written out: nothing here is a credential.
	fromEnv, fromFile := "canary-"+"environment-"+"value", "canary-"+"stored-"+"value"
	for name, v := range map[string]string{"HEIMDALL_API_KEY": fromFile, "OPENROUTER_API_KEY": fromFile, "HF_TOKEN": fromFile} {
		if err := config.SaveStoredKey(home, name, v); err != nil {
			t.Fatal(err)
		}
	}
	cmd := exec.Command(os.Args[0], "-test.run=^TestStoredKeysHelper$", "-test.v")
	cmd.Env = []string{
		"PATH=" + os.Getenv("PATH"), "HOME=" + home, "USERPROFILE=" + home,
		storedKeysHelperEnv + "=1",
		"HEIMDALL_API_KEY=" + fromEnv, // a key in both places
		"HF_TOKEN=" + fromEnv,         // named to MoveKeys, not ending in API_KEY
		// OPENROUTER_API_KEY is stored only; OPENAI_API_KEY is nowhere
	}
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil {
		t.Fatalf("helper: %v\n%s\n%s", err, stdout.String(), stderr.String())
	}
	_, res, ok := strings.Cut(stdout.String(), "STOREDKEYS-RESULT ")
	if !ok {
		t.Fatalf("no result from the helper:\n%s\n%s", stdout.String(), stderr.String())
	}
	var got map[string]string
	if err := json.Unmarshal([]byte(strings.TrimSpace(strings.SplitN(res, "\n", 2)[0])), &got); err != nil {
		t.Fatalf("result: %v\n%s", err, res)
	}
	for _, c := range []struct{ name, value, source string }{
		{"HEIMDALL_API_KEY", fromEnv, "environment"},
		{"HF_TOKEN", fromEnv, "environment"},
		{"OPENROUTER_API_KEY", fromFile, "stored"},
		{"OPENAI_API_KEY", "", "none"},
	} {
		if got[c.name] != c.value || got[c.name+"/source"] != c.source {
			t.Errorf("%s: the key in use is the %s one (want %s): value correct=%v", c.name, got[c.name+"/source"], c.source, got[c.name] == c.value)
		}
		if got[c.name+"/environ"] != "" {
			t.Errorf("%s is in the environment of the process", c.name)
		}
	}
}
