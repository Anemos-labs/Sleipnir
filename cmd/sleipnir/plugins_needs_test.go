package main

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/anemos-labs/sleipnir/internal/config"
	"github.com/anemos-labs/sleipnir/internal/plugin"
)

// needsCLI is a plugins command whose needs are looked for only in the returned bin directory.
func needsCLI(t *testing.T) (pluginCLI, *bytes.Buffer, string) {
	t.Helper()
	home, bin := t.TempDir(), t.TempDir()
	t.Setenv("SLEIPNIR_HOME", "")
	was := needEnv
	t.Cleanup(func() { needEnv = was })
	needEnv = func(h string) plugin.NeedEnv {
		return plugin.NeedEnv{State: filepath.Join(h, ".sleipnir"), Home: h, PATH: bin, SkipDefaults: true}
	}
	var out bytes.Buffer
	return pluginCLI{out: &out, home: home, cwd: home, root: home, target: config.UserConfigPath(home)}, &out, bin
}

// writeProgram writes a program that prints "name 1.0".
func writeProgram(t *testing.T, dir, name string) string {
	t.Helper()
	p, body := filepath.Join(dir, name), "#!/bin/sh\necho '"+name+" 1.0'\n"
	if runtime.GOOS == "windows" {
		p, body = p+".cmd", "@echo "+name+" 1.0\r\n"
	}
	if err := os.WriteFile(p, []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestPluginsSetupShowsTheCommandAndAsksFirst(t *testing.T) {
	p, out, bin := needsCLI(t)
	if err := p.setup(context.Background(), "rea-mcp", "", false, strings.NewReader("y\n")); err == nil || !strings.Contains(err.Error(), "npx") {
		t.Fatalf("without npx: %v", err)
	}
	writeProgram(t, bin, "npx")
	out.Reset()
	if err := p.setup(context.Background(), "rea-mcp", "", false, strings.NewReader("n\n")); err == nil {
		t.Fatal("a no installed it")
	}
	if !strings.Contains(out.String(), `adds the "rea" tool server`) {
		t.Errorf("the question did not say what it does:\n%s", out)
	}
	if _, err := os.Stat(config.UserConfigPath(p.home)); !os.IsNotExist(err) {
		t.Fatalf("a no wrote the configuration: %v", err)
	}
	if err := p.setup(context.Background(), "rea-mcp", "", false, strings.NewReader("y\n")); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(config.UserConfigPath(p.home)); !strings.Contains(string(b), "rea-agents@") {
		t.Errorf("config = %s", b)
	}
	out.Reset()
	if err := p.needs(); err != nil || !strings.Contains(out.String(), "found (config)") {
		t.Errorf("needs after setup: %v\n%s", err, out)
	}
}

func TestPluginsLocateAndForget(t *testing.T) {
	p, out, _ := needsCLI(t)
	prog := writeProgram(t, t.TempDir(), "blender")
	if err := p.locate(context.Background(), "blender", prog); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "blender 1.0") {
		t.Errorf("locate said:\n%s", out)
	}
	if err := p.locate(context.Background(), "nothing", prog); err == nil {
		t.Error("an unknown need was located")
	}
	if err := p.forget("blender"); err != nil {
		t.Fatal(err)
	}
	out.Reset()
	_ = p.needs()
	if !strings.Contains(out.String(), "blender       Blender                not found") {
		t.Errorf("after forget:\n%s", out)
	}
}
