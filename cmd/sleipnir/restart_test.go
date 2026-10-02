package main

import (
	"bytes"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"slices"
	"strings"
	"testing"

	"github.com/anemos-labs/sleipnir/internal/checkpoint"
	"github.com/anemos-labs/sleipnir/internal/core"
	"github.com/anemos-labs/sleipnir/internal/perm"
	"github.com/anemos-labs/sleipnir/internal/session"
	"github.com/anemos-labs/sleipnir/internal/tui/app"
)

func TestSplitArgsHonoursQuotesAndNothingElse(t *testing.T) {
	for in, want := range map[string][]string{
		``:          nil,
		`--swarm 8`: {"--swarm", "8"},
		`--verify "go test {dirs}" --isolation worktree`: {"--verify", "go test {dirs}", "--isolation", "worktree"},
		`--verify 'make test' x`:                         {"--verify", "make test", "x"},
		`  a   b  `:                                      {"a", "b"},
		`--x ""`:                                         {"--x", ""},
		`a$HOME \n`:                                      {`a$HOME`, `\n`},
	} {
		if got := splitArgs(in); !reflect.DeepEqual(got, want) {
			t.Errorf("%q: %q, want %q", in, got, want)
		}
	}
}

func TestParseBudget(t *testing.T) {
	for in, want := range map[string]float64{"5": 5, "$5": 5, "0.50": 0.5, "off": 0, "NONE": 0, "0": 0} {
		if got, err := parseBudget(in); err != nil || got != want {
			t.Errorf("%q: %v %v", in, got, err)
		}
	}
	for _, bad := range []string{"", "lots", "-1", "NaN", "Inf"} {
		if _, err := parseBudget(bad); err == nil {
			t.Errorf("%q was accepted", bad)
		}
	}
}

func TestProgramCommandsAnswerWithoutASession(t *testing.T) {
	h := &sessionHost{} // /verbose, /anim and the usage of /swarm never touch the session
	var out bytes.Buffer
	res, ok := h.programCommand("/verbose", &out)
	if !ok || res.Verbose != "on" {
		t.Errorf("/verbose: %+v %v", res, ok)
	}
	res, _ = h.programCommand("/verbose off", &out)
	if res.Verbose != "off" {
		t.Errorf("/verbose off: %+v", res)
	}
	res, _ = h.programCommand("/anim off", &out)
	if res.Anim != "off" {
		t.Errorf("/anim off: %+v", res)
	}
	out.Reset()
	if res, _ = h.programCommand("/anim maybe", &out); res.Anim != "" || !strings.Contains(out.String(), "usage: /anim [on|off]") {
		t.Errorf("/anim maybe: %+v %q", res, out.String())
	}
	out.Reset()
	if res, _ = h.programCommand("/swarm", &out); res.Restart != nil || !strings.Contains(out.String(), "usage: /swarm <agents>") {
		t.Errorf("/swarm alone: %+v %q", res, out.String())
	}
	if _, ok := h.programCommand("/cost", &out); ok {
		t.Error("/cost is not a program command")
	}
}

// /new, /resume and /restart build the arguments of the chat that follows: the model and the mode stay, a fresh conversation resumes nothing,
// and the person's own --resume is not doubled.
func TestRestartArgsKeepModelAndModeAndDecideWhatResumes(t *testing.T) {
	s := chatSession(t, false, nil)
	args, err := restartArgs(s, []string{"--no-mcp"}, false)
	if err != nil || !contains(args, "--no-mcp") || !contains(args, "--mode") {
		t.Fatalf("%v %v", args, err)
	}
	if args, _ := restartArgs(s, nil, true); contains(args, "--resume") || contains(args, "--continue") {
		t.Errorf("/new must start empty: %v", args)
	}
	if args, _ := restartArgs(s, []string{"--resume", "20260101-000000-abcdef"}, false); count(args, "--resume") != 1 {
		t.Errorf("the person's --resume is the only one: %v", args)
	}
	if _, err := restartArgs(s, []string{"fix the bug"}, false); err == nil {
		t.Error("a goal after /restart is refused: it is typed at the prompt")
	}
}

