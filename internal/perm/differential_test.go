package perm

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// The engine's whole job is to not let secrets out and not let protected files
// be written. This test checks that against reality: it generates many spellings
// of "read a secret" and "write a protected file", runs each in a real bash in
// the fixture, and requires that whenever bash actually leaked or wrote, the
// engine did not say yes. (The reverse - refusing things that are harmless - is
// safe and is not checked here.)

func runBash(t *testing.T, f fixture, cwd, script string) string {
	t.Helper()
	bash, err := exec.LookPath("bash")
	if err != nil {
		t.Skip("bash not installed")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, bash, "--noprofile", "--norc", "-c", script)
	cmd.Dir = cwd
	cmd.Env = []string{"PATH=/usr/bin:/bin", "HOME=" + f.home}
	out, _ := cmd.CombinedOutput()
	return string(out)
}

func TestNoLeakDifferential(t *testing.T) {
	if testing.Short() {
		t.Skip("short mode")
	}
	f := newFixture(t)
	paths := []string{
		`~/.ssh/id_rsa`, `$HOME/.ssh/id_rsa`, `${HOME}/.ssh/id_rsa`, `"$HOME"/.ssh/id_rsa`, `"$HOME/.ssh/id_rsa"`, `~/.ssh/../.ssh/id_rsa`,
		`~/./.ssh//id_rsa`, `~/.s*/id_rsa`, `~/.ss?/id_rsa`, `~/.[s]sh/id_rsa`, `~/{.ssh,.x}/id_rsa`, `~/.ssh/id_[r]sa`, `~/.ssh/id_r*`,
		`~/.ssh/id_[!x]sa`, `~/.ssh/id_[[:lower:]]sa`, `../.ssh/id_rsa`, `../../home/.ssh/id_rsa`, `link-ssh/id_rsa`, `link-ssh/../.ssh/id_rsa`,
		`link-outdir/../home/.ssh/id_rsa`, `~/'.ssh'/id_rsa`, `~/.ss\h/id_rsa`, `~/.s""sh/id_rsa`, `$'~'/.ssh/id_rsa`, `~/.aws/credentials`,
		`~/.aws/cred*`, `~/.gnupg/pubring.kbx`, `~/.config/gcloud/creds.json`, `~/.npmrc`, `.env`, `./.env`, `.e*`, `.en?`, `.{env,x}`, `src/../.env`,
		`$PWD/.env`, `"$PWD"/.env`, `$(pwd)/.env`, `${PWD}/.env`, `~/proj/.env`, `~+/.env`, `../proj/.env`, `/proc/self/../self/cwd/.env`,
		`{home}/.ssh/id_rsa`, `{root}/.env`, `{root}/../.ssh/id_rsa`, `{root}/link-ssh/id_rsa`,
	}
	forms := []string{
		`cat @P@`, `cat < @P@`, `head -c 100 @P@`, `tail @P@`, `sort @P@`, `while read l; do echo $l; done < @P@`, `grep -h S @P@`, `cat @P@ @P@`, `nl @P@`, `wc -c @P@; cat @P@`,
		`for f in @P@; do cat $f; done`, `cat -- @P@`, `cd .. && cat @P@`, `cd src && cat @P@`, `cd ~ && cat @P@`, `cp @P@ /dev/stdout`,
	}
	progs := []func(string) string{
		func(s string) string { return s },
		func(s string) string { return strings.Replace(s, "cat", `\cat`, 1) },
		func(s string) string { return strings.Replace(s, "cat", `c""at`, 1) },
		func(s string) string { return strings.Replace(s, "cat", `'c'a't`, 1) },
		func(s string) string { return strings.Replace(s, "cat", `$'\x63at'`, 1) },
		func(s string) string { return strings.Replace(s, "cat", `/bin/cat`, 1) },
		func(s string) string { return strings.Replace(s, "cat", `env cat`, 1) },
		func(s string) string { return strings.Replace(s, "cat", `command cat`, 1) },
		func(s string) string { return strings.Replace(s, "cat", `nohup cat`, 1) },
		func(s string) string { return strings.Replace(s, "cat", `time cat`, 1) },
	}
	contexts := []func(string) string{
		func(s string) string { return s },
		func(s string) string { return "echo $(" + s + ")" },
		func(s string) string { return "x=$(" + s + "); echo $x" },
		func(s string) string { return "bash -c '" + strings.ReplaceAll(s, "'", `'\''`) + "'" },
		func(s string) string { return "eval '" + strings.ReplaceAll(s, "'", `'\''`) + "'" },
		func(s string) string { return s + " | cat" },
		func(s string) string { return "{ " + s + "; }" },
		func(s string) string { return "( " + s + " )" },
		func(s string) string { return "cat <(" + s + ")" },
		func(s string) string { return "for i in 1; do " + s + "; done" },
		func(s string) string { return "f() { " + s + "; }; f" },
		func(s string) string { return "true && " + s },
		func(s string) string { return "ls; " + s + "; ls" },
		func(s string) string { return "cat <<EOF\n$(" + s + ")\nEOF" },
		func(s string) string { return "trap '" + strings.ReplaceAll(s, "'", `'\''`) + "' EXIT" },
	}
	engines := map[Mode]*Engine{}
	for _, m := range []Mode{ModeDefault, ModeAcceptEdits, ModePlan} {
		engines[m] = f.engine(t, Config{Mode: m})
	}
	total, seen, leaks, bad := 0, 0, 0, 0
	for _, p := range paths {
		for _, form := range forms {
			for _, prog := range progs {
				for ci, ctx := range contexts {
					// Vary the program spelling and the context independently but
					// not as a full cross product of everything (that is 100k runs).
					if ci%3 != (len(p)+len(form))%3 {
						continue
					}
					seen++
					if !fullDifferential() && seen%32 != 0 { // sample: the full set takes minutes
						continue
					}
					script := f.expand(ctx(prog(strings.ReplaceAll(form, "@P@", p))))
					total++
					out := runBash(t, f, f.root, script)
					if !strings.Contains(out, "SECRET") {
						continue
					}
					leaks++
					for mode, e := range engines {
						d := e.Check(bg, Request{Agent: "d", Tool: "Bash", Command: script, Cwd: f.root})
						if d.Allow {
							bad++
							t.Errorf("bash leaked a secret but %s mode allowed it (%s):\n  %q", mode, d.Reason, script)
						}
					}
				}
			}
		}
	}
	t.Logf("ran %d of %d generated scripts in bash (set SLEIPNIR_DIFF_FULL=1 for all): %d actually leaked a secret, engine allowed %d", total, seen, leaks, bad)
	if leaks == 0 {
		t.Error("the generator never produced a leaking script: the test is not testing anything")
	}
}

