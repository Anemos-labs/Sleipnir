package sched

import (
	"os/exec"
	"slices"
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
