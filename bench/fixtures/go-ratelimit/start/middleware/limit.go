// Package middleware holds HTTP middleware built on package ratelimit.
//
// It stands for the existing callers of package ratelimit: it must keep
// compiling and working without being changed.
package middleware

import (
	"net"
	"net/http"

	"example.com/svc/ratelimit"
)

// Global answers 429 Too Many Requests, with a Retry-After header of 1 second,
// to every request that arrives while l is out of tokens.
func Global(l *ratelimit.Limiter, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !l.Allow() {
			tooMany(w)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// PerClient is like Global, but with one bucket per client: the client is the
// host part of r.RemoteAddr.
func PerClient(k *ratelimit.Keyed, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !k.Allow(clientKey(r)) {
			tooMany(w)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func clientKey(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

func tooMany(w http.ResponseWriter) {
	w.Header().Set("Retry-After", "1")
	http.Error(w, "too many requests", http.StatusTooManyRequests)
}
