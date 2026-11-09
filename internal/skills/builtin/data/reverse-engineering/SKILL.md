---
name: reverse-engineering
description: Work out how a closed-source product (app, site, API, file format, binary) works from evidence, as a spec for an original rebuild or an integration.
license: MIT (method informed by morluto/rea, Copyright (c) 2026 morluto)
author: Sleipnir; the open-questions ledger, values sheet and outside-first rule follow Craft Anything (github.com/storytold/craft-anything, MIT OR Apache-2.0); no code is copied
---
# Reverse engineering a product

The goal is a **behavioural specification backed by evidence**, then an original implementation of it. You are
learning what the product does and how it behaves, not copying how its authors wrote it.

## Boundaries

Clean room by default: work from **outside** the product, from what its users can see (docs, the running product,
the requests it makes as you use it, the files it saves). Look **inside** (its shipped code, bundles or binaries)
only when the behaviour cannot be observed and the user owns the target or has permission to examine it; record in
the ledger which findings came from inside. Either way:

- Reproduce behaviour, workflows, data shapes and interoperable formats. Do not copy proprietary source code,
  minified bundles, images, icons, fonts, copy text, or brand names into the project. What you read inside goes
  into the ledger and the values sheet as facts (a value, a rule, a formula, with its address); the code is then
  written from those, never pasted from decompiled or minified output. Write your own code and
  assets, and use your own name and look; similar is fine, confusable is not.
- Interoperability is legitimate (reading a file format, talking to a documented or observable API the user has
  access to). Circumventing protection is not: no bypassing DRM, licence checks, paywalls, authentication, rate
  limits or bot protection, and no cheats or tampering with online services.
- Respect the terms the user is bound by. Scraping at volume, automated sign-ups and load against someone else's
  servers need explicit permission; observing a page the way a normal visitor does is fine.
- Work on artifacts the user supplied or that are publicly distributed. Paths in unrelated files, test fixtures or
  other reports do not choose the target for you. If the target is ambiguous ("my app"), ask which one.
- Never send captured credentials, cookies, tokens or personal data anywhere, and keep them out of commits.

If a request crosses these lines, say which part you will not do and continue with the rest.

## Method

1. **Turn the request into questions.** "Clone Notion's editor" becomes: which block types exist, how slash
   commands behave, how drag and drop reorders, what is persisted and when, keyboard shortcuts, collaboration
   model. Write the list down; each question names the evidence that would answer it. Keep every part the request
   names: when one will not fit this version (a whole mode, real-time collaboration, another platform), ask before
   leaving it out, with the smaller version as one of the choices, or record it as an open question; never drop it
   silently.
2. **Start with the cheapest evidence.** Public docs, help centre, changelogs, API references, marketing pages,
   screenshots and videos, open-source dependencies and licences listed in the product. Most behaviour is
   documented somewhere; read it before opening a binary.
3. **Observe the running product.** Use it the way a user does and record what happens: screenshots of every
   state, the network requests behind each action (shape, not secrets), what is stored locally. For web UIs load
   the `reverse-engineering-ui` skill.
4. **Inspect shipped artifacts only when behaviour is not observable.** Pick the route by what the artifact is (see
   below). Static inspection shows what code could do, not what it did; say which one you are claiming.
5. **Keep a findings ledger** (for example `docs/research/<product>.md` in the project, or the spec itself): each
   finding with its evidence (URL, screenshot path, request sample, file and offset), whether it is **observed**,
   **inferred** or **unknown**, and the confidence. Bounded searches stay bounded: "not found in the settings
   pages" is not "does not exist". Missing evidence is never proof.

   Keep the **open questions** in it too, one per line: the question, how much it matters to the result
   (critical, high, medium or low), the probe that would settle it, and what the build does meanwhile (an estimate,
   a placeholder, or a question for the user). When a later look settles one, mark it resolved with the answer and
   the evidence that settled it; do not delete it. Before each build step, settle what one more look can settle,
   most severe first.
