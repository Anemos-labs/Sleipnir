package perm

import (
	"context"
	"strings"
	"sync"
	"testing"

	"github.com/anemos-labs/sleipnir/internal/shellparse"
)

// trialScript is a worker's smoke test from a real session: the scratch file is named with the shell's pid and exported, a trap removes
// it, the program under test runs, and stderr goes to the scratch directory. Every part but the program itself (python3 -m todo) is
// allowed in accept-edits mode; the program asks, as any program does, and the question is the manager's.
const trialScript = "set -eu\nexport TASKS_FILE=\"$TMPDIR/todo-check-$$.json\"\ntrap 'rm -f \"$TASKS_FILE\"' EXIT\n" +
	"python3 -m todo add 'buy milk'\npython3 -m todo add 'write tests'\npython3 -m todo list\npython3 -m todo done 1\n" +
	"python3 -m todo list\npython3 -m todo remove 2\ncat \"$TASKS_FILE\"\n" +
	"if python3 -m todo done 8 2>\"$TMPDIR/todo-error-$$\"; then echo 'expected invalid ID to fail' >&2; exit 1; fi\n" +
	"grep '^Error:' \"$TMPDIR/todo-error-$$\"\nrm -f \"$TMPDIR/todo-error-$$\"\npython3 -m py_compile todo.py\nrm -rf __pycache__"

// The trial's script asks only about the program it tests, says so, and goes to the manager; without the program it asks nothing.
func TestTheTrialScriptAsksOnlyAboutItsProgram(t *testing.T) {
	for _, mode := range []Mode{ModeAcceptEdits, ModeDefault} {
		var mu sync.Mutex
		var delegated []Request
		r := newTeamRig(t, mode, func(context.Context, Request) Decision { return Decision{} })
		r.e.cfg.Delegate = func(_ context.Context, q Request) (Decision, string) {
			mu.Lock()
			delegated = append(delegated, q)
			mu.Unlock()
			return Decision{Allow: true}, "manager"
		}
		if d := r.check("be-1", r.wtA, trialScript); !d.Allow {
			t.Fatalf("%s: the manager's yes did not settle it: %+v", mode, d)
		}
		if len(delegated) != 1 {
			t.Fatalf("%s: %d questions went to the manager, want 1", mode, len(delegated))
		}
		why := delegated[0].Why
		if mode == ModeAcceptEdits && !strings.Contains(why, "python3 -m todo add 'buy milk': runs the Python module todo") {
			t.Errorf("%s: reason %q does not name the program", mode, why)
		}
		for _, bad := range []string{"Edit(", "~/.sleipnir", "only known when the command runs"} {
			if strings.Contains(why, bad) {
				t.Errorf("%s: reason %q says %q", mode, why, bad)
			}
		}
	}
	r := newTeamRig(t, ModeAcceptEdits, nil)
	var kept []string
	for _, l := range strings.Split(trialScript, "\n") {
		if !strings.HasPrefix(l, "python3 -m todo") {
			kept = append(kept, strings.ReplaceAll(l, "python3 -m todo done 8", "false"))
		}
	}
	if d := r.check("be-1", r.wtA, strings.Join(kept, "\n")); !d.Allow {
		t.Errorf("the script without its program: %+v", d)
	}
}

