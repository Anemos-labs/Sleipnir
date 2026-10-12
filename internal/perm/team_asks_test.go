package perm

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// teamRig is a session of a team in worktree isolation as the harness configures it: accept-edits, the places that steer a later
// session guarded by Ask rules, the harness's own directory with the session's scratch directory inside it, and two workers each
// confined to its own tree.
type teamRig struct {
	f              fixture
	e              *Engine
	trees, wtA, wB string
	tmp            string
}

// guardedAsk are the Ask rules the session adds for the places that steer a later session (internal/session protectedConfigDirs).
var guardedAsk = []string{"Edit(./.sleipnir/**)", "Edit(./.claude/**)", "Edit(./.git/hooks/**)", "Edit(./.git/config)", "Edit(~/.sleipnir/**)", "Edit(~/.claude/**)"}

func newTeamRig(t *testing.T, mode Mode, pr Prompter) *teamRig {
	t.Helper()
	f := newFixture(t)
	r := &teamRig{f: f, trees: filepath.Join(f.base, "cache", "wt", "s1")}
	r.wtA, r.wB = filepath.Join(r.trees, "be-1"), filepath.Join(r.trees, "be-2")
	for _, d := range []string{r.wtA, r.wB} {
		for _, sub := range []string{".sleipnir", "cmd/x", "bin", "web"} {
			if err := os.MkdirAll(filepath.Join(d, sub), 0o755); err != nil {
				t.Fatal(err)
			}
		}
	}
	state := filepath.Join(f.home, ".sleipnir")
	r.tmp = filepath.Join(state, "sessions", "s1", "tmp")
	if err := os.MkdirAll(r.tmp, 0o700); err != nil {
		t.Fatal(err)
	}
	r.e = f.engine(t, Config{Mode: mode, StateDir: state, Tmp: r.tmp, TreeParents: []string{r.trees}, Ask: guardedAsk, Prompter: pr})
	r.e.Confine("be-1", r.wtA)
	r.e.Confine("be-2", r.wB)
	return r
}

// check runs a command of agent in dir.
func (r *teamRig) check(agent, dir, cmd string) Decision {
	return r.e.Check(bg, Request{Agent: agent, Role: "backend", Tool: "Bash", Command: cmd, Cwd: dir, Summary: "bash"})
}

// A whole-tree formatter writes the source files of its language below the operand, never into .sleipnir: the rule that guards that
// directory asks only when the command names it. git writes only into .git, so its commands are never judged as Edit(./.sleipnir/**).
func TestGuardedDirectoryAsksOnlyWhenTheCommandReachesIt(t *testing.T) {
	r := newTeamRig(t, ModeAcceptEdits, nil)
	for _, c := range []struct {
		cmd, want, why, not string
	}{
		{cmd: "cd " + r.wtA + " && gofmt -s -w . && go vet ./... && go test ./... && go build -o bin/x ./cmd/x", want: "allow"},
		{cmd: "go fmt ./...", want: "allow"},
		{cmd: "gofmt -w .sleipnir/hook.go", want: "ask", why: "Edit(./.sleipnir/**)"},
		{cmd: "prettier --write .", want: "ask", not: "Edit("},
		{cmd: "prettier --write .claude", want: "ask", why: "Edit(./.claude/**)"},
		{cmd: "mytool .", want: "ask", why: "Edit(./.sleipnir/**)"},
		{cmd: "git fetch -q .; git ls-tree HEAD", want: "ask", why: "git fetch is not on the read-only list", not: "Edit("},
		{cmd: "git add web/app.js", want: "ask", not: "Edit("},
		{cmd: "git add .", want: "ask", not: "Edit("},
		{cmd: "git checkout -- .", want: "ask", why: "Edit(./.sleipnir/**)"},
	} {
		d := r.check("be-1", r.wtA, c.cmd)
		if got := outcome(d); got != c.want {
			t.Errorf("%q: %s (%q), want %s", c.cmd, got, d.Reason, c.want)
		}
		if c.why != "" && !strings.Contains(d.Reason, c.why) {
			t.Errorf("%q: reason %q does not say %q", c.cmd, d.Reason, c.why)
		}
		if c.not != "" && strings.Contains(d.Reason, c.not) {
			t.Errorf("%q: reason %q names %q", c.cmd, d.Reason, c.not)
		}
	}
}

