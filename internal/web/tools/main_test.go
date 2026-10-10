package tools

import (
	"encoding/json"
	"fmt"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/anemos-labs/sleipnir/internal/testutil"
)

// TestMain runs the tests with a leak check, and lets the test binary stand in for `sleipnir` when a route starts it (TOOLS_FAKE=1).
func TestMain(m *testing.M) {
	if os.Getenv("TOOLS_FAKE") == "1" {
		os.Exit(fakeSleipnir(os.Args[1:]))
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

// fakeSleipnir is what the test binary does as `sleipnir`.
func fakeSleipnir(args []string) int {
	if len(args) == 0 {
		return 2
	}
	switch args[0] {
	case "doctor":
		fmt.Fprintln(os.Stderr, "probing m-1 at http://127.0.0.1:1/v1 (key from $TOOLSTEST_API_KEY)")
		for _, l := range []string{"basic", "  ✓ basic            12ms  in=10 out=5 cost=false", "tools", "  ✓ tools            34ms  in=20 cached=0 out=8",
			"    ! it answered, but did not call the offered tool", "cache", "  ✓ cache            56ms  in=900 cached=800 out=1"} {
			fmt.Fprintln(os.Stderr, l)
		}
		fmt.Fprintln(os.Stderr, "key seen:", os.Getenv("TOOLSTEST_API_KEY"))
		b, _ := json.MarshalIndent(map[string]any{"model": "m-1", "steps": []map[string]any{{"name": "basic", "ok": true}, {"name": "tools", "ok": true}, {"name": "cache", "ok": true}},
			"findings":  map[string]any{"streaming": true, "ttfb": "12ms", "tools": false, "cache_works": true, "cache_hit_ratio": 0.89, "cache_repeats": 3, "cache_repeat_hits": 3, "notes": []string{"basic reply was empty"}},
			"total_usd": 0, "cost_complete": false}, "", "  ")
		fmt.Println(string(b))
		fmt.Fprintln(os.Stderr, "argv:", strings.Join(args, "|"))
		return 0
	case "run":
		fmt.Println("argv:", strings.Join(args, "|"))
		fmt.Println("key seen:", os.Getenv("TOOLSTEST_API_KEY"))
		if d := os.Getenv("TOOLS_FAKE_SLEEP"); d != "" {
			dur, _ := time.ParseDuration(d)
			time.Sleep(dur)
		}
		if os.Getenv("TOOLS_FAKE_FAIL") != "" {
			return 4
		}
		return 0
	}
	fmt.Println("argv:", strings.Join(args, "|"))
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
		s := string(b)
		i := strings.LastIndexByte(s, ')')
		if i < 0 {
			continue
		}
		f := strings.Fields(s[i+1:])
		if len(f) >= 2 && f[0] != "Z" {
			if ppid, _ := strconv.Atoi(f[1]); ppid == me {
				out = append(out, pid)
			}
		}
	}
	return out
}
