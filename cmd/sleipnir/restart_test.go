package main

import (
	"bytes"
	"github.com/anemos-labs/sleipnir/internal/session"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
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
	if res, _ = h.programCommand("/swarm", &out); res.Restart != nil || !strings.Contains(out.String(), "usage: /swarm <workers>") {
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
	h := &sessionHost{s: s}
	var out strings.Builder
	if res, ok := h.programCommand("/resume 20260101-000000-abcdef", &out); !ok || !contains(res.Restart, "--resume") {
		t.Errorf("/resume ID: %+v %v", res, ok)
	}
	if res, ok := h.programCommand("/resume", &out); !ok || !contains(res.Restart, "--continue") {
		t.Errorf("/resume: %+v %v", res, ok)
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
	if !contains(res.Restart, "--swarm") || !contains(res.Restart, "2") {
		t.Errorf("a team stays a team (--swarm 2) after a role change: %v", res.Restart)
	}
	// the role named on the line beats the one the session started with
	t.Setenv("TOGETHER_API_KEY", "k")
	named := chatSessionWith(t, false, nil, func(o *session.Options) {
		o.Swarm, o.MaxAgents = true, 3
		o.RoleModels = map[string]string{"backend": "together/a", "scout": "together/b"}
	}, nil)
	args, _ := restartArgs(named, []string{"--role-model", "backend=together/c"}, false)
	if contains(args, "backend=together/a") || !contains(args, "backend=together/c") || !contains(args, "scout=together/b") {
		t.Errorf("roles merge, the typed one wins: %v", args)
	}
}

// On a terminal the chat is a team of up to eight workers unless --swarm says otherwise; the person's own ceiling (swarm.max_agents) is
// kept, and a single agent that restarts stays a single agent.
func TestDefaultTeamAndASoloRestartStaysSolo(t *testing.T) {
	_, home := projectDir(t)
	if n := defaultTeam(); n != 8 {
		t.Errorf("default team = %d, want 8", n)
	}
	if err := os.MkdirAll(filepath.Join(home, ".sleipnir"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, ".sleipnir", "config.json"), []byte(`{"swarm":{"max_agents":4}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if n := defaultTeam(); n != 3 {
		t.Errorf("under a ceiling of 4 agents the team is a manager and 3 workers, got %d workers", n)
	}
	args, _ := restartArgs(chatSession(t, false, nil), nil, true)
	if !slices.Contains(args, "--swarm") || args[slices.Index(args, "--swarm")+1] != "0" {
		t.Errorf("a single agent restarts as one: %v", args)
	}
}
