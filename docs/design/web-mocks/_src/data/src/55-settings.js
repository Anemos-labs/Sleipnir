/* ---------------------------------------------------------------------------------------------------------------
 * permissions, trust, mcp, skills, commands, hooks, schedule, shortcuts. Wording of modes and rules is REAL (docs/
 * CONFIGURATION.md section 8, internal/perm); which rule came from where is SAMPLE but follows the real layering.
 * ------------------------------------------------------------------------------------------------------------- */
S.permissions = {
  mode: 'default',
  modes: [
    { id: 'default',      cycle: true,  danger: false, text: 'Reads inside the workspace and read-only shell commands are allowed; everything else asks' },
    { id: 'accept-edits', cycle: true,  danger: false, text: 'Also writes inside the workspace (file tools, redirections, mkdir, touch, cp, mv, rm, rmdir, tee on workspace paths) and the commands that build and test a project (go test, npm test, cargo build, pytest, make test… the --allow tests list); ask and deny rules still win' },
    { id: 'plan',         cycle: true,  danger: false, text: 'Read-only. Writes, network access and commands that are not provably read-only are refused with a message that says to present a plan. An allow rule still carves an exception (say Edit(docs/plan.md))' },
    { id: 'bypass',       cycle: false, danger: true,  text: 'Full control without asking, except about the very dangerous: the high-risk class (sudo, a recursive delete of the workspace, home or /, a forced push to a shared branch, disk tools, shutdown) still asks. Hard denies, deny rules, guarded paths without an allow rule and ask rules still apply', confirm: 'type bypass to confirm' },
    { id: 'yolo',         cycle: false, danger: true,  text: 'Never asks anything, for runs with nobody there: what bypass allows, and the high-risk class too. Hard denies, deny rules and guarded paths still refuse, and so does a rule that would have asked. For sandboxes. --continue never brings bypass or yolo back', confirm: 'type yolo to confirm' },
  ],
  cycle: ['default', 'accept-edits', 'plan'],
  cycleNote: 'shift+tab steps default → accept-edits → plan → default and never enters bypass or yolo (REAL: docs/CLI.md); bypass and yolo are set only by /mode bypass | /mode yolo or --mode',
  order: ['hard denies (built-in protections and your deny rules)', 'your ask rules', 'high-risk shell commands', 'your allow rules', 'the mode\'s defaults'],
  managerWrites: { refused: true, text: S.managerRefusal, note: 'the manager edits no file in any mode: its writes and writing shell commands are refused at run time' },
  roleOverlays: { reviewer: 'read-only (plan-mode profile)', scout: 'read-only (plan-mode profile)', manager: 'no writes' },
  rules: {
    allow: [
      { rule: 'Bash(git status:*)', origin: 'user config', file: '~/.sleipnir/config.json' },
      { rule: 'Bash(git diff:*)',   origin: 'user config', file: '~/.sleipnir/config.json' },
      { rule: 'Bash(git log:*)',    origin: 'user config', file: '~/.sleipnir/config.json' },
      { rule: 'Bash(go test:*)',    origin: 'project config', file: '.sleipnir/config.json', note: 'applied because you trusted this project (security-sensitive key)' },
      { rule: 'Bash(go vet:*)',     origin: 'project config', file: '.sleipnir/config.json' },
      { rule: 'Bash(make test)',    origin: 'project config', file: '.sleipnir/config.json' },
      { rule: 'Bash(make lint)',    origin: 'project config', file: '.sleipnir/config.json' },
    ],
    deny: [
      { rule: 'Read(~/.ssh/**)',    origin: 'user config', file: '~/.sleipnir/config.json' },
      { rule: 'Read(./.env)',       origin: 'project config', file: '.sleipnir/config.json', note: 'a project can add to your deny list, never remove from it' },
      { rule: 'Read(./secrets/**)', origin: 'project config', file: '.sleipnir/config.json' },
    ],
    ask: [
      { rule: 'Edit(./.sleipnir/**)',   origin: 'built-in protection', why: 'a write into the config directory always asks, in every mode (bypass included); an unattended run refuses it' },
      { rule: 'Edit(./.claude/**)',     origin: 'built-in protection' },
      { rule: 'Edit(./.git/hooks/**)',  origin: 'built-in protection' },
      { rule: 'Edit(./.git/config)',    origin: 'built-in protection' },
      { rule: 'Edit(~/.sleipnir/**)',   origin: 'built-in protection' },
      { rule: 'Edit(~/.claude/**)',     origin: 'built-in protection' },
      { rule: 'Bash(git push:*)',       origin: 'project config', file: '.sleipnir/config.json' },
    ],
  },
  builtinHard: [   // REAL tiers (internal/perm/builtin.go): no mode and no allow rule lifts a hard protection
    { tier: 'hard', what: 'credential directories under $HOME: ~/.ssh, ~/.aws, ~/.gnupg, ~/.config/gcloud', why: 'hold credentials' },
    { tier: 'hard', what: 'system directories: /etc /usr /bin /sbin /boot /dev /lib* /sys /proc (writes)', why: 'never written by an agent' },
    { tier: 'hard', what: 'private keys, cloud credentials, .git internals', why: 'protected whatever the mode' },
    { tier: 'guarded', what: '.env files and token files: ~/.netrc, ~/.git-credentials, ~/.npmrc, ~/.pypirc, ~/.docker/config.json, ~/.kube/config, ~/.config/gh/hosts.yml, ~/.sleipnir/auth.json, ~/.sleipnir/chatgpt.json', why: 'denied unless a user allow rule names them explicitly' },
  ],
  testsPreset: {
    name: 'tests',
    summary: 'go, cargo, npm, pnpm, yarn, pytest, unittest, mvn, gradle, dotnet and make: test, build, check, lint and vet, and go mod init and tidy, never install or run',
    rules: ['Bash(go test:*)', 'Bash(go build:*)', 'Bash(go vet:*)', 'Bash(gofmt:*)', 'Bash(go mod init:*)', 'Bash(go mod tidy:*)',
      'Bash(cargo test:*)', 'Bash(cargo build:*)', 'Bash(cargo check:*)', 'Bash(cargo clippy:*)', 'Bash(cargo fmt:*)',
      'Bash(npm test:*)', 'Bash(npm run test:*)', 'Bash(npm run build:*)', 'Bash(npm run lint:*)', 'Bash(pnpm test:*)', 'Bash(yarn test:*)',
      'Bash(node --test:*)',
      'Bash(pytest:*)', 'Bash(python -m pytest:*)', 'Bash(python3 -m pytest:*)', 'Bash(python -m unittest:*)', 'Bash(python3 -m unittest:*)',
      'Bash(mvn test:*)', 'Bash(mvn -q test:*)', 'Bash(gradle test:*)', 'Bash(./gradlew test:*)',
      'Bash(dotnet test:*)', 'Bash(dotnet build:*)',
      'Bash(make test:*)', 'Bash(make check:*)', 'Bash(make build:*)', 'Bash(make lint:*)', 'Bash(ctest:*)'],
    source: 'internal/perm/presets.go TestsAllow (REAL), cmd/sleipnir/allow.go',
  },
  runnerPrefixes: ['go test', 'go build', 'go vet', 'npm test', 'npm run', 'pytest', 'python -m pytest', 'python3 -m pytest', 'python -m unittest', 'python3 -m unittest', 'node --test', 'yarn test', 'pnpm test', 'mvn test', 'gradle test', 'dotnet test', 'dotnet build', 'cargo test', 'cargo build', 'cargo check', 'make', 'git add', 'git commit', 'git status'],
  runnerNote: '"don\'t ask again" for one of these remembers the prefix (Bash(go test:*)); for any other command it remembers the exact request (REAL: internal/perm/runner.go)',
  sessionRules: [],     // filled when the person answers "2" or /allow: {rule, list: 'allow', origin: 'don\'t ask again' | 'this session'}
  syntax: [
    { rule: 'Bash(go test:*)', matches: 'a shell command whose words start with go test (:* or a trailing " *" allows any further arguments)' },
    { rule: 'Bash(make lint)', matches: 'exactly make lint (make lint extra is not covered)' },
    { rule: 'Bash or Bash(*)', matches: 'every shell command' },
    { rule: 'Read(./.env), Read(~/.ssh/**)', matches: 'reads of those paths by any tool, the shell included (cat .env counts)' },
    { rule: 'Edit(docs/**)', matches: 'writes to matching paths, by the edit tools or by shell redirections' },
    { rule: 'WebFetch(domain:example.com)', matches: 'requests to that host and its subdomains' },
  ],
};

