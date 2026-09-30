// Package inspect is Sleipnir's cache inspector: a read-only model of a session's
// event log and an embedded, dependency-free web dashboard on top of it.
//
// The event log is the source of truth (docs/ARCHITECTURE.md), so the inspector
// never talks to a running agent. It streams events.jsonl into an in-memory
// model (Load, then Tail while the session is still writing), derives what the
// cache design promises (hit ratios, layer sizes, rebases, compactions, swarm
// coordination) and serves it over a GET-only HTTP API and a single-page UI
// embedded in the binary.
//
// Design rules that shape the code:
//
//   - Streamed, bounded: logs are read line by line with a byte offset, a torn
//     last line is left for the next poll, and memory is bounded by Options
//     (totals are exact, per-request detail is a sliding window).
//   - Untrusted input: every string in a log (agent ids, tool names, notes, mail)
//     is data. The API returns it JSON-escaped, the UI only ever assigns it with
//     textContent, hashes are validated before they touch the filesystem, and
//     session and agent parameters are map keys, never paths.
//   - Honest estimates: numbers that are not in the log (the "no cache" and
//     "naive whole-history" bills, layer sizes the log does not record) carry the
//     assumptions they were computed under.
package inspect
