package executil

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestCmdPreservesScriptQuotingAndOperators(t *testing.T) {
	t.Setenv("SLEIPNIR_QUOTING_VALUE", "environment value")
	for _, tc := range []struct {
		name, script, want string
		code               int
	}{
		{"quoted metacharacters", `echo "a&b | c"`, `"a&b | c"`, 0},
		{"pipeline", `echo "left | right" | findstr /L /C:"left | right"`, `"left | right"`, 0},
		{"redirection", `echo data>"result with spaces.txt" && type "result with spaces.txt"`, "data", 0},
		{"environment", `echo "%SLEIPNIR_QUOTING_VALUE%"`, `"environment value"`, 0},
		{"nonzero exit", `echo failure & exit /b 7`, "failure", 7},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			cmd := exec.CommandContext(ctx, "cmd", "/C", tc.script)
			cmd.Dir = t.TempDir()
			ConfigureShell(cmd)
			out, err := cmd.CombinedOutput()
			code := 0
			if err != nil {
				var exit *exec.ExitError
				if !errors.As(err, &exit) {
					t.Fatal(err)
				}
				code = exit.ExitCode()
			}
			if code != tc.code || strings.TrimSpace(string(out)) != tc.want {
				t.Fatalf("code=%d output=%q, want code=%d output=%q", code, out, tc.code, tc.want)
			}
		})
	}
}

func TestCmdAndDirectExecutablePreserveArguments(t *testing.T) {
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(exe)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "argument fixture.exe")
	if err := os.WriteFile(path, data, 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("SLEIPNIR_ARGUMENT_FIXTURE", "1")
	want := []string{"space value", "a&b", `C:\directory with spaces\file.txt`}
	for _, shell := range []bool{true, false} {
		t.Run(map[bool]string{true: "cmd", false: "direct"}[shell], func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			args := append([]string{"-test.run=^TestCmdArgumentProcessHelper$", "--"}, want...)
			cmd := exec.CommandContext(ctx, path, args...)
			if shell {
				script := `"` + path + `" -test.run=^TestCmdArgumentProcessHelper$ -- "space value" "a&b" "C:\directory with spaces\file.txt"`
				cmd = exec.CommandContext(ctx, "cmd", "/C", script)
			}
			ConfigureShell(cmd)
			out, err := cmd.CombinedOutput()
			if err != nil {
				t.Fatalf("command: %v; output=%q", err, out)
			}
			var got []string
			if err := json.Unmarshal(out, &got); err != nil {
				t.Fatalf("decode output %q: %v", out, err)
			}
			if !reflect.DeepEqual(got, want) {
				t.Fatalf("arguments=%q, want %q", got, want)
			}
		})
	}
}

func TestCmdArgumentProcessHelper(t *testing.T) {
	if os.Getenv("SLEIPNIR_ARGUMENT_FIXTURE") != "1" {
		return
	}
	for i, arg := range os.Args {
		if arg == "--" {
			if err := json.NewEncoder(os.Stdout).Encode(os.Args[i+1:]); err != nil {
				os.Exit(1)
			}
			os.Exit(0)
		}
	}
	os.Exit(2)
}

// A wrapper in front of cmd /C parses its own command line: it receives every argument, the script included, exactly, and cmd gets
// AutoRun turned off.
func TestConfigureShellThroughAWrapper(t *testing.T) {
	script := `echo "a&b" ^& echo c`
	cmd := exec.Command(`C:\tools\sandbox.exe`, "--net", "off", "--", "cmd", "/C", script)
	ConfigureShell(cmd)
	want := []string{`C:\tools\sandbox.exe`, "--net", "off", "--", "cmd", "/D", "/C", script}
	if !reflect.DeepEqual(cmd.Args, want) || (cmd.SysProcAttr != nil && cmd.SysProcAttr.CmdLine != "") {
		t.Fatalf("args %q, attrs %+v; want %q", cmd.Args, cmd.SysProcAttr, want)
	}
	if ConfigureShell(cmd); len(cmd.Args) != len(want) {
		t.Errorf("a second call changed the args again: %q", cmd.Args)
	}
	other := exec.Command(`C:\tools\sandbox.exe`, "--", "powershell", "/C", "x")
	if ConfigureShell(other); other.SysProcAttr != nil || len(other.Args) != 5 {
		t.Errorf("a wrapper over another program was changed: %q %+v", other.Args, other.SysProcAttr)
	}

	// what a wrapper that parses its command line the usual way receives
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("SLEIPNIR_ARGUMENT_FIXTURE", "1")
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	wrapped := exec.CommandContext(ctx, exe, "-test.run=^TestCmdArgumentProcessHelper$", "--", "cmd", "/C", script)
	ConfigureShell(wrapped)
	out, err := wrapped.CombinedOutput()
	if err != nil {
		t.Fatalf("wrapper: %v; output=%q", err, out)
	}
	var got []string
	if err := json.Unmarshal(out, &got); err != nil {
		t.Fatalf("decode output %q: %v", out, err)
	}
	if w := []string{"cmd", "/D", "/C", script}; !reflect.DeepEqual(got, w) {
		t.Fatalf("the wrapper received %q, want %q", got, w)
	}
}
