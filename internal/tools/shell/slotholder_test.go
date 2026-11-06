package shell

import (
	"strings"
	"testing"
)

// A heavy command is named to the commands that wait for its slot by its agent, its program and a well-known subcommand,
// and by no argument that may carry a secret: an assignment, a flag's value, a header, credentials in a URL, a word passed
// to a script.
func TestASlotHolderIsNamedWithoutItsArguments(t *testing.T) {
	for cmd, want := range map[string]string{
		"go test ./...":                                    "be-2: go test",
		"npm run build -- --prod":                          "be-2: npm run build",
		"npm run build s3cret":                             "be-2: npm run build",
		"python3 -m pytest -k s3cret":                      "be-2: python3 -m pytest",
		"pytest python -m s3cret":                          "be-2: pytest",
		"pytest -m s3cret":                                 "be-2: pytest",
		"make s3cret":                                      "be-2: make",
		"./scripts/test.sh s3cret":                         "be-2: ./scripts/test.sh",
		"mvn -Dpassword s3cret package":                    "be-2: mvn",
		"pip install https://user:s3cret@host/pkg.tar.gz":  "be-2: pip install",
		`curl -H "Authorization: Bearer s3cret" https://x`: "be-2: curl",
		`API_TOKEN="first s3cret last" pytest`:             "be-2: a command",
		"API_TOKEN='a s3cret' go test ./...":               "be-2: a command",
		"API_TOKEN=s3cret python -m pytest --token=abc tests/ -q\necho done": "be-2: python -m pytest",
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
