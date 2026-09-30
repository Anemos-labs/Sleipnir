package commands

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// expand loads the world and expands one command with the given options.
func (w *world) expand(o Opts, name, args string) (Expanded, error) {
	w.t.Helper()
	r, _ := Load(o)
	return r.Expand(context.Background(), name, args)
}

func TestExpandArguments(t *testing.T) {
	w := newWorld(t)
	w.proj(".claude", "all.md", "Fix $ARGUMENTS now")
	w.proj(".claude", "pos.md", "first=$1 second=$2 third=[$3] idx=$ARGUMENTS[1]")
	w.proj(".claude", "none.md", "no placeholder here")
	w.proj(".claude", "esc.md", "cost $$1 and $1")
	for _, tc := range []struct{ name, args, want string }{
		{"all", "issue 42", "Fix issue 42 now"},
		{"all", "", "Fix  now"},
		{"pos", `a "b c"`, "first=a second=b c third=[] idx=b c"},
		{"none", "some words", "no placeholder here\n\nARGUMENTS: some words"},
		{"none", "", "no placeholder here"},
		{"esc", "5", "cost $1 and 5"},
		{"/ALL", "x", "Fix x now"},
	} {
		e, err := w.expand(w.opts(), tc.name, tc.args)
		if err != nil || e.Prompt != tc.want {
			t.Errorf("%s %q: prompt = %q, err = %v; want %q", tc.name, tc.args, e.Prompt, err, tc.want)
		}
	}
	e, _ := w.expand(w.opts(), "all", "x")
	if e.Name != "all" || e.Hash == "" {
		t.Errorf("expanded metadata: %+v", e)
	}
}

func TestExpandCarriesFrontmatter(t *testing.T) {
	w := newWorld(t)
	w.proj(".claude", "c.md", "---\nallowed-tools: Read, Bash(git status:*)\nmodel: haiku\n---\nbody")
	e, err := w.expand(w.opts(), "c", "")
	if err != nil || e.Model != "haiku" || strings.Join(e.AllowedTools, "|") != "Read|Bash(git status:*)" {
		t.Fatalf("%+v %v", e, err)
	}
	e.AllowedTools[0] = "Write"
	r, _ := w.load()
	if c, _ := r.Get("c"); c.AllowedTools[0] != "Read" {
		t.Error("the caller's copy of the tool list altered the registry")
	}
}

func TestExpandErrors(t *testing.T) {
	w := newWorld(t)
	w.proj(".claude", "c.md", "body $ARGUMENTS")
	r, _ := w.load()
	for _, name := range []string{"", "/", "nope", "c/../c", "<b>x</b>"} {
		_, err := r.Expand(context.Background(), name, "")
		if err == nil || !strings.Contains(err.Error(), "unknown command") || strings.Contains(err.Error(), "<b>") {
			t.Errorf("Expand(%q) err = %v", name, err)
		}
	}
	if _, err := r.Expand(context.Background(), "c", strings.Repeat("x", 100000)); err == nil || !strings.Contains(err.Error(), "limit") {
		t.Errorf("huge arguments: %v", err)
	}
	o := w.opts()
	o.MaxPrompt = 50
	r, _ = Load(o)
	if _, err := r.Expand(context.Background(), "c", strings.Repeat("x", 100)); err == nil || !strings.Contains(err.Error(), "expanded prompt") {
		t.Errorf("prompt cap: %v", err)
	}
}

// Everything a caller supplies is data: arguments are inserted, never interpreted.
func TestArgumentsAreNeverInterpreted(t *testing.T) {
	w := newWorld(t)
	put(t, filepath.Join(w.root, "notes.txt"), "FILE-CONTENT")
	w.proj(".claude", "c.md", "Say: $ARGUMENTS")
	var ran []string
	o := w.opts()
	o.Exec = func(_ context.Context, cmd string) (string, error) { ran = append(ran, cmd); return "OUT", nil }
	e, err := w.expand(o, "c", "!`rm -rf ~` @notes.txt $1 $ARGUMENTS ${CLAUDE_SKILL_DIR}")
	if err != nil {
		t.Fatal(err)
	}
	if len(ran) != 0 {
		t.Fatalf("an argument ran a shell command: %v", ran)
	}
	want := "Say: !`rm -rf ~` @notes.txt $1 $ARGUMENTS ${CLAUDE_SKILL_DIR}"
	if e.Prompt != want {
		t.Fatalf("prompt = %q\nwant %q", e.Prompt, want)
	}
	if strings.Contains(e.Prompt, "FILE-CONTENT") {
		t.Fatal("an argument caused a file to be included")
	}
}

