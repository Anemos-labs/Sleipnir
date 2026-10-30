package perm

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Cases from a security review of the shell analysis: each is a command the
// analysis used to judge by a different file, program or option than the one
// that runs.
func TestReviewedShellAnalysisGaps(t *testing.T) {
	f := newFixture(t)
	// a numbered link the shell's $$ could select
	for _, n := range []string{"1", "2", "3"} {
		if err := os.Symlink(filepath.Join(f.home, ".ssh", "id_rsa"), filepath.Join(f.root, "out-"+n+".txt")); err != nil {
			t.Fatal(err)
		}
	}
	runCases(t, f, []tc{
		{name: "sort --out is --output", mode: ModeDefault, req: bash("sort --out=x.txt main.go"), want: "ask"},
		{name: "sort --files0-from reads the files a list names", mode: ModeDefault, req: bash("sort --files0-from=list.txt"), want: "ask"},
		{name: "sort --files0 is --files0-from", mode: ModeDefault, req: bash("sort --files0=list.txt"), want: "ask"},
		{name: "wc --files0-from reads the files a list names", mode: ModeDefault, req: bash("wc --files0-from list.txt"), want: "ask"},
		{name: "npx --no is not the system utility", mode: ModeDefault, req: bash("npx --no cat main.go"), want: "ask"},
		{name: "a rule for the system program does not cover npx's", mode: ModeDefault, allow: []string{"Bash(git status)"},
			req: bash("npx --no git status"), want: "ask"},
		{name: "a rule that names npx covers it", mode: ModeDefault, allow: []string{"Bash(npx git status)"},
			req: bash("npx --no git status"), want: "allow"},
		{name: "printf -v PATH steers later commands", mode: ModeDefault, req: bash("printf -v PATH %s /tmp; ls"), want: "ask"},
		{name: "for PATH in steers later commands", mode: ModeDefault, req: bash("for PATH in /tmp; do ls; done"), want: "ask"},
		{name: "curl write-out file", mode: ModeBypass, allow: []string{"Bash(curl:*)"},
			req: bash("curl -w '%output{{home}/.ssh/authorized_keys}x' https://example.com"), want: "deny"},
		{name: "a git push:* rule does not speak for :main", mode: ModeDefault, allow: []string{"Bash(git push:*)"},
			req: bash("git push origin :main"), want: "ask"},
		{name: "git -C elsewhere reset --hard", mode: ModeDefault, allow: []string{"Bash(git reset:*)"},
			req: bash("git -C {out} reset --hard"), want: "ask"},
		{name: "a $$ name a link may answer", mode: ModeDefault, req: bash("cat out-$$.txt"), want: "ask"},
		{name: "grep -r of the harness's own key directory", mode: ModeBypass,
			req: rq{tool: "Grep", paths: []string{"{home}/.sleipnir"}}, want: "deny"},
	})
}

func TestTooManyOperandsIsRefusedInEveryMode(t *testing.T) {
	f := newFixture(t)
	cmd := "cat" + strings.Repeat(" main.go", maxOperands+1)
	for _, m := range []Mode{ModeDefault, ModeBypass, ModeYolo} {
		runCases(t, f, []tc{{name: string(m), mode: m, req: bash(cmd), want: "deny", why: "too many to check"}})
	}
}
