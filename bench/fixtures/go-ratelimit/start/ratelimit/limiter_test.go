package ratelimit_test

import (
	"testing"
	"time"

	"example.com/svc/ratelimit"
)

// These tests use the real clock. The margins are generous so that a busy
// machine cannot make them fail: they never depend on two calls happening
// within a short time of each other.

func TestBurstThenDeny(t *testing.T) {
	l := ratelimit.New(0.001, 3) // one token per 1000 seconds: nothing refills during the test
	for i := 1; i <= 3; i++ {
		if !l.Allow() {
			t.Fatalf("call %d of the burst was denied", i)
		}
	}
	if l.Allow() {
		t.Fatal("call 4 was allowed although the bucket should be empty")
	}
}

func TestAllowN(t *testing.T) {
	l := ratelimit.New(0.001, 5)
	if !l.AllowN(3) || l.AllowN(3) {
		t.Fatal("want AllowN(3) allowed on a full bucket of 5, then denied with 2 tokens left")
	}
	if !l.AllowN(2) || l.AllowN(1) {
		t.Fatal("the denied call must not take tokens: want AllowN(2) allowed, then the bucket empty")
	}
}

func TestRefillsWithRealTime(t *testing.T) {
	l := ratelimit.New(20, 1) // one token per 50 ms
	if !l.Allow() {
		t.Fatal("first call was denied")
	}
	time.Sleep(300 * time.Millisecond) // six times as long as needed
	if !l.Allow() {
		t.Fatal("no token after 300ms")
	}
}

func TestKeysAreIndependent(t *testing.T) {
	k := ratelimit.NewKeyed(0.001, 1)
	if !k.Allow("a") {
		t.Fatal(`first Allow("a") was denied`)
	}
	if k.Allow("a") {
		t.Fatal(`second Allow("a") was allowed`)
	}
	if !k.Allow("b") {
		t.Fatal(`first Allow("b") was denied although only "a" is used up`)
	}
	if n := k.Len(); n != 2 {
		t.Fatalf("Len() = %d, want 2", n)
	}
}

func TestSweepForgetsIdleKeys(t *testing.T) {
	k := ratelimit.NewKeyed(1, 1)
	k.Allow("old")
	time.Sleep(400 * time.Millisecond)
	k.Allow("new")
	if n := k.Sweep(200 * time.Millisecond); n != 1 {
		t.Fatalf("Sweep removed %d keys, want 1", n)
	}
	if n := k.Len(); n != 1 {
		t.Fatalf("Len() = %d after the sweep, want 1", n)
	}
}
