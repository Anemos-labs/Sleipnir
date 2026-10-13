package delivery

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

type githubFake struct {
	pr                     pull
	threads, summary, runs string
	calls                  []string
	mergeErr               error
}

func (f *githubFake) exec(_ context.Context, name string, args ...string) ([]byte, error) {
	c := name + " " + strings.Join(args, " ")
	f.calls = append(f.calls, c)
	switch {
	case strings.Contains(c, "comments,reviews"):
		if f.summary == "" {
			return []byte(`{"comments":[],"reviews":[]}`), nil
		}
		return []byte(f.summary), nil
	case strings.Contains(c, "pr view"):
		return json.Marshal(f.pr)
	case strings.Contains(c, "api graphql"):
		if f.threads == "" {
			return []byte(`[{"data":{"repository":{"pullRequest":{"reviewThreads":{"nodes":[]}}}}}]`), nil
		}
		return []byte(f.threads), nil
	case strings.Contains(c, "squashMergeAllowed"):
		return []byte(`{"mergeCommitAllowed":true}`), nil
	case strings.Contains(c, "repo view"):
		return []byte("owner/repo\n"), nil
	case strings.Contains(c, "pr merge"):
		return nil, f.mergeErr
	case strings.Contains(c, "run list"):
		if f.runs == "" {
			return []byte(`[]`), nil
		}
		return []byte(f.runs), nil
	}
	return nil, errors.New("unexpected command: " + c)
}

func setup(t *testing.T, options Options) (*Controller, *githubFake) {
	t.Helper()
	f := &githubFake{pr: pull{Number: 4, URL: "https://github.com/owner/repo/pull/4", HeadRefOid: "head-a", State: "OPEN", MergeStateStatus: "CLEAN"}}
	c := &Controller{Path: filepath.Join(t.TempDir(), "delivery.json"), Exec: f.exec}
	if err := c.Start("repair cache", options); err != nil {
		t.Fatal(err)
	}
	return c, f
}

