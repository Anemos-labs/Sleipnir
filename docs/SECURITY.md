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
and `config.local.json`, `.mcp.json`, and the skills, commands and agents under `.sleipnir` and `.claude`) is used only when the project is
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
  off (`accept-edits` already lets the build and test commands through). For a repository you do not trust, use a container, a VM or the wrapper in section 3, not the permission prompt.
* **Same-user reach.** Section 2 closes `/proc/<pid>/environ`, `/proc/<pid>/mem` and ptrace of the harness for unprivileged
  same-user commands. It does not stop root or a process with `CAP_SYS_PTRACE`; it does not hide keys that live elsewhere: the
  environment of the shell that launched the harness (`/proc/<shell pid>/environ`), dotfiles and credential files, keyrings,
  credential helpers, agent sockets by path. `/proc/<pid>/cmdline` is world-readable on every OS: never pass a key as a flag.
* **The provider sees everything you send it**, including secrets in files the model was allowed to read; the log holds it too.
* **Egress is not filtered.** `web_fetch` is guarded; `curl` in `bash` is subject to permission rules only. Prompt injection can
  exfiltrate what a command can read.
* **The environment scrub is a heuristic** (names and value shapes), not a boundary; a credential with an unremarkable name
  and shape reaches commands.
* **Worktree isolation is not a boundary either.** With `swarm.isolation: worktree` each writer edits a git worktree of its own and its work
  reaches your checkout through a verifying merge queue; the permission engine hard-denies an isolated agent's access to the rest of the
  workspace (the shared checkout, other agents' trees), in every mode, on resolved paths, for shell commands as well as file tools. That
  keeps cooperating agents apart and every merge accounted for. A path a shell computes at run time cannot be resolved before it runs
  and is left to the mode, as everywhere else, so a worker that is hostile and in `bypass` mode is not stopped by it: use a container.

### "Don't ask again" for a runner command, and for edits in the project

A yes for the rest of the session is an exact rule (`Bash(mytool build ./x)`), except for edits inside the project and for a short table of runner commands (`internal/perm/runner.go`: `go test|build|vet`, `npm test|run`, `pytest`, `cargo test|build|check`, `make`, `git add|commit|status`), where it is the prefix (`Bash(go test:*)`): a person doing test-driven work is not asked again for every package. An edit is never repeated exactly, so a yes to an edit inside the project (`Edit(<root>/**)`) is a yes to edits inside it, as accept-edits mode would give; an edit outside the project stays exact, and what the project keeps asked about (`ask` rules, the protected configuration directories) or denied (`.git`, credentials) stays so, whatever was allowed. The dialog names what it remembers ("don't ask again for "go test" commands", "...for edits in this project"), the engine tells it (`Request.Remembers`). What this adds: any arguments, among them flags such as `go test -exec`, which run a program of the model's choosing. A test run already executes whatever tests the repository has (a model that can write a test file can run anything through `go test`), so the prefix gives up little that the exact rule did not. What it does not cover stays asked about: another subcommand (`go run`), a chain (`go test ./a && rm -rf x`), a command with environment assignments or wrappers (`sudo`, `timeout`). It is never offered for shells, interpreters, `rm`, `sudo`, `curl` or anything not in the table; a no stays exact, nothing beyond the session is widened, and an unattended session has nobody to give the yes.

### A loop over files that are written out

`for f in p1/p1.go p2/p2.go; do cat "$f"; done` names its files in its list, so each command of its body is judged as the command it will be, once for
each word (`internal/perm/loopvars.go`). It is done only where it is sound: the words are plain (no variable, substitution, quote, space or shell
syntax), at most sixteen, the variable is lower case (not `PATH` or `HOME`), exactly one loop sets it and nothing else in the line assigns it, nothing
in the line can change variables behind the analysis' back (`read`, `mapfile`, `eval`, `declare`, `export`, `printf -v`, `((`, an `IFS`), and a use that
is not a plain `$f` (`${f%.go}`) stays dynamic. A loop over `~/.ssh/id_rsa`, or over a path outside the workspace, is denied or asked about like the
command it expands to; a list that is not written out (`for f in $(ls)`) asks as before.

## 2. What is scrubbed and hardened, per OS

Call `harden.Process()` first thing in `main` (`cmd/sleipnir` does). It never fails: what could not be done is in its `Status`.

| | Linux | macOS | Windows and others |
|---|---|---|---|
| Same-user process reads the harness's memory (`/proc/<pid>/mem`, ptrace) | Blocked for unprivileged processes: `prctl(PR_SET_DUMPABLE, 0)`. Not for root / `CAP_SYS_PTRACE`. Children are unaffected (`execve` resets the flag); the process can still use its own `/proc/self/{stat,status,maps,fd,exe,ns}`, only `environ` and `mem` are closed to it. Side effects: no core dumps, debuggers cannot attach | Debugger attach refused: `ptrace(PT_DENY_ATTACH)`. Best effort, compiled but not run in this repository's tests; it makes a process that is already being traced exit | Nothing |
| The harness's initial environment | Values of credential-looking variables are erased in the kernel's copy (`/proc/<pid>/environ` shows `NAME=`), even against root; Go's own environment and every child are unaffected. Tested as an unprivileged user and as root | Not erased: `ps eww` / `sysctl kern.procargs2` show the environment of a same-user process. Keep keys out of it | Not erased; a same-user process can read it through the PEB. Keep keys out of it |
| Environment handed to commands | Scrubbed by name (`api key`, `secret`, `token`, `password`, `credential`, `private key`, trailing `_KEY`, `_PAT`, `_DSN`, `AUTH`, `COOKIE`, `_PWD`, `SSH_AUTH_SOCK`) and by value (a URL with a password, PEM private keys, `sk-`, GitHub, Slack, AWS, Google and JWT token shapes); proxy URLs keep their credentials. `PassEnv` lets named variables through; `BaseEnv` replaces the environment entirely | same | same |
| ChatGPT sign-in | `sleipnir login chatgpt` keeps the sign-in (access, refresh and ID tokens, the client id OpenAI issued, a host id) in `~/.sleipnir/chatgpt.json` (mode 0600, written atomically). Only `internal/chatgptauth` reads it: the tools and child processes of a session never see a token, and the file is guarded like the other token files of the home directory (a model's read or write of it asks). The sign-in is sent to the provider's own address only: no environment variable and no project file can name another. The redirect is a loopback port that answers one request with the right state; an ID token is checked for issuer, audience, nonce and expiry (not its signature: it came straight from the token endpoint over TLS, which OpenID Connect allows to stand in for it). `sleipnir logout chatgpt` revokes the refresh token | same | same |
| Stored keys | `sleipnir login` keeps a key in `~/.sleipnir/auth.json` (mode 0600, written atomically, never in `config.json`, which people share), keyed by the provider's key variable. At start `config.LoadStoredKeys` hands them to `harden.Provide`: they live in the process's memory like a moved key, an environment variable of the same name wins, and no tool or child process sees them. The file is under `~/.sleipnir`, so a model's write to it asks in every mode. It is as safe as the user's home directory: another program running as the same user can read it, as it can read `~/.ssh` | same | same |
| Keys out of the harness's own environment | `cmd/sleipnir` calls `harden.Process(harden.MoveKeys())`: provider API keys (every variable ending in `API_KEY`, and any credential the configuration names, such as a provider's `api_key_env`) move into memory and are unset, so no child that inherits the environment (git, hooks, verifiers, MCP servers) receives them. Every reader goes through `harden.Secret` (a source-level test, `TestReadersOfCredentialsUseSecret`, fails on a new `os.Getenv` of a credential; `TestKeysLeaveTheEnvironmentButStillReachTheirReaders` runs the real code paths in a child that did what `main` does). MCP `${NAME}` references still resolve against the same set of variables as before, held keys included, and approving a project entry still lists the variables it asks for. On macOS and Windows this keeps keys out of children, not out of `ps eww` for the harness itself: the key was in the environment when the process started | same | same |
| Scratch files of commands | `TMPDIR` is `<state>/sessions/<id>/tmp` (0700) for every command of a session (not for a rollout, which brings its own environment). The permission engine treats that directory as part of the workspace and knows `$TMPDIR`, so `mktemp` and `go build -o $TMPDIR/x` need no question; a refusal of any other place says where scratch files go. A program that ignores `TMPDIR` and writes `/tmp` is still asked about | same | same |
| Process tree cleanup | Own session and process group, SIGTERM then SIGKILL of the group; strays are reaped at shutdown | same | Windows task and integration verifiers use Job Objects to terminate descendants on cancellation, timeout, and exit. Other command runners use `taskkill /T`, best effort |
| Shell used | `bash`, else `sh`; the working directory persists between calls | same | `pwsh`, `powershell` or `cmd`; no working-directory persistence, no cwd guard |
| Terminal safety of tool output | `tools.SanitizeForTerminal` strips escape sequences, C0/C1 controls, bidi and other invisible characters and CRs; the shell tool already cleans its own output as it reads it. The UI must call the helper for file, web and listing text before printing | same | same |

`SLEIPNIR_DUMPABLE=1` leaves the process dumpable (for `dlv`, `gdb`, core dumps); the environment erasure still happens.

## 3. Sandboxing harder: the shell wrapper

`shell.Options.Wrap` (`session.Options.ShellWrap` when you embed the session) is an argv prefix every model-run command
starts under: `Wrap[0] Wrap[1:]... <shell> -c <script>`. There is no command-line flag for it yet. It confines what the model
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
  Windows system-directory protection and shell analysis remain incomplete. Run the harness inside WSL2 or a Linux container
  for the Linux permission and hardening path.
* **Everywhere.** A same-user process can read what the user can read. The harness cannot change that; sandboxing does.

## 5. Reading the code

Process hardening: `internal/harden`. Permissions: `internal/perm`. Shell: `internal/tools/shell` (`env.go`, `jobs.go`).
Files: `internal/tools/fs`. Web: `internal/tools/web` (`guard.go`). Instruction files: `internal/memory`. State:
`internal/events`, `internal/checkpoint`. Regression tests accompany these packages.
