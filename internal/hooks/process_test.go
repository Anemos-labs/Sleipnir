//go:build unix

package hooks

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
	"unicode/utf8"
)

// A hook that outlives its timeout must take its whole process tree with it.
func TestTimeoutKillsTheWholeProcessGroup(t *testing.T) {
	tests := []struct {
		name    string
		command string
	}{
		{"child in the background", "sleep 300 & echo $! > child.pid; wait"},
		{"leader ignores SIGTERM", "trap '' TERM; sleep 300 & echo $! > child.pid; while true; do sleep 1; done"},
		{"grandchildren", "(sleep 300 & echo $! > child.pid; wait) & wait"},
		{"child ignores SIGTERM too", "sh -c \"trap '' TERM; echo \\$\\$ > child.pid; while true; do sleep 1; done\" & wait"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			r := newRunner(t, settings(t, PreToolUse, group{hooks: []hookSpec{cmdHook(tc.command).timeout(1)}}))
			start := time.Now()
			res := run(t, r, Event{Name: PreToolUse})
			elapsed := time.Since(start)
			if elapsed > 5*time.Second {
				t.Fatalf("Run took %v", elapsed)
			}
			if !strings.Contains(errorText(res), "timed out after 1s") || res.Blocked {
				t.Errorf("errors = %q blocked=%v", errorText(res), res.Blocked)
			}
			if len(res.Runs) != 1 || !res.Runs[0].TimedOut || res.Runs[0].ExitCode < 128 {
				t.Errorf("run = %+v", res.Runs)
			}
			pid := readPID(t, filepath.Join(r.Dir, "child.pid"))
			killLater(t, pid)
			waitFor(t, 5*time.Second, "the child to die", func() bool { return !alive(pid) })
		})
	}
}

// A child that inherited the hook's output would hold the harness forever.
func TestBackgroundChildHoldingTheOutputIsTerminated(t *testing.T) {
	r := one(t, Stop, "", "sleep 300 & echo $! > child.pid; exit 0")
	start := time.Now()
	res := run(t, r, Event{Name: Stop})
	if elapsed := time.Since(start); elapsed > 3*time.Second {
		t.Fatalf("Run took %v: it waited for a background child", elapsed)
	}
	if !strings.Contains(errorText(res), "left background processes attached to its output") || res.Blocked {
		t.Errorf("errors = %q", errorText(res))
	}
	pid := readPID(t, filepath.Join(r.Dir, "child.pid"))
	killLater(t, pid)
	waitFor(t, 5*time.Second, "the child to be terminated", func() bool { return !alive(pid) })
}

// A hook that daemonises properly (output redirected) is not the runner's business.
func TestDetachedBackgroundChildIsLeftAlone(t *testing.T) {
	r := one(t, SessionStart, "", "sleep 300 >/dev/null 2>&1 </dev/null & echo $! > child.pid")
	r.PipeGrace = 10 * time.Second // the property under test is "not treated as a straggler", not speed
	start := time.Now()
	res := run(t, r, Event{Name: SessionStart})
	if elapsed := time.Since(start); elapsed > 3*time.Second {
		t.Fatalf("Run took %v", elapsed)
	}
	if len(res.Errors) != 0 {
		t.Errorf("errors = %q", errorText(res))
	}
	pid := readPID(t, filepath.Join(r.Dir, "child.pid"))
	killLater(t, pid)
	if !alive(pid) {
		t.Error("a properly detached child was killed")
	}
}

func TestEndlessOutputIsStopped(t *testing.T) {
	for _, tc := range []struct {
		name, command string
	}{
		{"stdout", "yes"},
		{"stderr", "yes >&2"},
		{"both", "yes & yes >&2"},
		{"long lines", "yes " + strings.Repeat("x", 4000)},
		{"no newlines", "while :; do printf aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa; done"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := one(t, PreToolUse, "", tc.command)
			r.MaxRawOutput = 1 << 20
			r.MaxStdout, r.MaxStderr = 4<<10, 4<<10
			start := time.Now()
			res := run(t, r, Event{Name: PreToolUse})
			if elapsed := time.Since(start); elapsed > 10*time.Second {
				t.Fatalf("Run took %v", elapsed)
			}
			if !strings.Contains(errorText(res), "printed more than it may") {
				t.Errorf("errors = %q", errorText(res))
			}
		})
	}
}

