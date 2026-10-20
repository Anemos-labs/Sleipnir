---
name: install-sleipnir
description: Install and set up the Sleipnir coding harness for the person you work with, connect a model provider, and check that it runs. Use when someone asks you to install, set up or try Sleipnir.
---

# Install Sleipnir

You are a coding agent (Claude Code, Codex, or another) setting up Sleipnir on the
person's machine. Work through the steps in order, run each check, and tell the person
what you did and what is left for them. Never ask for an API key in the chat, never
write one into a file, a command line or the shell history: the person enters it
themselves (step 3).

## 1. Install the binary

Check first: `sleipnir --help` succeeds means it is installed; go to step 2.

Pick one way:

- **Release script** (Linux, macOS; no Go needed). Read it before running it, then:
  `curl -fsSL https://raw.githubusercontent.com/anemos-labs/sleipnir/main/scripts/install.sh | sh`.
  It checks the archive's checksum and installs to `~/.local/bin` (set
  `SLEIPNIR_BIN_DIR` for another directory, `SLEIPNIR_VERSION` for another release).
- **Go 1.25 or newer** (`go version`):
  `go install github.com/anemos-labs/sleipnir/cmd/sleipnir@latest`. The binary goes to
  `go env GOBIN`, or `$(go env GOPATH)/bin` when that is empty.
- **Windows**: download the archive from
  https://github.com/Anemos-labs/Sleipnir/releases, put `sleipnir.exe` in a permanent
  folder and add that folder to the user `Path`.

Then run `sleipnir --help`. If it is "command not found", the install directory is not
on PATH: add it for this shell (`export PATH="$HOME/.local/bin:$PATH"`, or Go's bin
directory) and tell the person which startup file keeps it (`~/.zshrc`, `~/.bashrc`, or
the file their login shell reads). Change a startup file only if they agree.

## 2. Find what the person already has

Do not guess a provider. Check, without printing any value:

- which key variables are set: `env | cut -d= -f1 | grep -E '_API_KEY$|^HF_TOKEN$'`
  (`HEIMDALLM_API_KEY`, `OPENROUTER_API_KEY`, `OPENAI_API_KEY`, `ANTHROPIC_API_KEY`, …;
  the full list is in docs/PROVIDERS.md, "Built-in routes");
- whether a local server runs (Ollama on `localhost:11434`, LM Studio, vLLM), if the
  person mentioned one.

Ask the person which provider they want when there is more than one choice. Heimdallm
is the project's recommended provider; a ChatGPT plan works too (`chatgpt`).

## 3. Connect the provider

A key variable already set in their shell is enough: Sleipnir reads it.

Otherwise the person stores the key themselves. Give them the command to run in their
own terminal, and wait:

- `sleipnir login heimdallm` (or `sleipnir login` to choose the provider), which asks
  for the key without echoing it and keeps it in `~/.sleipnir/auth.json`, readable by
  them only;
- `sleipnir login chatgpt` to sign in with a ChatGPT plan in the browser.

Do not pipe a key into `sleipnir login` yourself: that puts it in your transcript.

## 4. Choose a model and check it

- `sleipnir models` lists what the provider serves (`--provider NAME` for another one).
  Model names change often; pick from this list, never from memory.
- `sleipnir init --user --model PROVIDER/MODEL` writes `~/.sleipnir/config.json` with
  that default. Without `--model`, on a terminal, the person chooses in a menu; the
  first `sleipnir` run does the same when nothing is configured.
- `sleipnir doctor --model PROVIDER/MODEL` sends real requests and reports streaming,
  tool calls and prompt caching. It costs a few requests; say so before running it.
  Exit status 1 means the probe failed: read its report to the person.

## 5. Set up the project (optional)

In the person's project directory, `sleipnir init` writes `.sleipnir/config.json`
(deny rules for `.env` and `secrets/**`), a starter `AGENTS.md` if there is none, and a
`.gitignore` line for `.sleipnir/config.local.json`. It never overwrites a file. Show the
person what it wrote before they commit it.

## 6. Hand over

Tell the person, in a few lines:

- where the binary is and its version (`sleipnir --version` or the install output);
- which provider and model are configured, and that the key lives in
  `~/.sleipnir/auth.json` or their shell, not in the project;
- how to start: `cd their-project && sleipnir` opens the team chat (a manager and eight
  workers); `sleipnir --swarm 0` is a single agent; `sleipnir web` is the browser UI;
- what is left for them, if anything (a PATH line, a login, a doctor failure).

Docs: docs/GETTING-STARTED.md, docs/PROVIDERS.md, docs/CONFIGURATION.md.
