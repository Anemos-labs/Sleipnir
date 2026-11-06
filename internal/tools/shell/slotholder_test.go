package shell

import (
	"strings"
	"testing"
)

// A heavy command is named to the commands that wait for its slot by its agent and its tool and verb, each from a fixed
// list, and by nothing the command wrote: no assignment, flag value, header, URL, path or script name.
func TestASlotHolderIsNamedWithoutItsArguments(t *testing.T) {
	for cmd, want := range map[string]string{
		"go test ./...":                                    "be-2: go test",
		"npm run build -- --prod":                          "be-2: npm run build",
		"npm run build s3cret":                             "be-2: npm run build",
		"npm run test-s3cret":                              "be-2: npm run",
		"python3 -m pytest -k s3cret":                      "be-2: python3 -m pytest",
		"python -m s3cret_tool":                            "be-2: python",
		"pytest python -m s3cret":                          "be-2: pytest",
		"pytest -m s3cret":                                 "be-2: pytest",
		"make s3cret":                                      "be-2: make",
		"mvn -Dpassword s3cret package":                    "be-2: mvn",
		"./scripts/test.sh s3cret":                         "be-2: a command",
		"/tmp/s3cret/test.sh":                              "be-2: a command",
		"s3cret-build --all":                               "be-2: a command",
		`curl -H "Authorization: Bearer s3cret" https://x`: "be-2: a command",
		"pip install https://user:s3cret@host/pkg.tar.gz":  "be-2: pip install",
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
}