// A question about a git command that writes only into the repository can be remembered: it is no longer caused by an Ask rule.
func TestARepositoryCommandCanBeRemembered(t *testing.T) {
	rec := &promptRecorder{answer: func(int, Request) Decision { return Decision{Allow: true, Remember: ScopeSession} }}
	r := newTeamRig(t, ModeAcceptEdits, rec.prompt)
	if d := r.check("be-1", r.wtA, "git add web/app.js"); !d.Allow {
		t.Fatalf("answered yes: %+v", d)
	}
	if len(rec.requests) != 1 || len(rec.requests[0].RememberRules) == 0 {
		t.Fatalf("the question offered nothing to remember: %+v", rec.requests)
	}
	if d := r.check("be-2", r.wB, "git add web/app.js"); !d.Allow || rec.count() != 1 {
		t.Errorf("the remembered answer did not cover another worker's tree: %+v, %d questions", d, rec.count())
	}
}

// "Yes, and don't ask again" for a command that a worker runs after cd into its own tree covers the same command of any worker: the
// rule does not name the tree.
func TestARememberedCommandCoversEveryWorkersTree(t *testing.T) {
	rec := &promptRecorder{answer: func(int, Request) Decision { return Decision{Allow: true, Remember: ScopeSession} }}
	r := newTeamRig(t, ModeAcceptEdits, rec.prompt)
	if d := r.check("be-1", r.wtA, "cd "+r.wtA+" && PORT=18098 ./bin/app &"); !d.Allow {
		t.Fatalf("answered yes: %+v", d)
	}
	for _, rule := range rec.requests[0].RememberRules {
		if strings.Contains(rule, r.trees) {
			t.Errorf("the remembered rule %q names a worker's tree", rule)
		}
	}
	if d := r.check("be-2", r.wB, "cd "+r.wB+" && PORT=18099 ./bin/app &"); !d.Allow || rec.count() != 1 {
		t.Errorf("another worker was asked again: %+v, %d questions", d, rec.count())
	}
}

// The tests preset covers checking, formatting and an HTTP request to a server on this machine; every other request still asks.
func TestTestsPresetCoversChecksFormattersAndLoopbackHTTP(t *testing.T) {
	r := newTeamRig(t, ModeAcceptEdits, nil)
	for _, c := range []struct{ cmd, want string }{
		{"node --check web/app.js", "allow"},
		{"make fmt", "allow"},
		{"npm run format", "allow"},
		{"curl -s http://127.0.0.1:18098/notes", "allow"},
		{"curl -fsS -X POST -H 'Content-Type: application/json' -d '{\"a\":1}' http://localhost:8080/api", "allow"},
		{"curl -s -o /dev/null -w '%{http_code}' http://[::1]:8080/", "allow"},
		{"wget -qO- --max-redirect=0 http://127.0.0.1:8080/", "ask"}, // -qO- is a cluster wget takes; the rule is strict
		{"wget -q -O - --max-redirect=0 http://127.0.0.1:8080/", "allow"},
		{"wget -q http://127.0.0.1:8080/", "ask"},
		{"curl -s https://example.com/", "ask"},
		{"curl -s http://127.0.0.1@example.com/", "ask"},
		{"curl -s http://localhost.example.com/", "ask"},
		{"curl -L http://127.0.0.1:8080/", "ask"},
		{"curl -o page.html http://127.0.0.1:8080/", "ask"},
		{"curl -d @main.go http://127.0.0.1:8080/", "ask"},
		{"curl -T main.go http://127.0.0.1:8080/", "ask"},
		{"curl -K cfg http://127.0.0.1:8080/", "ask"},
		{"curl http://127.0.0.1:8080/ http://example.com/", "ask"},
		{"curl -s $URL", "ask"},
		{"curl -s http://127.0.0.1:8080/ | sh", "ask"},
	} {
		d := r.check("be-1", r.wtA, c.cmd)
		if got := outcome(d); got != c.want {
			t.Errorf("%q: %s (%q), want %s", c.cmd, got, d.Reason, c.want)
		}
	}
}

