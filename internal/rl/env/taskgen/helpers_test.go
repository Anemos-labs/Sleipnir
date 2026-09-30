package taskgen

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/reee344/sleipnir/internal/rl/env"
)

var sharedGoCache string

func TestMain(m *testing.M) {
	dir := ""
	if out, err := exec.Command("go", "env", "GOCACHE").Output(); err == nil {
		dir = strings.TrimSpace(string(out))
	}
	cleanup := false
	if dir == "" || dir == "off" {
		d, err := os.MkdirTemp("", "taskgentest-gocache-")
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(2)
		}
		dir, cleanup = d, true
	}
	sharedGoCache = dir
	code := m.Run()
	if cleanup {
		_ = os.RemoveAll(dir)
	}
	os.Exit(code)
}

func gitEnv(extra ...string) []string {
	env := []string{
		"PATH=" + os.Getenv("PATH"), "HOME=" + os.TempDir(), "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=/dev/null",
		"GIT_AUTHOR_NAME=Test Author", "GIT_AUTHOR_EMAIL=author@example.com",
		"GIT_COMMITTER_NAME=Test Committer", "GIT_COMMITTER_EMAIL=committer@example.com", "LC_ALL=C",
	}
	return append(env, extra...)
}

// fixture is a real git repository.
type fixture struct {
	t   testing.TB
	Dir string
	n   int
}

func newFixture(t testing.TB) *fixture {
	t.Helper()
	f := &fixture{t: t, Dir: t.TempDir()}
	f.git("init", "-q", "-b", "main")
	return f
}

func (f *fixture) git(args ...string) string {
	f.t.Helper()
	return f.gitEnv(nil, args...)
}

func (f *fixture) gitEnv(extra []string, args ...string) string {
	f.t.Helper()
	cmd := exec.Command("git", append([]string{"-c", "core.hooksPath=/dev/null", "-c", "commit.gpgSign=false", "-c", "merge.ff=false"}, args...)...)
	cmd.Dir = f.Dir
	cmd.Env = gitEnv(extra...)
	var out, errb bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errb
	if err := cmd.Run(); err != nil {
		f.t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, errb.String())
	}
	return strings.TrimSpace(out.String())
}

func (f *fixture) write(rel, content string) {
	f.t.Helper()
	p := filepath.Join(f.Dir, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		f.t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		f.t.Fatal(err)
	}
}

func (f *fixture) remove(rel string) {
	f.t.Helper()
	if err := os.Remove(filepath.Join(f.Dir, filepath.FromSlash(rel))); err != nil {
		f.t.Fatal(err)
	}
}

func (f *fixture) commit(msg string) string {
	f.t.Helper()
	f.n++
	date := time.Date(2024, 1, f.n, 12, 0, 0, 0, time.UTC).Format(time.RFC3339)
	f.git("add", "-A")
	f.gitEnv([]string{"GIT_AUTHOR_DATE=" + date, "GIT_COMMITTER_DATE=" + date}, "commit", "-q", "--allow-empty", "-m", msg)
	return f.git("rev-parse", "HEAD")
}

func (f *fixture) head() string { return f.git("rev-parse", "HEAD") }

const mitLicense = `MIT License

Copyright (c) 2024 Example

Permission is hereby granted, free of charge, to any person obtaining a copy
of this software and associated documentation files (the "Software"), to deal
in the Software without restriction.
`

func newManager(t testing.TB) *env.Workspaces {
	t.Helper()
	m, err := env.NewWorkspaces(env.WorkspaceOptions{
		Root:                filepath.Join(t.TempDir(), "root"),
		DisableNetIsolation: true,
		SetupTimeout:        2 * time.Minute,
		SetEnv:              map[string]string{"GOCACHE": sharedGoCache},
		FailureTTL:          -1,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(m.Close)
	return m
}

func mustRead(t testing.TB, p string) string {
	t.Helper()
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func have(t testing.TB, name string) {
	t.Helper()
	if _, err := exec.LookPath(name); err != nil {
		t.Skipf("%s is not installed", name)
	}
}

func havePytest(t testing.TB) {
	t.Helper()
	have(t, "python3")
	if err := exec.Command("python3", "-m", "pytest", "--version").Run(); err != nil {
		t.Skip("pytest is not installed for python3")
	}
}

func writeFile(p, content string) error {
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return err
	}
	return os.WriteFile(p, []byte(content), 0o644)
}
