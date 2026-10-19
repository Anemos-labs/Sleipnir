---
name: codebase-design
description: Deep modules, interfaces and seams: vocabulary and principles. Load when designing or restructuring modules or deciding where an interface goes.
license: MIT
author: Adapted from Matt Pocock's codebase-design skill (github.com/mattpocock/skills) and pstack principles by Lauren Tan (github.com/cursor/plugins), MIT licence
---

# Codebase design

Design **deep modules**: a lot of behaviour behind a small interface, at a clean seam, tested through that interface. The aim is leverage for callers, locality for maintainers and testability for everyone.

## Terms

Use these words exactly, in agreements, tasks and reviews, so every agent means the same thing.

- **Module:** anything with an interface and an implementation, at any scale: a function, a type, a package, a slice through several tiers.
- **Interface:** everything a caller must know to use the module correctly: the signature, and also invariants, ordering constraints, error modes, required configuration and performance characteristics.
- **Implementation:** the code inside the module.
- **Depth:** how much behaviour a caller or a test gets per unit of interface it has to learn. Deep means a small interface over a lot of behaviour; shallow means the interface is nearly as complex as what it hides.
- **Seam:** the place where the module's interface lives and where behaviour can be swapped without editing callers.
- **Adapter:** a concrete thing that fills a seam (a Postgres store, an in-memory fake).
- **Leverage:** what callers gain from depth. **Locality:** what maintainers gain: change, bugs and knowledge concentrate in one place.

## Principles

- **The deletion test.** Imagine deleting the module. If complexity vanishes, it was a pass-through; inline it. If the complexity reappears in every caller, it earns its place.
- **The interface is the test surface.** Callers and tests cross the same seam. Wanting to test past the interface means the module has the wrong shape.
- **One adapter is a hypothetical seam; two make it real.** Do not add a seam until something varies across it.
- **Depth belongs to the interface.** A deep module may be built from small parts inside; they are just not part of its interface.
- **Organise by domain knowledge, not by sequence.** A module owns one body of knowledge and its invariants. Modules named for steps (load, validate, transform, save) repeat the same rules in each step.
- **Encode the domain in a structure:** a state machine, a typed model, a table or registry, a reducer, the right collection. Prefer boring code when the current shape is already clear and unlikely to grow; an abstraction must remove branches, duplicated rules or invalid states, not only add indirection.
- **Boundaries:** parse external data into domain types where it enters, expose domain concepts rather than wire, storage or framework types, keep general mechanism inside and special-purpose policy at the edge.

## Designing for testability

1. **Accept dependencies, do not create them.** `processOrder(order, gateway)` is testable; a function that constructs its own payment client is not.
2. **Return results instead of producing side effects** where you can: `discount(cart)` returning a value beats `applyDiscount(cart)` mutating it.
3. **Small surface.** Fewer operations and fewer parameters mean fewer tests and simpler setup.

When you design an interface, ask: can it have fewer operations, simpler parameters, more hidden inside?

## Design it twice

For an interface that many parts will depend on, sketch two or three radically different shapes (different operations, different seam placement) and compare them on depth, locality and where the seam sits before choosing. Say in the agreement or report which you rejected and why.

## Wide changes

A mechanical change whose blast radius crosses the codebase (renaming a shared type, a column) cannot land as one green step. Sequence it as expand, migrate, contract: add the new form beside the old, move callers over in batches that each keep the build green, then delete the old form once no caller remains.
