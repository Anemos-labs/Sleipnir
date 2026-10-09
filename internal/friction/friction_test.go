package friction

import (
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	"github.com/anemos-labs/sleipnir/internal/events"
)

type ev struct {
	agent, typ string
	data       map[string]any
}

// log writes a session log the way the harness does, and returns its directory.
func log(t *testing.T, dir string, evs ...ev) string {
	t.Helper()
	l, err := events.Open(dir, filepath.Base(dir))
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range evs {
		if _, err := l.Emit(e.agent, e.typ, e.data); err != nil {
			t.Fatal(err)
		}
	}
	if err := l.Close(); err != nil {
		t.Fatal(err)
	}
	return dir
}

func response(req string) ev { return ev{"a", events.TypeModelResponse, map[string]any{"req": req}} }

func toolCall(id, name string, input map[string]any) ev {
	return ev{"a", events.TypeToolCall, map[string]any{"id": id, "name": name, "input": input}}
}

func toolFail(id, name, kind string) ev {
	return ev{"a", events.TypeToolResult, map[string]any{"id": id, "name": name, "error": true, "meta": map[string]any{"error_kind": kind}}}
}

func find(rep Report, category, contains string) *Finding {
	for i := range rep.Findings {
		if f := &rep.Findings[i]; f.Category == category && strings.Contains(f.Key, contains) {
			return f
		}
	}
	return nil
}

// A log from before perm.decide existed has only the failed tool results: the refusals are grouped by the command, with
// the paths left out, so "cd /workspace" and "cd /repo" are one pattern and sed is another.
func TestRefusalsInOlderLogsAreGroupedByCommand(t *testing.T) {
	dir := log(t, filepath.Join(t.TempDir(), "s1"),
		response("r1"), toolCall("c1", "bash", map[string]any{"command": "cd /workspace 2>/dev/null || pwd; go test ./..."}), toolFail("c1", "bash", "permission"),
		response("r2"), toolCall("c2", "bash", map[string]any{"command": "cd /repo && ls"}), toolFail("c2", "bash", "permission"),
		response("r3"), toolCall("c3", "bash", map[string]any{"command": "sed -n 1,5p main.go"}), toolFail("c3", "bash", "permission"),
		response("r4"), toolCall("c4", "bash", map[string]any{"command": "go test ./..."}),
	)
	rep, err := Mine([]string{dir}, Options{})
	if err != nil {
		t.Fatal(err)
	}
	cd := find(rep, PermRefused, "cd <path>")
	if cd == nil || cd.Count != 2 || cd.Wasted != 2 || cd.Sessions != 1 {
		t.Fatalf("cd: %+v", cd)
	}
	if sed := find(rep, PermRefused, "sed -n"); sed == nil || sed.Count != 1 {
		t.Errorf("sed: %+v", sed)
	}
	if rep.Findings[0].Key != cd.Key {
		t.Errorf("the pattern that happened twice must rank first: %+v", rep.Findings[0])
	}
	if len(cd.Examples) != 2 || cd.Examples[0].Seq == 0 || !strings.Contains(cd.Examples[0].Detail, "cd /workspace") {
		t.Errorf("examples: %+v", cd.Examples)
	}
	if rep.Requests != 4 || rep.Sessions != 1 {
		t.Errorf("requests %d sessions %d", rep.Requests, rep.Sessions)
	}
}

