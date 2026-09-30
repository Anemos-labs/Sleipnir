package mcp

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync/atomic"
	"time"
)

// DialOptions are everything Dial needs besides the server's own entry.
type DialOptions struct {
	// Env is the only source of ${VAR} values. Nil means no variables: an
	// entry that references one then fails, loudly, rather than reading the
	// process environment behind the caller's back.
	Env map[string]string
	// BaseEnv is the environment a stdio server inherits before its own "env"
	// entries are laid over it. Nil means SafeBaseEnv(os.Environ()); pass an
	// empty non-nil slice for nothing at all.
	BaseEnv []string
	// Cwd is the working directory of stdio servers whose entry sets none (the
	// workspace root). A relative "cwd" in an entry is relative to it.
	Cwd string
	// Net configures HTTP and SSE connections (address guard, proxy).
	Net NetOptions
	// Client configures the connection.
	Client ClientOptions
	// StartupTimeout bounds connect, handshake and (for callers that pass the
	// returned context on) nothing else. It is overridden by the entry's
	// startup_timeout. Default 30s.
	StartupTimeout time.Duration
	// ShutdownGrace is how long each stage of stopping a child process waits
	// (stdin EOF, then SIGTERM, then SIGKILL). Default 2s.
	ShutdownGrace time.Duration
	// MaxMessageBytes bounds one incoming message (default DefaultMaxMessageBytes).
	MaxMessageBytes int
}

// errConfig marks problems in a definition, found before anything was started
// or contacted; retrying cannot fix them.
var errConfig = errors.New("invalid configuration")

type configError struct{ err error }

func (e *configError) Error() string        { return e.err.Error() }
func (e *configError) Unwrap() error        { return e.err }
func (e *configError) Is(target error) bool { return target == errConfig }

// redactedError carries an error whose text has been scrubbed of secrets while
// keeping the original in the chain for errors.Is/As.
type redactedError struct {
	msg string
	err error
}

func (e *redactedError) Error() string { return e.msg }
func (e *redactedError) Unwrap() error { return e.err }

func redactErr(r *redactor, err error) error {
	if err == nil || r == nil {
		return err
	}
	msg := r.apply(err.Error())
	if msg == err.Error() {
		return err
	}
	return &redactedError{msg: msg, err: err}
}

// Dial connects to the server described by cfg: it expands ${VAR} references
// from o.Env, validates the result, builds the transport (a child process, or
// an HTTP client behind the address guard) and completes the handshake. The
// returned client owns the connection; Close it.
//
// Errors name the server and the field and never contain a value from the
// entry's env or headers.
func Dial(ctx context.Context, name string, cfg ServerConfig, o DialOptions) (*Client, error) {
	x, err := cfg.Expand(o.Env)
	if err != nil {
		return nil, fmt.Errorf("server %q: %w", clipForError(name), &configError{err})
	}
	if err := x.Validate(); err != nil {
		return nil, fmt.Errorf("server %q: %w", clipForError(name), &configError{err})
	}
	red := newRedactor(x.secrets())

	timeout := o.StartupTimeout
	if timeout <= 0 {
		timeout = 30 * time.Second
	}
	if x.StartupTimeout > 0 {
		timeout = x.StartupTimeout
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	copts := o.Client
	copts.redact = red
	copts.InitTimeout = timeout
	if copts.Name == "" {
		copts.Name = "sleipnir"
	}

	var t Transport
	switch x.EffectiveType() {
	case TypeStdio:
		t, err = dialProc(x, o, red)
		copts.Roots = o.Client.Roots
	case TypeHTTP, TypeSSE:
		copts.Roots = nil // a remote server learns nothing about local paths
		netOpts := o.Net
		netOpts.AllowPrivate = netOpts.AllowPrivate || x.AllowPrivate
		ho := HTTPOptions{URL: x.URL, Headers: x.Headers, Net: netOpts, MaxMessageBytes: o.MaxMessageBytes, Redact: red}
		if x.EffectiveType() == TypeHTTP {
			t, err = NewHTTPTransport(ho)
		} else {
			t, err = NewSSETransport(ho)
		}
	default:
		err = errors.New("type: cannot tell whether this is a stdio, http or sse server")
	}
	if err != nil {
		return nil, fmt.Errorf("server %q: %w", clipForError(name), redactErr(red, err))
	}
	c, err := Connect(ctx, t, copts)
	if err != nil {
		return nil, fmt.Errorf("server %q: %w", clipForError(name), redactErr(red, err))
	}
	return c, nil
}

func dialProc(x ServerConfig, o DialOptions, red *redactor) (Transport, error) {
	base := o.BaseEnv
	if base == nil {
		base = SafeBaseEnv(os.Environ())
	}
	dir := x.Cwd
	switch {
	case dir == "":
		dir = o.Cwd
	case !filepath.IsAbs(dir) && o.Cwd != "":
		dir = filepath.Join(o.Cwd, dir)
	}
	if dir != "" {
		fi, err := os.Stat(dir)
		if err != nil || !fi.IsDir() {
			return nil, &configError{errors.New("cwd: is not an existing directory")}
		}
	}
	var drops atomic.Int64
	return startProc(procSpec{
		Command: x.Command, Args: x.Args, Dir: dir, Env: childEnv(base, x.Env),
		Grace: o.ShutdownGrace, Redact: red,
		Stream: StreamOptions{
			MaxMessageBytes: o.MaxMessageBytes,
			// Servers that print banners or logs on stdout are common enough to
			// tolerate and worth a hint; a busy one must not flood the log.
			OnDrop: func(reason string) {
				if n := drops.Add(1); o.Client.Logf != nil && (n == 1 || n%1000 == 0) {
					o.Client.Logf("mcp: %s (%d so far)", reason, n)
				}
			},
		},
	})
}
