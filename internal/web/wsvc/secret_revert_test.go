package wsvc

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"path/filepath"
	"strings"
	"testing"

	"github.com/anemos-labs/sleipnir/internal/checkpoint"
	"github.com/anemos-labs/sleipnir/internal/provider/mock"
	"github.com/anemos-labs/sleipnir/internal/web/wire"
)

// shownHunks is the diff of a file as the page sees it: computed on the masked text of a
// secret carrier, as diffOf computes it.
func shownHunks(old, new string, secret bool) []checkpoint.Hunk {
	if secret {
		old, new = maskSecrets(old), maskSecrets(new)
	}
	return checkpoint.DiffLines([]byte(old), []byte(new)).Hunks
}

func TestRevertNeverWritesAHiddenSecret(t *testing.T) {
	const keyA, keyB = "sk-live-0123456789abcdef0123456789abcdef", "sk-live-ffffffffffffffffffffffffffffffff"
	t.Run("a masked context line keeps the live value", func(t *testing.T) {
		old := "API_KEY=" + keyA + "\nDEBUG=0\n"
		new := "API_KEY=" + keyB + "\nDEBUG=1\n"
		h := shownHunks(old, new, true)
		if len(h) != 1 || h[0].Lines[0].Op != ' ' || strings.Contains(h[0].Lines[0].Text, "sk-live") {
			t.Fatalf("the page's hunk: %+v", h)
		}
		got, err := reverseHunk([]byte(old), []byte(new), h[0], true)
		if err != nil || string(got) != "API_KEY="+keyB+"\nDEBUG=0\n" {
			t.Fatalf("revert = %q %v: the live key must stay", got, err)
		}
	})
	t.Run("a hunk that changes a masked line is refused", func(t *testing.T) {
		old := "TOKEN=" + keyA + "\nDEBUG=0\n"
		new := "SECRET_TOKEN=" + keyB + "\nDEBUG=0\n"
		h := shownHunks(old, new, true)
		if len(h) != 1 {
			t.Fatalf("hunks: %+v", h)
		}
		got, err := reverseHunk([]byte(old), []byte(new), h[0], true)
		if !errors.Is(err, errSecretLine) || got != nil {
			t.Fatalf("revert = %q %v, want a refusal", got, err)
		}
	})
	t.Run("a secret removed by the hunk is refused too", func(t *testing.T) {
		old := "DEBUG=0\n"
		new := "DEBUG=0\nAPI_KEY=" + keyB + "\n"
		h := shownHunks(old, new, true)
		if _, err := reverseHunk([]byte(old), []byte(new), h[0], true); !errors.Is(err, errSecretLine) {
			t.Fatalf("err = %v", err)
		}
	})
	t.Run("a file that holds no secret reverts as before", func(t *testing.T) {
		old := "API_KEY=" + keyA + "\nDEBUG=0\n"
		new := "API_KEY=" + keyB + "\nDEBUG=1\n"
		h := shownHunks(old, new, false)
		got, err := reverseHunk([]byte(old), []byte(new), h[0], false)
		if err != nil || string(got) != old {
			t.Fatalf("revert = %q %v", got, err)
		}
	})
}

// Through the routes: the page sees the key masked, reverts DEBUG, and the live key
// stays; a hunk whose changed line holds a hidden value is refused, nothing written.
func TestRevertRouteKeepsSecretsItDoesNotShow(t *testing.T) {
	const keyA, keyB = "sk-live-0123456789abcdef0123456789abcdef", "sk-live-ffffffffffffffffffffffffffffffff"
	root := project(t, map[string]string{
		"config/app.key": "API_KEY=" + keyA + "\nDEBUG=0\n",
		"config/b.key":   "TOKEN=" + keyA + "\nMODE=a\n",
	})
	sc := &soloScript{turns: [][][]mock.ToolCall{{
		{call("r1", "read", map[string]any{"path": "config/app.key"}), call("r2", "read", map[string]any{"path": "config/b.key"})},
		{call("w1", "write", map[string]any{"path": "config/app.key", "content": "API_KEY=" + keyB + "\nDEBUG=1\n"}),
			call("w2", "write", map[string]any{"path": "config/b.key", "content": "SECRET_TOKEN=" + keyB + "\nMODE=a\n"})},
	}}}
	client, model := startMock(t, sc.respond)
	e := newEnv(t, root, options(t, root, client, model))
	if _, err := e.sess.Run(context.Background(), "rotate"); err != nil {
		t.Fatal(err)
	}
	var d wire.WsDiff
	expect(t, e.get("/ws/diff?path=config/app.key", &d), http.StatusOK, "")
	if strings.Contains(fmt.Sprint(d), "sk-live") || len(d.Hunks) != 1 {
		t.Fatalf("the page's diff: %+v", d)
	}
	body := wire.RevertRequest{Path: "config/app.key", Key: hunkKey(d.Hunks[0].OldStart, d.Hunks[0].NewStart), From: "base", To: "live", Scope: d.Hunks[0].Scope}
	expect(t, e.post("/ws/revert", body, body.Scope, nil), http.StatusOK, "")
	if got := readFile(t, filepath.Join(root, "config/app.key")); got != "API_KEY="+keyB+"\nDEBUG=0\n" {
		t.Fatalf("after the revert: %q (the live key must stay)", got)
	}

	expect(t, e.get("/ws/diff?path=config/b.key", &d), http.StatusOK, "")
	body = wire.RevertRequest{Path: "config/b.key", Key: hunkKey(d.Hunks[0].OldStart, d.Hunks[0].NewStart), From: "base", To: "live", Scope: d.Hunks[0].Scope}
	w := e.post("/ws/revert", body, body.Scope, nil)
	expect(t, w, http.StatusUnprocessableEntity, "rejected")
	if !strings.Contains(w.Body.String(), "secret") {
		t.Fatalf("refusal: %s", w.Body)
	}
	if got := readFile(t, filepath.Join(root, "config/b.key")); got != "SECRET_TOKEN="+keyB+"\nMODE=a\n" {
		t.Fatalf("a refused revert writes nothing: %q", got)
	}
	// a restore puts whole files back: the preview says so for a file of secrets
	var plan wire.RestorePlan
	expect(t, e.post("/ws/restore", wire.RestoreRequest{ID: "c01", DryRun: true}, "", &plan), http.StatusOK, "")
	for _, f := range plan.Files {
		if strings.HasSuffix(f.Path, ".key") && !strings.Contains(f.Reason, "holds secrets") {
			t.Fatalf("%s in the preview: %+v", f.Path, f)
		}
	}
}
