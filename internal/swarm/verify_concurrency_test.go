package swarm

import (
	"context"
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestVerifierSlotsStayOccupiedUntilCanceledRunnersExit(t *testing.T) {
	release := make(chan struct{})
	var releaseOnce sync.Once
	defer releaseOnce.Do(func() { close(release) })
	started, ended := make(chan struct{}, 4), make(chan struct{}, 4)
	var calls atomic.Int32
	s := New(Config{MaxVerifies: 2, VerifyTimeout: time.Second, VerifyCmd: "verify", Verify: func(context.Context, string, string) (string, int, error) {
		calls.Add(1)
		started <- struct{}{}
		<-release // deliberately ignores cancellation
		ended <- struct{}{}
		return "ok", 0, nil
	}}, Deps{}, nil)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	results := make(chan verifyResult, 2)
	for i := 0; i < 2; i++ {
		go func() { results <- s.runVerify(ctx, "", nil) }()
	}
	for i := 0; i < 2; i++ {
		select {
		case <-started:
		case <-time.After(2 * time.Second):
			t.Fatal("verifier did not start")
		}
	}
	cancel()
	for i := 0; i < 2; i++ {
		select {
		case result := <-results:
			if !result.infra || !errors.Is(result.err, context.Canceled) {
				t.Fatalf("canceled caller received a verdict: %+v", result)
			}
		case <-time.After(5 * time.Second):
			t.Fatal("canceled caller did not return while its verifier remained active")
		}
	}
	queued := make(chan verifyResult, 1)
	go func() { queued <- s.runVerify(context.Background(), "", nil) }()
	select {
	case result := <-queued:
		if !result.infra || result.ran || result.err == nil || !strings.Contains(result.err.Error(), "timed out") {
			t.Fatalf("queued verifier did not expire without a verdict: %+v", result)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("waiting for a verifier slot exceeded the verification deadline")
	}
	if got := calls.Load(); got != 2 {
		t.Errorf("%d runners started while two canceled runners still occupied the two slots", got)
	}
	releaseOnce.Do(func() { close(release) })
	for i := int32(0); i < calls.Load(); i++ {
		select {
		case <-ended:
		case <-time.After(5 * time.Second):
			t.Fatal("released verifier did not exit")
		}
	}
	if result := s.runVerify(context.Background(), "", nil); !result.ok {
		t.Fatalf("finished runners did not release their slots: %+v", result)
	}
}

func TestVerifierOutputAfterDeadlineIsNotAVerdict(t *testing.T) {
	s := New(Config{VerifyTimeout: time.Millisecond, VerifyCmd: "verify", Verify: func(ctx context.Context, _, _ string) (string, int, error) {
		<-ctx.Done()
		return "late success", 0, nil
	}}, Deps{}, nil)
	for i := 0; i < 20; i++ {
		if result := s.runVerify(context.Background(), "", nil); !result.infra || result.ran || result.ok {
			t.Fatalf("late output was accepted as a verdict: %+v", result)
		}
	}
}

func TestVerifierPanicReleasesItsSlot(t *testing.T) {
	var calls atomic.Int32
	s := New(Config{MaxVerifies: 1, VerifyTimeout: time.Second, VerifyCmd: "verify", Verify: func(context.Context, string, string) (string, int, error) {
		if calls.Add(1) == 1 {
			panic("fixture panic")
		}
		return "ok", 0, nil
	}}, Deps{}, nil)
	if result := s.runVerify(context.Background(), "", nil); !result.infra || !strings.Contains(result.err.Error(), "crashed") {
		t.Fatalf("panic was not reported as infrastructure failure: %+v", result)
	}
	if result := s.runVerify(context.Background(), "", nil); !result.ok {
		t.Fatalf("panic retained the slot: %+v", result)
	}
}

func TestCanceledVerificationDoesNotStartAnAvailableRunner(t *testing.T) {
	var calls atomic.Int32
	s := New(Config{VerifyCmd: "verify", Verify: func(context.Context, string, string) (string, int, error) {
		calls.Add(1)
		return "ok", 0, nil
	}}, Deps{}, nil)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	for i := 0; i < 20; i++ {
		if result := s.runVerify(ctx, "", nil); !result.infra || !errors.Is(result.err, context.Canceled) {
			t.Fatalf("canceled verification returned a verdict: %+v", result)
		}
	}
	if got := calls.Load(); got != 0 {
		t.Errorf("already canceled requests started %d verifiers", got)
	}
}
