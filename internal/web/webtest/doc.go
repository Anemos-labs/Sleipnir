// Package webtest holds the fakes and fixtures that let the builders of `sleipnir web` work without each other: an in-memory
// seam.Host and seam.Tab, a canned session in the page's event vocabulary (a snapshot, and a continuation that arrives as a live
// stream), the stream-frame classes of VOCAB.md section 14, canned answers for the pages that are not about the live session, and a
// complete fake server that serves the real UI with all of it.
//
// The fakes are deterministic: the same call gives the same bytes, with no clock, random number or map order in what a client sees
// (the run token and the session cookie of the real envelope are the exceptions, as they are the point of the envelope).
//
// # For route packages (internal/web/wsvc, settings, runner)
//
// NewShopHost returns a seam.Host with one tab. Register a package's routes on a web.Server with its own Register function and the
// host; Host.Frames lists what the routes published, Tab.Calls what they asked of the tab, and Tab.Fail makes a method of the tab
// return an error. SessionAccess.Session of a fake tab is nil unless Tab.SessionFn is set: tests that need a real harness session set
// it.
//
// # For front-end builders
//
// NewServer builds the fake server and Start runs it; cmd/fakeserver is the same thing as a command:
//
//	go run ./internal/web/webtest/cmd/fakeserver -addr 127.0.0.1:6969
//
// It prints the address to open as its first line, serves the UI embedded in the binary (or a directory, -ui DIR), answers the routes
// of CONTRACT.md that the live session needs from the fake host (hello, tabs, snapshots, messages, approvals, session settings, the
// stream) and fixed, contract-shaped data for the pages (models, providers, permissions, trust, MCP, skills, configuration,
// schedule, the Workspace, the recorded sessions and the CLI spec). A route that is not faked answers 501 not_implemented. The
// canned session is the "shop" team; its first 38 seconds are in the snapshot and the rest arrives on the stream at the pace of the
// session (-speed 1), all at once (-speed 0 plays nothing until POST /api/_fake/step or /api/_fake/play), or faster.
//
// The control routes (all POST, authenticated like every route) are /api/_fake/step (publish the next event of the continuation),
// /api/_fake/play (publish the rest), /api/_fake/reset (back to the snapshot) and /api/_fake/ask (open a question now). They exist
// only here.
package webtest
