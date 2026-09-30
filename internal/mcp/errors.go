package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"
)

// Sentinel errors. They are matched with errors.Is; the wrapped messages carry
// the specifics (which server, which method) but never a credential.
var (
	// ErrClosed means the connection is gone (closed locally, or the server
	// exited or hung up). The wrapped cause says which.
	ErrClosed = errors.New("mcp: connection closed")
	// ErrMessageTooLarge means a peer message exceeded the size limit and was
	// discarded instead of buffered.
	ErrMessageTooLarge = errors.New("mcp: message exceeds the size limit")
	// ErrProtocolVersion means the server answered initialize with a protocol
	// version this client cannot speak.
	ErrProtocolVersion = errors.New("mcp: unsupported protocol version")
	// ErrNotApproved means a server that needs approval (project-scoped, not
	// trusted) was not approved, so nothing was started or contacted.
	ErrNotApproved = errors.New("mcp: server not approved")
	// ErrSessionExpired means an HTTP server no longer knows our session id; the
	// connection must be re-initialised.
	ErrSessionExpired = errors.New("mcp: session expired")
	// ErrUnsupported means the server did not declare the capability needed.
	ErrUnsupported = errors.New("mcp: capability not supported by server")
	// ErrNotConnected means the server is not (currently) connected.
	ErrNotConnected = errors.New("mcp: server not connected")
	// ErrUnknownServer and ErrUnknownTool are lookup failures.
	ErrUnknownServer = errors.New("mcp: unknown server")
	ErrUnknownTool   = errors.New("mcp: unknown tool")
	// ErrBlocked means the address guard refused a destination (SSRF policy).
	ErrBlocked = errors.New("mcp: destination blocked")
)

// RPCError is a JSON-RPC error object sent by the server. Message is
// sanitised and length-capped when decoded: it is model-visible text from an
// untrusted party.
type RPCError struct {
	Code    int
	Message string
	Data    json.RawMessage
}

func (e *RPCError) Error() string {
	return fmt.Sprintf("mcp: server error %d: %s", e.Code, e.Message)
}

// Standard JSON-RPC error codes, plus the MCP one for missing resources.
const (
	CodeParseError       = -32700
	CodeInvalidRequest   = -32600
	CodeMethodNotFound   = -32601
	CodeInvalidParams    = -32602
	CodeInternalError    = -32603
	CodeResourceNotFound = -32002
)

// timeoutError reports a call that got no answer in time. It satisfies both
// errors.Is(err, context.DeadlineExceeded) and the net.Error Timeout contract
// so callers can treat it like any other deadline.
type timeoutError struct {
	method string
	after  time.Duration
	total  bool // the absolute cap fired, not the inactivity timer
}

func (e *timeoutError) Error() string {
	if e.total {
		return fmt.Sprintf("mcp: %s still running after %s; gave up", e.method, e.after.Round(time.Millisecond))
	}
	return fmt.Sprintf("mcp: no response to %s within %s", e.method, e.after.Round(time.Millisecond))
}
func (e *timeoutError) Timeout() bool   { return true }
func (e *timeoutError) Temporary() bool { return true }
func (e *timeoutError) Is(target error) bool {
	return target == context.DeadlineExceeded
}

// closedError wraps the reason a connection ended so errors.Is(err, ErrClosed)
// holds while the cause (exit status, EOF, session expiry) stays inspectable.
type closedError struct{ cause error }

func (e *closedError) Error() string {
	if e.cause == nil {
		return ErrClosed.Error()
	}
	return ErrClosed.Error() + ": " + e.cause.Error()
}
func (e *closedError) Unwrap() []error {
	if e.cause == nil {
		return []error{ErrClosed}
	}
	return []error{ErrClosed, e.cause}
}
