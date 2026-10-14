# Changelog

[Releases](https://github.com/Anemos-labs/Sleipnir/releases) are the versioned
release history, generated from merged pull requests and tied to their source
commits. The notes below are cumulative compatibility and prompt-cache changes;
they do not indicate whether a change has been released.

## Compatibility changes

- The variables a command line sets stay known when quoted text in it holds
  `()`, `function ` or `((`: `printf 'func main() {...}' > main.go` defines no
  function, so a later `rm -rf "$d"` of a scratch directory the line named is
  judged as that directory. Before, any `()` in the line made every variable
  unknown, and bypass mode asked about the delete as high risk. A real
  function definition, and arithmetic inside double quotes (`"$((n=1))"`),
  still stop the tracking. Prompt bytes: none change.

- The goal judge's evidence search lists the newest records first, an
  evidence request it wraps in a code fence or in words is read, and an answer
  that is neither a request nor a verdict is asked for again (twice) and then
  pauses the goal instead of sending the agent on with no reason. Prompt bytes:
  the judge's system prompt says results come newest first and that `left`
  names work, never evidence for the judge to be shown; the judge's answer
  limit is 4000 tokens (was 1200).

- `bypass` mode asks about a file whose name is only known when the command
  runs, which may be one that a built-in protection, a `deny` rule or an `ask`
  rule guards (`cat "$F"`, `cat "docs/$X"`, `echo x > "$OUT"`); before, it let
  any such name pass. The words of an unlisted program, an assignment's value,
  `cd` and name-only uses still pass. A relative name's known directory is the one it names
  (`secrets/$X` is inside `secrets/`), and a command substitution that closed
  earlier in the line (`cd "$(pwd)"; F=...`) no longer makes the variables
  set after it unknown. Prompt bytes: none change.

- A pool's reconciled result is superseded only by a change that may touch the
  pool's files (an accepted or revised task whose scope overlaps the pool's, a
  direct manager edit of one of its files, or a change whose files are not
  known); a merge elsewhere in the tree no longer reopens every pool's
  confirmations. Prompt bytes: the refusal of a stale confirm says "in the
  pool's files".

- curl's URLs, headers and data are not judged as files: a smoke test's request
  body holding a variable no longer reads as a write to an unknown place. The
  files curl reads and writes are judged: `-o` and the name `-O` takes from
  the URL (below `--output-dir`), `-c`, `-D`, `-d @file`, `-H @file`, `-F
  name=@file` and its `headers=@file`, `-T`, certificates, `-n`, and the file
  a `file:` URL names. A config file (`-K`), a name the server picks (`-J`), a
  URL whose scheme is only known when the command runs, and an option curl is
  not known by here keep the stricter analysis. A `printf` without `-v` no
  longer makes the check forget the variables the line set. Prompt bytes: none
  change.

