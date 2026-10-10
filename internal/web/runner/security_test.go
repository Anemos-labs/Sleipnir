package runner

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"os"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/anemos-labs/sleipnir/internal/harden"
	"github.com/anemos-labs/sleipnir/internal/sched"
	"github.com/anemos-labs/sleipnir/internal/web/clispec"
	"github.com/anemos-labs/sleipnir/internal/web/wire"
)

// plantKey makes the server hold a provider key, as MoveKeys or a stored key would.
func plantKey(t *testing.T) (name, key string) {
	name, key = "RUNNERTEST_API_KEY", "sk-test-runner-environ-0123456789abcdefg"
	t.Setenv(name, "")
	harden.Provide(name, key)
	t.Cleanup(func() { harden.Provide(name, "") })
	return name, key
}

// environOfChildren reads the environment blocks of this process's children as /proc shows them to any process of the user, until
// stop is closed: what an unprivileged loop polling /proc/*/environ captures.
func environOfChildren(stop <-chan struct{}) <-chan string {
	out := make(chan string, 1)
	go func() {
		var seen strings.Builder
		me := strconv.Itoa(os.Getpid())
		for {
			ents, _ := os.ReadDir("/proc")
			for _, e := range ents {
				stat, err := os.ReadFile("/proc/" + e.Name() + "/stat")
				if err != nil {
					continue
				}
				i := strings.LastIndexByte(string(stat), ')')
				if f := strings.Fields(string(stat)[i+1:]); i < 0 || len(f) < 2 || f[1] != me {
					continue
				}
				if b, err := os.ReadFile("/proc/" + e.Name() + "/environ"); err == nil {
					seen.Write(b)
					seen.WriteByte('\n')
				}
			}
			select {
			case <-stop:
				out <- seen.String()
				return
			case <-time.After(2 * time.Millisecond):
			}
		}
	}()
	return out
}

// A "net" command gets the held keys on a pipe it reads at its start: a loop reading its /proc/<pid>/environ for its whole life never
// sees one, and the command has the key (it prints a hash of it).
func TestNetChildEnvironNeverHoldsTheKey(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("reads /proc")
	}
	_, key := plantKey(t)
	rg := newRig(t, Options{}, "RUNNER_FAKE_HOLD=600ms")
	stop := make(chan struct{})
	seen := environOfChildren(stop)
	id := rg.start(map[string]any{"path": []string{"models"}}, nil)
	rg.wait(id, 30*time.Second)
	close(stop)
	environ := <-seen
	sum := sha256.Sum256([]byte(key))
	if got := rg.waitLine(id, "key-sha:", time.Second); got != "key-sha: "+hex.EncodeToString(sum[:8]) {
		t.Fatalf("the command did not get the key: %q", got)
	}
	if !strings.Contains(environ, sched.KeysFDEnv+"=") {
		t.Fatalf("the loop never read the command's environment: %q", environ)
	}
	if strings.Contains(environ, key) {
		t.Fatal("the key was in the command's environment")
	}
}

