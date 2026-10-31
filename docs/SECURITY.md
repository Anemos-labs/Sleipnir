# Sleipnir security model

Sleipnir gives a language model a shell, file tools and a network connection, on your machine and in your repositories.
This page says what that setup protects against, what it does not, what is hardened on each OS, and how to confine it
harder. It is blunt on purpose: a coding agent executes text that an attacker may have written, and nothing done inside
the process changes that. `AGENTS.md` and `docs/ARCHITECTURE.md` define the implementation contracts.

## Reporting a vulnerability

Report a vulnerability privately: not in a public issue, not in a pull request. GitHub reads this file as the repository's
security policy, and the **Security** tab has a **Report a vulnerability** button (private vulnerability reporting): it opens
an advisory that only you and the maintainer can see, where the fix can be prepared before anything is public.

**What to include.** The version or commit (`sleipnir version`) and your operating system; what you did, what happened and
what you expected; the smallest repository, command line or transcript that reproduces it; which boundary of section 1 you
think is crossed (a command the permission engine should have denied, a key that reached a host it should not, text from a
repository or a web page that was followed as an instruction); and whether the report must stay confidential until a fix
ships. Do not send real credentials: say where they would have been read from.

**What to expect.** Sleipnir has one maintainer, so this is an intention, not a contract. You should get an acknowledgement
within about a week. The maintainer then says whether the report is in scope (the "What it does not do" list below is
what the harness does not claim to stop), works on a fix with you in the private advisory, and publishes an advisory with
credit to you, if you want it, when the fix is released. There is no bug bounty. A finding that turns out to be a bug without
a security impact is handled as an ordinary issue, with your agreement.

## 1. Threat model on one page

**Assets.** Provider API keys; the files and credentials of the user account the harness runs as (`~/.ssh`, `~/.aws`,
cloud CLIs, browser profiles, git credential helpers); the integrity of the repository; session logs, which hold every
prompt, tool call and output verbatim.

**Adversaries.**

1. *Prompt injection*: text the model reads that tells it what to do: repository files, READMEs, issues, web pages, command
   output, mail from other agents, MCP results, another model's summary. It needs no exploit, only a model that sometimes
   complies. Assume it will.
2. *A hostile repository*: symlinked instruction files, `.gitignore` tricks, project config, hooks, MCP servers, test suites
   and build scripts that run code, invisible Unicode.
3. *A hostile or compromised endpoint*: a model gateway or a network on the path (redirects, huge streams, hostile headers).
4. *Another agent* in a swarm that was injected.
5. *A same-user process*, above all the commands the model itself runs: they start as children of the harness with your rights.
6. *The model itself.* Open weights, a fine-tuned adapter or a GGUF chat template can carry a backdoor: the model behaves until a
   trigger (a phrase, a date, a file name) appears, then acts against the person running it and hides it in a helpful answer. Assume
   any model may, and judge what it does, never what it says (below, "A model that works against you").

