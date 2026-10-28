# Changelog

[Releases](https://github.com/Anemos-labs/Sleipnir/releases) are the versioned
release history, generated from merged pull requests and tied to their source
commits. The notes below are cumulative compatibility and prompt-cache changes;
they do not indicate whether a change has been released.

## Compatibility changes

- RL pipeline hardening from a security review. `sleipnir rl serve` now
  applies `--budget-usd`, `--max-requests` and `--max-steps` to every HTTP
  rollout whose task leaves them open, and a blank `role_models` entry runs
  its role on the policy instead of the configuration's default model. A
  rollout's repository agent definitions run their `model:` on the policy
  endpoint (with an injected policy client, on that client). Protected patterns are limited to 1,024 bytes and 64 segments.
  Mutation tasks protect `testdata`, `tests` and `test` directories at any
  depth. A workspace id of a task whose name had to be changed carries a
  digest of the original name, so two tasks never share one. Rollouts hide
  the repositories their tasks name from confined commands wherever they
  are (including inline tasks, `file://` sources and a linked worktree's
  git directories), and `rl taskgen chain` and `rebuild` hide their blob store and sealed
  starts. Exports redact the policy's checkpoint and sampling settings,
  consent, the outcome in every format, and warnings, and drop token ids of
  steps whose prompt could not be read for redaction.
- Resuming a team's goal after an unjudged pause (an interruption, a
  failed turn, a restart) no longer asks the judge while the task board
  still has unfinished tasks, since the judge could not find the goal met
  then. The continuation names the open tasks instead of the verdict from
  before the pause, and the judge rules again at the end of the turn. A
  goal with nothing left open on the board is judged first, as before.
- The manager of a configured team is told in its live view when it has
  made more than six requests in one run with nothing on the board and
  no worker at work: unless the request is one it handles itself, it should
  create the tasks and spawn them, since each worker reads the code its own
  task touches. The staffing lines said nothing while no task was open, so a
  manager could map a whole code base alone, re-reading the same files,
  while the team waited.
- The goal judge's evidence search lists tool output (commands, reads,
  edits, merges) before the records of coordination tools (task, pool,
  mail, wait, spawn, plan, note, memory), each newest first. Those
  records restate what agents claimed; the tool output holds the proof.
  The result line counts both groups, and offsets count down the
  combined order. A tool name is classified as the agent dispatches it,
  so "functions.task.get" is the task tool's.
- Resuming a goal that paused on a turn the judge did not rule on (the
  turn was interrupted, failed or ran out of budget, the chat was restarted, or the judge
  could not be asked) now judges it first. Before, resume re-sent the
  judge's last "not met yet", from before that turn's work. A goal
  paused by the person between turns, or by a verdict, resumes as
  before.
- `sleipnir scan --start --diff BASE` creates a review of the change from BASE to HEAD (docs/SCAN.md): only the files the
  change wrote are mapped and hunted, a lead needs a trace step on a line the change made, and findings.json gains `base`
  and `changes`. Every finding in findings.json gains an `anchor`: in a review the last trace step on a changed line, where
  the review's SARIF result is located; in a scan the sink, as before.
- A session whose goal is a deep scan works on that scan: the `scan` tool's calls that name another scan of the project
  (an older unfinished one, whose lead numbers mean other things) go to the session's scan, with a note. A reproducer's
  marker may be followed by a colon (`SLEIPNIR-REPRODUCED: rows`).
- A goal judge request that fails with a network error, a timeout, a
  5xx or a rate limit is sent again, up to four times with a growing
  wait (or the server's `Retry-After`, when longer), and each retry is
  a `model.error` event. Before, one dropped connection paused the
  goal and discarded the judgment's evidence requests.
- A read-only worker (a reviewer, a scout) that stops on its task with
  no answer text is mailed once or twice for its report before its task
  is submitted, instead of reaching review with an empty result. A
  reviewer whose last turn was reasoning alone reached review with
  nothing to read, and its manager spent four minutes recalling the
  reviewer's history for the verdict before it mailed for it. About 35
  tokens in that mail, only when it is sent.
- The web page has a Deep scan view (`g d`, docs/SCAN.md): the project's scans, a scan's findings with their traces,
  verdicts and reproductions, its attack surface and shard map, the report exported as CSV, JSON, PDF, Markdown or SARIF,
  and a form that scans the project or a repository it clones (https or ssh, hooks and symbolic links off) into
  `~/sleipnir-scans`. A confirmed finding can be written up and opened as a GitHub issue, after the person reads it, or
  handed to the team as a fix goal. A scan's report now also writes `findings.csv` and `report.html`.
- A `scan` plugin, off by default, adds `/scan`, `sleipnir scan` and the `scan` tool (docs/SCAN.md): a team maps a whole
  repository at a pinned commit, hunts it for holes, and reports what verifiers confirmed and a sandboxed reproducer
  showed. With the plugin off nothing changes; turning it on adds the `scan` tool to every agent's tool list, so the tools
  layer of the cached prefix is written once more.
- `--sandbox docker` (or `docker=IMAGE`) on `sleipnir chat` and `sleipnir run`, and the *sandbox* field of the web page's
  New session dialog, run every shell command of a session inside a container of its own, with the project mounted at its
  own path, no network unless `--sandbox-network`, every capability dropped and none of the host's environment but a fixed
  allowlist of named variables. Without the flag nothing changes. The repository's `.git/config`, hooks and `info` are
  read-only inside and `.git` cannot be renamed away, a home directory (or the root) is never mounted, and a sandboxed
  session resumes sandboxed. The program now recognizes a hidden first argument, `__sandbox-exec`, which a sandboxed
  session's commands use to enter the container.
- A goal judge whose first answer is "continue", given before any
  evidence request, is sent back once to search the archive for each
  item it names as missing. Whatever it answers next stands, whether
  or not it searched.
  The page a judge is first shown holds only the newest records, so a
  verdict on it alone could call finished work missing.
- With auto-docs, a task resolved as no longer needed (`task resolve`)
  whose work did not merge in this run is no longer a docs change: it
  starts no catch-up pass, is not listed in one, and does not count as
  accepted code. A task resolved after its work merged is still one.
- A `write`, `edit` or `apply_patch` that leaves every file as it was
  (the result starting "No change") is marked unchanged. It no longer
  counts as progress for the stall sweep or as an edited file in a task's
  evidence, and the repetition guard counts it like a failure: the fourth
  identical write that changes nothing is told so, the eighth ends the run.
- A manager's `task reject` (or `reopen` of a task in review) without
  text is refused, and the worker now receives the whole feedback with
  its lines, up to 2000 characters; it used to be cut to 400 characters
  on one line. The `task` tool's description asks for feedback that says
  what is wrong and how it was seen, what to keep, and what will show it
  is fixed. The `task` tool's description grows by 104 bytes, so the tools
  layer of every agent's cached prefix is written once more.
- A rigor plan task asks for a plan an implementer can code from without
  the planner's research (every design decision settled, the file and
  function of each step, terms defined), and a rigor plan review checks
  for exactly that. The frontend role prompt names the new built-in
  `landing-page-design` skill for a landing page or project website; the
  frontend's role layer and the skills listing (one line) change once.
- A manager's `task update` with files and a text on a task another
  agent owns sets the scope and answers that it did, noting that the text
  was not recorded. It used to answer with an error after the scope had
  changed. A `task revise` with only files, on a task under way, that
  repeats the scope it already has now changes nothing and stops nobody;
  it used to stop the task's workers and put a pool back to planning.
- For ten minutes after a worker's run ends on an outage of its model's
  endpoint (a status of 500 or more, a 429, or no answer), the manager's
  spawn naming that worker is refused when a member of its role on another
  model is free, and the refusal names that member. A spawn by role starts
  that member instead.
- When a pool leader's automatic restarts run out in a pool with more
  than one member, the manager's line names the way past a leader whose
  model keeps failing: excuse it with `pool action=excuse`, and the next
  member leads. It used to say only "Resume this pool with its existing
  members".
- On a team with a reviewer slot, a manager's `wait` result ends, while
  implementation submissions wait in review, with a line that names them
  and says to decide each from its evidence (`T1, T2 in review: decide
  each from its evidence ...`): testing them is the reviewer's task over
  the merged result. A manager wrote probe tests for four submissions one
  after another while every worker sat idle; they waited in review four
  to eight minutes, and the reviewer it then spawned tested the same code
  again. About 40 tokens more in the manager's wait result, only while
  such submissions wait.
- The auto-docs pass's brief tells the docs member to read the diff of
  the changed files rather than whole sources, and to leave testing and
  probing the code to the reviewer and the verifier: it runs a documented
  command only when the change altered it. A docs pass after a manager
  fixed four modules itself ran every module's tests and wrote probe
  programs, and took 500 s of a 969 s run; with this brief the same task
  took 216 s of 538 s. About 45 tokens more in the brief.
- A spawn queued behind the busy members of its role, and the live view's
  line for such queued tasks, name the other writer roles with a free
  slot or idle member (`free now: frontend, devops: one of them can take
  T2 at once if it needs nothing only backend knows (spawn role=frontend
  task=T2)`). Managers on two models gave four independent modules to
  backend, the team's one backend slot, and the modules ran one after
  another while the frontend and devops slots stayed empty. About 35
  tokens more in that answer and line, only while they apply.
- A team whose reviewers all run on a model its writers use, while the
  team has another model too, gets a one-line tip when its manager starts:
  a reviewer on another model catches what the writers' model misses,
  including a backdoor in its weights. A suggestion, never a default; a
  team on one model is told nothing. Harness behavior only (an info
  notice); no prompt bytes change.
- With persistent pools, a task returned to todo after its worker's
  automatic restarts on it ran out is no longer started on that worker
  again by the repair pass: the manager assigns it. The manager's line
  now says the task returned to todo and asks for an assignment, instead
  of saying it was retrying with its existing worker while also saying
  the harness would not restart that worker. In a long team run five
  workers whose model had no available endpoints were started on their
  tasks again every few minutes without end, and the workers on another
  model stayed idle.
- `--role-model fallback=provider/model` (or `models.roles.fallback`)
  names a model that takes the requests of any other model of the session
  whose endpoint has been out for a minute, for ten minutes at a time. A
  team's manager whose endpoint answered "No endpoints are currently
  available" for five minutes failed, and the session ended as an error
  with every task done and verified. No prompt bytes change; the
  `--role-model` help names the new role.
- Persistence and credential dumps are high risk: asked about with
  someone there, refused in bypass mode with the reason, denied in plan
  mode. That covers writes to shell startup files, user services and login
  items, `~/.local/bin`, `~/.gitconfig`, package index configuration and
  `.pth` files outside the workspace, the commands `crontab`, `at`,
  `systemctl enable`, `launchctl load`, `loginctl enable-linger` and
  `git config --global` of a key that runs a program, and commands that
  print a stored credential (`gh auth token`, `git credential fill`). A
  backdoored model needs one of these to outlive its session or to take a
  token, and a coding task almost never does. An Allow rule that names the
  file or the exact command lifts it. Harness behavior only; no prompt
  bytes change.
- Auto-docs counts the product files the manager edits by itself as a
  change: once the manager has left them alone for 90 seconds, or when it
  means to finish, they start a docs pass scoped to the Markdown beside
  them. A manager that implemented two of four modules itself wrote their
  docs task by hand after the last review, eight minutes after its edits,
  while the docs worker sat idle. Harness behavior only; the live view's
  docs line can say that the manager's edits wait to settle.
- A review accepted after a merge that changed test files and docs alone
  since the review began covers that merge too. A tester's suite accepted
  while the review waited for its verdict started a second review of four
  test files, which took sixteen minutes. Harness behavior only; no prompt
  bytes change.
- A resumed team keeps in review a read-only role's handed-in result and a
  pool parent whose members confirmed its reconciliation, as it already
  kept merged work; the manager accepts or rejects them as before the stop.
  They went back to todo, "waiting to restart": after a container restart a
  finished review, its second-model complement and a confirmed pool parent
  all waited to be redone while their results sat on the board.
