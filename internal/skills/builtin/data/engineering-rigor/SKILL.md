---
name: engineering-rigor
description: Rigorous engineering: data shapes first, smallest correct diff, proof by running. Load for a change across files, a hard-to-undo decision, or a fix that keeps failing.
license: MIT
author: Adapted from pstack poteto-mode by Lauren Tan (github.com/cursor/plugins), MIT licence
---

# Engineering rigor

Write less code of higher quality, and prove it works. Apply the principles below that fit the task, and in your report name each one that changed a decision and what it changed.

## Before writing code

- **Name the data shape first.** Decide the core types and data structures before the logic: what the data is, who owns it, what concurrent actors share. Most later bugs are a missing or wrong type.
- **Model the domain.** Encode the domain in a structure instead of scattered conditionals: a state machine instead of booleans that must stay in sync, a typed model instead of loose parameters, a table or registry instead of an if/else chain repeated across files. The sign you skipped this: a new feature adds one more branch to an existing chain.
- **Redesign from first principles.** When a new requirement arrives, shape the code as if it had been there from day one instead of bolting it onto the side.
- **Subtract before you add.** Remove dead code and needless layers first, then build on the simpler base. No speculative parameters, hooks, validators or abstractions the request does not need. One implementation behind an interface is a hypothetical seam; two make it real.
- **Smallest diff that solves the problem completely.** Fewer lines beat elegant boilerplate. Put each decision in one place and pass the result.
- **Experience first.** When convenience for the implementer conflicts with the experience of the person using the result (an end user, a colleague calling the library, the next maintainer), choose their experience. Ship fewer features finished well over many rough ones.

## Structure

- **Boundary discipline.** Validate, parse and handle errors where data enters the system (arguments, files, requests, external APIs). Inside, trust the types and do not re-validate. Keep business logic in pure functions the thin shell calls.
- **Make illegal states unrepresentable.** Use the type system: discriminated unions, distinct types for ids and units, parsing into domain types at the boundary.
- **Idempotent operations.** Commands, migrations, lifecycle steps and loops that can crash and be retried converge to the same end state when run twice.
- **Separate before serialising shared state.** When concurrent actors could write the same file, key or branch, remove the sharing (one owner, separate files) before adding locks.
- **Migrate callers, then delete the old API** in the same change. No compatibility layer that nobody will remove.

## Verification

- **Prove it works.** Check the real thing directly: run the program, read the actual value, load the page. A proxy (it compiles, a teammate said so, a file changed) is not proof. When a check fails, suspect the observation before the system.
- **Script the check.** The strongest proof is a command a reviewer can rerun. Keep it and put it in your report.
- **Explain the number.** Before reporting a measurement (a speed-up, a latency, a pass rate), name what limits it and rule out that it measured something else: errors, cached or skipped work, code that never ran, noise. Give the run count and the spread.
- **Sequence verifiable units.** Split long work into small steps that each end in a check, and verify each before the next.

## When something keeps failing

- **Fix root causes.** The symptom is where a wrong state became visible; the cause is the first place the system did something it should not. A retry, a wider timeout, a swallowed error or a clamped value hides the bug. Ask whether the state that broke the code is legitimate: if yes, handle it where it broke; if no, find what produced it. Then look for the same fault elsewhere.
- **Attack the premise.** When two fixes that share one assumption have both failed, write the assumption down as one sentence and test it directly before trying a third.
- For a hard bug, load the diagnose skill.

## Decisions

- **Decide what is reversible.** Code, branches, files and tasks can be changed later: make the reasonable choice, state it, and continue.
- **Observe instead of asking.** When a "which approach" question is a fact you could find by running something (behaviour, timing, layout, output), build a quick prototype and let the result decide (the prototype skill). Keep questions for product or preference calls no experiment can settle, and for irreversible steps.
- **No is an acceptable answer.** When asked whether to add something, give a real judgement. Say when an idea does not earn its place.
