# Sleipnir

A terminal coding agent for local and hosted models. Work with one agent or let a
manager divide a task among workers, review their results, and verify changes.

[![CI](https://github.com/Anemos-labs/Sleipnir/actions/workflows/ci.yml/badge.svg?branch=main)](https://github.com/Anemos-labs/Sleipnir/actions/workflows/ci.yml)
[Releases](https://github.com/Anemos-labs/Sleipnir/releases) ·
[Getting started](docs/GETTING-STARTED.md) · [Configuration](docs/CONFIGURATION.md)

## Install

Download the archive for your platform from [Releases](https://github.com/Anemos-labs/Sleipnir/releases),
extract it, and put `sleipnir` on your PATH. Or install with Go:

```sh
go install github.com/anemos-labs/sleipnir/cmd/sleipnir@latest
cd your-project
sleipnir
```

The first run asks for a provider and model. Use `sleipnir login chatgpt` for a
ChatGPT subscription, or `sleipnir login` for a provider API key. Local model
servers are supported too. See [Providers](docs/PROVIDERS.md).

## Work

Type a task in chat. The default team has capacity for one manager and seven
workers; workers start when the manager needs them. Use `sleipnir --swarm 0`
for a single agent.

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
sleipnir swarm 4 "implement the API and client" --verify "go test {dirs}"
sleipnir --continue
```

Permissions control edits and commands. Review each project's trust settings
before allowing its hooks, configuration, or tool servers. [Security](docs/SECURITY.md)

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
- [Architecture](docs/ARCHITECTURE.md), [team coordination](docs/SWARM-PROTOCOL.md), [terminal interface](docs/UX.md)
- [Building](docs/BUILDING.md), [testing](docs/TESTING.md), [contributing](CONTRIBUTING.md)
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
