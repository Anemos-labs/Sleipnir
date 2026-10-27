package plugin

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// A Windows launcher keeps "%" in its target literal (doubled, delayed expansion off), and refuses a path a batch word cannot
// hold.
func TestTheWindowsLauncherKeepsThePathLiteral(t *testing.T) {
	state := t.TempDir()
	target := `C:\tools\%PATH%\!x!\bin\app.exe`
	if err := link(state, "windows", Need{Commands: []string{"app"}}, target); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(filepath.Join(NeedBin(state), "app.cmd"))
	if err != nil {
		t.Fatal(err)
	}
	want := "@echo off\r\nsetlocal DisableDelayedExpansion\r\n\"C:\\tools\\%%PATH%%\\!x!\\bin\\app.exe\" %*\r\n"
	if string(b) != want {
		t.Fatalf("launcher:\n%q\nwant:\n%q", b, want)
	}
	if err := link(state, "windows", Need{Commands: []string{"bad"}}, "C:\\a\"&calc&\"\\b.exe"); err == nil || !strings.Contains(err.Error(), "quote") {
		t.Fatalf("a quote in the path: %v", err)
	}
}