/* trust: the project's footprint and the ledger (trust.json: REAL format {version, projects: {dir: {digest, files{path: hash16}, saved}}}) */
S.trust = {
  project: { dir: '~/projects/shop', state: 'trusted', savedDay: '2026-01-01', digest: '', unlocks: 'what the project\'s own files may say to the harness' },
  files: [
    { path: '.mcp.json', kind: 'tool servers', bytes: 0, hash: '' },
    { path: '.sleipnir/config.json', kind: 'settings', bytes: 0, hash: '' },
    { path: 'AGENTS.md', kind: 'instructions', bytes: 0, hash: '' },
  ],
  covers: 'what the harness itself reads as text and settings: instruction files, .sleipnir/config.json and .mcp.json, skills, commands and agents; the repository\'s code is not covered, and running it (go test, make, a hook\'s script) is what every approval is about',
  ledger: [   // sorted by directory, as `trust list` prints them
    { dir: '~/projects/handbook',   saved: '2025-12-12', files: 2, now: 'changed: AGENTS.md changed', state: 'changed' },
    { dir: '~/projects/orders-api', saved: '2025-12-30', files: 2, now: 'unchanged', state: 'trusted' },
    { dir: '~/projects/old-prototype', saved: '2025-11-02', files: 1, now: 'directory is gone', state: 'gone' },
    { dir: '~/projects/shop',       saved: '2026-01-01', files: 3, now: 'unchanged', state: 'trusted' },
  ],
  commands: ['sleipnir trust', 'sleipnir trust add [--yes]', 'sleipnir trust forget [--all]', 'sleipnir trust list'],
  question: { title: 'use this project\'s own files?', options: ['Yes, use them this time', 'Yes, and remember them until they change', 'No, leave them out (esc)'], real: true },
};