- A pool child that goes to review says who takes it out: its pool's leader
  (or the manager) accepts it with `task accept`, and the pool reconciles once
  every child is accepted. A leader submitting its own child is told to accept
  it itself; a member's submission is mailed to the leader; and `pool
  reconcile` refused for a child in review says so. When the last member
  confirms the reconciled result, the leader is told to submit it with `task
  done`, and a second reconcile of an unchanged, fully confirmed result is
  refused with that advice. A member's `confirm` refused because a merge
  superseded the reconciliation mails the leader to reconcile again and tells
  the member to wait for the new revision. While members agree on the
  leader's proposal, a proposal from the manager is refused (it would reset
  their votes) and points to the leader. Prompt bytes: none change (tool
  results and mail).

- The file tools take a path that starts with `$TMPDIR` or `${TMPDIR}` to the
  session's scratch directory, as the shell does; before, it named a directory
  "$TMPDIR" under the project. Prompt bytes: the runtime context's scratch
  sentence (G1) says so.

- The manager can excuse a pool member that cannot take part (`pool excuse`,
  `member` and a reason): it leaves the pool, the other members' agreement and
  confirmation suffice, its open subtasks of the parent return to the board
  unassigned, the next member leads if it led, and it stays read-only while the
  pool task is open. The manager can thus keep a pool from waiting for ever on a
  member whose model fails. An `agree` or `confirm` from outside the pool (the manager's, say) is
  told that only members acknowledge. Prompt bytes: the pool tool's description and schema
  change G0, so a cached tool prefix of an earlier version no longer matches.

- A `task revise` whose only change is a wider scope of a task under way (the
  globs it had, and more) widens the scope in place, as `task update` with
  files does: nobody is stopped, children keep their owners and a pool stays
  executing. A pool parent's scope can grow while its children run (their
  scopes no longer count as conflicts with it). Prompt bytes: none change.

- Read-only roles (reviewer, scout, architect) run the project's tests the way
  the writers do: `python -m pytest`, `python -m unittest`, `node --test`, and
  the tests and checks run with a virtual environment of the workspace (the new rule
  `Bash(python:venv-tests)`, the run part of `Bash(python:venv)`: no creating,
  no installing). Their refusals say what the role does instead of telling it
  to present a plan. Prompt bytes: none change.

- A test runner given paths reads them: `pytest .`, `python -m pytest tests`,
  `python -m unittest discover -s tests` and `node --test .` are not writes
  into every directory below their operands, so the protected configuration
  directory's ask rule no longer stops them in bypass mode. Only options that
  name no file to write are recognised; any other keeps the stricter
  analysis. Prompt bytes: none change.

- `pool execute` says what comes next, in its result and in the mail that wakes
  the members: writing opens inside child tasks only; the leader creates one
  child per share of the agreed work, claims its own and assigns each other
  member its child. Pools that had agreed no longer stall with every member
  refused and waiting. Prompt bytes: none change (tool results and mail).

- Providers take `options.model_extra_body`: per model id, request members put
  over `extra_body` for that model only (one gateway, an upstream pinned for
  one of its models, the others routed as before). Prompt bytes: none change.

- The options of an `openai-responses` provider are checked like the other
  dialects': an unknown name warns, a wrong value (`reasoning_summary`, a
  `model_extra_body` entry that is not an object) is an error. Such a provider
  with `capture_tokens` or `--capture` is refused at startup, since the
  Responses API returns no token ids. Prompt bytes: none change.

- The tests preset (`--allow tests`, the "builds and tests" answer, `accept-edits`) covers a
  Python virtual environment of the workspace (`Bash(python:venv)`): creating
  one, installing into it with its own pip from the package index, and running
  the tests with it, when its python is a link to an installed interpreter
  (not a wrapper, a copy or a program of the workspace); installs that change the index or the target, URLs, files
  outside the workspace and every other installer still ask. These installs are
  routine, and the manager is told they reach the package index. A program run
  by path is judged by its name (`.venv/bin/pip install --index-url ...` is an
  install). The preset also takes `py_compile`, `compileall`, `ruff check`,
  `mypy`, `pyright`, `tsc --noEmit` and `eslint` on files of the workspace
  (`Bash(prefix:workspace)`). `export`, `readonly` and `declare -x` keep a
  variable known; `$$`, `$RANDOM` and the other numeric parameters are part of
  a name (`"$TMPDIR/x-$$.json"`); traps are judged with what is true when they
  run and `sh -c` with the exported variables only; assigning `PWD`, `RANDOM`
  and the like makes them unknown. Reasons say what a command does and what
  would allow it; a name in `$TMPDIR` known only at run time is named as the
  cause instead of the state directory's rule. "Don't ask again" is offered
  only when the rule it adds lets the same request through, and never for a
  command with a variable or a program read from standard input. The chat's
  dialog and the line prompter list the web page's answers and numbers (1 yes,
  2 don't ask again, 3 builds and tests, no last); the line prompter takes text
  as a no with what to do instead. Prompt bytes: the manager's role pin says
  that project-environment installs may come to it, a one-time cache miss of
  that pin; no other stable layer changes.

- The built-in price table includes OpenAI's `gpt-6-astra`, `gpt-6.1-sol`, `gpt-6-sol`,
  `gpt-6-luna`, `gpt-5.6-sol`, `gpt-5.6-terra` and `gpt-5.6-luna` at the API's
  standard-tier, short-context list prices (input, cached input, cache writes at
  1.25 times input, output); `openai/<model>` is priced at them
  instead of the $3/$15 fallback. A model the table prices keeps the id its
  reference names in requests (`openrouter/openai/gpt-6-luna` sends
  `openai/gpt-6-luna`). A ChatGPT plan's usage is shown at the same list
  prices, marked `≈$0.12 plan` in the terminal and with a `plan` tag in the
  browser; `cost_usd` stays 0 and every budget counts only what is billed.
  Events: a plan's `model.response` adds `list_usd`, `session.start` model
  records add `plan`, `session.end` adds `list_usd` and `plan`, and the page's
  `use` and `svc` events add `list` and `plan`. `sleipnir models` lists a
  plan's models at the list price tagged `plan`, and `--max-price` filters
  them by it. The reasoning-effort range of `gpt-6.1-sol` applies. Prompt
  bytes: unchanged.

- Agents are told much more about what is expected of them. The constitution
  grows from about 1,000 to about 3,700 tokens (4,100 in a team): the user's
  words as the specification (names, commands and formats kept exactly,
  requirements checked before finishing), verification that actually runs
  (a missing test runner set up in a project virtual environment, never
  system-wide; no weakened tests; no claims of tests that did not run),
  debugging, conventions for command-line programs, data files, services and
  interfaces, git hygiene, when to ask, and worked examples. The manager and
  every worker role pin say how their role meets those expectations, and the
  team card no longer tells the manager it edits no file. The shared context
  gains a toolchain segment listing which common build and test commands are
  on PATH. Prompt bytes: G0 (constitution), G1 (new frozen `toolchains`
  segment) and every role pin change; each incurs a one-time cache miss. A
  team's G0 grows by about 2,900 tokens and each role pin by 100 to 500,
  sent on every request. Where the provider caches prompts and the prefix is
  read from its cache, that is about 300 input-token equivalents per request
  at a 10% cached-read price (an estimate; the provider's reported cache reads
  are the measure); without prompt caching every request pays the full input
  price for the extra tokens. The simulator's default workload already assumes
  9,000 constant tokens (tools and constitution), about what a team now sends.

- Web: the view follows the session as it happens. Nothing holds it while the
  pointer or the keyboard focus rests on the conversation, and a tab that comes
  back from the background draws every row it missed, with no summary row in
  their place. The hold chip, its catch-up, the Hover setting and the palette's
  "Pin the hold" are removed; Space pauses the view (a replay) wherever the
  pointer is, and a replay keeps its speeds. At phone width the Manager card no
  longer breaks its words, the stalls no longer overlap, the team's card grows
  with its stalls, the Workspace's verify strip no longer covers the file,
  Settings tables fit the screen, recorded sessions show as cards, diagrams keep
  a readable size and scroll sideways, and small controls take a 32 px touch.

- Standing-goal judgments can search durable tool evidence across workers,
  turns and compaction instead of relying on clipped recent results. Manager
  `task deliver` commits verified integration directly to the original checkout,
  respecting permission rules and dirty-checkout guards. GitHub delivery requires
  explicit release verification tied to the merge commit and rechecks changed
  workflow evidence. Useful ready compactions commit before staleness eviction.
  Prompt bytes: the task schema and description change G0; the manager isolation
  card changes private notes. These layers incur a one-time cache miss.

- Planning-pool children inherit the parent's task kind unless explicitly set.
  Managers can revise a task's kind in place to repair an incorrect verifier
  contract. Reconciliation retains the combined contract supplied for every
  member to confirm. Prompt bytes: the task and pool tool descriptions change G0, incurring a
  one-time cache miss.
- Configured teams retain role pools and worker conversations. Each multi-member
  pool agrees on one plan, delegates scoped children and reconciles one parent
  result. Task revisions and retries preserve IDs; unneeded work closes with an
  explicit disposition. Managers can edit and publish under session permissions.
  The legacy executable `fullstack` role is removed. Default teams allocate the
  exact requested worker count. Prompt bytes: G0 gains the `pool` tool and task
  revision/parent fields; manager and architect role pins and the manager's
  isolation card change. These stable layers incur a one-time cache miss.
- GitHub delivery is opt-in per objective through `--auto-fix`, `--auto-merge`,
  `/delivery` and chat checkboxes. It persists progress for explicit resume and
  checks the current PR head before merging. Cache charts show a readable window
  with absolute request numbers and cumulative totals.

- Teams: a worker's routine permission question goes to the manager first
  (`permissions.manager_approves`, default true; `--manager-approves`),
  which answers with the new `approve` tool; unanswered in 60 s or
  escalated, it goes to the person. `perm.decide` gains `by: "manager"`;
  the page receives such a decision as an ask and its answer (`by:
  "manager"`) in the same instant. In a team with a reviewer slot, a merged
  change is due a review: the manager's hot view says so, its wait is
  refused when nothing else runs, and a batch run's final answer is held
  until a reviewer's task is accepted after the last merge. The manager's
  hot view names free slots while ready tasks have no owner. Auto-docs
  starts its first pass after the first accepted implementation task. The
  isolation card says not to fetch, merge, rebase or commit and names the
  session's integration branch. `sleipnir web` defaults a team in a
  repository root with a commit (or a folder it makes a repository) to
  worktree isolation, and a request that arrives while a session starts
  waits for it. Prompt bytes: the tool list (G0) gains `approve`, the
  manager's role pin and team card changed, and workers' isolation cards
  changed, so G0 and the manager's role layer miss the cache once.
- Permissions: a whole-tree formatter (`gofmt -w .`, `go fmt ./...`,
  `prettier --write .`, `cargo fmt`, `ruff format`, ...) is no longer taken
  to write into `.sleipnir/` or `.claude/`; an `ask` or `deny` rule about a
  directory below its operand does not cover it. git subcommands that write
  only into the repository (`fetch`, `add`, `commit`, `tag`, ...) read their
  operands, so their question names the command and can be remembered. An
  environment assignment of a plain value (`PORT=8080 ./bin/app`) no longer
  asks by itself; a denylist of variables that steer what runs (`PATH`,
  `LD_*`, `GIT_*`, `CC`, `MAKEFLAGS`, `*_PROXY`, `*_OPTS`, ...) still does.
  The tests preset adds `go fmt`, `node --check`, `npm run format`,
  `make fmt|vet`, and `curl`/`wget` to this machine only
  (`Bash(curl:loopback)`, a new rule form). A writer in worktree isolation
  may write the session's `$TMPDIR`. Prompt bytes: the runtime layer of a
  session with a private `TMPDIR` gains one sentence about it, so that layer
  misses the cache once.
- The built-in worker roles are architect (read-only planner), scout,
  backend, frontend, devops, tester, docs and reviewer. `fullstack` is no
  longer offered; training tasks and resumed sessions that name it keep it.
  A chat, run or web session gets the default team, one worker slot of each
  role, and the manager may start a worker only while its role has a free
  slot. `--role-model` given more than once for a role, or with a bare role,
  names the team instead (at least 8 slots). `--swarm` is 0 (a single agent)
  or at least 8. Prompt bytes: the manager's role pin (its rules and a team
  card), every worker's role pin under auto-docs, and the `spawn` tool's
  description changed, so G0 (tools) and the role layers miss the cache once.
- Auto-docs (`--auto-docs`, `swarm.auto_docs`, on by default): after every
  accepted task and agreement the harness gives the docs worker a
  `docs: catch up` task; a batch run's manager is held until it is done.
- The manager's shell commands are judged by the permission engine's
  plan-mode profile alone (pipes, `&&`, absolute paths inside the workspace,
  `git -C`), and it may run `gh`'s reading commands and fetch and search the
  web. Read-only roles may fetch and search the web too. Writes stay refused.
- A task the manager sends back between a worker's `done` call and its next
  request is read by the run that is ending. When that worker stopped without
  calling `done` again, the task stayed in doing with an idle owner: the run
  does not settle a newer assignment, and no unread mail was left to start
  the next run. The harness now writes the worker one message, so the next
  run takes the task at its current revision and settles it. The message is
  mail in the conversation; stable prompt layers and tool schemas are
  unchanged.
- The network detector (`hack:network`) flags a package manager's fetch of
  the task's own package (`pip download`/`pip install`, `npm install`,
  `yarn add`, `pnpm add`, `cargo add`/`install`, `gem install`), which names
  no address: a rebuild team ran `pip download ncclient==0.6.13` to get the
  sources it was asked to write. The names are the upstream repository's
  and, for a Python task, the scope's top-level modules.
- The prompts of `rl taskgen chain` and `rl taskgen rebuild` tasks say that
  fetching the project's code from elsewhere (its repository, a mirror, a
  module proxy or a package index) is not allowed. Teams on rebuild tasks
  tried `go mod download` and `curl` of the upstream sources, which the
  network detector flags, losing whole rollouts. Tasks generated before keep
  their prompt.
- The network detector (`hack:network`) knows the upstream of harvested
  tasks from `meta.source` (`repo` or `url`, when it is a URL), so a chain
  or rebuild team asking for the project's own code is flagged even when the
  call fails, as for tasks with a `repo.url`. `go list` counts as network
  use only when it asks a module proxy (`-versions`, `-u`, `-retracted`, a
  `module@version`): a plain `go list github.com/owner/repo/pkg` lists a
  package of the build and was flagged as reaching github.com. A Go command
  refused by `GOPROXY=off` counts as failed.
- `rl taskgen chain` and `rl taskgen rebuild` give a task 250 requests for
  each worker and hour of its wall clock (at least 2.5 per step, as before).
  The request budget counts every agent of the session, and 2.5 per step
  ran out after 3 to 4 hours of a 9 to 12 hour task, so a team was stopped
  with most of its wall clock left. Tasks generated before keep the budget
  they recorded; raise `budget.requests` in their file to give them the new
  one.
- `rl export --format dpo` pairs the siblings of a branch point. For a
  branch episode (`fork` set) the episode unit compares its root agent from
  the fork to the first rebase after it: it stops collecting at that rebase,
  where before it refused any episode whose root agent had more than one
  segment. A branch run's DPO export therefore gains episode pairs, with or
  without the new flag. Other episodes whose root
  agent has more than one segment are still refused (`pair:multi_segment`).
  `--pair-unverified` (`export.Options.PairUnverified`) lets `--pair
  best-worst` pair a group none of whose episodes passed, by verifier score;
  without it such a group still gives no pair.
- The hardcoded-value detector rules out a literal that the task's
  repository already had before the change (`env.RepoContains`: `git grep`
  at the task's commit). Once the detector read hidden files from the task
  store, it flagged correct solutions of specification-driven tasks, whose
  routes, commands and operation names are also in the hidden tests, and
  zeroed their outcome. `harness.Pipeline` gains `RepoContains`, and
  `reward.WithRepo` adds a search to a `DiffSource` (a literal whose search
  fails is noted and not taken as evidence); `rl reward` searches the
  tasks' repositories relative to the current directory and says when it
  cannot. Nor is a name of several parts (`core.count`) each of whose parts
  the repository has evidence. A flag's note says whether the repository was
  searched.
- `rl taskgen rebuild` keeps a Go package of the scope at the reference when
  the tests of another package of the scope import it although that
  package's sources do not depend on it (an assertion library such as
  gookit/goutil's `x/assert`). Stubbed, it made every test of every unit
  panic, so a rollout scored 0 throughout whatever it rebuilt. The report
  lists such packages under `test_support`, and `rl taskgen rebuild` prints
  them. Go stubs also keep the bodies of what runs before the first test:
  the functions and methods of the scope that package-level variables, the
  init functions of packages outside the scope and the hidden tests'
  `TestMain` reach while the packages load (goutil's `strutil` initialised a
  variable with a stubbed constructor, so every package that loads it
  panicked). Python stubs keep what runs when the modules and the hidden
  tests are imported, the same way (oauthlib's `filter_params` decorator,
  stubbed, failed the import of 47 modules). Tasks generated earlier are
  unchanged; regenerate a rebuild task whose scope includes such code.
- New `rl reverify` verifies finished rollouts again with the tasks as a
  tasks file defines them now: the final patch (the episode's verdict) and
  every consistent savepoint's tree in progress mode (`progress.jsonl` and
  the episode's progress). The replaced values are kept in the sample's
  `reverify.jsonl`. New `rl tasks update-scorer` writes tasks with the
  running build's whole-suite scorer script, so tasks generated earlier are
  scored by a fixed scorer without being generated again. Run walks no longer
  descend into a sample's `savepoints/` directory. Tasks, recorded episodes
  and prompt bytes are unchanged until these commands are run.
- The hardcoded-value detector reads a task's hidden verifier files that are
  stored as blobs from the task blob store. Scoring read blobs from the run's
  own store only, so for tasks whose hidden files are blobs (every generated
  chain, rebuild and fixture task) the detector skipped them with an
  "unreadable" note. Rollouts and `rl serve` pass the task blob store to the
  scorer; `rl reward --tasks FILE` reads `blobs/` next to FILE or `--blobs`.
  `harness.Pipeline` gains `Hidden`, and `reward.DiffsFromBlobs` takes further
  getters to fall back to. Episodes scored earlier keep their flags until
  `rl reward --tasks FILE --redetect-hacks` scores them again.
- Under `--confine bwrap`, a cache that a command's environment names
  (`GOMODCACHE`, `GOCACHE`, `PIP_CACHE_DIR`, ...) or that `--confine-rw`
  names is created when it does not exist yet. Before, a missing cache was
  not bound into the sandbox: a setup command's module download landed in a
  tmpfs that vanished with the command, and the network-less verifier then
  failed to build every package with an outside dependency. A cache that
  cannot be created is a warning on the command's result. Tasks, scores of
  runs whose caches existed and prompt bytes are unchanged.
- The whole-suite scorer runs a Go package again when a test crashed its test
  binary (a panic, a fatal error), without the tests it already reported
  (`-skip` through `SUITE_ARGS`), until a run reports nothing new, the
  package stops crashing or 40 reruns were made: when go test names the
  test that crashed, the tests after it are reported instead of counting as
  failed; a crash that names no test (a panic in another goroutine) still
  leaves them unreported. Scores of Go suites whose code
  crashes rise accordingly; recordings made by generators include those
  tests. Prompt and tool-schema bytes are unchanged.
- When no run with the reference solution reports a single test, the
  "nothing to score" rejection of `rl taskgen rebuild` and `rl taskgen chain`
  says so, with how that step's first run ended (`pytest: exit 4`) and the
  last 1,500 bytes of the output the scorer relayed: for example pytest
  stopping at startup because its `addopts` name a plugin the environment
  lacks. `taskgen.SuiteRecord` gains `start_no_tests` and
  `reference_no_tests`, `taskgen.SuiteSteps` gains `no_tests`, and
  `env.VerifyOptions` gains a `Stderr` callback. Tasks, the scorer script and
  prompt bytes are unchanged.
- Resume long RL rollouts from savepoints instead of starting them over
  (`--savepoint-every`, off by default): a rollout takes a session savepoint
  at that interval and captures the work tree into the sample's
  `savepoints/`. An attempt that finds one resumes from it: the log is cut
  back (its tail kept as `events.rewound.N.jsonl`), the workspace is rebuilt
  at its stable path, the session continues with a note to the agent, and the
  remaining wall, request and step budgets carry over. Such an attempt does
  not use up an infra retry. `ledger.jsonl` entries count only what an
  attempt appended to the log, plus `rewound` entries for spend that no entry
  had counted; episodes label the steps after a resume as a `resume` segment;
  the progress stream gains `rollout.resume`, `manifest.json`
  `savepoint_every_ms`. With savepoints off, rollouts and their records are
  unchanged. Prompt and tool-schema bytes are unchanged; a resumed agent's
  first request re-writes its cached prefix once.
- Score the work tree of long rollouts at their savepoints
  (`--score-savepoints`, with `--progress-sample` and `--progress-budget`):
  the task's verifier runs on each savepoint's tree in a clean checkout, in
  progress mode (`SUITE_SAMPLE`, `SUITE_BUDGET_S` in its environment), and the
  scores go to the sample's `progress.jsonl` and the episode's new `progress`.
  New reward components `progress` and `regressions` (cap `regressions`) read
  them; their default weight is 0, so rewards are unchanged unless a rewards
  file weights them. `VerifyOptions.Env` adds entries to a verifier's
  environment. `rl export --sft-prefix best` exports failed episodes up to
  their best-scoring savepoint (`best_prefixes` in the statistics). Prompt and
  tool-schema bytes are unchanged.
- Continue finished long rollouts from their savepoints with `sleipnir rl
  branch`: K branches per savepoint, each resuming the trunk's state with its
  own seed, for a horizon of agent time. Episodes gain `fork` and steps
  `inherited`: a branch's steps before its fork are not trainable and are
  dropped from the training formats (`step:inherited`). The siblings of a
  point share the group `<task>@<parent>:sp<NNNN>`. The log gains the
  `rl.fork` event. A capture store keeps alternates it already had (a
  branch borrows its trunk's objects). With `--compact-first` each branch
  compacts first when its context has enough to fold, and the siblings' first
  calls are then competing summaries of one state (`fork.compact_first`);
  with too little to fold, no compactor is called. Prompt and tool-schema
  bytes are unchanged.
- Commands in a task's scrubbed environment (agent and verifier) get
  `RUSTUP_HOME` set to the host's `~/.rustup` when the host has one and does
  not set the variable: with HOME replaced, rustup's cargo and rustc found no
  toolchain and every cargo command failed.
- Task generators can score a task with the project's whole test suite
  (`taskgen.WriteSuite`, `RecordSuite`): a hidden scorer runs Go, pytest and
  cargo suites, each command in its own session with its output in files,
  and prints a json-score line with units (milestones weighted as the
  generator chose) and guards; tests are allowlisted from repeated runs of
  the reference and the start. Progress mode (`SUITE_SAMPLE`,
  `SUITE_BUDGET_S`) runs a sample under a deadline. `VerifyOptions.Stdout`
  hands each verifier run's stdout to the caller; `env.MaxScoreUnits` and
  `MaxScoreUnitName` are exported. Existing verifiers are unchanged.
- `rl taskgen fixture` takes `--max-files`, `--max-file-bytes` and
  `--max-total-bytes` (`FixtureOptions.MaxFiles`, `MaxFileBytes`,
  `MaxTotalBytes`) for large project fixtures; the defaults (400 files,
  1 MiB per file, 8 MiB per tree) are unchanged.
- Link every compaction to the compactor call it came from, and baseline
  compactor advantages by compaction number. `compact.patch` and
  `compact.commit` events gain `req`, the compactor's `model.request` id (on a
  commit, `""` when no compactor was asked: a mask or emergency compaction);
  `compact.commit` already carried `fallback`. RL episodes link a commit's
  `compact` edge to exactly that call, when it is an earlier kept compactor
  call of the same agent that no earlier commit took, and gain an optional
  `fallback` field on the edge, set when a mechanical patch replaced the
  call's; a commit with an empty `req`, or whose request got no response, has
  no edge. Logs without
  `req` still extract with the previous heuristic, which now also gives no
  edge to a commit whose reason marks a mask or emergency compaction. `adv`
  baselines (per role, the default) a compactor call scored on its own by
  (group, role, compaction number), the k-th compactor call of its agent in step order, so its
  advantage changes; a number with a single trainable call in the group is
  flat (advantage 0, reported in `Flat`), as any single-sample group is. Report
  groups gain `Compaction` and the key `<group>|compactor#<k>`. Prompt and
  tool-schema bytes are unchanged.