// fullDifferential runs every generated script instead of a sample.
func fullDifferential() bool { return os.Getenv("SLEIPNIR_DIFF_FULL") != "" }

func TestNoProtectedWriteDifferential(t *testing.T) {
	if testing.Short() {
		t.Skip("short mode")
	}
	f := newFixture(t)
	targets := []struct {
		name  string
		path  string // as written in the script
		check string // file whose content is inspected afterwards
	}{
		{"git config", `.git/config`, filepath.Join(f.root, ".git", "config")},
		{"git config abs", `{root}/.git/config`, filepath.Join(f.root, ".git", "config")},
		{"git config dotdot", `src/../.git/config`, filepath.Join(f.root, ".git", "config")},
		{"git config glob", `.g*/config`, filepath.Join(f.root, ".git", "config")},
		{"git config brace", `.{git,x}/config`, filepath.Join(f.root, ".git", "config")},
		{"git hook", `.git/hooks/pre-commit`, filepath.Join(f.root, ".git", "hooks", "pre-commit")},
		{"ssh authorized_keys", `~/.ssh/authorized_keys`, filepath.Join(f.home, ".ssh", "authorized_keys")},
		{"ssh via link", `link-ssh/authorized_keys`, filepath.Join(f.home, ".ssh", "authorized_keys")},
		{"ssh via dangling link", `dangling`, filepath.Join(f.home, ".ssh", "newkey")},
		{"ssh via dotdot", `link-outdir/../home/.ssh/authorized_keys`, filepath.Join(f.home, ".ssh", "authorized_keys")},
		{"aws config", `$HOME/.aws/config`, filepath.Join(f.home, ".aws", "config")},
		{"dotenv", `.env`, filepath.Join(f.root, ".env")},
		{"outside file", `{out}/pwned`, filepath.Join(f.outside, "pwned")},
		{"outside via link", `link-outdir/pwned`, filepath.Join(f.outside, "pwned")},
		{"home file", `~/pwned`, filepath.Join(f.home, "pwned")},
	}
	forms := []string{
		`echo PWNED > @P@`, `echo PWNED >> @P@`, `echo PWNED | tee @P@`, `printf PWNED > @P@`, `cat <<< PWNED > @P@`, `: > @P@; echo PWNED >> @P@`,
		`cp main.go @P@`, `mkdir -p @P@; echo PWNED > @P@/x`, `echo PWNED 1> @P@`, `echo PWNED &> @P@`, `echo PWNED >| @P@`,
	}
	contexts := []func(string) string{
		func(s string) string { return s },
		func(s string) string { return "bash -c '" + strings.ReplaceAll(s, "'", `'\''`) + "'" },
		func(s string) string { return "eval '" + strings.ReplaceAll(s, "'", `'\''`) + "'" },
		func(s string) string { return "{ " + s + "; }" },
		func(s string) string { return "( " + s + " )" },
		func(s string) string { return "for i in 1; do " + s + "; done" },
		func(s string) string { return "ls && " + s },
		func(s string) string { return "cd src && " + s },
		func(s string) string { return "cd .. && " + s },
	}
	engines := map[Mode]*Engine{}
	for _, m := range []Mode{ModeDefault, ModeAcceptEdits} {
		engines[m] = f.engine(t, Config{Mode: m})
	}
	total, seen, wrote, bad := 0, 0, 0, 0
	for _, tg := range targets {
		for _, form := range forms {
			for ci, ctx := range contexts {
				script := f.expand(ctx(strings.ReplaceAll(form, "@P@", tg.path)))
				if strings.HasPrefix(ctx("x"), "cd ..") || ci == 8 { // relative spellings are written for cwd=root; adjust the ones that assume it
					if !strings.HasPrefix(tg.path, "~") && !strings.HasPrefix(tg.path, "$") && !strings.HasPrefix(tg.path, "{") {
						continue
					}
				}
				seen++
				if !fullDifferential() && seen%3 != 0 {
					continue
				}
				total++
				before, _ := os.ReadFile(tg.check)
				runBash(t, f, f.root, script)
				after, err := os.ReadFile(tg.check)
				wroteIt := err == nil && strings.Contains(string(after), "PWNED") && !strings.Contains(string(before), "PWNED")
				// restore
				if err == nil {
					_ = os.WriteFile(tg.check, before, 0o600)
				}
				if before == nil {
					_ = os.RemoveAll(tg.check)
				}
				_ = os.RemoveAll(filepath.Join(f.root, "src", "x"))
				if !wroteIt {
					continue
				}
				wrote++
				for mode, e := range engines {
					d := e.Check(bg, Request{Agent: "d", Tool: "Bash", Command: script, Cwd: f.root})
					// Default mode asks for every write; accept-edits must still refuse
					// everything protected or outside the workspace.
					if d.Allow && (mode == ModeDefault || tg.name != "outside file" || true) {
						bad++
						t.Errorf("bash wrote %s but %s mode allowed it (%s):\n  %q", tg.name, mode, d.Reason, script)
					}
				}
			}
		}
	}
	t.Logf("ran %d of %d generated scripts in bash: %d actually wrote a protected target, engine allowed %d", total, seen, wrote, bad)
	if wrote == 0 {
		t.Error("the generator never wrote anything: the test is not testing anything")
	}
}

