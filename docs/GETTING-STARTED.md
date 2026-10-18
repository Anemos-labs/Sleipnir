# Getting started

## Install

### Release archive

Download and extract the archive for your operating system and CPU from
[Releases](https://github.com/Anemos-labs/Sleipnir/releases).

On Linux or macOS, run these commands from the directory containing the extracted
`sleipnir` binary:

```sh
mkdir -p "$HOME/.local/bin"
install -m 755 sleipnir "$HOME/.local/bin/sleipnir"
export PATH="$HOME/.local/bin:$PATH"
sleipnir --help
```

For Bash, add the `export PATH` line to `~/.bashrc` for interactive non-login
shells. Login shells read the first available file among `~/.bash_profile`,
`~/.bash_login`, and `~/.profile`; if that file does not already set this PATH
or source `~/.bashrc`, add the export there too. For Zsh, add it to `~/.zshrc`.
Installation is per user; installing as root does not install it for other accounts.

On Windows, put `sleipnir.exe` in a permanent folder, add that folder to your
user `Path` through **Environment Variables**, and open a new terminal.
Run `sleipnir --help` to verify. From its folder, PowerShell can also run it
directly with `.\sleipnir.exe --help`.

### With Go

Go 1.25 or newer is required. On Linux or macOS, install to an explicit directory
and add it to the current shell's PATH:

```sh
GOBIN="$HOME/.local/bin" go install github.com/anemos-labs/sleipnir/cmd/sleipnir@latest
export PATH="$HOME/.local/bin:$PATH"
sleipnir --help
```

Keep the `export PATH` line in your shell's startup file as described above.
On Windows, run `go install github.com/anemos-labs/sleipnir/cmd/sleipnir@latest`,
then add Go's binary directory to your user `Path` and open a new terminal.
Go uses `go env GOBIN` when set; otherwise it uses the `bin` directory under
`go env GOPATH` (normally `%USERPROFILE%\go\bin` on Windows).

### Command not found

An installed binary must be in a directory listed in PATH. If an earlier
`go install` used the default location, on Linux or macOS run:

```sh
export PATH="$(go env GOPATH)/bin:$PATH"
sleipnir --help
```

If `go env GOBIN` prints a custom directory, add that directory instead.
Keep the matching PATH setting in your shell's startup file. If the binary is
absent, rerun the install command and check its error output before continuing.

## Connect a model

Run `sleipnir` from the project directory. The setup menu selects a provider,
authenticates it, and lists available models.

For a ChatGPT subscription:

```sh
sleipnir login chatgpt
```

For an API key, use `sleipnir login`. Credentials are stored under
`~/.sleipnir/`; do not put them in project files. [Providers](PROVIDERS.md)
describes local servers and custom endpoints.

## Start a task

```sh
cd your-project
sleipnir
```

Describe the result and how it should be verified. The default team is one
worker of each role alongside the manager: architect, scout, backend, frontend,
devops, tester, docs and reviewer. Capacity is not the number currently
working. `--swarm N` sets the number of workers (at least 8); use
`sleipnir --swarm 0` when you want one agent. To leave a role out or double
one, name the team with `--role-model`, one value per worker:
`--role-model backend --role-model backend --role-model frontend ...`.

A normal message runs one turn. A standing goal checks completion and continues
when more work is needed:

```text
/goal Add pagination, test boundary cases, and run the documented examples.
```

Use `/goal` to inspect progress, Escape or `/goal pause` to interrupt and pause,
and `/goal resume` to continue. The goal can also stop for missing user input,
lack of progress, errors, or its continuation limit.

A large goal can keep its brief in a file and list milestones that the judge
rules on one by one. Name the file on a line of its own, or at the end of a
one-line goal, with `Spec: PATH` (relative to the working directory, inside the
project, at most 64 KiB):

```markdown
# Spec: CSV viewer

## Milestones
1. Parses a 1 GB file in constant memory
   - `go test ./csv` passes
2. Renders a virtualised table in the web UI
3. Release v0.1.0 is published with binaries and a website
```

```text
/goal Build the CSV viewer. Spec: docs/SPEC.md
```

Milestones are read from a list under a `## Milestones` heading (or a
`Milestones:` line), from headings nested under a `## Milestones` heading, or
from headings that name one (`## Milestone 2: Web UI`, `### M2 - Web UI`). What
is written under a milestone (indented lines, paragraphs, deeper headings) is
kept as its acceptance details. Milestones may also be written in the goal
itself. The spec is read
once, when the goal is set: the judge holds the work to that copy, not to later
edits of the file. The goal is met only when one verdict confirms every
milestone with cited evidence; a "done" that leaves one out continues with the
unconfirmed milestones as what is missing. `/goal` and the web Goal sheet show
each milestone, open or met, with the judge's evidence.

## Review and control

- Ctrl+G or `/agents`: live team view.
- Ctrl+T or `/stats`: usage and cache panel; Escape returns to chat.
- `/model` and `/roles`: select the session and role models.
- `/effort`: inspect reasoning effort; `/effort high` changes it without restarting.
- `/permissions`: inspect rules. `/allow tests` allows recognized build and
  test commands for the session; it does not permit arbitrary scripts.
- `/diff`: inspect checkpoint changes. `/rewind` lists available checkpoints.
- `/help`: command reference.

Permission prompts apply to the named agent and operation. Trusting a project
allows its configuration and extensions to participate; inspect `sleipnir trust`
before saving that decision.

## Headless work

```sh
sleipnir run "fix the failing test"
sleipnir swarm 8 "implement the API and client" --verify "go test {dirs}"
sleipnir swarm 8 "implement independent modules" --isolation worktree --verify "go test {dirs}"
```

Put flags before the task if your shell or command wrapper requires it.
`{dirs}` scopes verification to a task's directories. A repository-wide verifier
can fail on another worker's unfinished changes.

## Resume

```sh
sleipnir sessions
sleipnir --continue
sleipnir chat --resume SESSION_ID
```

A resumed goal is paused; use `/goal resume` after reviewing its state.
For a worktree-isolated team, resume from the original working directory with
the same isolation and integration mode. Worker conversations, worktrees, and
unfinished assignments are restored; workers start idle so the manager can
continue their tasks. Older sessions without isolation recovery metadata cannot
be resumed. See [session recovery](EXTENDING.md#6-sessions-list-resume-continue)
for recovery requirements and interrupted edits.

## Inspect usage

The stats panel reports tokens returned by the provider. Cache hits are the
cached share of input tokens, not the share of requests that hit. Prices may be
unknown, and estimated savings are not an invoice.

Use `sleipnir doctor --model PROVIDER/MODEL` to test an endpoint. Probes send
model requests and consume the connected account's allowance.
