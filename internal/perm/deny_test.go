package perm

import (
	"context"
	"strings"
	"testing"
)

// DenyAll is what a constructor uses when it was given no Requester (S46).
func TestDenyAllDeniesEverythingAndSaysHowToAllow(t *testing.T) {
	for _, r := range []Request{
		{Tool: "read", Paths: []string{"/etc/hostname"}},
		{Tool: "bash", Command: "true"},
		{Tool: "web_fetch", Network: true},
		{},
	} {
		d := DenyAll{}.Check(context.Background(), r)
		if d.Allow || !strings.Contains(d.Reason, "no permission policy") || !strings.Contains(d.Reason, "perm.AllowAll") {
			t.Errorf("%+v: %+v", r, d)
		}
	}
	if d := (AllowAll{}).Check(context.Background(), Request{Tool: "bash", Command: "rm -rf /"}); !d.Allow {
		t.Error("AllowAll is still there for the callers that mean it")
	}
}