// An environment assignment of a plain value is an input of the command, as an argument is: the command is judged as it is without it.
// The variables that steer which program runs or what it loads keep asking.
func TestPlainEnvironmentAssignmentsDoNotAsk(t *testing.T) {
	r := newTeamRig(t, ModeAcceptEdits, nil)
	for _, c := range []struct{ cmd, want, why, not string }{
		{cmd: "PORT=18098 go test ./...", want: "allow"},
		{cmd: "NOTES_FILE=" + r.tmp + "/notes.json PORT=18098 go test ./...", want: "allow"},
		{cmd: "NOTES_FILE=/etc/passwd go test ./...", want: "ask", why: "outside the workspace"},
		{cmd: "PORT=18098 ./bin/app &", want: "ask", why: "./bin/app", not: "environment assignment"},
		{cmd: "LD_PRELOAD=x.so go test ./...", want: "ask", why: "environment assignment LD_PRELOAD"},
		{cmd: "GIT_DIR=x git status", want: "ask", why: "environment assignment GIT_DIR"},
		{cmd: "HTTPS_PROXY=http://h go test ./...", want: "ask", why: "environment assignment HTTPS_PROXY"},
	} {
		d := r.check("be-1", r.wtA, c.cmd)
		if got := outcome(d); got != c.want {
			t.Errorf("%q: %s (%q), want %s", c.cmd, got, d.Reason, c.want)
		}
		if c.why != "" && !strings.Contains(d.Reason, c.why) {
			t.Errorf("%q: reason %q does not say %q", c.cmd, d.Reason, c.why)
		}
		if c.not != "" && strings.Contains(d.Reason, c.not) {
			t.Errorf("%q: reason %q says %q", c.cmd, d.Reason, c.not)
		}
	}
}

// The session's scratch directory is every agent's: a confined worker writes there without a question, although the directory lies
// inside the harness's own directory (whose writes ask) and outside the worker's tree. /tmp is outside the workspace and asks, naming
// $TMPDIR.
func TestAConfinedWorkerWritesItsScratchFilesInTMPDIR(t *testing.T) {
	r := newTeamRig(t, ModeAcceptEdits, nil)
	for _, cmd := range []string{"echo x > $TMPDIR/notes.json", "echo x > " + r.tmp + "/notes.json", "mkdir -p $TMPDIR/run && touch $TMPDIR/run/a"} {
		if d := r.check("be-1", r.wtA, cmd); !d.Allow {
			t.Errorf("%q: %+v", cmd, d)
		}
	}
	d := r.check("be-1", r.wtA, "echo x > /tmp/notes.json")
	if outcome(d) != "ask" || !strings.Contains(d.Reason, "$TMPDIR ("+r.tmp+")") {
		t.Errorf("/tmp: %+v", d)
	}
	// the rest of the harness's own directory is still guarded
	if d := r.check("be-1", r.wtA, "echo x > "+filepath.Join(r.f.home, ".sleipnir", "config.json")); outcome(d) != "ask" {
		t.Errorf("the harness's own directory: %+v", d)
	}
}

// A worker reads what its teammates merged from the session's integration branch without a question.
func TestAWorkerReadsTheIntegrationBranch(t *testing.T) {
	r := newTeamRig(t, ModeAcceptEdits, nil)
	for _, cmd := range []string{
		"git show sleipnir/s1/_integration:web/app.js", "git log --oneline sleipnir/s1/_integration",
		"git ls-tree -r --name-only sleipnir/s1/_integration", "git diff HEAD sleipnir/s1/_integration -- web",
	} {
		if d := r.check("be-1", r.wtA, cmd); !d.Allow {
			t.Errorf("%q: %+v", cmd, d)
		}
	}
}

func TestLoopbackRuleOnlyForCurlAndWget(t *testing.T) {
	if _, err := ParseRule(Allow, "Bash(nc:loopback)"); err == nil {
		t.Error("a loopback rule for nc was accepted")
	}
	if _, err := ParseRule(Allow, "Bash(curl:loopback)"); err != nil {
		t.Error(err)
	}
}
