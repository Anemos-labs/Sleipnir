package sched

import (
	"os/exec"
	"slices"
	"strings"
	"testing"
)

func TestDeniedKeysReachTheChildAndStopThere(t *testing.T) {
	cmd := exec.Command("true")
	DenyKeys(cmd)
	if !slices.Contains(cmd.Env, NoStoredKeysEnv+"=1") {
		t.Fatalf("the child's environment does not deny keys: %v", cmd.Env)
	}
	t.Setenv(NoStoredKeysEnv, "1")
	if !StoredKeysDenied() {
		t.Fatal("a denied child loads stored keys")
	}
	if StoredKeysDenied() {
		t.Fatal("the denial is inherited by what the child starts")
	}
}
// A child denied keys gets none in its environment either, whatever the parent failed to move out of its own.
func TestDenyKeysDropsCredentialLookingVariables(t *testing.T) {
	cmd := exec.Command("true")
	cmd.Env = []string{"PATH=/bin", "MY_SERVICE_TOKEN=abc", "HOME=/h"}
	DenyKeys(cmd)
	got := strings.Join(cmd.Env, " ")
	if strings.Contains(got, "MY_SERVICE_TOKEN") || !strings.Contains(got, "PATH=/bin") || !strings.Contains(got, NoStoredKeysEnv+"=1") {
		t.Fatalf("env: %q", got)
	}
}
