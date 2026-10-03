package swarm

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/anemos-labs/sleipnir/internal/workspace"
)

func integrationCandidate(t *testing.T, r *isoRig) {
	t.Helper()
	ctx := context.Background()
	w, err := r.mgr.Create(ctx, "recovery-worker", workspace.CreateOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(w.Path, "resume.txt"), []byte("verified worker change\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := w.Commit(ctx, "worker change"); err != nil {
		t.Fatal(err)
	}
	result, err := r.q.Submit(ctx, workspace.Submission{Tree: w})
	if err != nil || !result.Merged() {
		t.Fatalf("submit: %+v, %v", result, err)
	}
}

func TestIntegrationResumeReconcilesInterruptedApplications(t *testing.T) {
	for _, commit := range []bool{false, true} {
		for _, beforeWrite := range []bool{false, true} {
			name := "patch"
			if commit {
				name = "commit"
			}
			if beforeWrite {
				name += "/before"
			} else {
				name += "/after"
			}
			t.Run(name, func(t *testing.T) {
				r := newIsoRig(t, isoOpts{commit: commit}, func(context.Context, *rvCall) rvReply { return rvReply{Text: "idle"} })
				integrationCandidate(t, r)
				var durable IntegrationState
				r.iso.SaveIntegration = func(state IntegrationState) error {
					if state.Pending == nil {
						return errors.New("crash before recording completion")
					}
					data, _ := json.Marshal(state)
					if err := json.Unmarshal(data, &durable); err != nil {
						t.Fatal(err)
					}
					if beforeWrite {
						return errors.New("crash after recording intent")
					}
					return nil
				}
				report := r.sw.Integrate(context.Background())
				if report.Applied == beforeWrite {
					t.Fatalf("unexpected apply result: %+v", report)
				}
				if durable.Pending == nil {
					t.Fatal("no durable intent")
				}
				r.iso.SaveIntegration = func(state IntegrationState) error { durable = state; return nil }
				restarted := &Swarm{deps: Deps{Isolation: r.iso}}
				if err := restarted.RestoreIntegration(context.Background(), durable); err != nil {
					t.Fatal(err)
				}
				if report := restarted.Integrate(context.Background()); !report.Applied {
					t.Fatalf("resume: %+v", report)
				}
				if got := readText(t, filepath.Join(r.repo, "resume.txt")); got != "verified worker change\n" {
					t.Fatalf("checkout: %q", got)
				}
				if durable.Pending != nil || durable.Applied != r.q.Tip() {
					t.Fatalf("cursor: %+v", durable)
				}
				if report := restarted.Integrate(context.Background()); !report.Applied || len(report.Files) != 0 {
					t.Fatalf("duplicate apply: %+v", report)
				}
			})
		}
	}
}

func TestIntegrationResumePreservesEditsInAnAmbiguousCrash(t *testing.T) {
	r := newIsoRig(t, isoOpts{snapshot: true, dirty: map[string]string{"user.txt": "uncommitted original\n"}}, func(context.Context, *rvCall) rvReply { return rvReply{Text: "idle"} })
	integrationCandidate(t, r)
	var durable IntegrationState
	r.iso.SaveIntegration = func(state IntegrationState) error {
		if state.Pending != nil {
			durable = state
		}
		return errors.New("process stopped")
	}
	if report := r.sw.Integrate(context.Background()); report.Applied {
		t.Fatal("write should have stopped at intent")
	}
	path := filepath.Join(r.repo, "resume.txt")
	if err := os.WriteFile(path, []byte("human edit after interruption\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	r.iso.SaveIntegration = func(IntegrationState) error { return nil }
	restarted := &Swarm{deps: Deps{Isolation: r.iso}}
	if err := restarted.RestoreIntegration(context.Background(), durable); err == nil || !strings.Contains(err.Error(), "neither") {
		t.Fatalf("want ambiguous recovery: %v", err)
	}
	if got := readText(t, path); got != "human edit after interruption\n" {
		t.Fatalf("human edit lost: %q", got)
	}
	if got := readText(t, filepath.Join(r.repo, "user.txt")); got != "uncommitted original\n" {
		t.Fatalf("original dirty base lost: %q", got)
	}
}
