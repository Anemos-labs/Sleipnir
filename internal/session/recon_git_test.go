package session

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

// gitIn runs git in dir with the user's and the system's configuration out of the way
// and returns its output; a git that cannot run, or a command that fails, skips the test
// (these tests are about what recon does with a repository, not about git).
func gitIn(t *testing.T, dir string, stdin string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-c", "user.name=t", "-c", "user.email=t@t", "-c", "commit.gpgsign=false"}, args...)...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GIT_CONFIG_GLOBAL="+os.DevNull, "GIT_CONFIG_NOSYSTEM=1", "GIT_TERMINAL_PROMPT=0")
	if stdin != "" {
		cmd.Stdin = strings.NewReader(stdin)
	}
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Skipf("git %v: %v\n%s", args, err, out)
	}
	return string(out)
}

func writeTree(t *testing.T, root string, files map[string]string) {
	t.Helper()
	for name, body := range files {
		p := filepath.Join(root, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

// hookScript writes an executable shell script that records that it ran, and returns
// its path and the file it records in.
func hookScript(t *testing.T, tail string) (script, marker string) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("the hook is a shell script")
	}
	marker = filepath.Join(t.TempDir(), "ran")
	script = filepath.Join(t.TempDir(), "hook.sh")
	if err := os.WriteFile(script, []byte("#!/bin/sh\necho ran >> '"+marker+"'\n"+tail), 0o755); err != nil {
		t.Fatal(err)
	}
	return script, marker
}

func ran(marker string) bool {
	_, err := os.Stat(marker)
	return err == nil
}

// A repository is untrusted input: its .git/config can name programs that git runs on
// ordinary commands, and BuildRecon runs at the start of every session, before the
// person has said whether the project is trusted. It must run git the way internal/gitx
// does, with every configuration key that names a program overridden, or a directory that
// someone else prepared (an unpacked archive, a project nobody audited) runs code when its
// survey is taken.
//
// Each case plants one such key, first checks that plain git really runs the program (so
// that the case means something on this git), then that BuildRecon does not.
func TestReconDoesNotRunProgramsARepositoryConfigNames(t *testing.T) {
	for _, tc := range []struct {
		name string
		// plant configures repo so that one git command the survey runs executes hook;
		// probe is that command as plain git would run it.
		plant func(t *testing.T, repo, hook string)
		probe []string
	}{
		{"core.fsmonitor runs on ls-files", func(t *testing.T, repo, hook string) {
			gitIn(t, repo, "", "config", "core.fsmonitor", hook)
			gitIn(t, repo, "", "config", "core.untrackedCache", "true")
		}, []string{"ls-files", "-z", "--cached", "--others", "--exclude-standard"}},

		{"a signature verifier runs on log", func(t *testing.T, repo, hook string) {
			// A commit that carries a signature header (this one is not a real signature: it only has to
			// be there) makes `git log` ask gpg.program to verify it when log.showSignature is on.
			tree := strings.TrimSpace(gitIn(t, repo, "", "rev-parse", "HEAD^{tree}"))
			parent := strings.TrimSpace(gitIn(t, repo, "", "rev-parse", "HEAD"))
			now := time.Now().Unix()
			commit := fmt.Sprintf("tree %s\nparent %s\nauthor t <t@t> %d +0000\ncommitter t <t@t> %d +0000\n"+
				"gpgsig -----BEGIN PGP SIGNATURE-----\n \n aGVsbG8=\n -----END PGP SIGNATURE-----\n\nsigned\n", tree, parent, now, now)
			sha := strings.TrimSpace(gitIn(t, repo, commit, "hash-object", "-t", "commit", "-w", "--stdin"))
			gitIn(t, repo, "", "update-ref", "HEAD", sha)
			gitIn(t, repo, "", "config", "log.showSignature", "true")
			gitIn(t, repo, "", "config", "gpg.program", hook)
		}, []string{"log", "--name-only", "--pretty=format:", "--since=180.days", "-n", "400"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			repo := t.TempDir()
			gitIn(t, repo, "", "init", "-q", ".")
			writeTree(t, repo, map[string]string{"a.go": "package a\n\nfunc A() {}\n"})
			gitIn(t, repo, "", "add", "-A")
			gitIn(t, repo, "", "commit", "-qm", "init")
			hook, marker := hookScript(t, "printf '\\0'\n")
			tc.plant(t, repo, hook)

			probe := exec.Command("git", append([]string{"-C", repo}, tc.probe...)...)
			probe.Env = append(os.Environ(), "GIT_CONFIG_GLOBAL="+os.DevNull, "GIT_CONFIG_NOSYSTEM=1")
			_ = probe.Run()
			if !ran(marker) {
				t.Skip("this git does not run that program for that command; nothing to guard against here")
			}
			os.Remove(marker)

			if _, err := BuildRecon(context.Background(), ReconOptions{Root: repo}); err != nil {
				t.Fatal(err)
			}
			if ran(marker) {
				t.Fatalf("BuildRecon ran the program the repository's configuration names")
			}
		})
	}
}

// The listing through git and the walk of the directory tree are two ways to the same
// survey: for a repository with nothing ignored they give the same files and the same
// text, from the top of the work tree and from a directory inside it (where git prints
// paths relative to the work tree, and the survey wants them relative to its root).
func TestReconThroughGitMatchesTheWalk(t *testing.T) {
	repo := t.TempDir()
	writeTree(t, repo, map[string]string{
		"go.mod":                    "module example.com/app\n\ngo 1.24\n",
		"main.go":                   "package main\n\nfunc main() {}\n",
		"core/core.go":              "// Package core is the heart.\npackage core\n\nfunc Run() {}\n\ntype Engine struct{}\n",
		"core/util/util.go":         "package util\n\nimport \"example.com/app/core\"\n\nfunc Helper() { core.Run() }\n",
		"docs/guide.md":             "# guide\n",
		"sub dir/with space.go":     "package s\n\nfunc Spaced() {}\n",
		"sub dir/ünïcode.go":        "package s\n\nfunc Uni() {}\n",
		"sub dir/deeper/x/file.go":  "package x\n\nfunc Deep() {}\n",
		"Makefile":                  "test:\n\tgo test ./...\n",
		".github/workflows/ci.yml":  "name: ci\n",
		"vendor/third/party/lib.go": "package party\n\nfunc Vendored() {}\n",
		"glob[1]*?/g.go":            "package g\n\nfunc Glob() {}\n",
		":magic/m.go":               "package m\n\nfunc Magic() {}\n",
		"back\\slash/b.go":          "package b\n\nfunc Back() {}\n",
	})
	gitIn(t, repo, "", "init", "-q", ".")
	gitIn(t, repo, "", "add", "-A")
	gitIn(t, repo, "", "commit", "-qm", "init")

	roots := []string{repo, filepath.Join(repo, "core"), filepath.Join(repo, "sub dir")}
	for _, odd := range []string{"glob[1]*?", ":magic", "back\\slash"} { // names that a pathspec would read as patterns
		roots = append(roots, filepath.Join(repo, odd))
	}
	for _, root := range roots {
		withGit, err := BuildRecon(context.Background(), ReconOptions{Root: root, BudgetTokens: 3000})
		if err != nil {
			t.Fatal(err)
		}
		walked, err := BuildRecon(context.Background(), ReconOptions{Root: root, BudgetTokens: 3000, NoGit: true})
		if err != nil {
			t.Fatal(err)
		}
		// Git lists what it tracks under vendor/ and the walk skips the directory; both drop it.
		if withGit.Files != walked.Files {
			t.Errorf("%s: %d files through git, %d by walking", root, withGit.Files, walked.Files)
		}
		if a, b := joinSegments(withGit), joinSegments(walked); a != b {
			t.Errorf("%s: the survey differs between git and the walk:\n--- git ---\n%s\n--- walk ---\n%s", root, a, b)
		}
	}
}

func joinSegments(r *Recon) string {
	var sb strings.Builder
	for _, s := range r.Segments {
		sb.WriteString("## " + s.Key + "\n" + s.Text + "\n")
	}
	return sb.String()
}

// Through git, files that are ignored stay out and untracked ones come in, and a
// directory that is not a repository at all is walked.
func TestReconHonoursGitignoreAndFallsBackToTheWalk(t *testing.T) {
	repo := t.TempDir()
	writeTree(t, repo, map[string]string{
		"a.go": "package a\n", ".gitignore": "ignored.go\n", "ignored.go": "package ignored\n",
	})
	gitIn(t, repo, "", "init", "-q", ".")
	gitIn(t, repo, "", "add", "a.go", ".gitignore")
	gitIn(t, repo, "", "commit", "-qm", "init")
	writeTree(t, repo, map[string]string{"untracked.go": "package u\n"})
	r, err := BuildRecon(context.Background(), ReconOptions{Root: repo})
	if err != nil {
		t.Fatal(err)
	}
	if r.Files != 3 { // a.go, .gitignore, untracked.go
		t.Errorf("%d files, want a.go, .gitignore and untracked.go", r.Files)
	}

	plain := t.TempDir()
	writeTree(t, plain, map[string]string{"x.go": "package x\n", "node_modules/m/y.go": "package y\n"})
	r, err = BuildRecon(context.Background(), ReconOptions{Root: plain})
	if err != nil {
		t.Fatal(err)
	}
	if r.Files != 1 {
		t.Errorf("a directory that is not a repository: %d files, want 1", r.Files)
	}
}

// A listing or a history bigger than the cap is not read: the survey carries on with the
// walk and without the churn ranking, it does not fail and does not buffer the lot.
func TestReconCutsOffGitOutputThatIsTooBig(t *testing.T) {
	repo := t.TempDir()
	files := map[string]string{}
	for i := 0; i < 400; i++ {
		files[fmt.Sprintf("pkg%03d/file%03d.go", i/20, i)] = "package p\n\nfunc F() {}\n"
	}
	writeTree(t, repo, files)
	gitIn(t, repo, "", "init", "-q", ".")
	gitIn(t, repo, "", "add", "-A")
	gitIn(t, repo, "", "commit", "-qm", "init")

	old := reconGitOutput
	reconGitOutput = 4 << 10 // the listing is about 16 KiB
	defer func() { reconGitOutput = old }()
	r, err := BuildRecon(context.Background(), ReconOptions{Root: repo})
	if err != nil {
		t.Fatal(err)
	}
	if r.Files != 400 {
		t.Errorf("%d files, want the walk's 400", r.Files)
	}
	if churn := gitChurn(context.Background(), repo); len(churn) != 0 {
		t.Errorf("a history over the cap gave %d churn entries", len(churn))
	}
}

// Churn is counted per file as the survey names it: relative to its root, whether that is the
// top of the work tree or a directory inside it, and with names that git would quote (a
// backslash, a newline) as they are.
func TestReconChurnIsKeyedByTheNamesOfTheSurvey(t *testing.T) {
	repo := t.TempDir()
	writeTree(t, repo, map[string]string{
		"core/a.go": "package core\n", "core/deep/b.go": "package deep\n", "other/c.go": "package other\n",
		"core/back\\slash.go": "package core\n", "core/new\nline.go": "package core\n",
	})
	gitIn(t, repo, "", "init", "-q", ".")
	gitIn(t, repo, "", "add", "-A")
	gitIn(t, repo, "", "commit", "-qm", "one")
	writeTree(t, repo, map[string]string{"core/a.go": "package core\n\nfunc A() {}\n"})
	gitIn(t, repo, "", "commit", "-qam", "two")

	top := gitChurn(context.Background(), repo)
	if top["core/a.go"] != 2 || top["core/deep/b.go"] != 1 || top["other/c.go"] != 1 || top["core/back\\slash.go"] != 1 || top["core/new\nline.go"] != 1 {
		t.Errorf("churn from the top of the work tree: %v", top)
	}
	sub := gitChurn(context.Background(), filepath.Join(repo, "core"))
	if sub["a.go"] != 2 || sub["deep/b.go"] != 1 || sub["back\\slash.go"] != 1 || sub["new\nline.go"] != 1 || len(sub) != 4 {
		t.Errorf("churn from a directory inside it: %v", sub)
	}
}