/* MCP: four servers. REAL shapes (cmd/sleipnir/mcp.go printMCP / mcpList / mcpTest); tools are SAMPLE. */
S.mcp = {
  servers: [
    { name: 'docs', from: 'your config', origin: 'user config (~/.sleipnir/config.json)', state: 'ready', trusted: true, transport: 'stdio', command: 'docs-mcp', server: 'docs-mcp 2.4.1', tools: 14, toolNames: ['export_page', 'find_owner', 'get_attachment', 'get_comments', 'get_outline', 'get_page', 'get_page_history', 'list_labels', 'list_pages', 'list_spaces', 'pages_by_label', 'recent_changes', 'resolve_link', 'search_docs'], note: 'allow rule: mcp__docs__* (none yet: each call asks)' },
    { name: 'tracker', from: 'this project', origin: 'project .mcp.json', state: 'needs approval', trusted: false, approved: false, transport: 'stdio', command: 'tracker-mcp', args: ['--project', 'SHOP'], server: 'tracker-mcp 0.9.3', tools: 3, toolNames: ['get_issue', 'list_issues', 'search_issues'], describe: 'runs a local program: tracker-mcp "--project" "SHOP"', note: 'each project entry needs your approval; the approval ends when the entry changes' },
    { name: 'sentry', from: 'your config', origin: 'user config (~/.sleipnir/config.json)', state: 'failed', trusted: true, transport: 'stdio', command: 'sentry-mcp', args: ['--org', 'shop'], tools: 0, error: 'server "sentry": sentry-mcp: executable file not found in $PATH', canReconnect: true },
    { name: 'postgres', from: 'your config', origin: 'user config (~/.sleipnir/config.json)', state: 'disabled', trusted: true, transport: 'stdio', command: 'postgres-mcp', disabled: true, tools: 0 },
  ],
  sessionNote: 'the shop session started 2 servers (docs, and tracker once approved: `mcp 2 servers running` in the header); the tool list is frozen for the session: /mcp reconnect NAME restarts one that gave up',
  frozenTools: 17,
  prompts: [{ command: '/mcp__docs__summarise_page', description: 'Summarise a documentation page' }],
  permissionExamples: { allow: ['mcp__docs__*', 'mcp__tracker__get_*'], deny: ['mcp__tracker__delete_*'] },
};

