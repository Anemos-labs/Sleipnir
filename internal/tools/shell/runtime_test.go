package shell

import (
	"context"
	"encoding/json"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/anemos-labs/sleipnir/internal/perm"
	"github.com/anemos-labs/sleipnir/internal/tools"
)

func TestRuntimeShellContextMatchesExecution(t *testing.T) {
	for _, tc := range []struct{ shell, label, command string }{
		{"bash", "bash", "printf runtime-shell-probe"},
		{"sh", "sh", "printf runtime-shell-probe"},
		{"pwsh", "PowerShell (pwsh)", "Write-Output runtime-shell-probe"},
		{"powershell", "Windows PowerShell (powershell)", "Write-Output runtime-shell-probe"},
		{"cmd", "cmd.exe", "echo runtime-shell-probe"},
	} {
		t.Run(tc.shell, func(t *testing.T) {
			if runtime.GOOS == "windows" && (tc.shell == "bash" || tc.shell == "sh") {
				t.Skip("Windows POSIX wrappers require a separate runtime environment")
			}
			path, err := exec.LookPath(tc.shell)
			if err != nil {
				t.Skipf("%s is not installed", tc.shell)
			}
			if runtime.GOOS == "windows" {
				path = strings.ToUpper(path) // Exercise an uppercase spelling of the installed executable.
			}
			m := NewManager(Options{Shell: path})
			defer m.Shutdown()
			text := m.RuntimeContext()
			if !strings.Contains(text, "Operating system: "+runtime.GOOS) || !strings.Contains(text, "Command shell: "+tc.label+".") || strings.Contains(text, path) {
				t.Fatalf("incorrect or machine-specific runtime context: %q", text)
			}
			if tc.shell == "powershell" && !strings.Contains(text, "&& and || operators are unavailable") {
				t.Fatal("Windows PowerShell needs accurate chaining guidance")
			}
			reg := tools.NewRegistry()
			Register(reg, m)
			tool, _ := reg.Get("bash")
			input, _ := json.Marshal(map[string]string{"command": tc.command})
			root := t.TempDir()
			ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
			defer cancel()
			res, err := tool.Run(ctx, &tools.Call{Input: input, Env: &tools.Env{Agent: "probe", Cwd: root, Root: root, Perm: perm.AllowAll{}}})
			if err != nil || res == nil || res.IsError || !strings.Contains(res.Text, "runtime-shell-probe") || !strings.Contains(res.Text, "[exit code 0]") {
				t.Fatalf("advertised shell did not execute its native command: result=%+v error=%v", res, err)
			}
			// Later PATH changes must not make the prompt contradict the cached
			// executable that the manager continues to use.
			t.Setenv("PATH", t.TempDir())
			if got := m.RuntimeContext(); got != text {
				t.Fatalf("runtime changed after initial detection: %q", got)
			}
		})
	}
}

func TestRuntimeShellContextUnavailableKeepsToolsStable(t *testing.T) {
	var baseline string
	for _, shell := range []string{"", "bash", "pwsh", "cmd", filepath.Join(t.TempDir(), "missing-shell")} {
		m := NewManager(Options{Shell: shell})
		defer m.Shutdown()
		text := m.RuntimeContext()
		reg := tools.NewRegistry()
		Register(reg, m)
		specs, err := reg.Specs()
		if err != nil {
			t.Fatal(err)
		}
		b, err := json.Marshal(specs)
		if err != nil {
			t.Fatal(err)
		}
		if baseline == "" {
			baseline = string(b)
		}
		if string(b) != baseline {
			t.Fatal("shell selection changed the tool definitions")
		}
		if filepath.IsAbs(shell) {
			if !strings.Contains(text, "Command shell: unavailable.") || strings.Contains(text, shell) {
				t.Fatalf("unavailable shell was misrepresented or leaked its path: %q", text)
			}
		}
	}
}
