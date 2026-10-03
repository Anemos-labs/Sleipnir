# Design rationale

Sleipnir combines a coding-agent loop with explicit team coordination and
reconstructible session records.

A manager can divide independent work among scoped workers, then review and
verify their results. Standing goals add an evidence check after each turn so
an incomplete answer can trigger another pass.

Stable project context can reduce repeated orientation. Compaction bounds long
histories while retaining older content through recall. Both mechanisms have
costs: shared context is sent repeatedly, and compaction can rewrite a cached
prefix. Their value depends on the workload and provider.

Permissions, checkpoints, stale-read checks, and optional worktrees constrain
changes to the project. These are separate from the model's judgment.

The event log supports interactive progress, replay, debugging, and trajectory
export. Provider usage remains the source for observed cache hits; simulations
help compare policies under declared assumptions.

[Architecture](ARCHITECTURE.md) · [Cache design](CACHE-DESIGN.md) ·
[Team protocol](SWARM-PROTOCOL.md)
