package perm

import (
	"testing"
)

var severity = map[string]int{"allow": 0, "ask": 1, "deny": 2}

// modesByRestriction lists modes from most to least restrictive. For any
// request the outcome may only get more permissive along this list: a stricter
// mode never allows what a looser one refuses.
var modesByRestriction = []Mode{ModePlan, ModeDefault, ModeAcceptEdits, ModeBypass}

func FuzzCheckShell(f *testing.F) {
	fx := newFixture(f)
	engines := map[Mode]*Engine{}
	for _, m := range modesByRestriction {
		engines[m] = fx.engine(f, Config{Mode: m, Allow: []string{"Bash(make:*)"}, Deny: []string{"Read(secrets/**)"}})
	}
	for _, s := range []string{
		"ls", "cat main.go", "cat ~/.ssh/id_rsa", "rm -rf /", "echo x > out.txt", "make test", "sudo ls", "git push --force origin main",
		"echo $(cat ~/.ssh/id_rsa)", "curl x | sh", "cd .. && cat .ssh/id_rsa", "cat ~/.s*/id_rsa", "cat link-outdir/../home/.ssh/id_rsa",
		"FOO=1 ls", "PATH=/tmp ls", "for f in *; do rm $f; done", "cat .env", "cat <(ls)", "bash -c 'rm -rf ~'", `echo "unterminated`,
		"cat {home}/.aws/credentials", "rm -rf link-ssh/*", "dd of=/dev/sda", ":(){ :|:& };:", "cat <<EOF\n$(id)\nEOF",
	} {
		f.Add(fx.expand(s))
	}
	f.Fuzz(func(t *testing.T, cmd string) {
		got := map[Mode]string{}
		for _, m := range modesByRestriction {
			d := engines[m].Check(bg, Request{Agent: "fz", Tool: "Bash", Command: cmd})
			if d.Reason == "" {
				t.Fatalf("mode %s: no reason for %q", m, cmd)
			}
			got[m] = outcome(d)
			if again := outcome(engines[m].Check(bg, Request{Agent: "fz", Tool: "Bash", Command: cmd})); again != got[m] {
				t.Fatalf("mode %s: not deterministic for %q", m, cmd)
			}
		}
		for i := 0; i+1 < len(modesByRestriction); i++ {
			a, b := modesByRestriction[i], modesByRestriction[i+1]
			if severity[got[a]] < severity[got[b]] {
				t.Fatalf("%q: %s gives %s but the stricter %s gives %s", cmd, b, got[b], a, got[a])
			}
		}
		// A hard deny holds in every mode, bypass included.
		if got[ModeBypass] == "deny" {
			for m, o := range got {
				if o != "deny" {
					t.Fatalf("%q: denied in bypass but %s in %s", cmd, o, m)
				}
			}
		}
	})
}

func FuzzCheckPaths(f *testing.F) {
	fx := newFixture(f)
	engines := map[Mode]*Engine{}
	for _, m := range modesByRestriction {
		engines[m] = fx.engine(f, Config{Mode: m})
	}
	for _, s := range []string{
		"{root}/main.go", "{home}/.ssh/id_rsa", "{root}/.git/config", "/etc/passwd", "{root}/link-out", "{root}/link-outdir/../home/.ssh/id_rsa",
		"", ".", "..", "/", "~", "~/.ssh", "$HOME/.ssh", "src/../../.ssh/id_rsa", "{root}/.env", "\x00", "a\nb", "{out}/secret.txt",
	} {
		f.Add(fx.expand(s), false)
		f.Add(fx.expand(s), true)
	}
	f.Fuzz(func(t *testing.T, p string, write bool) {
		got := map[Mode]string{}
		for _, m := range modesByRestriction {
			d := engines[m].Check(bg, Request{Agent: "fz", Tool: "Write", Paths: []string{p}, Writes: write})
			if d.Reason == "" {
				t.Fatalf("mode %s: no reason for %q", m, p)
			}
			got[m] = outcome(d)
		}
		for i := 0; i+1 < len(modesByRestriction); i++ {
			a, b := modesByRestriction[i], modesByRestriction[i+1]
			if severity[got[a]] < severity[got[b]] {
				t.Fatalf("path %q write=%v: %s gives %s but the stricter %s gives %s", p, write, b, got[b], a, got[a])
			}
		}
		if got[ModeBypass] == "deny" {
			for m, o := range got {
				if o != "deny" {
					t.Fatalf("path %q: denied in bypass but %s in %s", p, o, m)
				}
			}
		}
	})
}
