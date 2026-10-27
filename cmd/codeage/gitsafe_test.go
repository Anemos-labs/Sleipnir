package main

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// A repository's configuration cannot make reading its history run a program.
func TestHistoryReadsDoNotRunTheRepositorysPrograms(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("no git")
	}
	repo := t.TempDir()
	marker := filepath.Join(t.TempDir(), "ran")
	run := func(args ...string) {
		cmd := exec.Command("git", append([]string{"-C", repo}, args...)...)
		cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=a", "GIT_AUTHOR_EMAIL=a@b", "GIT_COMMITTER_NAME=a", "GIT_COMMITTER_EMAIL=a@b")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("%v: %s", err, out)
		}
	}
	run("init", "-q")
	if err := os.WriteFile(filepath.Join(repo, "f"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	run("add", "f")
	run("commit", "-qm", "c")
	// A commit that carries a signature, so that showing it would verify it with gpg.program.
	tree, err := exec.Command("git", "-C", repo, "rev-parse", "HEAD^{tree}").Output()
	if err != nil {
		t.Fatal(err)
	}
	obj := "tree " + strings.TrimSpace(string(tree)) + "\nauthor a <a@b> 1 +0000\ncommitter a <a@b> 1 +0000\n" +
		"gpgsig -----BEGIN PGP SIGNATURE-----\n \n x\n -----END PGP SIGNATURE-----\n\nsigned\n"
	hash := exec.Command("git", "-C", repo, "hash-object", "-t", "commit", "-w", "--stdin")
	hash.Stdin = strings.NewReader(obj)
	sha, err := hash.Output()
	if err != nil {
		t.Fatal(err)
	}
	run("update-ref", "HEAD", strings.TrimSpace(string(sha)))
	prog := filepath.Join(t.TempDir(), "evil.sh")
	if err := os.WriteFile(prog, []byte("#!/bin/sh\ntouch "+marker+"\nexit 1\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	run("config", "log.showSignature", "true")
	run("config", "gpg.program", prog)
	run("config", "core.fsmonitor", prog)
	if _, err := git(context.Background(), repo, "log", "-1", "--format=%H %G?"); err != nil && !strings.Contains(err.Error(), "exit") {
		t.Fatal(err)
	}
	if _, err := os.Stat(marker); err == nil {
		t.Fatal("reading the history ran the repository's program")
	}
}
