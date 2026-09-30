package workspace

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/reee344/sleipnir/internal/gitx"
)

// newShimmedEnv is a queue whose git is a wrapper script that can interfere with
// exactly one command, the update of the integration branch (the publish step),
// once the test arms it by creating $D/armed. snippet is the shell code that
// replaces the command that time ($D and $REAL are defined; the real git runs
// when the snippet falls through). Everything else, including the queue's own
// setup, runs the real git.
func newShimmedEnv(t *testing.T, snippet string) (e *queueEnv, ctl string) {
	t.Helper()
	skipWithoutUnix(t)
	realGit, err := exec.LookPath("git")
	if err != nil {
		t.Skip("no git")
	}
	ctl = t.TempDir()
	script := "#!/bin/sh\n" +
		"D=$(dirname \"$0\")\n" +
		"REAL='" + realGit + "'\n" +
		"case \" $* \" in\n" +
		"  *\" update-ref \"*\"/_integration \"*)\n" +
		"    if [ -f \"$D/armed\" ]; then\n" +
		"      rm -f \"$D/armed\"\n" +
		snippet + "\n" +
		"    fi;;\n" +
		"esac\n" +
		"exec \"$REAL\" \"$@\"\n"
	shim := filepath.Join(ctl, "git-shim")
	if err := os.WriteFile(shim, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	dir := newRepo(t)
	repo := openRepo(t, dir, gitx.WithGitPath(shim))
	m := newManager(t, repo)
	log := &eventLog{}
	m.OnEvent = log.fn()
	q := mustQueue(t, m, QueueOptions{})
	return &queueEnv{t: t, dir: dir, repo: repo, m: m, q: q, log: log}, ctl
}

func arm(t *testing.T, ctl string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(ctl, "armed"), nil, 0o644); err != nil {
		t.Fatal(err)
	}
}

func noLockFiles(t *testing.T, e *queueEnv) {
	t.Helper()
	var locks []string
	_ = filepath.WalkDir(filepath.Join(e.repo.CommonDir()), func(p string, d os.DirEntry, err error) error {
		if err == nil && !d.IsDir() && strings.HasSuffix(p, ".lock") {
			locks = append(locks, p)
		}
		return nil
	})
	if len(locks) > 0 {
		t.Fatalf("lock files left behind: %v", locks)
	}
}

// The caller giving up while the integration branch is being moved must not
// interrupt the move: half of it would leave the branch and the queue's tip
// disagreeing (or a lock file that blocks every later publish). The result
// counts, so the caller is told it landed.
func TestCancelingWhileTheResultIsBeingPublishedDoesNotStrandTheQueue(t *testing.T) {
	e, ctl := newShimmedEnv(t, `      echo ready > "$D/ready"; sleep 1`)
	a := e.agent("a")
	edit(t, a, "a.txt", "a\n")
	before := e.q.Tip()
	arm(t, ctl)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() {
		deadline := time.Now().Add(30 * time.Second)
		for time.Now().Before(deadline) && !exists(filepath.Join(ctl, "ready")) {
			time.Sleep(5 * time.Millisecond)
		}
		cancel() // the publish is under way
	}()
	res, err := e.q.Submit(ctx, Submission{Tree: a, Task: "a"})
	if err != nil || !res.Merged() {
		t.Fatalf("Submit after a cancel during publish: %+v, %v", res, err)
	}
	if e.q.Tip() == before {
		t.Fatal("the tip did not move although the branch did")
	}
	e.integrationClean()
	noLockFiles(t, e)
	b := e.agent("b")
	edit(t, b, "b.txt", "b\n")
	if r := e.submit(b, "b"); !r.Merged() {
		t.Fatalf("the queue is stuck after the canceled publish: %+v", r)
	}
	e.integrationClean()
}

// A publish whose outcome is unknown (git failed after it had already moved the
// ref) is judged by the branch, not by the error.
func TestAPublishThatFailedAfterItHappenedCountsAsPublished(t *testing.T) {
	e, ctl := newShimmedEnv(t, `      "$REAL" "$@" || exit $?
      echo "fatal: simulated: the update happened, then git failed" >&2
      exit 128`)
	a := e.agent("a")
	edit(t, a, "a.txt", "a\n")
	arm(t, ctl)
	res, err := e.q.Submit(tctx(t), Submission{Tree: a, Task: "a"})
	if err != nil || !res.Merged() {
		t.Fatalf("the branch moved but the submission was reported as failed: %+v, %v", res, err)
	}
	e.integrationClean()
	if st := e.q.Status(); !st.Healthy || st.Merged != 1 {
		t.Fatalf("status: %+v", st)
	}
}

// A publish that fails without moving the branch rolls the merge back and says
// what happened; the next submission is unaffected.
func TestAPublishThatFailsLeavesTheQueueWhereItWas(t *testing.T) {
	e, ctl := newShimmedEnv(t, `      echo "fatal: simulated: the disk is full" >&2
      exit 128`)
	a := e.agent("a")
	edit(t, a, "a.txt", "a\n")
	before := e.q.Tip()
	arm(t, ctl)
	res, err := e.q.Submit(tctx(t), Submission{Tree: a, Task: "a"})
	if err == nil || !strings.Contains(err.Error(), "publishing the integration branch failed") || !strings.Contains(err.Error(), "disk is full") {
		t.Fatalf("want the publish failure to be reported, got %+v, %v", res, err)
	}
	if e.q.Tip() != before {
		t.Fatal("the tip moved although nothing was published")
	}
	e.integrationClean()
	if st := e.q.Status(); !st.Healthy || st.Merged != 0 {
		t.Fatalf("status: %+v", st)
	}
	if r := e.submit(a, "a again"); !r.Merged() {
		t.Fatalf("resubmission after the failed publish: %+v", r)
	}
	e.integrationClean()
}
