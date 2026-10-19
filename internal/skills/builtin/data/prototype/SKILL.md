---
name: prototype
description: Throwaway prototype that settles a design question by observation. Load when choosing between layouts, interactions or state models.
license: MIT
author: Adapted from Matt Pocock's prototype skill (github.com/mattpocock/skills) and pstack's prototype playbook by Lauren Tan (github.com/cursor/plugins), MIT licence
---

# Prototype

A prototype is throwaway code that answers one question. Write the question down in one line first; the question decides the shape.

## Which kind

- **"What should this look like?"** Build several radically different variations of the screen, default 3 and at most 5, on the route where it will live, switched by a `?variant=` URL parameter and a small floating bar that flips between them. Use the real layout, header, navigation and realistic data: an empty page makes every variant look fine. Only when nothing existing can host it, add a throwaway route named so it is obviously a prototype. Screenshot each variant at phone and desktop widths and in both themes, compare them against the question, then pick one or combine the best parts.
- **"Does this logic or state model hold up?"** Build a small runnable model (one HTML file or one script) that drives the state machine through the cases that are hard to reason about, with buttons or a scripted walkthrough, and prints the full state after every action.
- **"Which approach is faster, or does it work at all?"** Build the smallest version of each candidate and measure them on the same input.

When the question is ambiguous, pick the kind that matches the code around it (a page means interface, a module means logic) and state the assumption at the top of the prototype.

## Rules

1. **Throwaway and marked as such.** Put it next to what it is for, named so nobody mistakes it for product code.
2. **One command to run.** A script in the project's task runner, or a file to open.
3. **No persistence** unless persistence is the question; then use a scratch file or database named for deletion.
4. **No polish.** No tests, no error handling beyond what lets it run, no abstractions.
5. **Show the state** after every action or variant switch.
6. **Capture the answer, delete the code.** Fold the chosen design into the real code, record the verdict and the question it settled in the task result or commit message, and remove the prototype (or keep it on a branch of its own, out of the main line).
