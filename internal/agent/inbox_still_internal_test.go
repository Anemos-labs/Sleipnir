package agent

import (
	"strings"
	"testing"
)

// A notice sent with SendIf that no longer holds when the agent takes its mail is dropped unread; the rest arrive in order.
func TestAStaleNoticeIsDroppedWhenTheMailIsTaken(t *testing.T) {
	a := &Agent{}
	decided := false
	a.Send("[mail m1 from mgr] first")
	a.SendIf("[mail m2 request from harness] T23 has waited in review", func() bool { return !decided })
	a.SendIf("[mail m3 from harness] still true", func() bool { return true })
	decided = true
	var got []string
	for _, b := range a.takeInbox() {
		got = append(got, b.Text)
	}
	text := strings.Join(got, "\n")
	if strings.Contains(text, "m2") || !strings.Contains(text, "[mail m1 from mgr] first\n[mail m3 from harness] still true") {
		t.Fatalf("the mail taken was %q", text)
	}
	if n := a.PendingInbox(); n != 0 {
		t.Fatalf("%d messages left in the inbox", n)
	}
	a.SendIf("[mail m4 from harness] gone", func() bool { return false })
	if blocks := a.takeInbox(); blocks != nil {
		t.Fatalf("an inbox of stale notices gave %v", blocks)
	}
}
