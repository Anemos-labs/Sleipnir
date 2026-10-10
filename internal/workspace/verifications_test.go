package workspace

import (
	"context"
	"strings"
	"testing"
)

func TestQueueKeepsItsVerifications(t *testing.T) {
	fake := func(ctx context.Context, req VerifyRequest) VerifyResult {
		if strings.Contains(req.Task, "bad") {
			return VerifyResult{Cmd: req.Cmd, ExitCode: 2, Output: "FAIL: TestX"}
		}
		return VerifyResult{Cmd: req.Cmd, Output: "ok  pkg"}
	}
	e := newQueueEnv(t, QueueOptions{VerifyCmd: "go test ./...", Verify: fake})
	a := e.agent("a")
	edit(t, a, "a.txt", "a\n")
	e.submit(a, "T1: good")
	b := e.agent("b")
	edit(t, b, "b.txt", "b\n")
	e.submit(b, "T2: bad")
	edit(t, b, "b2.txt", "b\n")
	e.submit(b, "T2: bad")

	all := e.q.Verifications("")
	if len(all) != 3 {
		t.Fatalf("verifications: %+v", all)
	}
	t2 := e.q.Verifications("T2: bad")
	if len(t2) != 2 || t2[0].Result.OK() || t2[0].Result.Output != "FAIL: TestX" || t2[0].Agent != "b" || t2[1].At.Before(t2[0].At) {
		t.Fatalf("T2: %+v", t2)
	}
	if one := e.q.Verifications("T1: good"); len(one) != 1 || !one[0].Result.OK() {
		t.Fatalf("T1: %+v", one)
	}
	if none := e.q.Verifications("T9"); len(none) != 0 {
		t.Fatalf("unknown task: %+v", none)
	}
}