func TestRepairSameObjectiveAndInspectBotSummaries(t *testing.T) {
	c, f := setup(t, Options{AutoFix: true, AutoMerge: true})
	f.summary = `{"comments":[{"body":"CodeRabbit: cache indexes lose their absolute offset","url":"https://example/comment"}]}`
	d, err := c.Step(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(d.Prompt, "repair cache") || !strings.Contains(d.Prompt, "CodeRabbit") || !strings.Contains(d.Prompt, "existing affected tasks") {
		t.Fatalf("missing repair context: %+v", d)
	}
	if strings.Contains(strings.Join(f.calls, "\n"), "pr merge") {
		t.Fatal("merged with findings")
	}
	d, err = c.Step(context.Background())
	if err != nil || d.Prompt != "" {
		t.Fatalf("unchanged summary repeatedly invoked model: %+v %v", d, err)
	}
	f.pr.HeadRefOid = "head-b"
	d, err = c.Step(context.Background())
	if err != nil || d.Prompt == "" {
		t.Fatalf("new head not reviewed: %+v %v", d, err)
	}
}

func TestMergeMatchesHeadAndUsesAllowedMethod(t *testing.T) {
	c, f := setup(t, Options{AutoMerge: true})
	if _, err := c.Step(context.Background()); err != nil {
		t.Fatal(err)
	}
	c.State.SettledAt = time.Now().Add(-2 * time.Minute)
	d, err := c.Step(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if d.Done || d.Phase != "merging" {
		t.Fatalf("merge request is not completion: %+v", d)
	}
	if !strings.Contains(strings.Join(f.calls, "\n"), "--auto --merge --match-head-commit head-a") {
		t.Fatalf("unsafe merge command: %v", f.calls)
	}
	saved, err := Load(c.Path, f.exec)
	if err != nil || !saved.State.MergeEnabled {
		t.Fatalf("merge intent not durable: %+v %v", saved, err)
	}
	if err := saved.Pause(context.Background()); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(strings.Join(f.calls, "\n"), "--disable-auto") || saved.State.MergeEnabled || saved.State.Phase != "paused" {
		t.Fatal("pause failed to revoke merge")
	}
}

func TestFailedChecksRevokePendingMergeAndDoNotCallModelWhenFixDisabled(t *testing.T) {
	c, f := setup(t, Options{AutoMerge: true})
	c.State.MergeEnabled = true
	json.Unmarshal([]byte(`[{"name":"CI","conclusion":"FAILURE"}]`), &f.pr.StatusCheckRollup)
	d, err := c.Step(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if d.Done || d.Prompt != "" || d.Wait <= 0 || c.State.MergeEnabled {
		t.Fatalf("failed checks: %+v %+v", d, c.State)
	}
}

func TestMergeCompletionWaitsForMainAndRelease(t *testing.T) {
	c, f := setup(t, Options{AutoMerge: true})
	f.pr.State = "MERGED"
	f.pr.MergeCommit.Oid = "merged-sha"
	f.runs = `[{"name":"ci","status":"in_progress","headSha":"merged-sha"}]`
	d, err := c.Step(context.Background())
	if err != nil || d.Done || d.Prompt != "" {
		t.Fatalf("pending main: %+v %v", d, err)
	}
	f.runs = `[{"name":"ci","status":"completed","conclusion":"success","headSha":"merged-sha"}]`
	c.State.SettledAt = time.Now().Add(-2 * time.Minute)
	d, err = c.Step(context.Background())
	if err != nil || d.Done || !strings.Contains(d.Prompt, "published artifacts") {
		t.Fatalf("release evidence: %+v %v", d, err)
	}
	d, err = c.Step(context.Background())
	if err != nil || !d.Done || d.Phase != "merged" {
		t.Fatalf("completion: %+v %v", d, err)
	}
}

func TestPauseFailureRemainsRecoverable(t *testing.T) {
	c, f := setup(t, Options{AutoMerge: true})
	c.State.MergeEnabled = true
	f.mergeErr = errors.New("network unavailable")
	if err := c.Pause(context.Background()); err == nil {
		t.Fatal("lost cancellation failure")
	}
	saved, err := Load(c.Path, f.exec)
	if err != nil {
		t.Fatal(err)
	}
	if !saved.State.MergeEnabled || saved.State.Phase != "paused" {
		t.Fatalf("lost pending remote action: %+v", saved.State)
	}
}

func TestOutputLimitAndFreshObjectiveAuthorization(t *testing.T) {
	var b cappedOutput
	large := make([]byte, 9<<20)
	n, err := b.Write(large)
	if err != nil || n != len(large) || b.Len() != 8<<20 || !b.overflow {
		t.Fatal("unbounded output")
	}
	c, _ := setup(t, Options{AutoMerge: true})
	c.State.PR = "old-pr"
	if err := c.Start("next objective", Options{}); err != nil {
		t.Fatal(err)
	}
	if c.State.AutoMerge || c.State.PR != "" || c.State.MergeEnabled {
		t.Fatalf("authorization leaked: %+v", c.State)
	}
}

func TestNewHeadRevokesQueuedMergeBeforeWaiting(t *testing.T) {
	c, f := setup(t, Options{AutoMerge: true})
	c.State.PR, c.State.Head, c.State.MergeEnabled = f.pr.URL, "old-head", true
	if err := json.Unmarshal([]byte(`[{"name":"CI","status":"IN_PROGRESS"}]`), &f.pr.StatusCheckRollup); err != nil {
		t.Fatal(err)
	}
	d, err := c.Step(context.Background())
	if err != nil || d.Done || d.Phase != "checking" || c.State.MergeEnabled {
		t.Fatalf("new head: %+v %+v %v", d, c.State, err)
	}
	if !strings.Contains(strings.Join(f.calls, "\n"), "--disable-auto") {
		t.Fatal("left old merge authorization active")
	}
	saved, err := Load(c.Path, f.exec)
	if err != nil || saved.State.MergeEnabled || saved.State.Head != f.pr.HeadRefOid {
		t.Fatalf("checkpoint: %+v %v", saved, err)
	}
}

func TestNewObjectiveCannotDiscardPendingMerge(t *testing.T) {
	c, _ := setup(t, Options{AutoMerge: true})
	c.State.MergeEnabled = true
	if err := c.Start("another objective", Options{}); err == nil {
		t.Fatal("discarded pending merge")
	}
	if c.State.Objective != "repair cache" || !c.State.MergeEnabled {
		t.Fatal("lost original objective")
	}
}
