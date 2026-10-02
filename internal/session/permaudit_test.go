package session

import (
	"reflect"
	"testing"

	"github.com/anemos-labs/sleipnir/internal/perm"
)

// What a run with no one to ask was refused is told at its end: the commands, and the edits (a run that listed only `go test` and was
// told how to allow it was refused its edit again the next time). A person's no, and a read that was refused, are not that.
func TestRefusedEditsAreRememberedWithTheCommands(t *testing.T) {
	var s Session
	audit := func(by string, r perm.Request) { s.auditPermission(perm.Audit{Kind: "decide", By: by, Request: r}) }
	audit("no one", perm.Request{Tool: "apply_patch", Writes: true, Paths: []string{"/w/slug.go"}})
	audit("no one", perm.Request{Tool: "apply_patch", Writes: true, Paths: []string{"/w/slug.go"}})
	audit("no one", perm.Request{Tool: "bash", Command: "go test ./..."})
	audit("user", perm.Request{Tool: "apply_patch", Writes: true, Paths: []string{"/w/other.go"}})
	audit("no one", perm.Request{Tool: "read", Paths: []string{"/etc/x"}})
	want := []RefusedCommand{{Path: "/w/slug.go", Times: 2}, {Command: "go test ./...", Times: 1}}
	if got := s.RefusedWithNoOneToAsk(); !reflect.DeepEqual(got, want) {
		t.Errorf("got %+v, want %+v", got, want)
	}
}