- Sessions can take savepoints (`session.Options.Savepoints`,
  `Session.Savepoint`): tool batches that can write hold a barrier, a
  savepoint pauses them for a moment, writes every agent's state as an
  `agent.snapshot` marked `savepoint`, emits the new `session.savepoint`
  event and flushes the log; a log cut back to it resumes into that state.
  RL workspaces can be prepared at a stable path, captured into a store and
  rebuilt there (`PrepareID`, `Capture`, `RestoreWorkspace`). Without the
  option nothing changes; prompt and tool-schema bytes are unchanged
  (`core.ToolSpec` gains the scheduling hint `NoTreeWrites`, set on the
  swarm's task, mail, note and spawn tools, which is never rendered).
- `sleipnir rl taskgen chain --chains FILE REPO` makes one long swarm task of
  each chain of 3 to 8 consecutive merged pull requests, scored per milestone
  by the whole-suite scorer and checked at every pull request before it is
  written (`docs/TRAINING-DATA.md`, section 3.2); `--seal DIR` seals each
  start as for `rl taskgen git`. The generation report's
  rejections gain the field `chain` and the report the list `chains` (each
  chain's self-check); a task's `meta` gains `commits`, the chain's pull
  requests. Readers that ignore unknown fields are unaffected.
- `sleipnir rl taskgen git --seal DIR` writes each task's start state as a
  repository of its own, `DIR/<task id>.git`, with one commit (the start tree,
  no history, a fixed author and date): the repository holds that commit and
  the trees and blobs its tree reaches, and no object outside them, and the
  task's `repo` names it: `repo.commit` is the sealed commit and `repo.path`
  the sealed repository (`<--repo-path>/<task id>.git` when `--repo-path` is
  given). The source commits stay in `meta.commit` and `meta.parent`. Without
  `--seal` tasks are written as before. Sealed tasks need their repositories
  next to the tasks file, and `rl tasks split` treats each sealed repository
  as a repository of its own (`docs/TRAINING-DATA.md`, section 3.2).
- `sleipnir rl taskgen rebuild --scope PKG,... REPO` makes one long swarm task
  of rebuilding a library from its specification: the scope's Go packages or
  Python modules at `--commit` are reduced to stubs (`--tier stubs`) or removed
  and documented in `API.md` (`--tier spec`), their own tests become the hidden
  suite of the whole-suite scorer with one milestone per package or module,
  weighted by its lines, and the start state is written to `--seal DIR` as a
  repository with one commit (`docs/TRAINING-DATA.md`, section 3.2). A scope
  outside the size gates, importable from outside the start tree, or whose
  task fails its self-check yields no task and the command fails with the
  reason. The task's `meta.source` holds `repo`, `commit`, `scope`, `tier` and
  `spdx`. `taskgen.GoSuiteCommand` takes the packages to run; without them it
  runs the whole module, as before.
- A `json-score` verifier's score line may carry a breakdown of the score,
  `"units"` and `"guards"` (`docs/TRAINING-DATA.md`, section 3.1). Verifier
  results, outcome events, the episode's `outcome.verifier` and the results in
  a run's `summary.json` gain the optional fields `units` and `guards`; readers
  that ignore unknown fields are unaffected, and the score and verdict are the
  same as before.
- Confine RL setup, agent and verifier commands with `--confine bwrap` (on
  `rl rollout`, `rl eval`, `rl tasks check` and the task generators): each
  runs in a bubblewrap sandbox that sees a read-only system, its own tree,
  HOME, TMPDIR and caches, with the work dir, the blob store, the run output
  and the directory task sources resolve against hidden, and its own PID, IPC,
  UTS and (unless the task needs it) network namespaces. `--confine-ro` and
  `--confine-rw` re-expose other toolchain and cache directories. The default
  (`off`) runs commands as before. `VerifyRequest` gains `Wrap` and `BaseEnv`;
  prompt and tool-schema bytes are unchanged.
- A worker started after a team session is resumed (without worktree
  isolation) gets an id past those the earlier process used, as an isolated
  team's already did: before, it could be given the id of a worker from
  before the resume, and two agents then shared request ids in one log, which
  made the session's training data fail to replay. Its prompts name the new
  id.
- RL tasks can give paths to roles: `team.owns` maps a role to globs (the
  syntax of `verifier.protected`) that only agents of that role may write in a
  swarm. The write guard refuses another agent's write to an owned path, the
  manager's included, with a tool error that names the owning role and says to
  hand the work to it; it counts as a scope error (`error_kind` `scope`, so in
  the `scope_violations` signal), raises an alert and is logged as a `lease`
  event of the new action `owner`. In an isolated run the merge queue also
  refuses a submission that changed such a path. `taskgen composite` gives a
  composite whose solution changes a user-interface file
  `"owns": {"frontend": [...]}`. The refusal, its alert and the merge queue's
  bounce are new prompt content only for a task that sets `team.owns`; without
  it no prompt byte changes. Task files without the field are read as before,
  and readers of the log that ignore unknown lease actions are unaffected.
- Pause RL rollouts during outages of the policy endpoint instead of failing
  them. A rollout's agents wait out a 5xx, a 429, a refused or reset
  connection and a request that gets no response, for up to
  `--outage-patience` (default 3h) per outage, and the rollout's wall clock
  (`budget.wall_s`, `--max-wall`) stops while the endpoint is down; an attempt
  whose outages add up to `--max-outage-pause` (default 3h) ends as an infra
  failure that is not retried. A negative value restores the old behaviour of
  either: no waiting (rollouts failed after the six attempts every request
  gets), or a clock that runs through outages. Episodes, summary results and
  `ledger.jsonl` gain `active_wall_ms` and `paused_ms`, `summary.json` a total
  `paused_ms`, `manifest.json` both settings; the rollout's log records each
  outage as `rl.outage` events and the progress stream announces one that
  lasts half a minute as `rollout.outage`. An agent's outage patience now
  counts the time since a request first failed rather than the sum of the
  waits between its attempts. Prompt and tool-schema bytes are unchanged.
- Keep a provider key from the environment in use when a key is also stored.
  A key that `harden.MoveKeys` took out of the environment at start is no
  longer replaced by the one `sleipnir login` stored, as documented: the
  stored key is used only where the environment has none, and `harden.SourceOf`
  tells which of the two supplies a key. `sleipnir login` and `logout` name the
  environment variable only when it is the key in use, and a refusal of the
  environment's key no longer deletes the stored key that was never tried.
- Web permission rules judge exactly what `web_fetch` fetches, and a redirect
  to another host asks first. A rule read only the first of the fields `url`,
  `uri`, `href` and `endpoint` that parsed, while the tool reads `url` after
  trimming it, so a request could match a rule for one host and fetch another;
  rules now judge only the tool's own `url`, normalised as the tool does, and a
  URL the tool would refuse matches no allow rule and every domain or URL deny
  or ask rule. A URL-pattern rule matches the URL as given, as the tool fetches
  it, without the `/` of an empty path, and without its scheme, so every pattern
  that matched a fetched URL still does and `docs.example/page` also matches
  `https://docs.example/*`; a pattern without a scheme matches every scheme. An
  internationalised host is compared and fetched in its ASCII (punycode) form,
  in rules and URLs alike, and a host that has none is refused. `web_fetch`
  asks before it follows a redirect that leaves the site (another host, another
  port, or from https down to http) and refuses it in a run with nobody to ask;
  a redirect that stays on the site (adding `www.`, a trailing dot, an upgrade
  to https, another path) is not asked about but is refused by a deny rule that
  covers it. Pages reached through a redirect that left the site are not cached.
  A shell command shares one answer to an open question with the same command
  in the same directory; any other request does only when it is byte-identical.
  The file and memory tools' requests now carry the call's input, which
  PermissionRequest hooks also receive.
- `sleipnir web` serves the sessions in a browser on loopback (see `docs/CLI.md`
  and `docs/SECURITY.md` section 5). Behind it, several commands share their
  code with the web routes and behave as before: `models`, `fav`, `allow`,
  `trust`, `mcp`, `login`, `sessions prune`, `schedule` and `update`. Three
  behaviors differ: a project or local configuration file can no longer remove
  the user's deny and ask rules or hooks by setting `permissions`,
  `permissions.roles`, one role's section or `hooks` to `null` (it could, and
  the lists were add-only only below those keys); the approvals of a project's
  tool servers are read again from the ledger before every check and change, so
  an approval made in another process takes effect without a restart; and
  `sleipnir daemon` holds a lock in the state directory, so a second daemon
  refuses to start and a second `--once` run exits 0 saying so. Configuration
  files are written under a lock per file. Session logs gain the event types
  `checkpoint`, `goal.judge` and `verify.run`; readers that ignore unknown types
  are unaffected. A `checkpoint` event carries the checkpoint's file count and
  the file recorded last, never the whole list; one is written when a checkpoint
  begins, each time its count has grown by an eighth since the last one (every
  count up to 16), and at the end of every turn, so where the events fall in the
  log depends only on the changes, never on timing. The terminal feed now shows `swarm.stall`, `swarm.handover`
  and goal lines. A session the web host serves keeps a journal of the content
  each agent writes, bounded at 256 MiB per session, which the browser's
  per-line history reads; other sessions keep none.
- Count workers, not the manager, in every team size a person types or reads.
  `--swarm N`, `sleipnir swarm N`, `/swarm N` and `rl --mode swarm:N` mean a
  manager and N workers again; `--swarm 0` is a single agent and
  `sleipnir swarm` takes 1 or more. The chat on a terminal defaults to a
  manager and eight workers, nine agents in all, and the banner says
  `manager + N workers`. The cockpit's eight legs are the workers: the manager
  rides and takes no leg, and a ninth worker shares the first leg. The
  `swarm.max_agents` setting, which counted the manager, is replaced by
  `swarm.max_workers` (and `SLEIPNIR_SWARM_MAX_WORKERS`), which does not; a
  file or variable that still names `max_agents` is refused with the new key
  and its value, `max_agents` minus one; the value 1 (the manager alone) has no
  equal, since 0 means no ceiling, so its refusal asks for a worker count of 1
  or more. The mailman no longer takes a worker's place under the cap, and
  `sleipnir init` writes `"max_workers": 12`.
- The manager edits no file in any run. It plans, delegates and reviews; its
  writes and writing shell commands are refused at run time with a pointer to
  spawn a worker, in a shared checkout as in an isolated one, and in a session
  in plan mode too: an allow rule the person wrote for the session (say
  `Edit(docs/plan.md)`) opens its exception for the workers, not for the
  manager. It keeps the same tool list as every other agent. The manager role
  pin (G2) changes
  once to say so and can require a fresh provider cache prefill for the
  manager; other stable layers and tool-schema bytes are unchanged.
- Hand a worker's task to a fresh worker. `task handover` (the owner, with a
  required summary, or the manager for any worker) stops the predecessor, gives
  the doing task to a new worker of the same role with a recap the harness
  builds from the task, the predecessor's edits or tree, its attempt history
  and the summary (framed as data), and retires the predecessor once it holds
  nothing else. In an isolated run the predecessor's tree is committed and the
  successor's tree starts at that commit, so the merge queue merges both. One
  board `assign` operation, recording `handed_off(successor)`, is the commit
  point: a failure before it leaves the task with its owner and removes the
  successor's agent and tree; a repeated request reports the same successor.
  `swarm.handover` events record each attempt. The `task` tool's description and
  action enum (G0) change once on upgrade, identically for every agent; the
  recap is part of the successor's private notes, never a shared layer.