// The shell's special parameters are numbers: a name made with one is a name in its directory. A part that would be "", "." or ".."
// when a parameter that may be empty is, a parameter the line assigns, and one the harness's environment defines are not known.
func TestSpecialParametersAreAPieceOfAName(t *testing.T) {
	r := newTeamRig(t, ModeAcceptEdits, nil)
	for _, c := range []struct{ cmd, want, why string }{
		{cmd: `echo x > "$TMPDIR/x-$$.json"`, want: "allow"},
		{cmd: `echo x > "${TMPDIR}/x.${$}.$PPID.$RANDOM.$SRANDOM.$SECONDS.$EPOCHSECONDS.$EPOCHREALTIME.$UID.$EUID.$LINENO.$BASHPID"`, want: "allow"},
		{cmd: `echo x > "$TMPDIR/$$" && echo y > "$TMPDIR/..$$" && cat "$TMPDIR/x$?$#"`, want: "allow"},
		{cmd: `echo x > "$TMPDIR/x$!"`, want: "allow"},
		{cmd: `rm -rf "$TMPDIR/$!"`, want: "ask"},    // no background job: $! is empty, and this is $TMPDIR itself
		{cmd: `echo x > "$TMPDIR/.$!"`, want: "ask"}, // "." when empty
		{cmd: `echo x > "$TMPDIR/..$RANDOM/a"`, want: "ask"},
		{cmd: `echo x > "$TMPDIR/$EPOCHREALTIME"`, want: "ask"},
		{cmd: `RANDOM=../..; echo x > "$TMPDIR/x$RANDOM"`, want: "ask"},
		{cmd: `read RANDOM; echo x > "$TMPDIR/x$RANDOM"`, want: "ask"},
		{cmd: `unset UID; UID=/; echo x > "$TMPDIR/x$UID"`, want: "ask"},
		{cmd: `: ${RANDOM:=/../..}; rm -rf "$TMPDIR/a$RANDOM"`, want: "ask"}, // another sh: an unset RANDOM takes the default
		{cmd: `read RAN""DOM < /dev/null; echo x > "$TMPDIR/a$RANDOM"`, want: "ask"},
		{cmd: `n=RANDOM; read "$n" < /dev/null; echo x > "$TMPDIR/a$RANDOM"`, want: "ask"},
		{cmd: `read P""WD < /dev/null; cat "$PWD/main.go"`, want: "ask"},
		{cmd: `declare -n r=TMPDIR; r=/etc; echo x > "$TMPDIR/a"`, want: "ask"},
		{cmd: `printf '%s\n' "$HOME" > "$TMPDIR/a-$$"`, want: "allow"}, // printf without -v sets nothing
		{cmd: `printf -v TMPDIR /etc; echo x > "$TMPDIR/a"`, want: "ask"},
		{cmd: `cat ~/.ssh/id_rsa$$`, want: "deny"},
		{cmd: `echo x > "$HOME/.aws/x$$"`, want: "deny"},
		{cmd: `echo x > "$TMPDIR/$UNKNOWN"`, want: "ask", why: "$UNKNOWN is only known when the command runs"},
		{cmd: `echo x > "$TMPDIR/$A/$B"`, want: "ask", why: "$A and $B"},
	} {
		d := r.check("be-1", r.wtA, c.cmd)
		if got := outcome(d); got != c.want {
			t.Errorf("%q: %s (%q), want %s", c.cmd, got, d.Reason, c.want)
		}
		if c.why != "" && !strings.Contains(d.Reason, c.why) {
			t.Errorf("%q: reason %q does not say %q", c.cmd, d.Reason, c.why)
		}
		if strings.Contains(d.Reason, "Edit(~/.sleipnir/**) requires approval") {
			t.Errorf("%q: reason %q blames the rule about the state directory", c.cmd, d.Reason)
		}
	}
	t.Setenv("RANDOM", "../..") // a shell other than bash would expand it to that
	r = newTeamRig(t, ModeAcceptEdits, nil)
	if d := r.check("be-1", r.wtA, `echo x > "$TMPDIR/x$RANDOM"`); d.Allow {
		t.Errorf("$RANDOM of the environment: %+v", d)
	}
}

// A question about a path in $TMPDIR whose rest is unknown is about that, not about the rule of the state directory, is never routine
// (a "../" in the unknown part would leave $TMPDIR) and cannot be remembered.
func TestAnUnknownNameInTmpdirSaysSo(t *testing.T) {
	rec := &promptRecorder{answer: func(int, Request) Decision { return Decision{Allow: true, Remember: ScopeSession} }}
	r := newTeamRig(t, ModeAcceptEdits, rec.prompt)
	delegated := 0
	r.e.cfg.Delegate = func(context.Context, Request) (Decision, string) { delegated++; return Decision{}, "" }
	if d := r.check("be-1", r.wtA, `echo x > "$TMPDIR/out-$NAME.json"`); !d.Allow {
		t.Fatalf("answered yes: %+v", d)
	}
	q := rec.requests[0]
	if !strings.Contains(q.Why, `"$TMPDIR/out-$NAME.json" is in $TMPDIR, but $NAME is only known when the command runs`) ||
		!strings.Contains(q.Why, "write to a fixed name in $TMPDIR, or set the variable earlier in the same command") {
		t.Errorf("reason %q", q.Why)
	}
	if delegated != 0 || len(q.RememberRules) != 0 || q.Remembers != "" {
		t.Errorf("routine or rememberable: delegated %d, %+v", delegated, q)
	}
	if got := r.e.Rules(Allow); len(got) != 0 {
		t.Errorf("rules after the answer: %q", got)
	}
}