S.skills = [   // listing format REAL: "  %-20s %s%s"
  { name: 'deploy',        summary: 'Run the deploy checklist. Use when the user asks to ship or release. after the tests pass', youOnly: false, source: '~/.sleipnir/skills/deploy/SKILL.md', scope: 'user', argumentHint: '[environment]' },
  { name: 'changelog',     summary: 'Write a CHANGELOG entry from the merged pull requests. Use after a release is tagged.',      youOnly: false, source: '.sleipnir/skills/changelog/SKILL.md', scope: 'project' },
  { name: 'release-notes', summary: 'Draft the public release notes from the changelog and the tags.',                             youOnly: true,  source: '~/.sleipnir/skills/release-notes/SKILL.md', scope: 'user', note: 'disable-model-invocation: true: only you can load it (/release-notes)' },
];
S.skillsBudget = { tokens: 1500, used: 96, note: 'the skills listing sits in the shared layer (G1); changing it costs one cache re-write' };
S.commands = [   // custom slash commands (commands/*.md); listing format REAL: "/%-17s %s"
  { name: 'fix-issue',   description: 'Fix a numbered issue', argumentHint: '[issue-number]', source: '~/.sleipnir/commands/fix-issue.md', scope: 'user', body: 'Fix issue #$1 in this repository.\n\nRecent history:\n!`git log --oneline -5`\n\nRead @docs/style.md first, then follow $ARGUMENTS.' },
  { name: 'review-diff', description: 'Review the diff of the current branch against main', argumentHint: '', source: '.sleipnir/commands/review-diff.md', scope: 'project' },
  { name: 'standup',     description: 'Summarise what the team merged since yesterday', argumentHint: '', source: '~/.sleipnir/commands/standup.md', scope: 'user' },
];
S.hooks = {
  events: ['SessionStart', 'UserPromptSubmit', 'PreToolUse', 'PermissionRequest', 'Notification', 'PostToolUse', 'PostToolUseFailure', 'Stop', 'SubagentStart', 'SubagentStop', 'PreCompact', 'PostCompact', 'SessionEnd'],
  configured: [
    { event: 'PreToolUse',  matcher: 'Bash',       command: '~/.sleipnir/hooks/block-dangerous.sh', timeout: 5,  origin: 'user config', purpose: 'refuse destructive commands' },
    { event: 'PostToolUse', matcher: 'Edit|Write', command: '~/.sleipnir/hooks/format.sh',          timeout: 10, origin: 'user config', purpose: 'gofmt the Go files the last edit touched' },
  ],
  trustNote: 'hooks from a project file run only with --trust-project; yours run first, so a repository cannot switch off your guard hook',
};