func TestBigButFiniteOutputIsCappedNotFatal(t *testing.T) {
	r := one(t, SessionStart, "", "head -c 3000000 /dev/zero | tr '\\0' 'a'")
	r.MaxStdout = 2048
	r.MaxContext = 1 << 20
	res := run(t, r, Event{Name: SessionStart})
	if len(res.Errors) != 0 {
		t.Fatalf("errors = %q", errorText(res))
	}
	if n := len(res.AdditionalContext); n < 2000 || n > 2100 || !strings.Contains(res.AdditionalContext, "aaaa") {
		t.Errorf("context is %d bytes: only the head of the output is kept", n)
	}
}

func TestContextIsCappedAtAValidRuneBoundary(t *testing.T) {
	r := one(t, SessionStart, "", "i=0; while [ $i -lt 5000 ]; do printf 'héllo wörld 世界 '; i=$((i+1)); done")
	res := run(t, r, Event{Name: SessionStart})
	if len(res.AdditionalContext) > DefaultMaxContext+len(truncMark) || !strings.HasSuffix(res.AdditionalContext, "[... hook output truncated ...]") {
		t.Fatalf("context is %d bytes, ends %q", len(res.AdditionalContext), res.AdditionalContext[max(0, len(res.AdditionalContext)-40):])
	}
	if !utf8.ValidString(res.AdditionalContext) {
		t.Error("the cap fell inside a character")
	}
}

func TestOutputIsCleanedBeforeItReachesAModel(t *testing.T) {
	tests := []struct {
		name    string
		command string
		want    []string
		absent  []string
	}{
		{"invalid utf8", `printf 'bad \377\376 bytes' >&2; exit 2`, []string{"bad \uFFFD bytes"}, nil},
		{"ansi colours", `printf '\033[1;31mred\033[0m text' >&2; exit 2`, []string{"red text"}, []string{"\x1b", "[31m", "[0m"}},
		{"osc title and clipboard", `printf 'a\033]0;evil title\007b\033]52;c;ZGF0YQ==\033\\c' >&2; exit 2`, []string{"abc"}, []string{"evil", "ZGF0YQ", "\x1b"}},
		{"cursor movement", `printf 'x\033[2J\033[Hy' >&2; exit 2`, []string{"xy"}, []string{"\x1b", "[2J"}},
		{"nul and controls", `printf 'a\000b\001c\002d' >&2; exit 2`, []string{"abcd"}, []string{"\x00", "\x01"}},
		{"hidden unicode", `printf 'ok \342\200\256hidden\342\200\254 text' >&2; exit 2`, []string{"ok hidden text"}, []string{"\u202E", "\u202C"}},
		{"crlf", `printf 'one\r\ntwo\rthree' >&2; exit 2`, []string{"one\ntwo\nthree"}, []string{"\r"}},
		{"unterminated escape", `printf 'tail\033[' >&2; exit 2`, []string{"tail"}, []string{"\x1b"}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			r := one(t, PreToolUse, "", tc.command)
			res := run(t, r, Event{Name: PreToolUse})
			if !res.Blocked || !utf8.ValidString(res.Reason) {
				t.Fatalf("reason %q (valid UTF-8: %v)", res.Reason, utf8.ValidString(res.Reason))
			}
			for _, w := range tc.want {
				if !strings.Contains(res.Reason, w) {
					t.Errorf("reason %q lacks %q", res.Reason, w)
				}
			}
			for _, a := range tc.absent {
				if strings.Contains(res.Reason, a) {
					t.Errorf("reason %q still contains %q", res.Reason, a)
				}
			}
		})
	}
}

func TestReasonIsCapped(t *testing.T) {
	r := one(t, PreToolUse, "", "head -c 100000 /dev/zero | tr '\\0' 'r' >&2; exit 2")
	res := run(t, r, Event{Name: PreToolUse})
	if !res.Blocked || len(res.Reason) > maxReason+len(truncMark) || !strings.HasSuffix(res.Reason, "truncated ...]") {
		t.Errorf("reason is %d bytes", len(res.Reason))
	}
}