**Trusted:** you, your own config under `~/.sleipnir`, the operating system, the provider you chose (it sees everything
you send it). **Not trusted:** model output, repository content, web content, tool output, mail, project-level config and
instruction files until you say `--trust-project`, or say yes to exactly those files (`sleipnir trust`; see "Trusting a
project" below).

**What the harness does about it**

* *Permissions are code, not a prompt* (`internal/perm`): modes (`default`, `accept-edits`, `plan`, `bypass`, `yolo`), allow/deny/ask
  rules, shell-syntax analysis that judges every simple command including those in substitutions, hard denies that no
  mode lifts (`~/.ssh` keys, `~/.aws`, `~/.gnupg`, `/proc/*/environ`, `.git` writes, system directories), guarded paths
  (`.env`), role profiles that can only tighten, and no prompter means deny. Every file, web and shell tool asks the engine
  before it acts; background-job tools ask before touching another agent's job, and a read-only role (reviewer, scout) is
  denied them by name. A question in `chat` takes only a line typed after it was shown (`cmd/sleipnir/chat_input.go`): text
  pasted or typed while a turn runs waits for the next prompt, so a `y` that arrived earlier cannot approve an action it was
  not typed for, and a question that Ctrl-C cancels takes nothing.
* *Tool output is data.* It arrives as `role:tool`, never as an instruction. Pins and notes are user-role context, mail is typed,
  capped and never authority (`docs/SWARM-PROTOCOL.md`). This is defence in depth for a probabilistic reader, not a guarantee.
* *Trust is an answer about files, not about a place.* `--trust-project` is the answer for one run. What a person can keep is a yes
  to the files they were shown, as a hash of them (below), so that a pull that changes any of them is a new question.
* *Repository text is confined.* Instruction files are read through `os.Root`: links out of the project, hidden directories,
  `.env`, non-text files are refused; invisible Unicode (tag characters, bidi controls, zero-width characters) is removed;
  repository-origin text is labelled unverified and is not loaded at all unless the project is trusted. Project config
  cannot set providers, permission modes, hooks or MCP servers unless trusted.
* *The shell tool* (`internal/tools/shell`): commands start in a session of their own and are killed as a group; the
  environment they get is scrubbed (below); output is sanitised (ANSI/OSC, C1, NUL, invisible Unicode) and bounded;
  the working directory cannot wander out of the project; default timeout 2 minutes, maximum 10; at most 64 background jobs.
* *File tools* resolve every path once (symlinks included) and give that one spelling to permissions, leases, snapshots and
  the staleness check; writes go through a lease guard, a content-hash staleness check and a checkpoint; reads and searches
  never wait on a FIFO (a `.gitignore` that is one, or a path swapped for one, cannot stall a tool).
* *Web tools* refuse loopback, private, link-local and cloud-metadata addresses and connect to the address they vetted
  (no DNS rebinding); behind a proxy the guard is weaker and the proxy's egress policy decides. Fetched text has invisible
  characters removed.
* *State is private*: `~/.sleipnir` (or `$SLEIPNIR_HOME`), directories `0700`, files `0600`; blobs are hash-verified; checkpoint
  manifests are vetted before a rewind writes anything. The log is verbatim by design (exact prompt replay); redaction happens
  when data leaves it (`internal/rl/redact`), so treat session directories like credentials and delete old ones.
* *The provider key* is put only in the `Authorization` header: never in a URL, in the harness's own error text, or in events.
  (What a hostile endpoint does with it once received is the endpoint's business.) It also goes only where you allowed it
  (`provider.CheckEndpoint`): to the provider's own host, to loopback, or to a host your *user* configuration lists
  (`providers.<name>.allow_hosts`); a base URL that arrives through the environment (`<PROVIDER>_BASE_URL`, which a `.envrc`
  or a CI job can set) or a project file cannot redirect it; over plain `http` it goes to loopback only unless the user
  configuration says `allow_insecure_http`; a redirect is followed only within the original scheme, host and port. A refusal
  says what was refused and the one line of configuration that allows it on purpose. The RL rollout server takes a policy
  endpoint and a key variable from each request, so it sends a key only to the hosts and from the variables its operator
  listed (`--policy-host`, `--policy-key-env`).
* *Endpoints are not believed.* A response is bounded as it is read (64 MiB of body, 8 MiB per line and per answer, 512 tool
  calls, 4 MiB per call's arguments, 30 minutes) and a violation ends the request; a server that accepts a request and
  goes silent is timed out (first byte 120 s, idle 60 s); usage counters and gateway-reported costs are clamped or priced
  from tokens instead; a catalogue entry with an impossible price or window is dropped; error text from the wire is
  sanitised and capped before it reaches a terminal or a log.
* *Spending is bounded.* A swarm has a built-in budget (US$50, `swarm.budget_usd`) that only you can raise or remove (a
  repository's file cannot); a cost that is not a number counts as spent, a budget that is not a positive number is refused, and peer
  mail can wake one worker only 40 times per task, so agents answering each other cannot run for as long as the budget lasts. Text
  that another agent or a file contributes is defused by the one definition the prompt layers use (`kv.EscapeMarkup`): harness
  markers such as `[mail`, `[stop hook]` and the label of a hook's context, and structural tags, cannot be forged.
* *The harness process is hardened* (section 2), and a PID-namespace or container wrapper can hide it entirely (section 3).

### Trusting a project

What a repository brings that the harness reads as text or settings (its instruction files and what they import, `.sleipnir/config.json`
and `config.local.json`, `.mcp.json`, the skills, commands and agents under `.sleipnir` and `.claude` (through the in-project directory links the loaders
follow), and the project's own plugins under `.sleipnir/plugins`) is used only when the project is
trusted. There are two ways to say so: `--trust-project` for one run, and a remembered yes, which a `chat` asks for at its start (`sleipnir
trust add` does it without a session). The remembered yes is kept in `trust.json` in the state directory, for one working directory, as a
SHA-256 over every one of those files (their kind, path and content as the loader reads it): `internal/trust` scans them, and a session
that starts with the same digest uses them without asking. Editing, adding or removing any of the files makes it another digest; the person
is told which file it was, and asked again. It is the model of direnv's `allow`, for the same reason: what was read last month is not what
a `git pull` brings today. The threats considered:

* *A pull that changes a trusted file* (a new line in AGENTS.md that tells the agent to send `~/.ssh` somewhere): another digest, not used,
  named in the notice and in the question. A file of the same name and another content is another file.
* *The repository trusting itself*: the ledger is in the user's state directory, never in the project, and a write there asks in every mode
  and is refused when nobody is there to ask (`Edit(~/.sleipnir/**)`, and `Config.StateDir` when `SLEIPNIR_HOME` moves it).
* *A question that lies*: the files are listed from the scan, each path cleaned of what a terminal acts on (`provider.SanitizeText`), a
  definition directory as one line with its file count, and what is not covered is said in the question itself.
* *A file the digest does not cover*: a settings file that is a symbolic link out of the project (the loaders follow it, the scan does not),
  an unreadable file, more than 400 files or 16 MB, or a tree of more than 50,000 entries makes the footprint partial, and a partial one is
  never remembered (the session can still be trusted for itself; the person is told).
* *A link to a file elsewhere in the project* (a skill that is a link to `docs/skill.md`): the loaders follow it, so the sum of the link
  includes what it leads to.
* *Which directory*: the answer is for the working directory the session starts in (what is read depends on it: the instruction files from
  the root down to it), not for the repository. A copy elsewhere is another project; a symbolic link spelled another way is asked about
  again, never trusted by mistake.
* *The window between the scan and the load*: a file edited by another process between the two is read in its new form. The same process
  can edit the ledger: it is a same-user process (below).
* *A chat that is cancelled at the question*: no session, nothing remembered.

What it does not cover is the repository's **code**. A hook that runs `./scripts/lint.sh` is covered (it is in the settings); the script is
not. Running a repository's code is what approving `go test`, `make` or a hook is already about, and it runs with your rights. Nor does it
make a project's tool servers start without asking: each entry of a `.mcp.json` still needs its own approval, by its fingerprint.

**What it does not do.**

* **It is not a sandbox.** A command the mode lets through runs as you, with your files, your network, your git credentials
  and your SSH agent. The permission engine judges commands by parsing them; parsing shell is best effort. A program you
  approve (`go test`, `make`, `npm test`, `pytest`) runs the repository's code with full rights, and `bypass` and `yolo` modes turn asking
  off (`accept-edits` already lets the build and test commands through, and installs into a virtual environment of the workspace, which
  run code from the package index). For a repository you do not trust, use a container, a VM or the wrapper in section 3, not the permission prompt.
* **Same-user reach.** Section 2 closes `/proc/<pid>/environ`, `/proc/<pid>/mem` and ptrace of the harness for unprivileged
  same-user commands. It does not stop root or a process with `CAP_SYS_PTRACE`; it does not hide keys that live elsewhere: the
  environment of the shell that launched the harness (`/proc/<shell pid>/environ`), dotfiles and credential files, keyrings,
  credential helpers, agent sockets by path. `/proc/<pid>/cmdline` is world-readable on every OS: never pass a key as a flag.
* **The provider sees everything you send it**, including secrets in files the model was allowed to read; the log holds it too.
* **Egress is not filtered.** `web_fetch` is guarded; `curl` in `bash` is subject to permission rules only. Prompt injection can
  exfiltrate what a command can read, and in `accept-edits` mode what the model has read can leave in the URL of a fetch nobody is
  asked about (next section).
* **The environment scrub is a heuristic** (names and value shapes), not a boundary; a credential with an unremarkable name
  and shape reaches commands.
* **Worktree isolation is not a boundary either.** With `swarm.isolation: worktree` each writer edits a git worktree of its own and its work
  reaches your checkout through a verifying merge queue; the permission engine hard-denies an isolated agent's access to the rest of the
  workspace (the shared checkout, other agents' trees), in every mode, on resolved paths, for shell commands as well as file tools. That
  keeps cooperating agents apart and every merge accounted for. A path a shell computes at run time cannot be resolved before it runs
  and is left to the mode, as everywhere else, so a worker that is hostile and in `bypass` mode is not stopped by it: use a container.

### A model that works against you

A backdoored model ("sleeper agent") is trained to act on a trigger and to look normal otherwise; safety training does not reliably
remove the behaviour (Hubinger et al., *Sleeper Agents*, 2024). Recent demonstrations: a LoRA fine-tune whose tool calls send the
environment to an attacker's storage when the year is 2026 (*Sleeper Cell*, arXiv:2603.03371), and a chat template in a redistributed
GGUF file that adds a hidden instruction when a phrase appears, which made a coding agent put an attacker's script tag in the app it
built (arXiv:2602.04653). Nothing that reads the model's text can tell such a model apart before it acts, so the harness does not try:
it limits what any model can do, in every mode, and asks or refuses the few actions a backdoor needs and a coding task does not.

What holds whatever the model is, bypass mode included:

* *Secrets it would send.* Provider keys leave the environment before any command starts, and other credential-looking variables are
  scrubbed from what commands inherit (section 2), so "read the environment and post it" finds nothing worth having. `~/.ssh`,
  `~/.aws`, `~/.gnupg` and gcloud credentials are hard-denied; token files (`~/.config/gh/hosts.yml`, `~/.netrc`, `.env`) need an Allow
  rule; the commands that print a stored credential (`gh auth token`, `git credential fill`, `gcloud auth print-access-token`,
  `security find-generic-password -w`, `secret-tool lookup`, ...) are in the high-risk class, so the token stays inside the tool that uses
  it (`gh api`, `git push`) and out of the transcript, which the provider and the session log keep.
* *Persistence.* What would run after the session is asked about, refused in bypass mode with the reason, and denied in plan mode
  (`internal/perm/persist.go`): writes to shell and session startup files, `~/.config/systemd/user`, `~/.config/autostart`,
  `~/Library/LaunchAgents`, `~/.local/bin` and `~/bin`, `~/.gitconfig`, editor and IPython startup files, package index configuration
  (`pip.conf`, `.cargo/config.toml`, `.m2/settings.xml`, `.yarnrc`, ...), the cron and at spools, and a `.pth` or `sitecustomize.py` in a
  `site-packages` outside the workspace; and the commands `crontab` (but `-l`), `at`, `systemctl enable|link|edit|...`, `systemd-run`,
  `launchctl load|bootstrap|submit|enable`, `loginctl enable-linger`, `schtasks /create`, and `git config --global|--system|--file` of a
  key that runs a program or rewrites where git fetches (`core.hooksPath`, `core.sshCommand`, `core.fsmonitor`, `alias.*`,
  `credential.helper`, `url.*.insteadOf`, filters, ...). Reading those files is not affected. An Allow rule that names the file or the
  exact command lets it through, a workspace chosen inside one of those places (`~/.config/nvim` as the project) is the project, and
  yolo mode does not look. A coding task rarely touches any of them, so this costs an ordinary session nothing.
* *Reach.* Read-only roles are read-only in the engine; an isolated writer cannot leave its tree; `.git` internals and system
  directories cannot be written; recursive deletes of broad paths, `sudo` and forced pushes to shared branches are high risk.
* *No clock in the prompt.* The prompt never carries the date (`AGENTS.md`: no timestamps in prompt text), so a trigger keyed to the year does not
  see it unless the model runs `date`.

What it does not stop, and what to do about it:

* *Code an approved interpreter runs.* `python -c`, a test, a build script or a program the model wrote can do anything you can, and
  the engine cannot read it. Bypass mode lets those run: that is what bypass means. The protections above cover the shell's view;
  the wrapper in section 3 covers the rest.
* *Exfiltration of what it may read.* The shell's network is not filtered. A backdoor can send the source code of the workspace, or
  anything outside the hard-denied and guarded places, to a host of its choosing.
* *Malice in the code it writes* (a script tag, a dependency, a weakened check). Review is the defence: a reviewer on another model,
  from another publisher, is unlikely to share the trigger. Give a team's reviewer slots a model other than the writers', or two
  models: a merge is then covered only once reviewers on two different models have each reviewed it (`docs/SWARM-PROTOCOL.md`).
  This is a suggestion, not a default: a team whose reviewers share the writers' model while another model is on the team gets a
  one-line tip when it starts, and a team on one model is never told anything.
* *Weights and templates you download.* Take them from their publisher; a chat template is code that runs on every request.

### Reading public websites in accept-edits mode

In `accept-edits` mode the built-in read-only web tools reach public websites without a question: `web_fetch` of a URL whose host is a
public internet host, and `web_search`. The engine's reason is `accept-edits mode: reading a public website`. The host is judged by its
text (`internal/weburl`, `LocalHost`), with no DNS lookup, and these still ask:

* a host on this machine or a local or reserved network: an IP literal (in any spelling a C resolver accepts, such as `2130706433` or
  `0x7f.1`) that is loopback, private, carrier-grade NAT, link-local, unique-local, multicast, unspecified, reserved or a cloud metadata
  address (`169.254.169.254`, `fd00:ec2::254`, `168.63.129.16`), and the IPv6 forms that carry such an IPv4 address;
* a local name: `localhost` and `*.localhost`, a single-label name (`intranet`, which a resolver completes with a local search domain),
  and names under `.local`, `.internal` (`metadata.google.internal`), `.lan` and `.home.arpa`;
* a fetch whose URL the tool would not fetch, and a request of a web tool that says it writes;
* every other way to reach the network: MCP tools, other tools that set the network flag, and shell commands (`curl`, `wget`), which
  are judged as in every other mode.

`default` mode asks about every network request and `plan` mode refuses them. Rules come first: a `deny` rule
(`WebFetch(domain:example.com)`, `WebSearch`) refuses and an `ask` rule asks, in this mode too.

The check reads the name and does not resolve it, so a public name whose DNS answer is a private address reads as public to the
permission engine. The `web_fetch` dialer (`internal/tools/web`, the SSRF guard) resolves the name itself and refuses to connect to a
blocked address, so such a request fails instead of reaching the private address, unless `web_fetch` is configured to allow private
addresses.

The trade-off is exfiltration. A fetched URL carries data out: a model steered by text it read (a README, an issue, a fetched page) can
put what it has read, a file of the workspace or a key, into the path or query of a URL on a host the attacker controls, and in this mode
nobody is asked first. Where that matters, use `default` or `plan` mode, or a `deny` rule (`WebFetch` for every fetch,
`WebFetch(domain:...)` for the hosts to keep out).

### "Don't ask again" for a runner command, and for edits in the project

A yes for the rest of the session is an exact rule (`Bash(mytool build ./x)`), except for edits inside the project and for a short table of runner commands (`internal/perm/runner.go`: `go test|build|vet`, `npm test|run`, `pytest`, `cargo test|build|check`, `make`, `git add|commit|status`), where it is the prefix (`Bash(go test:*)`): a person doing test-driven work is not asked again for every package. An edit is never repeated exactly, so a yes to an edit inside the project (`Edit(<root>/**)`) is a yes to edits inside it, as accept-edits mode would give; an edit outside the project stays exact, and what the project keeps asked about (`ask` rules, the protected configuration directories) or denied (`.git`, credentials) stays so, whatever was allowed. The dialog names what it remembers ("don't ask again for "go test" commands", "...for edits in this project"), the engine tells it (`Request.Remembers`). What this adds: any arguments, among them flags such as `go test -exec`, which run a program of the model's choosing. A test run already executes whatever tests the repository has (a model that can write a test file can run anything through `go test`), so the prefix gives up little that the exact rule did not. What it does not cover stays asked about: another subcommand (`go run`), a chain (`go test ./a && rm -rf x`), a command with an environment assignment that steers what runs or wrappers (`sudo`, `timeout`). The rule never names the directory the command ran in, so a yes for `cd <tree> && ./bin/app` covers `./bin/app` in every worker's tree. It is never offered for shells, interpreters, `rm`, `sudo`, `curl` or anything not in the table; a no stays exact, nothing beyond the session is widened, and an unattended session has nobody to give the yes.

The answer is offered only when the rules it adds would let the same request through (`internal/perm/ask.go`): a rule is a command's words,
so one with a variable in it (`rm -f "$F"`) would allow whatever the variable holds next time, and one for a program that reads its code
from standard input (`python3 - <<'PY'`) would allow any code; a path only known when the command runs, a question an `ask` rule puts, and a
role whose stricter mode leaves out the session's rules would ask again anyway. None of these is offered, in the chat's dialog, the line
prompter or the web page, and an answer that offered nothing remembers nothing.

### A refused question is not asked again

A question the person refused, or that nobody answered (no page had the interface open, the input ended, `--ask-timeout` ran out), is
not put to the person again in the same session: a repeat is refused at once with the earlier refusal's text after the fixed prefix
`asked earlier in this session: ` (`internal/perm/ask.go`). A question that is only about network access to a host (`network access to
soulver.io needs approval`) is the same question for every URL of that host; any other question is the same when its request is, as
coalesced questions are. A yes is not recorded this way: a yes for the session becomes a rule, and a yes for once is asked for again.
Changing the mode, or adding or removing a rule (`/allow`, the web interface's rules), forgets the recorded refusals, so those
questions are asked again. In a team, the scout's role prompt tells it to plan its web research and, after a refused fetch, to go on
from what it knows rather than try another host or tool for the same purpose.

### The manager's answers

In a team, a worker's question that the engine marks routine is put to the manager before the person (`permissions.manager_approves`,
`docs/SWARM-PROTOCOL.md` section 2). The class is the engine's: an action whose files all resolve inside the workspace, that reaches no
network but this machine, that no `ask` rule, built-in protection or high-risk marker covers, and that is not a package install, a
download, a signal to another process or a container, service or scheduler command. The one install that is routine is the one the tests
preset allows, into a virtual environment of the workspace from the package index (below); the manager is told that it reaches the index.
A program is judged by its name wherever it is run from: `.venv/bin/pip install --index-url ...` and `/usr/bin/pip install` are installs,
and never reach the manager. The manager is a model reading the worker's
command as data; what it approves runs with the mode's rights, inside those bounds. An unlisted program it approves (`./bin/app`,
`python3 tools/gen.py`) can do whatever the program does, as it could after a person's yes; the bound is the class, not the
program. Its answers are never remembered as rules, and `--manager-approves=false` puts every question to the person.

A question the person leaves unanswered can go to the manager too, when the person chose so: `permissions.manager_answers_after`
(`--manager-answers-after`) is how long it waits for the person first, and `permissions.manager_answers_up_to` the most dangerous kind the
manager may then answer (`low`, routine only; `medium`, the default, also the network, files outside the workspace and commands that
cannot be checked beforehand; `high`, also the high-risk class, guarded paths and the person's `ask` rules). Both are off unless set,
and both are security-sensitive: a project file sets them only when the project is trusted. The manager is told the question is not
routine and that a no is the safe answer when in doubt; its answer withdraws the person's question, which says who answered, and is
never remembered as a rule.

### Environment assignments, formatters and git

An environment assignment of a plain value (`PORT=8080 ./bin/app`, `NOTES_FILE=data.json go test ./...`) is an input of the command, as an
argument is: the command is judged as it would be without it, and a value that names a file is judged as a read of that file (whether or
not a rule allows the command). An assignment to a variable that steers which program runs, what it loads, where it connects or which
configuration it reads keeps the command from every allow rule and asks: `PATH`, `HOME`, `SHELL`, `IFS`, `ENV`, `PS4`, `CC`, `MAKEFLAGS`,
the families `LD_`, `DYLD_`, `BASH_`, `GIT_`, `GO`, `NODE_`, `NPM_`, `PYTHON`, `CARGO_`, `RUST`, `AWS_`, `DOCKER_` and more, and every name
that ends like a search path, an option list, a wrapper, a command or a proxy (`PERL5LIB`, `MAVEN_OPTS`, `RUSTC_WRAPPER`, `ftp_proxy`);
the list is in `internal/perm/cmds.go`. A denylist is not exhaustive: a program that reads an unremarkable variable as a command is not
recognised.

A source formatter (`gofmt -w .`, `go fmt ./...`, `goimports`, `prettier --write .`, `cargo fmt`, `rustfmt`, `ruff format`, `black`,
`isort`, `clang-format`, `shfmt`, `terraform fmt`, `dotnet format`) writes only the files of its language below its operands, back with
the same meaning. Its walk of a tree is therefore not taken as a write into every directory of that tree: an `ask` or `deny` rule about a
directory below the operand (`Edit(./.sleipnir/**)` under `gofmt -w .`) does not cover it, and one about the operand itself or a directory
above it does (`gofmt -w .sleipnir/hook.go` asks). The built-in protections still see every operand.

The git subcommands that change only the repository (`fetch`, `add`, `commit`, `tag`, `notes`, `merge-tree`, `branch`, `push`, `gc` and a
few more) read their operands: `git add .` and `git fetch .` are not writes into the work tree, and their question names the command, so a
yes can be remembered. Every other git subcommand that is not read-only (`checkout`, `reset`, `merge`, `stash`, `clean`) keeps the analysis
of an unlisted command, whose operands may be written.

A test runner given paths to find tests in (`pytest .`, `python -m pytest tests`, `python -m unittest discover -s tests`, `node --test .`)
reads them: `node --test .` is not a write into every directory of the project, so the `Edit(./.sleipnir/**)` ask rule does not stop it.
Only the runner's options that name no file to write are recognised (patterns, counts, reporters by name, a start directory); any other
(`--basetemp`, `--junitxml`, `--test-reporter-destination`, `--import`) keeps the analysis of an unlisted command. The tests themselves
are the project's code, run as the runner runs them.

curl's words are URLs, headers and data, not files: a request body that holds a variable (`-d "{\"id\":$ID}"`) is not a write to
an unknown place. The files curl does touch are judged. It writes `-o` and, with `-O`, the last part of the URL's path (each below
`--output-dir` when one is given), `-c`, `-D`, `--trace`, `--trace-ascii` and `--stderr`. It reads `@file` data (`-d`, `--data-binary`,
`--json`, `--data-urlencode name@file`), `-H @file`, a form field's `name=@file`, `name=<file` and `headers=@file`, `-T`, `-w @file`,
certificates and keys (`--cacert`, `--capath`, `-E`, `--key`), a cookie file (`-b`), `~/.netrc` with `-n`, `--netrc-file`, and the
file a `file:` URL names, which an upload also writes. What it cannot name keeps the analysis of an unlisted command: an option that is
not on its list, a config file (`-K`, whose options are not read), a file name the server picks (`-J`), a URL whose scheme or `file:`
path is only known when the command runs, and an output name filled from a URL glob (`#1`).

A writer confined to its own tree shares the repository's refs with the person's checkout, so moving a branch is not a file access the
tree check sees. Git commands that would move the checkout's branch or a branch of the run (`sleipnir/...`: the integration branch and
the agents' trees) are refused with an `isolation:` reason: `git update-ref` (and `--stdin`), `git symbolic-ref`, `git branch -f`, `-C`,
`-d`/`-D` and `-m`/`-M`, `git push .` and a `git fetch` refspec whose destination is a local branch. Only the merge queue moves those;
a branch moved without its files makes merged work look undone. The agent's own branch may still be moved (not renamed or deleted),
and branches outside the run are the ordinary rules' business.

The session's scratch directory (`$TMPDIR`) is every agent's: a writer confined to its own tree writes there, and an `ask` rule about the
state directory (where the scratch directory lives) does not ask about it; `deny` rules and the built-in protections still apply. A name
in it whose rest is only known when the command runs (`"$TMPDIR/$X"`) could leave it with a `../`, so the rule asks, and the question says
that the variable is the cause; it is never routine.

`bypass` asks only about the very dangerous: the high-risk class, and a write into a directory whose contents steer later sessions
(`.sleipnir/`, `.claude/`, git hooks and config). A file whose name is only known when the command runs (`cat "$F"`, `echo x > "$OUT"`)
cannot be checked against the built-in protections or the rules, so `bypass` allows it, unless a `deny` rule of the person's may cover
it: any `Read` rule for a read, any `Edit` rule for a write, since a name with no known beginning may be any file and the unknown part
of one whose beginning names a directory (`"docs/$X"`, `"$TMPDIR/$X"`) may hold a `../` that leads out of it. Such a name is then
refused, and the refusal says why; nothing asks. The refusal does not depend on the mode: `plan`, `default` and `accept-edits` refuse
the same name in place of the question their defaults would put about it or of what an `allow` rule would let through (`go test
"$PKG"` under the tests preset), since a `deny` rule beats both and a person asked about `"$F"` cannot see what it holds either;
without such a rule they ask. It is a file the command certainly names: a word that only might be a file (an unlisted program's
operand, an assignment's value), a name that is only listed (`ls "$X"`) and the directory `cd` enters are not refused this way. A
name whose known part is inside a steering directory (`".sleipnir/$N"`) still asks.
A built-in protection cannot see a name it cannot read: in `bypass` mode a command that spells a protected path through a variable is
not stopped by the engine (as it is not stopped from running a program that reads it); use a container where that matters. The words
of an unlisted program and the value of an assignment, which only might be files, are judged as the path they spell, not as a tree
below it (`node script.mjs .`). Two command substitutions have known values: `$(pwd)` and `$(git rev-parse --show-toplevel)`, and
`$(mktemp ...)` names a new path in the directory `mktemp` creates it in (see below). `yolo` mode never asks, and lets such a name pass,
whatever `deny` rule may cover it.

### Variables a command line sets

A variable that the same line sets to a value the engine can expand (`F="$TMPDIR/x"`, `export F=...`, `readonly F=...`, `declare -x
F=...`) names that value in the commands after it, so `rm -rf "$F"` is judged as the path it is. It is done only where it is sound: the
statement runs unconditionally (not after `&&`, `||` or a pipe, not in a subshell, a loop or a branch; a command substitution that closed
before it, as in `cd "$(pwd)"; F=...`, does not count), or it is a link after the first of an `&&` chain, whose following links are
then the only place the value is known (see below); the value is not empty (`${F:=x}` would replace an empty one), a name assigned
two values is unknown, and nothing in the line can change variables behind the analysis' back
(`read`, `eval`, `source`, `unset`, `wait -p`, `printf`, arithmetic, a function definition, a command in the background). A declaration
expands all its values before it assigns any (`export a=1 b=$a` gives `b` the old `a`). The commands of a statement's own substitutions
run while it is expanded, so they see only what it has assigned by then: one in an assignment's value sees the assignments to its left
and not the others (`X=$(rm -rf "$F") F=...` removes the `F` from before the line, `F=... X=$(rm -rf "$F")` the new one), one of a
declaration none of its assignments or exports, and one in its redirections or heredocs all of them. Where that and the value after the
statement give different verdicts, the stricter one stands. A new shell (`sh -c`, a script on a shell's standard
input) sees only the variables the line exported; the commands of a trap are judged as they run, after the rest of the line, in every
directory the line may be in by then and with only the variables that were known where the trap was set and still are at its end; a trap
that assigns makes the whole line unknown. The shell's own variables are never taken as assigned (`PWD`, which `cd` changes; `RANDOM`,
`SECONDS` and `LINENO`, whose reads are not what was assigned; `UID` and `PPID`, which cannot be assigned), and a line that assigns
`HOME`, `TMPDIR` or `PWD` no longer expands them as the harness's.

Each link of an `&&` chain runs only when the link before it succeeded, so an assignment in a link has been made whenever a later link
runs: in `cd <dir> && F="$TMPDIR/x" && rm -rf "$F" && tool --profile "$F"`, both commands after the assignment name `$TMPDIR/x`. The
chain is the links of one list that its first link joins by `&&` alone, and what such a link assigns is known to everything the later
links hold (a pipeline, a group, a subshell, a branch, a substitution; a new shell sees it only when the link exported it). The link
itself must be a plain statement of the line's own shell: an assignment, or a declaration whose operands all have a value (`export
F=...`, not `export F=... G`); one in a pipeline, a subshell, a group, a loop or a branch, or in a list of the line's own shell sent to
the background, is not one. A background job inside a later link (`a && F=x && { b & wait; } && rm -rf "$F"`) starts after the links
before it and does not end the chain. The rules for values above hold as they do anywhere (a value from a command's output may be
empty, two values make a name unknown, a statement's own substitutions see only what it has assigned by then). Once an `||` has
appeared in the list (`a || F=x && rm -rf "$F"`, where a successful `a` skips the assignment and still runs `rm`), the rest of the
list is in no chain. After the chain (`;`, `&`, a newline, an `||`), and for a trap set inside it, a name assigned in a link after
the first is unknown: the chain may have stopped before the assignment. The first link runs whenever its list does, so what it
assigns stays known after the chain, as an unconditional statement's value does (`F=x && a; rm -rf "$F"`).

The shell's special parameters whose value is a number (`$$`, `$?`, `$#`, `$!`, `$PPID`, and bash's `$BASHPID`, `$RANDOM`, `$SRANDOM`,
`$SECONDS`, `$EPOCHSECONDS`, `$EPOCHREALTIME`, `$UID`, `$EUID`, `$LINENO`) are a piece of a name: `"$TMPDIR/out-$$.json"` is a file in
`$TMPDIR`. A number holds no `/` and is never `.` or `..`, but some of them may be empty (`$!` before a background job; bash's names
under another `sh`), so a name part that an empty value would make `""`, `.` or `..` (`"$TMPDIR/$!"`, `"$TMPDIR/.$RANDOM"`) is not known;
neither is a parameter the line assigns or names otherwise, or one the harness's environment defines (another `sh` would expand it to
that value).

`$(mktemp ...)` is one new name in a known directory, so `tmp=$(mktemp -d "$TMPDIR/demo.XXXX")` makes `rm -rf "$tmp"` a delete inside
`$TMPDIR`. The directory is the one GNU `mktemp` picks: the template's own (`"$TMPDIR/demo.XXXX"`, `/x/rw.XXXXXX`; a relative template
stays relative, as `mktemp` prints it), the `DIR` of `-p DIR` or `--tmpdir=DIR`, and `$TMPDIR` with `-t`, with an empty or no `DIR`, or
with no template. `$TMPDIR` is known only as the session's scratch directory, and not on a line that names `TMPDIR`; otherwise such a
name is unknown (where `TMPDIR` is unset, `mktemp` uses `/tmp`, which the engine cannot tell). Only the options `-d`, `-q`, `-t`, `-u`, `-p` and their long names count, with at
most one template whose last part ends in three or more `X`s (with `-t`, a prefix without them, as BSD's `mktemp` takes it), which is a
plain name with `-t` or a `DIR`. The template and `DIR` expand to the same text whatever a command of the line printed: an unknown
variable, a value from a command's output, a glob, a brace, a blank, `~`, `..` or a substitution in them leaves the name unknown. A dry
run (`-u`) creates nothing, so its name counts only in the scratch directory, which is the session's own. A failed `mktemp` prints nothing
and leaves the variable empty, as does any command whose output a variable holds (`$(git rev-parse --show-toplevel)`): a word built from
it is judged that way as well, so `rm -rf "$tmp"` deletes the new directory or nothing, `rm -rf "$tmp"/*` (the root's contents) is
refused, and `rm -rf "$tmp"*` (the working directory's contents) is a high-risk recursive delete.

### Project virtual environments

The tests preset (`--allow tests`, the fourth answer of a question about a build or test command, and `accept-edits` mode) includes a
Python virtual environment of the workspace (`Bash(python:venv)`, `internal/perm/venv.go`): creating one (`python -m venv DIR`,
`virtualenv DIR`, `uv venv [DIR]`), installing packages into it with its own pip (`DIR/bin/pip install`, `DIR/bin/python -m pip install`,
`uv pip install --python DIR/bin/python`), and running the tests with it (`DIR/bin/python -m pytest`, `DIR/bin/pytest`). In `default` mode
these are routine questions, which the manager of a team may answer. It is the one place where an allowed or routine command reaches
beyond this machine: the package index pip or uv is configured with. The read-only roles (reviewer, scout, architect) hold only the run
part (`Bash(python:venv-tests)`): they run the tests and checks with an environment the writers made, and never create one or install into it.

What an install does is run code from the internet with the user's rights: a source distribution's build code runs during the install,
and the packages it installs run when the tests import them. A package name chosen by a model can be a malicious package of the index.
The preset does not make that safer than it is; it keeps the install where the tests are, and the command honest about its source:

* The environment is a directory of the workspace and not a workspace root (the project, an agent's tree, `$TMPDIR`); to install into it
  or run it, it has a `pyvenv.cfg`, every entry of it, of its `lib` directories and of their `site-packages` resolves inside it, its
  pip and pytest are files whose first line runs its own python (a copy of an environment made elsewhere runs the original's), and that
  python is a link to an installed interpreter: followed through its links, a native executable (ELF or Mach-O, not a script) named
  python, outside the workspace. CPython takes the environment from a `pyvenv.cfg` beside the path it was started by, so a link keeps
  the environment, while a wrapper that starts another python (`exec /usr/bin/python3 "$@"`, with or without `#!`) would install into
  the system's or the user's `site-packages`; a wrapper, a link to a script, a program of the workspace and a copy of the interpreter
  (`python -m venv --copies`) ask. An install therefore lands in the workspace. A directory that already holds something other than an
  environment is not used, and `--clear` and `--upgrade` ask. A test run with the environment is allowed only while every file it names
  is inside the workspace (`pytest --basetemp=/elsewhere` asks).
* An install names requirements by name and version (`pytest>=8`, `requests[socks]==2.32`), `-r` and `-c` files and `-e` or local paths
  of the workspace, and `-U`, `-q`, `--pre`, `--no-input`, `--no-cache-dir` and `--disable-pip-version-check`. An option that changes
  where packages come from (`-i`, `--index-url`, `--extra-index-url`, `-f`, `--trusted-host`, `--no-index`, uv's `--index` and
  `--config-file`) or where they go (`--target`, `--prefix`, `--root`, `--user`, `--src`, `--break-system-packages`, uv's `--system`), a
  URL or a repository (`git+https://...`, `name @ https://...`), a file outside the workspace, and `uv pip install` without `--python`
  (uv would pick an environment by itself) ask, and are not routine. So do `pip install` of the system or the user's Python, `python -m
  pip install`, and every other installer, as before.
* What the project's own files say is the project's: a requirement file can name an index or a URL, `pyproject.toml` can name direct
  references and a build backend, `setup.py` runs, a `pip.conf` inside the environment and `[tool.uv]` settings apply. A model that can
  edit those files can point an install anywhere, as it can make the tests run anything; the preset is no stronger than the tests it lets
  run. Caches are written where pip and uv keep them (`~/.cache/pip`, `~/.cache/uv`), and `uv venv` may download a Python build when no
  installed one fits.
* The checks are made when the command is judged: a link swapped into the environment between the check and the install is not seen.

To keep installs to a person, do not give the session the tests preset (`default` mode, no `--allow tests`), answer the question about a
build command without its fourth answer, or add `"deny": ["Bash(python:venv)"]` (or `"ask"`); `--manager-approves=false` keeps them from
the manager.

### A loop over files that are written out

`for f in p1/p1.go p2/p2.go; do cat "$f"; done` names its files in its list, so each command of its body is judged as the command it will be, once for
each word (`internal/perm/loopvars.go`). It is done only where it is sound: the words are plain (no variable, substitution, quote, space or shell
syntax), or such a word wholly in quotes with no glob and no leading `~` (`"390,844"`, which stands for what is inside the quotes), at most
sixteen, the variable is lower case (not `PATH` or `HOME`), exactly one loop sets it and nothing else in the line assigns it, nothing in the line can
change variables behind the analysis' back (`read`, `mapfile`, `eval`, `declare`, `export`, `printf -v`, `((`, an `IFS`), and a use that is not a
plain `$f` (`${f%.go}`) stays dynamic. A command that uses the variables of nested loops is judged once for each combination, up to 64 of them (a
screenshot loop over 9 pages, 2 sizes and 2 themes is 36); past that it stays dynamic. A loop over `~/.ssh/id_rsa`, or over a path outside the workspace, is denied or asked about like the
command it expands to; a list that is not written out (`for f in $(ls)`) asks as before.

## 2. What is scrubbed and hardened, per OS

Call `harden.Process()` first thing in `main` (`cmd/sleipnir` does). It never fails: what could not be done is in its `Status`.

| | Linux | macOS | Windows and others |
|---|---|---|---|
| Same-user process reads the harness's memory (`/proc/<pid>/mem`, ptrace) | Blocked for unprivileged processes: `prctl(PR_SET_DUMPABLE, 0)`. Not for root / `CAP_SYS_PTRACE`. Children are unaffected (`execve` resets the flag); the process can still use its own `/proc/self/{stat,status,maps,fd,exe,ns}`, only `environ` and `mem` are closed to it. Side effects: no core dumps, debuggers cannot attach | Debugger attach refused: `ptrace(PT_DENY_ATTACH)`. Best effort, compiled but not run in this repository's tests; it makes a process that is already being traced exit | Nothing |
| The harness's initial environment | Values of credential-looking variables are erased in the kernel's copy (`/proc/<pid>/environ` shows `NAME=`), even against root; Go's own environment and every child are unaffected. Tested as an unprivileged user and as root | Not erased: `ps eww` / `sysctl kern.procargs2` show the environment of a same-user process. Keep keys out of it | Not erased; a same-user process can read it through the PEB. Keep keys out of it |
| Environment handed to commands | Scrubbed by name (`api key`, `secret`, `token`, `password`, `credential`, `private key`, trailing `_KEY`, `_PAT`, `_DSN`, `AUTH`, `COOKIE`, `_PWD`, `SSH_AUTH_SOCK`) and by value (a URL with a password, PEM private keys, `sk-`, GitHub, Slack, AWS, Google and JWT token shapes); proxy URLs keep their credentials. `PassEnv` lets named variables through; `BaseEnv` replaces the environment entirely | same | same |
| ChatGPT sign-in | `sleipnir login chatgpt` keeps the sign-in (access, refresh and ID tokens, the client id OpenAI issued, a host id) in `~/.sleipnir/chatgpt.json` (mode 0600, written atomically). Only `internal/chatgptauth` reads it: the tools and child processes of a session never see a token, and the file is guarded like the other token files of the home directory (a model's read or write of it asks). The sign-in is sent to the provider's own address only: no environment variable and no project file can name another. The redirect is a loopback port that answers one request with the right state; an ID token is checked for issuer, audience, nonce and expiry (not its signature: it came straight from the token endpoint over TLS, which OpenID Connect allows to stand in for it). `sleipnir logout chatgpt` revokes the refresh token. The plan's model list is kept under the state directory's `cache/` with slugs, names and context windows only; a session asks for it only with an access token that needs no renewal, so a deadline never cuts a renewal off midway and loses the rotated refresh token | same | same |
| Stored keys | `sleipnir login` keeps a key in `~/.sleipnir/auth.json` (mode 0600, written atomically, never in `config.json`, which people share), keyed by the provider's key variable. At start `config.LoadStoredKeys` hands them to `harden.Provide`: they live in the process's memory like a moved key, an environment variable of the same name wins (including one that `harden.MoveKeys` has already taken out of the environment: a stored key never replaces it, and `harden.SourceOf` says whether the environment or the file supplies the key in use), and no tool or child process sees them. The file is under `~/.sleipnir`, so a model's write to it asks in every mode. It is as safe as the user's home directory: another program running as the same user can read it, as it can read `~/.ssh` | same | same |
| Keys out of the harness's own environment | `cmd/sleipnir` calls `harden.Process(harden.MoveKeys())`: provider API keys (every variable ending in `API_KEY`, and any credential the configuration names, such as a provider's `api_key_env`) move into memory and are unset, so no child that inherits the environment (git, hooks, verifiers, MCP servers) receives them. Every reader goes through `harden.Secret` (a source-level test, `TestReadersOfCredentialsUseSecret`, fails on a new `os.Getenv` of a credential; `TestKeysLeaveTheEnvironmentButStillReachTheirReaders` runs the real code paths in a child that did what `main` does). MCP `${NAME}` references still resolve against the same set of variables as before, held keys included, and approving a project entry still lists the variables it asks for. On macOS and Windows this keeps keys out of children, not out of `ps eww` for the harness itself: the key was in the environment when the process started | same | same |
| Scratch files of commands | `TMPDIR` is `<state>/sessions/<id>/tmp` (0700) for every command of a session (not for a rollout, which brings its own environment). The permission engine treats that directory as part of the workspace of every agent (an isolated writer included) and knows `$TMPDIR`, so `echo x > "$TMPDIR/out-$$.json"` and `go build -o $TMPDIR/x` need no question in `accept-edits` mode; the runtime context tells the model that scratch files go there and that `/tmp` asks, and a refusal of any other place names the directory. A program that ignores `TMPDIR` and writes `/tmp` is still asked about | same | same |
| Process tree cleanup | Own session and process group, SIGTERM then SIGKILL of the group; strays are reaped at shutdown, including background processes a finished command left running with its output sent elsewhere (`nohup server >log 2>&1 &`); a team worker's are reaped when its run ends (background jobs stay until `bash_kill` or shutdown) | same | Windows task and integration verifiers use Job Objects to terminate descendants on cancellation, timeout, and exit. Other command runners use `taskkill /T`, best effort |
| Shell used | `bash`, else `sh`; the working directory persists between calls | same | `pwsh`, `powershell` or `cmd`; no working-directory persistence, no cwd guard |
| Terminal safety of tool output | `tools.SanitizeForTerminal` strips escape sequences, C0/C1 controls, bidi and other invisible characters and CRs; the shell tool already cleans its own output as it reads it. The UI must call the helper for file, web and listing text before printing | same | same |

`SLEIPNIR_DUMPABLE=1` leaves the process dumpable (for `dlv`, `gdb`, core dumps); the environment erasure still happens.

## 3. Sandboxing harder: the shell wrapper

### `--sandbox docker`: a container per session

`sleipnir chat --sandbox docker` (and `run`, and the New session dialog's *sandbox* field) runs every shell command of the
session, the agents' and the verification's, inside one container of its own; `--sandbox docker=IMAGE` picks the image. Plain
`docker` picks one for the language of the project, from the manifest at its top: `golang:X.Y-bookworm` for a `go.mod` (its
`toolchain` or `go` line), `rust:1-bookworm` for a `Cargo.toml`, a Maven or Gradle JDK image, `python:3-bookworm`,
`ruby:3-bookworm`, `node:lts-bookworm`, else `buildpack-deps:bookworm`. For Go, whose fetch runs none of the project's code and
nothing its configuration names (`go mod download`), the container starts on the network, fetches the modules into its in-memory `HOME`, and is
disconnected from every network before the session's first command; a container that cannot be disconnected is removed and the
session does not start. Builds and tests then run offline. Other languages' package managers run install scripts, build
packages or run programs the project's configuration names (cargo's credential providers), so nothing is fetched for them: use `--sandbox-network` when the session must install dependencies. The harness stays outside: the model's keys, the session's recording, the file and web tools,
hooks and MCP servers run on the host, and each command is sent in with `docker exec`. This is the pattern hosted review
services use for untrusted repositories: the agent loop outside, commands streamed into the sandbox.

| | |
|---|---|
| Files | the project root (and the team's worktrees directory) mounted read-write at their own paths; nothing else of the host. `/tmp` and `HOME` are an in-memory filesystem. A directory that is your home or holds it, the root, or a link to either, is refused |
| Git | the repository's `.git/config`, `config.worktree`, `hooks` and `info` are mounted read-only over the project, and `.git` itself is a mount point of its own, so that it cannot be renamed away and replaced by a copy; a linked worktree's `.git` file is read-only. A command therefore cannot set `core.fsmonitor`, `core.hooksPath` or a hook that your own `git` on the host would then run (the harness's own git calls turn hooks and fsmonitor off in any case). The rest of `.git` (refs, objects, the index) stays writable for commits made inside; after running code you do not trust, treat the clone as you would the code, or scan a fresh clone |
| Network | none (`--network none`, or disconnected after the Go module fetch above) unless `--sandbox-network`, which a session needs only to install dependencies |
| Privileges | the host user's uid and gid, every capability dropped, `no-new-privileges`, an init that reaps orphans |
| Image | any image with `bash` on its PATH (the bash tool runs bash); one without it, such as alpine, is refused as the session starts |
| Limits | 2048 processes, 8 GB of memory, 4 GB of `/tmp` |
| Environment | none of the host's but a fixed allowlist of named variables (`TERM`, `LANG`, `TZ`, `NO_COLOR`, `CI`, the pager and prompt switches, ...); no key, token or path of the host crosses over |
| Resume | a session that ran in a sandbox resumes in the same one (its image and network setting are recorded in the session's start), whatever the resume asks |
| Lifetime | started as the session opens (a session that asked for a sandbox and cannot have one does not start), removed when it closes; a container whose owning process died is removed by the next sandboxed session on the host |

A command stopped by the harness (a timeout, an interrupt) is stopped inside the container too: `docker exec` alone leaves
what it started running. Each command runs with a variable naming its run, which every process it starts inherits; the stop
finds them by it, so nothing is written inside the container to track a command. Files the agents' tools write and files commands write are the same files, owned by you. What the
container cannot protect is what you mount: a command can change anything in the project, as it can on the host, and
checkpoints and the merge queue still review that. Exercised by `internal/sandbox` (`TestSandboxRunsCommandsInside`,
`TestSandboxStopReachesInside`) against a real engine.

### The wrapper underneath

`shell.Options.Wrap` (`session.Options.ShellWrap` when you embed the session) is an argv prefix every model-run command
starts under: `Wrap[0] Wrap[1:]... <shell> -c <script>`; `--sandbox` sets it to the exec helper. It confines what the model
*runs*; the harness itself, the file and web tools, hooks and MCP servers are not wrapped (the permission engine governs the
tools). Keep `os.TempDir()` writable inside the sandbox, or `cd` stops persisting between calls (it fails safe).

* **A private PID namespace and `/proc`** (Linux, no extra tools; exercised by
  `TestWrapInAPIDNamespaceHidesTheHarnessFromCommands`):
  `["unshare", "--user", "--map-current-user", "--pid", "--fork", "--mount-proc", "--kill-child"]`.
  The harness does not exist for the command: `/proc/$PPID` is gone, for root and `CAP_SYS_PTRACE` too, which the dumpable
  flag cannot promise. It does not restrict files or the network.
* **`bwrap`** (not exercised here; adapt and test on your system): read-only root, a writable project, private `/proc`, no
  network, credential directories masked (only directories that exist can be masked):
  `bwrap --die-with-parent --unshare-pid --unshare-ipc --unshare-uts --unshare-net --ro-bind / / --dev /dev --proc /proc
  --bind /tmp /tmp --bind <project> <project> --tmpfs $HOME/.ssh --tmpfs $HOME/.aws --tmpfs $HOME/.gnupg`; Sleipnir
  appends the shell as the command to run. Without `--unshare-net` the network stays open; with it, package registries are
  out unless you provide a proxy.
* **A container or VM around the whole harness** is the strongest and simplest answer for untrusted repositories: mount the
  project, pass the provider key in, run as a non-root user. Commands then share the harness's container; add the PID
  namespace above if the key must stay invisible to them.
* **Another uid** for commands (`["sudo", "-n", "-u", "<sandbox user>", "--"]` with a matching sudoers rule) keeps them out of
  your files and out of the harness's `/proc` entries without namespaces; not exercised here.
* `perm.ModePlan` (read-only) or a role profile is a policy for what the harness will run, not a confinement of what an
  approved program does.

## 4. Platform limitations

Go `test`, `build`, `list`, and `vet` package operands are input trees. Local
ellipsis selectors such as `./...` are checked against their literal parent;
deny and ask rules also apply to possible descendants, without requiring those
files to exist. File-valued flags and custom test arguments retain literal path
checks. An import path such as `example.com/project/...` has no statically known
filesystem location: it requires command authorization and conservatively matches
explicit read restrictions. Use local package paths when narrower checking is
needed. These commands require approval for `-overlay` in default and accept-edits
modes even with a matching command allow rule; plan mode refuses them. The overlay
JSON can redirect source reads to paths outside the command operands. Bypass and
yolo modes retain their explicit authorization to run such commands. These checks
do not inspect dependencies or sandbox an approved test.

* **macOS.** The initial environment of a same-user process is readable by other same-user processes (`ps eww`,
  `sysctl kern.procargs2`), and neither erasing nor `MoveKeys` can change what macOS reports for it; a key exported in the
  shell that launched the harness is readable there too. Use `sleipnir login` to load credentials from the protected auth file instead of exporting them at startup. `PT_DENY_ATTACH` stops debuggers, not `ps eww`. There are no PID namespaces: the harness stays
  visible to commands. Real mitigations are `sandbox-exec` through `Wrap` (deprecated by Apple, still works; not tested here), a
  container or VM, and short-lived, narrowly scoped keys.
* **Windows.** No hardening of the harness process at all. The shell is PowerShell or `cmd`, and the permission engine's shell
  analysis is written for POSIX syntax, so its verdicts on those command lines are not reliable. File-tool permissions normalize
  native separators, preserve drive and UNC roots, follow symlinks and junctions, and enforce workspace boundaries, credential
  protections, and worker confinement. Required Windows tests exercise local paths and real file tools; UNC root handling is
  checked without a network share. Permission-rule patterns use forward slashes (`Read(C:/project/src/**)`); backslashes escape
  glob characters. Device namespaces, alternate streams, drive-relative paths, and names ending in dots or spaces are refused.
  Windows system-directory protection and shell analysis remain incomplete. Session recordings (the journal and its blobs)
  are created with modes 0700 and 0600, which Windows does not enforce, and a journal's owner is not checked there: their
  privacy is the access list of the directory they are written in. A user profile's is private by default; record into a
  shared folder only if its access list is. Run the harness inside WSL2 or a Linux container for the Linux permission and
  hardening path.
* **Everywhere.** A same-user process can read what the user can read. The harness cannot change that; sandboxing does.

## 5. The web interface (`sleipnir web`)

`sleipnir web` runs a web server on the machine, and what it serves can start agents, answer permission questions and change
settings. A request that reaches it is therefore a request to run commands with the user's rights, and the server is built
around who may make one. The code is `internal/web` (`doc.go` states the rules; `auth.go` is kept apart for review).

**Who is considered.**

1. *Other users of the machine.* Any local user can connect to a loopback port.
2. *Other websites.* A page in the user's browser can send requests to `127.0.0.1`: cross-site forgery (a form or `fetch` aimed at
   the server), DNS rebinding (a name that first resolves to the attacker and then to `127.0.0.1`), and a page served by
   another program on the same host name but another port.
3. *A prompt-injected agent* that shares the user's uid, and so can run `curl` against the port and read what the user can read.
4. *Anything on the network*, if the server were reachable from it.

**What stops them.**

| Threat | Control |
|---|---|
| Reaching the server from a network | The command accepts a loopback `--addr` only, and the rule is enforced three times (the address is checked when the server is created, again on the address that was bound, and again when serving). A non-loopback address is refused with a pointer to an SSH tunnel. There is no TLS: traffic that left the machine would carry the token in the clear. |
| Other local users, a prompt-injected agent | Every route but `/healthz` needs the run token or a session made from it. The token is 256 bits from `crypto/rand`, generated at start, compared in constant time, never taken from the command line or the environment (so it is in no `/proc/<pid>/cmdline` or `environ`) and printed only on the first line of standard output. Failed attempts are throttled for the whole server (10 a minute), without evaluating the attempt once the limit is reached, so the token cannot be guessed at line rate; sessions already made are not slowed. Attempts that arrive from another site (a page navigating the browser to the address) are counted on their own, so a page open in the same browser cannot use up the person's attempts. |
| The browser opener's argument list | `--open` starts the platform's opener with the address as an argument, and every process of the user can read an argument list. The address it gets carries a *launch code*, not the run token: 128 random bits, accepted once, within 30 seconds, on the page URL only; it is no bearer credential, a rotation ends it, and a reuse counts as a failed attempt. The window in which an agent that watches process listings could use it is the time between the opener starting and the browser loading the page. |
| Cookie theft by script, fixation | The cookie holds a random session id unrelated to the token; the server maps it to a session that ends after 24 hours without use (a request in the second half of that time moves its end a full 24 hours ahead and sends the cookie again, and an open page asks every half hour, so it is not signed out of a long run; the server binds to loopback only, and the run token that starts a session is valid for the whole run anyway), a logout and a rotation (`POST /api/auth/rotate` ends every session and the token itself). It is `HttpOnly` and `SameSite=Strict`, and `Secure` over TLS. The token in the address is accepted on `/` only (never on an API path), becomes a cookie, and the redirect that removes it always goes to `/` on the same origin. |
| DNS rebinding | The `Host` header must be a loopback name or address on the port that was bound; anything else is 403. |
| Cross-site forgery | Every request that is not GET or HEAD must carry an `Origin` equal to the server's own (scheme, host and port), `Sec-Fetch-Site` must not say `cross-site` or `same-site` (another port of the same host is same-site), a custom header `X-Sleipnir-Web: 1` (which forces a CORS preflight the server never answers), and a JSON content type when there is a body. A request with neither `Origin` nor fetch metadata is not from a browser and is accepted only with the bearer token, never with the cookie. GET routes do not change state. Subresource requests from other sites to GET routes are refused as well. |
| A request or a client that costs too much | Header size, body size (per route, 64 KiB unless a route says otherwise), requests in flight, open streams, outstanding confirmations, sessions, topics, event size and every per-subscriber queue are bounded; connections have header and read timeouts; every response has a write deadline (a stream refreshes its own on each write); a client that stops reading is cut off. |
| Paths, directories and ids the page names | A file path is a project-relative path in clean slash form (`cleanRel`: no absolute or drive form, no `..` that leaves the project, no NUL, backslash or control character, nothing `filepath.IsLocal` refuses) before anything else looks at it, and the checkpoint store tests the key it derives from it again. A directory for a resumed session or a trust request must be one of the server's listed projects, tabs or trust-ledger entries, and the server goes on with its own spelling of the match, never the page's. A new session, a restart's `--cwd` and a scheduled job (which is only added with the confirmation of its own scope) may name any folder that exists (and a new session one that the request makes, with a git repository in it): a folder that is not a listed project, a folder to make and a repository to make are reasons of the request's confirmation, shown whole, and nothing is made before it is given; project files in the folder still need the trust step, and what the folder holds is read only as folder names by the completion route (names with a control or reordering character are never offered). The route is bounded (four listings at once, 60 names, 5,000 entries read, four seconds). A session is named by an id of the shape `newID` makes, or `latest`. Permission rules and the "Would it ask?" tester take arbitrary patterns, paths and commands by design: the engine reads only link metadata and directory listings to judge them, and answers with a verdict. |
| Injected markup | The Content-Security-Policy allows scripts, styles, fonts, images and connections from the server's own origin only (inline style attributes and `data:` images excepted), and forbids framing, forms and `<base>`; `nosniff`, frame denial, no referrer, same-origin opener and resource policies and `no-store` are on every response, errors and streams included. JSON is HTML-escaped. The page is built from files in the binary, which a test reads for inline script, inline handlers and remote references. |
| Privilege escalation through the page | What raises privilege (bypass or yolo mode, trusting a project, an allow rule the session did not have, removing a deny or ask rule, a verify command, approving a tool server, adding a schedule, updating the binary, restoring files, and a command started from the page whose flags widen its own privileges) requires a second step: a confirmation id that is single use, valid for a minute, bound to the session that asked and to one scope. The decision is taken where the effect happens, on the final settings (typed flags over staged launch settings, the parsed value of every flag as Go's flag package reads it), so a slash line, a staged setting or a restart cannot get around it: the server answers 428 with the scope and the reasons, and the page asks the person. |
| What a question shows | A permission question shows the whole command (up to the shell tool's own limit) and the whole change of an edit (up to 256 KiB), with control and bidirectional characters made visible. A command or change that is longer than the page can show is refused without asking, with the reason sent to the model; a value shaped like a secret is never sent to the page. A question waits for its answer; it is refused only after `--ask-timeout` or when no page has had the interface open for `--ask-grace`, when either is set. |
| Provider keys in commands the page starts | A command that needs the provider keys (the doctor, `models`, `run`, `swarm`, `rl`, scheduled jobs) receives them on an inherited pipe that it reads first thing in `main`, never in its environment, so they are not in `/proc/<pid>/environ` for the life of the command. A run that names its own endpoint (`--base-url`, `--api-key-env`, `--policy-host`) gets no keys. In the moment between the child starting and reading the pipe, a process of the same user could still open `/proc/<child>/fd/N`; that window is much narrower than the whole run. |
| Leaks through logs and errors | Logs never carry bodies, query strings, the token, cookie values, confirmation ids or held provider keys (they are masked wherever they appear); a request cannot forge log lines; a panic is a generic 500 and a masked log line; error bodies are short, uncacheable JSON with no paths, no credentials and no body content (a rejected field is named, bounded and escaped). |

**What the token protects.** Whoever holds it can do everything the page can do: read sessions, start agents, answer
questions, change settings. It is a capability for the user's own browser and scripts, not a login among several users.
`POST /api/auth/rotate` is the way to cut off every other holder (a shared screen, a pasted address): it ends all sessions and
replaces the token without telling anyone the new one, and the browser that asked keeps working.

**What it does not protect.**

* **Another process of the same user** can read the terminal that printed the address, a file that standard output was
  redirected to, the browser's cookie store and the browser's memory. Do not redirect the first line to a file an agent can read,
  and do not run the command under a terminal recorder you do not control. The harness's own hardening (section 2) keeps the
  token out of `/proc/<pid>/environ` and out of the memory of commands the model runs, not out of the terminal.
* **Cookies are not isolated by port.** A browser sends the cookie to every port of the same host name. Another web server on
  `localhost` or `127.0.0.1` that the user visits in the same browser profile receives it. `Origin` checks stop that server's pages
  from acting through the browser, but a program that copies the cookie can replay it, since a non-browser client chooses its own
  headers. Use a browser profile for this page, and rotate the token if another local server was visited.
* **A local process can use up the sign-in throttle.** Eleven wrong tokens in a minute from any process of the machine make the next
  sign-in with the right token wait out the minute (the 429 says how long). It cannot sign in, and sessions that exist are not
  affected; a page of another site is counted apart and cannot do this through the browser.
* **A confirmation is not a second credential.** Anything that can use the cookie can obtain a confirmation. It stops forged,
  replayed and mistaken requests and makes escalation an explicit act; it does not stop a holder of the session.
* **Plain HTTP.** On a network path the token and the cookie are visible. Use an SSH tunnel, not a bind to another address.
* **Sessions run with the user's rights.** Everything in sections 1 to 4 applies to the agents the page starts, including the
  modes that turn asking off. The page makes them easier to start; it does not change what they can do.
* **Errors produced by Go's HTTP server before a handler runs** (a malformed request line, headers over the cap) are plain
  responses without these headers.

## 6. Reading the code

Process hardening: `internal/harden`. Permissions: `internal/perm`. Shell: `internal/tools/shell` (`env.go`, `jobs.go`). Web interface: `internal/web` (`doc.go`, `auth.go`).
Files: `internal/tools/fs`. Web: `internal/tools/web` (`guard.go`). Instruction files: `internal/memory`. State:
`internal/events`, `internal/checkpoint`. Regression tests accompany these packages.
