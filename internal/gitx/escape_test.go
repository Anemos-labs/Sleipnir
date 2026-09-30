package gitx

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestGitEscapeHatchRefusesWhatRunsProgramsOrWritesElsewhere(t *testing.T) {
	dir := newRepo(t)
	r := openRepo(t, dir)
	ctx := ctxT(t)
	marker := filepath.Join(t.TempDir(), "ran")
	out := filepath.Join(t.TempDir(), "written")

	denied := [][]string{
		nil,
		{""},
		{"-c", "core.editor=evil", "status"},
		{"--exec-path=/tmp", "status"},
		{"--git-dir=/tmp/elsewhere", "status"},
		{"config", "core.editor", "evil"},
		{"fetch"}, {"pull"}, {"push"}, {"clone", "x"}, {"remote", "add", "x", "y"},
		{"submodule", "update"}, {"gc"}, {"daemon"}, {"filter-branch"}, {"format-patch", "HEAD~1"}, {"grep", "x"},
		// programs run by the command line
		{"rebase", "--exec", "touch " + marker, "HEAD"},
		{"rebase", "--exe=touch " + marker, "HEAD"},
		{"rebase", "--ex", "touch " + marker, "HEAD"},
		{"rebase", "-x", "touch " + marker, "HEAD"},
		{"rebase", "-ix", "touch " + marker, "HEAD"},
		{"rebase", "-S", "HEAD"},
		{"commit", "-S", "-m", "x"},
		{"commit", "-aSkeyid", "-m", "x"},
		{"commit", "--gpg-sign", "-m", "x"},
		{"commit", "--gpg-sign=keyid", "-m", "x"},
		{"commit", "--gpg", "-m", "x"},
		{"merge", "--verify-signatures", "HEAD"},
		{"merge", "--verify-sig", "HEAD"},
		{"merge", "-S", "HEAD"},
		{"cherry-pick", "-S", "HEAD"},
		{"revert", "--gpg-sign", "HEAD"},
		{"log", "--show-signature"},
		{"log", "--show-sig"},
		{"show", "--show-signature", "HEAD"},
		{"diff", "--ext-diff"},
		{"diff", "--ext", "HEAD"},
		{"diff", "--textconv", "HEAD"},
		{"cat-file", "--textconv", "HEAD:a.txt"},
		{"cat-file", "--filters", "HEAD:a.txt"},
		// files at paths of the caller's choosing
		{"log", "--output=" + out},
		{"log", "--out=" + out},
		{"diff", "--output=" + out, "HEAD"},
		{"read-tree", "--index-output=" + out, "HEAD"},
		{"apply", "--unsafe-paths", "p.patch"},
		// NUL cannot be passed to a process at all
		{"status", "a\x00b"},
	}
	for _, args := range denied {
		if _, err := r.Git(ctx, args...); !errors.Is(err, ErrInvalid) {
			t.Errorf("Git(%q) = %v, want ErrInvalid", args, err)
		}
	}
	for _, p := range []string{marker, out} {
		if _, err := os.Stat(p); err == nil {
			t.Errorf("%s exists: a refused command ran", p)
		}
	}

	// Legitimate options that merely start like a denied one are not in the way.
	allowed := [][]string{
		{"rev-parse", "--verify", "HEAD"},
		{"rev-parse", "--show-toplevel"},
		{"log", "--oneline", "-1"},
		{"log", "-S", "alpha", "--format=%H"},
		{"log", "--format=%H", "--", "--output"}, // after -- it is a path
		{"status", "--porcelain"},
		{"diff", "--stat", "HEAD"},
		{"diff", "--output-indicator-new=+", "HEAD"},
		{"diff", "--name-only", "HEAD"},
		{"rev-list", "--count", "HEAD"},
		{"show", "--stat", "--format=%H", "HEAD"},
		{"commit", "--allow-empty", "--no-gpg-sign", "-m", "empty"},
		{"branch", "--list"},
		{"apply", "--index", "--check"},
		{"rebase", "--onto", "HEAD", "HEAD", "HEAD"},
	}
	for _, args := range allowed {
		if err := checkGitArgs(args); err != nil {
			t.Errorf("Git(%q) is refused by the option rules: %v", args, err)
		}
	}
	for _, args := range allowed[:7] {
		if _, err := r.Git(ctx, args...); err != nil {
			t.Errorf("Git(%q): %v", args, err)
		}
	}
}

// A signing or verification asked for on the command line runs the program the
// repository names, unless the configuration is blanked: the hostile fixture's
// gpg.program leaves a marker when plain git is asked to sign.
func TestSigningRequestedOnTheCommandLineFindsNoProgram(t *testing.T) {
	skipWithoutUnix(t)
	h := newHostileRepo(t, false)
	r := openRepo(t, h.dir)
	ctx := ctxT(t)
	env, err := Author{Name: "agent"}.env(r.s.now)
	if err != nil {
		t.Fatal(err)
	}
	_, err = r.run(ctx, call{args: []string{"commit", "--allow-empty", "-S", "-m", "signed"}, env: env, mutating: true})
	if err == nil {
		t.Fatal("a commit that asked for a signature succeeded without one")
	}
	if fired := h.fired(t); len(fired) > 0 {
		t.Fatalf("asking for a signature ran repository-controlled code: %v", fired)
	}
}
