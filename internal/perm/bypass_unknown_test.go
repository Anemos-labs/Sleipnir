package perm

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Bypass asks only about the very dangerous: the high-risk class (sudo, a recursive delete of a broad or unknown path, ...) and a write
// into a directory whose contents steer later sessions (.sleipnir/, .claude/, git hooks). A file whose name is only known when the
// command runs is allowed; when a deny rule of the person's may cover it (any Read rule for a read, any Edit rule for a write), it is
// refused instead, and nothing asks. The words that only might be files (an unlisted program's operands, an assignment's value), a cd
// and a name-only use pass. Yolo mode never asks.
func TestBypassAsksOnlyAboutTheVeryDangerous(t *testing.T) {
	f := newFixture(t)
	ruled := Config{Mode: ModeBypass, Tmp: t.TempDir(), Ask: []string{"Edit(./.sleipnir/**)"}, Deny: []string{"Read(./secrets/**)"}}
	plain := Config{Mode: ModeBypass, Tmp: t.TempDir(), Ask: []string{"Edit(./.sleipnir/**)"}}
	for _, c := range []struct {
		cmd          string
		ruled, plain string
	}{
		{`cat "$U"`, "deny", "allow"},               // may be any file: the read deny rule may cover it
		{`cat "$PWD/$X"`, "deny", "allow"},          // secrets/ is below the project
		{`cat ~/"$X"`, "deny", "allow"},             // and below home, through a ../
		{`cat < "$F"`, "deny", "allow"},             // a redirection's file
		{`echo hi > "$F"`, "allow", "allow"},        // a write: no Edit deny rule
		{`cat "docs/$X"`, "deny", "allow"},          // X may be ../secrets/k
		{`cat "$TMPDIR/$X"`, "deny", "allow"},       // and lead out of the scratch directory as well
		{`printf x > ".sleipnir/$N"`, "ask", "ask"}, // a write into the steering directory
		{`printf x > .sleipnir/config.json`, "ask", "ask"},
		{`rm -rf "$X"`, "ask", "ask"},                    // high risk: an empty X is "" or "/"
		{`sudo ls`, "ask", "ask"},                        // high risk
		{`cd "$X"`, "allow", "allow"},                    // cd reads nothing
		{`ls "$X"`, "allow", "allow"},                    // names only
		{`node app.js --port "$PORT"`, "allow", "allow"}, // an unlisted program's words only might be files
		{`node script.mjs .`, "deny", "allow"},           // nor is "." a tree it writes (the deny rule still sees the tree it may read)
		{`go run . -data-dir "$D"`, "deny", "allow"},     // go run reads its package (a tree, under the deny rule); the rest is the program's
		{`D=$(mktemp -d); go run ./cmd/x -data-dir "$D" > "$D/log"; rm -rf "$D"`, "allow", "allow"}, // mktemp names a new scratch path
		{`ID=$(curl -s localhost:8099/api/x | python3 -c 'import sys,json;print(json.load(sys.stdin)["id"])'); echo "$ID"`, "allow", "allow"},
		{`cd "$(pwd)"; X="$TMPDIR/a.db"; rm -f "$X"`, "allow", "allow"}, // the substitution in cd does not make X unknown
		{`D=docs; cat "$D/README.md"`, "allow", "allow"},                // a variable the line set
		{`go test ravenwatch/internal/store`, "deny", "allow"},          // an import path is read where the module puts it
	} {
		for _, k := range []struct {
			name string
			cfg  Config
			want string
		}{{"with a read deny rule", ruled, c.ruled}, {"without", plain, c.plain}} {
			d := f.engine(t, k.cfg).Check(context.Background(), Request{Agent: "be-1", Tool: "Bash", Command: f.expand(c.cmd)})
			if got := outcome(d); got != k.want {
				t.Errorf("%s (%s)\n  = %s (%s), want %s", c.cmd, k.name, got, d.Reason, k.want)
			}
		}
	}
	ruled.Mode = ModeYolo
	y := f.engine(t, ruled)
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
		{`cd "$(git rev-parse --git-dir)" && cat x`, "deny"}, // unknown: the read deny rule may cover it
		{`GIT_DIR=/elsewhere; cd "$(git rev-parse --show-toplevel)" && cat x`, "deny"},
		{`cd "$(git rev-parse --show-toplevel)/$X" && cat x`, "deny"},
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

// go run reads and builds its package and passes the rest to the program; $(mktemp ...) is a new path in $TMPDIR unless the line moves
// $TMPDIR or the template names a directory; a read-only role in a bypass session reads a name only known when the command runs as the
// session does.
func TestGoRunMktempAndReadOnlyRolesInBypass(t *testing.T) {
	f := newFixture(t)
	roles := map[string]RoleProfile{"reviewer": {Mode: ModePlan}}
	e := f.engine(t, Config{Mode: ModeBypass, Tmp: t.TempDir(), Ask: []string{"Edit(./.sleipnir/**)"}, Roles: roles})
	for _, c := range []struct{ role, cmd, want string }{
		{"", `go run .`, "allow"},
		{"", `go run main.go util.go -mock 3`, "allow"},
		{"", `go run . -out .sleipnir/x`, "ask"}, // a word naming the steering directory may be written there
		{"", `D=$(mktemp -d); rm -rf "$D"`, "allow"},
		{"", `D=$(mktemp -d -t rw.XXXXXX); rm -rf "$D"`, "allow"},
		{"", `D=$(mktemp -d /x/rw.XXXXXX); rm -rf "$D"`, "ask"},
		{"", `TMPDIR=/; D=$(mktemp -d); rm -rf "$D"`, "ask"},
		{"reviewer", `cat "$F"`, "allow"},
		{"reviewer", `echo x > "$F"`, "deny"}, // a read-only role still writes nothing
	} {
		d := e.Check(context.Background(), Request{Agent: "a", Role: c.role, Tool: "Bash", Command: f.expand(c.cmd)})
		if got := outcome(d); got != c.want {
			t.Errorf("%s %s = %s (%s), want %s", c.role, c.cmd, got, d.Reason, c.want)
		}
	}
}
