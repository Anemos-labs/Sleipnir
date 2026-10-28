// Package scan is the deep scan: a team maps a whole repository at one pinned commit, investigates it for security and
// correctness holes, and reports only what it proved.
//
// The harness keeps the ledger (scan.json) and is its only writer, through the scan tool. It works out the inventory itself
// (every tracked file, every function, a complexity score) and splits it into shards a team can share. Agents record what
// they find as cited facts: a point of the attack surface, an edge between two places, a lead with the trace from where
// input enters to where it does harm. Every citation is checked against the pinned commit: a quote that is not at the line
// it names is refused, so nothing in the report rests on code that is not there.
//
// A lead becomes a finding only when agents that did not file it confirm it, and, from the depth's proof bar up, when a
// reproducer run in the session's sandbox shows it. Severity is computed from the impact, how reachable the code is and how
// hard it is to exploit, not chosen. The report (REPORT.md, findings.json, findings.sarif and map.json) is rewritten after
// every change, so it can be read while the scan runs.
package scan
