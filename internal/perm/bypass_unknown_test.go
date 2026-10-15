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
		{`rm -rf "$X"`, "ask", "ask"},                             // high risk: an empty X is "" or "/"
		{`sudo ls`, "ask", "ask"},                                 // high risk
		{"true && for f in x\ndo rm -rf ~; done", "deny", "deny"}, // the next line of a loop after && is its body, not its list
		{`cd "$X"`, "allow", "allow"},                             // cd reads nothing
		{`ls "$X"`, "allow", "allow"},                             // names only
		{`node app.js --port "$PORT"`, "allow", "allow"},          // an unlisted program's words only might be files
		{`node script.mjs .`, "deny", "allow"},                    // nor is "." a tree it writes (the deny rule still sees the tree it may read)
		{`go run . -data-dir "$D"`, "deny", "allow"},              // go run reads its package (a tree, under the deny rule); the rest is the program's
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
		{"D=$TMPDIR/x; echo $((1<<2)); rm -rf $D", "allow"}, // a shift, not a heredoc; and an expansion that names no variable sets none
	} {
		d := e.Check(context.Background(), Request{Agent: "be-3", Tool: "Bash", Command: f.expand(c.cmd)})
		if got := outcome(d); got != c.want {
			t.Errorf("%q\n  = %s (%s), want %s", c.cmd, got, d.Reason, c.want)
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

// A command run in the background assigns in a shell of its own, and the rest of the line keeps the variables it set: D=$(mktemp -d);
// server & PID=$!; ...; rm -rf "$D" deletes the scratch directory (a backend agent's smoke test was asked about as "a recursive delete
// of $D"). What the background command itself assigns is not the line's.
func TestABackgroundJobKeepsTheLinesVariablesKnown(t *testing.T) {
	f := newFixture(t)
	e := f.engine(t, Config{Mode: ModeBypass, Tmp: t.TempDir()})
	for _, c := range []struct{ cmd, want string }{
		{`D=$(mktemp -d); go build -o "$TMPDIR/srv" ./cmd/x && "$TMPDIR/srv" --data "$D" & APP=$!; sleep 2; kill $APP; rm -rf "$D"`, "allow"},
		{`D=$TMPDIR/x; sleep 1 & rm -rf "$D"`, "allow"},
		{`D=$TMPDIR/x; D=/ & rm -rf "$D"`, "ask"}, // the background assignment may or may not have run: D is no longer known
		{`D=$TMPDIR/x; cmd 2>&1 & rm -rf "$D"`, "allow"},
	} {
		d := e.Check(context.Background(), Request{Agent: "be-7", Tool: "Bash", Command: f.expand(c.cmd)})
		if got := outcome(d); got != c.want {
			t.Errorf("%s\n  = %s (%s), want %s", c.cmd, got, d.Reason, c.want)
		}
	}
}

// A case statement is parsed: a line with one keeps what it set (a packaging dry run listed each archive by its kind after
// T="$TMPDIR/t25"; rm -rf "$T", and was asked about as if T were unknown). An assignment or unset in a body runs only when its
// pattern matches, so the variable is no longer known after it.
func TestACaseStatementKeepsTheLinesVariablesKnown(t *testing.T) {
	f := newFixture(t)
	e := f.engine(t, Config{Mode: ModeBypass, Tmp: t.TempDir()})
	for _, c := range []struct{ cmd, want string }{
		{"T=\"$TMPDIR/t25\"; rm -rf \"$T\"; mkdir -p \"$T\"\nsh scripts/dist.sh \"$T\"\n" +
			`for a in "$T"/dist/*; do case "$a" in *.tar.gz) tar -tzf "$a";; *.zip) python3 -c "import zipfile,sys;print(zipfile.ZipFile(sys.argv[1]).namelist())" "$a";; esac; done`, "allow"},
		{`T="$TMPDIR/t"; case "$1" in a) echo "$T";; *) rm -rf "$T";; esac`, "allow"},
		{`T="$TMPDIR/t"; case "$1" in a) T=/;; esac; rm -rf "$T"`, "ask"},
		{`T="$TMPDIR/t"; case "$1" in a) unset T;; esac; rm -rf "$T"/*`, "ask"},
		{`case "$1" in a) T="$TMPDIR/t";; esac; rm -rf "$T"`, "ask"},
		{`T="$TMPDIR/t"; case "$1" in a) rm -rf "$T";;`, "ask"},              // no esac: not parsed, nothing is known
		{`T=/; T="$TMPDIR/t" && case y in *) :;; esac & rm -rf "$T"`, "ask"}, // the second assignment ran in the background
		{`T=/; for T in "$TMPDIR/t"; do :; done | cat; rm -rf "$T"`, "ask"},  // and this loop in the pipe's shell
	} {
		d := e.Check(context.Background(), Request{Agent: "dv-1", Tool: "Bash", Command: f.expand(c.cmd)})
		if got := outcome(d); got != c.want {
			t.Errorf("%s\n  = %s (%s), want %s", c.cmd, got, d.Reason, c.want)
		}
	}
}

// A heredoc that is a program's data (python3 - <<'PY') cannot change this shell, so what it says does not make the line's variables
// unknown: os.environ["TMPDIR"] in a Python script is not an assignment of $TMPDIR, and o["copy_to"]="..." is not an array
// assignment (two devops runs were asked about deleting the scratch directory they had just named). A body a shell runs, a builtin
// reads, or the shell expands still counts.
func TestAProgramsHeredocIsItsData(t *testing.T) {
	f := newFixture(t)
	e := f.engine(t, Config{Mode: ModeBypass, Tmp: t.TempDir()})
	for _, c := range []struct{ cmd, want string }{
		{"SB=\"$TMPDIR/drill\"; rm -rf \"$SB\"; mkdir -p \"$SB/data\"\npython3 - <<'PY'\nimport sqlite3, os\nbase = os.environ[\"TMPDIR\"]\nsqlite3.connect(base + \"/drill/data/x.db\").close()\nPY\nrm -rf \"$SB\"", "allow"},
		{"d=$(mktemp -d); cp packaging/raven.json \"$d/bad.json\"\npython3 - \"$d/bad.json\" <<'EOF'\nimport json,sys\np=sys.argv[1]; o=json.load(open(p)); o[\"copy_to\"]=\"/etc/raven\"; json.dump(o,open(p,\"w\"))\nEOF\nrm -rf \"$d\"", "allow"},
		{"python3 - <<PY\nos.environ[\"TMPDIR\"]\nPY\nrm -rf \"$TMPDIR/x\"", "ask"}, // unquoted: the shell expands the body
		{"source /dev/stdin <<'E'\nTMPDIR=/\nE\nrm -rf \"$TMPDIR\"/*", "ask"},
		{"read TMPDIR <<'E'\n/\nE\nrm -rf \"$TMPDIR\"/*", "ask"},
		{"$PY - <<'E'\nTMPDIR=/\nE\nrm -rf \"$TMPDIR\"/*", "ask"},
		{"sh <<'E'\nTMPDIR=/\nE\nrm -rf \"$TMPDIR\"/*", "ask"},
	} {
		d := e.Check(context.Background(), Request{Agent: "dv-3", Tool: "Bash", Command: f.expand(c.cmd)})
		if got := outcome(d); got != c.want {
			t.Errorf("%q\n  = %s (%s), want %s", c.cmd, got, d.Reason, c.want)
		}
	}
}

// A for loop with no word list walks the positional parameters, which a line can set to anything (set -- $(echo /)): it sets its
// variable to what is only known when the command runs. Written "for X do", with no ";" before the do, its body is commands: it used
// to be read as more words of the loop, so for X do rm -rf ~; done was no command at all.
func TestALoopOverThePositionalParametersSetsItsVariable(t *testing.T) {
	f := newFixture(t)
	e := f.engine(t, Config{Mode: ModeBypass, Tmp: t.TempDir()})
	for _, c := range []struct{ cmd, want string }{
		{`set -- $(echo /); X="$TMPDIR/a"; for X; do :; done; rm -rf "$X"/*`, "ask"},
		{`set -- "$(dirname /a)"; X="$TMPDIR/a"; for X do :; done; rm -rf "$X"/*`, "ask"},
		{"X=\"$TMPDIR/a\"; for X\ndo :; done; rm -rf \"$X\"/*", "ask"},
		{`for X do rm -rf ~; done`, "deny"},
		{`for X do cat ~/.ssh/id_rsa; done`, "deny"},
	} {
		d := e.Check(context.Background(), Request{Agent: "be-1", Tool: "Bash", Command: f.expand(c.cmd)})
		if got := outcome(d); got != c.want {
			t.Errorf("%s\n  = %s (%s), want %s", c.cmd, got, d.Reason, c.want)
		}
	}
}

// A loop runs its commands again, so what its body assigns may be there before any of them: X="$TMPDIR/a"; while :; do rm -rf
// "$X"/*; X=/; done deletes /* the second time round. The names a loop assigns are unknown from its start. A builtin that may set a
// variable makes only the names it can set unknown (a loop's variable, read's names; nothing for wait), not every variable of the
// line: a tester's for i in $(seq 1 200) or wait "$pid" made the scratch directory it then deleted unknown.
func TestALoopsAssignmentsReachItsWholeBody(t *testing.T) {
	f := newFixture(t)
	e := f.engine(t, Config{Mode: ModeBypass, Tmp: t.TempDir()})
	for _, c := range []struct{ cmd, want string }{
		{`X="$TMPDIR/a"; while true; do rm -rf "$X"/*; X=/; done`, "ask"},
		{`X="$TMPDIR/a"; until false; do rm -rf "$X"/*; X=/; done`, "ask"},
		{"X=\"$TMPDIR/a\"\nwhile :; do\n  rm -rf \"$X\"/*\n  X=$(echo /)\ndone", "ask"},
		{`X="$TMPDIR/a"; for i in 1 2; do while :; do rm -rf "$X"/*; break; done; X=/; done`, "ask"},
		{`X="$TMPDIR/a"; while :; do read X; rm -rf "$X"/*; done`, "ask"},
		{`D="$TMPDIR/a"; for i in $(seq 1 3); do sleep 0; done; rm -rf "$D"`, "allow"},
		{`D="$TMPDIR/a"; sleep 1 & p=$!; wait "$p"; rm -rf "$D"`, "allow"},
		{`D="$TMPDIR/a"; read -r x < f; rm -rf "$D"`, "allow"},
		{`D="$TMPDIR/a"; while read -r l; do echo "$l"; done < f; rm -rf "$D"`, "allow"},
		{`D="$TMPDIR/a"; read -r D < f; rm -rf "$D"`, "ask"},
		{`D="$TMPDIR/a"; for D in /; do :; done; rm -rf "$D"`, "ask"},
		{`D="$TMPDIR/a"; read "$v" < f; rm -rf "$D"`, "ask"},
		{`D="$TMPDIR/a"; eval x; rm -rf "$D"`, "ask"},
		{`D="$TMPDIR/a"; let x=1; rm -rf "$D"`, "ask"},
		{`D="$TMPDIR/a"; printf -vD '%s' /; rm -rf "$D"/*`, "ask"}, // printf -v with the name attached
		{`D="$TMPDIR/a"; printf -v D '%s' /; rm -rf "$D"/*`, "ask"},
		{`D="$TMPDIR/a"; printf -v x '%s' 1; rm -rf "$D"`, "allow"},
		{`D="$TMPDIR/a"; printf -v D* '%s' /; rm -rf "$D"/*`, "ask"}, // a glob: a file D in the directory makes it D
		{`D="$TMPDIR/a"; read D? < f; rm -rf "$D"/*`, "ask"},
		{`D="$TMPDIR/a"; read a[1] < f; rm -rf "$D"`, "ask"},
		{`read HOM? < f; rm -rf "$HOME/x"`, "ask"},
		{`D="$TMPDIR/a"; mapfile -C 'D=/;:' -c 1 x < f; rm -rf "$D"/*`, "ask"}, // the callback runs text in this shell
		{`readarray -t -C 'rm -rf' -c 1 x < f`, "ask"},                         // and gets the line it read as a word: rm -rf 0 LINE
		{`D="$TMPDIR/a"; mapfile -t x < f; rm -rf "$D"`, "allow"},
	} {
		d := e.Check(context.Background(), Request{Agent: "ts-3", Tool: "Bash", Command: f.expand(c.cmd)})
		if got := outcome(d); got != c.want {
			t.Errorf("%s\n  = %s (%s), want %s", c.cmd, got, d.Reason, c.want)
		}
	}
}

// An arithmetic expansion assigns only integers, and only to the names it reaches: those of its text, and those named by the values
// of the variables it reads, which bash evaluates as expressions too. When every name it reads holds an integer (a number, another
// expansion, $(date +%s), from an unconditional assignment before it), only the names of its text become unknown; a test script's
// counter (n=$((n+1))) or timer ($(( $(date +%s) - S0 ))) no longer makes the scratch directory it deletes unknown. A name that may
// hold anything (x='D=0', read, the environment, $1), a substitution or ${...} in the expression, or one the parser cannot place
// (a case subject), still makes every variable unknown.
func TestArithmeticAssignsOnlyTheNamesItReaches(t *testing.T) {
	f := newFixture(t)
	tmp := filepath.Join(f.home, ".sleipnir", "sessions", "s1", "tmp")
	if err := os.MkdirAll(tmp, 0o755); err != nil {
		t.Fatal(err)
	}
	e := f.engine(t, Config{Mode: ModeBypass, Tmp: tmp, Ask: []string{"Edit(~/.sleipnir/**)"}})
	for _, c := range []struct{ cmd, want string }{
		{"SD=\"$TMPDIR/t67\"; DD=\"$SD/rw\"; rm -rf \"$DD\"; mkdir -p \"$DD\"\nn=1\nfor ts in a b c; do printf x > \"$DD/$ts\"; n=$((n+1)); done\nrm -rf \"$DD\"", "allow"},
		{"UP=\"$TMPDIR/up\"; S0=$(date +%s); S=\"$UP/bundle-crash\"; rm -rf \"$S\"; echo \"took=$(( $(date +%s) - S0 ))s\"; rm -rf \"$S\"", "allow"},
		{`D="$TMPDIR/x"; n=1; echo $((RANDOM % 5 + n)); rm -rf "$D"`, "allow"},
		{`D="$TMPDIR/x"; : $((D=0)); rm -rf "$D"/*`, "ask"},
		{`x='D=0'; D="$TMPDIR/x"; : $((x)); rm -rf "$D"/*`, "ask"},
		{`read v; D="$TMPDIR/x"; : $((v)); rm -rf "$D"`, "ask"},
		{`D="$TMPDIR/x"; : $(( $(cat f) )); rm -rf "$D"`, "ask"},
		{`D="$TMPDIR/x"; : $((n+1)); rm -rf "$D"`, "ask"}, // n is the environment's
		{`D="$TMPDIR/x"; n=1; : $(( $1 + n )); rm -rf "$D"`, "ask"},
		{`D="$TMPDIR/x"; : $(( $* )); rm -rf "$D"/*`, "ask"}, // the positional parameters may be D=0
		{`D="$TMPDIR/x"; : $(( $@ )); rm -rf "$D"/*`, "ask"},
		{`D="$TMPDIR/x"; : $(( $- )); rm -rf "$D"/*`, "ask"},
		{`bash -c 'D="$TMPDIR/x"; : $(( $* )); rm -rf "$D"/*' _ D=0`, "ask"},
		{`D="$TMPDIR/x"; n=1; : $(( ${n} )); rm -rf "$D"`, "ask"},
		{"D=\"$TMPDIR/x\"; n=1; cat <<EOF\n$((D=n))\nEOF\nrm -rf \"$D\"/*", "ask"},
		{`D="$TMPDIR/x"; case $((D=0)) in *) :;; esac; rm -rf "$D"/*`, "ask"},
		{`D="$TMPDIR/x"; n=1; while :; do rm -rf "$D"/*; : $((D=n)); done`, "ask"},
		{`D="$TMPDIR/x"; if a; then n=1; fi; : $((n)); rm -rf "$D"`, "ask"}, // n may still be the environment's
	} {
		d := e.Check(context.Background(), Request{Agent: "ts-3", Tool: "Bash", Command: f.expand(c.cmd)})
		if got := outcome(d); got != c.want {
			t.Errorf("%q\n  = %s (%s), want %s", c.cmd, got, d.Reason, c.want)
		}
	}
}

// A line of more commands than the engine judges (maxUnits) is checked up to that bound, and what follows is not judged at all: no
// mode allows it. Bypass asks, yolo (which never asks) refuses; a loop that assigns past the bound counts as well.
func TestALineTooLongToCheckIsNeverAllowed(t *testing.T) {
	f := newFixture(t)
	pad := strings.Repeat(":; ", maxUnits+1)
	for _, c := range []struct {
		mode Mode
		cmd  string
		want string
	}{
		{ModeBypass, pad + "rm -rf ~", "ask"},
		{ModeBypass, pad + "cat ~/.ssh/id_rsa", "ask"},
		{ModeBypass, `X="$TMPDIR/a"; while true; do rm -rf "$X"/*; ` + pad + `X=/; done`, "ask"},
		{ModeYolo, pad + "rm -rf ~", "deny"},
		{ModeDefault, pad + "ls", "ask"},
		{ModeBypass, strings.Repeat(":; ", 10) + "ls", "allow"},
	} {
		d := f.engine(t, Config{Mode: c.mode, Tmp: t.TempDir()}).Check(context.Background(), Request{Agent: "be-1", Tool: "Bash", Command: c.cmd})
		if got := outcome(d); got != c.want {
			t.Errorf("%s %.40q...: %s (%s), want %s", c.mode, c.cmd[len(c.cmd)-40:], got, d.Reason, c.want)
		}
	}
}

// A "$" that starts no expansion is the character: pgrep -af "$INSTALL/raven($| )" names a path in $TMPDIR, not one with a part
// only known when the command runs (a tester was asked about it, as a write that might leave $TMPDIR for ~/.sleipnir, where a
// session's scratch directory lives). A "$" before a name, a brace, a parenthesis, a bracket or a special parameter still expands.
func TestADollarThatStartsNoExpansionIsTheCharacter(t *testing.T) {
	f := newFixture(t)
	tmp := filepath.Join(f.home, ".sleipnir", "sessions", "s1", "tmp")
	if err := os.MkdirAll(tmp, 0o755); err != nil {
		t.Fatal(err)
	}
	e := f.engine(t, Config{Mode: ModeBypass, Tmp: tmp, Ask: []string{"Edit(~/.sleipnir/**)"}})
	for _, c := range []struct{ cmd, want string }{
		{`INSTALL="$TMPDIR/opt/ravenwatch"; pgrep -af "$INSTALL/raven($| )" || true`, "allow"},
		{`node tool.js "$TMPDIR/a$"`, "allow"},
		{`node tool.js "$TMPDIR/a$/b$%c$ d"`, "allow"},
		{`node tool.js "$TMPDIR/$*"`, "ask"},
		{`node tool.js "$TMPDIR/$@"`, "ask"},
		{`node tool.js "$TMPDIR/${X}"`, "ask"},
		{`node tool.js "$TMPDIR/$[1]"`, "ask"},
		{`node tool.js "$TMPDIR/$(id -un)"`, "ask"},
		{`node tool.js "$TMPDIR/$-"`, "ask"},
	} {
		d := e.Check(context.Background(), Request{Agent: "ts-4", Tool: "Bash", Command: f.expand(c.cmd)})
		if got := outcome(d); got != c.want {
			t.Errorf("%s\n  = %s (%s), want %s", c.cmd, got, d.Reason, c.want)
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
		{`echo \$'x'; d=$TMPDIR/x; f() { d=/; }; f; rm -rf "$d"`, "ask"},                 // \$' opens a plain quote; the definition is real
		{`echo $$'x'; d=$TMPDIR/x; f() { d=/; }; f; rm -rf "$d"`, "ask"},                 // so does $$'
		{"d=\"$TMPDIR/x\"; cat <<EOF\n'\nEOF\necho $((d=1))\nrm -rf $d", "ask"},          // a quote in an unquoted body hides no arithmetic
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

// $(mktemp) can fail, and is then empty: rm -rf "$D" after D=$(mktemp -d) deletes nothing in that case, while rm -rf "$D"/* would be
// rm -rf /*, which the system directories' protection refuses.
func TestAFailedMktempIsJudgedEmpty(t *testing.T) {
	f := newFixture(t)
	e := f.engine(t, Config{Mode: ModeBypass, Tmp: t.TempDir()})
	for _, c := range []struct{ cmd, want string }{
		{`D=$(mktemp -d); rm -rf "$D"`, "allow"},
		{`D=$(mktemp -d); rm -rf "$D"/*`, "deny"},
		{`D=$(mktemp -d); cat "$D/proc/self/environ"`, "deny"},
	} {
		d := e.Check(context.Background(), Request{Agent: "be-7", Tool: "Bash", Command: f.expand(c.cmd)})
		if got := outcome(d); got != c.want {
			t.Errorf("%s\n  = %s (%s), want %s", c.cmd, got, d.Reason, c.want)
		}
	}
}

// Bash evaluates arithmetic, which may assign, beyond $((...)): in [[ ]] comparisons, array subscripts, substring offsets and the
// legacy $[...]. Each makes the line's variables unknown, so rm -rf "$d" after one asks; ${PORT:-8080} and [[ -f x ]] do not.
func TestOtherArithmeticContextsMakeTheLinesVariablesUnknown(t *testing.T) {
	f := newFixture(t)
	e := f.engine(t, Config{Mode: ModeBypass, Tmp: t.TempDir()})
	for _, c := range []struct{ cmd, want string }{
		{`d="$TMPDIR/x"; [[ 1 -eq d=5 ]]; rm -rf "$d"`, "ask"},
		{`d="$TMPDIR/x"; a[d=5]=1; rm -rf "$d"`, "ask"},
		{`d="$TMPDIR/x"; echo ${a[d=5]}; rm -rf "$d"`, "ask"},
		{`d="$TMPDIR/x"; echo ${x:d=5}; rm -rf "$d"`, "ask"},
		{`d="$TMPDIR/x"; echo $[d=5]; rm -rf "$d"`, "ask"},
		{`d="$TMPDIR/x"; echo "${PORT:-8080}"; rm -rf "$d"`, "allow"},
		{`d="$TMPDIR/x"; [[ -f x ]] && echo y; rm -rf "$d"`, "allow"},
	} {
		d := e.Check(context.Background(), Request{Agent: "be-1", Tool: "Bash", Command: f.expand(c.cmd)})
		if got := outcome(d); got != c.want {
			t.Errorf("%s\n  = %s (%s), want %s", c.cmd, got, d.Reason, c.want)
		}
	}
}
