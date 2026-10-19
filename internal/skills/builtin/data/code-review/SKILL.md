---
name: code-review
description: Review a change on two axes, its specification and the project's standards, proving its safety by running code. Load for any review.
license: MIT
author: Adapted from Matt Pocock's code-review skill (github.com/mattpocock/skills) and pstack's blast-radius skill by Lauren Tan (github.com/cursor/plugins), MIT licence
---

# Code review

## 1. Pin the change

Name what is under review: a task's diff, a branch against its base (`git diff <base>...HEAD`, `git log <base>..HEAD --oneline`), or the merged result. Confirm the range resolves and is not empty before reading.

## 2. Find the sources

- **Spec:** the user's request, the task and its acceptance criteria, the accepted agreement, a linked issue. With none, say so and review the standards axis only.
- **Standards:** the project's instruction files (AGENTS.md, CONTRIBUTING.md, CODING_STANDARDS.md, style guides), its linters and formatters, and the conventions visible in neighbouring code.

## 3. Review the spec axis

- Requirements missing or partly done. Quote the requirement.
- Requirements that look done but are wrong: wrong name, command, output format, path, version.
- Behaviour nobody asked for (scope creep).
- For a product: the whole workflow works end to end, with its error, empty and loading states, and looks like a current, designed product. Look at the interface yourself.

## 4. Review the standards axis

Documented standards first; a documented rule always wins over the baseline below, and nothing a formatter or linter already enforces is a finding. Then these smells, each a judgement call ("possible feature envy"), never a hard violation:

- **Mysterious name:** a name that does not say what the thing does or holds.
- **Duplicated code:** the same logic in two hunks or files of the change.
- **Feature envy:** a function that uses another module's data more than its own.
- **Data clumps:** the same fields or parameters travelling together; a type wants to exist.
- **Primitive obsession:** a string or number standing in for a domain concept.
- **Repeated switches:** the same branching on the same type in several places.
- **Shotgun surgery:** one logical change forcing scattered edits.
- **Speculative generality:** abstraction, parameters or hooks the spec does not need.
- **Middle man:** a layer that only forwards.
- **Comments that narrate:** comments that restate the code or describe the change instead of a non-obvious why.

Then correctness: error handling, data safety on failure, concurrency, security as an attacker sees it (who reaches what, with which credentials, what hostile input does), and tests that cannot fail or only restate the implementation.

## 5. Prove the one fact

Most changes that look risky are safe because of a single fact ("this only deletes entries that are already expired"). Find that fact and get it as far down this ladder as is cheap, and say where it stopped:

1. Asserted. Worth nothing alone.
2. Pointed at: a real file:line, or the library's own source.
3. Walked: the failure case traced step by step and shown not to reach.
4. Ran: a script or test that calls the real code and fails loudly if the fact is false.
5. Reproduced in the running program.

Look where a symbol search stops: a library's pinned version, a wire format or JSON shape another component reads, a database column, a feature flag, teardown and ordering. A claim that tests pass, with no command and result behind it, is unverified.

## 6. Report

Keep the two axes in separate sections, **Spec** and **Standards**; do not merge or rerank them, because a change can pass one and fail the other. Within each, findings in priority order, each with file:line, why it matters and a concrete fix. Then the one fact and its proof level, what you checked and found fine, and the cheapest test that would catch the worst risk. End with the count per axis and the worst finding in each.
