package repocheck

import (
	"path"
	"regexp"
	"strings"
	"testing"
)

// A test file that carries //go:build !race is left out of every run with the race detector, and every run of ci's test job
// has it on: what is in such a file (the allocation gates of the hot paths, whose counts the instrumentation changes) would never
// run, and the gate would only look like a guard. One step of the job runs those tests without -race. This test finds a test
// that step does not select, or a package it does not reach, as soon as such a test is written.
func TestTestsBuiltOnlyWithoutRaceAreRunByCI(t *testing.T) {
	notRace := regexp.MustCompile(`(?m)^//go:build\s+!race\s*$`)
	testFunc := regexp.MustCompile(`(?m)^func (Test\w+)\(`)
	type gate struct{ file, dir, name string }
	var gates []gate
	for _, f := range treeFiles(t) {
		if !strings.HasSuffix(f, "_test.go") {
			continue
		}
		src := read(t, f)
		header, _, _ := strings.Cut(src, "\npackage ")
		if !notRace.MatchString(header) {
			continue
		}
		for _, m := range testFunc.FindAllStringSubmatch(src, -1) {
			gates = append(gates, gate{f, path.Dir(f), m[1]})
		}
	}
	if len(gates) == 0 {
		t.Skip("no test is built only without the race detector")
	}

	testJob := workflowNamed(t, "ci.yml").job("test")
	if testJob == nil {
		t.Fatal("ci.yml has no job named test")
	}
	var args string
	for _, l := range lines(blocks(testJob.Text)) {
		if rest, ok := strings.CutPrefix(strings.TrimSpace(l), "go test"); ok && !strings.Contains(rest, "-race") {
			args = rest
		}
	}
	if args == "" {
		t.Fatalf("the test job of ci.yml runs every `go test` with -race, but %d test(s) are built only without it (%s: %s ...): add a step that runs them without -race",
			len(gates), gates[0].file, gates[0].name)
	}

	// -run 'Pattern' (or "Pattern", or an unquoted word), and the packages: ./... or a list of ./dir
	selected := regexp.MustCompile(`-run[ =]+(?:'([^']*)'|"([^"]*)"|(\S+))`).FindStringSubmatch(args)
	if selected == nil {
		t.Fatalf("the step of ci.yml that runs go test without -race (go test%s) has no -run: it would run the whole suite a second time; select the tests built only without -race", args)
	}
	pattern, err := regexp.Compile(selected[1] + selected[2] + selected[3])
	if err != nil {
		t.Fatalf("the -run pattern of the step without -race is not a regular expression: %v", err)
	}
	var pkgs []string
	for _, a := range strings.Fields(args) {
		if strings.HasPrefix(a, "./") {
			pkgs = append(pkgs, a)
		}
	}
	for _, g := range gates {
		if !pattern.MatchString(g.name) {
			t.Errorf("%s: %s is built only without -race, and the -run pattern %q of the step in ci.yml does not select it, so no run of ci would run it", g.file, g.name, pattern)
		}
		if !reachedBy(pkgs, g.dir) {
			t.Errorf("%s: the step in ci.yml that runs tests without -race names %v, which does not include ./%s", g.file, pkgs, g.dir)
		}
	}
}

// reachedBy reports whether one of go test's package arguments (./..., ./dir/..., ./dir) includes the directory dir.
func reachedBy(pkgs []string, dir string) bool {
	for _, p := range pkgs {
		switch {
		case p == "./...":
			return true
		case strings.HasSuffix(p, "/..."):
			base := strings.TrimPrefix(strings.TrimSuffix(p, "/..."), "./")
			if dir == base || strings.HasPrefix(dir, base+"/") {
				return true
			}
		case p == "./"+dir:
			return true
		}
	}
	return false
}

func TestReachedBy(t *testing.T) {
	for _, c := range []struct {
		pkgs []string
		dir  string
		want bool
	}{
		{[]string{"./..."}, "internal/kv", true},
		{[]string{"./internal/kv"}, "internal/kv", true},
		{[]string{"./internal/kv"}, "internal/kv/sim", false},
		{[]string{"./internal/kv/..."}, "internal/kv/sim", true},
		{[]string{"./internal/kv/..."}, "internal/kvx", false},
		{[]string{"./internal/core", "./internal/kv"}, "internal/kv", true},
		{nil, "internal/kv", false},
	} {
		if got := reachedBy(c.pkgs, c.dir); got != c.want {
			t.Errorf("reachedBy(%v, %q) = %v, want %v", c.pkgs, c.dir, got, c.want)
		}
	}
}
