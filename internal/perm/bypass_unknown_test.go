package perm

import (
	"context"
	"os"
	"path/filepath"
	"slices"
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

// A file whose name is only known when the command runs, and that a deny rule may cover, is refused in every mode that bypass refuses
// it in: plan, default and accept-edits too, before they ask about the command or an allow rule (the tests preset's go test) lets it
// through. A deny rule beats every question and every allow rule, and a person asked about "$F" cannot see what it holds either. The
// modes stay ordered (FuzzCheckShell): a looser mode never refuses what a stricter one asks about or allows. The first rows are inputs
// the fuzzer found: the shell's name ($0), a positional parameter ($1 followed by digits), an unknown variable and an unparsed
// substitution. The words that only might be files, a cd and a name-only use are not refused, and yolo mode lets such a name pass.
func TestAnUnknownNameADenyRuleMayCoverIsRefusedInEveryMode(t *testing.T) {
	f := newFixture(t)
	modes := append(slices.Clone(modesByRestriction), ModeYolo)
	engines := func(deny string) map[Mode]*Engine {
		es := map[Mode]*Engine{}
		for _, m := range modes {
			es[m] = f.engine(t, Config{Mode: m, Allow: []string{"Bash(make:*)"}, Deny: []string{deny}})
		}
		return es
	}
	reads, writes := engines("Read(secrets/**)"), engines("Edit(secrets/**)")
	for _, c := range []struct {
		req  rq
		edit bool   // judged under the Edit deny rule instead of the Read one
		want string // plan, default, accept-edits, bypass, yolo
	}{
		{req: bash("cat $00"), want: "deny deny deny deny allow"}, // $0, then a 0
		{req: bash("$0/0"), want: "deny deny deny deny allow"},    // running a file reads it
		{req: bash("cat /$1000000000"), want: "deny deny deny deny allow"},
		{req: bash("cat $A0"), want: "deny deny deny deny allow"},
		{req: bash("/`"), want: "deny deny deny deny allow"}, // an unterminated substitution
		{req: bash(`cat "$F"`), want: "deny deny deny deny allow"},
		{req: bash(`cat < "$F"`), want: "deny deny deny deny allow"},
		{req: bash(`cat "docs/$X"`), want: "deny deny deny deny allow"},  // X may be ../secrets/k
		{req: bash(`go test "$PKG"`), want: "deny deny deny deny allow"}, // accept-edits allowed it by the tests preset
		{req: read("$F"), want: "deny deny deny deny allow"},
		{req: bash(`echo x > "$F"`), edit: true, want: "deny deny deny deny allow"},
		{req: write("$F"), edit: true, want: "deny deny deny deny allow"},
		{req: bash(`echo x > "$F"`), want: "deny ask ask allow allow"},       // a write, and the rule is about reads
		{req: bash(`cat "$F"`), edit: true, want: "ask ask ask allow allow"}, // a read, and the rule is about edits
		{req: bash(`ls "$F"`), want: "ask ask ask allow allow"},              // names only
		{req: bash(`cd "$F"`), want: "ask ask ask allow allow"},              // cd reads nothing
		{req: bash(`node app.js "$F"`), want: "deny ask ask allow allow"},    // an unlisted program's words only might be files
		{req: bash(`FOO="$F" ls`), want: "ask ask ask allow allow"},          // and so may an assignment's value
	} {
		es := reads
		if c.edit {
			es = writes
		}
		want := strings.Fields(c.want)
		got := map[Mode]string{}
		for i, m := range modes {
			r := f.request(c.req)
			r.Agent = "be-1"
			d := es[m].Check(context.Background(), r)
			if got[m] = outcome(d); got[m] != want[i] {
				t.Errorf("%s %q%v (edit rule %v) in %s mode = %s (%s), want %s", c.req.tool, c.req.cmd, c.req.paths, c.edit, m, got[m], d.Reason, want[i])
			}
			if got[m] == "deny" && m != ModePlan && !strings.Contains(d.Reason, "only known when the command runs") {
				t.Errorf("%s %q%v in %s mode is refused for another reason: %s", c.req.tool, c.req.cmd, c.req.paths, m, d.Reason)
			}
		}
		for i := 0; i+1 < len(modes); i++ {
			if a, b := modes[i], modes[i+1]; severity[got[a]] < severity[got[b]] {
				t.Errorf("%s %q%v: %s gives %s but the stricter %s gives %s", c.req.tool, c.req.cmd, c.req.paths, b, got[b], a, got[a])
			}
		}
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

// go run reads and builds its package and passes the rest to the program; $(mktemp ...) is a new path in $TMPDIR, or in the directory
// its template names, unless the line moves $TMPDIR; a read-only role in a bypass session reads a name only known when the command runs
// as the session does.
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
		{"", `D=$(mktemp -d /x/rw.XXXXXX); rm -rf "$D"`, "allow"}, // a new directory in /x
		{"", `D=$(mktemp -d /x/rw.$N.XXXXXX); rm -rf "$D"`, "ask"},
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

// Each link of an && chain runs only when the one before it succeeded, so an assignment in a link has been made whenever a later link
// runs: an unattended run kept stopping on cd <project> && P="$TMPDIR/ffprofile" && rm -rf "$P" && firefox -profile "$P" ..., asked
// about "a recursive delete of $P". Past the chain (after ;, a newline or &), or behind an || that may skip the assignment, the name is
// unknown, as is one assigned where the shell may not keep it (a pipeline, a subshell, a group, a branch, the background) or to a value
// the engine cannot be sure of.
func TestAnAndChainKnowsWhatItsLinksAssigned(t *testing.T) {
	f := newFixture(t)
	e := f.engine(t, Config{Mode: ModeBypass, Tmp: t.TempDir()})
	for _, c := range []struct{ cmd, want string }{
		{`cd {root} && P="$TMPDIR/ffprofile" && rm -rf "$P" && firefox -profile "$P" --screenshot "$TMPDIR/f.png" --window-size=1280,800 "file://{root}/index.html"`, "allow"},
		{`cd x && D="$TMPDIR/d" && rm -rf "$D"`, "allow"},
		{`D="$TMPDIR/d" && mkdir -p "$D" && rm -rf "$D"`, "allow"},
		{`cd x && D="$TMPDIR/d" && E="$D/e" && rm -rf "$E"`, "allow"},           // a value built from one the chain set
		{`cd x && export D="$TMPDIR/d" && sh -c 'rm -rf "$D"'`, "allow"},        // exported in the chain: the new shell starts with it
		{`cd x && D="$TMPDIR/d" && ls "$D" | head && rm -rf "$D"`, "allow"},     // a pipeline later in the chain does not end it
		{`cd x && D="$TMPDIR/d" && { rm -rf "$D"; } && (rm -rf "$D")`, "allow"}, // nor do a group and a subshell, which see D
		{`cd x && D=$(mktemp -d) && rm -rf "$D"`, "allow"},                      // a new scratch path
		{`cd x && D="$TMPDIR/d" && echo "$(rm -rf "$D")"`, "allow"},             // a later link's substitution
		{`cd x && D=$(mktemp -d) && rm -rf "$D"/*`, "deny"},                     // empty when mktemp fails: /*
		{`cd x && D=/ && rm -rf "$D"`, "deny"},                                  // known, and the root
		{`cd x && F="{home}/.ssh/id_rsa" && cat "$F"`, "deny"},                  // known, and protected
		{`D="$TMPDIR/d" && false; rm -rf "$D"`, "allow"},                        // the first link runs whenever its list does
		{`cd x && D="$TMPDIR/d"; rm -rf "$D"`, "ask"},                           // after ;, the chain may have stopped before D=
		{"cd x && D=\"$TMPDIR/d\"\nrm -rf \"$D\"", "ask"},
		{`cd x && D="$TMPDIR/d" && sleep 1 & rm -rf "$D"`, "ask"},
		{`false || D="$TMPDIR/d" && rm -rf "$D"`, "ask"}, // false succeeding skips D= and still runs rm
		{`a && D="$TMPDIR/d" || rm -rf "$D"`, "ask"},
		{`if c; then D="$TMPDIR/d"; fi && rm -rf "$D"`, "ask"},
		{`(D="$TMPDIR/d") && rm -rf "$D"`, "ask"},
		{`D="$TMPDIR/d" | true && rm -rf "$D"`, "ask"},
		{`{ D="$TMPDIR/d"; } && rm -rf "$D"`, "ask"},
		{`cd x && D="$TMPDIR/d" && sleep 1 && rm -rf "$D" &`, "ask"},             // the whole list runs in the background
		{`true && D="$TMPDIR/d" && { sleep 1 & wait; } && rm -rf "$D"`, "allow"}, // a background job inside a link: the chain goes on
		{`true && D="$TMPDIR/d" && if true; then sleep 1 & fi && rm -rf "$D"`, "allow"},
		{`true && D="$TMPDIR/d" && { D=/ & wait; } && rm -rf "$D"`, "ask"}, // an assignment in it may or may not have run
		{`a && D="$TMPDIR/d" & rm -rf "$D"`, "ask"},
		{`false || D="$TMPDIR/d" && { sleep 1 & wait; } && rm -rf "$D"`, "ask"},
		{`a || true && D="$TMPDIR/d" && { sleep 1 & wait; } && rm -rf "$D"`, "ask"},
		{`D=/; cd x && D="$TMPDIR/d" && rm -rf "$D"`, "ask"},      // two values
		{`cd x && D="$TMPDIR/d" && D=/ && rm -rf "$D"`, "ask"},    // two values in the chain
		{`cd x && D="$TMPDIR/d" && read D && rm -rf "$D"`, "ask"}, // a builtin may set it
		{`cd x && D="$TMPDIR/d" && for D in /; do :; done && rm -rf "$D"`, "ask"},
		{`cd x && D="$TMPDIR/d" && sh -c 'rm -rf "$D"'`, "ask"},     // not exported: the new shell does not have it
		{`cd x && export D="$TMPDIR/d" E && rm -rf "$D"`, "ask"},    // a declaration that also names E
		{`cd x && D="$TMPDIR/d" && trap 'rm -rf "$D"' EXIT`, "ask"}, // the trap runs after the chain, which may have stopped
		{`cd x && X=$(rm -rf "$D") D="$TMPDIR/d" && :`, "ask"},      // the link's own substitution runs before it assigns D
		{`D="$TMPDIR/a"; cd x && D=/ X=$(rm -rf "$D")`, "ask"},
		{`cd x && D=$(cat name) && rm -rf "$D"`, "ask"},                 // a value only known when the line runs
		{`D=$(git rev-parse --show-toplevel) && rm -rf "$D"/*`, "deny"}, // empty when git fails: /*
		{`true && D=$(git rev-parse --show-toplevel) && rm -rf "$D"/*`, "deny"},
		{`cd {root} && D=$(git rev-parse --show-toplevel) && rm -rf "$D"/*`, "deny"},
		{`cd x && D=$(git rev-parse --show-toplevel) && rm -rf "$D"/*`, "deny"}, // the chain runs on in x, inside the same repository
		{`cd x; D=$(git rev-parse --show-toplevel) && rm -rf "$D"/*`, "ask"},    // x or the start directory
	} {
		d := e.Check(context.Background(), Request{Agent: "be-7", Tool: "Bash", Command: f.expand(c.cmd)})
		if got := outcome(d); got != c.want {
			t.Errorf("%s\n  = %s (%s), want %s", c.cmd, got, d.Reason, c.want)
		}
	}
}

// The substitutions of a statement run while bash expands it, before it has made all its assignments: in X=$(rm -rf "$D") D=x, rm
// removes the D from before the line, and a declaration (export, declare, readonly) expands all its words before it assigns any. Such
// a command is judged with what the statement had not yet assigned or exported as unknown; an assignment to the left of it, one an
// earlier statement made, and the substitutions of the statement's redirections and heredocs, which run after its assignments, see
// the new values. Where the two views differ, the stricter verdict stands: a protected read stays refused.
func TestAStatementsSubstitutionsRunBeforeItsLaterAssignments(t *testing.T) {
	f := newFixture(t)
	e := f.engine(t, Config{Mode: ModeBypass, Tmp: t.TempDir()})
	for _, c := range []struct{ cmd, want string }{
		{`X=$(rm -rf "$D") D="$TMPDIR/d"`, "ask"},
		{`export X=$(rm -rf "$D") D="$TMPDIR/d"`, "ask"},
		{`declare -x X=$(rm -rf "$D") D="$TMPDIR/d"`, "ask"},
		{`readonly X=$(rm -rf "$D") D="$TMPDIR/d"`, "ask"},
		{`export D="$TMPDIR/d" X=$(rm -rf "$D")`, "ask"}, // a declaration expands X's value before it assigns D
		{`X=$(rm -rf "$D") D="$TMPDIR/d" Y=$(rm -rf "$D")`, "ask"},
		{`X=$(echo $(rm -rf "$D")) D="$TMPDIR/d"`, "ask"},      // a substitution inside one
		{`X=$(trap "rm -rf $D" EXIT) D="$TMPDIR/d"`, "ask"},    // a trap of the substitution's shell, which ends first
		{`export X=$(sh -c "rm -rf $D") D="$TMPDIR/d"`, "ask"}, // a new shell started before the export
		{`export D="$TMPDIR/d" X=$(sh -c "rm -rf $D")`, "ask"},
		{`D="$TMPDIR/d"; export D X=$(sh -c 'rm -rf "$D"')`, "ask"}, // D is exported only after X's value is expanded
		// and only after a trap set in X's substitution has fired
		{`D="$TMPDIR/d"; export D X=$(trap 'sh -c '"'"'rm -rf "$D"'"'"'' EXIT)`, "ask"},
		{`D="$TMPDIR/d"; export D X=$(trap "sh -c 'rm -rf \"\$D\"'" EXIT)`, "ask"},
		{`D="$TMPDIR/d"; export D X=$(trap "rm -rf $D" EXIT)`, "allow"}, // no new shell: the trap's own D
		{`D="$TMPDIR/d"; export D; X=$(trap 'sh -c '"'"'rm -rf "$D"'"'"'' EXIT)`, "allow"},
		{`cd x && X=$(rm -rf "$D") D="$TMPDIR/d" && :`, "ask"}, // a link of an && chain
		{`D="$TMPDIR/d"; X=$(rm -rf "$D")`, "allow"},           // an earlier statement assigned D
		{`D="$TMPDIR/d" X=$(rm -rf "$D")`, "allow"},            // the assignment to its left is made first
		{`D="$TMPDIR/d" X=$(rm -rf "$D") Y=$(rm -rf "$D")`, "allow"},
		{`D="$TMPDIR/d" X=$(trap "rm -rf $D" EXIT)`, "allow"},
		{"D=\"$TMPDIR/d\" <<EOF\n$(rm -rf \"$D\")\nEOF", "allow"},  // a heredoc is expanded after the assignments
		{`D="$TMPDIR/d"; X=$(rm -rf "$D") D="$TMPDIR/d"`, "allow"}, // the same value before and after
		{`export D="$TMPDIR/d"; X=$(sh -c 'rm -rf "$D"')`, "allow"},
		{`D="$TMPDIR/d"; export D; X=$(sh -c 'rm -rf "$D"')`, "allow"},
		{`D="$TMPDIR/d"; case $(rm -rf "$D") in *) ;; esac`, "allow"}, // a later statement's substitution
		{`cd x && D="$TMPDIR/d" X=$(rm -rf "$D") && rm -rf "$D"`, "allow"},
		{`D="{home}/.ssh/id_rsa" X=$(cat "$D")`, "deny"}, // cat reads the key
		{`X=$(cat "$D") D="{home}/.ssh/id_rsa"`, "deny"}, // the stricter of the two views
		{`D=/ X=$(rm -rf "$D")`, "deny"},
	} {
		d := e.Check(context.Background(), Request{Agent: "be-7", Tool: "Bash", Command: f.expand(c.cmd)})
		if got := outcome(d); got != c.want {
			t.Errorf("%q\n  = %s (%s), want %s", c.cmd, got, d.Reason, c.want)
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

// A command that prints one name token (date +%Y%m%dT%H%M%SZ, seq, id -u, nproc, uuidgen) gives a piece of a file name, as $$ does:
// "$D/rw.$ts.tar.gz" with ts=$(date ...) is a file in $D, and so is "$D/pad$i.txt" in a loop over $(seq 1 800) (testers' backup
// drills were asked about both). It is judged empty too, as the command's failure leaves it, so rm -rf "$HOME/$ts" is still refused.
// A format that prints a "/" (%D), a dot alone, or any other command's output is still only known when the line runs.
func TestATokenSubstitutionIsAPieceOfAName(t *testing.T) {
	f := newFixture(t)
	tmp := filepath.Join(f.home, ".sleipnir", "sessions", "s1", "tmp")
	if err := os.MkdirAll(tmp, 0o755); err != nil {
		t.Fatal(err)
	}
	e := f.engine(t, Config{Mode: ModeBypass, Tmp: tmp, Ask: []string{"Edit(~/.sleipnir/**)"}})
	for _, c := range []struct{ cmd, want string }{
		{`D="$TMPDIR/x"; for i in $(seq 1 800); do printf x > "$D/pad$i.txt"; done`, "allow"},
		{`D="$TMPDIR/x"; ts="$(date -u '+%Y%m%dT%H%M%SZ')"; printf x > "$D/rw.$ts.tar.gz"`, "allow"},
		{`D="$TMPDIR/x"; ts="$(date -u -d "+$off sec" '+%Y%m%dT%H%M%SZ')"; printf x > "$D/rw.$ts.tar.gz"`, "allow"},
		{`D="$TMPDIR/x"; printf x > "$D/log-$(date +%s).txt"`, "allow"},
		{`D="$TMPDIR/x"; ts="$(date -u +%s)"; rm -rf "$D/$ts"`, "allow"},
		{`D="$TMPDIR/x"; u=$(id -u); printf x > "$D/run-$u-$(nproc).log"`, "allow"},
		{`ts="$(date +%s)"; rm -rf "$HOME/$ts"`, "deny"},          // the date may fail: rm -rf "$HOME/"
		{`D="$TMPDIR/x"; printf x > "$D/$(date +%D).txt"`, "ask"}, // %D prints m/d/y
		{`D="$TMPDIR/x"; printf x > "$D/$(date +.)"`, "ask"},
		{`D="$TMPDIR/x"; ts="$(cat f)"; printf x > "$D/$ts"`, "ask"},
		{`D="$TMPDIR/x"; ts="$(date +$FMT)"; printf x > "$D/$ts"`, "ask"},
		{`D="$TMPDIR/x"; for i in $(seq 1 $N); do printf x > "$D/$i"; done`, "ask"},
		{`TZ='<a/b>0'; printf x > "$TMPDIR/y$(date +%Z)"`, "ask"}, // %Z is TZ's name, here a/b: no name token
		{`ts='$(date +%s)'; printf x > "$TMPDIR/$ts"`, "ask"},     // single quotes: no substitution, text the engine does not track (as before)
	} {
		d := e.Check(context.Background(), Request{Agent: "ts-3", Tool: "Bash", Command: f.expand(c.cmd)})
		if got := outcome(d); got != c.want {
			t.Errorf("%s\n  = %s (%s), want %s", c.cmd, got, d.Reason, c.want)
		}
	}
}

// A cd to a directory only known when the line runs (cd "$(git rev-parse --show-toplevel 2>/dev/null || pwd)", the first line of
// many a script) makes relative paths unknown, not full ones: "$TMPDIR/x", and a variable set to it, still name that path. A relative
// path, and a value that names the directory ($PWD, $(pwd)), are still only known when the line runs.
func TestAnUnknownDirectoryKeepsFullPathsKnown(t *testing.T) {
	f := newFixture(t)
	e := f.engine(t, Config{Mode: ModeBypass, Tmp: t.TempDir()})
	for _, c := range []struct{ cmd, want string }{
		{"cd \"$(git rev-parse --show-toplevel 2>/dev/null || pwd)\"\nT=\"$TMPDIR/bk\"; rm -rf \"$T\"", "allow"},
		{`cd "$X"; rm -rf "$TMPDIR/bk"`, "allow"},
		{`cd "$X"; T="$TMPDIR/bk"; rm -rf "$T"`, "allow"},
		{`cd "$X"; rm -rf build`, "ask"},
		{`cd "$X"; D=build; rm -rf "$D"`, "ask"},
		{`cd "$X"; D="$PWD/x"; rm -rf "$D"`, "ask"},
		{`cd "$X"; D="$(pwd)/x"; rm -rf "$D"`, "ask"},
		{`cd "$X"; cat ~/.ssh/id_rsa`, "deny"},
		{`cd "$X"; rm -rf "$HOME"`, "deny"},
	} {
		d := e.Check(context.Background(), Request{Agent: "dv-3", Tool: "Bash", Command: f.expand(c.cmd)})
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
		{`D=$(mktemp -d); rm -rf "$D"*`, "ask"}, // *: the working directory's contents
		{`ts=$(date +%s); rm -rf "$ts"*`, "ask"},
	} {
		d := e.Check(context.Background(), Request{Agent: "be-7", Tool: "Bash", Command: f.expand(c.cmd)})
		if got := outcome(d); got != c.want {
			t.Errorf("%s\n  = %s (%s), want %s", c.cmd, got, d.Reason, c.want)
		}
	}
}

// A variable set from $(mktemp -d "$TMPDIR/x.XXXX") names a new directory in $TMPDIR, as one set from $(mktemp -d) does: a team's
// smoke test built a server into one, ran it with its data and logs there and removed it in an EXIT trap, and was asked about "a
// recursive delete of $tmp" each time. The template's own directory, or -p's, is where the name is judged. mktemp leaves the variable
// empty when it fails, so a target that an empty value makes the root or the working directory's contents ("$tmp/"*, "$tmp"*) is
// still refused or asked about. A template the engine cannot expand, a dry run outside the scratch directory, a second value and a
// line that sets TMPDIR leave the name unknown.
func TestAMktempTemplateNamesANewPathInItsDirectory(t *testing.T) {
	f := newFixture(t)
	e := f.engine(t, Config{Mode: ModeBypass, Tmp: t.TempDir(), Ask: []string{"Edit(./.sleipnir/**)"}})
	for _, c := range []struct{ cmd, want string }{
		{`set -e; tmp=$(mktemp -d "$TMPDIR/demo.XXXX"); go build -o "$tmp/demo" ./cmd/demo; "$tmp/demo" serve --data-dir "$tmp/data" ` +
			`--addr 127.0.0.1:18089 >"$tmp/out" 2>"$tmp/err" & pid=$!; trap 'kill $pid 2>/dev/null || true; rm -rf "$tmp"' EXIT; ` +
			`sleep 1; curl -s 127.0.0.1:18089/`, "allow"},
		{`tmp=$(mktemp -d "$TMPDIR/x.XXXX"); rm -rf "$tmp"`, "allow"},
		{`tmp=$(mktemp -d "${TMPDIR}/XXX"); trap 'rm -rf "$tmp"' EXIT; go build -o "$tmp/app" ./cmd/x; "$tmp/app" >"$tmp/out"`, "allow"},
		{`tmp=$(mktemp -d "$TMPDIR/x.XXXX"); go build -o "$tmp/bin" ./cmd/x`, "deny"}, // /bin when mktemp fails
		{`tmp=$(mktemp -d -p "$TMPDIR" run.XXXXXX); rm -rf "$tmp"`, "allow"},
		{`tmp=$(mktemp -d --tmpdir run.XXXXXX); rm -rf "$tmp"`, "allow"},
		{`B="$TMPDIR/b"; mkdir -p "$B"; tmp=$(mktemp -d "$B/x.XXXX"); rm -rf "$tmp"`, "allow"},
		{`export tmp=$(mktemp -d "$TMPDIR/x.XXXX"); rm -rf "$tmp"`, "allow"},
		{`tmp=$(mktemp -d build/x.XXXX); rm -rf "$tmp"`, "allow"},                                        // relative: below the working directory
		{`tmp=$(mktemp -d "$TMPDIR/x.XXXX"); tmp=$(mktemp -d "$TMPDIR/x.XXXX"); rm -rf "$tmp"`, "allow"}, // either is a new x.* there
		// what a failed mktemp, or the target's own "..", makes of it
		{`tmp=$(mktemp -d "$TMPDIR/x.XXXX"); rm -rf "$tmp/"*`, "deny"}, // /*
		{`tmp=$(mktemp -d "$TMPDIR/x.XXXX"); rm -rf $tmp/*`, "deny"},
		{`tmp=$(mktemp -d "$TMPDIR/x.XXXX"); rm -rf "$tmp"/.*`, "ask"},
		{`tmp=$(mktemp -d "$TMPDIR/x.XXXX"); rm -rf "$tmp"*`, "ask"}, // *
		{`tmp=$(mktemp -d "$TMPDIR/x.XXXX"); cat "$tmp/proc/self/environ"`, "deny"},
		{`tmp=$(mktemp -d "$TMPDIR/x.XXXX"); rm -rf "$tmp/.."`, "deny"},
		// the directory is judged
		{`D=$(mktemp -d "$HOME/.ssh/x.XXXX"); rm -rf "$D"`, "deny"},
		{`D=$(mktemp -d -p .sleipnir); rm -rf "$D"`, "ask"},
		// not known
		{`tmp=$(mktemp -d "$TMPDIR/x.XXXX"); tmp=$(mktemp -d "$TMPDIR/y.XXXX"); rm -rf "$tmp"`, "ask"},
		{`tmp=$(mktemp -d "$X/x.XXXX"); rm -rf "$tmp"`, "ask"},
		{`tmp=$(mktemp -d "$TMPDIR/$X.XXXX"); rm -rf "$tmp"`, "ask"},
		{`tmp=$(mktemp -d "$(pwd)/x.XXXX"); rm -rf "$tmp"`, "ask"},
		{`tmp=$(mktemp -u "$TMPDIR/x.XXXX"); rm -rf "$tmp"`, "ask"},
		{`tmp=$(mktemp -d "$TMPDIR/../x.XXXX"); rm -rf "$tmp"`, "ask"},
		{`tmp=$(mktemp -d "$TMPDIR/x.XX"); rm -rf "$tmp"`, "ask"},
		{`tmp=$(mktemp -d "$TMPDIR/x*.XXXX"); rm -rf "$tmp"`, "ask"},
		{`TMPDIR=/; tmp=$(mktemp -d -p "" x.XXXX); rm -rf "$tmp"`, "ask"},
		{`P=$(git rev-parse --show-toplevel); tmp=$(mktemp -d -p "$P"); rm -rf "$tmp"`, "ask"}, // P is empty when git fails
	} {
		d := e.Check(context.Background(), Request{Agent: "be-7", Tool: "Bash", Command: f.expand(c.cmd)})
		if got := outcome(d); got != c.want {
			t.Errorf("%s\n  = %s (%s), want %s", c.cmd, got, d.Reason, c.want)
		}
	}
	// a file: with an Edit deny rule, a name only known when the command runs is refused, and a new file in $TMPDIR is not
	ruled := f.engine(t, Config{Mode: ModeBypass, Tmp: t.TempDir(), Deny: []string{"Edit(./secrets/**)"}})
	for _, c := range []struct{ cmd, want string }{
		{`f=$(mktemp "$TMPDIR/x.XXXX"); rm -f "$f"`, "allow"},
		{`f=$(mktemp -p secrets); rm -f "$f"`, "deny"},
		{`f=$(mktemp "$X/x.XXXX"); rm -f "$f"`, "deny"},
		{`f=$(mktemp -u "$TMPDIR/x.XXXX"); rm -f "$f"`, "deny"},
	} {
		d := ruled.Check(context.Background(), Request{Agent: "be-7", Tool: "Bash", Command: f.expand(c.cmd)})
		if got := outcome(d); got != c.want {
			t.Errorf("%s\n  = %s (%s), want %s", c.cmd, got, d.Reason, c.want)
		}
	}
}

// mktempPath takes GNU mktemp's directory: the template's, -p's, or $TMPDIR's, which the engine knows only as the scratch directory of
// a session that has one and a line that does not name TMPDIR. It answers the same whether a command of the line failed or not, so a
// variable set from it is judged empty as well (noteAssignments).
func TestMktempPathIsANameInItsDirectory(t *testing.T) {
	rs := &resolver{home: "/h", tmp: "/s/tmp"}
	x := expansion{vars: map[string]string{"B": "/b", "P": "/p"}, fallible: map[string]bool{"P": true}}
	for cmd, want := range map[string]string{
		`mktemp -d`:                          "/s/tmp/tmp.$(mktemp)",
		`mktemp`:                             "/s/tmp/tmp.$(mktemp)",
		`mktemp -u`:                          "/s/tmp/tmp.$(mktemp)",
		`mktemp -q -d -t rw.XXXXXX`:          "/s/tmp/rw.$(mktemp)",
		`mktemp -dt rw`:                      "/s/tmp/rw.$(mktemp)", // BSD: a prefix
		`mktemp -d "$TMPDIR/demo.XXXX"`:      "/s/tmp/demo.$(mktemp)",
		`mktemp -d ${TMPDIR}/XXX`:            "/s/tmp/$(mktemp)",
		`mktemp --directory "$B/run.XXXXXX"`: "/b/run.$(mktemp)",
		`mktemp -d "$HOME/x.XXXX"`:           "/h/x.$(mktemp)",
		`mktemp -d /x/rw.XXXXXX`:             "/x/rw.$(mktemp)",
		`mktemp -d x.XXXX`:                   "x.$(mktemp)", // GNU: in the working directory
		`mktemp -d build/x.XXXX`:             "build/x.$(mktemp)",
		`mktemp -d -p /x`:                    "/x/tmp.$(mktemp)",
		`mktemp -dp /x/ rw.XXXX`:             "/x/rw.$(mktemp)",
		`mktemp -dp/x rw.XXXX`:               "/x/rw.$(mktemp)",
		`mktemp -d --tmpdir=/x rw.XXXX`:      "/x/rw.$(mktemp)",
		`mktemp -d --tmpdir rw.XXXX`:         "/s/tmp/rw.$(mktemp)",
		`mktemp -d -p "" rw.XXXX`:            "/s/tmp/rw.$(mktemp)",
		`mktemp -d -t -p /x rw.XXXX`:         "/s/tmp/rw.$(mktemp)", // -t prefers $TMPDIR
		`mktemp -u -p "$TMPDIR"`:             "/s/tmp/tmp.$(mktemp)",
		`mktemp -u /x/rw.XXXX`:               "", // a dry run creates nothing outside the scratch directory
		`mktemp -u "$TMPDIR/x.XXXX"`:         "",
		`mktemp -d "$X/x.XXXX"`:              "",
		`mktemp -d -p "$P"`:                  "", // empty when the command P came from failed: $TMPDIR, or /tmp
		`mktemp -d "$TMPDIR/*.XXXX"`:         "",
		`mktemp -d "$TMPDIR/a b.XXXX"`:       "",
		`mktemp -d "$TMPDIR/{a,b}.XXXX"`:     "",
		`mktemp -d "$TMPDIR/../x.XXXX"`:      "",
		`mktemp -d "$TMPDIR/x.XX"`:           "",
		`mktemp -d "$TMPDIR/XXXX/x"`:         "",
		`mktemp -d "$TMPDIR/x.XXXX/"`:        "",
		`mktemp -d x`:                        "",
		`mktemp -d ""`:                       "",
		`mktemp -d a.XXXX b.XXXX`:            "",
		`mktemp -d --suffix=.d x.XXXX`:       "",
		`mktemp -d -- x.XXXX`:                "",
		`mktemp -d -t /x/rw.XXXX`:            "",
		`mktemp -d -p /x sub/rw.XXXX`:        "",
		`mktemp -d -p /x /y/rw.XXXX`:         "",
		`mktemp -d -p`:                       "",
		`mktemp -d '$TMPDIR/x.XXXX'`:         "", // the characters $TMPDIR
		`mktemp -d "\$TMPDIR/x.XXXX"`:        "",
		`mktemp -d ~/x.XXXX`:                 "",
		`mktemp -d "$(pwd)/x.XXXX"`:          "",
		`TMPDIR=/ mktemp -d`:                 "",
		`env mktemp -d`:                      "",
	} {
		for _, emptied := range []bool{false, true} {
			x.emptied = emptied
			got, ok := rs.mktempPath(cmd, "/w", x)
			if ok != (want != "") || got != want {
				t.Errorf("%s (emptied %v) = %q, %v; want %q", cmd, emptied, got, ok, want)
			}
		}
	}
	// $TMPDIR is not known without a scratch directory, nor on a line that names TMPDIR; a directory the template or -p names still is
	off := expansion{off: map[string]bool{"TMPDIR": true}}
	for _, c := range []struct {
		rs        *resolver
		x         expansion
		cmd, want string
	}{
		{&resolver{}, expansion{}, `mktemp -d`, ""},
		{&resolver{}, expansion{}, `mktemp -d -t x.XXXX`, ""},
		{&resolver{}, expansion{}, `mktemp -d /x/XXX`, "/x/$(mktemp)"},
		{&resolver{}, expansion{}, `mktemp -d -p /x`, "/x/tmp.$(mktemp)"},
		{&resolver{}, expansion{}, `mktemp -u -p /x`, ""},
		{rs, off, `mktemp -d`, ""},
		{rs, off, `mktemp -d --tmpdir x.XXXX`, ""},
		{rs, off, `mktemp -d "$TMPDIR/x.XXXX"`, ""},
		{rs, off, `mktemp -d -p /x`, "/x/tmp.$(mktemp)"},
	} {
		if got, ok := c.rs.mktempPath(c.cmd, "/w", c.x); ok != (c.want != "") || got != c.want {
			t.Errorf("%s (tmp %q, off %v) = %q, %v; want %q", c.cmd, c.rs.tmp, c.x.off, got, ok, c.want)
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

// A command that a new shell runs (sh -c, bash -c, a script on a shell's standard input) assigns and sources in that shell: the
// line's variables are what they were. A devops agent's preflight test sourced the project's installer in `sh -c` between
// T=$(mktemp -d) and rm -rf "$T", and was asked about "a recursive delete of $T" three times in one hour: a source anywhere made
// $TMPDIR, and with it the mktemp directory, unknown to the whole line. What eval and . do still counts: they run in this shell.
// A loop around a new shell still makes the names it assigns unknown to that shell, which sees what the line exported.
func TestAChildShellDoesNotChangeTheLinesVariables(t *testing.T) {
	f := newFixture(t)
	e := f.engine(t, Config{Mode: ModeBypass, Tmp: t.TempDir()})
	for _, c := range []struct{ cmd, want string }{
		{`T=$(mktemp -d); sh -c '. scripts/x.sh' _; rm -rf "$T"`, "allow"},
		{`T=$(mktemp -d); bash -c 'source a.sh'; rm -rf "$T"`, "allow"},
		{`T=$(mktemp -d); sh -c 'T=/; echo "$T"' _; rm -rf "$T"`, "allow"},
		{`T=$(mktemp -d); sh -c 'export T=/; read T; unset T; for T in /; do :; done; echo $((T=1))'; rm -rf "$T"`, "allow"},
		{"T=$(mktemp -d)\nbash <<'EOF'\nT=/\n. ./x.sh\nEOF\nrm -rf \"$T\"", "allow"},
		{"cd /x\nT=$(mktemp -d)\ncp a \"$T/e.json\"\nINSTALL_UNIX_NO_MAIN=1 sh -c '. scripts/install-unix.sh; check \"$1\" && echo A || echo R' _ \"$T/e.json\" 2>/dev/null\nrm -rf \"$T\"", "allow"},
		{`T=$(mktemp -d); eval 'T=/'; rm -rf "$T"`, "deny"}, // eval runs in this shell
		{`T=$(mktemp -d); . ./a.sh; rm -rf "$T"`, "ask"},    // so does .
		{`sh -c 'D=/; rm -rf "$D"'`, "ask"},                 // inside the new shell its own names are unknown
		{`export T=$TMPDIR/a; for i in 1 2; do sh -c 'rm -rf "$T"'; export T=/; done`, "ask"},
		{`export T=$TMPDIR/a; sh -c 'rm -rf "$T"'`, "allow"},
		{`sh -c 'TMPDIR=/etc; rm -rf "$TMPDIR/x"'`, "ask"}, // what the new shell assigns is unknown in it
		{`bash -c '. ./x.sh; rm -rf "$TMPDIR/x"'`, "ask"},  // and so is all of it after it sources
		{`sh -c '. ./x.sh'; rm -rf "$TMPDIR/x"`, "allow"},
		{`export TMPDIR=/etc; sh -c 'rm -rf "$TMPDIR/x"'`, "ask"}, // what the line exports is what the new shell starts with         // but not in the line's own shell
		{`sh -c 'cd /etc'; T=$(mktemp -d); rm -rf "$T"`, "allow"}, // nor its directory
	} {
		d := e.Check(context.Background(), Request{Agent: "dv-2", Tool: "Bash", Command: f.expand(c.cmd)})
		if got := outcome(d); got != c.want {
			t.Errorf("%q\n  = %s (%s), want %s", c.cmd, got, d.Reason, c.want)
		}
	}
}

// A screenshot loop of a frontend worker (a live run): nested loops over pages, quoted sizes and themes write one file each into the scratch
// directory. Each variable stands for one of its loop's words, quoted ones included, so every file the command writes is known, and none of
// them is in a path an ask rule of the person's covers: bypass does not ask. A quoted word that holds a variable is not bound, and the
// file stays unknown.
func TestALoopOverQuotedWordsNamesItsFiles(t *testing.T) {
	f := newFixture(t)
	// A team agent's scratch directory is inside the session's state directory, which the ask rule covers: a name that is not known could
	// lead out of the scratch directory into the rest of it.
	tmp := filepath.Join(f.home, ".sleipnir", "sessions", "s1", "tmp")
	if err := os.MkdirAll(tmp, 0o700); err != nil {
		t.Fatal(err)
	}
	cfg := Config{Mode: ModeBypass, Tmp: tmp, Ask: []string{"Edit(~/.sleipnir/**)"}}
	shots := `for route in insights funnels retention paths people live dashboards settings login; do
  for size in "390,844" "1440,900"; do
    for theme in light dark; do
      timeout 60 firefox --screenshot "$TMPDIR/tess-$route-$size-$theme.png" --window-size "$size" "http://127.0.0.1:8731/$route?theme=$theme" >/dev/null 2>&1 || echo "fail $route $size $theme"
    done
  done
done`
	for _, c := range []struct{ cmd, want string }{
		{shots, "allow"},
		{`for size in "390,844" '1440,900'; do firefox --screenshot "$TMPDIR/s-$size.png" http://127.0.0.1:8731/; done`, "allow"},
		{`for size in "$W,844"; do firefox --screenshot "$TMPDIR/s-$size.png" http://127.0.0.1:8731/; done`, "ask"}, // a variable in the quotes
	} {
		d := f.engine(t, cfg).Check(context.Background(), Request{Agent: "fe-1", Tool: "Bash", Command: f.expand(c.cmd)})
		if got := outcome(d); got != c.want {
			t.Errorf("%s\n  = %s (%s), want %s", c.cmd, got, d.Reason, c.want)
		}
	}
}

// What a quoted loop word stands for: what is inside the quotes when that is a plain word without a glob, and nothing otherwise.
func TestUnquotedWord(t *testing.T) {
	for _, c := range []struct {
		in, want string
		ok       bool
	}{
		{`"390,844"`, "390,844", true}, {`'dark'`, "dark", true}, {`"a.b-c_d"`, "a.b-c_d", true},
		{`"*"`, "", false}, {`"src/*.go"`, "", false}, {`"a?"`, "", false}, {`"[ab]"`, "", false}, // a glob is not expanded in quotes
		{`"$W"`, "", false}, {`"a b"`, "", false}, {`"a;b"`, "", false}, {`"a'b"`, "", false}, {`'a"b'`, "", false},
		{`""`, "", false}, {`"x'`, "", false}, {`plain`, "", false}, {`"~/x"`, "", false}, // nor is a "~" in quotes
	} {
		got, ok := unquotedWord(c.in)
		if got != c.want || ok != c.ok {
			t.Errorf("unquotedWord(%s) = %q, %v; want %q, %v", c.in, got, ok, c.want, c.ok)
		}
	}
}
