# Documentation

Every document in this directory, grouped by what it is for.

## Use

| Document | Covers |
| --- | --- |
| [Getting started](GETTING-STARTED.md) | Installing the binary, the first run, and choosing a model |
| [Providers](PROVIDERS.md) | Selecting `provider/model`, credentials, and local servers |
| [Configuration](CONFIGURATION.md) | Settings, permissions, limits, and settings shared through a repository |
| [Command-line reference](CLI.md) | Every command and flag of the binary |
| [Terminal interface](UX.md) | The chat program, its views and keys, and the web interface |
| [Storage](STORAGE.md) | Where sessions, worktrees and other state are kept, and how space is reclaimed |
| [GitHub delivery](GITHUB-DELIVERY.md) | Auto-fix of CI and reviews, and auto-merge |
| [Deep research](RESEARCH.md) | The `research` plugin: a question turned into a cited wiki |
| [Deep scan](SCAN.md) | The `scan` plugin: a team review of a whole repository |
| [Security model](SECURITY.md) | What the setup protects against, OS hardening, and reporting a vulnerability |
| [Capabilities and limitations](STATUS.md) | What is supported and its known limits |

## Extend

| Document | Covers |
| --- | --- |
| [Extending Sleipnir](EXTENDING.md) | Instruction files, skills, slash commands, agent definitions, hooks, and sessions |
| [Plugins](PLUGINS.md) | The plugin system and writing a plugin ([example](examples/plugins/hello)) |
| [MCP tool servers](MCP.md) | Connecting to tool servers, and Sleipnir as a tool server |
| [Web API](WEB-API.md) | The HTTP and event-stream interface of `sleipnir web` |

## Understand

| Document | Covers |
| --- | --- |
| [Design rationale](WHY.md) | Why the harness is built the way it is |
| [Architecture](ARCHITECTURE.md) | How a session is assembled from its parts |
| [Swarm protocol](SWARM-PROTOCOL.md) | How a manager and its workers coordinate (normative) |
| [Prompt and cache design](CACHE-DESIGN.md) | Prompt layering and what makes a cache reusable |
| [Cache policy simulation](CACHE-ECONOMICS.md) | Modeled comparisons of cache policies |
| [Parity of the interfaces](PARITY.md) | What the terminal and web interfaces must both offer |
| [Training data and RL](TRAINING-DATA.md) | The harness as a reinforcement-learning environment |
| [Roadmap](ROADMAP.md) | Unfinished product work, in priority order |

## Evaluate

| Document | Covers |
| --- | --- |
| [Benchmarking](BENCHMARKS.md) | The benchmark suite and comparisons with other harnesses |
| [Comparing coding harnesses](LANDSCAPE.md) | Dimensions for a fair comparison |
| [Endpoint validation](VALIDATION.md) | Measuring interoperability and cache behavior on real endpoints |
| [Manual acceptance checks](DOGFOOD.md) | Interaction checks run by hand |
| [Terminal recordings](GALLERY.md) | The recorded sessions and how they are made |

## Develop

| Document | Covers |
| --- | --- |
| [Building and contributing](BUILDING.md) | Toolchain, dependencies, and code conventions |
| [Testing](TESTING.md) | What each kind of test guards and what runs it |
| [Repository configuration](REPO-SETUP.md) | CI, review automation, releases, and protections |
| [Repository maintenance](MAINTENANCE.md) | Public contracts and their sources of truth |
| [Brand assets](media/README.md) | The logo files and how they are generated |
