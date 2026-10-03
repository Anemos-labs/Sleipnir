# Manual acceptance checks

Use a disposable project and an isolated Sleipnir home. A scripted endpoint is
useful for repeatable interaction checks; a real endpoint is needed for model
quality and cache validation.

| Workflow | Acceptance criteria |
|---|---|
| First run | Authentication choices are clear; local servers work without a key; failed login offers recovery |
| Ordinary task | Input remains editable; progress identifies the active operation; the final answer appears once |
| Standing goal | Plan and completion evidence are visible; missing work triggers continuation; pause interrupts promptly |
| Team | Started and active workers are distinguishable from capacity; scopes and verification are enforced |
| Approvals | Requesting agent and full operation are readable; queued typing cannot approve; canceled requests disappear |
| Panels | Statistics update during work; opening and closing preserves chat and input; narrow screens can scroll |
| Checkpoints | Diff scope is clear; rewind restores files and informs the agent |
| Resume | Model, team shape, rules, and goal state are restored accurately; unsupported cases fail clearly |
| Endpoint failure | Retry status is useful; cancellation works; no partial answer is called complete |
| Terminal | Resize, color modes, paste, long output, and keyboard focus remain usable |

For each defect, retain the minimum reproducible case in a regression test.
Keep temporary session logs outside tracked files. Report provider, model,
platform, reproduction steps, expected result, and observed result in the issue
or change description.

[Testing](TESTING.md) · [Validation](VALIDATION.md)
