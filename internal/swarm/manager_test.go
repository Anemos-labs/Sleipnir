package swarm

import (
	"context"
	"errors"
	"fmt"
	"github.com/anemos-labs/sleipnir/internal/perm"
	"github.com/anemos-labs/sleipnir/internal/tools"
	"path/filepath"
	"testing"
)

func TestManagerPublicationIncludesWorkAcceptedAfterItsOwnChanges(t *testing.T) {
	sc := script{"be-1": {act(writeCall("b.txt", "peer change\n")), act(doneCall("T1", "updated b"))}}
	r := newIsoRig(t, isoOpts{verify: true}, sc.fn())
	if _, err := r.sw.StartManager(); err != nil {
		t.Fatal(err)
	}
	m := r.sw.get(r.sw.ManagerID())
	ctx := context.Background()
	if err := writeFileString(filepath.Join(m.dir, "a.txt"), "manager change\n"); err != nil {
		t.Fatal(err)
	}
	if err := r.sw.flushManager(ctx, m); err != nil {
		t.Fatal(err)
	}
	r.mustSpawn("backend", "update b", "b.txt")
	r.waitStatus("T1", StatusReview)
	if result := r.accept("T1"); result.IsError {
		t.Fatal(result.Text)
	}
	if err := r.sw.flushManager(ctx, m); err != nil {
		t.Fatal(err)
	}
	if got := readText(t, filepath.Join(m.dir, "b.txt")); got != "peer change\n" {
		t.Fatalf("publication misses accepted work: %q", got)
	}
	if got := readText(t, filepath.Join(m.dir, "a.txt")); got != "manager change\n" {
		t.Fatalf("lost manager work: %q", got)
	}
	if n := len(r.sw.Board.Snapshot().Tasks); n != 1 {
		t.Fatalf("direct manager work invented a task: %d", n)
	}
}

func TestPublicationCommandsRecognizeWrappersAndGitOptions(t *testing.T) {
	for _, command := range []string{"git push", "git  push origin HEAD", "git -C . push", "env X=y git -c color.ui=never push", "gh pr create", "cd repo && gh pr view", "sh -c 'git push'"} {
		if !publishesManagerWork(command) {
			t.Errorf("missed %q", command)
		}
	}
	for _, command := range []string{"git status", "git log --grep push", "echo 'git push'", "git -C repo merge main"} {
		if publishesManagerWork(command) {
			t.Errorf("misclassified %q", command)
		}
	}
}

func TestManagerVerifierUsesTheChangedDirectories(t *testing.T) {
	r := newIsoRig(t, isoOpts{dirsVerify: true, files: map[string]string{"api/x.go": "package api\n", "api/done.txt": "verified\n"}}, func(context.Context, *rvCall) rvReply { return rvReply{Text: "done"} })
	if _, err := r.sw.StartManager(); err != nil {
		t.Fatal(err)
	}
	m := r.sw.get(r.sw.ManagerID())
	if err := writeFileString(filepath.Join(m.dir, "api/x.go"), "package api\n// changed\n"); err != nil {
		t.Fatal(err)
	}
	if err := r.sw.flushManager(context.Background(), m); err != nil {
		t.Fatalf("verifier inspected unchanged directories: %v", err)
	}
}

func TestManagerDeliversWithoutInventingTasks(t *testing.T) {
	r := newIsoRig(t, isoOpts{verify: true}, func(context.Context, *rvCall) rvReply { return rvReply{Text: "done"} })
	if _, err := r.sw.StartManager(); err != nil {
		t.Fatal(err)
	}
	m := r.sw.get(r.sw.ManagerID())
	if err := writeFileString(filepath.Join(m.dir, "a.txt"), "manager delivery\n"); err != nil {
		t.Fatal(err)
	}
	result := (&taskTool{r.sw}).deliver(context.Background(), &tools.Call{Env: &tools.Env{Agent: "mgr", Role: "manager", Perm: perm.AllowAll{}}}, taskIn{Text: "fix: direct delivery"})
	if result.IsError {
		t.Fatal(result.Text)
	}
	if got := readText(t, filepath.Join(r.repo, "a.txt")); got != "manager delivery\n" {
		t.Fatalf("checkout: %q", got)
	}
	if got := gitOut(t, r.repo, "status", "--porcelain"); got != "" {
		t.Fatalf("delivery left uncommitted work: %s", got)
	}
	if len(r.sw.Board.Snapshot().Tasks) != 0 {
		t.Fatal("delivery invented a task")
	}
	result = r.callTool(context.Background(), "task", "be-1", "backend", map[string]any{"action": "deliver"})
	if !result.IsError {
		t.Fatal("worker delivered")
	}
}