func TestIncludes(t *testing.T) {
	w := newWorld(t)
	put(t, filepath.Join(w.root, "src", "a.go"), "package a\n\nfunc A() {}\n")
	put(t, filepath.Join(w.root, "README.md"), "# Readme\r\nwith CRLF\r\n")
	put(t, filepath.Join(w.root, "ticks.md"), "code:\n```go\nx := 1\n```\n````\nnested\n````\n")
	w.proj(".claude", "one.md", "Review @src/a.go, then look at (@README.md).")
	w.proj(".claude", "dup.md", "@src/a.go and again @src/a.go and @./src/a.go")
	w.proj(".claude", "arg.md", "Explain @$1 please")
	w.proj(".claude", "ticks.md", "See @ticks.md")

	e, err := w.expand(w.opts(), "one", "")
	if err != nil {
		t.Fatal(err)
	}
	wantPrompt := "Review @src/a.go, then look at (@README.md)." +
		"\n\nReferenced files (contents inserted by the harness):" +
		"\n\nContents of src/a.go:\n```\npackage a\n\nfunc A() {}\n```" +
		"\n\nContents of README.md:\n```\n# Readme\nwith CRLF\n```"
	if e.Prompt != wantPrompt {
		t.Fatalf("prompt:\n%s\nwant:\n%s", e.Prompt, wantPrompt)
	}
	if len(e.Notices) != 0 {
		t.Errorf("notices: %v", e.Notices)
	}

	e, _ = w.expand(w.opts(), "dup", "")
	if strings.Count(e.Prompt, "Contents of src/a.go:") != 1 {
		t.Errorf("a file mentioned three times must be included once:\n%s", e.Prompt)
	}

	e, _ = w.expand(w.opts(), "arg", "src/a.go")
	if !strings.HasPrefix(e.Prompt, "Explain @src/a.go please") || !strings.Contains(e.Prompt, "func A() {}") {
		t.Errorf("an @-mention built from an argument:\n%s", e.Prompt)
	}

	e, _ = w.expand(w.opts(), "ticks", "")
	if !strings.Contains(e.Prompt, "`````\ncode:\n```go") || !strings.HasSuffix(e.Prompt, "````\n`````") {
		t.Errorf("the fence must be longer than any run of backticks in the file:\n%s", e.Prompt)
	}
}

