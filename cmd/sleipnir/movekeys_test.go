package main

import (
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"strings"
	"testing"

	"github.com/reee344/sleipnir/internal/config"
	"github.com/reee344/sleipnir/internal/harden"
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