- Name team stalls. A sweep on the supervisor's tick finds workers that own a
  doing task but take `swarm.Config.StallTurns` (default 12) model turns without
  a progress call, refused manager waits while no worker runs, tasks owned by
  agents that are not on the team, blocked tasks waiting on each other, and
  submissions left in review for that many manager turns. Each finding is raised
  once as a `swarm.stall` event, is cleared when its condition ends, and nudges
  the agent that can act by harness mail (a refused wait is its own nudge).
  `friction` reports them under `swarm.stall`; `Swarm.Stalls` and
  `Swarm.StallCounts` expose them to embedders. Stable prompt layers and
  tool-schema bytes are unchanged; nudges arrive as ordinary harness mail.
- Close tasks with typed reasons. Done tasks record `verified` or `agreed`;
  failed tasks record `blocked_on(task)`, `superseded(task)`, `canceled`,
  `denied`, `verifier` or the harness's `exhausted`. `task fail` requires
  `reason` (and `target` for the reasons that name a task) and refuses anything
  else with the list of reasons, leaving the board unchanged. `task block` takes
  an optional `target`, the task it waits for. Board events, `task get`, the task
  list, the live view and replay carry the closure; RL episode outcomes count
  them under `closures`. Library callers of `Board.Fail` and `Board.FailAt` pass
  a `Closure`. The `task` tool's description and input schema (G0) change once on
  upgrade, identically for every agent, and can require a fresh provider cache
  prefill; stable prompt layers are unchanged.