func TestIncludeRefusals(t *testing.T) {
	w := newWorld(t)
	outside := filepath.Join(filepath.Dir(w.root), "outside")
	put(t, filepath.Join(outside, "secret.txt"), "SECRET-CANARY")
	put(t, filepath.Join(w.root, ".env"), "TOKEN=placeholder")
	put(t, filepath.Join(w.root, ".env.example"), "TOKEN=")
	put(t, filepath.Join(w.root, ".git", "config"), "[core]")
	put(t, filepath.Join(w.root, "certs", "server.pem"), "PEM")
	put(t, filepath.Join(w.root, "bin.dat"), "a\x00b")
	put(t, filepath.Join(w.root, "dir", "x.txt"), "x")
	put(t, filepath.Join(w.root, "ok.txt"), "fine")
	link(t, filepath.Join(outside, "secret.txt"), filepath.Join(w.root, "leak.txt"))
	link(t, outside, filepath.Join(w.root, "outdir"))

	cases := []struct{ mention, want string }{
		{"@../outside/secret.txt", "outside the project"},
		{"@" + filepath.Join(outside, "secret.txt"), "outside the project"},
		{"@leak.txt", "outside the project"},
		{"@outdir/secret.txt", "outside the project"},
		{"@~/.ssh/id_rsa", "only files inside the project"},
		{"@.env", "environment file"},
		{"@.git/config", "credentials or repository-internal"},
		{"@certs/server.pem", "private key"},
		{"@bin.dat", "binary"},
		{"@dir", "not a regular file"},
		{"@missing.txt", "no such file"},
	}
	for _, tc := range cases {
		w.proj(".claude", "t.md", "Look at "+tc.mention+" now")
		e, err := w.expand(w.opts(), "t", "")
		if err != nil {
			t.Errorf("%s: %v", tc.mention, err)
			continue
		}
		if strings.Contains(e.Prompt, "SECRET-CANARY") || strings.Contains(e.Prompt, "TOKEN=placeholder") || strings.Contains(e.Prompt, "Referenced files") {
			t.Errorf("%s: something was included:\n%s", tc.mention, e.Prompt)
		}
		if !strings.HasPrefix(e.Prompt, "Look at "+tc.mention+" now") {
			t.Errorf("%s: the mention must stay as plain text: %q", tc.mention, e.Prompt)
		}
		if len(e.Notices) != 1 || !strings.Contains(e.Notices[0], tc.want) {
			t.Errorf("%s: notices = %v, want one mentioning %q", tc.mention, e.Notices, tc.want)
		}
	}
	// A sample environment file is fine.
	w.proj(".claude", "t.md", "@.env.example @ok.txt")
	e, _ := w.expand(w.opts(), "t", "")
	if !strings.Contains(e.Prompt, "TOKEN=") || !strings.Contains(e.Prompt, "fine") || len(e.Notices) != 0 {
		t.Errorf("harmless files were refused: %v\n%s", e.Notices, e.Prompt)
	}
}

func TestMentionsThatAreNotFiles(t *testing.T) {
	w := newWorld(t)
	put(t, filepath.Join(w.root, "alice"), "a file that happens to be named alice")
	w.proj(".claude", "t.md", "Ping @bob about mail@example.com and email@x. Also @ alone, and @@x, and `@code` and @")
	e, err := w.expand(w.opts(), "t", "")
	if err != nil || len(e.Notices) != 0 || strings.Contains(e.Prompt, "Referenced") {
		t.Fatalf("notices %v, prompt %q, err %v", e.Notices, e.Prompt, err)
	}
	// A word-like mention that names a real file is included.
	w.proj(".claude", "t.md", "Ping @alice.")
	e, _ = w.expand(w.opts(), "t", "")
	if !strings.Contains(e.Prompt, "a file that happens to be named alice") {
		t.Errorf("prompt: %q", e.Prompt)
	}
}

func TestIncludeLimits(t *testing.T) {
	w := newWorld(t)
	for i := 0; i < 5; i++ {
		put(t, filepath.Join(w.root, fmt.Sprintf("f%d.txt", i)), fmt.Sprintf("content %d", i))
	}
	put(t, filepath.Join(w.root, "big.txt"), strings.Repeat("a line of text\n", 5000))
	w.proj(".claude", "many.md", "@f0.txt @f1.txt @f2.txt @f3.txt @f4.txt")
	w.proj(".claude", "big.md", "@big.txt")
	o := w.opts()
	o.MaxIncludes = 3
	e, _ := w.expand(o, "many", "")
	if strings.Count(e.Prompt, "Contents of") != 3 || len(e.Notices) != 2 || !strings.Contains(e.Notices[0], "more than 3 files") {
		t.Errorf("notices %v\n%s", e.Notices, e.Prompt)
	}
	o = w.opts()
	o.MaxIncludeBytes = 1000
	e, _ = w.expand(o, "big", "")
	if !strings.Contains(e.Prompt, "[... truncated") || len(e.Prompt) > 1500 || len(e.Notices) != 1 {
		t.Errorf("big include: %d bytes, notices %v", len(e.Prompt), e.Notices)
	}
}

