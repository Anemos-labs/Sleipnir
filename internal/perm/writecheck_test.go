package perm

import (
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

// SetWriteCheck holds every write a request is known to make to the embedder's check, for shell commands as for the file tools,
// in every mode: a redirection, cp's destination and rm's operand are checked; reads, the scratch directory, and writes no static
// reading can pin down (an unlisted program's operands) are not. Other agents and a removed check are not affected.
func TestSetWriteCheckHoldsKnownWrites(t *testing.T) {
	f := newFixture(t)
	tmp := filepath.Join(f.base, "scratch")
	e, err := NewEngine(Config{Root: f.root, Home: f.home, Tmp: tmp, Mode: ModeBypass})
	if err != nil {
		t.Fatal(err)
	}
	var mu sync.Mutex
	var seen []string
	allowed := filepath.Join(f.root, "internal", "store")
	e.SetWriteCheck(func(agent, path string, tree bool) string {
		mu.Lock()
		seen = append(seen, path)
		mu.Unlock()
		if agent != "be-1" || path == allowed || strings.HasPrefix(path, allowed+string(filepath.Separator)) {
			return ""
		}
		return path + " is outside the scope of your task T1"
	})
	as := func(agent string, q rq) Decision {
		r := f.request(q)
		r.Agent, r.Cwd = agent, f.root
		return e.Check(bg, r)
	}
	cases := []struct {
		name, agent string
		q           rq
		allow       bool
	}{
		{"redirection inside the scope", "be-1", bash("echo x > internal/store/a.go"), true},
		{"redirection outside the scope", "be-1", bash("echo x > cmd/main.go"), false},
		{"cp into another area", "be-1", bash("cp internal/store/a.go cmd/a.go"), false},
		{"rm outside the scope", "be-1", bash("rm -rf cmd"), false},
		{"tee outside the scope", "be-1", bash("echo x | tee README.md"), false},
		{"a read outside the scope", "be-1", bash("cat cmd/main.go"), true},
		{"the scratch directory", "be-1", bash("echo x > " + filepath.Join(tmp, "probe.txt")), true},
		{"a file tool write outside the scope", "be-1", write("{root}/cmd/main.go"), false},
		{"another agent", "be-2", bash("echo x > cmd/main.go"), true},
	}
	for _, tc := range cases {
		d := as(tc.agent, tc.q)
		if d.Allow != tc.allow {
			t.Errorf("%s: allow=%v (%s), want %v", tc.name, d.Allow, d.Reason, tc.allow)
		}
		if !d.Allow && !strings.Contains(d.Reason, "outside the scope of your task T1") {
			t.Errorf("%s: the refusal should be the check's reason: %q", tc.name, d.Reason)
		}
	}
	e.AddRule(ScopeSession, Rule{Action: Allow, Tool: "Bash", Pattern: "echo:*"})
	if d := as("be-1", bash("echo x > cmd/main.go")); d.Allow {
		t.Error("an allow rule lifted the write check")
	}
	e.SetWriteCheck(nil)
	if d := as("be-1", bash("echo x > cmd/main.go")); !d.Allow {
		t.Errorf("a removed check still refuses: %s", d.Reason)
	}
}