// Since perm.decide exists a refusal is in the log twice, as that event and as the failed result: it is counted once, from
// the event, which says why and who decided.
func TestARefusalIsCountedOnceWhenThereIsAPermDecide(t *testing.T) {
	dir := log(t, filepath.Join(t.TempDir(), "s1"),
		response("r1"), toolCall("c1", "bash", map[string]any{"command": "cd /workspace && ls"}),
		ev{"a", events.TypePermAsk, map[string]any{"tool": "bash", "command": "cd /workspace && ls", "reason": "reads /workspace outside the workspace (/root/w/tree); approval needed"}},
		ev{"a", events.TypePermDecide, map[string]any{"tool": "bash", "command": "cd /workspace && ls", "reason": "approval required: reads /workspace outside the workspace (/root/w/tree)", "allow": false, "by": "no one"}},
		toolFail("c1", "bash", "permission"),
		response("r2"), toolCall("c2", "bash", map[string]any{"command": "go vet ./..."}),
		ev{"a", events.TypePermAsk, map[string]any{"tool": "bash", "command": "go vet ./...", "reason": "not on the allowlist"}},
		ev{"a", events.TypePermDecide, map[string]any{"tool": "bash", "command": "go vet ./...", "reason": "approved by the user", "allow": true, "by": "user"}},
		response("r3"), toolCall("c3", "read", map[string]any{"path": "/home/u/.ssh/id_rsa"}),
		ev{"a", events.TypePermDecide, map[string]any{"tool": "read", "reason": "built-in protection: ~/.ssh", "allow": false, "by": "policy"}},
		ev{"a", events.TypePermDecide, map[string]any{"tool": "bash", "reason": "approval canceled: context canceled", "allow": false, "by": "canceled"}},
	)
	rep, err := Mine([]string{dir}, Options{})
	if err != nil {
		t.Fatal(err)
	}
	var refused int
	for _, f := range rep.Findings {
		if f.Category == PermRefused {
			refused += f.Count
		}
	}
	if refused != 2 { // the unattended one and the policy one; the cancelled wait is nobody's refusal
		t.Errorf("%d refusals, want 2: %+v", refused, rep.Findings)
	}
	cd := find(rep, PermRefused, "cd <path>")
	if cd == nil || !strings.Contains(cd.Key, "[no one]") || !strings.Contains(cd.Key, "outside the workspace") {
		t.Errorf("the key should say why and who decided: %+v", cd)
	}
	if asked := find(rep, PermAsked, "go vet"); asked == nil || asked.Count != 1 || asked.Severity != S1 {
		t.Errorf("a question a person answered: %+v", asked)
	}
	if p := find(rep, PermRefused, "[policy]"); p == nil {
		t.Errorf("a refusal by policy is missing: %+v", rep.Findings)
	}
}

func TestStuckCancelRetryCacheAndRepetitionAreFound(t *testing.T) {
	evs := []ev{
		response("r1"),
		{"a", events.TypeAgentStuck, map[string]any{"phase": "nudge", "note": "you ran the same command 3 times"}},
		{"a", events.TypeAgentStuck, map[string]any{"phase": "stop", "error": "agent a: agent stuck: go test failed the same way 4 times"}},
		{"a", events.TypeAgentCancel, map[string]any{"phase": "model", "cause": "canceled", "steps": 4}},
		{"a", events.TypeModelError, map[string]any{"req": "r1", "kind": "rate_limit", "status": 429, "attempt": 2}},
		{"a", events.TypeModelError, map[string]any{"req": "r1", "kind": "rate_limit", "status": 429, "attempt": 3}},
		{"a", events.TypeModelError, map[string]any{"req": "r1", "error": "boom"}}, // a final failure is not a retry
		{"a", events.TypeCacheAnomaly, map[string]any{"kind": "drift"}},
		{"a", events.TypeCompactReject, map[string]any{"stage": "model_patch", "reason": "patch keeps 12 turns, the floor is 4"}},
	}
	for i := 0; i < 4; i++ { // the same file read four times, the same command run three
		id := string(rune('a' + i))
		evs = append(evs, toolCall("r"+id, "read", map[string]any{"path": "main.go"}))
	}
	for i := 0; i < 3; i++ {
		evs = append(evs, toolCall("b"+string(rune('a'+i)), "bash", map[string]any{"command": "go test ./..."}))
	}
	evs = append(evs, toolCall("u", "bash<|channel|>commentary", map[string]any{}), toolFail("u", "bash<|channel|>commentary", "unknown_tool"))
	rep, err := Mine([]string{log(t, filepath.Join(t.TempDir(), "s1"), evs...)}, Options{})
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []struct {
		category, key string
		count, sev    int
	}{
		{Stuck, "stop", 1, S3}, {Stuck, "nudge", 1, S2}, {Cancelled, "canceled during model", 1, S3},
		{Retried, "rate_limit (429)", 2, S1}, {CacheBreak, "drift", 1, S1}, {CompactFail, "model_patch", 1, S1},
		{ReRead, "three or more times", 3, S2}, {RepeatedCall, "go test", 2, S2}, {ToolUnknown, "bash<|channel|>commentary", 1, S2},
	} {
		f := find(rep, want.category, want.key)
		if f == nil || f.Count != want.count || f.Severity != want.sev {
			t.Errorf("%s %q: %+v, want count %d severity %d", want.category, want.key, f, want.count, want.sev)
		}
	}
	if rep.Findings[0].Category != ReRead && rep.Findings[0].Category != RepeatedCall && rep.Findings[0].Category != Stuck {
		t.Logf("top: %+v", rep.Findings[0])
	}
}