// export, readonly and declare -x keep a variable known as an assignment does; a new shell (sh -c) sees only the exported ones, and the
// commands of a trap run with what is still true when it fires, in every directory the line may be in by then.
func TestDeclarationsAndTrapsKeepVariablesKnown(t *testing.T) {
	r := newTeamRig(t, ModeAcceptEdits, nil)
	// The new shell of sh -c is itself a program that asks; what it is told to run is judged too, and only an exported variable is
	// known there.
	for cmd, want := range map[string]string{
		`export F=~; bash -c 'cat "$F/.npmrc"'`: "deny", `F=~; export F; sh -c 'cat "$F/.npmrc"'`: "deny",
		`F=~; bash -c 'cat "$F/.npmrc"'`:               "ask", // not exported: the new shell's $F is empty, and the engine does not know it
		`export F=~; env -i bash -c 'cat "$F/.npmrc"'`: "ask", // env -i: the new shell gets no variable
	} {
		if d := r.check("be-1", r.wtA, cmd); outcome(d) != want {
			t.Errorf("%q: %q, want %s", cmd, d.Reason, want)
		}
	}
	for _, c := range []struct{ cmd, want string }{
		{`export F="$TMPDIR/a"; rm -rf "$F"`, "allow"},
		{`readonly F="$TMPDIR/a"; rm -rf "$F"`, "allow"},
		{`declare -x F="$TMPDIR/a"; rm -rf "$F"`, "allow"},
		{`F="$TMPDIR/a"; rm -rf "$F" 2>&1 >/dev/null && echo ok`, "allow"},
		{`export F="$TMPDIR/a" G=$F; rm -rf "$G"`, "ask"}, // G gets F's value from before the export
		{`export F=; rm -rf "$F/x"`, "ask"},
		{`export F="$TMPDIR/a"; F=/etc; rm -rf "$F"`, "ask"},
		{`export -n F="$TMPDIR/a"; rm -rf "$F"`, "ask"},
		{`declare -n F=HOME; rm -rf "$F"`, "ask"},
		{`local F="$TMPDIR/a"; rm -rf "$F"`, "ask"}, // local outside a function fails
		{`F="$TMPDIR/a"; wait -p F; rm -rf "$F"/*`, "ask"},
		{`F="$TMPDIR/a"; echo $((F=5)); rm -rf "$F"`, "ask"},
		{`RANDOM="$TMPDIR/a"; rm -rf "$RANDOM"`, "ask"},
		{`PWD=` + r.f.outside + `; cat "$PWD/secret.txt"`, "ask"},
		{`F="$TMPDIR/a"; trap 'rm -rf "$F"' EXIT; echo hi`, "allow"},
		{`F="$TMPDIR/a"; trap 'rm -rf "$F"' EXIT; F=` + r.f.outside, "ask"},
		{`trap 'rm -rf "$F"' EXIT; F="$TMPDIR/a"`, "ask"},
		{`F="$TMPDIR/a"; trap 'rm -rf "$F"' EXIT; read F`, "ask"},
		{`F="$TMPDIR/a"; trap 'F=/etc' USR1; rm -rf "$F"`, "ask"},
		{`trap 'rm -rf build' EXIT; cd ` + r.f.outside, "ask"},
		{`F="$TMPDIR/a" & rm -rf "$F"`, "ask"},
	} {
		d := r.check("be-1", r.wtA, c.cmd)
		if got := outcome(d); got != c.want {
			t.Errorf("%q: %s (%q), want %s", c.cmd, got, d.Reason, c.want)
		}
	}
}

