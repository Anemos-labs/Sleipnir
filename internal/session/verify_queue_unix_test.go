//go:build unix

package session

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/anemos-labs/sleipnir/internal/workspace"
)

// The integration queue verifies under the session's command wrapper and environment, as the agents' own commands run.
func TestQueueVerifierKeepsTheSessionsSandbox(t *testing.T) {
	s := &Session{opts: Options{ShellWrap: []string{"env", "WRAPPED=yes"}, ShellEnv: []string{"PATH=/usr/bin:/bin", "BASE=only"}}}
	v := s.queueVerifier()
	if v == nil {
		t.Fatal("no verifier")
	}
	res := v(context.Background(), workspace.VerifyRequest{Dir: t.TempDir(), Cmd: `echo "$WRAPPED $BASE $EXTRA"`, Timeout: time.Minute, MaxOutput: 4096, Env: []string{"EXTRA=kept"}})
	if res.ExitCode != 0 || strings.TrimSpace(res.Output) != "yes only kept" {
		t.Fatalf("%+v", res)
	}
	if (&Session{}).queueVerifier() != nil {
		t.Error("a session without a sandbox should leave the queue's default")
	}
}
