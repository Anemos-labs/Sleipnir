---
name: spec
description: Goal to specification, vertical slices and milestones a judge can check. Load when planning a product or large feature, writing an agreement, or splitting work into tasks.
license: MIT
author: Adapted from Matt Pocock's grilling, to-spec and to-tickets skills (github.com/mattpocock/skills), MIT licence
---

# Spec

Misalignment is the most common failure: the result works and is not what was asked for. A spec closes the gap before code is written. Use the user's words for every name the user gave.

## 1. Walk the design tree

List the decisions the goal requires as a tree: each decision has the decisions that depend on it. Work it in rounds. The frontier is every decision whose prerequisites are settled.

- A fact (what the code does, which tools are installed, what a library supports) is found by looking, never asked: read the code, run a command, send a scout.
- A decision that is yours (implementation, layout, internal names, libraries in a new product) is made with your recommended answer and recorded as an assumption.
- A decision only the user can make (product scope, a preference, something irreversible) is asked when someone can answer; otherwise take the most literal reasonable reading, record it as an assumption and continue.

Recompute the frontier after each round. Stop when nothing is left silently assumed.

## 2. Write the spec

Keep it short and concrete. Sections, dropping any that do not apply:

- **Problem:** what the user faces, in their terms.
- **Solution:** what the user will be able to do, in their terms.
- **User stories:** a long numbered list, "As a <actor>, I want <capability>, so that <benefit>", covering every workflow including errors, empty states, limits, configuration and operations.
- **Decisions:** modules built or changed and their interfaces, data shapes, API contracts, schema, the stack and why, security assumptions. Names and shapes, not file-by-file code. A snippet that pins a decision more precisely than prose (a type, a state machine, a schema) may be inlined.
- **Contract:** when parts built separately talk to each other (a user interface and its API, a client and a service), one machine-readable file in the repository that both sides build against: an OpenAPI document, a JSON Schema, shared types or a protobuf schema. It lists every endpoint with request and response shapes, status codes and error bodies, and the authentication flow from a signed-out start. Name its owner and the contract test that fails when either side drifts from it.
- **Testing:** the seams under test (prefer existing ones, the highest that works, as few as possible), what a good test is here (observable behaviour, not internals), and the end-to-end check that proves the whole.
- **Out of scope:** what will not be built, so nobody builds it.
- **Assumptions:** every decision taken without the user, so they can correct it.

## 3. Slice it vertically

Break the work into tracer-bullet slices:

- Each slice cuts a narrow but complete path through every layer it needs (data, API, interface, tests) and is demonstrable on its own. Not one layer at a time.
- Each slice fits in one agent's assignment.
- Prefactoring that makes later slices easy comes first ("make the change easy, then make the easy change").
- Each slice names the slices that block it. A slice with no blockers can start now, so independent slices run in parallel.
- A wide mechanical refactor is the exception: expand, migrate in batches, contract.

## 4. Milestones a judge can check

Group slices into milestones. Each milestone has acceptance criteria that someone who did not build it can check by observation: a command and its expected output, a page and what it must show, a test suite that must pass, a measured limit ("lists 10 000 rows in under 200 ms"). A milestone whose flows cross parts (a page that calls the API) is checked by an end-to-end run of the assembled program, with the real server, from a clean start ("signed out, sign in through the form, create a record, see it in the list with the values entered"), never by unit tests of each side alone. "Works well" or "is polished" is not a criterion; "every screen has loading, empty and error states, screenshotted at 375 and 1440 pixels in both themes" is.

## In a team

The spec becomes the agreement of a planning task: interfaces and data shapes, ownership of each slice by role and files, acceptance criteria, and how the result is verified. The manager turns slices into tasks with the blocking edges as dependencies and copies the acceptance criteria into each task verbatim.