// What the person allowed while the session ran (/allow, "don't ask again") goes with the restart: the session continues, and it asked
// for the same go test again after /swarm, /model and /restart.
func TestRestartArgsCarryWhatTheSessionAllowed(t *testing.T) {
	s := chatSession(t, false, nil)
	s.Perm.AddRule(perm.ScopeSession, perm.Rule{Action: perm.Allow, Tool: "Bash", Pattern: "go test:*"})
	s.Perm.AddRule(perm.ScopeSession, perm.Rule{Action: perm.Allow, Tool: "Bash", Pattern: "go test:*"}) // said twice: carried once
	args, err := restartArgs(s, []string{"--no-mcp"}, false)
	if err != nil {
		t.Fatal(err)
	}
	if !contains(args, "Bash(go test:*)") || count(args, "--allow") != 1 {
		t.Errorf("the grant is not in %v", args)
	}
	// And a person who names the rule again is not asked twice.
	args, _ = restartArgs(s, []string{"--allow", "Bash(go test:*)"}, false)
	if count(args, "Bash(go test:*)") != 1 {
		t.Errorf("the rule is given twice: %v", args)
	}
}

func contains(xs []string, x string) bool { return count(xs, x) > 0 }

func count(xs []string, x string) (n int) {
	for _, v := range xs {
		if v == x {
			n++
		}
	}
	return
}

// /roles shows who runs on which model and where each choice came from; changing a role restarts with the flag, keeping the ones named before;
// a single agent has one model, so only its compactor can be named.
func TestRolesShowsTheTableAndRestartsToChangeOne(t *testing.T) {
	t.Setenv("TOGETHER_API_KEY", "k") // a role's model is checked before the chat is ended: its provider needs a key
	solo := &sessionHost{s: chatSession(t, false, nil)}
	var out strings.Builder
	if _, ok := solo.programCommand("/roles", &out); !ok || !strings.Contains(out.String(), "default") || !strings.Contains(out.String(), "compactor") || !strings.Contains(out.String(), "no compactor model is set") {
		t.Errorf("solo table:\n%s", out.String())
	}
	out.Reset()
	if res, _ := solo.programCommand("/roles worker=a/b", &out); res.Restart != nil || !strings.Contains(out.String(), "single agent") {
		t.Errorf("a single agent has no workers: %+v %q", res, out.String())
	}
	out.Reset()
	if res, _ := solo.programCommand("/roles compactor=ollama/qwen3:8b", &out); !contains(res.Restart, "compactor=ollama/qwen3:8b") || !contains(res.Restart, "--role-model") {
		t.Errorf("the compactor can be named: %+v %q", res, out.String())
	}
	out.Reset()
	if res, _ := solo.programCommand("/roles nonsense", &out); res.Restart != nil || !strings.Contains(out.String(), "usage: /roles") {
		t.Errorf("usage: %+v %q", res, out.String())
	}
	swarm := &sessionHost{s: chatSessionWith(t, false, nil, func(o *session.Options) {
		o.Swarm, o.MaxAgents = true, 3
	}, nil)}
	out.Reset()
	swarm.programCommand("/roles", &out)
	if !strings.Contains(out.String(), "manager") || !strings.Contains(out.String(), "backend") || !strings.Contains(out.String(), "the session's model") {
		t.Errorf("a team's table names its roles and where each came from:\n%s", out.String())
	}
	out.Reset()
	res, _ := swarm.programCommand("/roles backend=together/a", &out)
	if !contains(res.Restart, "backend=together/a") || !contains(res.Restart, "--role-model") {
		t.Errorf("changing a worker's model restarts with the flag: %v", res.Restart)
	}
	if !contains(res.Restart, "--swarm") || !contains(res.Restart, "3") {
		t.Errorf("a team stays a team (--swarm 3, three agents in all) after a role change: %v", res.Restart)
	}
	// the role named on the line beats the one the session started with
	named := chatSessionWith(t, false, nil, func(o *session.Options) {
		o.Swarm, o.MaxAgents = true, 3
		o.RoleModels = map[string]string{"backend": "together/a", "scout": "together/b"}
	}, nil)
	args, _ := restartArgs(named, []string{"--role-model", "backend=together/c"}, false)
	if contains(args, "backend=together/a") || !contains(args, "backend=together/c") || !contains(args, "scout=together/b") {
		t.Errorf("roles merge, the typed one wins: %v", args)
	}
}

