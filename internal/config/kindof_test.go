package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestReportKindOfTellsTheUsersSettingsFromTheRepositorys(t *testing.T) {
	home, root := t.TempDir(), t.TempDir()
	write := func(path, body string) {
		t.Helper()
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	write(UserConfigPath(home), `{"mcp":{"mine":{"command":"a"}}}`)
	write(ProjectConfigPath(root), `{"mcp":{"theirs":{"command":"b"}}}`)
	write(LocalConfigPath(root), `{"mcp":{"local":{"command":"c"}}}`)
	_, rep, err := Load(LoadOpts{Cwd: root, Root: root, Home: home})
	if err != nil {
		t.Fatal(err)
	}
	for key, want := range map[string]string{"mcp.mine": "user", "mcp.theirs": "project", "mcp.local": "local", "mcp.absent": ""} {
		if got := rep.KindOf(key); got != want {
			t.Errorf("KindOf(%q) = %q, want %q", key, got, want)
		}
	}
	var none *Report
	if none.KindOf("mcp.mine") != "" {
		t.Error("a nil report knows nothing")
	}
}