/* ---- schedule: `sleipnir schedule` (REAL columns), `daemon` ----------------------------------------------------- */
S.schedule = {
  jobs: [
    { id: 'j1', cron: '0 9 * * 1-5', goal: 'summarize yesterday\'s commits', dir: '~/projects/shop', model: '', mode: '', budgetUsd: 1, created: '2025-12-12T10:21:00', lastRun: '2026-01-01T09:00:00', lastExit: 'ok', log: '~/.sleipnir/schedule-logs/j1-20260101-090000.log', next: '2026-01-02T09:00:00' },
    { id: 'j2', cron: '0 18 * * 5', goal: 'run the full test suite and report the flaky tests', dir: '~/projects/shop', model: 'anthropic/claude-haiku-5-5', mode: 'accept-edits', budgetUsd: 2, created: '2025-12-12T10:24:00', lastRun: '2025-12-26T18:00:00', lastExit: 'ok', log: '~/.sleipnir/schedule-logs/j2-20251226-180000.log', next: '2026-01-02T18:00:00' },
    { id: 'j3', cron: '@weekly', goal: 'write the CHANGELOG entry for the week\'s merged work', dir: '~/projects/handbook', model: '', mode: 'accept-edits', budgetUsd: 0.5, created: '2025-12-14T08:02:00', lastRun: '2025-12-28T00:00:00', lastExit: 'timed out after 1h0m0s', log: '~/.sleipnir/schedule-logs/j3-20251228-000000.log', next: '2026-01-04T00:00:00' },
    { id: 'j4', cron: '30 * * * *', goal: 'check the build on main and say what broke', dir: '~/projects/shop', model: 'anthropic/claude-haiku-5-5', mode: '', budgetUsd: 0.25, created: '2025-12-20T21:40:00', lastRun: '2026-01-02T02:30:00', lastExit: 'ok', log: '~/.sleipnir/schedule-logs/j4-20260102-023000.log', next: '2026-01-02T03:30:00' },
  ],
  daemon: { running: true, pid: 20417, startedAt: '2025-12-12T10:30:00', every: '30s', jobsFile: '~/.sleipnir/schedule.json', logs: '~/.sleipnir/schedule-logs/', line: 'sleipnir daemon: looking for due jobs every 30s (ctrl-c stops it); jobs: ~/.sleipnir/schedule.json', timeout: '1h0m0s per run', once: 'sleipnir daemon --once starts what is due now, waits for it and exits (for cron or a systemd timer)' },
  logs: [   // the job's own output (`sleipnir run --quiet`: the final answer on stdout, the summary line on stderr)
    { job: 'j1', file: '~/.sleipnir/schedule-logs/j1-20260101-090000.log', exit: 'ok', text: 'Yesterday: 7 commits on main.\n- api: catalogue paging clamps instead of failing (be-1)\n- api: cart totals in whole cents (be-2)\n- web: item grid and pager (fe-1)\n- tests: table tests for the paging contract (ts-1)\nOpen: the cart has no HTTP endpoint yet.\n── 38s · 6 steps · $0.0412 · cache hit 86% · 0 compactions · ~/.sleipnir/sessions/20260101-090001-5c3e7a\n   no file was changed' },
    { job: 'j2', file: '~/.sleipnir/schedule-logs/j2-20251226-180000.log', exit: 'ok', text: 'go test -count=5 ./... ran 5 times: no flaky test found. TestRetry passed 5 of 5.\n── 1m42s · 9 steps · $0.0288 · cache hit 91% · 0 compactions · ~/.sleipnir/sessions/20251226-180001-a07d12\n   no file was changed' },
    { job: 'j3', file: '~/.sleipnir/schedule-logs/j3-20251228-000000.log', exit: 'timed out after 1h0m0s', text: 'sleipnir: interrupted\n(the run was ended by the daemon after one hour; work that had passed verification was applied first)' },
    { job: 'j4', file: '~/.sleipnir/schedule-logs/j4-20260102-023000.log', exit: 'ok', text: 'main builds and `go test ./...` passes (commit 5b2fee0).\n── 21s · 4 steps · $0.0107 · cache hit 90% · 0 compactions · ~/.sleipnir/sessions/20260102-023001-e19b40\n   no file was changed' },
    { job: 'j4', file: '~/.sleipnir/schedule-logs/j4-20260102-013000.log', exit: 'ok', text: 'main builds and `go test ./...` passes (commit 5b2fee0).\n── 19s · 4 steps · $0.0099 · cache hit 91% · 0 compactions · ~/.sleipnir/sessions/20260102-013001-7a02cc\n   no file was changed' },
    { job: 'j1', file: '~/.sleipnir/schedule-logs/j1-20251231-090000.log', exit: 'approval required: Bash(git log --since=yesterday) …', text: 'I could not read the history: git log with --since asks in the default mode (this run has no one to ask, so nothing can be approved: use an action that is allowed, or finish and say which permission you needed).\nGive the job --mode accept-edits or an allow rule for Bash(git log:*).\n── 12s · 2 steps · $0.0061 · cache hit 80% · 0 compactions · ~/.sleipnir/sessions/20251231-090001-c4f1d9\n   no file was changed' },
  ],
  cronNote: 'five fields (minute hour day-of-month month day-of-week; *, lists, ranges, /step, Sunday is 0 or 7) or @hourly, @daily, @weekly, in the local time zone; a run is a headless `sleipnir run --quiet` in --cwd with the job\'s model, mode and budget (US$1 by default)',
  runCommand: 'sleipnir run --quiet [--model M] [--cwd DIR] [--mode MODE] [--budget-usd N] -- GOAL',
  startedWhileDown: 'a daemon that was down starts an overdue job once, not once per missed slot',
};
/** cronNext(expr, fromISO): next run time of a five-field cron expression or @hourly/@daily/@weekly after `from`, as ISO local "YYYY-MM-DDTHH:MM:00" (minute resolution; local time of the sample). */
S.cronNext = function (expr, fromISO) {
  expr = String(expr).trim();
  var alias = { '@hourly': '0 * * * *', '@daily': '0 0 * * *', '@weekly': '0 0 * * 0' };
  if (alias[expr]) expr = alias[expr];
  var f = expr.split(/\s+/);
  if (f.length !== 5) return null;
  var ranges = [[0, 59], [0, 23], [1, 31], [1, 12], [0, 7]];
  var sets = f.map(function (field, i) {
    var out = {};
    field.split(',').forEach(function (part) {
      var step = 1, m = /^(.*)\/(\d+)$/.exec(part);
      if (m) { part = m[1]; step = +m[2]; }
      var lo, hi;
      if (part === '*') { lo = ranges[i][0]; hi = ranges[i][1]; }
      else if (/^\d+-\d+$/.test(part)) { var p = part.split('-'); lo = +p[0]; hi = +p[1]; }
      else if (/^\d+$/.test(part)) { lo = hi = +part; if (m) hi = ranges[i][1]; }
      else { out = null; return; }
      if (out) for (var v = lo; v <= hi; v += step) { out[i === 4 && v === 7 ? 0 : v] = true; }
    });
    return out;
  });
  if (sets.some(function (s) { return !s; })) return null;
  var d = new Date(fromISO.replace(' ', 'T') + (/Z|[+-]\d\d:\d\d$/.test(fromISO) ? '' : ''));
  d.setSeconds(0, 0); d.setMinutes(d.getMinutes() + 1);
  var domStar = f[2] === '*', dowStar = f[4] === '*';
  for (var n = 0; n < 366 * 24 * 60; n++, d.setMinutes(d.getMinutes() + 1)) {
    if (!sets[3][d.getMonth() + 1] || !sets[1][d.getHours()] || !sets[0][d.getMinutes()]) continue;
    var domOk = sets[2][d.getDate()], dowOk = sets[4][d.getDay()];
    var dayOk = domStar && dowStar ? true : domStar ? dowOk : dowStar ? domOk : (domOk || dowOk);
    if (!dayOk) continue;
    var p2 = function (x) { return (x < 10 ? '0' : '') + x; };
    return d.getFullYear() + '-' + p2(d.getMonth() + 1) + '-' + p2(d.getDate()) + 'T' + p2(d.getHours()) + ':' + p2(d.getMinutes()) + ':00';
  }
  return null;
};

