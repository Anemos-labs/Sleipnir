package scan

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/anemos-labs/sleipnir/internal/gitx"
)

// Runner runs a command in the session's sandbox from dir and returns its combined output and exit status. err is for a
// command that could not be run at all; a timeout is an exit status of -1.
type Runner func(ctx context.Context, dir string, argv []string, timeout time.Duration) (output []byte, exit int, err error)

// Proof bounds.
const (
	DefaultProofTimeout = 5 * time.Minute
	MaxProofTimeout     = 30 * time.Minute
	maxScriptBytes      = 64 << 10
	maxProofOutput      = 6000
)

// ProofScript is the file a reproducer is written to, in the copy of the code it runs in.
const ProofScript = ".sleipnir-proof.sh"

// leadIDRE is what a lead id looks like (L1, L12-3): a directory name of letters, digits and dashes.
var leadIDRE = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9-]{0,39}$`)

// Prover runs reproducers: each in a fresh copy of the code at the scan's commit, made as a detached worktree under the scan's
// directory, through Run (the session's sandbox). The copy is removed after the run, whatever the script did to it.
type Prover struct {
	Repo *gitx.Repo
	Run  Runner
	// Network says the sandbox lets commands reach the network; a proof then records that it could have.
	Network bool
}

// Prove runs script against the code at r's commit and judges the run by its markers: reproduced when the script printed
// both the target marker and the reproduced marker and exited 0, not reproduced when it reached the target without
// reproducing, inconclusive otherwise (the program did not start, the script failed before the target, the run timed out).
func (p *Prover) Prove(ctx context.Context, root string, r *Run, lead, by, script string, timeout time.Duration) (Proof, error) {
	if p == nil || p.Run == nil || p.Repo == nil {
		return Proof{}, errors.New("reproducers run only in a sandbox, and this session has none: start it with --sandbox docker")
	}
	if strings.TrimSpace(script) == "" {
		return Proof{}, errors.New("send the reproducer as script: a bash script that sets up, runs the trace and prints the markers")
	}
	if len(script) > maxScriptBytes {
		return Proof{}, fmt.Errorf("the script is %d bytes; the limit is %d", len(script), maxScriptBytes)
	}
	if !strings.Contains(script, MarkerExercised) || !strings.Contains(script, MarkerReproduced) {
		return Proof{}, fmt.Errorf("the script never prints %s or %s: print each only when the run shows it (grep the response, check the file, compare the output)", MarkerExercised, MarkerReproduced)
	}
	if bare := bareMarker(script); bare != "" {
		return Proof{}, fmt.Errorf("the script prints %s unconditionally (%q): print a marker only when a check of what the run did passes", bare, clip(strings.TrimSpace(firstLineWith(script, bare)), 120))
	}
	if timeout <= 0 {
		timeout = DefaultProofTimeout
	}
	timeout = min(timeout, MaxProofTimeout)
	// the lead names a directory: one read back from a scan record is checked like one the scan made
	if !leadIDRE.MatchString(lead) {
		return Proof{}, fmt.Errorf("%q is not a lead id", clip(lead, 40))
	}
	dir := filepath.Join(RunDir(root, r.Slug), "proofs", fmt.Sprintf("%s-%d", strings.ToLower(lead), time.Now().UnixNano()))
	if err := insideRoot(root, filepath.Dir(dir)); err != nil {
		return Proof{}, err
	}
	if err := os.MkdirAll(filepath.Dir(dir), 0o755); err != nil {
		return Proof{}, err
	}
	if err := insideRoot(root, filepath.Dir(dir)); err != nil { // and as made
		return Proof{}, err
	}
	if err := p.Repo.WorktreeAdd(ctx, gitx.WorktreeAddOptions{Path: dir, Commit: r.Commit, Detach: true}); err != nil {
		return Proof{}, fmt.Errorf("could not make a copy of the code at %s: %w", short(r.Commit), err)
	}
	defer func() {
		cctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), time.Minute)
		defer cancel()
		if p.Repo.WorktreeRemove(cctx, dir, true) != nil {
			_ = os.RemoveAll(dir)
		}
	}()
	// a new file: the checked-out code may have its own entry by that name, a link to somewhere else on the host
	f, err := os.OpenFile(filepath.Join(dir, ProofScript), os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o755)
	if err != nil {
		return Proof{}, fmt.Errorf("could not write the reproducer next to the code (the code has a %s of its own?): %w", ProofScript, err)
	}
	_, err = f.Write([]byte(script))
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return Proof{}, err
	}
	start := time.Now()
	out, exit, err := p.Run(ctx, dir, []string{"bash", ProofScript}, timeout)
	if err != nil {
		return Proof{}, fmt.Errorf("the reproducer did not run: %w", err)
	}
	pr := Proof{By: by, Script: script, Exit: exit, Network: p.Network, Seconds: int(time.Since(start).Round(time.Second) / time.Second),
		At: time.Now().UTC().Format(time.RFC3339)}
	text := string(out)
	pr.Ready = hasMarker(text, MarkerReady)
	pr.Exercised = hasMarker(text, MarkerExercised)
	switch {
	case exit == 0 && pr.Exercised && hasMarker(text, MarkerReproduced):
		pr.Outcome = ProofReproduced
	case pr.Exercised && exit >= 0:
		pr.Outcome = ProofNotReproduce
	default:
		pr.Outcome = ProofInconclusive
	}
	pr.Output = tail(text, maxProofOutput)
	if exit < 0 {
		pr.Output += fmt.Sprintf("\n[stopped after %s]", timeout)
	}
	return pr, nil
}

// hasMarker reports whether a line of the output starts with the marker, as a word of its own (a note may follow it, after a colon or not).
func hasMarker(out, marker string) bool {
	for _, l := range strings.Split(out, "\n") {
		if f := strings.Fields(l); len(f) > 0 && strings.TrimRight(f[0], ":;,.") == marker {
			return true
		}
	}
	return false
}

// bareMarker is a marker the script prints on a line of its own with no condition: an echo that is not part of an if, a
// test (&&, ||) or a block. It is a cheap guard against a reproducer that claims a result without checking it.
func bareMarker(script string) string {
	depth := 0
	for _, l := range strings.Split(script, "\n") {
		t := strings.TrimSpace(l)
		if strings.HasPrefix(t, "#") {
			continue
		}
		for _, w := range strings.Fields(t) {
			switch strings.Trim(w, ";") {
			case "if", "case", "while", "until", "for", "{":
				depth++
			case "fi", "esac", "done", "}":
				depth--
			}
		}
		for _, m := range []string{MarkerExercised, MarkerReproduced} {
			if !strings.Contains(t, m) || depth > 0 || strings.Contains(t, "&&") || strings.Contains(t, "||") {
				continue
			}
			if strings.HasPrefix(t, "echo") || strings.HasPrefix(t, "printf") {
				return m
			}
		}
	}
	return ""
}

func firstLineWith(s, sub string) string {
	for _, l := range strings.Split(s, "\n") {
		if strings.Contains(l, sub) {
			return l
		}
	}
	return ""
}

func tail(s string, n int) string {
	if len(s) <= n {
		return s
	}
	cut := s[len(s)-n:]
	if i := strings.IndexByte(cut, '\n'); i >= 0 && i < 200 {
		cut = cut[i+1:]
	}
	return "[…]\n" + cut
}
