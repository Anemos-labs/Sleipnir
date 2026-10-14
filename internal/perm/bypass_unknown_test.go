package perm

import (
	"context"
	"strings"
	"testing"
)

// Bypass mode still applies the built-in protections and the deny and ask rules, and a file whose name is only known when the command
// runs cannot be checked against them: bypass asks about it, since even a name with a known directory ("docs/$X") may lead out of it
// with a "../". The words that only might be files (an unlisted program's operands, an assignment's value), a cd and a name-only use
// pass. Yolo mode never asks.
func TestBypassAsksAboutAFileOnlyKnownWhenItMayBeGuarded(t *testing.T) {
	f := newFixture(t)
	cfg := Config{Mode: ModeBypass, Tmp: t.TempDir(), Ask: []string{"Edit(./.sleipnir/**)"}, Deny: []string{"Read(./secrets/**)"}}
	e := f.engine(t, cfg)
	for _, c := range []struct{ cmd, want string }{
		{`cat "$U"`, "ask"},                     // may be any file
		{`cat "$PWD/$X"`, "ask"},                // secrets/ is below the project
		{`cat ~/"$X"`, "ask"},                   // ~/.ssh is below home
		{`cat < "$F"`, "ask"},                   // a redirection's file
		{`echo hi > "$F"`, "ask"},               // a redirection's file
		{`printf x > ".sleipnir/$N"`, "ask"},    // the ask rule covers the directory
		{`cat "docs/$X"`, "ask"},                // X may be ../../.ssh/id_rsa
		{`cat "$TMPDIR/$X"`, "ask"},             // and lead out of the scratch directory as well
		{`cd "$X"`, "allow"},                    // cd reads nothing
		{`ls "$X"`, "allow"},                    // names only
		{`node app.js --port "$PORT"`, "allow"}, // an unlisted program's words only might be files
		{`ID=$(curl -s localhost:8099/api/x | python3 -c 'import sys,json;print(json.load(sys.stdin)["id"])'); echo "$ID"`, "allow"},
		{`cd "$(pwd)"; X="$TMPDIR/a.db"; rm -f "$X"`, "allow"}, // the substitution in cd does not make X unknown
		{`D=docs; cat "$D/README.md"`, "allow"},                // a variable the line set
	} {
		d := e.Check(context.Background(), Request{Agent: "be-1", Tool: "Bash", Command: f.expand(c.cmd)})
		if got := outcome(d); got != c.want {
			t.Errorf("%s\n  = %s (%s), want %s", c.cmd, got, d.Reason, c.want)
		}
	}
	cfg.Mode = ModeYolo
	y := f.engine(t, cfg)
	if d := y.Check(context.Background(), Request{Agent: "be-1", Tool: "Bash", Command: `cat "$U"`}); outcome(d) != "allow" {
		t.Errorf("yolo: %s (%s)", outcome(d), d.Reason)
	}
}

// A statement after a command substitution that has closed is not inside it: cd "$(pwd)"; X=1 sets X for what follows.
func TestWithoutClosedSubstitutions(t *testing.T) {
	for in, want := range map[string]string{
		`cd "$(pwd)"; `:          `cd "      "; `,
		"a=`date`; ":             "a=      ; ",
		`x=$(echo $(id -u)); `:   "x=" + strings.Repeat(" ", 16) + "; ",
		`echo "$(cat <<x`:        `echo "$(cat <<x`, // still open: what follows is inside it
		"echo `":                 "echo `",
		`cd x && $(true); (y); `: `cd x &&        ; (y); `,
	} {
		if got := withoutClosedSubstitutions(in); got != want {
			t.Errorf("%q: %q, want %q", in, got, want)
		}
	}
}

// A relative name whose known part ends in a directory is inside that directory: secrets/$X is a file in secrets/, which a deny rule
// covers, not one beside it. filepath.Join dropped the slash, so the known directory was taken to be the one above.
func TestARelativeUnknownNameIsInsideItsKnownDirectory(t *testing.T) {
	f := newFixture(t)
	e := f.engine(t, Config{Mode: ModeDefault, Deny: []string{"Read(./secrets/**)"}})
	d := e.Check(context.Background(), Request{Agent: "be-1", Tool: "Bash", Command: `cat "secrets/$X"`})
	if outcome(d) != "deny" {
		t.Errorf("cat secrets/$X: %s (%s)", outcome(d), d.Reason)
	}
}
