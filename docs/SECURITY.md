# Sleipnir security model

Sleipnir gives a language model a shell, file tools and a network connection, on your machine and in your repositories.
This page says what that setup protects against, what it does not, what is hardened on each OS, and how to confine it
harder. It is blunt on purpose: a coding agent executes text that an attacker may have written, and nothing done inside
the process changes that. The audit trail is `docs/reviews/`; `AGENTS.md` and `docs/ARCHITECTURE.md` have the rules
the code follows.

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
instruction files until you say `--trust-project`.

**What the harness does about it**

* *Permissions are code, not a prompt* (`internal/perm`): modes (`default`, `accept-edits`, `plan`, `bypass`), allow/deny/ask
  rules, shell-syntax analysis that judges every simple command including those in substitutions, hard denies that no
  mode lifts (`~/.ssh` keys, `~/.aws`, `~/.gnupg`, `/proc/*/environ`, `.git` writes, system directories), guarded paths
  (`.env`), role profiles that can only tighten, and no prompter means deny. Every file, web and shell tool asks the engine
  before it acts; background-job tools ask before touching another agent's job, and a read-only role (reviewer, scout) is
  denied them by name. A question in `chat` takes only a line typed after it was shown (`cmd/sleipnir/chat_input.go`): text
  pasted or typed while a turn runs waits for the next prompt, so a `y` that arrived earlier cannot approve an action it was
  not typed for, and a question that Ctrl-C cancels takes nothing.
* *Tool output is data.* It arrives as `role:tool`, never as an instruction. Pins and notes are user-role context, mail is typed,
  capped and never authority (`docs/SWARM-PROTOCOL.md`). This is defence in depth for a probabilistic reader, not a guarantee.
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

**What it does not do.**

* **It is not a sandbox.** A command the mode lets through runs as you, with your files, your network, your git credentials
  and your SSH agent. The permission engine judges commands by parsing them; parsing shell is best effort. A program you
  approve (`go test`, `make`, `npm test`, `pytest`) runs the repository's code with full rights, and `bypass` mode turns asking
  off. For a repository you do not trust, use a container, a VM or the wrapper in section 3, not the permission prompt.
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

## 2. What is scrubbed and hardened, per OS

Call `harden.Process()` first thing in `main` (`cmd/sleipnir` does). It never fails: what could not be done is in its `Status`.

| | Linux | macOS | Windows and others |
|---|---|---|---|
| Same-user process reads the harness's memory (`/proc/<pid>/mem`, ptrace) | Blocked for unprivileged processes: `prctl(PR_SET_DUMPABLE, 0)`. Not for root / `CAP_SYS_PTRACE`. Children are unaffected (`execve` resets the flag); the process can still use its own `/proc/self/{stat,status,maps,fd,exe,ns}`, only `environ` and `mem` are closed to it. Side effects: no core dumps, debuggers cannot attach | Debugger attach refused: `ptrace(PT_DENY_ATTACH)`. Best effort, compiled but not run in this repository's tests; it makes a process that is already being traced exit | Nothing |
| The harness's initial environment | Values of credential-looking variables are erased in the kernel's copy (`/proc/<pid>/environ` shows `NAME=`), even against root; Go's own environment and every child are unaffected. Tested as an unprivileged user and as root | Not erased: `ps eww` / `sysctl kern.procargs2` show the environment of a same-user process, as far as we know. Keep keys out of it | Not erased; a same-user process can read it through the PEB. Keep keys out of it |
| Environment handed to commands | Scrubbed by name (`api key`, `secret`, `token`, `password`, `credential`, `private key`, trailing `_KEY`, `_PAT`, `_DSN`, `AUTH`, `COOKIE`, `_PWD`, `SSH_AUTH_SOCK`) and by value (a URL with a password, PEM private keys, `sk-`, GitHub, Slack, AWS, Google and JWT token shapes); proxy URLs keep their credentials. `PassEnv` lets named variables through; `BaseEnv` replaces the environment entirely | same | same |
| Keys out of the harness's own environment | `cmd/sleipnir` calls `harden.Process(harden.MoveKeys())`: provider API keys (every variable ending in `API_KEY`, and any credential the configuration names, such as a provider's `api_key_env`) move into memory and are unset, so no child that inherits the environment (git, hooks, verifiers, MCP servers) receives them. Every reader goes through `harden.Secret` (a source-level test, `TestReadersOfCredentialsUseSecret`, fails on a new `os.Getenv` of a credential; `TestKeysLeaveTheEnvironmentButStillReachTheirReaders` runs the real code paths in a child that did what `main` does). MCP `${NAME}` references still resolve against the same set of variables as before, held keys included, and approving a project entry still lists the variables it asks for. On macOS and Windows this keeps keys out of children, not out of `ps eww` for the harness itself: the key was in the environment when the process started | same | same |
| Process tree cleanup | Own session and process group, SIGTERM then SIGKILL of the group; strays are reaped at shutdown | same | `taskkill /T`, best effort |
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

## 4. macOS and Windows, honestly

* **macOS.** The initial environment of a same-user process is readable by other same-user processes (`ps eww`,
  `sysctl kern.procargs2`), and neither erasing nor `MoveKeys` can change what macOS reports for it; a key exported in the
  shell that launched the harness is readable there too. There is no key-file or keychain support yet, so the key has to be in
  the environment at start-up. `PT_DENY_ATTACH` stops debuggers, not `ps eww`. There are no PID namespaces: the harness stays
  visible to commands. Real mitigations are `sandbox-exec` through `Wrap` (deprecated by Apple, still works; not tested here), a
  container or VM, and short-lived, narrowly scoped keys.
* **Windows.** No hardening of the harness process at all. The shell is PowerShell or `cmd`, and the permission engine's shell
  analysis is written for POSIX syntax, so its verdicts on those command lines are not reliable. There is no tested wrapper: run
  the harness inside WSL2 (then it is Linux) or a container.
* **Everywhere.** A same-user process can read what the user can read. The harness cannot change that; sandboxing does.

## 5. Reading the code

Process hardening: `internal/harden`. Permissions: `internal/perm`. Shell: `internal/tools/shell` (`env.go`, `jobs.go`).
Files: `internal/tools/fs`. Web: `internal/tools/web` (`guard.go`). Instruction files: `internal/memory`. State:
`internal/events`, `internal/checkpoint`. Findings and their status: `docs/reviews/`.
