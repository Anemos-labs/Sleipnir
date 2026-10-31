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
/scan rerun [SCAN]        scan the same code again at HEAD
/scan compare [SCAN] [SINCE]
                          its findings as new, fixed and unchanged since an earlier scan
sleipnir scan [SCAN]      the same from a shell (--json, --report, --rerun [--pull], --compare [--since OLD])
```

The web page has a **Deep scan** view of its own (`g d`): the project's scans with their phase and numbers, the selected scan's
findings (each opens with its trace, its verdicts and its reproductions), the attack surface by lane and the shards with their
maps, and a form that starts a scan of the project or of a repository. For a repository it clones it into a new folder of
`~/sleipnir-scans` (https and ssh only, without submodules, with git's hooks, fsmonitor and symbolic links off, so that nothing
in the clone runs on the host or points a host tool outside it), creates the scan there, and opens the New session dialog filled
in with a sandboxed team of the scan's size and its goal. The findings card exports the report as CSV, JSON or PDF (the report
laid out for print; the browser saves it), and as Markdown or SARIF.

On a confirmed finding the person can act:

- **Open issue…** writes the finding up (the claim, the cited trace, how it was checked, the reproducer, the fix) for the person
  to read and edit, then opens it on the project's GitHub repository with `gh` on the machine, or copies it. Nothing is sent
  until the person presses Open.
- **Fix…** sets a goal for the team: fix the cause on the trace, add a regression test that fails without the fix, and run the
  reproducer again to show it no longer reproduces; optionally with a pull request driven through CI and review, and merged.

A scan is driven as a goal: `/scan` writes a brief (the protocol, below) and sets the goal that points at it, and the goal's
judge holds the team to the brief's milestones on the evidence of the `scan` tool's own reports.

## What the harness does, and what the agents do

The harness is the only writer of the ledger (`.sleipnir/scan/SCAN/scan.json`), through the `scan` tool. It does what must not
depend on a model:

- **Inventory.** Every file git tracks at the commit, nothing left out. A file no person wrote is scanned too and marked by
  its kind: `vendored` (vendor/, node_modules/, dist/...), `generated` (minified bundles, protobuf output, lock files),
  `data`, `other` (text in no known language: docs, templates), `large` (over 1 MiB, scanned whole), `binary` (read with
  the sandbox's reverse-engineering tools, quoted by the strings it holds), `symlink` and `submodule`. The report counts
  them. Such files weigh a quarter of their lines in a shard, a binary a quarter of a shard. Functions are found and scored for complexity: exactly for Go (`go/parser`, McCabe), by their usual
  shapes for JavaScript, TypeScript, Python, Ruby, PHP, Rust, Kotlin, Swift, C-like languages and others, in the code a
  person wrote. Configuration
  files are kept for the config lane.
- **Sizing.** The inventory is split into shards of at most N functions or M lines, files of one directory together. The
  work is sized by that, not by a clock: the scan asks for a team of one agent per two shards, between 8 and 32.
- **Citations.** Every point, edge, trace step and verdict quotes the code. The harness looks for the quote in the file at
  the pinned commit (white space aside, near the line named, then anywhere in the file) and refuses a quote it cannot find,
  answering with the lines that are there. A changed work tree changes nothing: the commit is what is read.
- **Severity.** Computed from the CVSS v3.1 base vector the agents give
  (`CVSS:3.1/AV:N/AC:L/PR:N/UI:N/S:U/C:H/I:H/A:H`). The harness checks the vector, computes its base score with the
  specification's formula (including its Roundup), and takes the severity from the rating scale: critical from 9.0, high
  from 7.0, medium from 4.0, low below (0.0, rated none by the scale, is reported as low). A lead is low at most when nothing
  reaches the code or no practical exploit was shown, since a base score assumes the code is reached. A confirming verifier
  may correct the vector, the reachability or the exploitability; the lowest-scoring vector and the most cautious tiers win.
  Findings are ranked by severity, then by score. A ledger written before leads carried vectors keeps the earlier rule: the
  impact level, one lower when only an insider reaches the code and one lower when exploiting it is difficult.
- **Rules of evidence.** A lead is ruled on only by agents that did not file it; a reproducer counts only when an agent other
  than the filer wrote it; a reproducer that prints a marker without a check is refused.
- **Stopping.** A scan is sized by its code and it ends (see [When a scan ends](#when-a-scan-ends)): a lead budget, fewer
  leads verified once the hunt is over, settled leads that take no more verdicts, and a goal judged from the ledger.
- **Report.** `REPORT.md`, `report.html`, `findings.json`, `findings.sarif` (SARIF 2.1.0, the trace as a code flow and the CVSS score as `security-severity`, or the band's representative value when a cap lowered the severity, for code
  scanning) and `map.json` (nodes and edges in the shape code graphs such as graphify use; an edge is `EXTRACTED` when a checked
  quote backs it, else `INFERRED`) are rewritten after every change, so the report can be read while the scan runs (see
  [The report](#the-report)).

The agents read the code and record what they find:

| Phase | Who | Through |
|---|---|---|
| Map | scouts, the architect, anyone free | `shards claim`, `functions`, `point` (entry, boundary, access, sink, config), `edge`, `map` |
| Hunt | backend, frontend, devops, any worker | `shards claim for=hunt`, `lead` (title, category, claim, trace from entry to sink, CVSS vector, reach, exploit, fix), `hunt` |
| Verify | reviewers, testers, anyone but the filer | `show`, `verdict` on an open lead (confirm, or reject as duplicate, mitigated, speculative, test-only, dead-code, unreachable, wrong) |
| Prove | testers, anyone but the filer | `prove` (a bash reproducer) |
| Report | the manager | `report`, then `status` says done |

Phases overlap: a shard is hunted as soon as it is mapped, a lead verified as soon as it is filed. A shard is closed with `hunt` only once each of its sinks is in a lead or named in the hunt note with the reason it is safe.

A session whose goal is a scan (from `/scan`, the web page or `sleipnir scan`) works on that scan: an agent's call naming another
scan of the project, such as an older unfinished one, goes to the session's scan with a note.

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

## When a scan ends

A team can always find one more thing to check. The harness ends a scan by rules the agents cannot argue with:

- **A budget.** The scan verifies at most a set number of leads: 4 per shard at survey depth, 6 at standard, 8 at deep, and at
  least 24. Past it, a lead below critical is recorded as **deferred**.
- **Diminishing returns.** Once every shard is hunted, a lead is *late*. Only a few late leads are verified (a tenth of those
  the hunt filed, at least 3), and none once the last 3 late leads ruled on were all rejected. A later lead below critical is
  deferred.
- **A settled lead is final.** A confirmed, unproven, rejected or deferred lead takes no more verdicts. A lead that two more
  agents than the depth asks for (4 at standard depth, 5 at deep) ruled on without agreeing is **disputed**: settled,
  and reported apart.
- **The ledger decides.** The goal a scan runs under is judged from the scan's ledger, not by a model: it is met when
  `scan status` says done, and when only the report is missing the harness writes it. Until then the agents are sent on with
  the ledger's own list of what is missing, and a turn that changes nothing in the ledger counts as no progress.

A deferred lead is kept in the ledger and listed in the report as *not verified*, with the reason: nothing more is done with
it, and a critical lead is never deferred. `scan status` shows the intake: how many leads are verified against the budget,
and the late leads against their allowance.

## The report

The report says what was found and how it was checked; it names no agent. Verdicts are given by role (reviewer, defender),
a reproduction as independent or not, and agents' ids written into notes are replaced. It is laid out as an assessment:

1. The scan's facts: the commit, the code's size, the depth and whether the scan is complete.
2. A summary: a sentence, the findings by severity (and how many were not reproduced), and by category.
3. An index of the findings and the disputed leads, worst first, with their location and status.
4. Each finding: its severity, status, category, location, CVSS score and vector (linked to FIRST's calculator),
   reachability and exploitability; the description; the trace with its quotes; how it was verified; the remediation; and the reproducer that showed it.
5. Appendices: the leads still being verified, those not verified, the rejected ones with the reason, and the scope (the
   counts, the attack surface by lane and the files with the largest one).
6. The method.

`report.html` is the same document as a page with no script: on a screen it uses the product's dark palette and filters the
index and the findings by severity and status (checkboxes the stylesheet reads); printed, it is light and keeps every finding.
The Deep scan view shows the report live: the summary (each tile, category and row of the leads narrows the index), an index
filtered by words, severity, status and category, and each finding in full.

## Rerunning and comparing

Every scan stays in `.sleipnir/scan/` with its ledger and report, so the next one can be compared with it. A **rerun**
(`/scan rerun SCAN`, `sleipnir scan SCAN --rerun`, or **Rerun…** on the page) creates a new scan of what SCAN scanned at the
repository's HEAD now: the same depth and focus, or for a review, the change from the same base. The new scan names SCAN as its
`previous`, and its brief lists SCAN's findings with where their sinks were, for the hunters to check each again at the new
commit: one that still holds is filed again with its trace as it is now, one the code no longer allows is named in the hunt note.
`--pull` (the page's *update from the remote first*, checked for a repository cloned for a scan) fast-forwards the checked-out
branch from its upstream first, over https or ssh with git's hooks off; a branch that has diverged is left as it is and the rerun
is refused.

A **comparison** (`/scan compare`, `sleipnir scan SCAN --compare`, the Changes card on the page) sorts the findings (confirmed or
unproven leads) of a scan and of an earlier one: the scan it reruns, else the newest earlier whole scan of the same focus, or the
one `--since` names. A finding of one is a lead of the other when they have the same category and their sinks hold the same code
(the fingerprint SARIF carries, so a line that moved still matches), or failing that when their traces share a step.

| Kind | Meaning |
|---|---|
| new | the newer scan found it, the older did not |
| fixed | the older scan found it; the newer one, finished, did not find it again, or its verifiers rejected it (the reason is given) |
| not confirmed again yet | the older scan found it; the newer one is still running and has not confirmed it |
| unchanged | both found it (a changed severity is noted) |

## Reviewing a change

`sleipnir scan --start --diff BASE` makes a review instead of a scan: the change from BASE (a commit or a branch; the review
starts from their merge base, so commits BASE gained since do not count) to HEAD. Its inventory and shards hold only the files
the change added or modified, and `scan lead` refuses a lead with no step of its trace on a line the change wrote: a review
reports the holes a change brings in, widens or leaves open in the code it rewrote, not the rest of the repository. Every other
rule (checked quotes, verifiers, the defender, reproducers) is the same. A review is named `review-…`, its findings.json carries
`base` and `changes` (the line ranges at HEAD), and each finding an `anchor`: the last step of its trace on a changed line, where
a pull request review comments on it. SARIF results are located at the anchor too.

```text
sleipnir swarm 8 --sandbox docker "$(sleipnir scan --start --diff origin/main)"
```

## Reproducers

`scan prove` runs a bash script in a fresh copy of the code at the commit (a detached worktree under the scan's `work/`
directory, removed afterwards), inside the session's sandbox: no network unless the session has `--sandbox-network`, no host
environment, no keys. `--sandbox docker` runs the image of the repository's language (Go, Rust, Java, Python, Ruby, Node), and
a Go module's dependencies are fetched into the container before its network goes off, so a reproducer can build and
run the code offline (see the sandbox in [SECURITY.md](SECURITY.md)). A session without a sandbox refuses to prove. The script prints, each on a line of its own and only when
a check passes:

- `SLEIPNIR-APP-READY` once what it tests is up;
- `SLEIPNIR-TARGET-EXERCISED` once the input reached the code of the trace;
- `SLEIPNIR-REPRODUCED` once the harm is observed.

The run is *reproduced* when it printed the last two and exited 0, *not reproduced* when it reached the target without the
harm, and *inconclusive* otherwise (including a timeout: 5 minutes by default, 30 at most).

Every run that reproduced is kept in the scan's `proofs/` directory, rewritten with the report: `proofs/README.md` indexes
them, and each `proofs/LEAD-N/` (the lead's N-th reproduced run) holds `reproduce.sh`, its `output.txt`, `proof.json` and a `README.md` with the claim,
the trace, the outcome and how to run it again, so a proof can be shared on its own. Like the report, they name no agent. A
scan made before this layout gets its `proofs/` filled on its next report (`/scan report SCAN`, or `sleipnir scan --report SCAN`).

## Where it lives

```text
.sleipnir/scan/.gitignore          keeps every scan out of the project's commits
.sleipnir/scan/SCAN/scan.json      the ledger
.sleipnir/scan/SCAN/functions.json the inventory's functions (written once)
.sleipnir/scan/SCAN/BRIEF.md       the protocol the goal points at
.sleipnir/scan/SCAN/REPORT.md      the report, with findings.json, findings.sarif, map.json, findings.csv and
                                   report.html (the report as a page that prints to PDF)
.sleipnir/scan/SCAN/proofs/        every reproduced run, self-contained (see Reproducers)
```
