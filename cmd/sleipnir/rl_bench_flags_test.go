package main

import (
	"bytes"
	"context"
	"errors"
	"math"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/anemos-labs/sleipnir/internal/agent"
	"github.com/anemos-labs/sleipnir/internal/perm"
	"github.com/anemos-labs/sleipnir/internal/rl/env"
)

func TestModeAndSwarmFlagsSayWhoWorks(t *testing.T) {
	for _, tc := range []struct {
		name         string
		mode         string
		swarm        int
		wantSwarm    bool
		wantAgents   int
		wantSingle   bool
		wantErrMatch string
	}{
		{name: "the task's team", wantSwarm: false},
		{name: "--swarm", swarm: 3, wantSwarm: true, wantAgents: 2},
		{name: "swarm:N", mode: "swarm:5", wantSwarm: true, wantAgents: 4},
		{name: "swarm:N agrees with --swarm", mode: "swarm:3", swarm: 3, wantSwarm: true, wantAgents: 2},
		{name: "single", mode: "single", wantSingle: true},
		{name: "single against --swarm", mode: "single", swarm: 2, wantErrMatch: "contradict"},
		{name: "swarm:N against --swarm", mode: "swarm:4", swarm: 2, wantErrMatch: "different sizes"},
		{name: "swarm:0", mode: "swarm:0", wantErrMatch: "at least 2"},
		{name: "swarm:x", mode: "swarm:x", wantErrMatch: "swarm:N"},
		{name: "something else", mode: "duo", wantErrMatch: "single or swarm:N"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p := policyFlags{mode: tc.mode, swarm: tc.swarm}
			swarm, agents, single, err := p.team()
			if tc.wantErrMatch != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantErrMatch) {
					t.Fatalf("err = %v, want one mentioning %q", err, tc.wantErrMatch)
				}
				return
			}
			if err != nil || swarm != tc.wantSwarm || agents != tc.wantAgents || single != tc.wantSingle {
				t.Fatalf("team() = %v %d %v %v", swarm, agents, single, err)
			}
		})
	}
}

func TestPermissionFlags(t *testing.T) {
	mode, allow, err := (&policyFlags{}).permissions()
	if err != nil || mode != "" || allow != nil {
		t.Fatalf("defaults: %q %v %v", mode, allow, err)
	}
	mode, allow, err = (&policyFlags{permMode: "plan", allow: "Bash(go:*), Bash(git status:*)"}).permissions()
	if err != nil || mode != perm.ModePlan || len(allow) != 2 || allow[1] != "Bash(git status:*)" {
		t.Fatalf("explicit: %q %v %v", mode, allow, err)
	}
	if _, allow, _ = (&policyFlags{allow: "none"}).permissions(); allow == nil || len(allow) != 0 {
		t.Fatalf("none must mean no rules, not the defaults: %#v", allow)
	}
	if _, _, err = (&policyFlags{permMode: "yolo"}).permissions(); err == nil {
		t.Fatal("an unknown permission mode was accepted")
	}
}

func TestBudgetFlags(t *testing.T) {
	for _, bad := range []rigFlags{{budgetUSD: -1}, {maxSpend: math.NaN()}, {budgetUSD: math.Inf(1)}, {maxSteps: -3}, {maxRequests: -1}} {
		if err := bad.validate(); err == nil {
			t.Errorf("%+v was accepted", bad)
		}
	}
	var stderr bytes.Buffer
	rf := rigFlags{maxSteps: 40, maxRequests: 80, budgetUSD: 0.25}
	if b := rf.budget(&stderr); b.Steps != 40 || b.Requests != 80 || b.USD != 0.25 || stderr.Len() != 0 {
		t.Fatalf("budget = %+v, stderr %q", b, stderr.String())
	}
	// A run cap alone holds each rollout to a share of it, and says so.
	rf = rigFlags{maxSpend: 10, concurrency: 6}
	b := rf.budget(&stderr)
	if want := 10.0 / 12; math.Abs(b.USD-want) > 1e-9 || !strings.Contains(stderr.String(), "no --budget-usd") {
		t.Fatalf("budget = %+v, stderr %q", b, stderr.String())
	}
}

func TestNothingCompletedIsATemporaryFailure(t *testing.T) {
	err := nothingCompleted("rl rollout", &env.Summary{Rollouts: 4, Infra: 3, Capped: 1})
	if err == nil {
		t.Fatal("a run in which nothing completed succeeded")
	}
	var buf bytes.Buffer
	if code := reportError(&buf, err); code != exitTempFail {
		t.Fatalf("exit status %d, want %d", code, exitTempFail)
	}
	if !strings.Contains(buf.String(), "3 infrastructure failures") || !strings.Contains(buf.String(), "resume") {
		t.Errorf("message: %q", buf.String())
	}
	for _, s := range []*env.Summary{nil, {}, {Rollouts: 4, Completed: 1, Infra: 3}, {Rollouts: 4, Interrupted: true}} {
		if err := nothingCompleted("rl rollout", s); err != nil {
			t.Errorf("%+v: %v", s, err)
		}
	}
	// Plain errors still exit 1.
	if code := reportError(&buf, errors.New("boom")); code != 1 {
		t.Errorf("a plain error exits %d", code)
	}
}

// A run against an endpoint that is down completes nothing, and says so with the status a script resumes on.
func TestRLRolloutAgainstADeadEndpointExitsWithTempFail(t *testing.T) {
	if testing.Short() {
		t.Skip("end-to-end")
	}
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("SLEIPNIR_HOME", filepath.Join(home, ".sleipnir"))
	for _, k := range []string{"HEIMDALL_API_KEY", "OPENROUTER_API_KEY", "OPENAI_API_KEY"} {
		t.Setenv(k, "")
	}
	agent.RetryBase = time.Millisecond
	dead := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { http.Error(w, "down", http.StatusBadGateway) }))
	defer dead.Close()

	repo := gitRepo(t)
	dir := t.TempDir()
	work, tasksFile := filepath.Join(dir, "work"), filepath.Join(dir, "tasks", "tasks.jsonl")
	shared := []string{"--no-net-isolation", "--set-env", "GOFLAGS=-mod=mod", "--set-env", "GOTOOLCHAIN=local"}
	r := rlRun{t}
	r.must(rlTaskgen, append([]string{"git", "--repo", repo, "-o", tasksFile, "--work-dir", work, "--id-prefix", "demo", "--tag", "go"}, shared...)...)
	runDir := filepath.Join(dir, "runs", "dead")
	out, errb, err := r.do(rlRollout, append([]string{"--tasks", tasksFile, "--model", "policy-model", "--base-url", dead.URL, "--group", "2",
		"--out", runDir, "--work-dir", work, "--infra-retries", "-1", "--max-spend-usd", "5"}, shared...)...)
	var ee *exitError
	if !errors.As(err, &ee) || ee.code != exitTempFail {
		t.Fatalf("want exit status %d, got %v\nstdout:\n%s\nstderr:\n%s", exitTempFail, err, out, errb)
	}
	if !strings.Contains(errb, "no --budget-usd: each rollout is capped at") {
		t.Errorf("a run cap alone must say it holds each rollout to a share:\n%s", errb)
	}
	if !strings.Contains(out, "2 infra errors") && !strings.Contains(out, "infra errors") {
		t.Errorf("summary:\n%s", out)
	}
	if _, err := os.Stat(filepath.Join(runDir, env.LedgerFile)); err != nil && !os.IsNotExist(err) {
		t.Error(err)
	}
	_ = context.Background
}
