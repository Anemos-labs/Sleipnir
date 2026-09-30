// Package ratelimit provides token-bucket rate limiters.
package ratelimit

import (
	"math"
	"sync"
	"time"
)

// Limiter is a token bucket: it holds at most burst tokens and gains rate
// tokens per second. Every permitted operation takes tokens out of the bucket.
// It is safe for concurrent use.
type Limiter struct {
	mu     sync.Mutex
	rate   float64   // tokens gained per second
	burst  float64   // capacity of the bucket
	tokens float64   // tokens in the bucket as of last
	last   time.Time // when tokens was last brought up to date
}

// New returns a limiter that allows bursts of up to burst operations and, in
// the long run, rate operations per second. The bucket starts full.
func New(rate float64, burst int) *Limiter {
	return &Limiter{
		rate:   rate,
		burst:  float64(burst),
		tokens: float64(burst),
		last:   time.Now(),
	}
}

// Allow reports whether one operation may happen now, and takes a token if so.
func (l *Limiter) Allow() bool {
	return l.AllowN(1)
}

// AllowN reports whether n operations may happen now. If the bucket holds at
// least n tokens it takes them and returns true; otherwise it takes nothing and
// returns false. An n that is zero or negative is always allowed.
func (l *Limiter) AllowN(n int) bool {
	if n <= 0 {
		return true
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	l.refill()
	if l.tokens < float64(n) {
		return false
	}
	l.tokens -= float64(n)
	return true
}

// Tokens returns the number of tokens in the bucket right now.
func (l *Limiter) Tokens() float64 {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.refill()
	return l.tokens
}

// refill brings tokens up to date with the time that has passed since last.
// The caller holds l.mu.
func (l *Limiter) refill() {
	now := time.Now()
	if elapsed := now.Sub(l.last); elapsed > 0 {
		l.tokens = math.Min(l.burst, l.tokens+elapsed.Seconds()*l.rate)
	}
	l.last = now
}