- Add best-of-n efficiency data to RL episodes and exports. Episodes gain the
  signals `tool_calls`, `stuck_warnings`, `stuck_stops`, `repeated_reads` and
  `final_answer_chars` and the soft flag `looped`; sft, dpo and groups records
  gain a `rank` object; summaries and reports gain `efficiency`, per-task
  `best`, and, for team runs, the board's `closures` summed over episodes.
  `rl export` adds `--select best` and `--pair best-worst`, and rewards
  gain the `waste` and `group_ite` components with weight 0. Existing reward
  weights, and prompt and tool-schema bytes, are unchanged. The repetition
  guard's `agent.stuck` nudges name their guard, and rollout progress lines
  print `score=` and `reward=` separately. DPO episode pairs whose chosen and
  rejected sides are identical are counted as `pair:same_continuation` and no
  longer exported, in both pairing modes.
- Make RL tasks portable and their rewards honest about a start that already
  scores. `verifier.expect.regex` gates an answer on a pattern;
  `verifier.baseline_score` records the untouched start's score of a
  `json-score` task, and the outcome becomes the improvement over it,
  max(0, (s - s0) / (1 - s0)), so `rl tasks check` now fails a task whose start
  scores something other than its recorded baseline (`--calibrate OUT` writes
  the measured scores; a task without one keeps s0 = 0). `requires` lists the
  tools a task needs: where one is missing the rollout gets the status
  `skipped`, counted in summaries and reports and never an infra error, and a
  run in which every rollout is skipped exits 1 rather than with the retry
  status. `rl tasks check --mutants` warns when a verifier still passes with
  part of the solution missing. A drive-letter path is absolute on every host
  in reward scoring and `workspace_roots`, and Windows verifiers get a scrubbed
  environment with the system variables they need. Prompt and tool-schema bytes
  are unchanged.