// A stall the swarm's sweep named is a finding of its kind; that it cleared later is not another one.
func TestTeamStallsAreFoundByKind(t *testing.T) {
	stall := func(action, kind, task string) ev {
		return ev{"swarm", events.TypeSwarmStall, map[string]any{"action": action, "kind": kind, "task": task, "detail": task + " detail"}}
	}
	dir := log(t, filepath.Join(t.TempDir(), "s1"),
		stall("raise", "claimed_no_progress", "T1"), stall("clear", "claimed_no_progress", "T1"),
		stall("raise", "claimed_no_progress", "T2"), stall("raise", "blocked_cycle", "T3"))
	rep, err := Mine([]string{dir}, Options{})
	if err != nil {
		t.Fatal(err)
	}
	if f := find(rep, TeamStall, "claimed_no_progress"); f == nil || f.Count != 2 || f.Severity != S2 {
		t.Errorf("claimed_no_progress: %+v", f)
	}
	if f := find(rep, TeamStall, "blocked_cycle"); f == nil || f.Count != 1 || len(f.Examples) != 1 || f.Examples[0].Detail != "T3 detail" {
		t.Errorf("blocked_cycle: %+v", f)
	}
}

// A file that was changed between two reads is not read "again": reading it after the edit is how a model checks its work.
func TestReadingAFileAfterChangingItIsNotARepeat(t *testing.T) {
	evs := []ev{
		toolCall("1", "read", map[string]any{"path": "/w/tree/main.go"}),
		toolCall("2", "read", map[string]any{"path": "/w/tree/main.go"}),
		toolCall("3", "edit", map[string]any{"path": "main.go"}), // the same file, spelled relatively
		toolCall("4", "read", map[string]any{"path": "/w/tree/main.go"}),
		toolCall("5", "read", map[string]any{"path": "/w/tree/main.go"}),
		toolCall("6", "read", map[string]any{"path": "/w/tree/other.go"}),
		toolCall("7", "read", map[string]any{"path": "/w/tree/other.go"}),
		toolCall("8", "read", map[string]any{"path": "/w/tree/other.go"}),
	}
	rep, err := Mine([]string{log(t, filepath.Join(t.TempDir(), "s1"), evs...)}, Options{})
	if err != nil {
		t.Fatal(err)
	}
	f := find(rep, ReRead, "three or more")
	if f == nil || f.Count != 2 { // only other.go: read three times, two of them avoidable; main.go was read twice either side of its edit
		t.Errorf("%+v", f)
	}
}

// A big file is read a window at a time: five windows of one file are five different reads, not one read five times. (On a real
// benchmark this was most of what "a file read three or more times" found: 1152 of 2401 reads were of a file read before, and 110 of
// those were the same window.) Only the same part of the same file, unchanged between, is a repeat.
func TestReadingDifferentPartsOfAFileIsNotARepeat(t *testing.T) {
	window := func(id string, off, lim int) ev {
		return toolCall(id, "read", map[string]any{"path": "/w/tree/big.go", "offset": off, "limit": lim})
	}
	evs := []ev{
		window("1", 1, 80), window("2", 81, 80), window("3", 161, 80), window("4", 241, 80), window("5", 321, 15),
		// the same window three times is a repeat, two of them avoidable
		window("6", 1, 80), window("7", 1, 80),
		// a read of the whole file is not the same as a read of a window
		toolCall("8", "read", map[string]any{"path": "/w/tree/big.go"}),
	}
	rep, err := Mine([]string{log(t, filepath.Join(t.TempDir(), "s1"), evs...)}, Options{})
	if err != nil {
		t.Fatal(err)
	}
	f := find(rep, ReRead, "three or more")
	if f == nil || f.Count != 2 {
		t.Errorf("%+v", f)
	}
	if f != nil && len(f.Examples) > 0 && !strings.Contains(f.Examples[0].Detail, "offset 1") {
		t.Errorf("the example must say which part of the file was read again: %+v", f.Examples)
	}

	evs = []ev{window("1", 1, 80), window("2", 81, 80), window("3", 161, 80), window("4", 241, 80)}
	rep, err = Mine([]string{log(t, filepath.Join(t.TempDir(), "s2"), evs...)}, Options{})
	if err != nil {
		t.Fatal(err)
	}
	if f := find(rep, ReRead, "three or more"); f != nil {
		t.Errorf("four windows of a file are not a repeat: %+v", f)
	}
}

