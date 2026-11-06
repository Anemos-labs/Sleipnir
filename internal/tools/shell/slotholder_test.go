package shell

import (
	"strings"
	"testing"
)

// A heavy command is named to the commands that wait for its slot by its agent and the words that name its program, and by no
// argument that may carry a secret: an assignment, a flag's value, a header, credentials in a URL.
func TestASlotHolderIsNamedWithoutItsArguments(t *testing.T) {
	for cmd, want := range map[string]string{
		"API_TOKEN=s3cret python -m pytest --token=abc tests/ -q\necho done": "be-2: python -m pytest",
		"go test ./...":           "be-2: go test",
		"npm run build -- --prod": "be-2: npm run build",
		`curl -H "Authorization: Bearer s3cret" https://x`: "be-2: curl",
		"pip install https://user:s3cret@host/pkg.tar.gz":  "be-2: pip install",
		"mvn -Dpassword s3cret package":                    "be-2: mvn",
	} {
		got := slotHolder("be-2", cmd)
		if got != want {
			t.Errorf("slotHolder(%q) = %q, want %q", cmd, got, want)
		}
		if strings.Contains(got, "s3cret") {
			t.Errorf("slotHolder(%q) shows the secret: %q", cmd, got)
		}
	}
	if long := slotHolder("", strings.Repeat("x", 100)); len([]rune(long)) != 60 || !strings.HasSuffix(long, "…") {
		t.Fatalf("a long command is named %q", long)
	}
}
