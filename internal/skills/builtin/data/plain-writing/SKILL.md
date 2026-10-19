---
name: plain-writing
description: Clear technical writing without machine-generated tells. Load before writing a README, docs, site copy, release notes, a commit or PR message, or a report.
license: MIT
author: Adapted from pstack's unslop and technical-writing skills by Lauren Tan (github.com/cursor/plugins), MIT licence
---

# Plain writing

Write so a tired engineer understands on the first read. Write it clean as you draft; a cleanup pass afterwards misses most of it.

## Above everything

- Cut every word that does no work. "In order to" is "to"; "it is important to note that" is nothing.
- Use the short, everyday word: use, not utilise or leverage; help, not facilitate; many, not numerous.
- Write the real name: the actual command, flag, file, function or setting, never a synonym or a description of it.
- Say what it does, not how it feels. "Queries are fast and safe" says nothing; "`.toSQL()` returns the exact string sent to the database" says something. A sentence that could appear unchanged in another project's documentation tells the reader nothing about this one. Cut it.
- Give the number: not "significantly faster" but "from 1.8 s to 240 ms on 10 000 rows".

## Pick one mode per document

- **Tutorial** (learning by doing): says what the reader will build, every step shows a visible result and what they should see.
- **How-to** (a goal): steps only, titled by the task ("How to deploy behind nginx"), forks as "If you want x, do y".
- **Reference** (lookup): facts, options, limits and errors, dry and complete, structured like the thing it describes. Generate it from the code where possible.
- **Explanation** (why): one topic, the design decisions, constraints and trade-offs, with a view.

A README usually opens with what the project is and who it is for, then a quick start that works when pasted, then links to the rest. Do not mix tutorial steps into reference tables; split and link.

## Sentences

- Address the reader as "you", in the present tense. Instructions are commands: "Run `make test`." Put the condition first: "To reset the database, run ...".
- Name the actor: "the server rejects the request", not "the request is rejected".
- One idea per sentence; split a sentence the reader has to parse twice. Mix lengths so it does not read like a machine.
- Whole sentences with articles and verbs. No arrows, symbol-speak or unexplained abbreviations.
- Never "simply", "easy" or "just" in a procedure.
- Link text says where the link goes, never "click here".

## Tells to remove

- Vocabulary: delve, crucial, pivotal, robust, seamless, comprehensive, vibrant, showcase, underscore, testament, tapestry, landscape, leverage, harness, unlock, elevate, empower, cutting-edge, game-changer, supercharge, effortless, blazing fast.
- "Serves as", "stands as", "boasts" for "is" or "has". "Not just X, but Y". Ideas forced into groups of three. Synonyms rotated for one thing. "From X to Y" where X and Y are not on a scale.
- Trailing "-ing" phrases: "..., ensuring reliability", "..., highlighting its flexibility".
- Vague attributions ("experts agree"), hedging stacks ("could potentially"), generic conclusions ("the future looks bright").
- Abstract metaphor nouns where a plain word exists: substrate, vector, surface (as in "API surface"), north star, flywheel, paradigm.
- Em dashes as connectors and colons as mid-sentence connectors: end the sentence or use a comma. A colon before a list is fine.
- Bold on every other phrase, a bold label restating the line ("**Speed:** Speed improved"), Title Case Headings (use sentence case), decorative emoji, curly quotes.
- Chatbot phrases: "Great question", "I hope this helps", "Let me know if", "Certainly".

## Commits, pull requests and reports

Say what changed and why, for the person who reads it later. Before and after, in what a user would notice. Every claim carries its evidence (the command and its result, a file:line) or its label (measured, inferred, guessed). Never invent a link, a number or a reference.