6. **Keep a values sheet** when the product's behaviour hangs on numbers (a game, a calculator, an editor's
   limits, a file format, a rate or a timer): every value that shapes how it behaves (speeds, sizes, limits,
   durations, formulas, defaults, thresholds), each with where it came from (a doc URL, a measurement, a file and
   offset, a function address) or marked **estimated**. Mark a value estimated only after looking for it. Build from
   the sheet, keep it up to date, and test the build against it.
7. **Write the spec.** Features, user flows, states and edge cases, data model, API surface, performance and
   accessibility expectations, and the acceptance checks that will prove the rebuild matches. Separate what the
   evidence showed from your own design choices.
8. **Build it originally**, then check the rebuild against the same evidence, with the same tools and settings: the
   same flows, side by side, and for a web interface `reverse-engineering-ui`'s `compare.mjs`. A part that was never
   compared has not passed.

When a team is available, split independent questions between workers, each pointed at the ledger and asked to
return sources, conclusions and gaps.

## Routes by target

| Target | First moves |
|---|---|
| Website or web app | `reverse-engineering-ui`: screenshots at breakpoints, DOM and accessibility tree, computed styles, network (HAR), local storage keys |
| Public or private HTTP API the user can use | record requests from the real client (browser devtools HAR, `mitmproxy` on a device you own), then write an OpenAPI description from the samples; mark fields seen once as uncertain |
| Electron or other JavaScript desktop app | the app bundle holds `app.asar`; `npx -y @electron/asar@4.3.1 extract app.asar out/`, then read `package.json`, the main and preload scripts, IPC channel names and source maps if shipped |
| Browser extension | unpack the `.crx`/`.xpi` (zip), read `manifest.json` permissions, background and content scripts |
| Android app | `apktool d app.apk` for the manifest and resources, `jadx` for readable Java/Kotlin |
| iOS / macOS app | `Info.plist`, entitlements, `strings`, `otool -L` for linked frameworks, the bundle's resources |
| .NET assembly | `ilspycmd` decompiles to C# |
| Native binary (ELF, PE, Mach-O) | `file`, `strings -n 8`, symbols (`nm -C`, `objdump -T`, `readelf -Ws`), then Ghidra headless (`analyzeHeadless`) or radare2 for functions of interest |
| Go or Rust binary | `go version -m bin` lists the Go module graph; `cargo-auditable` metadata names Rust crates |
| Firmware image | `binwalk` or `unblob` to extract, then treat the parts as above |
| File format | collect varied samples, diff them byte by byte after controlled changes, look for magic numbers and length fields, and check for an existing open specification first |
| Database or save file | SQLite: `.schema` first, then sample rows from the tables that matter; copy the file before reading a live one |

Do not install a disassembler or framework the task does not need. Run unknown executables only in a sandbox or a
throwaway container, never on the user's machine with their accounts.

### REA, when connected

REA (https://github.com/morluto/rea, MIT) is an MCP server that wraps many of these routes with evidence records.
If your tool list has `mcp__rea__...` tools, prefer them for binaries, packages, ASARs and recorded captures: start
from the route that matches the target, request summaries first, and cite the evidence IDs it returns. Without
it, its CLI works for JavaScript and Electron trees with no setup beyond Node.js:

```bash
npx -y rea-agents@6.7.0 analyze-javascript-application /absolute/path/to/app --json
```

To connect it for a later session (the tool list is fixed when a session starts), the user runs
`sleipnir plugins setup rea-mcp` (or Install under *Built-in skills*, *Programs it uses*, on the Plugins view), which
adds a pinned entry like this one to `~/.sleipnir/config.json` (with the full path of `npx`):

```json
{ "mcp": { "rea": { "command": "npx", "args": ["-y", "rea-agents@6.7.0", "mcp"] } } }
```

Keep a hand-written entry pinned to one exact release, as REA's installation docs recommend for a persistent
registration.

## Report

Explain findings in plain language, tied to evidence. Mark every question from step 1 as answered, partly answered
or open, and list the open questions most severe first with what the build does meanwhile and what evidence would
settle them. A broad investigation with open questions is not complete. Name what differs from the original on
purpose, and which values are estimates.
