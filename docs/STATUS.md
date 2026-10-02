# Status

Built and tested: the layered cache engine, the swarm (typed board and mail, leases, git-worktree isolation, a verifying merge queue),
the terminal programs (`chat`, the `watch` cockpit, `replay`), MCP, skills, hooks, the RL environment, the permission engine and project
trust. About 4,200 tests, 100 fuzz targets and a nightly run (every fuzz target, the suite three times under the race detector, a thousand
random mixtures of endpoint faults, a twenty-thousand-step soak) back it; CI runs on Linux (amd64 and arm64) and macOS, and builds
Windows, which is informational. It has been measured on real models (nine models of one marketplace, `docs/VALIDATION.md`), compared before
and after its own fixes on a fixed suite of verifiable tasks (`docs/BENCHMARKS.md`, one model so far: about a third cheaper per task, the pass
rate within noise, and one measure that got worse and is open), and used for real work: a register of forty-two findings from the benchmark,
the end-to-end tests and four logged sessions, each with a fix and a test (`docs/DOGFOOD.md`).

Not done: a first release (the version is unreleased and no licence is chosen yet), Windows support for the permission engine, the OpenAI
Responses dialect, the scale ladder on a live endpoint and the rest of the planned dogfooding, and the second half of the benchmark. Those and
what the owner has to switch on are in **`docs/ROADMAP.md`**, which is where an agent or a person picking this up should start.

