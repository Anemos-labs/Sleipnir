<h1 align="center">
  <picture>
    <source media="(prefers-color-scheme: dark)" srcset="docs/media/logo-wordmark-dark.svg">
    <img src="docs/media/logo-wordmark.svg" alt="Sleipnir" width="620">
  </picture>
</h1>

A terminal coding agent for local and hosted models. Work with one agent or let a
manager divide a task among workers, review their results, and verify changes.

[![CI](https://github.com/Anemos-labs/Sleipnir/actions/workflows/ci.yml/badge.svg?branch=main)](https://github.com/Anemos-labs/Sleipnir/actions/workflows/ci.yml)
[![CodeQL](https://github.com/Anemos-labs/Sleipnir/actions/workflows/codeql.yml/badge.svg?branch=main)](https://github.com/Anemos-labs/Sleipnir/actions/workflows/codeql.yml)
[![Release](https://img.shields.io/github/v/release/Anemos-labs/Sleipnir?sort=semver)](https://github.com/Anemos-labs/Sleipnir/releases)
[Releases](https://github.com/Anemos-labs/Sleipnir/releases) ·
[Getting started](docs/GETTING-STARTED.md) · [Configuration](docs/CONFIGURATION.md)

## Install

Download the archive for your platform from [Releases](https://github.com/Anemos-labs/Sleipnir/releases),
then follow the [installation steps](docs/GETTING-STARTED.md#install).
Or, with Go 1.25 or newer on Linux or macOS:

```sh
GOBIN="$HOME/.local/bin" go install github.com/anemos-labs/sleipnir/cmd/sleipnir@latest
export PATH="$HOME/.local/bin:$PATH"
sleipnir --help
cd your-project
sleipnir
```

Save the PATH setting in your [shell startup file](docs/GETTING-STARTED.md#release-archive)
to keep it available in new terminals. [Windows and troubleshooting](docs/GETTING-STARTED.md#install)

The first run asks for a provider and model. Use `sleipnir login chatgpt` for a
ChatGPT subscription, or `sleipnir login` for a provider API key. Local model
servers are supported too. See [Providers](docs/PROVIDERS.md).

## Work

Type a task in chat. The default team is one manager and up to eight workers;
workers start when the manager needs them. The manager plans, delegates and
reviews, and edits no file itself. `--swarm N` sets the number of workers, and
`sleipnir --swarm 0` is a single agent.

For work that needs several passes, enter:

```text
/goal Implement pagination, add tests, and verify the examples in the README.
```

The harness checks the result after each turn and continues when evidence is
missing. Escape interrupts the turn and pauses the goal. `/goal resume` continues it.

| Control | Action |
|---|---|
| `/model`, `/roles` | Choose models |
| `/goal` | View the goal and its plan |
| `/goal pause` | Interrupt and pause the goal |
| Ctrl+G or `/agents` | Open the live team view |
| Ctrl+T or `/stats` | Open live usage and cache statistics |
| `/diff`, `/rewind` | Inspect or restore a checkpoint |
| `/help` | List commands |

Headless commands:

```sh
sleipnir run "fix the failing test"
sleipnir swarm 3 "implement the API and client" --verify "go test {dirs}"
sleipnir --continue
```

Permissions control edits and commands. Review each project's trust settings
before allowing its hooks, configuration, or tool servers. [Security](docs/SECURITY.md)

## Browser interface

`sleipnir web` serves the same sessions in a browser on this machine:

```sh
sleipnir web --open
```

The page shows the team at work, asks the permission questions, and has the workspace (files, changes and
checkpoints), the settings and the tools of the command line. It listens on loopback only and prints an address whose
token is valid for that run. [Browser interface](docs/UX.md#web-interface) · [Command](docs/CLI.md#sleipnir-web) ·
[Security](docs/SECURITY.md#5-the-web-interface-sleipnir-web)

## Cache behavior

Sleipnir keeps shared instructions stable and manages long conversations through
compaction. Cache reuse still depends on the provider, model, request boundaries,
and workload. A team does not guarantee shared cache hits or lower cost.

The statistics panel uses provider-reported token usage. Cache expectations are
estimates; a stable internal prefix does not prove that the API cached it.
Simulation results are not measured savings. [Cache design](docs/CACHE-DESIGN.md) ·
[Validation](docs/VALIDATION.md)

## Documentation

- [Getting started](docs/GETTING-STARTED.md), [CLI reference](docs/CLI.md), [configuration](docs/CONFIGURATION.md)
- [Providers](docs/PROVIDERS.md), [extensions](docs/EXTENDING.md), [MCP](docs/MCP.md)
- [Architecture](docs/ARCHITECTURE.md), [team coordination](docs/SWARM-PROTOCOL.md), [terminal and browser interfaces](docs/UX.md)
- [Building](docs/BUILDING.md), [testing](docs/TESTING.md), [maintenance](docs/MAINTENANCE.md), [contributing](CONTRIBUTING.md)
- [Limitations](docs/STATUS.md), [roadmap](docs/ROADMAP.md), [training data](docs/TRAINING-DATA.md)

## Development

Go 1.25 or newer. Dependencies are limited to `golang.org/x/net`, `x/sys`, and `x/term`.

```sh
go build ./...
go test -race -count=1 ./...
go vet ./...
```

## License

[Business Source License 1.1](LICENSE). See the license for production-use terms
and conversion to Apache 2.0.