// On a terminal the chat is a team of eight agents, the manager included unless --swarm says otherwise; the person's own ceiling (swarm.max_agents) is
// kept, and a single agent that restarts stays a single agent.
func TestDefaultTeamAndASoloRestartStaysSolo(t *testing.T) {
	_, home := projectDir(t)
	if n := defaultTeam(); n != 8 {
		t.Errorf("default team = %d agents, want 8", n)
	}
	if err := os.MkdirAll(filepath.Join(home, ".sleipnir"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, ".sleipnir", "config.json"), []byte(`{"swarm":{"max_agents":4}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if n := defaultTeam(); n != 4 {
		t.Errorf("under a ceiling of 4 agents the team is 4 agents, got %d", n)
	}
	args, _ := restartArgs(chatSession(t, false, nil), nil, true)
	if !slices.Contains(args, "--swarm") || args[slices.Index(args, "--swarm")+1] != "0" {
		t.Errorf("a single agent restarts as one: %v", args)
	}
}

// A model of the ChatGPT plan cannot be used before there is a sign-in, and the chat says so before it ends anything.
func TestModelOfThePlanNeedsASignInBeforeTheChatEnds(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("USERPROFILE", os.Getenv("HOME"))
	t.Setenv("TOGETHER_API_KEY", "k")
	team := &sessionHost{s: chatSessionWith(t, false, nil, func(o *session.Options) { o.Swarm, o.MaxAgents = true, 4 }, nil)}
	var out strings.Builder
	res, ok := team.programCommand("/model chatgpt/some-model", &out)
	if !ok || res.Restart != nil || !strings.Contains(out.String(), "/login chatgpt") {
		t.Errorf("not signed in: nothing restarts, and the way to sign in is named: %v %q", res.Restart, out.String())
	}
}

// A team has no single model, but it has a default one, and the chat is a team by default: /model must work there. It starts the team
// again on the new model (keeping the roles that name their own), and a reference that cannot be used is refused before anything is ended.
func TestModelInATeamStartsItAgainOnTheNewModel(t *testing.T) {
	t.Setenv("TOGETHER_API_KEY", "k")
	t.Setenv("GROQ_API_KEY", "") // no key: a model of Groq cannot be used
	team := &sessionHost{s: chatSessionWith(t, false, nil, func(o *session.Options) {
		o.Swarm, o.MaxAgents = true, 4
		o.RoleModels = map[string]string{"scout": "together/small"}
	}, nil)}
	var out strings.Builder
	res, ok := team.programCommand("/model together/big", &out)
	if !ok || !contains(res.Restart, "--model") || !contains(res.Restart, "together/big") || !contains(res.Restart, "--swarm") || !contains(res.Restart, "scout=together/small") {
		t.Fatalf("the team restarts on the new model, a team, with its roles: %v\n%s", res.Restart, out.String())
	}
	if count(res.Restart, "--model") != 1 {
		t.Errorf("one --model, the new one: %v", res.Restart)
	}
	out.Reset()
	if res, ok = team.programCommand("/model groq/zzz", &out); !ok || res.Restart != nil || !strings.Contains(out.String(), "model:") {
		t.Errorf("a reference that cannot be used ends nothing: %v %q", res.Restart, out.String())
	}
	out.Reset()
	if res, ok = team.programCommand("/roles scout=groq/zzz", &out); !ok || res.Restart != nil || !strings.Contains(out.String(), "roles: scout=groq/zzz") {
		t.Errorf("a role's model that cannot be used ends nothing: %v %q", res.Restart, out.String())
	}
	// a single agent moves its own conversation, which is slashTo's, and without an argument /model only says which model it is
	solo := &sessionHost{s: chatSession(t, false, nil)}
	if _, ok := solo.programCommand("/model together/big", &out); ok {
		t.Error("a single agent's /model is not a restart")
	}
	if _, ok := team.programCommand("/model", &out); ok {
		t.Error("/model alone only shows the model")
	}
}

// /login cannot be hosted by the program (a key is typed hidden, a browser sign-in prints an address): it ends the chat as a restart that
// signs in first and then comes back, a single agent or a team's manager with its conversation and its mode.
func TestLoginInTheChatLeavesItToSignInAndComesBack(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	solo := &sessionHost{s: chatSession(t, false, nil)}
	var out strings.Builder
	res, ok := solo.programCommand("/login groq", &out)
	if !ok || res.Restart == nil || !contains(res.Restart, "--mode") || count(res.Restart, "--swarm") != 1 || !reflect.DeepEqual(solo.first, []string{"login", "groq"}) ||
		!strings.Contains(out.String(), "comes back where you were") {
		t.Errorf("/login groq: restart %v, first %v\n%s", res.Restart, solo.first, out.String())
	}
	for _, line := range []string{"/login chatgpt", "/login"} { // the plan, and the menu that asks
		solo.first, out = nil, strings.Builder{}
		if res, ok = solo.programCommand(line, &out); !ok || res.Restart == nil || len(solo.first) == 0 || solo.first[0] != "login" {
			t.Errorf("%s: restart %v, first %v\n%s", line, res.Restart, solo.first, out.String())
		}
	}
	// a name that takes no key, and too many words, end nothing
	for line, want := range map[string]string{"/login nonsense": "not a provider that takes a key", "/login a b": "usage: /login [provider]"} {
		solo.first, out = nil, strings.Builder{}
		if res, ok = solo.programCommand(line, &out); !ok || res.Restart != nil || solo.first != nil || !strings.Contains(out.String(), want) {
			t.Errorf("%s: restart %v, first %v, said %q (want %q)", line, res.Restart, solo.first, out.String(), want)
		}
	}
	team := &sessionHost{s: chatSessionWith(t, false, nil, func(o *session.Options) { o.Swarm, o.MaxAgents = true, 4 }, nil)}
	out = strings.Builder{}
	if res, ok = team.programCommand("/login", &out); !ok || !contains(res.Restart, "--swarm") || contains(res.Restart, "--resume") || !strings.Contains(out.String(), "comes back where you were") {
		t.Errorf("a team comes back as a team (with nothing to resume yet: it has not finished a turn): %v\n%s", res.Restart, out.String())
	}
}

// What runs on the terminal before the chat comes back is run first, whatever it comes to, and the process ends with the chat's status.
func TestTheCommandThatRunsFirstIsFollowedByTheChat(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the stand-in program is a shell script")
	}
	dir := t.TempDir()
	logFile := filepath.Join(dir, "log")
	stub := filepath.Join(dir, "sleipnir")
	script := "#!/bin/sh\necho \"$*\" >> \"$STUB_LOG\"\n[ \"$1\" = login ] && exit 3\n[ \"$1\" = chat ] && exit 5\nexit 0\n"
	if err := os.WriteFile(stub, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("STUB_LOG", logFile)
	code, err := runChildren(stub, []string{"--model", "x/y", "--mode", "default"}, []string{"login", "heimdall"})
	got, _ := os.ReadFile(logFile)
	if err != nil || code != 5 || string(got) != "login heimdall\nchat --model x/y --mode default\n" {
		t.Errorf("status %d, error %v, ran:\n%s", code, err, got)
	}
	os.Remove(logFile)
	if code, err = runChildren(stub, nil, nil); err != nil || code != 5 {
		t.Errorf("nothing first: status %d, error %v", code, err)
	}
	if got, _ = os.ReadFile(logFile); string(got) != "chat\n" {
		t.Errorf("nothing runs before the chat when there is nothing to run: %q", got)
	}
}

// A resumed chat says where the conversation was: the last thing the person asked and the start of what it answered (the screen was empty).
func TestRecapLinesSayWhereAResumedConversationWas(t *testing.T) {
	turns := []core.Turn{
		{Role: core.RoleUser, Blocks: []core.Block{core.Text("first goal")}},
		{Role: core.RoleAssistant, Blocks: []core.Block{core.Text("did the first")}},
		{Role: core.RoleUser, Blocks: []core.Block{core.Text("fix the failing test\nin slug")}},
		{Role: core.RoleAssistant, Blocks: []core.Block{core.Text("Fixed: slug.go joined with _")}},
		{Role: core.RoleUser, Origin: core.OriginSystem, Blocks: []core.Block{core.Text("[harness] a note")}},
		{Role: core.RoleUser, Origin: core.OriginTool, Blocks: []core.Block{core.ToolResult("c1", false, core.Text("ok"))}},
	}
	got := recapLines(turns)
	want := []string{"you asked: fix the failing test in slug", "it said: Fixed: slug.go joined with _"}
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Errorf("recap %q, want %q: the harness's own turns are not what the person asked", got, want)
	}
	// A goal that was cancelled has no answer, and the answer of the goal before it is not the answer to this one.
	cancelled := append(turns[:4:4], core.Turn{Role: core.RoleUser, Blocks: []core.Block{core.Text("run the tests")}})
	if got := recapLines(cancelled); len(got) != 2 || got[0] != "you asked: run the tests" || !strings.Contains(got[1], "had not answered") {
		t.Errorf("a goal that was cancelled: %q", got)
	}
	if got := recapLines(nil); len(got) != 0 {
		t.Errorf("a conversation with no turns recaps %q", got)
	}
}

// /resume with nothing to resume says so and leaves the chat open: it used to restart the chat, which then ended with the error of a start that
// could not happen.
func TestResumeWithNothingToResumeStaysInTheChat(t *testing.T) {
	h := &sessionHost{s: chatSession(t, false, nil)}
	for _, line := range []string{"/resume", "/resume nosuchid"} {
		var out strings.Builder
		res, ok := h.programCommand(line, &out)
		if !ok || res.Restart != nil || !strings.Contains(out.String(), "resume:") {
			t.Errorf("%s: handled %v, restart %v, said %q", line, ok, res.Restart, out.String())
		}
	}
}

// /goal in the chat: setting one says how to stop it and sends the goal; pause, resume and clear do what they say, and the state is in the session log.
func TestGoalCommandSetsPausesResumesAndClears(t *testing.T) {
	h := &sessionHost{s: chatSession(t, false, nil)}
	run := func(line string) (app.CommandResult, string) {
		var out strings.Builder
		res, ok := h.programCommand(line, &out)
		if !ok {
			t.Fatalf("%s was not handled", line)
		}
		return res, out.String()
	}
	if res, out := run("/goal"); res.Send != "" || !strings.Contains(out, "no goal") {
		t.Errorf("no goal yet: %+v %q", res, out)
	}
	res, out := run("/goal make the tests pass")
	if !strings.Contains(res.Send, "make the tests pass") || !strings.Contains(out, "Esc pauses it") || h.goal == nil {
		t.Errorf("setting a goal: %+v %q", res, out)
	}
	run("/goal pause")
	if h.goal.Paused == "" {
		t.Error("pause did not pause")
	}
	if res, _ := run("/goal resume"); !strings.Contains(res.Send, "continuation") || h.goal.Paused != "" {
		t.Errorf("resume: %+v paused %q", res, h.goal.Paused)
	}
	run("/goal clear")
	if h.goal != nil {
		t.Error("clear left the goal")
	}
}

// A run's last lines say which files it changed, or that it changed none (a run refused its edits ends with a diagnosis and a summary that look alike).
func TestChangedLineSaysWhatARunChanged(t *testing.T) {
	cp := func(files ...string) checkpoint.Info { return checkpoint.Info{Files: files} }
	for _, tc := range []struct {
		list []checkpoint.Info
		want string
	}{
		{nil, "   no file was changed"},
		{[]checkpoint.Info{cp()}, "   no file was changed"},
		{[]checkpoint.Info{cp("a.py"), cp("a.py", "b.py")}, "   changed: a.py, b.py"},
		{[]checkpoint.Info{cp("1", "2", "3", "4", "5", "6", "7")}, "   changed: 1, 2, 3, 4, 5 and 2 more"},
	} {
		if got := changedLine(tc.list); got != tc.want {
			t.Errorf("%v: %q, want %q", tc.list, got, tc.want)
		}
	}
}

// The recap of a resumed chat is a line of words: the fences of a code block in the answer are left out of it.
func TestRecapLeavesOutCodeFences(t *testing.T) {
	turns := []core.Turn{
		{Role: core.RoleUser, Blocks: []core.Block{core.Text("fix it")}},
		{Role: core.RoleAssistant, Blocks: []core.Block{core.Text("Done:\n```python\nx = 1\n```\nand tested")}},
	}
	got := strings.Join(recapLines(turns), "\n")
	if strings.Contains(got, "```") || !strings.Contains(got, "x = 1") {
		t.Errorf("the recap: %q", got)
	}
}

// A goal's messages carry the harness's own words; the recap of a resumed chat shows only what the person typed.
func TestRecapShowsTheGoalNotTheHarnessWords(t *testing.T) {
	turns := []core.Turn{
		{Role: core.RoleUser, Blocks: []core.Block{core.Text("make the tests pass\n\n[standing goal: before you begin, write what it requires as steps]")}},
		{Role: core.RoleAssistant, Blocks: []core.Block{core.Text("done")}},
	}
	got := strings.Join(recapLines(turns), "\n")
	if strings.Contains(got, "standing goal") || !strings.Contains(got, "you asked: make the tests pass") {
		t.Errorf("the recap: %q", got)
	}
}

// A swarm run that had a --verify command says that the gate ran, and what came of it.
func TestVerifyLineSaysWhatTheGateDid(t *testing.T) {
	runs := func(ran, failed int) func() (int, int) { return func() (int, int) { return ran, failed } }
	for _, c := range []struct {
		cmd         string
		ran, failed int
		want        string
	}{
		{"", 3, 0, ""},
		{"go test ./...", 0, 0, "the gate never ran"},
		{"go test ./...", 5, 0, "ran 5 times and passed every time"},
		{"go test ./...", 5, 2, "2 failed and the work was sent back"},
	} {
		got := verifyLine(c.cmd, runs(c.ran, c.failed))
		if (c.want == "") != (got == "") || !strings.Contains(got, c.want) {
			t.Errorf("verifyLine(%q, %d, %d) = %q, want it to hold %q", c.cmd, c.ran, c.failed, got, c.want)
		}
	}
}

// A /rewind puts files back; the model is told which, or it goes on quoting the edit it remembers as if it were on disk.
func TestRewindNoteNamesTheFilesPutBack(t *testing.T) {
	rep := checkpoint.RestoreReport{ID: "cp_0002", Files: []checkpoint.FileResult{
		{Path: "avg.py", Action: checkpoint.ActionRestore, Outcome: checkpoint.OutcomeDone},
		{Path: "old.txt", Action: checkpoint.ActionRestore, Outcome: checkpoint.OutcomeUnchanged},
	}}
	n := rewindNote(rep)
	if !strings.Contains(n, "avg.py") || strings.Contains(n, "old.txt") || !strings.Contains(n, "cp_0002") || !strings.Contains(n, "Read") {
		t.Errorf("the note is %q", n)
	}
	if n := rewindNote(checkpoint.RestoreReport{ID: "cp_0001"}); n != "" {
		t.Errorf("nothing was put back and the note is %q", n)
	}
	if n := rewindNote(checkpoint.RestoreReport{ID: "cp_0001", DryRun: true, Files: rep.Files}); n != "" {
		t.Errorf("a dry run changed nothing and the note is %q", n)
	}
}