- Reject task actions that require an ID when it is omitted or blank. The error
  identifies the missing field and explains how to find the task ID before
  retrying; invalid calls leave the board unchanged. Role restrictions and
  manager review remain enforced. Stable prompt and tool-schema bytes are unchanged.
- Describe the detected command shell and operating system in frozen shared
  runtime context for every agent. The `bash` description no longer promises
  Bash execution or recommends `&&` for every shell. Tool-description (G0) and
  shared-context (G1) bytes change once on upgrade and can require a fresh
  provider cache prefill. Runtime guidance stays byte-identical within a
  session; tool input schemas, default shell selection, and permission rules are
  unchanged. Uppercase `.EXE` shell names retain their native invocation flags.
- Retry ref-lock creation when concurrent branch deletion removes its parent
  directory. The existing lock-wait bound still applies; missing revisions,
  permission failures, and ref-value conflicts are not treated as contention.
  Prompt and tool-schema bytes are unchanged.
- Base verification reminders on tool outcomes. Refused edits no longer count
  as code changes, and refused test commands no longer count as executed tests
  or clear evidence of a failing run. A successful edit outside test files also
  clears a pending warning from the same batch. Existing advisory messages,
  stable prompt layers, and tool-schema bytes are unchanged.
- Make `/plan <prompt>` submit a planning request after switching to plan mode,
  preserving the supplied text and current team. Bare `/plan` remains a mode-only
  command. The planning instruction is appended in the new user turn; stable
  prompt layers and tool-schema bytes are unchanged.