func TestFindingsRankByFrequencySeverityAndWaste(t *testing.T) {
	var evs []ev
	for i, id := range []string{"1", "2", "3"} { // three refusals, three different requests
		evs = append(evs, response("r"+id), toolCall(id, "bash", map[string]any{"command": "cd /x && ls"}), toolFail(id, "bash", "permission"))
		_ = i
	}
	evs = append(evs, response("r9"), ev{"a", events.TypeAgentStuck, map[string]any{"phase": "stop", "error": "stuck"}})
	rep, err := Mine([]string{log(t, filepath.Join(t.TempDir(), "s1"), evs...)}, Options{})
	if err != nil {
		t.Fatal(err)
	}
	// 3 refusals x severity 2 squared x (1 + 3 wasted / 3) = 24; one stop x 3 squared x (1 + 1/1) = 18.
	if len(rep.Findings) != 2 || rep.Findings[0].Category != PermRefused || rep.Findings[0].Score != 24 || rep.Findings[1].Score != 18 {
		t.Errorf("%+v", rep.Findings)
	}
	if got, _ := Mine([]string{log(t, filepath.Join(t.TempDir(), "s2"), evs...)}, Options{MinCount: 2}); len(got.Findings) != 1 {
		t.Errorf("MinCount 2 must drop what happened once: %+v", got.Findings)
	}
	if got, _ := Mine([]string{log(t, filepath.Join(t.TempDir(), "s3"), evs...)}, Options{Examples: 1}); len(got.Findings[0].Examples) != 1 {
		t.Errorf("Examples 1: %+v", got.Findings[0].Examples)
	}
}

// Every events.jsonl under a directory is a session: a run directory of rollouts, or ~/.sleipnir/sessions.
func TestMineWalksDirectoriesAndNamesSessions(t *testing.T) {
	root := t.TempDir()
	one := []ev{response("r1"), toolCall("c", "bash", map[string]any{"command": "cd /a"}), toolFail("c", "bash", "permission")}
	log(t, filepath.Join(root, "run", "task-1", "0"), one...)
	log(t, filepath.Join(root, "run", "task-1", "1"), one...)
	log(t, filepath.Join(root, "run", "task-2", "0", "blobs"), one...) // blobs are not sessions
	rep, err := Mine([]string{root}, Options{})
	if err != nil {
		t.Fatal(err)
	}
	f := find(rep, PermRefused, "cd <path>")
	if rep.Sessions != 2 || f == nil || f.Count != 2 || f.Sessions != 2 {
		t.Fatalf("sessions %d: %+v", rep.Sessions, f)
	}
	if want := "run/task-1/0"; f.Examples[0].Session != want {
		t.Errorf("session named %q, want %q", f.Examples[0].Session, want)
	}
	if _, err := Mine([]string{t.TempDir()}, Options{}); err == nil || !strings.Contains(err.Error(), "no events.jsonl") {
		t.Errorf("an empty directory: %v", err)
	}
	if _, err := Mine([]string{filepath.Join(root, "nope")}, Options{}); err == nil {
		t.Error("a missing path must be an error")
	}
}

