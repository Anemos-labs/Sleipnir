---
name: diagnose
description: Feedback-loop-first diagnosis of hard bugs, flaky tests and performance regressions. Load before changing code when something fails and the cause is not obvious.
license: MIT
author: Adapted from Matt Pocock's diagnosing-bugs skill (github.com/mattpocock/skills) and pstack principles by Lauren Tan (github.com/cursor/plugins), MIT licence
---

# Diagnose

Do the phases in order. Skip one only with a reason you state in your report.

Never show a secret in a command or its output: write `<REDACTED>` in its place and keep credentials in environment variables.

## 1. Build a feedback loop

This is most of the work. With a fast pass/fail signal that goes red on this bug, the cause will be found; without one, reading code produces theories, not fixes. Try, roughly in this order:

1. A failing test at whatever level reaches the bug (unit, integration, end to end).
2. A curl or HTTP script against a running server.
3. The command-line program with a fixture input, its output diffed against a known-good one.
4. A headless browser script (Playwright) that drives the page and asserts on the DOM, the console or the network.
5. A captured request, payload or event log replayed through the code path.
6. A throwaway harness that runs the smallest part of the system that shows the bug.
7. A property or fuzz loop: many random inputs, looking for the failure.
8. `git bisect run` with a script, when the bug appeared between two known commits.
9. A differential run: the same input through the old and the new version, outputs diffed.

Then tighten it: faster (skip unrelated setup), sharper (assert the exact symptom, not "did not crash"), deterministic (pin time, seed randomness, isolate files and network). For a flaky bug, raise the reproduction rate (loop it, add load, narrow timing windows) until it fails often enough to study.

Phase 1 is done when you can name one command you have already run that drives the real code path, asserts the reported symptom, gives the same verdict every run and takes seconds. If you catch yourself building a theory before that command exists, stop and build the loop.

If no loop can be built, say so in your report: what you tried, and what would make it possible (access to an environment, a captured log, permission to add instrumentation). Do not guess at a fix without one.

## 2. Reproduce and minimise

Run the loop and watch it fail with the symptom that was reported, not a nearby failure. Then shrink the scenario: remove inputs, steps, configuration and callers one at a time, rerunning after each cut, until every remaining piece is needed for the failure.

## 3. Hypothesise

Write 3 to 5 ranked hypotheses before testing any. Each must be falsifiable: "if X is the cause, changing Y makes the bug disappear". A hypothesis without a prediction is discarded. In a team, post the ranked list where the owner of the code will see it (mail or the task); someone may rule one out at once. Do not wait for an answer.

## 4. Instrument

Each probe tests one prediction; change one variable at a time. Prefer a debugger or REPL, then targeted logs at the boundaries that tell hypotheses apart. Tag every temporary log with a unique prefix such as `[DEBUG-a4f2]` so one search removes them all. For performance, measure a baseline first (a timing harness, a profiler, a query plan) and compare against it; logs rarely explain slowness.

When two fixes built on the same assumption have failed, write the assumption down and test it directly before a third.

## 5. Fix at the root, with a regression test

Find the first place the system did something it should not. A retry, a longer timeout, a caught exception or a guard where the symptom surfaced hides the bug; it is a fix only when the state it handles is legitimate.

1. Turn the minimised repro into a test at a level that exercises the real bug pattern. If no such level exists, say so: that is a finding about the design.
2. Watch the test fail.
3. Fix the cause. Check for the same fault elsewhere and fix those too.
4. Watch the test pass, then rerun the original loop on the unminimised scenario.

## 6. Clean up

- The original loop no longer reproduces the bug.
- The regression test passes (or the missing seam is reported).
- No `[DEBUG-` logs remain (search for the prefix).
- Throwaway harnesses are deleted.
- The report or commit message names the cause that turned out to be right, and the cheap always-on signal (a log line, an assertion, a metric) that would have shown it at once, if there is one.
