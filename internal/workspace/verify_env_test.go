package workspace

import (
	"strings"
	"testing"
)

func TestVerifyEnvScrubsKeyComponents(t *testing.T) {
	for _, name := range []string{"KEY", "KEY_CONTENT", "SIGNING_KEY", "SIGNING_KEY_CONTENT", "signing-key-content", "prefix_Key_suffix", "MY_API_KEY", "APIKEY", "PRIVATE_KEY", "ACCESS_TOKEN", "PASSWORD", "CREDENTIAL", "SSH_AUTH_SOCK"} {
		t.Run(name, func(t *testing.T) {
			for _, variable := range verifyEnv([]string{name + "=fixture-only"}, VerifyRequest{}) {
				if strings.HasPrefix(variable, name+"=") {
					t.Fatalf("credential-like variable %s reached the verifier", name)
				}
			}
		})
	}
	for _, name := range []string{"PATH", "KEYBOARD_LAYOUT", "MONKEY_HOME", "HARMLESS_SETTING"} {
		t.Run(name, func(t *testing.T) {
			want := name + "=keep-me"
			for _, variable := range verifyEnv([]string{want}, VerifyRequest{}) {
				if variable == want {
					return
				}
			}
			t.Fatalf("ordinary variable %s was removed", name)
		})
	}
}