/* ---- shortcuts: the TUI keys (REAL: docs/CLI.md, docs/UX.md) and the web aliases ------------------------------------ */
S.shortcuts = [
  { keys: 'Enter', web: 'Enter', action: 'send the message', scope: 'chat' },
  { keys: '\\ at end of line · alt+enter · ctrl+j', web: 'same (alt+enter, shift+enter)', action: 'a newline in the message', scope: 'chat' },
  { keys: 'Up / Down', web: 'Up / Down', action: 'recall history (history.jsonl in the state directory)', scope: 'chat' },
  { keys: 'ctrl+r', web: 'ctrl+r', action: 'search the history', scope: 'chat' },
  { keys: '/', web: '/ or ctrl+k', action: 'open the command palette', scope: 'chat' },
  { keys: '@', web: '@', action: 'complete a path from the project tree', scope: 'chat' },
  { keys: 'shift+tab', web: 'shift+tab', action: 'step the permission mode default → accept-edits → plan (never bypass or yolo)', scope: 'chat' },
  { keys: 'ctrl+t', web: 'ctrl+t · alt+t (the browser keeps ctrl+t)', action: 'the stats page (the same as /stats)', scope: 'chat' },
  { keys: 'ctrl+g', web: 'ctrl+g · alt+g', action: 'the team cockpit (/agents writes the table)', scope: 'chat' },
  { keys: 'ctrl+o', web: 'ctrl+o', action: 'write the whole of the newest output that was shown collapsed', scope: 'chat' },
  { keys: 'Esc', web: 'Esc', action: 'interrupt the running turn (and pause a goal); releases a pinned hold and closes overlays first', scope: 'chat' },
  { keys: 'Ctrl-C', web: 'ctrl+c (copy when text is selected)', action: 'at the prompt: discard the line; twice within 2 s: quit; during a turn: cancel the turn', scope: 'chat' },
  { keys: 'Ctrl-D', web: 'ctrl+d', action: 'on an empty line: quit', scope: 'chat' },
  { keys: '1 2 3 (4)', web: '1 2 3 (4)', action: 'answer a question; arrows/Tab and Enter choose; esc is no; letters never answer; keys are taken only after the keyboard has been quiet for 350 ms', scope: 'question' },
  { keys: 'o c m b · 1-4 · Tab', web: 'o c m b', action: 'cockpit, cache, mail, board (watch and replay)', scope: 'watch' },
  { keys: 'arrows', web: 'arrows', action: 'choose the agent the cache view is about', scope: 'watch' },
  { keys: 'space', web: 'space', action: 'pause the screen (watch, replay)', scope: 'watch' },
  { keys: 'a', web: 'a', action: 'animation off', scope: 'watch' },
  { keys: 'arrows · shift+arrows · + - · Home End', web: 'same', action: 'replay: seek 10 s (shift: a minute), speed, start, end', scope: 'replay' },
  { keys: '?', web: '?', action: 'list the keys', scope: 'everywhere' },
  { keys: 'q', web: 'q', action: 'leave (watch, replay)', scope: 'watch' },
  { keys: '(web) Space over the chat', web: 'Space · Esc', action: 'pin the hover hold; Esc releases it', scope: 'web' },
];