// "Don't ask again" is offered only when the rules it adds would let the same request through, and an answer that offered nothing
// remembers nothing.
func TestDontAskAgainOnlyWhenItWouldNotAskAgain(t *testing.T) {
	f := newFixture(t)
	rec := &promptRecorder{answer: func(int, Request) Decision { return Decision{Allow: true, Remember: ScopeSession} }}
	plain := f.engine(t, Config{Prompter: rec.prompt})
	strict := f.engine(t, Config{Prompter: rec.prompt, Roles: map[string]RoleProfile{"strict": {Mode: ModeDefault}}, Mode: ModeAcceptEdits})
	for _, c := range []struct {
		cmd, role string
		remembers bool
		tests     bool
	}{
		{cmd: "go test ./...", remembers: true, tests: true},
		{cmd: "./bin/app --port 8080", remembers: true},
		{cmd: `F=main.go; ./bin/app "$F"`, remembers: false},      // a rule with "$F" would allow it whatever F holds
		{cmd: "python3 - <<'PY'\nprint(1)\nPY", remembers: false}, // the program is not in the rule
		{cmd: `npm test > "$OUT"`, remembers: false, tests: true}, // the redirection keeps asking
		{cmd: "./bin/other", role: "strict", remembers: false},    // the role's stricter mode ignores the session's rules
	} {
		before := len(rec.requests)
		e := plain
		if c.role != "" {
			e = strict
		}
		e.Check(bg, Request{Agent: "a1", Role: c.role, Tool: "Bash", Command: c.cmd, Cwd: f.root})
		if len(rec.requests) != before+1 {
			t.Errorf("%q: not asked", c.cmd)
			continue
		}
		q := rec.requests[before]
		if got := len(q.RememberRules) > 0; got != c.remembers {
			t.Errorf("%q: offers to remember %v (%q), want %v", c.cmd, got, q.RememberRules, c.remembers)
		}
		if q.OffersTests != c.tests {
			t.Errorf("%q: offers the tests preset %v, want %v", c.cmd, q.OffersTests, c.tests)
		}
	}
	for _, rule := range append(plain.Rules(Allow), strict.Rules(Allow)...) {
		if strings.Contains(rule, "$") || strings.Contains(rule, "python3") || strings.Contains(rule, "bin/other") {
			t.Errorf("remembered %q, which was not offered", rule)
		}
	}
}

// The names the harness expands itself ($HOME, $TMPDIR, $PWD and the special parameters) are unknown in a line that may set them, by
// any spelling a builtin takes (quotes removed, a name only known when the line runs, a nameref), and known in one that only reads them.
func TestTheHarnessNamesALineMaySetAreUnknown(t *testing.T) {
	for _, c := range []struct {
		cmd  string
		off  []string
		keep []string
	}{
		{cmd: `read RAN""DOM`, off: []string{"RANDOM"}, keep: []string{"TMPDIR"}},
		{cmd: `n=x; read "$n"`, off: []string{"RANDOM", "TMPDIR", "PWD", "HOME"}},
		{cmd: `read P""WD`, off: []string{"PWD"}, keep: []string{"TMPDIR"}},
		{cmd: `declare -n r=TMPDIR`, off: []string{"TMPDIR"}},
		{cmd: `printf -v TMPDIR x`, off: []string{"TMPDIR"}},
		{cmd: `wait -p UID`, off: []string{"UID"}},
		{cmd: `eval x`, off: []string{"PWD", "RANDOM"}},
		{cmd: `: ${RANDOM:=/x}`, off: []string{"RANDOM"}},
		{cmd: `for PWD in /etc; do :; done`, off: []string{"PWD"}},
		{cmd: `printf '%s' "$HOME" > "$TMPDIR/a"`, keep: []string{"HOME", "TMPDIR"}},
		{cmd: `export F="$TMPDIR/x-$$"; echo "${RANDOM}" "${#PWD}"`, off: []string{"PWD"}, keep: []string{"TMPDIR", "RANDOM"}},
		{cmd: `echo "${TMPDIR:-/tmp}"`, keep: []string{"TMPDIR"}},
	} {
		off := harnessOff(c.cmd, shellparse.Parse(c.cmd).Commands)
		for _, n := range c.off {
			if !off[n] {
				t.Errorf("%q: %s is still known", c.cmd, n)
			}
		}
		for _, n := range c.keep {
			if off[n] {
				t.Errorf("%q: %s is unknown", c.cmd, n)
			}
		}
	}
}
