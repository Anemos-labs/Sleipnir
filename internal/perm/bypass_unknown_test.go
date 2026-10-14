package perm

import (
	"context"
	"os"
	"path/filepath"
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

// Quoted text defines no function and runs no arithmetic: a line that writes Go source with printf ('func main() {...}') keeps the
// variables it set known, so its rm -rf "$d" of a scratch directory it named is judged as that directory. (A devops agent's 12-line
// build check was asked about as "a recursive delete of $d, a path that is only known when the command runs" because of the "()" in
// a printf.) A real function definition, or arithmetic in double quotes, still stops the tracking.
func TestQuotedParenthesesKeepTheLinesVariablesKnown(t *testing.T) {
	f := newFixture(t)
	e := f.engine(t, Config{Mode: ModeBypass, Tmp: t.TempDir()})
	for _, c := range []struct{ cmd, want string }{
		{`d="$TMPDIR/mkverify"; rm -rf "$d"; mkdir -p "$d/cmd/raven"; cd "$d"
printf 'package main\n\nimport "fmt"\n\nfunc main() { fmt.Println("raven") }\n' > cmd/raven/main.go`, "allow"},
		{`d="$TMPDIR/x"; echo "f() is fine in quotes"; rm -rf "$d"`, "allow"},
		{`d="$TMPDIR/x"; echo $'a function () here'; rm -rf "$d"`, "allow"},
		{`d="$TMPDIR/x"; f() { d=/; }; f; rm -rf "$d"`, "ask"},   // a function may set d
		{`d="$TMPDIR/x"; echo "$((n=1))"; rm -rf "$d"`, "ask"},   // arithmetic runs inside double quotes
		{`d="$TMPDIR/x"; function g { :; }; rm -rf "$d"`, "ask"}, // a definition with the keyword
	} {
		d := e.Check(context.Background(), Request{Agent: "dv-1", Tool: "Bash", Command: f.expand(c.cmd)})
		if got := outcome(d); got != c.want {
			t.Errorf("%s\n  = %s (%s), want %s", c.cmd, got, d.Reason, c.want)
		}
	}
}

func TestBlankQuoted(t *testing.T) {
	for _, c := range []struct {
		in     string
		double bool
		want   string
	}{
		{`printf 'f() {}' > x`, true, `printf '      ' > x`},
		{`echo "a()" 'b()' c()`, true, `echo "   " '   ' c()`},
		{`echo "a()" 'b()' c()`, false, `echo "a()" '   ' c()`},
		{`echo \'f() x\'`, true, `echo \'f() x\'`},
		{`echo $'it\'s ()' ()`, true, `echo $'        ' ()`},
		{`echo "q\"()" ()`, true, `echo "     " ()`},
	} {
		if got := blankQuoted(c.in, c.double); got != c.want {
			t.Errorf("blankQuoted(%q, %v) = %q, want %q", c.in, c.double, got, c.want)
		}
	}
}

// Two command substitutions have a value fixed by where the command runs: $(pwd) is the directory and $(git rev-parse --show-toplevel)
// the nearest one at or above it that holds a .git. A line that starts with cd "$(git rev-parse --show-toplevel)" (a common first step of
// an agent's test run) is judged in that directory, so its relative paths are known, and the deny rules still see them. Any other
// substitution, or a line that names a GIT_ variable (git may then answer for another tree), stays unknown and asks in bypass mode.
func TestTheRepositoryRootAndTheWorkingDirectoryAreKnownSubstitutions(t *testing.T) {
	f := newFixture(t)
	if err := os.MkdirAll(filepath.Join(f.root, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(f.root, "internal", "web"), 0o755); err != nil {
		t.Fatal(err)
	}
	e := f.engine(t, Config{Mode: ModeBypass, Tmp: t.TempDir(), Deny: []string{"Read(./secrets/**)"}})
	for _, c := range []struct{ cmd, want string }{
		{`cd "$(git rev-parse --show-toplevel)" && go vet ./internal/web/... && go test ./internal/web/... -v 2>&1 | tail -40`, "allow"},
		{`cat "$(git rev-parse --show-toplevel)/internal/web/x.go"`, "allow"},
		{`cd "$(pwd)" && cat web/x.go`, "allow"},
		{`cd "$(git rev-parse --show-toplevel)" && cat secrets/k`, "deny"},
		{`cat "$(git rev-parse --show-toplevel)/secrets/k"`, "deny"},
		{`cd "$(git rev-parse --git-dir)" && cat x`, "ask"},
		{`GIT_DIR=/elsewhere; cd "$(git rev-parse --show-toplevel)" && cat x`, "ask"},
		{`cd "$(git rev-parse --show-toplevel)/$X" && cat x`, "ask"},
	} {
		d := e.Check(context.Background(), Request{Agent: "be-6", Tool: "Bash", Command: f.expand(c.cmd), Cwd: filepath.Join(f.root, "internal")})
		if got := outcome(d); got != c.want {
			t.Errorf("%s\n  = %s (%s), want %s", c.cmd, got, d.Reason, c.want)
		}
	}
}

// A here-document's body is a command's input, not the line's syntax: Go source written with cat <<'EOF' keeps the variables the line
// sets known, so DATA=$TMPDIR/rwdata; rm -rf $DATA after it is judged as that scratch directory (a backend agent's smoke test was asked
// about as "a recursive delete of $DATA"). An unquoted heredoc still expands $((...)), which may assign, and a definition after the body
// is still one.
func TestAHeredocBodyKeepsTheLinesVariablesKnown(t *testing.T) {
	f := newFixture(t)
	e := f.engine(t, Config{Mode: ModeBypass, Tmp: t.TempDir()})
	for _, c := range []struct{ cmd, want string }{
		{"cat > $TMPDIR/smoke.go <<'EOF'\npackage main\n\nfunc main() {\n\tif len(x) > 2 { run() }\n}\nEOF\nDATA=$TMPDIR/rwdata; rm -rf $DATA\ngo run $TMPDIR/smoke.go $DATA", "allow"},
		{"cat > $TMPDIR/a.txt <<-\"END\"\n\tf() is text\n\tEND\nD=$TMPDIR/x; rm -rf $D", "allow"},
		{"cat > $TMPDIR/a.txt <<EOF\nf() and g()\nEOF\nD=$TMPDIR/x; rm -rf $D", "allow"}, // "()" in an unquoted body is text too
		{"cat > $TMPDIR/a.txt <<EOF\nx $((D=1))\nEOF\nD=$TMPDIR/x; rm -rf $D", "ask"},    // arithmetic in an unquoted body runs
		{"cat > $TMPDIR/a.txt <<'EOF'\ntext\nEOF\nD=$TMPDIR/x; g() { D=/; }; g; rm -rf $D", "ask"},
		{"D=$TMPDIR/x; echo $((1<<2)); rm -rf $D", "ask"}, // arithmetic (and no heredoc)
	} {
		d := e.Check(context.Background(), Request{Agent: "be-3", Tool: "Bash", Command: f.expand(c.cmd)})
		if got := outcome(d); got != c.want {
			t.Errorf("%q\n  = %s (%s), want %s", c.cmd, got, d.Reason, c.want)
		}
	}
}

func TestBlankHeredocs(t *testing.T) {
	for _, c := range []struct {
		in         string
		quotedOnly bool
		want       string
	}{
		{"cat <<'EOF'\nf()\nEOF\nx()", false, "cat <<'EOF'\n   \nEOF\nx()"},
		{"cat <<EOF\nf()\nEOF\n", true, "cat <<EOF\nf()\nEOF\n"},
		{"cat <<EOF\nf()\nEOF\n", false, "cat <<EOF\n   \nEOF\n"},
		{"cat <<-E\n\tf()\n\tE\n", false, "cat <<-E\n    \n\tE\n"},
		{"echo $((1<<2)) f()", false, "echo $((1<<2)) f()"},
		{"cat <<< 'f()'", false, "cat <<< 'f()'"},
		{"cat <<EOF\nnever closed f()", false, "cat <<EOF\nnever closed f()"},
	} {
		if got := blankHeredocs(c.in, c.quotedOnly); got != c.want {
			t.Errorf("blankHeredocs(%q, %v) = %q, want %q", c.in, c.quotedOnly, got, c.want)
		}
	}
}

// A substitution that can fail ($(git rev-parse --show-toplevel)) is empty when it does: a word built from it, directly or through a
// variable, is judged as that too, so "$d/proc/self/environ" cannot reach the protected file and "$D"/* cannot reach the root through
// a failure. A .git that is not a repository (no HEAD, a worktree pointer that leads nowhere) makes git fail: the substitution is then
// not known. A "<<" inside quotes is text, not a here-document that could hide what follows it.
func TestAFailedSubstitutionIsJudgedEmpty(t *testing.T) {
	f := newFixture(t)
	e := f.engine(t, Config{Mode: ModeBypass, Tmp: t.TempDir()})
	for _, c := range []struct{ cmd, want string }{
		{`d=$(git rev-parse --show-toplevel); cat "$d/proc/self/environ"`, "deny"},
		{`cat "$(git rev-parse --show-toplevel)/proc/self/environ"`, "deny"},
		{`D=$(git rev-parse --show-toplevel); rm -rf "$D"/*`, "deny"}, // empty, it is /*: the system directories
		{`d=$(git rev-parse --show-toplevel); cat "$d/README.md"`, "allow"},
		{`d=$(pwd); cat "$d/README.md"`, "allow"},
		{`echo "a <<EOF"; d=$TMPDIR/x; f() { d=/; }; f; echo EOF; rm -rf "$d"`, "ask"},
		{"echo $'\\'' '<<EOF'\nd=$TMPDIR/x; f() { d=/; }; f\nEOF\nrm -rf \"$d\"", "ask"}, // an escaped quote inside $'...' does not end it
	} {
		d := e.Check(context.Background(), Request{Agent: "be-6", Tool: "Bash", Command: f.expand(c.cmd)})
		if got := outcome(d); got != c.want {
			t.Errorf("%s\n  = %s (%s), want %s", c.cmd, got, d.Reason, c.want)
		}
	}
	// a .git that is not a repository
	bad := t.TempDir()
	if err := os.WriteFile(filepath.Join(bad, ".git"), []byte("not a pointer\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	x := expansion{}
	if _, ok := (&resolver{}).knownSubstitution("git rev-parse --show-toplevel", filepath.Join(bad), x); ok {
		t.Error("an invalid .git file was taken as a repository")
	}
	if _, ok := (&resolver{}).knownSubstitution("git rev-parse --show-toplevel", f.root, x); !ok {
		t.Error("the fixture's repository was not recognised")
	}
}
