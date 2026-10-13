package delivery

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"slices"
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
	if name == "gh" && len(args) >= 3 && args[0] == "pr" && args[1] == "view" && strings.HasPrefix(args[2], "--") && slices.Contains(args, "--repo") {
		return []byte("argument required when using the --repo flag"), errors.New("missing PR selector")
	}
	if slices.Contains(args, "--slurp") {
		return nil, errors.New("unknown flag: --slurp")
	}
	switch {
	case c == "git symbolic-ref --quiet --short HEAD":
		return []byte("codex/delivery-fix\n"), nil
	case strings.Contains(c, "comments,reviews"):
		if f.summary == "" {
			return []byte(`{"comments":[],"reviews":[]}`), nil
		}
		return []byte(f.summary), nil
	case strings.Contains(c, "pr view"):
		return json.Marshal(f.pr)
	case strings.Contains(c, "api graphql"):
		if f.threads == "" {
			return []byte(`{"data":{"repository":{"pullRequest":{"reviewThreads":{"nodes":[]}}}}}`), nil
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
	if err := c.RecordReview(`{"delivery_review":"clear"}`); err != nil {
		t.Fatal(err)
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

func TestReportedReviewBlockerPreventsMerge(t *testing.T) {
	c, f := setup(t, Options{AutoMerge: true})
	f.summary = `{"comments":[{"body":"Possible regression"}]}`
	d, err := c.Step(context.Background())
	if err != nil || !d.AssessReview {
		t.Fatalf("review not requested: %+v %v", d, err)
	}
	if err := c.RecordReview("I have inspected it."); err != nil {
		t.Fatal(err)
	}
	if c.State.ReviewVerdict != "pending" {
		t.Fatal("implicit acknowledgement cleared findings")
	}
	if err := c.RecordReview(`{"delivery_review":"blocked"}`); err != nil {
		t.Fatal(err)
	}
	c.State.SettledAt = time.Now().Add(-2 * time.Minute)
	d, err = c.Step(context.Background())
	if err != nil || d.Done || d.Prompt != "" || d.Phase != "review blocked" {
		t.Fatalf("blocker ignored: %+v %v", d, err)
	}
	if strings.Contains(strings.Join(f.calls, "\n"), "--auto") {
		t.Fatal("merged despite manager's blocker")
	}
	saved, err := Load(c.Path, f.exec)
	if err != nil || saved.State.ReviewVerdict != "blocked" {
		t.Fatalf("lost verdict: %+v %v", saved, err)
	}
	f.pr.HeadRefOid = "new-head"
	d, err = c.Step(context.Background())
	if err != nil || !d.AssessReview {
		t.Fatalf("new head not assessed: %+v %v", d, err)
	}
}

func TestMergeCommandFailureRevokesIntentOrPreservesUnknownState(t *testing.T) {
	for _, revokeFails := range []bool{false, true} {
		c, f := setup(t, Options{AutoMerge: true})
		if _, err := c.Step(context.Background()); err != nil {
			t.Fatal(err)
		}
		c.State.SettledAt = time.Now().Add(-2 * time.Minute)
		c.Exec = func(ctx context.Context, name string, args ...string) ([]byte, error) {
			command := strings.Join(args, " ")
			if strings.Contains(command, "--auto") {
				return nil, errors.New("merge request failed")
			}
			if revokeFails && strings.Contains(command, "--disable-auto") {
				return nil, errors.New("revocation failed")
			}
			return f.exec(ctx, name, args...)
		}
		if _, err := c.Step(context.Background()); err == nil {
			t.Fatal("merge error lost")
		}
		saved, err := Load(c.Path, f.exec)
		if err != nil || saved.State.MergeEnabled != revokeFails {
			t.Fatalf("intent: %+v %v", saved, err)
		}
	}
}

func TestReviewVerdictAcceptsOnlyTheFinalStructuredBlock(t *testing.T) {
	c, _ := setup(t, Options{AutoMerge: true})
	c.State.ReviewVerdict = "pending"
	if err := c.RecordReview("```json\n{\"delivery_review\":\"clear\"}\n```"); err != nil {
		t.Fatal(err)
	}
	if c.State.ReviewVerdict != "clear" {
		t.Fatal("rejected final fenced verdict")
	}
	c.State.ReviewVerdict = "pending"
	if err := c.RecordReview("{\"delivery_review\":\"clear\"}\nActually, a blocker remains."); err != nil {
		t.Fatal(err)
	}
	if c.State.ReviewVerdict != "pending" {
		t.Fatal("accepted a verdict preceding contradictory prose")
	}
}

func TestDiscoveryPinsPRAndReadsEveryReviewPage(t *testing.T) {
	c, f := setup(t, Options{AutoFix: true})
	f.threads = `{"data":{"repository":{"pullRequest":{"reviewThreads":{"nodes":[]}}}}}
 {"data":{"repository":{"pullRequest":{"reviewThreads":{"nodes":[{"id":"late-thread","isResolved":false,"comments":{"nodes":[{"body":"repair this finding"}]}}]}}}}}`
	d, err := c.Step(context.Background())
	if err != nil || !strings.Contains(d.Prompt, "late-thread") {
		t.Fatalf("paginated discovery: %+v %v", d, err)
	}
	if c.State.PR != f.pr.URL {
		t.Fatalf("PR not pinned: %+v", c.State)
	}
	f.calls = nil
	if _, err := c.Step(context.Background()); err != nil {
		t.Fatal(err)
	}
	for _, call := range f.calls {
		if strings.HasPrefix(call, "git ") {
			t.Fatalf("rediscovered branch after pinning PR: %s", call)
		}
	}
}

func TestDiscoveryWithoutPRRequestsOpening(t *testing.T) {
	c, f := setup(t, Options{AutoFix: true})
	c.Exec = func(ctx context.Context, name string, args ...string) ([]byte, error) {
		if name == "gh" && len(args) > 2 && args[0] == "pr" && args[1] == "view" {
			if args[2] != "codex/delivery-fix" {
				t.Fatalf("missing working branch selector: %v", args)
			}
			return []byte("no pull requests found for branch codex/delivery-fix"), errors.New("not found")
		}
		return f.exec(ctx, name, args...)
	}
	d, err := c.Step(context.Background())
	if err != nil || d.Phase != "opening" || d.Prompt == "" {
		t.Fatalf("missing PR: %+v %v", d, err)
	}
}
