package runner

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/anemos-labs/sleipnir/internal/sched"
	"github.com/anemos-labs/sleipnir/internal/testutil"
)

// TestMain runs the tests with a leak check, and lets the test binary stand in for `sleipnir` when a run starts it (RUNNER_FAKE=1):
// the runner's children are this program, and what each fake command does is below.
func TestMain(m *testing.M) {
	switch os.Getenv("RUNNER_FAKE") {
	case "1":
		os.Exit(fakeSleipnir(os.Args[1:]))
	case "sleep":
		time.Sleep(10 * time.Minute)
		os.Exit(0)
	}
	code := testutil.CheckLeaks(m)
	if code == 0 {
		if kids := children(); len(kids) > 0 {
			fmt.Fprintf(os.Stderr, "child processes left behind: %v\n", kids)
			code = 1
		}
	}
	os.Exit(code)
}

// fakeSleipnir is what the test binary does as a child of the runner.
func fakeSleipnir(args []string) int {
	sched.ReceiveKeys() // as main does, first
	if len(args) == 0 {
		return 2
	}
	switch args[0] {
	case "sim":
		n, _ := strconv.Atoi(os.Getenv("RUNNER_FAKE_LINES"))
		if n == 0 {
			n = 5
		}
		for i := range n {
			fmt.Printf("line %d\n", i)
		}
		fmt.Fprintln(os.Stderr, "a note on stderr")
		return 0
	case "mock":
		fmt.Println("serving")
		time.Sleep(10 * time.Minute)
		return 0
	case "recon":
		cmd := exec.Command(os.Args[0])
		cmd.Env = append(os.Environ(), "RUNNER_FAKE=sleep")
		cmd.Stdout = os.Stdout // the grandchild holds the output open, as a server a command starts would
		if err := cmd.Start(); err != nil {
			fmt.Println("no grandchild:", err)
			return 1
		}
		fmt.Printf("grandchild %d\n", cmd.Process.Pid)
		time.Sleep(10 * time.Minute)
		return 0
	case "friction":
		fmt.Println(strings.Repeat("x", 10000))
		fmt.Println("after")
		return 0
	case "doctor":
		fmt.Println("doctor key:", os.Getenv("RUNNERTEST_API_KEY") != "")
		fmt.Fprintln(os.Stderr, "probing m-1 at http://127.0.0.1:1/v1 (key from none)")
		for _, l := range []string{"basic", "  ✓ basic            12ms  in=10 out=5 cost=false", "tools", "  ✓ tools            34ms  in=20 cached=0 out=8",
			"    ! it answered, but did not call the offered tool", "cache", "  ✓ cache            56ms  in=900 cached=800 out=1", "  ✗ cache-2        connection refused"} {
			fmt.Fprintln(os.Stderr, l)
		}
		b, _ := json.MarshalIndent(map[string]any{"model": "m-1", "steps": []map[string]any{{"name": "basic", "ok": true}},
			"findings": map[string]any{"streaming": true, "tools": false, "cache_works": true, "cache_hit_ratio": 0.89, "cache_repeats": 3, "cache_repeat_hits": 3, "notes": []string{"a note"}}}, "", "  ")
		fmt.Println(string(b))
		return 0
	case "run":
		fmt.Println("argv:", strings.Join(args, "|"))
		if d := os.Getenv("RUNNER_FAKE_SLEEP"); d != "" {
			dur, _ := time.ParseDuration(d)
			time.Sleep(dur)
		}
		return 3
	}
	fmt.Println("argv:", strings.Join(args, "|"))
	fmt.Println("key:", os.Getenv("RUNNERTEST_API_KEY"))
	sum := sha256.Sum256([]byte(os.Getenv("RUNNERTEST_API_KEY")))
	fmt.Println("key-sha:", hex.EncodeToString(sum[:8]))
	if wd, err := os.Getwd(); err == nil {
		fmt.Println("cwd:", wd)
	}
	if d, err := time.ParseDuration(os.Getenv("RUNNER_FAKE_HOLD")); err == nil {
		time.Sleep(d)
	}
	return 0
}

// children lists the processes whose parent is this one (Linux; elsewhere none are found).
func children() []int {
	ents, err := os.ReadDir("/proc")
	if err != nil {
		return nil
	}
	var out []int
	me := os.Getpid()
	for _, e := range ents {
		pid, err := strconv.Atoi(e.Name())
		if err != nil {
			continue
		}
		b, err := os.ReadFile("/proc/" + e.Name() + "/stat")
		if err != nil {
			continue
		}
		// pid (comm) state ppid ...: the command may hold spaces and parentheses, the fields after its last ) do not
		s := string(b)
		i := strings.LastIndexByte(s, ')')
		if i < 0 {
			continue
		}
		f := strings.Fields(s[i+1:])
		if len(f) < 2 {
			continue
		}
		if ppid, _ := strconv.Atoi(f[1]); ppid == me && f[0] != "Z" {
			out = append(out, pid)
		}
	}
	return out
}

// alive reports whether a process exists and is not a zombie.
func alive(pid int) bool {
	b, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/stat")
	if err != nil {
		return false
	}
	s := string(b)
	i := strings.LastIndexByte(s, ')')
	return i >= 0 && !strings.HasPrefix(strings.TrimSpace(s[i+1:]), "Z")
}