func TestAllowReadHook(t *testing.T) {
	w := newWorld(t)
	put(t, filepath.Join(w.root, "ok.txt"), "fine")
	put(t, filepath.Join(w.root, "private", "p.txt"), "PRIVATE")
	w.proj(".claude", "t.md", "@ok.txt @private/p.txt")
	var asked []string
	var sawCommand string
	o := w.opts()
	o.AllowRead = func(ctx context.Context, p string) error {
		asked = append(asked, p)
		if r, ok := FromContext(ctx); ok {
			sawCommand = r.Name
		}
		if strings.Contains(p, "private") {
			return errors.New("denied by policy")
		}
		return nil
	}
	e, err := w.expand(o, "t", "")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(e.Prompt, "PRIVATE") || !strings.Contains(e.Prompt, "fine") {
		t.Errorf("prompt:\n%s", e.Prompt)
	}
	if len(e.Notices) != 1 || !strings.Contains(e.Notices[0], "denied by policy") {
		t.Errorf("notices: %v", e.Notices)
	}
	if len(asked) != 2 || !filepath.IsAbs(asked[0]) || sawCommand != "t" {
		t.Errorf("the hook must see real absolute paths and the running command: %v %q", asked, sawCommand)
	}
}

// Included files are data: nothing they say is interpreted, so a cycle cannot
// exist and a file cannot smuggle in a shell command.
func TestIncludesAreNotInterpretedAndCyclesAreHarmless(t *testing.T) {
	w := newWorld(t)
	put(t, filepath.Join(w.root, "a.md"), "A says: @b.md and @a.md and !`id` and $ARGUMENTS\n")
	put(t, filepath.Join(w.root, "b.md"), "B says: @a.md !`whoami`\n")
	w.proj(".claude", "t.md", "Start @a.md")
	var ran atomic.Int32
	o := w.opts()
	o.Exec = func(context.Context, string) (string, error) { ran.Add(1); return "OUT", nil }
	done := make(chan struct{})
	var e Expanded
	var err error
	go func() { e, err = w.expand(o, "t", "arg"); close(done) }()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("expansion did not finish")
	}
	if err != nil {
		t.Fatal(err)
	}
	if ran.Load() != 0 {
		t.Fatalf("a file's shell syntax was executed %d times", ran.Load())
	}
	if strings.Count(e.Prompt, "Contents of a.md:") != 1 || strings.Contains(e.Prompt, "Contents of b.md:") {
		t.Errorf("only the directly mentioned file is included, once:\n%s", e.Prompt)
	}
	if !strings.Contains(e.Prompt, "A says: @b.md and @a.md and !`id` and $ARGUMENTS") {
		t.Errorf("file content was altered:\n%s", e.Prompt)
	}
}

func TestIncludesAndCommandsInsideCodeFencesAreLiteral(t *testing.T) {
	w := newWorld(t)
	put(t, filepath.Join(w.root, "a.txt"), "A")
	w.proj(".claude", "doc.md", "Syntax:\n```\n@a.txt and !`echo hi` and $1\n```\nAfter: @a.txt\n")
	var ran atomic.Int32
	o := w.opts()
	o.Exec = func(context.Context, string) (string, error) { ran.Add(1); return "OUT", nil }
	e, err := w.expand(o, "doc", "X")
	if err != nil || ran.Load() != 0 {
		t.Fatalf("ran %d, err %v", ran.Load(), err)
	}
	if !strings.Contains(e.Prompt, "```\n@a.txt and !`echo hi` and X\n```") || strings.Count(e.Prompt, "Contents of a.txt") != 1 {
		t.Errorf("prompt:\n%s", e.Prompt)
	}
}

func TestExecIsRefusedWithoutAnExecutor(t *testing.T) {
	w := newWorld(t)
	w.proj(".claude", "st.md", "Status:\n!`git status`\nDone")
	_, err := w.expand(w.opts(), "st", "")
	if err == nil || !strings.Contains(err.Error(), "no executor is configured") || !strings.Contains(err.Error(), "git status") {
		t.Fatalf("err = %v", err)
	}
	if !strings.HasPrefix(err.Error(), "command /st:") {
		t.Errorf("the error should name the command: %v", err)
	}
}