func TestManagerDeliveryPreservesDirtyCheckoutAndPermissionDenials(t *testing.T) {
	for _, deny := range []bool{false, true} {
		t.Run(fmt.Sprint("deny=", deny), func(t *testing.T) {
			r := newIsoRig(t, isoOpts{verify: true}, func(context.Context, *rvCall) rvReply { return rvReply{Text: "done"} })
			if _, err := r.sw.StartManager(); err != nil {
				t.Fatal(err)
			}
			m := r.sw.get(r.sw.ManagerID())
			original := gitOut(t, r.repo, "rev-parse", "HEAD")
			if err := writeFileString(filepath.Join(m.dir, "a.txt"), "verified change\n"); err != nil {
				t.Fatal(err)
			}
			var checker perm.Requester = perm.AllowAll{}
			if deny {
				checker = perm.DenyAll{}
			} else if err := writeFileString(filepath.Join(r.repo, "personal.txt"), "unrelated user edit\n"); err != nil {
				t.Fatal(err)
			}
			result := (&taskTool{r.sw}).deliver(context.Background(), &tools.Call{Env: &tools.Env{Agent: "mgr", Role: "manager", Perm: checker}}, taskIn{Text: "fix: test"})
			if !result.IsError {
				t.Fatal("delivery bypassed guard")
			}
			if head := gitOut(t, r.repo, "rev-parse", "HEAD"); head != original {
				t.Fatal("checkout moved on refusal")
			}
			if !deny && readText(t, filepath.Join(r.repo, "personal.txt")) != "unrelated user edit\n" {
				t.Fatal("lost unrelated edit")
			}
		})
	}
}

func TestManagerDeliveryChecksActualGitOperations(t *testing.T) {
	for _, tc := range []struct {
		mode, deny string
		blocked    bool
	}{
		{"fast-forward", "Bash(git merge:*)", true},
		{"commit", "Bash(git add:*)", true},
		{"commit", "Bash(git diff:*)", true},
		{"commit", "Bash(git commit:*)", true},
		{"commit", "Bash(git merge:*)", false},
	} {
		t.Run(tc.mode+tc.deny, func(t *testing.T) {
			r := newIsoRig(t, isoOpts{verify: true}, func(context.Context, *rvCall) rvReply { return rvReply{Text: "done"} })
			if _, err := r.sw.StartManager(); err != nil {
				t.Fatal(err)
			}
			m := r.sw.get(r.sw.ManagerID())
			ctx := context.Background()
			if err := writeFileString(filepath.Join(m.dir, "a.txt"), "delivery result\n"); err != nil {
				t.Fatal(err)
			}
			if err := r.sw.flushManager(ctx, m); err != nil {
				t.Fatal(err)
			}
			if tc.mode == "commit" {
				if rep := r.sw.Integrate(ctx); !rep.Applied {
					t.Fatal(rep.Message)
				}
			}
			before := gitOut(t, r.repo, "rev-parse", "HEAD")
			engine, err := perm.NewEngine(perm.Config{Root: r.repo, ExtraRoots: []string{m.dir}, Mode: perm.ModeBypass, Deny: []string{tc.deny}})
			if err != nil {
				t.Fatal(err)
			}
			engine.Confine("mgr", m.dir)
			result := (&taskTool{r.sw}).deliver(ctx, &tools.Call{Env: &tools.Env{Agent: "mgr", Role: "manager", Perm: engine}}, taskIn{Text: "literal $(do-not-run) 'message'"})
			if result.IsError != tc.blocked {
				t.Fatalf("blocked=%v result=%s", tc.blocked, result.Text)
			}
			if tc.blocked && gitOut(t, r.repo, "rev-parse", "HEAD") != before {
				t.Fatal("denied delivery changed branch")
			}
			if !tc.blocked && gitOut(t, r.repo, "log", "-1", "--format=%s") != "literal $(do-not-run) 'message'" {
				t.Fatal("commit message was interpreted")
			}
		})
	}
}

func TestDeliveryPreviewPinsModeAndIntegrationTip(t *testing.T) {
	r := newIsoRig(t, isoOpts{verify: true}, func(context.Context, *rvCall) rvReply { return rvReply{Text: "done"} })
	r.sw.StartManager()
	m := r.sw.get(r.sw.ManagerID())
	ctx := context.Background()
	if err := writeFileString(filepath.Join(m.dir, "a.txt"), "first\n"); err != nil {
		t.Fatal(err)
	}
	if err := r.sw.flushManager(ctx, m); err != nil {
		t.Fatal(err)
	}
	preview, err := r.sw.Accept(ctx, AcceptOptions{Commits: true, DryRun: true})
	if err != nil {
		t.Fatal(err)
	}
	if rep := r.sw.Integrate(ctx); !rep.Applied {
		t.Fatal(rep.Message)
	}
	if _, err := r.sw.Accept(ctx, AcceptOptions{Commits: true, ExpectCommitMode: preview.CommitMode}); !errors.Is(err, ErrAcceptChanged) {
		t.Fatalf("changed operation used old authorization: %v", err)
	}
	if err := writeFileString(filepath.Join(m.dir, "a.txt"), "second\n"); err != nil {
		t.Fatal(err)
	}
	if err := r.sw.flushManager(ctx, m); err != nil {
		t.Fatal(err)
	}
	before := gitOut(t, r.repo, "rev-parse", "HEAD")
	if _, err := r.q.FastForwardChecked(ctx, preview.Tip); err == nil {
		t.Fatal("queue accepted stale preview")
	}
	if gitOut(t, r.repo, "rev-parse", "HEAD") != before {
		t.Fatal("stale preview moved branch")
	}
}