// A hook that never reads stdin must not wedge the runner, even when the
// payload is far larger than a pipe buffer.
func TestHookThatIgnoresStdin(t *testing.T) {
	for _, command := range []string{"exit 0", "sleep 1", "echo done"} {
		r := one(t, PreToolUse, "", command)
		start := time.Now()
		res := run(t, r, Event{Name: PreToolUse, Extra: map[string]any{"bulk": strings.Repeat("z", 3<<20)}})
		if elapsed := time.Since(start); elapsed > 5*time.Second {
			t.Errorf("%q: Run took %v", command, elapsed)
		}
		if len(res.Errors) != 0 {
			t.Errorf("%q: errors %q", command, errorText(res))
		}
	}
}

func TestHookThatReadsAHugePayload(t *testing.T) {
	r := one(t, PreToolUse, "", "wc -c > size.txt")
	run(t, r, Event{Name: PreToolUse, Extra: map[string]any{"bulk": strings.Repeat("z", 2<<20)}})
	b, _ := os.ReadFile(filepath.Join(r.Dir, "size.txt"))
	if n := strings.TrimSpace(string(b)); n == "" || n[0] < '2' {
		t.Errorf("hook read %q bytes of a 2 MiB payload", n)
	}
}

func TestCancellationKillsRunningHooks(t *testing.T) {
	r := one(t, PreToolUse, "", "sleep 300 & echo $! > child.pid; wait")
	ctx, cancel := context.WithCancel(context.Background())
	time.AfterFunc(400*time.Millisecond, cancel)
	start := time.Now()
	res, err := r.Run(ctx, Event{Name: PreToolUse})
	if err != context.Canceled {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
	if elapsed := time.Since(start); elapsed > 5*time.Second {
		t.Fatalf("Run took %v after cancellation", elapsed)
	}
	if res.Blocked || len(res.Errors) != 0 {
		t.Errorf("a cancelled hook is not a hook failure: %+v", res)
	}
	pid := readPID(t, filepath.Join(r.Dir, "child.pid"))
	killLater(t, pid)
	waitFor(t, 5*time.Second, "the child to die", func() bool { return !alive(pid) })
}

func TestHooksRunInParallel(t *testing.T) {
	var hs []hookSpec
	for i := 0; i < 6; i++ {
		hs = append(hs, cmdHook("sleep 0.6"))
		hs[i]["command"] = "sleep 0.6; echo " + string(rune('a'+i)) + " > /dev/null"
	}
	r := newRunner(t, settings(t, PreToolUse, group{hooks: hs}))
	start := time.Now()
	res := run(t, r, Event{Name: PreToolUse})
	elapsed := time.Since(start)
	if res.Ran() != 6 || len(res.Errors) != 0 {
		t.Fatalf("ran %d: %s", res.Ran(), errorText(res))
	}
	if elapsed > 3*time.Second { // 6 x 0.6 s sequentially would be 3.6 s
		t.Errorf("6 hooks of 0.6 s took %v: they ran one after another", elapsed)
	}

	r.MaxParallel = 1
	start = time.Now()
	run(t, r, Event{Name: PreToolUse})
	if elapsed := time.Since(start); elapsed < 3*time.Second {
		t.Errorf("MaxParallel=1 took %v; hooks should have been serialised", elapsed)
	}
}

// The answer must not depend on which hook finishes first.
func TestCombinationIsDeterministic(t *testing.T) {
	deny := `sleep 0.5; ` + heredoc(`{"hookSpecificOutput":{"permissionDecision":"deny","permissionDecisionReason":"A denies"}}`)
	allow := heredoc(`{"hookSpecificOutput":{"permissionDecision":"allow","permissionDecisionReason":"B allows"}}`)
	ask := `sleep 0.2; ` + heredoc(`{"hookSpecificOutput":{"permissionDecision":"ask","permissionDecisionReason":"C asks"}}`)
	orders := [][]string{{deny, allow, ask}, {allow, ask, deny}, {ask, deny, allow}}
	for i, order := range orders {
		var hs []hookSpec
		for _, c := range order {
			hs = append(hs, cmdHook(c))
		}
		r := newRunner(t, settings(t, PreToolUse, group{hooks: hs}))
		res := run(t, r, Event{Name: PreToolUse})
		if res.Decision != Deny || !res.Blocked || res.Reason != "A denies" {
			t.Errorf("order %d: decision %q blocked %v reason %q", i, res.Decision, res.Blocked, res.Reason)
		}
	}
	// Ask beats allow.
	r := newRunner(t, settings(t, PreToolUse, group{hooks: []hookSpec{cmdHook(allow), cmdHook(ask)}}))
	if res := run(t, r, Event{Name: PreToolUse}); res.Decision != Ask || res.Blocked || res.Reason != "C asks" {
		t.Errorf("ask vs allow: %q %v %q", res.Decision, res.Blocked, res.Reason)
	}
	// Allow alone.
	r = newRunner(t, settings(t, PreToolUse, group{hooks: []hookSpec{cmdHook(allow), cmdHook("exit 0")}}))
	if res := run(t, r, Event{Name: PreToolUse}); res.Decision != Allow || res.Blocked || res.Reason != "B allows" {
		t.Errorf("allow: %q %v %q", res.Decision, res.Blocked, res.Reason)
	}
}

func TestContextAndReasonsFollowConfigurationOrder(t *testing.T) {
	slow := `sleep 0.5; ` + heredoc(`{"hookSpecificOutput":{"additionalContext":"from the slow hook"}}`)
	fast := heredoc(`{"hookSpecificOutput":{"additionalContext":"from the fast hook"}}`)
	r := newRunner(t, settings(t, PostToolUse, group{hooks: []hookSpec{cmdHook(slow), cmdHook(fast)}}))
	res := run(t, r, Event{Name: PostToolUse, Tool: "bash"})
	if res.AdditionalContext != "from the slow hook\n\nfrom the fast hook" {
		t.Errorf("context = %q", res.AdditionalContext)
	}

	// Blocking reasons too: exit-2 stderr and JSON denials interleave in hook order.
	r = newRunner(t, settings(t, PreToolUse, group{hooks: []hookSpec{
		cmdHook("sleep 0.4; echo 'first reason' >&2; exit 2"),
		cmdHook("echo 'second reason' >&2; exit 2"),
		cmdHook(heredoc(`{"decision":"block","reason":"third reason"}`)),
	}}))
	res = run(t, r, Event{Name: PreToolUse})
	if res.Reason != "first reason\nsecond reason\nthird reason" || res.Decision != Deny {
		t.Errorf("reason = %q decision %q", res.Reason, res.Decision)
	}
}

func TestFirstRewriteWins(t *testing.T) {
	slow := `sleep 0.4; ` + heredoc(`{"hookSpecificOutput":{"updatedInput":{"command":"from-first"}}}`)
	fast := heredoc(`{"hookSpecificOutput":{"updatedInput":{"command":"from-second"}}}`)
	r := newRunner(t, settings(t, PreToolUse, group{hooks: []hookSpec{cmdHook(slow), cmdHook(fast)}}))
	res := run(t, r, Event{Name: PreToolUse, Tool: "bash", Input: json.RawMessage(`{"command":"orig"}`)})
	if string(res.UpdatedInput) != `{"command":"from-first"}` {
		t.Errorf("updated input = %s", res.UpdatedInput)
	}
	if !strings.Contains(errorText(res), "its updatedInput was ignored") {
		t.Errorf("the ignored rewrite must be reported: %q", errorText(res))
	}
}

func TestDenialBeatsARewrite(t *testing.T) {
	r := newRunner(t, settings(t, PreToolUse, group{hooks: []hookSpec{
		cmdHook(heredoc(`{"hookSpecificOutput":{"updatedInput":{"command":"rewritten"}}}`)),
		cmdHook("echo 'nope' >&2; exit 2"),
	}}))
	res := run(t, r, Event{Name: PreToolUse, Tool: "bash"})
	if !res.Blocked || res.UpdatedInput != nil {
		t.Errorf("blocked=%v updated=%s", res.Blocked, res.UpdatedInput)
	}
}

func TestIdenticalHooksRunOnce(t *testing.T) {
	r := newRunner(t, settings(t, Stop,
		group{hooks: []hookSpec{cmdHook("echo x >> count.txt"), cmdHook("echo x >> count.txt")}},
		group{matcher: "*", hooks: []hookSpec{cmdHook("echo x >> count.txt")}},
		group{hooks: []hookSpec{cmdHook("echo y >> count.txt")}},
	))
	res := run(t, r, Event{Name: Stop})
	b, _ := os.ReadFile(filepath.Join(r.Dir, "count.txt"))
	if got := strings.Count(string(b), "x"); got != 1 || strings.Count(string(b), "y") != 1 {
		t.Errorf("count.txt = %q", b)
	}
	if res.Ran() != 2 {
		t.Errorf("ran %d hooks, want 2", res.Ran())
	}
}

func TestPerHookTimeouts(t *testing.T) {
	r := newRunner(t, settings(t, PreToolUse, group{hooks: []hookSpec{
		cmdHook("sleep 30").timeout(0.4),
		cmdHook("echo fine >&2; exit 2"),
	}}))
	start := time.Now()
	res := run(t, r, Event{Name: PreToolUse})
	if elapsed := time.Since(start); elapsed > 4*time.Second {
		t.Errorf("took %v", elapsed)
	}
	if !res.Blocked || res.Reason != "fine" || !strings.Contains(errorText(res), "timed out after 400ms") {
		t.Errorf("one hook's timeout must not affect the others: %+v (%s)", res, errorText(res))
	}

	// A hook cannot ask for more than the runner allows.
	r = newRunner(t, settings(t, PreToolUse, group{hooks: []hookSpec{cmdHook("sleep 30").timeout(3600)}}))
	r.MaxTimeout = 300 * time.Millisecond
	start = time.Now()
	res = run(t, r, Event{Name: PreToolUse})
	if elapsed := time.Since(start); elapsed > 4*time.Second || !strings.Contains(errorText(res), "timed out") {
		t.Errorf("MaxTimeout not applied: %v %s", elapsed, errorText(res))
	}
	// And the default applies to hooks that name none.
	r = one(t, PreToolUse, "", "sleep 30")
	r.DefaultTimeout = 300 * time.Millisecond
	res = run(t, r, Event{Name: PreToolUse})
	if !strings.Contains(errorText(res), "timed out after 300ms") {
		t.Errorf("default timeout: %s", errorText(res))
	}
}

func TestSpawnFailuresAreReported(t *testing.T) {
	r := one(t, PreToolUse, "", "exit 0")
	r.Dir = filepath.Join(r.Dir, "does", "not", "exist")
	res := run(t, r, Event{Name: PreToolUse})
	if !strings.Contains(errorText(res), "could not run") || res.Blocked {
		t.Errorf("errors = %q", errorText(res))
	}
	r.FailClosed = true
	if res := run(t, r, Event{Name: PreToolUse}); !res.Blocked || !strings.Contains(res.Reason, "fail closed") {
		t.Errorf("a hook that cannot start must block when fail-closed: %+v", res)
	}
}

func TestConcurrentRunsAreIndependent(t *testing.T) {
	r := newRunner(t, settings(t, PreToolUse, group{hooks: []hookSpec{
		cmdHook(`cat > /dev/null; printf '{"hookSpecificOutput":{"additionalContext":"%s"}}' "$SLEIPNIR_AGENT"`),
	}}))
	var wg sync.WaitGroup
	for i := 0; i < 24; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			agent := "agent-" + itoa(i)
			res, err := r.Run(context.Background(), Event{Name: PreToolUse, Tool: "bash", Agent: agent})
			if err != nil || res.AdditionalContext != agent {
				t.Errorf("%s: context %q err %v", agent, res.AdditionalContext, err)
			}
		}()
	}
	wg.Wait()
}