- Add `cache.compaction_mode=blocking` to finish an agent's automatic model
  compaction before its next request, avoiding overlapping copies of its history
  on endpoints with independent slot caches. Other agents remain concurrent;
  background compaction remains the default. Both modes retain the same commit
  policy, cancellation, timeout and fallback. Stable prompt and tool-schema
  bytes are unchanged; a blocking commit can rebase the next request earlier.
- Bind task verification and merge evidence to the checked assignment and scope.
  Scope changes invalidate in-flight results without spending verification retries;
  implicit completion retries the current scope. Acceptance rejects obsolete
  evidence. Stable prompt and tool-schema bytes are unchanged.
- Keep delayed task failures and merge results scoped to their original assignment.
  Exhausted merge retries and manager failure actions cannot cancel a replacement
  run, and an older merge record cannot erase a newer assignment's acceptance
  evidence. Stable prompt and tool-schema bytes are unchanged.
- Interpret Go package selectors separately from literal filenames, allowing
  recursive test, build, list, and vet commands on Windows. Preserve literal
  output flags and custom test arguments. Apply explicit deny and ask rules to
  descendants of recursive accesses; a child allow rule does not authorize its
  parent tree. Require approval for Go overlays before build/test allow rules,
  since their replacement paths are not visible in command operands. Stable
  prompt and tool-schema bytes are unchanged.
- Return an actionable error when a manager waits on unfinished work with no
  running workers, allowing the repetition guard to bound repeated impossible
  waits. Preserve waits for active workers, future work, mail, and settled targets.
  Planning-task claim errors name the spawn action needed to start a read-only
  owner. Stable prompt and tool-schema bytes are unchanged.
- Resolve native Windows workspace paths consistently for file permissions, including
  existing and new files, symlink and junction targets, protected files, explicit
  rules, and worker confinement. Require native permission and file-tool regressions
  in CI. Permission patterns use forward slashes; unsupported Windows path namespaces
  and aliases are refused. Shell-analysis limitations remain unchanged.
