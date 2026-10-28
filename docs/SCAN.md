# Deep scan

`/scan` has a team read a whole repository at one pinned commit, map how it works, hunt it for security and correctness holes,
and report only what it proved. It is a plugin (`scan`), off by default: `/plugins enable scan`, then start the session again.
Run it in a sandbox (`sleipnir chat --sandbox docker`) so that reproducers can run, and on a clone you do not mind the agents
reading.

```text
/scan                     list the project's scans
/scan [survey|standard|deep] [DIR]
                          start a scan of the repository at HEAD (or of DIR in it)
/scan status [SCAN]       where a scan stands and what it needs next
/scan report [SCAN]       rewrite its report
sleipnir scan [SCAN]      the same from a shell (--json, --report)
```

A scan is driven as a goal: `/scan` writes a brief (the protocol, below) and sets the goal that points at it, and the goal's
judge holds the team to the brief's milestones on the evidence of the `scan` tool's own reports.

## What the harness does, and what the agents do

The harness is the only writer of the ledger (`.sleipnir/scan/SCAN/scan.json`), through the `scan` tool. It does what must not
depend on a model:

- **Inventory.** Every file git tracks at the commit, minus vendored, built, generated, binary and oversized files (counted
  under *left out*). Functions are found and scored for complexity: exactly for Go (`go/parser`, McCabe), by their usual
  shapes for JavaScript, TypeScript, Python, Ruby, PHP, Rust, Kotlin, Swift, C-like languages and others. Configuration
  files are kept for the config lane.
- **Sizing.** The inventory is split into shards of at most N functions or M lines, files of one directory together. The
  work is sized by that, not by a clock: the scan asks for a team of one agent per two shards, between 8 and 32.
- **Citations.** Every point, edge, trace step and verdict quotes the code. The harness looks for the quote in the file at
  the pinned commit (white space aside, near the line named, then anywhere in the file) and refuses a quote it cannot find,
  answering with the lines that are there. A changed work tree changes nothing: the commit is what is read.
- **Severity.** Computed from the impact, the reachability and the exploitability the agents give: one level lower when
  only an insider reaches the code, one lower when exploiting it is difficult, low at most when nothing reaches it or no
  practical exploit was shown. A verifier's more cautious correction wins.
- **Rules of evidence.** A lead is ruled on only by agents that did not file it; a reproducer counts only when an agent other
  than the filer wrote it; a reproducer that prints a marker without a check is refused.
- **Report.** `REPORT.md`, `findings.json`, `findings.sarif` (SARIF 2.1.0, the trace as a code flow, for code scanning) and
  `map.json` (nodes and edges in the shape code graphs such as graphify use; an edge is `EXTRACTED` when a checked quote backs
  it, else `INFERRED`) are rewritten after every change, so the report can be read while the scan runs.

The agents read the code and record what they find:

| Phase | Who | Through |
|---|---|---|
| Map | scouts, the architect, anyone free | `shards claim`, `functions`, `point` (entry, boundary, access, sink, config), `edge`, `map` |
| Hunt | backend, frontend, devops, any worker | `shards claim for=hunt`, `lead` (title, category, claim, trace from entry to sink, impact, reach, exploit, fix), `hunt` |
| Verify | reviewers, testers, anyone but the filer | `show`, `verdict` (confirm, or reject as duplicate, mitigated, speculative, test-only, dead-code, unreachable, wrong) |
| Prove | testers, anyone but the filer | `prove` (a bash reproducer) |
| Report | the manager | `report`, then `status` says done |

Phases overlap: a shard is hunted as soon as it is mapped, a lead verified as soon as it is filed.

## Depths

| Depth | Shards | Verifiers per lead | Defender | Reproduced from |
|---|---|---|---|---|
| survey | 120 functions or 3000 lines | 1 | no | none |
| standard (default) | 60 functions or 1500 lines | 2 | yes | high |
| deep | 30 functions or 800 lines | 3 | yes | medium |

The *defender* is a verifier who argues the lead is false (the guard the filer missed, the caller that cannot pass the input)
and confirms only when it finds none. A lead is confirmed when enough verifiers confirm it, more than reject it, a defender
among them; it is rejected once as many reject it as the depth asks to confirm it. A confirmed lead the depth wants
reproduced is *unproven* until a reproducer shows it, and is reported apart from the findings.

## Reproducers

`scan prove` runs a bash script in a fresh copy of the code at the commit (a detached worktree under the scan's directory,
removed afterwards), inside the session's sandbox: no network unless the session has `--sandbox-network`, no host
environment, no keys. A session without a sandbox refuses to prove. The script prints, each on a line of its own and only when
a check passes:

- `SLEIPNIR-APP-READY` once what it tests is up;
- `SLEIPNIR-TARGET-EXERCISED` once the input reached the code of the trace;
- `SLEIPNIR-REPRODUCED` once the harm is observed.

The run is *reproduced* when it printed the last two and exited 0, *not reproduced* when it reached the target without the
harm, and *inconclusive* otherwise (including a timeout: 5 minutes by default, 30 at most).

## Where it lives

```text
.sleipnir/scan/.gitignore          keeps every scan out of the project's commits
.sleipnir/scan/SCAN/scan.json      the ledger
.sleipnir/scan/SCAN/functions.json the inventory's functions (written once)
.sleipnir/scan/SCAN/BRIEF.md       the protocol the goal points at
.sleipnir/scan/SCAN/REPORT.md      the report, with findings.json, findings.sarif and map.json
```