// The three requests of the review: a key sent to an endpoint the request names, a server on every interface, and a run widened by
// its flags. Each needs a confirmation, and the first runs without any key once confirmed; a loopback server needs none.
func TestRequestsThatWidenARunNeedAConfirmation(t *testing.T) {
	_, key := plantKey(t)
	rg := newRig(t, Options{Grace: time.Second})
	exfil := map[string]any{"path": []string{"doctor"}, "flags": map[string]any{"provider": "openai", "base-url": "https://attacker.invalid/v1", "model": "x"}}
	for name, body := range map[string]map[string]any{
		"a key to a named endpoint":  exfil,
		"api-key-env":                {"path": []string{"models"}, "flags": map[string]any{"api-key-env": "RUNNERTEST_API_KEY"}},
		"every interface":            {"path": []string{"mock"}, "flags": map[string]any{"addr": "0.0.0.0:39777"}},
		"no host is every interface": {"path": []string{"mock"}, "flags": map[string]any{"addr": ":39777"}},
		"allow and trust":            {"path": []string{"run"}, "flags": map[string]any{"allow": "Bash(*)", "trust-project": true}, "pos": map[string]string{"PROMPT": "x"}},
		"trust spelled TRUE":         {"path": []string{"run"}, "flags": map[string]any{"trust-project": "TRUE"}, "pos": map[string]string{"PROMPT": "x"}},
		"verify":                     {"path": []string{"swarm"}, "flags": map[string]any{"verify": "make"}, "pos": map[string]string{"N": "2", "GOAL": "x"}},
		"yolo in capitals":           {"path": []string{"run"}, "flags": map[string]any{"mode": "YOLO"}, "pos": map[string]string{"PROMPT": "x"}},
		"perm-mode bypass":           {"path": []string{"rl", "rollout"}, "flags": map[string]any{"perm-mode": "bypass"}},
		"pass-env":                   {"path": []string{"rl", "eval"}, "flags": map[string]any{"pass-env": "HOME"}},
		"no-net-isolation":           {"path": []string{"rl", "tasks", "check"}, "flags": map[string]any{"no-net-isolation": "1"}, "pos": map[string]string{"FILE": "t.jsonl"}},
		"a policy host":              {"path": []string{"rl", "serve"}, "flags": map[string]any{"policy-host": "evil.invalid"}},
	} {
		w := rg.do(req{method: "POST", path: "/api/runs", body: body})
		if w.Code != http.StatusPreconditionRequired || !strings.HasPrefix(w.Header().Get("X-Confirm-Scope"), "run:") {
			t.Errorf("%s: %d %s, want 428", name, w.Code, w.Body.String())
		}
	}
	// confirmed, the endpoint run gets no key
	w := rg.do(req{method: "POST", path: "/api/runs", body: exfil})
	id := rg.start(exfil, map[string]string{"X-Confirm": rg.confirm(w.Header().Get("X-Confirm-Scope"))})
	rg.wait(id, 30*time.Second)
	if got := rg.waitLine(id, "doctor key:", time.Second); got != "doctor key: false" {
		t.Errorf("a run that names its endpoint got a key: %q", got)
	}
	// a loopback server, and the same flags switched off, run unconfirmed
	for name, body := range map[string]map[string]any{
		"loopback":       {"path": []string{"mock"}, "flags": map[string]any{"addr": "127.0.0.1:0"}},
		"localhost":      {"path": []string{"mock"}, "flags": map[string]any{"addr": "localhost:0"}},
		"ipv6 loopback":  {"path": []string{"mock"}, "flags": map[string]any{"addr": "[::1]:0"}},
		"trust off":      {"path": []string{"run"}, "flags": map[string]any{"trust-project": false}, "pos": map[string]string{"PROMPT": "x"}},
		"a mode allowed": {"path": []string{"run"}, "flags": map[string]any{"mode": "plan"}, "pos": map[string]string{"PROMPT": "x"}},
	} {
		w := rg.do(req{method: "POST", path: "/api/runs", body: body})
		if w.Code != http.StatusAccepted {
			t.Errorf("%s: %d %s, want 202", name, w.Code, w.Body.String())
			continue
		}
		var started wire.RunStarted
		_ = json.Unmarshal(w.Body.Bytes(), &started)
		rg.do(req{method: "DELETE", path: "/api/runs/" + started.ID})
		rg.wait(started.ID, 30*time.Second)
	}
	rg.host.mu.Lock()
	b, _ := json.Marshal(rg.host.frames)
	rg.host.mu.Unlock()
	if strings.Contains(string(b), key) {
		t.Fatal("the key reached a frame")
	}
}

// The decision is taken on the vector as Go's flag package parses it, whatever its spelling.
func TestJudgeParsesEverySpelling(t *testing.T) {
	lookup := func(p ...string) *wire.CLICommand {
		c, ok := clispec.Lookup(p)
		if !ok {
			t.Fatalf("no %v", p)
		}
		return c
	}
	run, mock, doctor := lookup("run"), lookup("mock"), lookup("doctor")
	for _, tc := range []struct {
		name   string
		c      *wire.CLICommand
		argv   []string
		mode   string
		noKeys bool
	}{
		{"-mode yolo", run, []string{"-mode", "yolo", "x"}, "priv", false},
		{"--mode=Bypass", run, []string{"--mode=Bypass"}, "priv", false},
		{"-trust-project=1", run, []string{"-trust-project=1"}, "priv", false},
		{"--trust-project=false", run, []string{"--trust-project=false", "goal"}, "net", false},
		{"-allow after a word", run, []string{"goal", "-allow", "Bash(go:*)"}, "priv", false},
		{"after --", run, []string{"--", "--mode=yolo"}, "net", false},
		{"-addr :8080", mock, []string{"-addr", ":8080"}, "priv", false},
		{"--addr=[::1]:9", mock, []string{"--addr=[::1]:9"}, "server", false},
		{"the default address", mock, nil, "server", false},
		{"-base-url u", doctor, []string{"-base-url", "http://x", "--model=m"}, "priv", true},
		{"--api-key-env=X", doctor, []string{"--api-key-env=X"}, "priv", true},
	} {
		parsed, err := parseArgv(tc.c, tc.argv)
		if err != nil {
			t.Errorf("%s: %v", tc.name, err)
			continue
		}
		mode, noKeys, reasons := judge(tc.c, parsed)
		if mode != tc.mode || noKeys != tc.noKeys || (mode == "priv") != (len(reasons) > 0) {
			t.Errorf("%s: %s noKeys=%v %v, want %s noKeys=%v", tc.name, mode, noKeys, reasons, tc.mode, tc.noKeys)
		}
	}
	if _, err := parseArgv(run, []string{"--bogus"}); err == nil {
		t.Error("a flag the command does not have")
	}
	if _, err := parseArgv(run, []string{"--trust-project=maybe"}); err == nil {
		t.Error("a bool that is not one")
	}
}