// snapshot records every file under dir (size, mode, mtime, content) so a test
// can tell whether a command changed anything.
func snapshot(t *testing.T, dir string) map[string]string {
	t.Helper()
	m := map[string]string{}
	err := filepath.Walk(dir, func(p string, fi os.FileInfo, err error) error {
		if err != nil {
			return nil
		}
		// Toolchains keep caches and module directories under HOME; they are not
		// the workspace and a read-only command may legitimately touch them.
		for _, tc := range []string{"/.cache", "/go/", "/.config/go", "/Library/Caches"} {
			if strings.Contains(filepath.ToSlash(p)+"/", tc) {
				return nil
			}
		}
		sig := fi.Mode().String() + "|" + fi.ModTime().String()
		if fi.Mode().IsRegular() {
			b, _ := os.ReadFile(p)
			sig += "|" + string(b)
		}
		m[p] = sig
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return m
}

// Everything on the allowlist claims to be read-only. Run each entry for real
// and require that no file anywhere in the fixture changes.
func TestAllowedCommandsChangeNothing(t *testing.T) {
	if testing.Short() {
		t.Skip("short mode")
	}
	f := newFixture(t)
	e := f.engine(t, Config{})
	// The go command records telemetry under $HOME/.config/go the first time it runs, which
	// would create a directory the snapshot below sees appear. That is the toolchain's own
	// bookkeeping and not what a command does to the workspace: switch it off before the
	// first snapshot (the file is what `go telemetry off` writes).
	mode := filepath.Join(f.home, ".config", "go", "telemetry", "mode")
	if err := os.MkdirAll(filepath.Dir(mode), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(mode, []byte("off 2026-01-01\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, c := range safeCommands {
		d := e.Check(bg, Request{Agent: "d", Tool: "Bash", Command: c, Cwd: f.root})
		if !d.Allow || strings.Contains(c, "tail -f") {
			continue // the table test covers what is allowed; tail -f would just wait for the timeout
		}
		before := snapshot(t, f.base)
		runBash(t, f, f.root, c)
		after := snapshot(t, f.base)
		for p, sig := range after {
			if before[p] != sig {
				t.Errorf("%q was allowed as read-only but changed %s", c, p)
			}
		}
		for p := range before {
			if _, ok := after[p]; !ok {
				t.Errorf("%q was allowed as read-only but removed %s", c, p)
			}
		}
	}
}