- A web request that waits out the three-minute start window while the
  session is still starting is now refused with 503 `starting` ("send this
  again shortly") instead of 409 `no_session` ("the session did not
  start"). A resumed eighteen-agent team took six minutes to start after a
  container restart, and a goal resumed in that time was told three times
  that the session had not started.
- The tester's role pin says that tests put in a package beside other tests
  give every helper, type and test they add a prefix of the tester's own, so
  a test file someone else adds to that package still compiles. A tester's
  suite declared a `fakeClock` helper in a package where another test file
  declared one too, and the package stopped building. About 40 tokens more
  in the tester's role pin.
- With one reviewer model, a merge after an accepted review leaves that
  review standing when the work tree has changed, since the review began,
  in test files and docs alone: the harness snapshots the git tree when a
  review starts and compares it at each acceptance. A tester's suite
  accepted after the review started a second review of the four new test
  files, and the run hit its limit while that review ran. Harness
  behavior only; no prompt bytes change.
- A file edit of the manager's own while submissions wait for its decision
  carries a line naming them with their waits (`T3 (6 min) and T2 (2 min)
  wait in review. A submission comes before edits of your own ...`), once per
  submission. A manager that wrote verification tests of its own between
  acceptances kept four submissions waiting six to seven minutes each, and
  the stall sweep's mail about each reached it after five. Tool-result text
  only; no prompt bytes change.
- The manager's role pin says that the tester's tests are not the manager's
  to write (a second tester is spawned when the tester is busy and a slot is
  free) and that a submission in review comes before edits of the manager's
  own. A manager created a verification-test task and finished it itself,
  with one tester slot used of eight. About 30 tokens more in the manager's
  role pin.
- A task whose scope names test files alone (`*_test.go`, `test_*.py`,
  `*.spec.ts`, a `tests/` directory) starts no docs catch-up: it leaves no
  document stale. A docs pass over four new test files held a run's final
  answer for the pass's whole length. Harness behavior only; no prompt
  bytes change.
- The manager's role pin says that a review round that finds no defect
  and names only tests or polish the agreement does not require ends the
  rounds: those go in the report as remaining. A review that met every
  requirement and named three tests it would add started a test-writing
  task, a second review and a docs pass, and the run hit its limit. About
  10 tokens more in the manager's role pin.
- The duration a review is measured against (`review_overrun`) ends at
  the work's submission, not its acceptance: the wait in review for the
  manager's decision is the manager's time, not the work's. Two
  submissions waited seven and six minutes for a manager that was
  running tests of its own, and the review's bound grew from ten minutes
  to sixteen. Harness mail only; no prompt bytes change.
- The review gate's line to the manager names the merges since the accepted
  review (`review: the merges since the accepted review (T3, T4) have not
  been reviewed ... review that work (the accepted review stands for the
  rest)`), so a second review covers that work and not the whole result
  again. The manager's role pin says the same, and that a docs catch-up is
  not reviewed, so a review does not wait for one. After a one-line fix
  from the first review's findings, a manager waited out the docs pass and
  then gave the reviewer the whole four-module result a second time. About
  45 tokens more in the manager's role pin.
- `review_starved` also holds by the clock: a submission that has waited
  in review for 5 minutes is starved whatever the manager's turn count.
  A manager waiting on a long review with long timeouts takes few turns,
  and a docs pass sat unread in review for 13 minutes beside one, which
  held up the pass that followed it. Harness mail only; no prompt bytes
  change.
- The verdict phase (no `idle_capacity` while the team waits on a review
  or a docs pass) also covers a writer still running after its task was
  accepted: it is finishing its turn and owns nothing open. A docs worker
  whose pass had just been accepted counted as work in progress, and the
  manager was told of idle writers during the review after all.
- A pool member whose `pool inspect` left conflict markers in its checkout
  may now edit the conflicted files, and only those, until the merge is
  concluded, in every pool phase; the inspect refusal names the repair and
  says to inspect again. Reconciliation made members read-only outright, so
  a member whose uncommitted draft conflicted with the combined result could
  neither inspect ("repair checkout before confirming") nor repair ("members
  change nothing while the pool reconciles"): it retried the inspect until
  the repeat guard stopped it, and the pool went on without it.
- A streaming model request that carries nothing but keep-alive comments
  for ten minutes, from its start or since its last content line, is ended
  as a timeout and retried (`nothing but keep-alives from the server for
  10m0s`). The first-byte and idle deadlines took the comments for a
  response, so a gateway whose upstream had hung held a worker until the
  swarm's stuck-worker cut at twenty minutes: two workers sat that long
  right after a resume and answered in a minute when retried. The three
  streaming adapters take a `StreamContentTimeout` (negative disables it);
  non-streaming calls are unchanged. Only that error text is new.
- A parent whose pool starts again after a resume no longer keeps the line
  "the session was resumed: waiting to restart" while it runs. The line
  stayed on the running task, and a manager that read it mailed the pool's
  leader five times in twenty minutes to claim the task it was already on.
  The board's task line is the only output that changes.
- The stall sweep does not raise `idle_capacity` while every worker at work
  is a read-only role or on a docs task: the team then waits for a verdict
  on finished work, and idle writers are expected. Told of idle capacity
  during the review, a manager created an adversarial-test task and the
  run waited on it after the review had passed. Harness mail only; no
  prompt bytes change.
- A docs catch-up task's scope names the Markdown files that exist beside
  the changed code (a module's README), besides the root Markdown and
  docs/: named files, so a later task on that directory collides with
  those files alone. Its brief says to edit the documents that exist, to
  add no README or AGENTS.md to a project that has none, and to keep the
  pass short. A docs worker scoped to the root alone mailed the manager to
  widen its scope in every run that changed a module with a README, and
  one wrote a README and an AGENTS.md into a fixture that had neither.
  Task descriptions only; no prompt bytes change.
- The `review_overrun` finding names each merged task once.
- The manager is told to accept a task from its evidence (the merge, the
  last test run, the worker's command and result) and to leave probing
  the result to the reviewer, in parallel with the next work, instead of
  verifying accepted work with tests of its own: one manager spent eleven
  minutes writing and running probe tests in four finished modules before
  accepting them, while the tasks sat in review and the team sat idle.
  About 50 tokens more in the manager's role pin.
- A reviewer whose run outlasts the work it reviews is told to write its
  report: the stall sweep's new `review_overrun` finding holds when a
  running reviewer's task has run longer than the merges no review covers
  took to build (the longest of them, at least ten minutes), and the
  reviewer's pin says a review takes less time than the work did. In one
  run a reviewer spent twenty requests re-walking four small modules that
  took fifteen minutes to build and never reported. The review gate now
  records when each assignment first ran. About 60 tokens more in the
  reviewer's role pin.
- A writer's accepted task that the merge queue had nothing to merge for
  (a tester's check whose throwaway tests were deleted again) is no longer
  a merge for the review gate, so a review accepted beside it still covers
  the result. The manager is told that a tester's check of the merged
  result and the review run side by side: a review chained after an
  independent check waited out the whole check. About 45 tokens more in
  the manager's role pin.
- Read-only roles are told plainly that they write no file, $TMPDIR
  included, and run no program of their own: the reviewer's pin, its
  refusals and the manager's line on picking a role say so, and the
  reviewer proves the change's safety fact with the project's own tests or
  by walking the failure case, naming the missing test as a finding. A
  reviewer asked to write throwaway tests in $TMPDIR tried five spellings
  of the same write. About 40 tokens more in the reviewer's and the
  manager's role pins.
- A resumed parent whose pool had accepted its plan starts again with that
  pool and that plan. The free members of its saved pool form it again, in
  their order, and are told to continue the accepted plan and its child
  tasks; a saved member that is away (on a task of its own, or beyond the
  cap) is left out and the pool goes on without it. It used to be rebuilt
  from the role's first free members under the cap, and when they differed
  from the saved pool it went back to planning with no agreement: after a
  container restart a two-member backend pool, executing with a child task
  half done in its leader's tree, was planned again by the leader and three
  idle members. A pool still planning starts over unless its members are
  the same, as before.
- A worker that claims the task it is already on, with nothing merged since
  its last sync, is told nothing changed and to carry on. It was told its
  checkout "now includes integrated work" and to re-read its files, its
  assignment was queued again, and the claim reset the no-progress alarm. A
  worker on a perf task claimed its two tasks three times in 45 minutes and
  re-read the same file after each. When work has merged, the claim still
  syncs and says so. Only that tool result changes, and the repeated
  assignment card is no longer sent.
- The board's `ctx_tokens` for an agent is now the input of its latest
  answered request, cached or not. It was 0 for every agent because the
  member never wrote it, so the status line a worker sees each turn never
  showed its context size. That line now reads, for example,
  `you: be-6 (backend) · T87 · ctx 167k`: the only prompt bytes that change
  are in that live status line, which is outside the cached prefix.
  `Agent.PromptTokens` reports the figure.
- A parent whose pool would have one member (the other members of the role
  work alone, or sit in pools that spare nobody) goes to that member as a
  solo task. A pool of one proposed a plan to itself, agreed with itself and
  made one child with its parent's scope: three reviews in one local run did
  this while the other reviewer worked alone. A parent that already has a
  pool keeps it. Tool results and mail only; no prompt bytes change.
- The merge queue's refusal of files outside a task's scope (or of files it
  may not change) still names the first eight, and now says where the rest
  are, by directory: "; and 9 more: docs/screenshots/ (9)". A worker whose
  e2e run rewrote 17 screenshots in two directories was shown eight from
  one, reverted those and had to guess the other. Tool results only; no
  prompt bytes change.
- A child task can no longer depend on its parent or another ancestor:
  the create (or revise) is refused with what to do instead, and such a dep
  on a board saved before neither holds the child back nor blocks its
  assignment. A reviewer pool's leader
  created a child of T66 with deps=[T66]; the child waited for its parent,
  the parent waited for the child, and the review stood still. Tool results
  only; no prompt bytes change.
- A worker whose work merged but whose verification failed with "no space
  left on device" (or "disk quota exceeded") is told that the disk is full,
  that this is the machine and not the code, to remove untracked build
  outputs it made and submit again, and to block the task if the disk stays
  full. Three workers whose linker could not map its output file were told
  to reproduce the failure and fix it. Tool results only; no prompt bytes
  change.
- A worker's `wait` without `until` now returns only for its own work: a
  task it owns or last owned, a pool it belongs to (the parent and its
  children), a task its open work depends on or is blocked on, and mail.
  Changes elsewhere on the board no longer wake it; they still come in its
  report when it does wake. In a local team run three pool members waiting on
  their leader's one child were woken by every review on the board, about
  twenty requests each in fifteen minutes. The manager's wait is unchanged.
  Tool results only; no prompt bytes change.
- In a goal judge's evidence, a tool's output now names the call that
  produced it (the tool and the start of its input, the command for a
  shell), so a search for a command finds its output; and a tool call, in a
  search or a read, names the record of its result. A judge that searched
  for `make cross` found only the calls, read one of them three times
  looking for the output, and ruled that no command output proved anything.
  No agent prompt bytes change.
- A goal judge's repeated evidence request is answered by naming the
  earlier request whose answer it repeats, and a record it already read by
  record or records is not served again (the others in the request are).
  One judgment re-read one record three times and ran one search twice, a
  page of context and about a minute each on a slow model. A repeat still
  counts against the 24 requests. No agent prompt bytes change.
- A goal's judge is told after each evidence answer how many requests it
  has left (24 in all), and the last answer asks for its verdict. A judge
  never told the limit spent all 24 re-reading records (22 minutes on a slow
  model), and the judgment ended with no verdict, pausing a goal whose 41
  tasks were done. A request after the last is not answered. Only the
  judge's evidence messages change; no agent prompt bytes change.
- The constitution's "Verify your work" adds one sentence: when an
  agent-authored check fails, the agent re-reads the requirement the check
  encodes once, then fixes the code or the check, and does not debug a
  throwaway check for a second round. About 35 tokens more in the system
  prompt (the cached prefix of every agent changes once).
- `run` and `swarm` without a terminal, with no `--mode` and no `--allow`
  rule, now run in `bypass` mode where they would have run in `default`
  (another mode in the configuration still holds): nobody can answer a question,
  and the default mode refused every edit and every command that is not
  read-only. Built-in protections, `deny` rules and guarded paths hold as
  always. The run says so first (stderr, or a `notice` of level `info` with
  `--json`). A run that chose a mode, `--mode default` included, behaves as
  before. No prompt bytes change.
- Building Sleipnir now needs Go 1.26.10 or newer (go.mod said 1.26.0).
  Go 1.26.0 to 1.26.4 race-instrument the darwin syscall path that runs in
  a forked child before exec (golang/go#79804, fixed in 1.26.5), so under
  `-race` on macOS a child could crash or spin, and the test that started it
  waited until the 25-minute timeout. 1.26.10 carries that fix and the
  1.26.x security fixes. A go command of 1.21 or newer with GOTOOLCHAIN=auto
  (the default) fetches it.
- A `sed` that edits (`sed -i 's/a, 5,/b, 6,/' x.go`) is judged by the
  files it touches: its file operands, a script it reads, and the files its
  script names (`r`, `R`, `w`, `W`, the `w` flag of `s`). The script's own
  words were taken for file names and split at commas and colons, so
  `s/a, 5,/b, 6,/` named the root and the credentials protection refused
  the edit in every mode. A script that runs a program (`e`) or is not
  understood is judged as before.
- A shell command whose output shows a file whole, as it is now (`cat a.go
  b.go`), counts as the read of that file: an edit or an overwrite after it
  is no longer refused for want of a read tool call. Only the output's
  bytes decide (a text file of 16 bytes to 48 KiB, from the start of a
  line, as `cat` or `cat -n` prints it), so a part of a file, clipped
  output or a file changed since is not a read. The refusal of an unread file says so. Tool
  results only; no prompt bytes change.
- `run --json` without a terminal, in the default mode and with no
  `--allow` rule, now starts its stream with a `notice` (level `warn`) that
  says nothing can be approved and that `--mode accept-edits` lets edits and
  the usual build and test commands through; the text mode already printed
  it on stderr. A run that the guard ends because its actions kept being
  refused with no one to ask names the same remedy in its error (and in the
  result's `error`). No prompt bytes change.
- Refusals by plan mode and by a read-only role's profile (a reviewer, a
  scout) now count as refusals by a fixed rule: at the third of the last
  twenty actions the agent is told, once, that no one can approve them and
  another spelling is refused the same way, and at the tenth its run ends.
  These refuse and never ask, so the guard for runs with nobody to ask never
  saw them, and a reviewer asked to run the program it reviewed spent half
  an hour on other ways to run it. The note now names plan mode and
  read-only roles among the fixed rules. Tool results only; no prompt bytes
  change.
- The system prompt scales planning and verification to the task. A focused
  fix no longer calls plan first (before: any task of three steps or more),
  the program is run as a user would only when the change alters what its
  users see, and verification is asked to happen in few requests: one
  command for every check, one check for a changed requirement that no test
  covers, no re-runs of passing checks. The constitution grows by about 120
  tokens and its bytes change, so every session's cached prefix is written
  once anew.
- An edit, write or patch refused because the agent has not read the file,
  or because the file changed since it did, now carries the file's current
  content, line-numbered as `read` shows it, and the file counts as read:
  the agent makes the change again at once instead of spending a request
  on a `read` first. Nothing is changed unseen. A file over 48 KiB,
  or one that is not text, keeps the bare refusal. Tool results only; no
  prompt bytes change.
- The manager's `wait` with `until` now also returns when one of the awaited
  top-level tasks newly reaches review while a worker sits free (idle, with
  no unfinished task of its own). It used to return only once every awaited
  task settled. A live team run slept ten minutes beside two tasks in review
  while one long task ran on, with four of its five workers idle. With every
  worker busy, the wait still goes on for all of them. The `wait` tool's
  description changes (66 bytes), so the tool block's cached prefix is
  rebuilt once.
- A single agent that changed code and then ran the tests with the
  command a project script wraps (`node --test`, `mvn -q test`,
  `./gradlew test`, `npm run test:unit`, `bun test`, `dotnet test`, ...) is
  no longer told it has not run the tests. The check knew a short fixed list
  of spellings, so a Node project whose `test` script is
  `node --test test/*.test.js` drew the "[harness] You changed code and have
  not run the tests since" note after a passing run, costing a request and
  a second run of the suite. A test tool's subcommand now counts
  after its flags (for mvn, gradle, make, rake and sbt after other tasks
  too), and so does a command that starts with the words of the project's
  own test command from the survey. The command line is parsed: a runner
  that only appears as an argument (`grep 'go test' Makefile`) does not
  count. No prompt bytes change.
- Sessions now keep only what a resume reads, unless `--training-logs` is
  given (chat, run, web). That covers the event log, tool outputs and each
  agent's newest snapshot. Off by default: each request's prompt manifest
  blobs, each response's completion blob, the stable layer texts, and every
  superseded snapshot. A new snapshot replaces the agent's previous one in the
  blob store once the log holds it; savepoints still keep every recoverable
  snapshot. A 32-agent run had written 13 GB of superseded snapshots.
  Rollouts (`sleipnir rl`) keep training logs as before. A session recorded
  without the flag cannot be exported as trajectories. No prompt bytes
  change.
- A child task that an executing pool gives one of its members is no longer
  refused by another pool the member is in or was excused from, even where
  the two pools' scopes overlap. Before, a member excused from one pool and
  given a one-member pool of its own for the same files could change nothing
  in its child. No prompt bytes change.
- A pool member, or one the manager excused, that is doing a task of its own
  outside its open pools now writes in it under the session and the task's
  scope. Before, every write was refused as a pool member's until the pool's
  task closed, so excusing a member to free it for other work left it unable
  to change anything. A path outside that task's scope, or a shell command
  while the member still holds a pool child in doing, keeps the pool's rule.
  No prompt bytes change.
- A task scope segment spelled exactly like a path segment now matches it as
  that name, so a scope naming a Next.js dynamic route
  (`web/app/entities/[id]/page.tsx`) covers that file for the write check and
  the merge queue alike. Before, `[id]` was read only as a character class
  (one of `i`, `d`), so the scope covered `web/app/entities/i/page.tsx` but
  not the file it named, and writers were refused their own files. No prompt
  bytes change.
- A writer confined to its own tree may no longer move the person's checkout's
  branch or a branch of the run (`sleipnir/...`) with `git update-ref`,
  `git symbolic-ref`, `git branch -f/-C/-d/-D/-m/-M`, `git push .` or a
  `git fetch` refspec into a local branch: the command is refused with an
  `isolation:` reason. A manager that ran `git update-ref` on the checkout's
  branch moved it to the integration tip while the person's files stayed
  behind. No prompt bytes change.
- Background jobs name what they serve. When the output of a job an agent
  started with `run_in_background` names a local address
  (`http://localhost:5173/`, `http://0.0.0.0:8080`, shown as localhost), the
  job keeps it (at most four) and a `tool.job` event with status `serving`
  and `urls` announces it; the activity feed says `background job job_1
  serves http://localhost:5173/`. `/jobs` lists the jobs with their
  addresses and `/jobs stop ID` ends one. The web interface's status bar
  shows a chip while jobs run (`▶ 1 serving`) that opens them, with links to
  the addresses and a Stop button; `wire.MetaPatch` has `jobs`. No prompt
  bytes change.
- Review comments in the web interface's Workspace: a click on a line number
  of a diff or a whole file comments that line; Send comments puts every
  comment, with its file, line and code, in the message box for the agent
  (or the manager) to act on.
- The question notification of the web interface (Settings › Appearance)
  also tells when a session finishes a turn while the page is out of sight,
  unless the turn ended on a question or the team still works.
- The session event log survives a failed write. A full disk used to stop it
  for good: the first failed write made every later event fail until a
  restart, so a long run silently stopped being recorded. Events now wait in
  memory (up to 64 MiB) and reach the file once writes succeed again, with
  no torn line left behind; `Emit` keeps succeeding meanwhile, and `Flush`
  and `Close` report the failure while it lasts.
- A role's pool takes at most four members for one parent task
  (`swarm.pool_max`, new; `0` is the default of 4, a negative value sets no
  cap). A larger role used to put every free member into the first parent's
  pool, which held all of them while it agreed on its plan; now the others
  form the pool of the role's next parent at once. Teams with four or fewer
  slots of a role behave as before.
- Saved teams (loadouts) in both interfaces. The chat's `/team` lists the
  saved teams, saves the running team (`/team save NAME [--project]`), starts
  again as one (`/team load NAME`, keeping the conversation; `--fresh` for an
  empty one), prints one to share (`/team show NAME`) and removes one. The web
  interface has a team library, opened from the New session dialog (Saved
  teams…), Settings › Team & run and `/team`: save, use, copy, download,
  import and remove a team; a team used in the dialog starts as
  `--team NAME`. The files are those `--team` reads. New routes: `GET
  /api/teams`, `POST /api/teams/check`, `PUT` and `DELETE /api/teams/{name}`,
  `GET /api/sessions/{id}/team`; `wire.NewSessionRequest` has `teamName`. No
  prompt bytes change.

- The duration a review is measured against (`review_overrun`) ends at
  the work's submission, not its acceptance: the wait in review for the
  manager's decision is the manager's time, not the work's. Two
  submissions waited seven and six minutes for a manager that was
  running tests of its own, and the review's bound grew from ten minutes
  to sixteen. Harness mail only; no prompt bytes change.
- The review gate's line to the manager names the merges since the accepted
  review (`review: the merges since the accepted review (T3, T4) have not
  been reviewed ... review that work (the accepted review stands for the
  rest)`), so a second review covers that work and not the whole result
  again. The manager's role pin says the same, and that a docs catch-up is
  not reviewed, so a review does not wait for one. After a one-line fix
  from the first review's findings, a manager waited out the docs pass and
  then gave the reviewer the whole four-module result a second time. About
  45 tokens more in the manager's role pin.
- `review_starved` also holds by the clock: a submission that has waited
  in review for 5 minutes is starved whatever the manager's turn count.
  A manager waiting on a long review with long timeouts takes few turns,
  and a docs pass sat unread in review for 13 minutes beside one, which
  held up the pass that followed it. Harness mail only; no prompt bytes
  change.
- The verdict phase (no `idle_capacity` while the team waits on a review
  or a docs pass) also covers a writer still running after its task was
  accepted: it is finishing its turn and owns nothing open. A docs worker
  whose pass had just been accepted counted as work in progress, and the
  manager was told of idle writers during the review after all.
- Agents save memory notes without asking. A change to the agents' notes
  (`~/.sleipnir/MEMORY.md`, or the project's `.sleipnir/MEMORY.local.md`) no
  longer waits for the person's approval; the protection of `~/.sleipnir` and
  `.sleipnir` is for files an agent writes there itself, not for the memory
  tool's own file. Instead the Memory item of the web interface's left bar
  counts the session's new memory changes until the Memory view is opened
  (`wire.Tool` gains `mem`: added or removed). A `memory` ask rule puts each
  change to the person again, and a `memory` deny rule refuses it. The memory
  tool's description changed, so the tools layer of the prompt cache is
  rebuilt once.

- The Memory page shows the project's own notes (`.sleipnir/MEMORY.local.md`),
  to edit, remove and add like the user's (`PUT .../memory/notes` takes
  `scope: project`), and the team's lore by role, read-only
  (`wire.MemoryView` gains `project` and `lore`).

- The research tool no longer makes agents think about `run`. Its
  description, schema and the run's brief say to leave `run` out (every
  call goes to the run the session works on), and a call that names a run
  that does not exist goes to the active run with a note instead of being
  refused. Agents had spent turns working out whether `research cite`
  needs `run`. The tool's description changed, so the tools layer of the
  prompt cache is rebuilt once.

- Quick research is quick. The quick depth asked for 15 sources from 8
  sites, 24 findings and 4 to 7 pages, each fact-checked by another agent, so
  a quick run (or Research first at quick depth in New session) took hours.
  It now asks for 8 sources from 4 sites, 12 findings, 1 to 3 pages and 2
  snippets from 2 projects, and has no fact-check round (`Depth.Reviewers` 0):
  the harness still verifies every quote and citation, and a review that
  finds issues still holds the page. The brief and goal of a run at quick
  depth change accordingly. A plan that leaves out the overview page (`index`) gets
  it, instead of sending the run back to planning. The Research view leaves
  the fact-check phase out of a quick run's progress (`wire.ResearchRun`
  gains `noFactCheck`).

- Notices no longer pile up after restarts. A resumed session found the same
  skills, hooks, plugins and instruction files as its earlier start and
  logged the same warnings again, so a page replaying the log showed one more
  copy of each per restart. A start-up warning already in the session's log
  is now not logged or shown again.

- An agent waits out a flaky endpoint that has answered it before. A failure
  in which nothing answered (a dropped or reset connection, a gateway that
  timed out) got only the six attempts, about half a minute, unless the
  session said `WaitUnreachable`; a gateway that dropped connections for a
  minute cost a worker its turn. Once the endpoint has answered the agent it
  is known to exist, and such failures are waited out under the outage
  patience (five minutes by default), like a 502 or 503. Before any answer a
  misspelt URL still fails after the six attempts.

- Questions wait for their answer. `sleipnir web` no longer refuses the open
  questions after a minute without a page (`--ask-grace` now defaults to 0,
  never); `--ask-grace` and `--ask-timeout` still set a limit when given.

- Bypass mode never asks. What it used to ask about (the high-risk class,
  the paths an `ask` rule names, such as `.sleipnir/`) is refused at once,
  and the refusal says what flagged it and asks the agent for a safer way.
  The Permissions page's tester shows it as refused.

- The manager can answer for a person who is away. With
  `permissions.manager_answers_after` (`--manager-answers-after`, or "when
  you are away" in New session), a worker's question left unanswered that
  long goes to the manager, if it is no more dangerous than
  `permissions.manager_answers_up_to` (`low`, `medium` by default, or
  `high`). Its answer withdraws the person's question, which shows who
  answered and how. Off unless set; both keys are security-sensitive.

- A worker can clear a false-green refusal. The refusal said to replace a
  dependency store that links outside the tree, but `unlink` on the link and
  any command that both removed the link and reinstalled through it were
  refused, since the link resolved outside the tree. `unlink` now removes the
  link itself, as `rm` does, and the refusal names the command to run on its
  own first (`rm .venv`), one for each link.

- `sleipnir run --mcp-config FILE` (and `sleipnir swarm`) starts the MCP servers
  a file names for that run only, beside the configured ones. The file is
  `{"mcpServers": {...}}` as `.mcp.json` writes it, or the `mcp` section's own
  shape; the flag repeats. An entry is trusted as the user configuration is,
  replaces a configured server of the same name for the run, and cannot be
  replaced by a project's `.mcp.json`. A program that drives the harness one
  session per conversation hands a session its tool servers this way. Started
  from the web interface's command runner, a run with the flag asks for a
  confirmation first, as `--trust-project` does. No prompt bytes change for a
  run without the flag.

- A research call that names no run reaches the project's only unfinished
  run after a session resumes. The run a call means was kept only in memory,
  so after a resume every `research` call without `run` was refused with "no
  research run is open" until one named it. With several unfinished runs,
  the refusal now lists them.

- Previews of files in the web interface. The Workspace shows images (SVG
  included), video, audio and PDF files, rendered Markdown (with mermaid
  blocks), mermaid diagrams and CSV/TSV tables in a Preview view instead of
  "a binary file"; a changed picture is shown before and after. A picture an
  agent read or wrote is shown under its tool row in the chat and the agent
  drawer. New routes: `GET /api/sessions/{id}/ws/raw` and
  `GET /api/preview/mermaid` (a sandboxed frame with the vendored
  `@mermaid-js/tiny` build). The page's policy adds `media-src 'self'` and
  `frame-src 'self'`. No prompt bytes change.

- A research-first goal keeps the free workers building. The goal and the
  brief of a `/goal --research` run now say that the workers the research
  leaves free lay the groundwork no finding can change (what the project
  fixes outright: stack, layout, tooling, stated requirements) while it runs,
  and that any free worker writes a wiki page, one writer per page. Before,
  "research first, then build" held the whole team for the research, and
  the docs role alone wrote the pages. The generated goal text of new
  research-first runs and the generated brief of new runs change; the shared
  prompt layer does not.

- `--team NAME` looks in the project of `--cwd`. A team named by NAME is
  found in `.sleipnir/teams` of the project `--cwd` names, wherever `--cwd`
  stands on the command line, not only in the project of the directory the
  command runs from (`sleipnir web --cwd ~/app --team build` from another
  directory said there was no such team). The file is now read once the
  whole command line is parsed, so an unreadable team is reported then.

- Isolated agents read what git does not track. A confined agent may read a
  file of the checkout that git does not track (untracked, or ignored as
  before), since its tree, made from the commits, may have no copy: with
  `--commit` the untracked files are not copied, and files written during
  the run (a research run's brief) never are. A research-first goal names
  its brief by its absolute path in the checkout when the team works in
  trees, so the manager can read it. No prompt bytes change.
- A diagnosis owner for named stalls. A stall finding that still holds ten
  minutes after its nudge is escalated once to the manager with a
  diagnosis (the task's and agent's state, how long it held, who was told,
  and what ends it), logged as `swarm.stall` with `action: escalate`. No
  prompt bytes change.
- No false greens from linked dependencies. In an isolated team, the
  verification gate fails without running the verifier when a dependency
  store in the worker's tree (`node_modules`, `.venv`, …) is a symlink to
  somewhere outside it, since tests there could pass against the main
  checkout's code. No prompt bytes change.
- Ask a past agent. `recall` takes `agent` (a teammate in this session) and
  `session` (`last` or an id: an earlier session of the same project, read
  from its log without writing to it), so an agent can search or page what
  another agent found or decided. Prompt bytes: the `recall` tool's
  description and schema change, so the tool prefix is written once.
- Rigor per task. `task create` takes `rigor`: `plan` creates a read-only
  plan task for the architect (or scout) that the work waits for and
  inherits; `reviewed` adds a reviewer's check of that plan, whose
  amendments the work follows as acceptance criteria. Prompt bytes: the
  `task` tool's description and schema change, so the tool prefix is
  written once, and the manager's pin gains a sentence.
- Review by two models. When a team's reviewer slots run on two or more
  different models, the review gate holds the goal until reviewers on two
  different models have each covered the last merge; the gate's line says
  so. A team whose reviewers share one model keeps the single review.
  Prompt bytes: the manager's pin gains a few words.
- `--team NAME` (chat, run, swarm, and web for new sessions) starts the
  team saved in `.sleipnir/teams/NAME.json` or `~/.sleipnir/teams/NAME.json`:
  its `slots` are the workers in order, each on its own model or its role's,
  and `models` names the manager's, compactor's, judge's and the other
  service roles' models. It is the same team as the `--role-model` values it
  stands for; no prompt bytes change.
- Memory in two scopes. The `memory` tool takes `scope`: `user` (the
  default, `~/.sleipnir/MEMORY.md`, shown in every project) or `project`
  (`.sleipnir/MEMORY.local.md` at the project root, shown only there and only
  when the project is trusted; the first project note writes a
  `.sleipnir/.gitignore` that lists it). `list` without a scope shows both.
  Lessons of `/goal` record the project they were learned in, and retrieval
  multiplies the score of a lesson from the same project by 1.25; a lesson
  learned again in another project becomes general. Prompt bytes: the memory
  tool's description and schema change, so the tool prefix is written once.
- Project memory by role: in a team session, a worker's `note` with
  `scope: role` is kept for the agents of its role in later sessions of the
  same project once a task of that worker is accepted, in
  `~/.sleipnir/lore/<project>-<hash>/<role>.md` (an insight, confirmed again
  each time a later session saves it; a person may mark a line `[canon]`).
  Each worker role's pin carries its file's items in a `<role-lore>` block,
  read once at session start, so the role layer is stable within a session.
  `--no-memory` turns it off. Prompt bytes: the `note` tool's description
  grows by one sentence, and a role whose file has items gets a longer pin
  (at most about 1,500 tokens), written once when the session starts.
- A capability map: the constitution asks an agent to check the instruction
  files' Capabilities section, and to search by what a thing does, before it
  adds a subsystem, and to extend what exists rather than build a second one.
  Under auto-docs the docs worker keeps a `## Capabilities` section in
  AGENTS.md (one line per existing system: what it does, where it lives, its
  doc as `path#heading`), workers check it and mail new capabilities, and the
  reviewer reports a module that duplicates an existing one. `sleipnir init`
  writes the section into a new AGENTS.md. Prompt bytes: the constitution grows
  by about 40 tokens (the bound is raised from 5150 to 5200), and the
  manager, worker, docs and reviewer pins change, so the first request of each
  role after upgrading writes its prefix once.
- `POST /api/sessions/{id}/model` takes `team`, the whole team to go on with
  (one `{role, model}` per worker slot), and `wire.RestartRequest` of kind
  `roles` carries it: one restart changes how many workers each role has and
  the model of each slot, with the role models of the same request. A
  `/restart` line that names a whole team with `--role-model` (a role without
  a model, a role given twice, or as many worker entries as its `--swarm`)
  now replaces the team the session started with instead of adding to its
  slots. No prompt bytes change.
- The `read` tool takes `section`: a Markdown heading's text or anchor slug
  (`path#heading` works too, unless a file of that literal name exists). Only
  that heading and the lines under it, up to the next heading of the same or a
  higher level, are returned, with their real line numbers; an unknown heading
  is refused with a list of the file's headings. Prompt bytes: the tool list's
  `read` description and schema change.
- In a team with worktree isolation, an agent may now read the files git
  ignores in the person's checkout (data sets, assets, local inputs kept beside
  the project), which no worktree has a copy of; writing them is still refused.
  `perm.Config.Ignored` is the new hook that says which paths those are. An
  isolation refusal now says where the path is (the checkout, another agent's
  worktree), why it is out of reach and what to use instead (for a project
  file, the same file in the agent's own tree, named in full). Refusals by a
  fixed policy (isolation, a built-in protection, a deny rule) now count
  toward the repetition guard in every run, attended or not: the third in
  twenty calls is told to stop retrying, the tenth ends the run.
  `perm.IsPolicyRefusal` tells them apart. Prompt bytes: both isolation cards
  (worker and manager) end with one more sentence about ignored files.

- MCP tools that do not fit the tool list (more than 128 per server or 256 in
  all, a schema over 8 KiB or nested past 24 levels, or past the 64 KiB byte
  budget) are no longer dropped: they load on demand through two new tools,
  `mcp__search` (finds tools and returns their full schemas) and `mcp__call`
  (runs one, asking the same permission under the tool's own name). A schema
  over the 64 KiB on-demand limit is compacted (repeated subschemas move to
  `$defs`, `examples` go) before it is refused. A new server entry option,
  `defer_tools`, loads all of a server's tools that way. The per-tool warnings
  become one info notice per server. `SnapshotOptions.NoDefer` keeps the old
  behaviour. Prompt bytes: none for sessions whose servers fit; otherwise the
  list holds what fits plus `mcp__search` and `mcp__call` (their bytes come out
  of the same 64 KiB budget), a change made once at session start.

- A need's install method (docs/PLUGINS.md) can declare `setup`, commands run
  in order before `run`, and `env`, variables its commands get from the
  `NAME=value` line a command prints; `GET /api/needs` and `sleipnir plugins
  needs` show both. Blender's Flatpak install adds Flathub as a user remote
  first (it failed with "No remote refs found for 'flathub'" where Flathub was
  only set up system-wide), and the Blender add-on install asks the located
  Blender for its add-ons folder (`BLENDERMCP_ADDONS_DIR`), so a Flatpak or
  Snap Blender, or one never started, no longer fails with "Could not find a
  Blender user addons directory". Prompt bytes: none.
- New `research` plugin (on in the standard profile; docs/RESEARCH.md): the
  `research` tool, `/research`, `sleipnir research`, the web page's Research
  view and the `/research/{id}/{slug}/` wiki route, and
  `/goal --research=DEPTH GOAL`. Runs live under `.sleipnir/research/`.
  `web.RegisterReader` and `web.Reader` expose the guarded fetcher to other
  tools. Prompt bytes: the tool list gains `research`, so the first request
  after upgrading writes that prefix once; a published research-first run
  adds one segment, `research (from web sources, unverified)`, to the shared
  layer, a declared change made once at publication. `sleipnir plugins
  disable research` leaves both out.
- `rl export --pack` (sft, tokens, groups) locates a step's completion in the
  later prompts by its content and tool calls, whatever reasoning the completion
  or the replayed turn carries: a prompt replays a turn's reasoning only where
  the provider's replay binding asks for it, so every turn of a reasoning model
  on another provider was `step:choice_misaligned` and left untrained (4,040 of
  4,747 steps of one swarm session). Those turns are now targets.
- `rl export`, `rl verify` and `rl reward` read an ordinary session's directory
  (one that holds `events.jsonl` and no `episode.json`), or a directory of them,
  as docs/TRAINING-DATA.md has said: the episode's task is the session id, every
  model in it is a teacher and it is flagged `weak_label`, so the training
  formats take it with `--keep-weak` and `--teacher MODEL`. Before, such a
  directory was "no rollouts found".
- A small top-level task of a role with several members can go to one
  member without the role's pool: `spawn … solo=true` gives it to the
  member named with `agent=`, or to the first free one, and marks the task
  solo (`Task.Solo`, JSON `solo`), so a restart, the repair pass or a
  queued start gives it to one member again and its member may claim it.
  While a member owns an open solo task, no new pool of its role takes it.
  `solo` is refused for a pool's child and for a parent that already has a
  pool. Before, `agent=` on such a task only chose the pool's leader, and
  every member voted on it (docs/SWARM-PROTOCOL.md, **Solo tasks**). New
  `SpawnReq.Solo` and `TaskSpec.Solo`. Prompt bytes: the `spawn` tool's
  description and schema change (the tools layer of every swarm agent), and
  the manager pin gains a sentence (small work, such as a scaffold, goes
  to one member with `solo=true`; the pool is for a task big enough to
  split), so the first swarm session after upgrading re-writes those
  prefixes once. The mail that starts a pool gains one sentence (a small
  task is not divided: the leader proposes one child it does itself);
  mail is not part of a stable layer.
- A permission question the person refused, or that nobody answered, is
  not put to the person again in the same session: a repeat is refused at
  once with the earlier refusal, after the fixed prefix `asked earlier in
  this session: `. A question about network access to a host covers every
  URL of that host. Changing the mode or adding or removing a rule lets
  those questions be asked again; approvals are remembered as before
  (docs/SECURITY.md). The scout role prompt gains one line: plan web
  research before fetching, and after a refused or unanswered fetch go on
  from what is known, citing the URLs it would have read as unverified.
  Prompt bytes: the scout role pin changes, so the first session after
  upgrading re-writes that prefix once; the refusal texts are fixed.
- `accept-edits` mode reads public websites without asking: `web_fetch` of a
  URL on a public internet host, and `web_search`, are allowed with the
  reason "accept-edits mode: reading a public website". A host on this
  machine, a private, link-local, unique-local, multicast, unspecified or
  reserved network, a cloud metadata address, `localhost`/`*.localhost`, a
  single-label name and names under `.local`, `.internal`, `.lan` and
  `.home.arpa` still ask; the host is judged by its text, without DNS. MCP
  tools, other network tools and shell commands such as `curl` still ask,
  `default` mode still asks, `plan` mode still refuses, and `deny` and `ask`
  rules still win. A fetched URL can carry data out without a question: use
  `default` or `plan` mode or a `deny` rule (`WebFetch`,
  `WebFetch(domain:...)`) to prevent it (docs/SECURITY.md). New
  `weburl.LocalHost`, `weburl.BlockedAddr`, `weburl.LiteralIP` and
  `weburl.ErrBadIP`, shared with the `web_fetch` address guard. Prompt bytes:
  none change.
- The harness's commit of a worker's tree (at `done`, at a handover, and
  when `sessions prune` salvages edits) leaves out untracked files inside a
  dependency store (`node_modules`, `bower_components`, `.next`, `.turbo`,
  `.parcel-cache`, `.venv`, `venv`, `__pycache__`, `.pytest_cache`,
  `.mypy_cache`, `.gradle`, at any depth), so an install in a project
  without a `.gitignore` for it no longer reaches the integration branch.
  Files already tracked there are committed as before. New
  `gitx.Repo.CommitAllWith` and `gitx.CommitOptions`; `CommitAll` is
  unchanged. Prompt bytes: none change.
- A role pool's leader is mailed when every other member has agreed to (or
  confirmed) the current revision and only its own vote is missing, so a
  leader that proposed and went idle no longer holds the pool in planning.
  Prompt bytes: none change.
- When a team worker's run ends, the processes its ordinary shell commands
  left running (`server &`, `nohup server >log 2>&1 &`) are ended, so a
  server a worker started does not hold its port for the rest of the
  session. Background jobs (`run_in_background`) stay session-wide and end
  with `bash_kill` or the session. Prompt bytes: none change.
- Shell commands always have `~/.sleipnir/bin` at the front of their `PATH`
  (before, only when it held a launcher at session start), so a program
  located or installed on the Plugins page reaches a running team at once.
  Prompt bytes: none change; the toolchain line lists only commands that
  exist. Blender gains a `snap` install method and the `~/blender*/blender`
  and `~/.local/opt/blender*/blender` default locations on Linux.
- Plugins declare the programs and tool servers they use (`needs` in
  `plugin.json`; docs/PLUGINS.md): the built-in skills need Blender, uv,
  Node.js, REA and the Blender and REA tool servers, and GitHub delivery the
  GitHub CLI. `sleipnir plugins needs` and the Plugins page show each one,
  found or not; a missing one can be located (`sleipnir plugins locate`) or
  installed with one of its declared commands, only when asked
  (`sleipnir plugins setup`, or the page after a confirmation). New routes
  `GET /api/needs`, `POST /api/needs/{id}/locate`, `DELETE
  /api/needs/{id}/locate` and `POST /api/needs/{id}/install`; new state
  `~/.sleipnir/needs.json` and `~/.sleipnir/bin`, which shell commands now
  have at the front of their `PATH` when it holds a launcher. Prompt bytes:
  none change unless a launcher is there; then the toolchain line of the
  shared prompt lists the commands it makes available.
- Everything beyond a small core is a plugin you can switch on and off
  (docs/PLUGINS.md): the tool families, skills, built-in skills, custom
  commands, agent definitions, hooks, instruction files, the team and each
  worker role, the mailman, docs on autopilot, manager approvals, push
  progress, standing goals, GitHub delivery, the project survey, the update
  check, and each view of the web page. New key `plugins` (`profile`,
  `enabled`, `settings`), new command `sleipnir plugins`, new slash command
  `/plugins`, a Plugins view on the web page, and the profiles `standard`,
  `minimal`, `team` and `headless`. `swarm.mailman`, `swarm.auto_docs`,
  `swarm.push_progress`, `permissions.manager_approves`,
  `tools.builtin_skills`, `--no-web`, `--no-mcp`, `--no-recon`, `--mailman`,
  `--auto-docs` and `--no-memory` (for the new `lessons` plugin) keep working as layers of the plugin that owns them.
  External plugins (a directory with `plugin.json` and any of skills,
  commands, agents, hooks and MCP servers, in the Claude Code layout too)
  install with `sleipnir plugins add` and are off until turned on. Switching
  a plugin on in a project's file needs the project to be trusted. Every card
  on every page of the web UI can now be moved, resized, hidden and added
  back, as the Cockpit's could. The session log gains the events `plugins`
  and `plugin.state`. Prompt bytes: none change with the default plugins;
  turning a tool plugin off removes its tools from the tool list, and turning
  `skills`, `instructions` or `recon` off removes their part of the shared
  layer, each costing one cache write when the team starts again.

- The branch that progress pushing writes on the remote is named after the
  work, not the session id: `sleipnir/<topic>-<session suffix>`, where the
  topic is a slug of the standing goal's objective, or else of the first merged
  commit's subject (for example `sleipnir/add-csv-export-a1b2c3`). Before this it
  was `sleipnir/<session>/_integration`, which is still the local integration
  branch and the remote name when there is no topic. A session resumed from
  before this change pushes to the new name. Prompt bytes: the constitution's
  line on commit messages also covers branch names, and so does the manager's
  isolation card (a few tokens each), written to the cache once.

- The `embedder` and `reranker` roles (`models.roles`, `--role-model`) turn
  on a lesson memory for standing goals: lessons from earlier goals, stored in
  `~/.sleipnir/lessons.json`, are appended to a goal's first message, and new
  ones are distilled when a goal ends. Without an `embedder` nothing changes.
  `goal.State` gains `Lessons` and `Verdicts`; the log gains `lessons.retrieve`
  and `lessons.learn`. Prompt bytes: a goal's first message gains the lessons
  block when there are lessons to show; nothing else changes.

- `--no-memory` (`chat`, `run`, and `web` for its new sessions; a checkbox of
  the New session dialog) starts a session without the global memory: the
  notes in `~/.sleipnir/MEMORY.md` are not loaded into the shared layer, and
  the `memory` tool, still in the tool list, answers that the memory is off.
  Session metadata gains `noMemory`. Prompt bytes: none change for a session
  with the memory; one without it lacks the notes in its shared layer.

- Commits the harness records describe the change instead of the run that
  made it. A worker's commit has the task's title as its subject (without a
  leading board id such as `T12:`), what the worker reported as its body, and
  the id as a `Sleipnir-Task:` trailer; a merge commit is
  `Merge "<subject>"` rather than `Merge worker-3: T12: ...`; a handover is
  `Work in progress: <title>`; the manager's own edits are named by the files
  they touch; and the default delivery commit lists the landed tasks by title.
  The commits are authored by the person whose repository it is: when
  `user.name` and `user.email` are configured, every commit of the trees and
  the merge queue carries that identity and names its agent in a
  `Sleipnir-Agent:` trailer (the web blame reads the trailers, and still
  understands older history); without one, agents author their own commits
  as `<agent>@sleipnir.invalid`, as before.
  Prompt bytes: "Write good code" gains one line asking for commit messages and
  pull request titles and descriptions that say what the change does, never
  task ids or agent names (constitution about +50 tokens), and the manager's
  isolation card gains one sentence; each is written to the cache once.

- The `judge` role (`--role-model judge=M`, `models.roles.judge`, `/roles
  judge=M`) names the model a standing goal is judged on; without it the
  session's model judges, as before. With a judge model that is not the
  session's, a "done" is confirmed by a second judgment on the session's
  model and the goal is met only when both agree; a disagreement continues
  with what the confirmation found missing. `goal.judge` gains `judge` (the
  judge's model) and `confirm` (`model`, `verdict`). Prompt bytes: none
  change.

- Eight more built-in skills, used by default: `engineering-rigor`,
  `diagnose`, `tdd`, `codebase-design`, `spec`, `code-review`, `prototype`
  and `plain-writing`, adapted from pstack (Lauren Tan) and Matt Pocock's
  skills, both MIT. Every role prompt says when that role loads which, and
  the constitution carries their core rules: a failing command before any
  theory in debugging, testing the assumption after two fixes built on it
  failed, the two imagined edits that judge a test, subtract before you add,
  and trying what running something can answer instead of asking. New key
  `tools.builtin_skills` (default `true`) leaves all built-in skills out.
  Prompt bytes: the constitution grows by about 70 tokens (its bound goes
  from 5,100 to 5,150), seven role pins by about 40 tokens each, and the
  skills listing in the shared layer by about 330 tokens; each is written to
  the cache once a session.

- Sleipnir ships built-in skills, listed after every skills directory and
  loaded on demand like any other: `blender`, `blender-web-assets`,
  `blender-scene`, `reverse-engineering` and `reverse-engineering-ui`. They are
  written under `<state>/cache/skills/`; a user or project skill of the same
  name replaces one. Prompt bytes: the skills listing in the shared layer
  gains one line per built-in skill, and the frontend and scout role prompts
  gain one line each naming when to load them, so the first session after
  upgrading re-writes those prefixes once.

- A new project is presented by default. "Build to ship" gains "Presented":
  a new project is a git repository with a `.gitignore`, CI that runs the
  build, tests and linters, and a README with install and quick start,
  screenshots of the running product (or real sample output for a program
  without a screen) and measured numbers with the command behind each. The
  manager plans CI and the README as tasks of their own, the docs role
  captures the screenshots and numbers, and the goal judge holds a new
  product to the same presentation unless the goal asks for something
  minimal, a prototype, or rules it out. Prompt bytes: the constitution
  grows by about 165 tokens (solo 4560, swarm 4996), the manager and docs
  role pins and the judge's system text change; each is written to the
  cache once.

- In the browser, model changes on Settings › Roles & effort and Settings ›
  Models wait in a bar until Apply sends them together: a team starts again
  once for any number of roles, and editing goes on while it restarts.
  `POST /api/sessions/{id}/model` takes `roles` (role to model) for that. A
  restart for a change of models resumes the goal that was active, or, with
  no goal, tells the manager to go on when the team was working; the goal is
  no longer left paused for the person to resume. `agent.restore` carries the
  agent's model, and an agent's model follows its main requests, so the
  roster shows the model a restored or switched agent runs on now. Prompt
  bytes: no stable layer changes; the go-on message is a user turn.

- A proxy that refuses the CONNECT to the endpoint for good (any status but
  407, which asks for credentials, and the gateway failures 502, 503 and 504,
  which are retried) is reported as "the proxy does not allow HOST", with the
  proxy's rules, `HTTPS_PROXY` or another endpoint as the remedies, instead of
  naming "the network policy of this machine". Prompt bytes: none change.

- A worktree-isolated team pushes its integration branch to the checkout's
  push remote (else `origin`) as `sleipnir/<session>/_integration` after
  every merge, so verified work no longer lives only on the local disk until
  the run ends. `swarm.push_progress = false` turns it off. New log event
  `swarm.progress_push`. Prompt bytes: none change.

- A permission question that ended without an answer (no page had the web
  interface open for `--ask-grace`, the input of a line prompter ended) is
  refused with the same advice as one that passed `--ask-timeout`: the tool
  result reads `approval required: REASON (no answer, so nothing was
  approved: do not ask for it again; ...)` instead of `no answer`. Both
  refusals now tell the model not to ask for the same thing again. `free`
  and `uptime` join the read-only list. Prompt bytes: none change.

- The permission engine's read-only list takes in the commands agents use
  to read text and JSON and to look at the machine: `awk` and `jq` when
  their program only selects and prints (no `system`, `getline`, redirected
  `print`, `ENVIRON`, `ARGV`, gawk's `@`; no jq `env`, `$ENV`, modules or
  `-f`), `egrep`, `fgrep`, `column`, `comm`, `join`, `expand`, `unexpand`,
  `od`, `hexdump`, `xxd`, `strings`, `base64`, `ps` (not the forms that
  print each process's environment) and `pgrep`, reads of `git config`
  (`git config user.name`, `--get`, and `--list` with `--local`), `npm ls`
  and a bare `npm run`. They no longer ask in default mode and are no
  longer refused to read-only roles and planning pool members. Read-only
  roles also run `npm test` with arguments (`npm test -- --run`), `pnpm
  test`, `yarn test` and `cargo clippy` without `--fix`. In a probe of 250
  commands agents commonly run, these were the read-only ones the engine
  refused. Prompt bytes: none change.

- A standing goal can name a spec file (`Spec: PATH`) and list milestones
  in a `Milestones` section of the goal or the spec. The spec is read once
  when the goal is set and kept with it; the judge rules on every milestone
  and a `done` verdict that does not confirm each one with evidence is read
  as `continue`. The verdict JSON gains an optional `milestones` array; the
  `goal.state` record and the web `goal` event gain `Spec`, `SpecText` and
  `Milestones` (`spec` and `milestones` on the page). Prompt bytes: a goal
  without milestones or a spec sends the agent and the judge the same bytes
  as before; with them, the judge's user message carries the spec and a
  milestones section, and the goal's first message the spec after its note.

- The manager's `spawn` of an existing task while every member of its role
  is busy is queued instead of refused: the harness starts it on the first
  member of the role that is free and tells the manager. The refusal it
  replaces named one busy pool ("busy on T2") where several were busy;
  it now names each with its leader. In a 13-worker run a manager spawned
  the same backend parent seventeen times in fifty minutes against it.
  Prompt bytes: none change.

- `npx --no-install X` and `npx --no X` are read as `X` by the permission
  engine (npx installs nothing then: it runs the project's own program or
  fails), so a read-only reviewer can type-check with the project's
  TypeScript (`npx --no-install tsc --noEmit`) under the `tsc --noEmit`
  rule. A plain `npx`, which installs a package it does not find (`npx tsc`
  without TypeScript installs an unrelated `tsc`), stays the program, and a
  read-only role refused one is told the `--no-install` form. Prompt bytes:
  none change.

- A child of a pool whose parent is todo (after a resume the parent waits,
  reserved for its leader, until it is started again) is refused with how to
  start the parent first (`spawn task=T6 agent=dv-1`). In a resumed team run
  the manager spawned the child five times against "parent pool T6 is todo".
  Prompt bytes: none change.

- The permission engine binds a loop variable whose words are quoted plain
  words (`for size in "390,844" "1440,900"`) to what is inside the quotes,
  and judges a command that uses the variables of nested loops once for each
  combination up to 64 (was 32). A frontend worker's screenshot loop over 9
  pages, 2 sizes and 2 themes into `$TMPDIR` asked in bypass mode because
  its file names were unknown; it is now allowed. Quoted words with a glob,
  a variable or a leading `~` stay unbound. Prompt bytes: none change.

- The web interface writes token counts as the terminal does: "1.2k",
  "3.4M", "1.2B", with one decimal above 999 (it stopped at k: an agent with
  three million tokens showed "3000k"). Its footer shows the tokens of every
  agent of the session instead of the manager's. Prompt bytes: none change.

- A pool's leader may set the scope of a child of its pool that nobody has
  claimed (`task update id=… files=[…]`, inside the pool's scope), and a
  claim refused because a child overlaps a sibling says that the leader can
  narrow it. Before, only the manager could change a scope: a frontend
  leader whose two children both had the pool's scope could not part them,
  and its members' claims were refused for half an hour. Prompt bytes: the
  task tool's refusal texts change; no prompt layer does.

- A tool call named `tool.action` (`task.get`, `pool.agree`, also behind a
  `functions.` namespace) runs as that tool with the action in its input,
  when the tool's schema has that action and the call's input names no
  other one; the event log's `tool.call` says which tool ran (`as`).
  Before, it was refused as an unknown tool. Prompt bytes: none change.

- The manager may excuse a pool member whose runs are failing (one the
  harness is restarting after a failed run, or whose restarted run is under
  way) even while it seems to make progress. A model that loops on `go test`
  and `ls` counted as working, and a pool cannot agree a plan without every
  member, so a broken member held its pool in planning. A `task` or `pool`
  call without an action is now told so and given the tool's actions,
  instead of `unknown action ""`. Prompt bytes: none change.

- A team worker whose run fails while it holds work (a task in doing, the
  pool it leads, or its place in an open pool) is restarted on that work by
  the harness: after 5 seconds, then 10, at most twice in a row on the same
  work, with harness mail that says why the last run stopped and what to do
  differently (after the step limit: the run has a fresh step allowance,
  make the next concrete change or record where the task stands). The task
  stays assigned to the worker with status `doing`, and its failed attempt
  is counted in place (`board.op` `requeue` with the status unchanged).
  Without persistent pools, the harness restarts a worker on a task only
  while the task still leaves the manager an attempt for another worker.
  The manager is told once per stop that the worker is restarted and must
  not be replaced; past the bound the stop is settled as before, and the
  manager's line says how many restarts were made. While a restart waits
  the worker's state is `idle` with the line `restarting: <why>`, so the
  terminal and the web interface no longer show it as stuck; a worker whose
  failed run is not restarted still shows as failed, and one whose waiting
  restart is dropped because its work moved on to other hands or closed
  shows as `idle`. Budget stops, interrupts, unanswered questions,
  step-limit stops under `FixedSteps` and workers that hold nothing are not
  restarted; an interrupt or a spent swarm budget calls off a waiting
  restart. New `swarm.restart` events
  record each restart. Prompt bytes: none change.

- The compactor decides what to keep by what the work in progress depends
  on: its brief states a budget (half of the planner's soft limit for the
  agent's model), the priorities (the task's criteria, the files being
  edited, the material the output is built from, unresolved errors) and,
  for each foldable unit, its size, how much keeping from it keeps, the
  references of its large results and the reads made stale by a later edit
  or read of the same file. A patch may name results to `keep`, which stay
  verbatim where old bulky results were masked by age. The mechanical
  fallback keeps the same budget, at most half the thread, instead of a
  quarter of it. Prompt bytes: the compactor's brief changes; it is the
  appended instruction of a fork request, after every cached block, so no
  cached prefix changes.

- `sleipnir chat --compact-at N` and `sleipnir web --compact-at N` set the
  share of each model's context window, from 10 to 90 percent, at which a
  session's agents start compacting, over `cache.compact_at_percent` for
  that session; 0 or no flag leaves the configuration's. The New session
  dialog of the page has a slider for it (default: the configuration's
  share, else 25%), sends it only when it differs from that default, and
  shows it in its command line. A restart (`/restart`, `/swarm`, `/model`,
  `/new`, and the page's restart family) keeps the share the session was
  started with. Prompt bytes: none change.

- Compaction starts at a share of each model's context window instead of a
  fixed 20,000-token conversation: by default when the conversation fills
  25% of the window (`cache.compact_at_percent`, 10 to 90), forced halfway
  from there to 95%. On a model with a 1M-token window that is 250,000
  tokens instead of 20,000, so an agent keeps what it read for its current
  work in view through a long task; in a 12-worker run under the old limit,
  58% of the reads that followed a compaction re-read a file whose earlier
  read had just been folded away. Requests carry a larger context in
  exchange: on an automatic prefix cache the simulated cost of long tasks
  roughly doubles (docs/CACHE-ECONOMICS.md), while on an explicit cache,
  where each compaction re-writes the prefix, it stays about level. Token
  limits set without `compact_at_percent` (`thread_soft_limit_tokens`,
  `compact_threshold_tokens`) keep applying to every model, and a model
  whose window is not known (a model nothing describes, whose 200,000-token
  window is a placeholder) uses them. Prompt bytes: the stable layers do not
  change; the conversation stays verbatim for longer before it is folded, so
  requests past the former 20,000-token limit carry more of it.

- Every model a session calls takes its context window from what its
  provider reports, for models the built-in table knows as well: the
  endpoint's catalogue entry, or for a ChatGPT plan the plan's own model
  list (`gpt-6-luna`: 272k there, 200k in the table). The table keeps its
  prices. Where the provider reports no window, the table's stands. A
  provider's `options.context_window` now wins over the window its
  catalogue reports (before, the catalogue's replaced it for a model the
  table did not know). The plan's model list is kept for six hours in
  `<state>/cache/chatgpt-models-*.json` (no token), asked only with an
  access token that needs no renewal, and an older copy is used while it
  cannot be asked; `sleipnir models` and the Models page update it. A
  catalogue that cannot be read is asked once per session.
  `session.start` records `context_source` beside `source` for each model.
  Compaction starts at a different point for a model whose reported window
  differs from the table's. Prompt bytes: none change for a window of 64k
  tokens or more; below that, the shared layer's instruction allowance (a
  quarter of the main model's window, at most 16,384 tokens) follows the
  reported window.

- A model on a server of this machine takes the window the server serves it
  with instead of an assumed 8,192 tokens: vLLM's and SGLang's catalogue
  (`max_model_len`), llama.cpp's `/props` (`n_ctx` of a slot), the instance
  LM Studio has loaded (`/api/v1/models`), and Ollama's `num_ctx` for the
  model (`/api/show`, at most the model's own context length) or the
  context its running model was loaded with (`/api/ps`). Where the server
  does not say, 8,192 stands; `options.context_window` still wins. These
  questions are asked once per model in a session, for three seconds at
  most, without a key; `session.start` records `context_source: "server"`
  for a window learnt this way. A catalogue entry that gives a window and
  no prices now gives the window only: the model keeps its estimated prices
  instead of being priced at zero. Compaction on such a model starts at its
  share of the served window, so a local model served with 32k compacts at
  8k instead of at about 4k. Prompt bytes: the shared layer's instruction
  allowance (a quarter of the main model's window) follows the served
  window for a local main model.

- A background process that a shell command started and left running with
  its output sent elsewhere (`nohup server >log 2>&1 </dev/null &`) now ends
  when the session does, as one that holds the command's output already
  did. Within the session it keeps running for later commands. Before, it
  outlived the session. Prompt bytes: none change.

- A team manager's shell command that sleeps a minute or more (`sleep 1800;
  date`) is a wait: it is refused, with the reason a `wait` gets, when no
  worker runs and work needs the manager or every task is settled, and the
  stall sweep counts the manager as waiting while it runs. Shorter sleeps,
  and sleeps in loops or the background, are unchanged. Prompt bytes: none
  change.

- The session's `compactor` model is benched when 5 of its last 8 patches
  cannot be used (JSON that does not parse, a tool call in place of the
  patch, a patch that does not apply): every agent then compacts on its own
  model, reading its own cache, and the compactor is asked again every 10th
  compaction until 3 of those probes in a row are usable. Before, each such
  patch fell back to the mechanical compaction, which keeps no summary.
  Compactions while it is benched are booked at each agent's model price.
  The `compact.patch` request event says which model was asked (`model`,
  `compactor`), and a `compactor` stage records each change. Prompt bytes:
  none change.

- Read-only roles (reviewer, scout, architect) run the project's static
  checks as well as its tests: `node --check`, and on the tests preset's
  terms (every file inside the workspace, no `--fix`, `--install-types`,
  `--build` or other option that writes the project's files or installs)
  `ruff check`, `ruff format --check|--diff`, `mypy`, `pyright`,
  `tsc --noEmit` and `eslint`. They still run only what the session allows:
  in `default` mode these still ask. For every role, the tests preset no
  longer covers a checker that writes a report or build information
  (`eslint -o`, `eslint --suppress-all|--suppress-rule|--prune-suppressions`,
  which rewrite the project's suppressions file, `ruff check
  -o|--output-file|--config` (a setting can turn the fixes on), `mypy
  --junit-xml` and its
  other report options, `tsc --incremental|--tsBuildInfoFile`); a one-letter
  option counts attached and in a cluster (`-qo out.txt`). Such a command is
  judged as if the preset did not exist, so it asks where the mode asks.
  Prompt bytes: none change.

- A team manager's `wait` is refused when no worker is running and every task
  of the board is settled, as it already was with unfinished work and nobody
  running: the error says to start what the objective still needs or to end
  the turn. A wait that began while a worker ran ends that way when the last
  worker goes idle. An empty board, a docs pass about to start, and mail still
  with the mailman still allow the wait. `wait`'s `until` and `task`'s `deps`
  take one string of task IDs as well as a list. A restored worker shows
  `restored; waiting for T7 to restart` only while it holds an unfinished task.
  A standing goal's continuation to a team manager names the free workers.
  Prompt bytes: the tool schemas and stable layers do not change; the
  continuation message, which joins the conversation's tail, gains a line.

- A file whose name is only known when the command runs (`cat "$F"`, `cat
  < "$F"`, `go test "$PKG"`, a file tool given `$F`) is refused in `plan`,
  `default` and `accept-edits` mode when a `deny` rule may cover it (any
  `Read` rule for a read, any `Edit` rule for a write), as `bypass` already
  refused it: it is no longer asked about, and an `allow` rule (the tests
  preset's `go test`) no longer lets it through. Without such a rule these
  modes still ask. `yolo` is unchanged. Prompt bytes: none change.

- A `sleipnir web` session ends after 24 hours without use instead of 24
  hours after sign-in: a request in the second half of that time moves its
  end a full 24 hours ahead and sends the cookie again, and an open page asks
  the server every half hour, so a page watching a long run is no longer
  signed out of it. Prompt bytes: none change.

- The manager may not excuse a pool member that is running and made
  progress (an edit, a test, a task update) within the stall check's bound
  of model turns; the refusal names it and says to ask it by mail. A member
  that has stopped, made no progress at all, or gone the bound without
  progress (a loop of reads included) may still be excused. In a persistent
  pool the stall check's nudge no longer offers a `task handover`, which such
  pools refuse: it says to state why on the task and block it. Prompt bytes:
  none change (the nudge and the refusal are mail and tool results).

- Pool refusals name the condition that failed and the call that would
  succeed. An `agree` or `confirm` is refused for one stated cause: no
  revision (`pool agree needs revision: T6 is at revision 2 (planning)`), a
  stale or unknown revision, nothing proposed yet, the pool already
  executing (`no vote needed`), or the wrong phase for the action (with
  the call that phase takes). A vote without `revision` counts for the
  revision the member last read in full (`task get`, `pool inspect`, or the
  proposal or reconciliation text it wrote) while that revision and its
  agreement are current, once that read's result has reached the model (a
  read in the vote's own batch of calls does not count). A refused `pool
  execute` names the members still to agree. A refused `pool reconcile` or parent `task done` names each
  unfinished child with its status and owner, or the members whose
  confirmation is missing. The leader is no longer mailed each vote: the
  vote that completes a round tells it once what follows. `pool get` reads
  a task as `task get` does; another unknown action lists pool's actions.
  `pool inspect` and a reconciliation without text answer with the pool's
  task in full, and a proposal or reconciliation text with the revision it
  made. `pool inspect` outside reconciliation and a proposal after planning
  say what to use instead. The `task` and `spawn` refusals state the rule
  (the manager's, except that a pool leader creates, accepts and assigns
  the children of the pool it leads while it executes) and the condition
  that failed; an `accept` of an unknown id says there is no such task.
  Messages that offered a leader to reject a child now say to ask the
  manager. A child created without a role takes its parent's, and a child
  stays claimable while its parent is blocked on it. Prompt bytes: none
  change; tool descriptions and schemas are unchanged, and the refusals and
  notices are thread text.

- A revise of a pool child concerns the child: the pool's leader and the
  child's owner are told, not every member. The parent keeps its
  assignment (its rev) while its leader works on it, so the leader's run is
  not told its task changed; the leader's own child is revised without
  stopping the leader, which claims it again. A pool that was reconciling,
  or whose result was submitted or accepted, still executes again with a new
  revision and no confirmation left; a pool that plans or executes keeps its
  revision and votes, and one back in planning is no longer moved to
  executing before its members agree. The manager is no longer asked to resume a pool
  leader whose run its own revise (or a shutdown) stopped; a leader's
  failure still reaches it. Prompt bytes: the child-revise notice is new
  harness mail to two agents instead of the pool-wide "Child T was
  revised" mail; nothing cached changes.

- `task accept` refuses an implementation task whose recorded harness
  evidence says no file was edited (in an isolated run, unless its merge
  carried a change): it reached review without work, often submitted by the
  gate for a worker that stopped without `done`. A task with no recorded
  evidence (submitted on the board directly by an embedder) is not judged.
  The refusal names the evidence; the manager rejects the task, or accepts
  it with text `nothing to change: <why>`. Planning tasks, pool results,
  documentation, read-only roles' tasks and submissions with an agreement
  are accepted as before. A task that the harness requeues, or that the
  manager revises or reopens, no longer keeps the result and evidence of an
  earlier submission on the board (the event log keeps them); a failed
  verification gate still records its own. Evidence is recorded per task:
  an edit counts for the task its agent is working on, or for the one other
  task it is doing whose scope holds the file, so a task's submission lists
  the files edited for it, not for the agent's other or earlier tasks.
  Prompt bytes: the live view and `task get` of a requeued or revised task
  no longer show the stale result and evidence; nothing cached changes.

- Writer scopes overlap only when some path could lie in both: `**` is
  aligned as matching aligns it, instead of colliding with everything, so
  `**/*.md` overlaps `docs/**` and `web/*` but not `web/*.ts` or
  `**/*_test.go`, and two `*`-and-suffix globs (`*.md`, `*.go`) are
  disjoint unless one suffix ends the other. A path without a glob that
  names a regular file in the shared tree covers that file alone, so
  `**/*.md` and the file `web/package.json` no longer collide; a directory,
  or a path that does not exist yet, still covers what is below it. A pool
  child's scope lies inside its parent's when every path it names does:
  `README.md`, `*.md` and `docs/*.md` lie inside `**/*.md`, `src/a.go`
  inside the directory `src`; a glob is compared segment by segment, and
  one only several parent entries cover together stays outside. A child
  writes only what its parent's scope also holds: the lease guard and, in
  an isolated run, the merge queue refuse a change below a child's path
  that lies outside the parent's scope. The refusal names the parent and
  its scope, and an overlap refusal for a task the spawn would create says
  "this task" instead of an empty id. Prompt bytes: none change; the
  refusals are thread text.

- Mail that carries content (`info`, `answer`, `contract`) may have 1,200
  characters; mail that asks for something (`request`, `blocker`) keeps 600
  (`RouterConfig.MaxContentChars` and `MaxChars`; a configuration without a
  content limit holds every kind to `MaxChars`). A message over its kind's
  limit is refused with its length, how far over the limit it is, a target
  below the limit, and where long content lasts: a file in the sender's task
  scope, or the agreement submitted with `task done`, read with `task get`.
  In mailman mode a `blocker` to the manager is delivered at once and never
  digested; the mailman is asked only for a recipient with at least three
  parcels in a batch (two or fewer are delivered directly); it is shown each
  parcel whole, within 16,000 characters of parcel text per request, instead
  of its first 240 characters; and a burst's quiet period is counted from its
  last parcel, so the supervisor's housekeeping no longer holds every burst
  for the full 6-second maximum. Prompt bytes: no stable layer changes (the
  mail tool's description states no limit, and the mailman's pin is
  unchanged); the refusal is new tool-result text, a content message can put
  up to 1,200 characters into its recipient's thread, and the batch in the
  mailman's thread carries whole parcels.

- Every teammate is in every agent's live view. The team section listed
  the first 30 teammates in roster order and dropped the rest silently, so
  in a team of more than 30 the last workers were shown to nobody, the
  manager included. Teammates are now ranked by relevance before the cap (a
  worker's pool mates, the owners of related tasks and the manager first,
  then its role; for the manager, workers waiting on it, then those at
  work), and the manager's view lists up to 64. Teammates without a line
  of their own (beyond the cap, or dropped for the budget) are counted on a
  final `+N not shown: …` line instead of in "(+N more lines omitted)",
  which names the most relevant of them: at most 64, fewer when the budget
  needs it, the rest counted (`and 6 more`). The omission note is
  included in the budget too. Prompt bytes: the live view's team section changes
  (order of dropping, the new line); it is per-request text, so nothing
  cached changes.

- The stall sweep no longer takes a shell command that only reads for
  progress. A worker that reads code with `cat`, `sed -n`, `grep` or `find`
  (chained with `cd`, `echo` banners, pipes into `sort` or `head`,
  `2>/dev/null`) is reading, as it is with the read tool, and is found as
  `claimed_no_progress` after `StallTurns` turns. A line counts as reading
  when every command in it is on the permission engine's read-only allowlist
  and no redirection writes a file (`perm.ReadsOnly`). Edits, task updates,
  test and check runs (`go test`, `npm test`, `pytest`, `go vet`) and any
  command that may change files still restart the count. Prompt bytes: the
  stall nudge and the finding's detail say "without editing a file, running
  a test or a command that changes files, or updating the task" and that
  reading is not progress; nothing cached changes.

- What a new shell does stays in that shell: an assignment, a `source`, a
  `read` or an arithmetic expansion inside `sh -c`, `bash -c`, `su -c` or a
  script fed to a shell no longer makes the line's own variables, or its
  `$HOME`, `$TMPDIR` and `$PWD`, unknown. `T=$(mktemp -d); sh -c '. ./x.sh';
  rm -rf "$T"` is allowed in bypass mode. Inside the new shell its own
  names stay unknown, and so do the harness's names the line assigns
  (an exported one reaches it); `eval` and `.` still count (they run in
  this shell), and a loop around a new shell still makes what the loop
  assigns unknown to it. Prompt bytes: none change.

- A step limit is a window, not a ceiling, for team workers and for the
  single agent of `sleipnir run`, the terminal and the web interface. A run
  that made at least 3 new edits in its last window (an edit it repeats does
  not count) goes on for another window of the same length, with a notice.
  A run that did not is told in the results of its last step how many steps
  are left (a tenth of the window, 2 to 10) and to leave the tree building
  and hand its task over (a worker; in a persistent pool, which keeps its
  workers, record where the task stands with `task update`) or give its
  answer (a single agent);
  then it ends at the limit as before, recorded as `agent.stuck` with phase
  `steps`. Before, a worker was stopped mid-edit with nothing said to its
  successor, and the stop counted as a failed attempt. The manager, the
  mailman, RL rollouts and `sleipnir run --max-steps N` keep a fixed
  ceiling. Prompt bytes: a team manager's pin says `N steps a task, more
  while it keeps editing` for each role instead of `max N steps a task`
  (unchanged with a fixed ceiling); the wrap-up note is new thread text.

- A member of a role pool may keep a memory note while its pool plans,
  reconciles or waits for a subtask: the note goes to the session, which
  puts it to the person with the exact text, as it does for any agent. It
  was refused by the pool's read-only check, and the log showed a question
  "decided by no one" that nobody had been asked. That check (and any
  `PlanOnly` engine) is now a silent probe: it asks no one, delegates
  nothing to the manager, records nothing in the audit and keeps no rule.
  Prompt bytes: none change.

- A compaction patch whose JSON broke on a quote the model did not escape
  inside a string (`"line": "fixed the "foo" handler"`) is read with that
  quote as text instead of refused, with a warning; the agent keeps the
  model's own digest of its work instead of folding mechanically. Replayed
  over the 348 compactor replies of a 32-worker run, 156 patches are used
  instead of 116. A missing comma (`"a" "b"`) and JSON broken any other way
  are still refused, at most 32 quotes are repaired per patch, and every
  attempt counts against the parser's bound on hostile replies. Prompt
  bytes: none change.

- A command that prints one name token is a piece of a file name, as `$$`
  is: `date` with a `+FORMAT` of numeric conversions and plain characters
  (`+%Y%m%dT%H%M%SZ`, `+%s`; not `%Z`, the zone name `TZ` sets), `seq` over
  numbers, `id -u`/`-g`, `nproc` and
  `uuidgen`. `"$D/rw.$(date +%s).tar.gz"`, a variable set to such a
  substitution, and the variable of `for i in $(seq 1 800)` name a file in
  `$D`. The output is also judged empty, as a failed command leaves it, so
  `rm -rf "$HOME/$ts"` is still refused. And a `cd` to a directory only
  known when the line runs (`cd "$(git rev-parse --show-toplevel || pwd)"`)
  makes relative paths unknown, not full ones: `"$TMPDIR/x"` and a variable
  set to it still name that path. Prompt bytes: none change.

- A `$` that starts no expansion (`$|` in a regex, a trailing `$`) is the
  character, as it is in bash: `pgrep -af "$INSTALL/raven($| )"` names a
  known path rather than one with a part only known when the command runs,
  which `bypass` asked about as a write that might leave `$TMPDIR` for
  `~/.sleipnir`. Prompt bytes: none change.

- An arithmetic expansion no longer makes every variable of the line
  unknown. Bash arithmetic assigns only integers, and only to the names it
  reaches: those its text names, and those named by the values of the
  variables it reads. When every name an expansion reads holds an integer
  on the line (a number, another expansion or `$(date +%s)`, assigned
  unconditionally before it), only the names of its text become unknown, so
  a counter (`n=$((n+1))`) or a timer (`$(( $(date +%s) - S0 ))`) leaves the
  scratch directory the line deletes known. A name that may hold anything
  (`x='D=0'`, `read`, the environment, `$1`), a substitution or `${...}`
  inside the expression, or an expansion the parser places on no command
  (a `case` subject) still makes every variable unknown. A loop's variable
  is now also bound in the redirections of its body (`> "$DD/$ts"`).
  Prompt bytes: none change.

- A loop's assignments reach its whole body: a loop runs its commands again,
  so what any of them assigns may be there before the first one runs.
  `X="$TMPDIR/a"; while :; do rm -rf "$X"/*; X=/; done` was allowed and
  deletes `/*` the second time round; the names a `while`, `until`, `for` or
  `select` loop assigns are now unknown from its start. A builtin that may
  set a variable makes only the names it can set unknown, not every
  variable of the line: a loop's variable, the names given to `read`,
  `unset`, `getopts`, `mapfile`, `wait -p` and `printf -v`, and nothing for a
  plain `wait`. `eval`, `source`, `let` and a name only known when the line
  runs still make every variable unknown. A line of more commands than the
  engine judges (2000) is no longer allowed in `bypass` or `yolo`: the
  commands past that bound were never judged, so `bypass` asks and `yolo`
  refuses, as other modes already asked. The callback of `mapfile -C` (and
  `readarray -C`) is judged as the shell code it is, with the line it read
  as an unknown last word; it used to be no command at all, so
  `mapfile -C 'cat ~/.ssh/id_rsa;:' -c 1 x < f` was allowed in `bypass`. A
  callback may set any variable. Prompt bytes: none change.

- A `for` (or `select`) loop with no word list is understood. Written
  `for X do ...; done`, with no `;` before the `do`, its body used to be read
  as more words of the loop, so the commands in it were not judged at all:
  `for X do cat ~/.ssh/id_rsa; done` was allowed. And `for X; do` (or
  `for X do`) sets X from the positional parameters, which a line can set
  (`set -- $(echo /)`); X is now unknown after it, so a later
  `rm -rf "$X"/*` asks. Prompt bytes: none change.

- A heredoc that is a program's data (`python3 - <<'PY'`, a quoted
  delimiter, read by a program named in plain words that is no shell and no
  builtin such as `read` or `source`) no longer counts as text that may
  change the line's variables: `os.environ["TMPDIR"]` in a Python script does
  not make `$TMPDIR` unknown, and `o["k"]=1` is not taken for an array
  assignment. A line that named a scratch directory and then fed Python a
  script was asked about deleting it in `bypass`. A body a shell runs, a
  builtin reads or the shell expands still counts. Prompt bytes: none change.

- A `case` statement is parsed: the commands of its bodies are judged as any
  other (as conditional ones), and the line keeps the variables it set. It
  used to make the whole line unparsed, so `T="$TMPDIR/t"; rm -rf "$T"` was
  asked about when a `case` followed it, even in `bypass`. An assignment or
  `unset` inside a body makes that variable unknown after the `case`. A
  malformed `case` (no `in`, no `esac`, a body that leaves a group open) is
  still unparsed. A `&` after any compound command (`fi`, `done`, `}`, `)`,
  `esac`) marks the whole list as run in the background, the commands before
  the compound included, and a `|` after one the whole compound; a `for`
  header after `&&` or `|` is conditional. A loop that follows `&&`, `||` or
  `|` and continues on the next line (`true && for f in x`, then `do rm -rf
  ~; done`) has its body read as commands; its next line used to be read as
  words of the loop's list, so `bypass` allowed the delete. An empty `case`
  after `&&` is parsed. Prompt bytes: none change.

- `bypass` asks only about the very dangerous: the high-risk class (`sudo`, a
  recursive delete of a broad or unknown path, a forced push to a shared
  branch, disk tools, shutdown) and a write into `.sleipnir/`, `.claude/` or
  git hooks and config. A file whose name is only known when the command runs
  is allowed, or refused when a `deny` rule may cover it (any
  `Read` rule for a read, any `Edit` rule for a write); it no longer asks. A
  read-only role in a `bypass` session reads such a name the same way. A word
  that only might be a file (an unlisted program's operand, `node script.mjs
  .`) is judged as the path it spells, not as a tree an `ask` rule overlaps.
  `go run` is understood: it reads and builds its package and passes the rest
  to the program. `$(mktemp ...)` names a new path in `$TMPDIR` (and, as a substitution that
  can fail, is also judged empty: `rm -rf "$D"/*` is not allowed). A command run
  in the background (`server &`) no longer makes every variable of the line
  unknown; only what it assigns itself is. Prompt bytes: none change.

- The variables a command line sets stay known on what the shell parser
  found, not on the line's text: quoted text or a here-document body that
  holds `()`, the keyword `function`, `((`, braces or keywords
  (`printf 'func main() {...}' > main.go`, Go source written with
  `cat <<'EOF'`) no longer makes them unknown, so a later `rm -rf "$d"` of a
  scratch directory the line named is judged as that directory. A line the
  parser does not parse (a function definition among others), an arithmetic
  expansion (`$((n=1))`, in a word, double quotes or an unquoted
  here-document), and a statement the parser marks conditional (in a
  compound command, a group or a subshell, after `&&` or `||`, in a
  pipeline, in the background) still stop or skip the tracking. Two command
  substitutions are known values: `$(pwd)` is the directory the command runs
  in, and `$(git rev-parse --show-toplevel)` the nearest directory at or
  above it that holds a `.git`, so `cd "$(git rev-parse --show-toplevel)" &&
  go test ./...` is judged in that directory and its paths against the rules;
  any other substitution, a `.git` that is not a repository, or a line that
  names a `GIT_` variable, stays unknown. A substitution that can fail is
  empty when it does, so a word built from it, directly or through a
  variable, is judged that way as well (`"$d/proc/self/environ"` reaches the
  protected file, and `"$D"/*` the root). Prompt bytes: none change.

- A compactor model of its own (`--role-model compactor=M`,
  `models.roles.compactor`) is asked by its own name. Before, the compaction
  request kept the agent's model name, so an agent on one provider with a
  compactor on another sent its model to the compactor's endpoint (every
  compaction of a ChatGPT-plan agent with a Heimdallm compactor failed with
  404 and fell back to mechanical folding), and agents on the compactor's own
  provider were compacted by their own model. Prompt bytes: the compactor
  request names the compactor's model; the agents' prompts do not change.

- A role works several parents side by side. Assigning a role a parent while
  another of its parents is open no longer fails with "wait before assigning
  another parent" whenever the role has a pool: the members an executing
  parent can spare (not its leader, no open subtask of it) leave its pool and
  form the new parent's, and its leader is told. A parent still agreeing on its
  plan, or one whose members are all busy, refuses with what to do instead.

- `sleipnir web` takes a goal of up to 32,000 characters (was 4,000), in the
  New session dialog and as `/goal TEXT` (through the goal route or as a slash
  command); the routes that carry one accept a body large enough for it. An over-long goal is refused with the code
  `bad_goal` (was `empty`). The page refuses it before sending, a `/goal` line
  the server refuses comes back into the composer as a refused message does,
  and the Goal sheet opens only once the server has taken the goal: before, a
  refused goal emptied the composer while the sheet said it was starting. The
  page reads the limit from `hello.limits.maxGoal`. Prompt bytes: none change.

- A prefix rule for a build or test runner (`Bash(go test:*)`, the tests
  preset, the read-only roles' rules, or a rule of the person's) no longer
  covers an option that makes the runner run a program of the caller's
  choosing, for example `go test -exec PROG`, `-toolexec`, `-vettool`, `-ldflags` naming
  `-extld`/`-extar`, `cargo --config` and `-Z`, `npm`/`pnpm`/`yarn
  --script-shell` and `--node-options`, `node --require`/`--import`/loaders,
  `make SHELL=`, `-f`, `--eval`, `gradle --init-script` (the full list is in
  docs/CONFIGURATION.md). Such a command is judged as if the rule did not
  exist: it asks, or a read-only role is refused; `Bash(go test:*)` does not
  cover `go test -exec='rm -rf x' ./...`. Prompt bytes: none change.

- The manager and the architect give a user interface (pages, components,
  styling, client code, a service's web interface) to the frontend role when
  the team has one, with the API it calls as a backend task of its own, and
  the unit tests beside code to that code's implementer; the tester works
  black-box in test directories of its own, so its scope does not reach into
  packages the implementers lease. Prompt bytes: the manager, architect and
  tester role layers change, so the first request of each after the upgrade
  writes them anew.

- In a `bypass` or `yolo` session, a read-only role (architect, reviewer,
  scout) reads a file outside the workspace as the session does, without a
  question: its plan profile keeps it from writing, not from reading the Go
  toolchain's source or a module cache. Before, every such read asked the
  person, even in bypass mode.
  Writes outside the workspace stay refused; the built-in protections and the
  deny and ask rules still apply; in any other mode the read still asks.
  Prompt bytes: none change.

- A pool member that is planning or reconciling keeps the research and checks
  of a read-only role: it may fetch and search the web (an API's
  documentation, a package registry's versions) and run the project's tests
  and static checks, and the session's own permissions still decide. Before, a
  planning pool was judged in plan mode with no allow rule at all, so an
  architect pool could not look up the current version of a library it was
  choosing. Writes stay refused until the member's child task. Prompt bytes:
  none change.

- A request to build something is a request for a product: production quality
  is implied and never has to be asked for. The constitution has a new section,
  "Build to ship" (complete workflows with their unhappy paths, secure by
  default with a threat model, a professionally designed interface on mature
  maintained libraries, tested at realistic scale, every screen looked at in a
  real browser), and a new project starts on a current, well-maintained stack
  instead of a minimal layout. Inside an existing project its stack and
  conventions still win and changes stay focused; a request for something
  simple, minimal or a prototype gets that. The manager plans security,
  packaging, documentation, scale tests and a visual design pass as tasks of
  their own when the goal builds a product, works through review findings by
  severity and reports a round of only minor polish as remaining instead of
  starting another; the frontend sets up a typed stack and a design system and
  screenshots every screen; the reviewer judges the result as a shipped
  product, says bluntly when an interface looks dated, and checks security as
  an attacker; the tester covers security, scale and browser end-to-end tests;
  the architect chooses the stack and writes a threat model and design system
  into the agreement; devops ships release builds, CI and a vulnerability
  scan. Prompt bytes: the constitution (solo and team) and the manager,
  frontend, tester, reviewer, architect and devops role layers change, so the
  first request of each after the upgrade writes those layers anew.

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
