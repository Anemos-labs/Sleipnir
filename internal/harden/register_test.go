package harden

import (
	"os"
	"testing"
)

// A variable a provider names as its key leaves the environment when it is registered, whatever it is called and whatever
// it holds; an essential variable stays, and so does everything without moving mode.
func TestARegisteredProviderKeyLeavesTheEnvironment(t *testing.T) {
	resetVault(t)
	t.Setenv("HARDEN_T_GATEWAY_SECRET", "plain words")
	Register("HARDEN_T_GATEWAY_SECRET")
	if os.Getenv("HARDEN_T_GATEWAY_SECRET") == "" {
		t.Fatal("registered without moving mode, the variable left")
	}
	applyForTest(MoveKeys())
	Register("HARDEN_T_GATEWAY_SECRET", "PATH", "")
	if _, set := os.LookupEnv("HARDEN_T_GATEWAY_SECRET"); set {
		t.Error("the registered key is still in the environment")
	}
	if Secret("HARDEN_T_GATEWAY_SECRET") != "plain words" {
		t.Error("the registered key cannot be read back")
	}
	if os.Getenv("PATH") == "" {
		t.Error("PATH was taken")
	}
}
