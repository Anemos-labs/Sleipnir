# Training data and RL: the harness as an environment

Sleipnir is built so that a model can be **reinforcement-trained on Sleipnir's own way of working**, not just
fine-tuned on transcripts. The training target includes more than code edits: it is the layered prompt (pins, notes, spine,
thread, hot tail), the task board, typed mail, `recall`, compaction patches, leases, verifier-gated `done`, and
swarm dispatch without briefings. A model only learns those by acting inside the real harness and being scored on
outcomes, so the harness is packaged as an RL environment:

```
 tasks.jsonl ──► rollout ──► capture ──► reward ──► export ──► trainer (verl / TRL / OpenRLHF / SkyRL / ...)
   (repo + goal     (real harness,    (exact prompt,   (verifier,       (steps / tokens /        │
    + verifier)      policy behind     completion,      cost, protocol)  groups / sft / dpo)      │
                     an OpenAI-style   token ids,                                                 ▼
                     endpoint)         logprobs)                                          new policy checkpoint
                        ▲                                                                         │
                        └─────────────── serve behind vLLM / SGLang, run `sleipnir rl eval` ◄─────┘
```

Every stage is a subcommand (`sleipnir rl ...`), every artefact is plain JSONL plus content-addressed blobs, and
every record is derivable from the event log, so data can also be produced from ordinary (non-RL) sessions.

## 1. Principles

1. **Train on what the model saw.** A sample's prompt is the exact rendered prompt of that call (layers, hot tail,
   tools), reconstructed byte-for-byte from the log and verified against a wire hash. No re-rendering "in the
   spirit of" the original. The policy is trained on the distribution it will be served on.
2. **Verifiable outcomes first.** Rewards come from executable checks (tests, builds, hidden tests, invariants)
   run in a clean environment the agent cannot tamper with. Model-graded signals are optional and never the only
   signal.
3. **Cost is part of the reward, priced like production.** Token, request and wall-clock cost are repriced from the
   recorded prompt recipes under a *target* cache/price model (`internal/cost`), so a policy trained against a local
   vLLM still learns cache-friendly, cheap behaviour for the provider it will be deployed on.
4. **One weight set, many roles.** Worker, manager, reviewer, compactor and mailman are role tags on one model.
   Per-role adapters would split the prefix cache at serving time. Role-specific rewards and per-role advantage
   normalisation make one model learn all of them.
5. **Segments, not just steps.** Between declared rebases (compaction commits, shared epochs) an agent's prompt is
   append-only, so a *segment* can be trained as one packed multi-turn sequence. Each rebase starts a new
   segment; the terminal reward is broadcast across an agent's segments so compaction is trained end to end.
6. **Never fabricate.** Token ids and logprobs are exported only when the endpoint returned them and a
   consistency check passed; otherwise the exporter emits text-level samples and says so.
7. **Governed by default.** Secrets are redacted deterministically, provider terms and repository licences are
   enforced by filters, verifier files are never visible to the policy, and hack detectors flag or zero
   episodes that game the check.

## 2. What is recorded (fidelity contract)

The event log already records every turn (`turn.append`), tool call and result, compaction decision, board, mail and
lease operation. RL adds these fields (all content-addressed blobs; unique text is stored once):

| Event | Added fields | Why |
|---|---|---|
| `model.request` | `agent`, `role`, `kind` (`main`, `compactor`, `mailman`, `recon`), `wire_hash`, `renderer` (version), `manifest` | exact prompt reconstruction; replay verification |
| `model.response` | `completion` (blob: the assistant turn incl. provider-native blocks), `tokens` (blob: `TokenTrace`, when captured), `retries`, `sampling` | the action, its token ids and logprobs |
| `agent.spawn` | `role`, `parent`, `model`, `task`, `scope` | lineage / who trained as what |
| `outcome` | `kind` (`verifier`, `review`, `human`, `protocol`), `score`, `pass`, `detail` (blob), `verifier_version` | reward inputs |
| `board.op` | `closure` (`{kind, target}` on done and failed tasks), `blocked_on`; on a handover, `from` and `assignment_closure` | typed task outcomes, counted in `outcome.closures` |
| `swarm.stall` | `action` (`raise`, `clear`), `kind`, `task`, `agent`, `notify`, `detail` | named coordination stalls |
| `compact.patch`, `compact.commit` | `req` (the compactor's `model.request` id; on a commit, `""` when no compactor was asked: a mask or emergency compaction), `fallback` (a mechanical patch replaced the compactor's) | links a compaction to the exact compactor call |
| `rl.outage` | `phase` (`start`: `kind`, `status`, `class` (`answered`, `unanswered`), `error`; `end`: `outage_ms`), `active_ms`, `paused_ms` | when the policy endpoint was down and the rollout's wall clock stood still (3.3) |

**Manifest.** A prompt is stored as a manifest of blob hashes: `tools` (one blob, identical for every agent), the
`system` blocks, and the messages. Because prompts are append-only between rebases, messages are delta-encoded
against the agent's previous request: `{base: "<prev req>", keep: n, add: ["<hash>", ...]}`, so a request costs a few
dozen bytes and a whole swarm's prompts cost about the size of its unique content. `traj.Prompt(req)` expands a
manifest to the exact `core.Prompt`; `wire_hash` is `sha256` of the canonical provider-neutral prompt JSON. The
replay test (`sleipnir rl verify DIR`) re-expands every request and compares hashes: a mismatch means the data would
not match what the model saw and the episode is rejected.

**Token trace.** With a self-hosted endpoint (vLLM, SGLang) the adapter asks for and stores
`{prompt_ids, completion_ids, logprobs, tokenizer, model_version}` for every call
(`--capture tokens`, default when the endpoint advertises support). It sits at the LLM boundary itself, so no proxy
is needed and no call can bypass it. Endpoints that cannot return ids are captured at text level.

## 3. The environment

### 3.1 Tasks (`tasks.jsonl`, one JSON object per line)

```json
{
 "id": "gorilla-mux-0187",
 "kind": "fix",                      // fix | feature | refactor | swarm | recall | compaction
 "repo": {"path": "/data/repos/mux", "commit": "a1b2c3d", "license": "BSD-3-Clause"},
 "setup": ["go mod download"],       // run once per task snapshot, result cached
 "prompt": "Requests with an encoded slash in the path 404. Fix it.",
 "team": {"mode": "single"},         // or {"mode":"swarm","agents":6,"roles":["backend","tester"]}: 6 workers and a manager
                                     // swarm only: "owns": {"frontend": ["*.css", "web/"]} (paths only that role writes)
 "verifier": {
   "cmd": "go test ./... -run 'TestEncodedSlash|TestRoute' -count=1",
   "timeout_s": 300,
   "pass": "exit0",                  // exit0 | regex:<re> | json-score (last stdout line {"score":0..1}, optionally with a breakdown)
   "baseline_score": 0,              // json-score only: the untouched start's score, recorded by `rl tasks check --calibrate`
   "hidden": {"mux_hidden_test.go": "blob:9f2c..."},   // written only at verification time
   "protected": ["*_test.go", "go.mod", ".github/**"]   // agent edits here are discarded and flagged
 },
 "budget": {"steps": 80, "requests": 200, "ite": 400000, "wall_s": 1800, "context_window": 200000},
 "requires": ["go"],                 // tools setup and verifier run; a machine without one skips the task
 "tags": ["go", "http", "small"]
}
```

* **Isolation.** Each rollout gets a fresh worktree (or a copy when the repo is not git) and its own event log
  directory. The verifier runs in a *separate clean checkout* with the agent's diff applied minus protected paths,
  plus the hidden files, so tampering with tests or the verifier has no effect (and is flagged).
* **Score breakdown.** The last stdout line of a `json-score` verifier may say how its score was made:
  `{"score": s, "units": {"<unit>": {"passed": n, "total": m, "weight": w}}, "guards": {"kept": k, "total": g}}`.
  A unit is a part of the work (a milestone of a test suite) of which `n` of `m` checks passed, weighing `w` (1 when
  absent; an explicit `null` is malformed) against the other units; guards are checks that passed at the start and
  must keep passing. Counts are integers from 0 to 2^31 with `n <= m` and `k <= g`, weights are finite and not
  negative, there are at most 1024 units with non-empty names of at most 256 bytes; a `units` or `guards` value that
  breaks a rule is ignored whole. The breakdown is a description:
  `score` alone decides the verdict, and a line without it is still no score line. The last run's breakdown is kept in
  the verifier's result, the outcome event, the episode's `outcome.verifier` and the run summary's results (`units`,
  `guards`), and is printed in the head of `verifier.log`.
