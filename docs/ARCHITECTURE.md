# Architecture

Sleipnir assembles a provider, tools, permissions, context management, and an
event log into a coding session. A session runs either one agent or a manager
with workers.

## Request path

1. The agent applies any ready compaction or shared-context update.
2. `internal/kv` renders stable context before conversation history and team
   updates. It plans provider-supported cache boundaries.
3. A prefix guard compares the internal prompt with the previous request.
   It detects undeclared changes; it cannot prove server-side cache reuse.
4. The governor and warm gate control request admission.
5. A provider adapter renders the API payload and streams a response.
6. The event log records requests, responses, usage, and tool results.
7. Tools run subject to permissions, scopes, leases, checkpoints, and stale-read checks.

## Packages

| Package | Responsibility |
|---|---|
| `cmd/sleipnir` | CLI and session commands |
| `internal/session` | Configuration and component assembly |
| `internal/agent` | Model/tool loop, retry, budgets, compaction, snapshots |
| `internal/goal` | Standing-goal state and completion evaluation |
| `internal/kv` | Prompt layers, rendering, cache guard, compaction and archive |
| `internal/provider` | Chat Completions, Responses, Messages, probes and mocks |
| `internal/cost` | Token categories, provider cache models and pricing |
| `internal/swarm` | Tasks, workers, mail, leases, admission and verification |
| `internal/workspace`, `internal/gitx` | Worktree isolation and integration |
| `internal/tools`, `internal/perm` | Tool execution and permission enforcement |
| `internal/checkpoint` | File snapshots and rewind |
| `internal/events` | Append-only log and content-addressed blobs |
| `internal/tui` | Terminal input, rendering, live views and replay |
| `internal/web` | Browser interface: loopback HTTP server, authentication, event streams and the embedded UI; `wire` (its JSON shapes), `seam` (the interfaces between its packages) and `webtest` (fakes and a fake server) |
| `internal/config`, `internal/memory` | Settings and instruction files |
| `internal/skills`, `internal/hooks`, `internal/mcp` | Extensions |
| `internal/rl` | Rollouts, verification, rewards and trajectory exports |

## Goals and teams

A standing goal records an objective and plan. After a turn, a separate judge
uses the answer, plan, and tool evidence to decide whether to finish, continue,
or request user input. Continuations append to the conversation. Cancellation
pauses the goal. Progress and continuation limits bound the loop.

The manager creates scoped tasks and starts workers as needed. Workers share a
tool list and project context; permissions restrict their actions at execution
time. Task acceptance and optional verification belong to the harness.
Planning tasks can publish agreements reviewed by the manager before dependent
work starts. Accepted contracts are carried transitively in protected worker
assignments, independently of the bounded live note buffer.
[Team protocol](SWARM-PROTOCOL.md)

## Context and caching

Stable context precedes private history. Compaction and shared-context changes
are explicit rebases. Responses routes persist team notices so earlier message
endings remain present. Provider cache behavior is a capability, not a property
of the internal prompt alone. [Cache design](CACHE-DESIGN.md)

## Trust boundaries

Tool output, remote content, mail, and repository files are data. Project trust
gates executable configuration. Writes use permission checks, ownership checks,
checkpoints, and file-state validation. Credentials stay outside tool
environments. [Security](SECURITY.md)

## Observability

The CLI, terminal panels, inspector, and trajectory exporters read session events.
Replay reconstructs recorded prompts and state; it does not reproduce a
provider's hidden prompt processing or cache placement.