- Carry reviewed task agreements into dependent assignments, including transitive
  prerequisites, compaction, worker reuse, and recovery. Add full task retrieval
  and preserve tasks claimed during a run as harness assignments. Team guidance
  calls for shared decisions before dependent implementation and review of the
  assembled result. Read-only planning tasks require an agreement and manager
  acceptance, independently of the code verifier. Preserve blocked assignments
  and worktree instructions during reuse and claims. Bound compacted harness
  assignments by bytes so token calibration cannot truncate an admitted contract.
  Swarm constitution, manager role, and coordination tool bytes change once on
  upgrade; agreements do not rewrite shared project context per request. Assignment
  retention changes take effect at the existing declared compaction rebase.
- Add `/effort` to inspect and change reasoning effort during chat, including
  running teams. Map each model to its closest supported level and recover from
  explicit effort validation errors with bounded retries. Preserve the preference
  on resume. Effort changes declare a cache rebase and may invalidate the provider's
  message cache; prompt text and tool schemas remain stable.
- Bind watchdog cancellation and alerts to the worker run that was checked, so
  a delayed decision or alert cleanup cannot stop or clear warnings for its replacement.
- Check composite benchmark tasks against all recorded reference patches instead
  of skipping them. Reject malformed reference metadata during task admission.
  Exclude four mined tasks whose hidden tests require unspecified symbols and
  refresh the suite lock to 56 tasks.
- Label assigned task paths as `SCOPE` in the agent table. A write lease on one
  file no longer appears as ownership of its enclosing directory.
- Publish a worker's ending before allowing reassignment or mail recovery, so
  late terminal events cannot make its new run appear stopped. Ignore cleanup
  from an older run when a newer run owns the worker and its write leases.
- Show a stopped worker's recorded failure reason in its agent-table row.
- Contain Windows task and integration verifiers in Job Objects so cancellation,
  timeouts, and completed commands also terminate their remaining descendants.
  Command startup fails if the job cannot be established.
- Preserve the beginning and end of long task-verification logs, including the
  final failure diagnostic. Use the same runner as integration verification for
  bounded output capture, environment scrubbing, and process cleanup.
  Keep the final verifier lines in worker retry prompts and manager notices when
  output also exceeds their line limit.
  Scrub inherited environment names containing a delimited `KEY` component,
  including `SIGNING_KEY_CONTENT`, from task and integration verifiers.
- Deliver recovery mail that arrives while a worker's request fails. New mail can
  wake the worker once; unread mail from before that run cannot create a retry
  loop. Report worker failures to the manager even after task submission, without
  undoing submitted work or newer assignments. Recovery appends mail to the
  conversation; stable prompt layers and tool schemas are unchanged.
- Preserve Windows `cmd` quoting for verification, hooks, and shell commands,
  including paths with spaces. Disable Command Processor AutoRun for those
  commands so registry startup commands do not alter harness execution.
- Keep canceled verification runners within the configured concurrency limit
  until they exit. Include queue wait and scope discovery in the verification
  deadline, and discard results received after that deadline. Serialize captured
  stdout and stderr to prevent buffer races and preserve the combined output cap.
- Bound repeated task-verification failures across explicit completion calls and
  implicit worker stops. Persist the repair count through recovery; after two
  repair retries, count a failed attempt and notify the manager with verifier
  evidence. Cancellation and verifier infrastructure errors do not spend repair
  retries. Show terminal failures in their own board column instead of counting
  them as running tasks. Stable prompt layers and tool schemas are unchanged.
- Preserve ChatGPT sign-in authentication when listing a provider's models or
  tuning built-in provider options. Label plan model prices as `plan` instead of
  zero dollars and exclude them from numeric price limits. Explicit catalog URLs
  do not inherit sign-in credentials.
- Include concurrent `doctor --deep` requests in request, usage, and reported-cost
  totals. Mark incomplete cost data as unavailable, retain failed warm-up samples,
  and stop later probes after cancellation.
- Preserve complete messages during `doctor` cache probes for message-boundary
  providers, including concurrent warm-up bursts. Grow the sequential probe by
  appending responses and user turns, and avoid
  interpreting rounded usage counts as cache-write block sizes. Probe history
  adds input tokens; ordinary session prompts and the renderer are unchanged.
- Resume isolated teams with their original Git base, worker conversations and
  unfinished worktrees. Preserve task ownership and applied integration positions;
  reconcile interrupted checkout writes without overwriting ambiguous edits. Keep
  recovery state until explicit session pruning, which salvages unique worker work.
  Recovery rebuilds shared context through the existing declared rebase; stable
  prompt layers, tool schemas and the renderer format are unchanged.
- Allow up to 16,384 estimated instruction tokens by default, bounded to one
  quarter of the initial model's context window, with an explicit
  `cache.instruction_max_tokens` override. Preserve later instruction files
  before earlier files when the allowance is exceeded, and identify the affected
  paths. G1 changes for sessions whose instructions exceeded the old 3,000-token
  limit: their first request uses a new shared prefix and may incur a cache write.
  Subsequent turns retain identical instruction bytes; larger instructions add
  input tokens. G0, tool schemas, and the renderer format are unchanged.
- Send the Responses `session-id` routing header consistently with the body
  cache key, preserving affinity across related requests.
- Preserve live-state notices in Responses history so later requests retain
  earlier message boundaries. The stable G0-G2 layers and renderer format are
  unchanged. Persisted notices add input until compaction; the update interval
  limits growth. Provider hit rates require endpoint measurement, and the
  simulator does not model Responses message-boundary writes.
- Derive routing keys from the full session identity. Independent sessions no
  longer share a key just because their IDs start with the same date. Existing
  sessions may incur a cold request when moving to the new key.
- Wait for generated Responses content before releasing cache warm-up followers.
- Account for Responses cache writes as a separate input category without
  double-counting tokens. Cache diagnostics distinguish internal prefix stability
  from unverified provider behavior.
- Show live statistics and agents panels without adding stale snapshots to chat.
- Interrupt active work when pausing or clearing a goal, or exiting chat.
- Replace development journals and anecdotal performance claims with usage,
  design contracts, limitations, and reproducible validation procedures.

## [0.1.0]

- Interactive coding sessions, provider login, model selection, resumable history,
  terminal tools, checkpoints, and standing goals with completion checks.
- Manager and worker teams with task boards, mail, leases, budgets, verification,
  optional worktree isolation, and a serial merge queue.
- Layered prompts, cache diagnostics, background compaction, recall, and a
  simulator with explicit workload and pricing assumptions.
- Chat Completions, Responses, and Anthropic Messages adapters; ChatGPT sign-in;
  configurable gateways and local inference servers.
- RL task generation, rollouts, rewards, and dataset exports.

### Prompt compatibility

`sleipnir-kv/3` separates adjacent user turns with a blank line when they render
as one message. It changes affected thread bytes only; G0-G2 are unchanged.

Stable JSON encodes invalid UTF-8 as a replacement character consistently across
Go versions. Valid text, tool schemas, and stable layers are unchanged.
