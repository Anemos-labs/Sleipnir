// Package web is the HTTP foundation of `sleipnir web`: a hardened loopback server, its
// authentication, a server-sent-events hub and the embedded browser UI. It knows nothing about
// sessions, workspaces or settings; the packages that implement those register routes through
// Server.Handle and publish events through Server.Hub.
//
// A page that can start agents and approve shell commands is a remote control for a process that
// runs with the user's rights, so the server is built around who may talk to it and how.
//
// # The envelope
//
// Every request goes through one chain, in this order, and every route registered with
// Server.Handle inherits all of it:
//
//  1. Security headers (a strict Content-Security-Policy, nosniff, frame denial, no referrer,
//     same-origin opener and resource policies, no caching) are set before anything can fail,
//     so they are on every response: errors, redirects and event streams included.
//  2. A panic in any later step becomes a generic 500 and a log line; the server keeps serving.
//  3. A cap on requests in flight answers 503 instead of queueing without bound.
//  4. The Host header must name this machine and the port that was bound. A page that rebinds its
//     own DNS name to 127.0.0.1 sends its own name and gets 403.
//  5. Fetch metadata and Origin: a cross-site or same-site subresource request is refused, and
//     every request that is not GET or HEAD must carry an Origin that is exactly this server's
//     (scheme, host and port). A request with neither Origin nor Sec-Fetch-Site is not a browser;
//     it is accepted only with the bearer token, never with the cookie.
//  6. Authentication: the per-run token as a bearer token, or the session cookie that the token
//     was exchanged for. `?token=` is accepted on the page URL "/" only.
//  7. Method rules: patterns carry a method ("POST /api/x"); an unknown path is 404 and a known
//     path with another method is 405, as uniform JSON.
//  8. For every method but GET: the X-Sleipnir-Web header (a custom header forces a CORS
//     preflight that this server never answers), a JSON content type when there is a body, the
//     route's body cap, and, for routes that declare it, a single-use X-Confirm id.
//  9. The handler. DecodeJSON reads the body strictly: one value, no unknown fields, within the cap.
//
// # Authentication
//
// The token is 256 random bits generated when the server is created. It is never read from the
// command line or the environment, and never configurable: the only place it is printed is the
// URL on the first line of standard output (Server.URL). Opening that URL exchanges the token for
// an HttpOnly, SameSite=Strict cookie that holds an unrelated random session id; the server keeps
// the sessions, so a session can expire, be logged out or be revoked without touching the token.
// Failed token attempts are throttled for the whole server, and a request that presents a valid
// session is never delayed by them. See auth.go; it is kept apart for review.
//
// # Confirmation
//
// A route that raises privilege (bypass or yolo mode, trusting a project, approving a tool
// server, adding a schedule, updating the binary, saving a key) declares RouteOpts.NeedsConfirm.
// Its caller first obtains an id for a scope (POST /api/confirm, or Server.IssueConfirm from a
// handler) and sends it as X-Confirm. An id is single use, expires after a minute, belongs to
// the credential that asked for it and to one scope. A request that needs the confirmation only
// for some inputs calls Server.RequireConfirm from the handler instead.
//
// # Streams
//
// Hub fans events out to subscribers of named topics (for example "session/<id>" or "global")
// as text/event-stream. Each topic keeps a bounded replay ring so that a reconnecting client can
// resume from Last-Event-ID, and is told when the id it asks for has aged out (a gap event) so
// that it fetches a snapshot instead. Each subscriber has a bounded queue with a loss policy
// that never silently drops what matters: events marked Coalescable are superseded by a newer
// event with the same key or dropped first, ordinary events are dropped next (the client is told
// with a gap event), and events marked Critical are never dropped: a client that cannot keep up
// with them is disconnected with a lagged event and reconnects with Last-Event-ID. Streams have
// no goroutines of their own; a write deadline is refreshed on every write, and comment lines
// keep idle connections open.
//
// # What this package does not do
//
// It does not provide TLS, user accounts or authorisation within a session: the token holder is
// the user. It does not protect against another process of the same user, which can read the
// terminal that printed the URL, the browser's cookie store and this process's memory. docs/SECURITY.md
// section 5 states the threat model and its limits.
//
// # Writing a route
//
//	srv.Handle("POST /api/things/{id}/rename", h, web.RouteOpts{MaxBody: 1 << 10})
//
// The handler receives only requests that passed the whole envelope. It should write answers with
// WriteJSON and Error (uniform JSON, no caching), read bodies with DecodeJSON, treat every string
// it receives as data, and log through Logf, which tags the line with the request and masks
// credentials. A GET route must not change state: the envelope does not require a custom header
// or an Origin for it. Error bodies are {"error": message, "code": identifier}; the identifiers
// are stable: unauthenticated, bad_host, forbidden_origin, forbidden_site, csrf, unsupported_media_type,
// body_too_large, bad_json, bad_request, not_found, method_not_allowed, confirm_required,
// confirm_invalid, rate_limited, busy, too_many_streams, shutting_down, internal.
package web