func TestExecRunsThroughTheHook(t *testing.T) {
	w := newWorld(t)
	w.proj(".claude", "st.md", "---\nallowed-tools: Bash(git status:*)\n---\nBranch: !`git branch --show-current` / Files: !`git diff --name-only $1` end\nTail line")
	var mu sync.Mutex
	var cmds []string
	var seen Running
	o := w.opts()
	o.Exec = func(ctx context.Context, cmd string) (string, error) {
		mu.Lock()
		defer mu.Unlock()
		cmds = append(cmds, cmd)
		seen, _ = FromContext(ctx)
		if strings.HasPrefix(cmd, "git branch") {
			return "main\n", nil
		}
		return "a.go\nb.go\n\n", nil
	}
	e, err := w.expand(o, "st", "HEAD~1")
	if err != nil {
		t.Fatal(err)
	}
	if want := "Branch: main / Files: a.go\nb.go end\nTail line"; e.Prompt != want {
		t.Errorf("prompt = %q, want %q", e.Prompt, want)
	}
	if got := strings.Join(cmds, " | "); got != "git branch --show-current | git diff --name-only 'HEAD~1'" {
		t.Errorf("commands = %q", got)
	}
	if seen.Name != "st" || strings.Join(seen.AllowedTools, ",") != "Bash(git status:*)" || seen.Scope != ScopeProject {
		t.Errorf("running command in ctx: %+v", seen)
	}
}

func TestExecArgumentsCannotAddOperators(t *testing.T) {
	w := newWorld(t)
	w.proj(".claude", "log.md", "!`git log $ARGUMENTS`")
	w.proj(".claude", "one.md", "!`echo $1`")
	var got string
	o := w.opts()
	o.Exec = func(_ context.Context, cmd string) (string, error) { got = cmd; return "", nil }
	for _, tc := range []struct{ name, args, want string }{
		{"log", "--oneline -5", "git log '--oneline' '-5'"},
		{"log", "main; rm -rf ~", "git log 'main;' 'rm' '-rf' '~'"},
		{"log", "$(id) `id` && x | y > z", "git log '$(id)' '`id`' '&&' 'x' '|' 'y' '>' 'z'"},
		{"one", `"it's a; b"`, `echo 'it'\''s a; b'`},
		{"one", "", "echo"},
	} {
		if _, err := w.expand(o, tc.name, tc.args); err != nil {
			t.Fatal(err)
		}
		if got != tc.want {
			t.Errorf("%s %q: command = %q, want %q", tc.name, tc.args, got, tc.want)
		}
	}
}

func TestExecFailuresAndLimits(t *testing.T) {
	w := newWorld(t)
	w.proj(".claude", "fail.md", "x !`false` y")
	w.proj(".claude", "many.md", strings.Repeat("!`echo a` ", 10))
	w.proj(".claude", "loud.md", "!`loud`")
	w.proj(".claude", "dirty.md", "!`dirty`")
	w.proj(".claude", "empty.md", "a !`` b !` ` c")
	o := w.opts()
	var calls atomic.Int32
	o.Exec = func(_ context.Context, cmd string) (string, error) {
		calls.Add(1)
		switch cmd {
		case "false":
			return "", errors.New("exit status 1: permission denied by policy")
		case "loud":
			return strings.Repeat("output line\n", 5000), nil
		case "dirty":
			return "ok\x1b[31mred\x00\u202Eend\r\n", nil
		}
		return "a", nil
	}
	if _, err := w.expand(o, "fail", ""); err == nil || !strings.Contains(err.Error(), "failed") || !strings.Contains(err.Error(), "permission denied by policy") {
		t.Errorf("exec error: %v", err)
	}
	calls.Store(0)
	if _, err := w.expand(o, "many", ""); err == nil || !strings.Contains(err.Error(), "more than 8 shell commands") || calls.Load() != 8 {
		t.Errorf("exec count: %v after %d calls", err, calls.Load())
	}
	e, err := w.expand(o, "loud", "")
	if err != nil || len(e.Prompt) > DefaultMaxExecOutput+100 || !strings.HasSuffix(e.Prompt, "[... output truncated ...]") {
		t.Errorf("loud output: %d bytes, %v", len(e.Prompt), err)
	}
	e, _ = w.expand(o, "dirty", "")
	if e.Prompt != "ok[31mredend" {
		t.Errorf("command output must be cleaned: %q", e.Prompt)
	}
	calls.Store(0)
	e, _ = w.expand(o, "empty", "")
	if calls.Load() != 0 || e.Prompt != "a !`` b !` ` c" {
		t.Errorf("empty command spans must stay text: %q (%d calls)", e.Prompt, calls.Load())
	}
}