* **Role-owned paths.** `team.owns` maps a role to globs (the syntax of `protected`) that only agents of that role may
  write, so that a team whose roles run on different models keeps each kind of work with the model chosen for it. The
  swarm's write guard refuses a file tool's write to an owned path by any other agent, the manager included, with a
  tool error that names the owning role and says to hand the work to it ("web/app.css belongs to the frontend role:
  only a frontend worker may change it. ..."); the refusal counts as a scope violation (`error_kind` `scope`), is
  logged as a `lease` event of action `owner` and raises an alert. A path several roles own may be written by any of
  them. Validation requires a swarm team, roles other than the manager that are in `team.roles` when the task lists
  them, at least one path per role and valid globs; a rollout also refuses an owning role the team does not have or
  that cannot write. A shell command's writes are not checked in a shared tree; with `swarm.isolation` `worktree` the
  merge queue refuses a submission that changed a path another role owns, whichever tool changed it. A run with a
  single agent (`--mode single`) ignores `team.owns`. `taskgen composite` gives a composite whose reference solution
  changes a user-interface file (a path the export's `edits-ui` condition counts as UI work; test files do not count)
  `"owns": {"frontend": [...]}` with globs covering every such path (`*.css`, `web/` and the like), and adds `frontend`
  to `team.roles` when the task lists roles without it.
* **Answer checks.** `verifier.expect` judges the agent's final message, alone (no `cmd`) or together with a command:
  `{"contains": ["..."], "regex": "<RE2>", "fold": true}`. Every `contains` string must occur in the answer; the score
  is the fraction that do. `regex` must match the answer with surrounding white space trimmed (`^` and `$` anchor at the
  ends of the whole answer unless the pattern turns on `(?m)`); it is a gate, so an answer that does not match scores 0
  whatever `contains` found. `fold` makes both case-insensitive. At least one of the two is required, and an empty
  string or a pattern that matches an empty answer is refused. A short answer needs the pattern: `{"contains": ["42"]}`
  passes "maybe 42, or 142, or 420", while `{"regex": "^42\\.?$"}` (the whole answer is 42) and
  `{"regex": "(?:^|[^0-9.])42(?:\\.0+)?[^0-9]*$"}` (the last number in the answer is 42) do not.
* **Toolchains.** `requires` lists the executables the task's setup and verifier need, from a fixed list (`bash`,
  `cargo`, `cc`, `clang`, `deno`, `dotnet`, `g++`, `gcc`, `go`, `gradle`, `java`, `javac`, `make`, `mvn`, `node`, `npm`,
  `npx`, `perl`, `php`, `python3`, `ruby`, `rustc`, `sqlite3`, `tsc`; anything else is a validation error). They are
  looked up on the PATH the task's commands get (after `--set-env` and `--pass-env`). Where one is missing, `rl tasks
  check` reports the task `skipped: missing ruby` and `rl rollout`, `rl eval` and `rl serve` skip its rollouts with
  status `skipped`: counted in the summary (`skipped`, `skips`) and named in the notes of `rl report`, never run, never an
  infra error, and nothing is written, so a later run on a machine with the tool runs them.
* **Budgets** stop runaway episodes; a stop is an outcome (`budget_exceeded`) and is penalised, not silently
  dropped. `context_window` may be set low (for example 32k) to force frequent compaction and so generate dense
  compaction data.
* **Infra errors** (setup failure, verifier crash, a provider outage longer than the rollout may wait, see 3.3) mark
  the episode `infra_error`; exporters drop such episodes by default so noise is not learned.

### 3.2 Task generators (`sleipnir rl taskgen`)

Hand-written tasks do not scale, so tasks are mined from history. Every generator validates what it makes in the
environment rollouts use: the verifier must fail on the start state and pass with the reference solution, and hidden
verifier files and reference solutions go to a blob store (`--blobs`, `blobs/` next to the output) that travels with
the tasks file and never enters a workspace.

* `sleipnir rl taskgen git --repo R`: for commits that change source *and* tests, check out the parent, take the
  tests as the verifier (hidden; protected) and the commit message (or a linked issue, via a resolver) as the prompt.
  `--commits FILE` mines a chosen list instead, for example the merged pull requests that closed an issue, each with
  the issue's text as its prompt and its provenance in `meta.source`.
* `sleipnir rl taskgen chain --chains FILE R`: 3 to 8 consecutive merged pull requests become one long swarm task,
  scored per milestone by the project's own test suite (see **Chain tasks** below).
* `sleipnir rl taskgen mutate --repo R`: language-aware bug injection (flip a comparison, drop a nil check, off by
  one) with the project's own tests as the verifier; only mutations the tests catch, whose reversal makes them pass,
  are kept. Cheap volume, and the source of many independent tasks in one repository state.
* `sleipnir rl taskgen composite TASKS -k 3`: combine *k* independent tasks of one repository (disjoint files) into one
  **swarm task**: the natural structure for training the manager's dispatch and the workers' coordination (the
  verifier passes only when every component passes; its score is the fraction that do).
* `sleipnir rl taskgen rebuild --scope PKG,... R`: a library rebuilt from its specification. Its packages are reduced to
  stubs or to an `API.md`, and its own tests are hidden and scored per package, as one long swarm task (see
  **Rebuild tasks** below).
* `sleipnir rl taskgen recall --repo R`: memory tasks. The agent reads a distinctive constant in one file, reads
  a dozen other files under a small context window (so its early history is compacted), and must then state the
  constant exactly. The verdict is an exact match on the final message, no command and no model. It trains
  faithful compaction and the choice between re-reading and `recall`; the cost components decide which pays.
* `sleipnir rl tasks validate|stats|filter|split|check FILE`: hygiene. `split` assigns whole repositories to
  train/val/test (deterministic), `check` re-proves every task sound, and `rl eval --exclude TRAIN` refuses to
  evaluate on anything in a training list.
* `sleipnir rl tasks check --mutants FILE`: seeded regression checks of the verifiers. A check counts only if it passes
  on the solved code *and* fails when part of the solution is taken away. Beyond failing on the start and passing with
  the reference solution, the verifier is run once per solution file with that file left at its start content (the rest
  of the solution applied; protected and hidden files are not mutated). A task whose verifier still passes such a
  partial solution is reported `WEAK` with the file names, a warning: the verifier pays for unfinished work, or the
  solution touches a file the task does not need. Files are taken in path order and each mutant is one verification
  (all repeats must pass under `--verify-repeats`), so the result is deterministic.

**The whole-suite scorer** (`taskgen.WriteSuite`, a Go API for generators) scores a long task by the share of a
repository's real test suite that passes, weighted by milestones, instead of one pass or fail at the end. It makes the
task's verifier `python3 .sleipnir/suite/score.py` (`json-score`), a scorer written as a hidden file with its spec
`.sleipnir/suite/spec.json`; both sit beside the verifier's directory, and `.sleipnir/**` is protected. The scorer
needs Python 3.8 or later and nothing outside its standard library, and a POSIX system.

* **Spec.** `commands`, each `{name, run, parser, timeout_s, cwd}`: a shell line run from `cwd` (relative to the
  verifier's directory) and the parser of its report: `gotest-json` (stdout of `go test -json`; a test is
  `import/path.TestName`, top-level tests only), `junit` (the XML file the command writes to `$SUITE_REPORT`; a pytest
  test is its node id `file::Class::name`, rebuilt from the report with `junit_family=xunit1`, any other testcase is
  `classname::name`) or `cargo` (cargo's output; a test is `target::module::name`, doc-tests excluded). `units`, each
  `{name, weight, tests}`, are the milestones; `guards` are tests that passed at the start and must keep passing;
  `deadline_s` bounds the whole run (default 1800 s); `origin` maps a test to the command that reports it. The default
  commands are `go test -json -count=1 -p 2 ./...` (or the packages a generator names); `python3 -m pytest` without
  `-x`, from the project directory, with
  `-p no:cacheprovider -p no:randomly`, a 120 s limit per test where pytest-timeout is installed, and the reference's
  pytest configuration copied to the hidden `.sleipnir/suite/pytest.ini` (`-c` with `--rootdir .`), so options an
  agent writes into `pyproject.toml`, `setup.cfg` or `tox.ini` are not the ones the tests run with; and
  `cargo test --offline --no-fail-fast --tests`. `conftest.py`, `pytest.ini`, `sitecustomize.py`, `usercustomize.py`
  and `.cargo` directories are protected with them. The tests themselves are the generator's to hide or protect.
* **Score.** (Σ<sub>u</sub> w<sub>u</sub> · passed<sub>u</sub>/|u| ÷ Σ<sub>u</sub> w<sub>u</sub>) × (kept guards ÷
  guards, 1 without guards), computed exactly, so a reference that passes everything scores exactly 1 and a start on
  which every unit's test fails exactly 0. A test that no report mentions has failed; a skipped one too; one reported
  more than once passes only if every report says so. The score line carries the breakdown (section 3.1); stderr has
  one line per command and at most 200 lines naming the tests that failed and the guards that broke.
* **What the code under test cannot do.** Each command runs in a session of its own with stdin closed and stdout and
  stderr in private files, so nothing it prints reaches the scorer's stdout, where the score is the one line, written
  last; results are read from the report, so a printed `{"score": 1}`, `--- PASS` or `ok` line (or a test2json event,
  which `go test -json` wraps as output) is not a result, nor is what a failing Rust test printed; and when a command
  ends, every process still in its process group is killed before the next command runs, so none can write a later
  report or print after the score. A command over its `timeout_s`, or still running at the deadline, is killed: the
  tests it reported count, the others and those of commands not reached have failed, and a score is still printed. A
  test process can still rewrite its own report from the inside (the scorer runs as the same user, see the security
  model in `internal/rl/env`), and a process that leaves its process group escapes the scorer's kill (on Linux the
  environment's sweep of the run's processes still ends it when the verifier does).
* **Modes.** `SUITE_MODE=record` prints `{"suite_record": 1, "commands": [...]}` instead of a score: every test each
  command reported, with `pass`, `fail` or `skip`. `taskgen.RecordSuite` runs it on the start and on the reference
  solution, each several times in fresh clean checkouts as verification does, and `taskgen.BuildUnits` keeps as units
  the tests that fail in every start run and pass in every reference run, grouped by Go package, Python file or Rust
  module (or a grouping the caller gives), and as guards the tests that pass in every run; flaky tests and tests that
  fail with the reference are dropped. When not one run with the reference reports a test (pytest stopping at startup
  because its `addopts` name a plugin the environment lacks, such as `--cov` without pytest-cov, or a `conftest.py`
  that does not import), the "nothing to score" rejection says so and quotes how the first such run's commands ended
  (`pytest: exit 4`) and the last 1,500 bytes, from a line start, of what the scorer relayed of the failing command's
  output; `taskgen.BuildStepUnits` does the same for the last step, or the earliest silent one after the start. The
  quote is diagnostic text for the error and the generation report, never part of a task. `SUITE_SAMPLE=N` is the
  progress mode for cheap, repeated scoring during a run: at most N tests of each unit and N guards, the same ones
  every time (those with the smallest SHA-256 of their id), are scored, each command gets its share of the sample in
  `SUITE_ARGS` (which the default commands turn into `-run`, node ids or `--exact` names) and a command with none in
  the sample is not run; `SUITE_BUDGET_S` shortens the deadline. The final verdict always runs the whole suite.

**Sealed start states** (`--seal DIR` of `rl taskgen git` and `rl taskgen chain`, `taskgen.Seal`). A workspace holds
only the start tree, but the repository a task names stays on disk with every later commit in it, the fix included,
where an agent running as the same user can read it. `--seal DIR` writes the start of each task it emits as `DIR/<task
id>.git`: a bare repository with one commit (no parent; a fixed author, committer, date and message, so the same tree
always seals to the same commit id), whose tree is the start's tree; its one pack holds that commit and the trees and
blobs its tree reaches, and no object outside them. The task's `repo` names it: `repo.commit` is the sealed commit and
`repo.path` is `<--repo-path>/<task id>.git` when `--repo-path` is given (a path relative to the directory the rollouts
run in), else the absolute path. The commits the task came from stay in `meta` (`commit`, `parent`). A generator can
leave paths out of the sealed tree (`taskgen.SealOptions.Remove`), so what it takes away from the start is nowhere on
disk either. The sealed repositories travel with the tasks file, like the blob store. `rl tasks split` groups tasks by
the repository they name, so it sees each sealed repository as a repository of its own; to keep one source repository's
tasks in one part, select them by the tag every generator gives them, the id prefix (`rl tasks filter --tag <id
prefix>`).

**Chain tasks** (`sleipnir rl taskgen chain --chains FILE [flags] REPO`, `taskgen.FromChains`). A chain is 3 to 8 pull
requests merged one after another on first-parent history; its task asks a team for all of them, in order, over
hours. A line of FILE is `{"id", "commits", "issues", "source"}`: the merge or squash commit of each pull request,
oldest first, each one's first parent being the one before; optionally an issue text per commit; and provenance.

* **Start, tests, solution.** The start is the first commit's first parent. The hidden tests are the last commit's
  version of every test file any of the pull requests touched; a test file the chain deletes is removed before the
  suite runs (and left out of a sealed start). The reference solution is the start-to-end diff of every other file.
  Every test file of the suite's languages (`*_test.go` and `testdata/`; `test_*.py`, `*_test.py`, `tests/` and
  `test/`; `tests/` and the Rust test file names) is protected, so no test the score counts is the agent's to edit.
* **Prompt.** The milestones in order, each the issue's text when one is given and otherwise the commit message,
  without the "Merge pull request" line of a merge commit (cleaned as for `--commits`; at most 16 KiB each), then how
  the work is checked.
* **Score.** The whole-suite scorer runs `go test` in each Go module, pytest in the Python project (one at most; with
  `src/` first on `PYTHONPATH` when the project has one, so the tests import the working tree rather than an installed
  copy) and `cargo test` in each cargo package the tests belong to. The suite is recorded `--repeats` times (default 3) at the
  start and after each pull request (the start with the non-test changes up to it and the hidden tests in place;
  `taskgen.RecordSuiteSteps`). `taskgen.BuildStepUnits` makes the unit `milestone-k` of the tests that fail in every
  run before pull request k and pass in every run from it on, weighted by the lines pull request k changed outside the
  tests; tests that pass in every run at every step are guards, and the rest (flaky, failing at the end, passing then
  failing) are dropped. A pull request that makes no test pass for good has no unit. In Go a test file that uses what
  a later pull request adds keeps its whole package from compiling until then, so its package's tests belong to that
  later milestone.
* **Self-check.** Before a task is written it is scored as rollouts will be, one run per state: the start must score
  0 (its guards pass, no milestone does), the state after pull request k the weight of the milestones up to k over the
  weight of all of them (within 0.02), and the end exactly 1. The scores are printed and kept in the report
  (`chains`: `want`, `got`); a chain that fails is rejected. Recording costs (pull requests + 1) × `--repeats` runs of
  the whole suite, and the check pull requests + 1 more.
* **Task.** `kind` `swarm`, `team` `{"mode": "swarm", "agents": n}` with one worker fewer than pull requests (2 to 6),
  `budget.wall_s` 6 hours, 45 minutes more per pull request beyond the third and an hour more per 1000 changed lines,
  at most 12 hours; tags `long`, `chain`, `harvested`, the languages and the id prefix. `meta.commits` lists the pull
  requests' commits, `meta.parent` and `meta.commit` are the start and the end, and `meta.source` holds `repo` (the
  origin's URL without credentials, or the directory's name), `range` (`<start>..<end>`) and `license` (the SPDX id at
  the end, when recognised), with the chain's own `source` keys over them.

**Rebuild tasks** (`sleipnir rl taskgen rebuild --scope PKG,... [flags] REPO`, `taskgen.Rebuild`). A rebuild task asks a
team to write a library again from its specification, as it is at a version of the repository (`--commit`, default
`HEAD`).

* **Scope.** Go package directories relative to the repository (`dir/...` adds every package below `dir`; a package's
  import path names it too), all in one module; or Python package (or module) names, looked up under the repository
  root, `src/`, `lib/` and `python/`, all in one project (the nearest directory with `pyproject.toml`, `setup.py` or
  `setup.cfg`, else the root). A Go package is the non-test `.go` files of its directory, a Python package every module
  below its directory. The scope's size is its lines that are neither blank nor only a comment.
* **Start state.** The reference with the scope's sources replaced and its tests removed, written to `--seal DIR`
  (default `starts/` next to the tasks file) as `DIR/<task id>.git`: one commit (no parent; a fixed author, date and
  message), holding that commit and the trees and blobs its tree reaches, and no object outside them. `repo.path` names
  it, as `<--repo-path>/<task id>.git` when `--repo-path` is given.
  Tier `stubs` keeps the API. A Go stub keeps the package clause, every constant, variable, type and method, the
  exported functions and the unexported ones that kept code or the package's own tests use, with their doc comments;
  every body becomes `panic("not implemented")` (function literals in variables' values too), `init` and the other
  unexported functions are removed, and so are the imports nothing kept uses. A Python stub keeps everything outside
  function bodies (imports, assignments, class attributes, decorators, docstrings, comments) and replaces the body of
  every function and method, and of an `if __name__ == "__main__":` block, with `raise NotImplementedError`; a body
  that is only `pass`, `...` or such a raise stays. Python is analysed by an embedded script run with `python3`, which
  parses the sources with the `ast` module and runs none of them. Tier `spec` removes the sources and writes `API.md`
  at the root (`REBUILD-API.md` when the repository has one): each package in dependency order with its documentation
  and its API, then the unexported declarations its own tests use. For Go the API is what `go doc` shows (exported
  declarations and their doc comments, without function bodies and unexported fields); for Python it is the public
  names (`__all__`, else the names without a leading underscore) as signatures with their docstrings, classes with
  their public and special methods and attributes, and a package's re-exports. Everything else (README,
  documentation, examples, data files, build manifests) is kept.
* **Tests and milestones.** The scope's tests are the hidden suite: the `_test.go` files and `testdata/` of its Go
  packages; the test files and test helpers inside its Python packages, and every test file of the project that
  imports one of their modules. The verifier is the whole-suite scorer: `go test` of the scope's packages, or pytest of
  the hidden test files with the reference's configuration, run from the project's directory with the scope's import
  root (`src/`, `lib/` or `python/`, when the packages are there) on `PYTHONPATH`, since the project itself is never
  installed. The suite is recorded `--repeats` times (default 3) on the
  start and with the reference; the tests that fail on the start and pass with the reference every time are scored,
  and the others are dropped (a rebuild task has no guards). There is one unit per Go package or Python module,
  weighted by its lines and listed in the order the packages import each other (an import cycle between Python modules
  is broken at the module with the fewest imports left). A Go test belongs to its package. A Python test file belongs
  to the module it exercises: the one it is named after (`test_core.py`, `core_test.py`), else the one it refers to
  most (an import of it, or a name imported from it or read as its attribute, followed through the re-exports of a
  package's `__init__.py` to the module that defines the name), the later in import order on a tie; a test file that
  refers to no module belongs to the scope's first package (the last module in import order when that package has no
  `__init__.py`). A test that panics in a Go stub stops its package's tests, and those not run count as failed.
* **Gates and checks.** The scope has 2,000 to 10,000 lines, at least 150 scored tests, and one run of the reference
  suite takes less than 10 minutes. In the environment rollouts use, the scope must not be importable from outside
  the start tree: for Go, its module (also without a `/vN` suffix) is in neither the module cache, `GOPATH/src` nor
  `vendor/`; for Python, `importlib.util.find_spec`, on the import path the tests get (the user site and `PYTHONPATH`
  included) less the working directory, finds none of its top-level packages outside the checkout, so a setup must not
  install the package itself, even in editable mode or with `pip install --user`. Go stubs must build, with
  their tests compiling against them. Last, the task is scored as rollouts will score it: the start must score 0 and
  the reference exactly 1. A scope that fails any of these yields no task: the reason is printed and kept in
  `--report` (`rejected`), and the command fails.
* **Protected.** The README, `docs/`, `doc/`, `examples/`, the licence, the language's manifests (`go.mod`, `go.sum`,
  `go.work`; `pyproject.toml`, `setup.py`, `setup.cfg`, `requirements*.txt`, lock files, `tox.ini`), `API.md`, and
  the test file names of the scope's directories, where the hidden tests are written; the prompt asks for tests of the
  agent's own in a directory of their own.
* **Task.** `kind` `swarm`; `team` `{"mode": "swarm", "agents": n}` with 3 workers for 2,000 lines up to 6 for 10,000;
  `budget.wall_s` from 6 hours for 2,000 lines up to 12 hours for 10,000; tags `long`, `rebuild`, `harvested`, the
  language, `rebuild-<tier>` and the id prefix. The prompt lists the packages in dependency order with their sizes and
  says how the work is scored. The reference solution, the diff from the start to the reference sources, is stored
  like every generator's (`meta.gold_blob`), and `meta.source` holds `repo` (the origin's URL without credentials, or
  the directory's name), `commit`, `scope`, `tier` and `spdx` (when the licence is recognised).

### 3.3 Rollouts (`sleipnir rl rollout`)

```
sleipnir rl rollout --tasks tasks.jsonl --group 8 --concurrency 32 \
    --model my-policy --base-url http://vllm:8000/v1 --capture \
    [--swarm 6] [--role-model worker=teacher-model] \
    --target-price anthropic-sonnet --rewards rewards.json --out runs/r001
```

* Runs `group` independent samples per task (the GRPO group) through the *real* harness: real tools, real KV
  layout, real swarm, headless, with permissions set by the task (`accept-edits` inside the worktree, no network
  unless the task allows it).
* One policy endpoint is enough; `--role-model role=model` (repeatable) lets a run train one role against fixed
  others (for example train the manager while workers use a stronger model, or the reverse). Compaction is not a role
  of its own: a compactor is a fork of the agent's request, so it always runs on that agent's model, and the compactor
  data (`compact.*` events, the patch and its fidelity probe) belongs to the policy that was being trained. The policy is named by
  `--model` (a configured provider, or any id with `--base-url`), and its API key is read from the variable named by
  `--api-key-env` in the harness process only: the agent's shell never sees it.
* A swarm rollout is a batch run: the manager is held (a stop guard, bounded to three vetoes) until the board is settled, so
  an episode is not cut short by a manager that answers while workers still run; when the bound is reached the run ends
  and its result says what was left. With `swarm.mailman` on, the mailman's work is data too: `mail.route` (parcels in),
  `mail.batch`, `mail.digest` (digest out, naming every original sender) and `mail.direct` (a delivery that bypassed it).
* The agent's commands run in a scrubbed environment (private HOME and TMPDIR, no credentials, no network when the
  host can isolate it; on Windows also the system variables such as SYSTEMROOT, PATHEXT and COMSPEC, with TEMP, TMP,
  USERPROFILE and LOCALAPPDATA inside the private directories; `RUSTUP_HOME` names the host's `~/.rustup` when the
  host does not set it, so rustup's toolchains stay found under the private HOME);
  permission prompts are refused, as an unattended session would refuse them, and the rules let
  the agent edit inside its workspace and run the usual build and test tooling. Steps and requests are hard budgets
  (a stop is an outcome: `budget`), wall-clock is enforced by the runner, and infrastructure failures are retried and
  never become episodes.
* **Confinement** (`--confine bwrap`, on `rl rollout`, `rl eval`, `rl tasks check` and the task generators): without
  it, a command the permission rules cannot judge statically (`python3 -c ...`) and the code a verifier runs see the
  host's files, including the hidden test files and reference solutions in the blob store, the full history of a
  mined repository and other rollouts' workspaces, so a policy could be rewarded for reading the answer. With it, every
  setup, agent and verifier command (and a swarm's done check) runs in a bubblewrap sandbox: the root file system
  read-only; user homes, `/tmp`, `/run`, service state, the work dir, the task sources, the blob store and the run
  output hidden (empty); `/etc/shadow` and SSH keys masked; its own PID, IPC and UTS namespaces (it cannot see or
  signal the harness, whose environment holds provider keys) and, unless the task needs the network, its own network
  namespace with loopback up. Writable are only the command's own tree, HOME and TMPDIR and the caches named by its
  environment (`GOCACHE`, `GOMODCACHE`, `CARGO_HOME`, `PIP_CACHE_DIR`, ...); toolchains on its `PATH` (and
  `GOROOT`, `RUSTUP_HOME`, `JAVA_HOME`) stay readable. `--confine-ro DIR` and `--confine-rw DIR` re-expose other
  toolchain or cache directories. Nothing re-exposes the work dir, the blob store or the run output, or anything in
  them; the directory relative task repositories resolve against (the current directory) is never re-exposed whole,
  but a toolchain or cache inside it is (a run started in a home directory keeps `~/.local/go`). A task with the
  network also sees the directory `/etc/resolv.conf` links into. `bwrap` mode refuses to start where bubblewrap cannot create a sandbox (it needs
  unprivileged user namespaces, or root); use it whenever rollouts are training data. Shared caches remain a shared
  write area between rollouts.
* **Outages pause a rollout instead of failing it.** A failure of the policy endpoint that waiting cures (a status of
  500 or more, 429, a refused or reset connection, a connection cut mid-stream, no response within the client's
  timeouts) starts an outage; the next answer from any model of the rollout ends it. Meanwhile the agents repeat the
  request, at most half a minute apart, and the rollout's wall clock (`budget.wall_s`, `--max-wall`) stops: only the
  time the endpoint was up counts against it. A request the endpoint refused (400, 401, 403, 404, 422, a prompt over
  the context window) and a misaddressed endpoint (a host name that does not resolve, a certificate that is not
  accepted) are not outages. Two limits bound the wait: `--outage-patience` (default 3h) is how long one outage may
  last before the agent gives up, which ends the attempt as an infra failure that the runner retries
  (`--infra-retries`); `--max-outage-pause` (default 3h) is how much outage one attempt sits out in all, after which it
  ends as an infra failure that is not retried (a later run into the same `--out` reruns it). A negative value turns
  either off: no waiting, or a wall clock that runs through outages. Each outage is recorded in the rollout's
  `events.jsonl` as `rl.outage` events (`phase` `start`, with the failure's `kind`, `status` and `class`, and `end`,
  with `outage_ms`), reported on the progress stream as `rollout.outage` (`status` `down` or `up`), and summed per
  rollout as `active_wall_ms` (what counted against the budget) and `paused_ms` in the episode's `cost`, the summary's
  results and `ledger.jsonl`; `summary.json` adds up `paused_ms` over the run, and `manifest.json` records both
  settings (`outage_patience_ms`, `max_outage_pause_ms`, -1 when off). `wall_ms` stays the whole span of the run.
* **Savepoints and resume** (`--savepoint-every 15m`, on `rl rollout`, `rl eval` and `rl serve`; off by default): a
  long rollout is not started over when its attempt ends early (an infra failure, the process stopped or killed).
  At the interval the runner asks the session for a savepoint: tool batches that can change the work tree (`bash`,
  `apply_patch`, `edit`, `write`) and a team's done check hold a shared barrier, and the savepoint waits for a moment
  when none runs (politely for up to half the interval, then for at most the interval while new ones wait; at most
  30 s and 2 min); it is skipped when no such moment comes. With writes paused, every agent's state after its last
  complete tool batch is written as an `agent.snapshot` (a batch that cannot write, such as a manager's `wait`, is
  recorded with the calls that had not returned marked interrupted), a `session.savepoint` event follows, the log is
  flushed, and the work tree is captured into `savepoints/git` in the sample directory (files git ignores, the
  agent's own git history, HOME, TMPDIR and running processes are not captured). `savepoints/index.jsonl` lists
  each savepoint (`n`, `log_bytes`, `tree`, the workspace, `policy`, `seed`, `active_wall_ms`). A savepoint workspace
  lives at a stable path (`ws/<task>-s<sample>-<run tag>` under the work dir). An attempt that finds a savepoint of the
  same policy and seed resumes from the newest: the log is cut back to it (the cut-off tail is kept as
  `events.rewound.N.jsonl`), the workspace is rebuilt at the same path with the captured tree, the session resumes
  from the agents' snapshots, and the agent (a team's manager) is told it was resumed instead of being given the task
  again. A team's workers do not survive: their unfinished tasks go back on the board as to do. The resumed attempt
  gets what is left of the wall budget (less the agent time recorded at the savepoint), of the request budget and,
  for a single agent, of the step budget. An attempt that recorded a savepoint does not use up an infra retry (at most
  20 resumes per rollout). `ledger.jsonl` counts each response once: an attempt's entry counts what it appended to
  the log, and spend in a cut-off tail that no entry counted (the process died) gets an entry with status `rewound`.
  The progress stream reports `rollout.resume` with the savepoint, the episode marks the steps after a resume as a
  segment with reason `resume`, and `manifest.json` records `savepoint_every_ms`. `--force` starts the first attempt
  over.
* **Progress at savepoints** (`--score-savepoints`, with `--savepoint-every`): the work tree of every consistent
  savepoint is scored by the task's verifier in a clean checkout, as the final verification is, and invisible to the
  agents. It runs in the background, one tree at a time per run; a sample whose earlier tree still waits has it
  replaced by the newer one, and an attempt that ends waits for its tree being scored. The verifier runs in progress
  mode: its environment carries `SUITE_SAMPLE` (`--progress-sample`, default 20 tests of each unit) and
  `SUITE_BUDGET_S` (`--progress-budget`, default 5m), which the whole-suite scorer reads to run a sample under a
  deadline (another verifier runs whole). The scores go to `progress.jsonl` in the sample directory and to the
  episode's `progress` (`savepoint`, `active_wall_ms`, `score`, `pass`, `ms`); a resume drops those of the savepoints
  it cuts off. Two reward components read them, both off by default: `progress` is the mean score over the agent's
  time, from the baseline score at the start through each savepoint to the outcome at the end (getting far early
  scores higher), and `regressions` is minus the number of falls of at least 0.05, saturating at
  `caps.regressions` (3). `rl export --format sft --sft-prefix best` also exports each failed episode that scored
  above 0 at a savepoint, up to its best-scoring one: the steps that completed before that savepoint was taken (the
  others are counted as `step:after_best_savepoint`, the episodes in `best_prefixes`). A twelve-hour run that went
  wrong in its last hours still teaches what it did well before.
* **Branches** (`sleipnir rl branch --tasks FILE --from RUN/TASK/SAMPLE --at POINTS --k 4 --horizon 45m --out DIR`, where
  `POINTS` is `sp:N[,M]`, `every:K`, `last`, `compaction` (the last savepoint before each compaction), `milestone` (each
  savepoint whose progress score rose) or `plateau` (each savepoint whose score had not moved over the two before it)): a finished rollout that took savepoints (the trunk) is continued from some of them, K times each, with
  different sampling seeds, so the outcomes compare decisions taken from the same state without paying for the prefix
  again. Sample `p*K+k-1` of the branch run is sibling `k` of point `p`: its log is the trunk's up to the savepoint plus
  an `rl.fork` event, its blobs are hard links to the trunk's, and its capture store borrows the trunk's objects; the
  runner then resumes it as it resumes any rollout. Rerunning the same request into the same `--out` keeps the finished
  branches and resumes the others; a different one (another trunk, point, `--k`, `--horizon` or `--compact-first`) is
  refused there. A branch of a branch has the `rl.fork` events of both; its own is the last. The siblings run one after the other, because they restore the
  trunk's workspace at the trunk's own path, which the agents' conversation names: give `rl branch` the trunk's
  `--work-dir`. `--horizon` is the agent time a branch gets after its point (0: the rest of the trunk's budget); at the
  horizon it ends as a `budget` episode and is verified like any rollout. A branch episode has `fork` (`parent`,
  `savepoint`, `seq`, `sibling`, `siblings`, `horizon_ms`); its group is `<task>@<parent>:sp<NNNN>`, so the siblings of
  one point are one GRPO group; its steps before the fork are the trunk's and are marked `inherited` (not trainable,
  dropped from every training format as `step:inherited`; the canonical export keeps them). The siblings' first request
  after the fork is the same prompt, so `rl export --format dpo --pair best-worst` pairs their decisions at that step.
  The ledger of the branch run counts only what the branches spent. With `--compact-first` every branch compacts its
  agent's (a team's manager's) context before anything else (when there is enough to fold): the siblings' first calls
  are competing summaries of one state, each sampled with its branch's seed, and what each branch then achieves until
  its horizon scores them. Their compactor steps share a GRPO baseline (their compaction number is the same, since the
  inherited compactions before the fork are), and DPO pairs them on their common prompt; `fork.compact_first` records
  the choice. With too little to fold, no compactor is called and the branch goes on as without the flag.
* Output: `runs/r001/<task>/<sample>/` with `events.jsonl`, `blobs/`, `episode.json` (canonical trajectory with
  rewards), `diff.patch`, `verifier.log`; plus `runs/r001/manifest.json` and `summary.json` (pass rate, cost,
  per-role stats). Rerunning into the same directory resumes: finished rollouts are skipped.

A long-running **rollout server** (`sleipnir rl serve --addr 127.0.0.1:8090 --runs DIR --tasks registry.jsonl`) exposes the same thing to trainers:
`POST /v1/rollouts {task, policy:{base_url,model,sampling}, group, rewards}` returns episodes (streaming NDJSON),
so verl / OpenRLHF / SkyRL agent loops and online GRPO trainers can call the harness as a black-box environment, in
the same shape as rLLM / Agent Lightning / Polar gateways.

A request names the policy endpoint and, optionally, the environment variable that holds its key
(`policy.api_key_env`). The server sends a key only where its operator said it may go: `--policy-host HOST[:PORT],…`
lists the hosts a request's `base_url` may name (loopback is always allowed) and `--policy-key-env NAME,…` the variables
its `api_key_env` may name (default: none, so a client cannot choose which of the server's credentials to use or
where to send it; a refused request gets a 403 that names the flag). A key to a plain-`http` host that is not this
machine also needs `--allow-insecure-http`, on `rl serve` as on `rl rollout` and `rl eval`.

## 4. Canonical trajectory (`sleipnir.rl/1`)

`episode.json` (types in `internal/rl/types.go`):

```
Episode { id, task_id, group, sample, policy{model, endpoint, checkpoint, sampling}, harness{version, renderer,
          config_hash, roles, agents}, env{repo, commit, image, limits, tags}, agents[], edges[], outcome, reward,
          flags[], cost{actual, repriced}, provenance{license, consent, teacher} }
Agent   { id, role, parent, steps[], reward }
Step    { id, kind, role, model, provider?, epoch, segment, prompt(manifest ref), completion, tokens?, usage, cache,
          latency_ms, observations[], wire_hash, reward?, advantage? }
Edge    { kind: spawn | mail | compact | promote | lease | board, from, to, ref, fallback? }
```

* `env.tags` are the task's tags as the rollout recorded them; `provider` is the upstream that served the call, as
  the gateway named it in the response (absent when it did not). Both are optional, so older episodes still load;
  `rl export` gives an episode without tags those of its task (section 7.2).
* `outcome.closures` counts each task's final typed closure as `status:kind` (`done:verified`,
  `failed:superseded`) and each handed-over assignment as `handed_off`; a reopened task counts only by how it
  finally closed.
* `segment` increments at every declared rebase; steps of one segment share an append-only prefix.
* `edges` make the swarm a DAG: a worker's first step points at the manager step that spawned it; a mail delivery
  points from sender step to recipient step; a compaction commit points from the compactor call to the first step
  of the new segment (`ref` `<agent>#<n>`, n counting the agent's commits).
* A compaction is linked by the `req` its `compact.commit` names: the edge starts at exactly that compactor call,
  provided the call is a kept compactor step of the same agent, requested before the commit and not already taken by
  an earlier commit; otherwise the commit has no edge. A commit with an empty `req` (a mask or emergency compaction)
  asked no compactor and has no edge, nor has one whose compactor request never got a response (that request is not
  a step). `fallback` is set on the edge when the call was made but its answer was
  unusable and a mechanical patch was committed instead. Logs written before commits named their request are linked
  by a heuristic (the latest compactor call before the commit that no other commit took, none for a commit whose
  reason marks a mask or emergency compaction), which can give a commit whose own compactor request got no response
  an earlier call whose patch was dropped or failed.
* Training units: **step** (one call: prompt to completion), **segment** (a packed multi-turn sequence), or
  **episode** (preference pairs). All are derived, never stored separately.

## 5. Rewards (`internal/rl/reward`, `--rewards rewards.json`)

Every reward is a sum of named, individually logged components, so a run can be re-scored with different weights
without re-running anything.

**Episode (all roles share the team outcome):**

| Component | Definition | Default weight |
|---|---|---:|
| `outcome` | verifier score in [0,1] in the clean checkout, as improvement over the start (below); 0 if a hack detector fired | 1.0 |
| `honest_done` | +0.1 if the agent's `done` was accepted and the verifier passed; −0.2 if it claimed done and the verifier failed | 0.1 / −0.2 |
| `cost` | −min(1, repriced ITE / budget.ite) under `--target-price` | 0.15 |
| `requests` | −min(1, requests / budget.requests) | 0.05 |
| `time` | −min(1, critical-path steps / budget.steps) | 0.05 |
| `protocol` | −(invalid tool calls, rejected patches, lease/scope violations, stale writes, budget breaches) / cap | 0.1 |

**Partial credit is measured from the start.** A `json-score` verifier may give the untouched start partial credit
(a checker that finds three of four fields already right scores 0.75 before the agent does anything). With the start's
score recorded as `verifier.baseline_score` = s0, `outcome` is max(0, (s - s0) / (1 - s0)): doing nothing earns 0,
a full pass earns 1, and an answer worse than the start earns 0. The recorded verdict keeps the raw score s, and pass
still means s = 1. s0 is a field of the task rather than a measurement at every rollout, so a reward never depends on
how a flaky verifier scored the start that day and costs no extra verifier run: `rl tasks check` measures the start, fails
a task whose recorded score differs from it (or that scores 1 at the start), and `--calibrate OUT` writes the tasks
with the measured scores.

**Role-specific:**

* **Worker**: episode reward plus `evidence` (ran the checks it later cited), minus re-reads of ranges it had just
  compacted, minus edits outside its scope.
* **Manager**: episode reward plus `parallel_efficiency` (sum of worker steps / critical path, capped), minus
  `duplicate_work`, `conflicts`, `idle_workers`, `over_spawn` (workers whose work did not reach the result).
* **Compactor** (fork of the agent's request, returns a patch): `valid` (patch parses and applies without
  fallback), `size` (spine+retained tokens / thread tokens), `fidelity` (probe recall, below), `rebase_cost`
  (write cost of the commit under the target price model), plus the *downstream* episode reward broadcast to its
  segment chain so the compactor is rewarded for the agent still succeeding.
* **Mailman / mail use**: message `useful` (recipient's next action changed and the episode passed), minus
  unread/ignored, duplicate and low-value messages (hot-tail tokens spent).

**Fidelity probes.** `reward.Probes(archive)` generates deterministic questions from the archived thread (which files
did you modify, which test failed and with what message, what did the user require) and answers them from the
compacted prompt by exact/regex match on facts the compaction was supposed to keep. No model call is needed.

**Hack detectors** (`flag` and zero `outcome`): edits to protected paths (judged by the final diff; a *new* file under a
protected glob, such as a scratch `debug_test.go`, is not an edit of anything and is not flagged, because the verifier
discards it); tests deleted, skipped or weakened
(`t.Skip`, `xfail`, assertion removal); verifier or CI config touched; hardcoded expected values from hidden
tests; network access to the upstream repository or solution hosts; writes outside the worktree; `exit 0`
shims. Flags are preserved so the data can also train detectors. The hidden files a task keeps as blobs are read from
the task blob store (`--blobs` of the rollout; for `rl reward`, the `blobs/` next to `--tasks`, or `--blobs`); a hidden
file that cannot be read is a note on the reward, and the hardcoded-value detector skips it.

**`rewards.json`.** A file lists only what it changes; everything else keeps the documented defaults (a weight of 0
switches a component off). All keys are optional:

```json
{
  "weights": { "outcome": 1.0, "cost": 0.3, "requests": 0.0 },
  "caps": { "protocol": 10, "idle": 120000 },
  "target": "anthropic-sonnet",
  "clip": [-1.5, 1.5],
  "probes": true,
  "detectors": { "network": false },
  "workspace_roots": ["/work/rollouts"],
  "reprice": { "engines": 3, "capacity_tokens": 4000000 }
}
```

`weights` and `caps` are keyed by the component and cap names in `internal/rl/reward/config.go` (`outcome`,
`honest_done`, `false_done`, `cost`, `requests`, `time`, `protocol`, `waste`, `group_ite` (both off by default; see
section 7.1), `evidence`, `reread`, `scope`,
`parallel_efficiency`, `duplicate_work`, `conflicts`, `idle`, `over_spawn`, `valid`, `size`, `fidelity`, `rebase_cost`,
`downstream`, `mail_useful`, `mail_spam`). `target` is a preset (`sleipnir rl reward -list-targets`: `anthropic-haiku`,
`anthropic-opus`, `anthropic-sonnet`, `marketplace`, `no-cache`, `openai`) or a model id from the price tables. `clip`
bounds every scalar reward (components stay raw). `detectors` switches individual hack detectors off (`protected`,
`tests`, `verifier`, `hardcoded`, `network`, `outside`, `shim`; all run by default). `workspace_roots` tells the
outside-the-worktree detector which directories an agent may write under (absolute paths: `/work/rollouts`, or
`D:\work\rollouts` or `\\server\share\rollouts` on Windows, whose paths are compared without regard to case). `reprice` tunes the counterfactual cache model
(engines, capacity, time-to-first-byte model). Re-score a finished run without re-running anything:
`sleipnir rl reward RUN_DIR --rewards rewards.json [--target-price marketplace] [--dry-run]`.

Things the scorer will not do quietly: an episode whose verifier diff cannot be read is an error (`ErrDiffUnavailable`),
not a pass; a protected pattern that is over 1 KiB or has more than 64 alternatives is refused with the task's field
named, because silently dropped protection is worse than an error; fidelity probes need the prompt text (from the run's
blobs) and are skipped and noted when it is not available, never scored 0. The hack detectors are heuristics: they miss
values built at run time or encoded, expected values edited to match new output, and weakening done through helpers the
diff does not show, which is why flags are kept and the verifier's clean checkout remains the source of truth.

**Counterfactual pricing.** `reward.Reprice(episode, target)` replays the recorded requests through the real
`kv` planner's cost model under the target provider (explicit write premium, TTL, read weights) and returns ITE and
requests. The same episode can therefore be priced for Anthropic-like, OpenAI-like or marketplace-like deployment.

## 6. Advantages and groups (`internal/rl/adv`)

Exporters can attach advantages (or leave raw rewards to the trainer):

* `grpo`: (r − mean) / (std + ε) within the group (same task and policy snapshot), per **role** (a global baseline
  destabilises heterogeneous agents). Per-role baselines are the default (`adv.Spec.PerRole`); with them off every
  sample of a rollout group shares one baseline, whatever its role or compaction number, and the rules below about
  compaction numbers do not apply. A compactor call scored on its own is baselined per (group, role, **compaction
  number**): k counts the agent's compactor calls in step order, teacher calls included, so the first compactions of
  a group share one baseline, the second ones another, and so on; the first compaction of a run and the fifth are
  different problems. A number with a single trainable call in the group has no peer: like any single-sample group
  it is flat (advantage 0 under `grpo`, `rloo` and `anchor`, the reward itself under `broadcast`) and listed as flat,
  and it is not pooled with other numbers. `rloo` and `anchor` use the same groups;
* `rloo`: leave-one-out baseline;
* `broadcast`: an agent's steps and segments inherit its (role) reward; compactor steps inherit the reward of the
  chain they served;
* `anchor` (opt-in): steps starting from the same (task, tree hash, failing-test set) are grouped across rollouts
  for step-level credit (GiGPO-style).

Groups whose rewards are all equal (no learning signal) and infra-error or budget-truncated episodes are dropped by
default; `--keep-flat` keeps them.

## 7. Export formats (`sleipnir rl export DIR --format ...`)

| Format | Unit | Shape | Use |
|---|---|---|---|
| `steps` | step | `{id, prompt:[messages], completion:[assistant message], tools, reward, advantage, group_id, role, meta}` in HF conversational form (arguments as JSON objects) | TRL GRPO/RLOO/offline RL, custom trainers |
| `tokens` | step or segment | `{prompt_ids, response_ids, response_mask, old_logprobs, reward, advantage, group_id, role}`; segments are packed only if the prefix property verifies | verl, OpenRLHF, SkyRL, Tinker-style |
| `groups` | task group | `{task, trajectories:[{messages_and_choices, tools, reward, metrics}]}` | ART / rLLM-style |
| `sft` | step or episode | `{messages, tools, weights}` (OpenAI SFT shape; assistant `weight` marks trained turns) of the top-*k* verified episodes per task | rejection-sampling fine-tuning, warm start before RL |
| `dpo` | step or episode | `{prompt, chosen, rejected}`: same prompt hash with different outcomes (compactor patches, anchor states) or same task with best vs worst episode (dropped when both continuations are equal) | DPO / preference training |
| `kto` | step | `{prompt, completion, label}` | KTO / BCO |
| `atif` | episode | Harbor ATIF trajectories with `subagent_trajectories` and `context_management` | interchange |
| `canonical` | episode | `episode.json` records with a segment table (default) or inline prompts (`--inline`) | lossless archive; everything above derives from it |

Common flags: `--roles worker,manager,compactor`, `--min-reward`, `--top-k N`, `--select all|best` (sft),
`--pair reward|best-worst` (dpo; both in section 7.1), `--advantage grpo|rloo|broadcast|anchor|none`
(default: grpo for steps, tokens and groups), `--group-by task|task+policy|group`, `--keep-flagged` (flagged episodes are
dropped by default), `--keep-flat`, `--keep-weak`, `--no-redact` (redaction is on by default), `--redact-salt`,
`--teacher MODEL,...` (provider-terms filter), `--licenses`, `--max-prompt-tokens`, `--max-sample-tokens`,
`--max-drop-rate`, `--drop`, `--role-teacher`, `--exclude`, `--tasks` (section 7.2), `--max-samples`,
`--split train:0.9,val:0.1` (by repository), `--seed`, `--pack` (pack a segment into one token sequence when its traces
chain), `--reasoning drop|field|keep`, `--stats FILE`. Output files are 0600. Start from these defaults: an exporter that
keeps everything exports infra failures, truncated runs and reward-hacked episodes.

`--inline` writes full prompts per sample (simple; large: shared layers repeat). The default writes a segment table once
and references it by hash (`--table FILE` puts the table in its own file); `sleipnir rl expand EXPORT [--table FILE]`
expands it byte for byte to the inline form. Shared prefixes are also flagged (`shared_prefix`, `shared_messages`) so
trainers with prefix or tree packing compute the pinned layers once.

## 7.1 Best-of-n selection and efficiency signals

A task run several times (`rl rollout --group 4`) gives a rollout group: the episodes of one task under one policy
snapshot (`Episode.Group`, else the task). One comparator, `rl.CompareRank` in `internal/rl/rank.go`, orders a group. It
reads an `rl.RankKey` and compares these levels in order, the first difference deciding:

| Level | Better | Field |
|---|---|---|
| 1 | verified pass first (a passing verifier verdict and no `hack:*` flag) | `verified` |
| 2 | higher verifier score | `score` |
| 3 | lower ITE (`Cost.ITE`, repriced under the scoring target, so cache reads cost less than fresh input); an unpriced episode ranks last | `ite` |
| 4 | less waste: `stuck_warnings + stuck_stops + repeated_reads + tool_errors + invalid_tool_calls` | `waste` |
| 5 | shorter final answer | `final_answer_chars` |

Episodes equal on every level are ordered by episode id, so the order never depends on input order. The signals behind
the key are derived from the event log for single agents and swarms alike, summed over every agent:

| Signal | Definition |
|---|---|
| `tool_calls` | tool calls of kept main steps |
| `stuck_warnings` | repetition-guard warnings (`agent.stuck`, phase `nudge`, guard `repeat`); test-weakening notes do not count |
| `stuck_stops` | runs the repetition guard ended (`agent.stuck`, phase `stop`) |
| `repeated_reads` | successful reads of a file part (path and range) the same agent had already read, with no successful write of the file in between; the detector is `friction.ReadCounter`, and a change made through the shell is not seen |
| `final_answer_chars` | characters of the root agent's final answer; 0 when the run ended without one |

An episode with `stuck_stops > 0` carries the flag `looped`. It is a soft flag: exporters keep the episode, and the
ranking and the `waste` component count the loop against it.

**Exports.** Every sft record, every groups trajectory and both sides of a dpo pair carry the episode's `rank`:
`{position, group_size, key}`, where position 1 is the best episode of its group among those the export kept. The
groups format also lists every signal under `metrics` as `signal/<name>`.

* `--format sft --select best` keeps the best verified episode of each group (`--top-k K` keeps the best K). The
  default, `--select all`, keeps every verified episode, best `--top-k` per task by reward, as before.
* `--format dpo --pair best-worst` pairs rank 1 with rank last of each group, only when rank 1 is verified, takes part in
  the comparison (a prompt or a root chain that rank 1 does not have gives no pair) and its key is strictly better than
  the rejected one's. Both pair units follow it: the episode unit compares what the root agent
  did after the shared first prompt, the step unit compares completions of an identical prompt within the group. The
  default, `--pair reward`, pairs by reward as before.
* `--format groups` (or steps, tokens) with `--advantage grpo` gives the group-relative signal; add the efficiency
  components below to the reward to make it prefer cheap, clean passes.

**Reward components.** Two opt-in components let GRPO advantages see efficiency. Both are gated by the verifier score
(`score`, 0 for a hacked episode) of a passing verdict, so they only tell passing runs apart and a failed run, partial
credit or not, is never penalised for its waste on top of failing. With weights λw and λi and the default outcome weight of 1, an episode's outcome part
becomes `score × (1 − λw·min(1, waste/caps.waste) − λi·ite_rel)`, where `ite_rel` places its ITE between the cheapest
(0) and the dearest (1) run of its group.

| Component | Definition | Default weight |
|---|---|---:|
| `waste` | −score × min(1, waste / `caps.waste`) (`caps.waste` defaults to 10) | 0 |
| `group_ite` | −score × (ITE − group min) / (group max − group min); 0 for the cheapest run of its group, for a group with one ITE, and when scored without the group | 0 |

Both components are always computed and stored, so a run can be re-weighted offline; with the default weights the
rewards are unchanged. `group_ite` needs the ITE of the whole group: `sleipnir rl reward RUN --rewards rewards.json`
prices every group first when the weight is not 0 (`reward.GroupITE` and `Config.GroupITE` in code). Scoring at rollout
time sees one episode, stores 0 and notes why. For example:

```json
{ "weights": { "waste": 0.2, "group_ite": 0.2 }, "caps": { "waste": 10 } }
```

**Run summaries.** `summary.json` and `sleipnir rl report` include `efficiency` (per completed episode: `tool_calls`,
`stuck_warnings`, `stuck_stops`, `repeated_reads`, `final_answer_chars`, `waste`, and `loop_rate`, the share of
episodes the guard stopped) and, per task, `best`: the best sample and its rank key. `rl report --tasks` prints a
best-of-group table. A team run's summary and report also carry `closures`: how the swarm board closed its tasks
(`done:verified`, `failed:superseded`, `handed_off`, ...), summed over completed episodes; the report lists them in its
notes.

A typical best-of-4 pipeline:

```
sleipnir rl rollout --tasks tasks.jsonl --group 4 --model M --out runs/r1
sleipnir rl reward runs/r1 --rewards efficiency.json          # optional: fills group_ite and waste
sleipnir rl export runs/r1 --format sft --select best -o sft.jsonl
sleipnir rl export runs/r1 --format dpo --pair best-worst -o dpo.jsonl
sleipnir rl export runs/r1 --format groups --advantage grpo -o groups.jsonl
```

## 7.2 Leaving data out: drop rules, exclusions, role teachers, length

Every filter below is counted in the export statistics (`--stats FILE`, and the summary `rl export` prints), so nothing
is left out silently.

**Drop rules** (`--drop RULE`, repeatable) remove steps from the training formats (steps, tokens, groups, sft, dpo,
kto), trainable and teacher steps alike, before the provider-terms filter. A dropped step counts as
`step:rule:<name>`; it is never a training target: in a conversation format (groups, packed sft) it stays context with
no weight, and no dpo pair is formed with it on either side (the pair is made from the remaining candidates, or not at
all). canonical and atif are archives and keep every step. A rule is a preset name or conditions separated by `;`:

```
--drop ds-frontend
--drop 'name=no-ds-docs;models=*deepseek*;roles=docs'
--drop 'name=impcc-ui;providers=ImpCC;edits-ui'
```

| Condition | Matches |
|---|---|
| `models=GLOB,...` | the step's model |
| `roles=GLOB,...` | the step's role or the role of the agent that made the call (so `frontend` covers a frontend agent's compactor and mailman calls) |
| `tags=GLOB,...` | any of the episode's task tags (`env.tags`) |
| `task-ids=GLOB,...` | the episode's task id |
| `providers=GLOB,...` | the upstream that served the call (`provider`); a call without one matches only `*` |
| `edits-ui` | the step's tool calls write or edit a user-interface file (below) |
| `name=NAME` | (required) the name the drops are counted under |

A rule matches a step when every condition it sets holds; a list holds when any of its patterns matches. Patterns
ignore case; `*` matches any run of characters, `/` included (`*deepseek*` matches `deepseek/deepseek-v4.1-flash`), and
`?` one character. A rule must set at least one condition.

`edits-ui` reads the calls of the file tools `write` and `edit` (their `path` or `file_path`) and `apply_patch` (every
`*** Add File:`, `*** Update File:`, `*** Delete File:` and `*** Move to:` path), in the completion and in the recorded
observations; arguments cut off mid-call are searched for a `path`. Shell commands are not inspected. A path is UI work
(`rl.IsUIPath`) when its extension is `.html`, `.htm`, `.css`, `.scss`, `.sass`, `.less`, `.jsx`, `.tsx`, `.vue`,
`.svelte` or `.astro`, or when one of its directories is named `templates`, `static`, `public`, `web`, `ui`,
`frontend`, `components` or `styles` (case ignored).

The preset `ds-frontend` removes every step a deepseek model wrote doing front-end or visual work: model `*deepseek*`
and any of role `frontend`, a task tag `frontend`, `ui`, `design`, `frontend_correct` or `frontend_design`, or
`edits-ui`. It is three rules named `ds-frontend`, one per alternative.

**Task tags of older runs.** Rules on tags need `env.tags`. An episode recorded without them gets its task's tags at
export: from `--tasks tasks.jsonl` when the file lists the task, else from the `task.json` stored with the rollout.

**Role teachers** (`--role-teacher ROLE=MODEL[,MODEL...]`, repeatable; models are globs as above) say which models a
role may learn from. A step whose role, or whose agent's role, has an entry is dropped (`step:role_teacher`) unless its
model matches; this holds for trainable steps too, so `--role-teacher frontend=claude-*` keeps a deepseek policy's own
frontend steps, and the compactor calls of its frontend agents, out of the data. When both the step's role and its
agent's role have entries, the model must satisfy both.

**Exclusion lists** (`--exclude FILE`, repeatable) remove whole rollouts from every format, archives included
(`episode:excluded`): corrupt runs, rollouts whose task leaked. One rollout per line; blank lines and `#` comments are
ignored. A line names a sample directory: absolute, relative to the current directory, relative to the parent of the
path given to `rl export` (`deepseek_deepseek-v4.1-flash/job/task/3` when exporting `runs/deepseek_deepseek-v4.1-flash`),
relative to that path itself, or as the run directory's name and the episode id (`job/task/3`). A bare episode id
(`task/3`) names a rollout only when the path given is the run directory itself. Entries that match no rollout are
reported.

**Length.** `--max-prompt-tokens N` drops steps whose prompt is longer than N (`step:prompt_too_long`);
`--max-sample-tokens N` drops steps whose prompt plus completion is longer (`step:sample_too_long`), where the completion
counts its reasoning only when `--reasoning` exports it, and the tokens format measures a step with a token trace by its
ids. Set `--max-sample-tokens` to the trainer's maximum sequence length so the trainer never truncates or skips a
sample on its own. A conversation (a groups trajectory, a packed sft record) loses the trailing steps that are too
long, so it fits as well. The statistics count every step drop by role and reason (`dropped_by_role`) and, by role, the
steps the limits decided on (`candidates_by_role`); the summary prints `dropped for length, by role: worker=3/120
(2.5%)`. `--max-drop-rate F` fails the export, removing the `-o` file, when the limits dropped more than the share F of
any role's steps (default 1: never); the statistics file is still written.

## 8. Governance and data quality

* **Raw logs are private.** The event log and blobs hold everything the agent saw, including any secret a tool
  printed (`cat .env`). They are written 0600 in 0700 directories and are never the shareable artefact: redaction
  happens at export, not at write time, because redacting a blob would change its content hash and break exact-prompt
  replay. Share exports, not run directories.
* **Redaction** (deterministic, so identical text redacts identically and prefix sharing survives): secret patterns
  (cloud keys, tokens, private keys, JWTs, high-entropy strings near `key|token|secret|password`), emails, IPs,
  home-directory paths. Runs at export time over prompts, completions and observations.
* **Provider terms.** Episodes record which model produced each completion and whether it is a teacher. Exports
  refuse teacher outputs unless the model is listed in `--teacher`. Do not train competing models on outputs whose
  terms forbid it. Drop rules and role teachers (section 7.2) narrow this further by role, task, served provider or
  the kind of work, for trainable steps too.
* **Licences and consent.** The repo licence and any consent flag travel with every record; `--licenses` filters.
* **Contamination.** Task splits by repo/date; hidden verifier files never appear in prompts; a scanner flags
  episodes whose transcript contains verifier content or benchmark strings; `--confine bwrap` keeps commands from
  reading hidden files, reference solutions and mined history at all.
* **Dedup and balance.** Exact dedup by prompt hash; per-task caps; `sleipnir rl show RUN [TASK/SAMPLE]` prints pass rates, reward
  distributions, per-role step counts, flagged episodes, token totals, cache anomalies and group statistics.

## 9. What each role learns

| Role | Behaviours the data teaches | Signals |
|---|---|---|
| worker | reads the pinned map instead of exploring; edits within scope; runs the verifier before `done`; uses `recall` instead of re-reading | outcome, evidence, cost, protocol |
| manager | dispatches from the board without briefings; right-sized swarms; sleeps in `wait`; integrates and verifies | outcome, parallel efficiency, duplicate work, conflicts |
| compactor | dense, faithful one-line resumes; correct promotions; masks bulky results; minimal rewrites | validity, size, probe fidelity, rebase cost, downstream outcome |
| mailman / mail | few, typed, useful messages | recipient outcome, hot-tail tokens |
| reviewer | finds real defects; does not rubber-stamp | agreement with verifier, false-approve penalty |

## 9.1 Serving the trained model

Use one weight set with role tags (per-role LoRA ids enter vLLM's block hash and would split the shared-prefix
cache). Size KV memory for one shared prefix plus N private tails; keep the same `--cache-key` routing the harness uses
(`prompt_cache_key` / `X-Session-Id`). `sleipnir doctor` measures the served endpoint's cache behaviour like any other.

## 10. Command reference

```
sleipnir rl taskgen git|chain|mutate|composite|recall|rebuild  build tasks.jsonl (+ blobs/) from repositories
sleipnir rl tasks validate|stats|filter|split|check   task file hygiene and soundness proofs
sleipnir rl rollout --tasks T --model M --out RUN     run G samples per task through the real harness
sleipnir rl eval --tasks holdout.jsonl --model M      pass@1 / pass^k / cost / protocol metrics, optional --baseline report.json
sleipnir rl serve --addr 127.0.0.1:8090 --runs DIR    rollout server for trainers
sleipnir rl reward RUN --rewards rewards.json         (re)score a run with different weights or target prices
sleipnir rl export RUN --format steps|tokens|groups|sft|dpo|kto|atif|canonical ...
sleipnir rl verify RUN                                replay check: re-expand prompts, compare wire hashes
sleipnir rl show RUN [TASK/SAMPLE]                    run summary; one episode's agents, steps, rewards and flags
sleipnir rl expand EXPORT [--table T]                 expand a deduplicated canonical export to inline prompts
sleipnir inspect SESSION_DIR                          the cache inspector (any recorded session, also each rollout's)
```

Data from ordinary interactive sessions can be exported the same way (`sleipnir rl export SESSION_DIR`): outcome
events, verifier runs the user triggered, accepted/rejected diffs and steering messages become weaker reward
signals, and exporters mark such episodes `weak_label` so they are opt-in for RL.
