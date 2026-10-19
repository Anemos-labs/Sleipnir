---
name: tdd
description: Test-first development with tests worth keeping: seams, red before green, behaviour over internals. Load when adding behaviour or writing or judging tests.
license: MIT
author: Adapted from Matt Pocock's tdd skill (github.com/mattpocock/skills) and pstack's test-behavior-not-implementation principle by Lauren Tan (github.com/cursor/plugins), MIT licence
---

# Test-driven development

## Seams first

A seam is the public interface where a test observes behaviour without reaching inside: an exported function, a command line, an HTTP endpoint, a page. Before the first test, write down the seams you will test, with one line each on what a test there catches and what it misses. Prefer existing seams and the highest one that works; the fewer seams, the better. In a team, the seams come from the accepted agreement; when it names none, put yours in your task result. Test only at those seams.

## The loop

- **Red before green.** Write one failing test, watch it fail for the right reason, then write only enough code to pass it.
- **Vertical slices.** One seam, one test, one minimal implementation per cycle; each cycle is a tracer bullet that teaches the next. Writing all the tests first tests imagined behaviour and the shape of things instead of what users see.
- **Refactor after green**, with the tests passing before and after.
- Run the single test file often, the type checker regularly and the whole suite at the end.

## What a good test is

A test is a claim about what users of the code can observe: return values, errors, state they can read back, and effects on systems outside the code (a request sent, a row written, a message emitted). It reads like a specification ("a user can check out a valid cart") and survives a rewrite of the internals.

Judge every test with two imagined edits:

1. **Break the behaviour.** Make the code return a wrong value, skip an effect or do it twice. If the test still passes, it proves nothing.
2. **Change only the internals.** Keep every observable result and restructure the code. If the test now fails, it is pinned to the implementation.

Keep tests that fail the first edit and pass the second. Rewrite or delete the rest.

## Anti-patterns

- **Implementation-coupled:** mocks your own modules, tests private functions, asserts call counts or order, or verifies through a side channel (querying the database instead of reading back through the interface).
- **Tautological:** the expected value is computed the way the code computes it, so the test cannot disagree with the code. Expected values come from an independent source: a literal, a worked example, the specification.
- **Hollow:** asserts something looser than the contract ("no error", "some result") or data the test built itself.
- **Pinned wording:** asserts an exact error message nobody reads. Assert the error type or code callers branch on.

## Mocks

Mock only at system boundaries: external services, time, randomness, sometimes the file system or the database (prefer a real test database). Never mock code you own. Make boundaries easy to replace by passing dependencies in rather than constructing them inside, and by giving each external operation its own function (`getUser`, `createOrder`) instead of one generic fetcher. What a mocked external service receives is behaviour, because that service observes it.

## Example

Good: the expected value is a literal, and the result is read back through the interface.

```
test("createUser makes the user retrievable", () => {
  const user = createUser({ name: "Alice" })
  expect(getUser(user.id).name).toBe("Alice")
})
```

Bad: recomputes the answer the way the code does.

```
test("total sums the items", () => {
  const items = [{ price: 10 }, { price: 5 }]
  expect(total(items)).toBe(items.reduce((s, i) => s + i.price, 0))
})
```