func TestExecTimeoutAndCancellation(t *testing.T) {
	w := newWorld(t)
	w.proj(".claude", "slow.md", "!`sleep`")
	release := make(chan struct{})
	defer close(release)
	o := w.opts()
	o.ExecTimeout = 100 * time.Millisecond
	o.Exec = func(ctx context.Context, _ string) (string, error) {
		<-release // ignores ctx on purpose: Expand must still return
		return "late", nil
	}
	r, _ := Load(o)
	start := time.Now()
	_, err := r.Expand(context.Background(), "slow", "")
	if err == nil || !strings.Contains(err.Error(), "timed out") || time.Since(start) > 5*time.Second {
		t.Fatalf("err = %v after %v", err, time.Since(start))
	}

	ctx, cancel := context.WithCancel(context.Background())
	time.AfterFunc(50*time.Millisecond, cancel)
	o.ExecTimeout = time.Minute
	r, _ = Load(o)
	start = time.Now()
	_, err = r.Expand(ctx, "slow", "")
	if !errors.Is(err, context.Canceled) || time.Since(start) > 5*time.Second {
		t.Fatalf("cancel: err = %v after %v", err, time.Since(start))
	}
}

func TestExpandIsSafeForConcurrentUse(t *testing.T) {
	w := newWorld(t)
	put(t, filepath.Join(w.root, "a.txt"), "A")
	w.proj(".claude", "c.md", "Run !`echo $1` on @a.txt with $ARGUMENTS")
	o := w.opts()
	o.Exec = func(_ context.Context, cmd string) (string, error) { return strings.TrimPrefix(cmd, "echo "), nil }
	r, _ := Load(o)
	var wg sync.WaitGroup
	for i := 0; i < 16; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			for j := 0; j < 20; j++ {
				arg := fmt.Sprintf("v%d-%d", i, j)
				e, err := r.Expand(context.Background(), "c", arg)
				if err != nil || !strings.HasPrefix(e.Prompt, "Run '"+arg+"' on @a.txt with "+arg) || strings.Count(e.Prompt, "Contents of a.txt") != 1 {
					t.Errorf("%s: %q %v", arg, e.Prompt, err)
					return
				}
			}
		}(i)
	}
	wg.Wait()
}

func TestRegistryIsASnapshot(t *testing.T) {
	w := newWorld(t)
	p := w.proj(".claude", "c.md", "---\nallowed-tools: Read\n---\noriginal")
	r, _ := w.load()
	if err := os.WriteFile(p, []byte("---\nallowed-tools: Bash(*)\n---\nhijacked"), 0o644); err != nil {
		t.Fatal(err)
	}
	e, err := r.Expand(context.Background(), "c", "")
	if err != nil || e.Prompt != "original" || strings.Join(e.AllowedTools, ",") != "Read" {
		t.Fatalf("%+v %v", e, err)
	}
}

func TestNoProjectRootMeansNoIncludes(t *testing.T) {
	w := newWorld(t)
	w.user(".claude", "c.md", "Look at @src/a.go and @bob")
	o := Opts{Home: w.home}
	e, err := w.expand(o, "c", "")
	if err != nil || strings.Contains(e.Prompt, "Referenced") || len(e.Notices) != 1 || !strings.Contains(e.Notices[0], "no project root") {
		t.Fatalf("%+v %v", e, err)
	}
}