func TestCommandKey(t *testing.T) {
	for in, want := range map[string]string{
		"cd /workspace && ls":                "cd <path>",
		"cd \"$(pwd)\" && go test ./...":     "cd <path>",
		"GOFLAGS=-mod=mod go test ./...":     "go test",
		"sed -n '1,5p' main.go":              "sed -n",
		"grep -rn foo src":                   "grep -rn",
		"git diff HEAD~1":                    "git diff",
		"":                                   "(empty)",
		"cat > /tmp/x.go << 'EOF'\npkg\nEOF": "cat >",
	} {
		if got := commandKey(in); got != want {
			t.Errorf("commandKey(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestNormaliseKeepsWhatIsTheSameAndDropsWhatVaries(t *testing.T) {
	a := normalise("reads /workspace outside the workspace (/root/.bench/w/mut-1/tree); approval needed after 12 tries")
	b := normalise("reads /repo outside the workspace (/home/u/x/tree); approval needed after 3 tries")
	if a != b || !strings.Contains(a, "outside the workspace") {
		t.Errorf("%q vs %q", a, b)
	}
}

// A response that the output limit cut off is a full-length generation that did not finish: it ranks as waste, with its request
// counted, and the example says how long it went on. A response that stopped for any other reason is not one.
func TestResponsesCutOffAtTheOutputLimitAreFound(t *testing.T) {
	cut := func(req string, tokens, ms int) ev {
		return ev{"a", events.TypeModelResponse, map[string]any{"req": req, "stop": "max_tokens", "total_ms": ms, "usage": map[string]any{"output_tokens": tokens}}}
	}
	ended := ev{"a", events.TypeModelResponse, map[string]any{"req": "r3", "stop": "end_turn", "total_ms": 900, "usage": map[string]any{"output_tokens": 40}}}
	dir := log(t, filepath.Join(t.TempDir(), "s1"), cut("r1", 16000, 189729), cut("r2", 16000, 120000), ended)
	rep, err := Mine([]string{dir}, Options{})
	if err != nil {
		t.Fatal(err)
	}
	f := find(rep, OutputLimit, "max_tokens")
	if f == nil || f.Count != 2 || f.Wasted != 2 || f.Severity != S2 {
		t.Fatalf("output limit: %+v", f)
	}
	if len(f.Examples) == 0 || !strings.Contains(f.Examples[0].Detail, "16000 output tokens in 190 s") {
		t.Errorf("examples: %+v", f.Examples)
	}
	if f.Hint == "" {
		t.Error("no hint")
	}
	if rep.Requests != 3 {
		t.Errorf("requests %d, want 3", rep.Requests)
	}
}

func TestReadCounterCountsRereadsOfUnchangedParts(t *testing.T) {
	var c ReadCounter
	whole := json.RawMessage(`{"path":"/w/a.go"}`)
	window := json.RawMessage(`{"path":"/w/a.go","offset":80}`)
	for i, want := range []int{1, 2} {
		if _, _, n := c.Read("/w/a.go", whole); n != want {
			t.Fatalf("read %d of the whole file: n = %d, want %d", i+1, n, want)
		}
	}
	if _, what, n := c.Read("/w/a.go", window); n != 1 || what != "/w/a.go (offset 80)" {
		t.Fatalf("another range is another part: n = %d, what = %q", n, what)
	}
	c.Changed("a.go")
	if _, _, n := c.Read("/w/a.go", whole); n != 1 {
		t.Fatalf("a read after a change of the relatively spelled file starts over: n = %d", n)
	}
	parts, counts := c.Counts()
	if len(parts) != 2 || counts[0] != 1 || counts[1] != 0 {
		t.Fatalf("Counts = %q %v", parts, counts)
	}
}

// A write resets the reads of the same file, spelled relatively or absolutely, but never those of another file that only shares
// the tail of its path.
func TestChangedDoesNotMatchTwoRelativePathsBySuffix(t *testing.T) {
	var c ReadCounter
	read := func(p string) int { _, _, n := c.Read(p, json.RawMessage(`{"path":"`+p+`"}`)); return n }
	read("a.go")
	read("a.go")
	read("/w/src/b.go")
	read("/w/src/b.go")
	c.Changed("src/a.go") // another file than a.go
	if n := read("a.go"); n != 3 {
		t.Errorf("a write to src/a.go reset the reads of a.go: the third read counts %d", n)
	}
	c.Changed("./a.go") // the same file
	if n := read("a.go"); n != 1 {
		t.Errorf("a write to ./a.go did not reset the reads of a.go: %d", n)
	}
	c.Changed("src/b.go") // the relative spelling of an absolute path it ends
	if n := read("/w/src/b.go"); n != 1 {
		t.Errorf("a write to src/b.go did not reset the reads of /w/src/b.go: %d", n)
	}
	c.Changed("/w/src/b.go")
	read("src/b.go")
	read("src/b.go")
	c.Changed("/w/src/b.go") // the absolute spelling of a relative path
	if n := read("src/b.go"); n != 1 {
		t.Errorf("a write to /w/src/b.go did not reset the reads of src/b.go: %d", n)
	}
}
