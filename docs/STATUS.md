# Capabilities and limitations

Sleipnir provides interactive chat, single-agent and team runs, standing goals,
file and shell tools, permission rules, checkpoints, session logs, MCP, skills,
hooks, and trajectory export.

## Limitations

- Cache reuse varies by endpoint and request structure. Simulations and mock
  providers do not establish production hit rates or cost savings.
- ChatGPT subscription requests have a different feature set from API-key
  requests. Model availability comes from the signed-in account.
- A zero dollar total can mean no price is configured. Subscription quota usage
  is not measured in API dollars.
- Isolated-team resume restores worker contexts and worktrees as idle, reserving
  unfinished tasks for their workers. Sessions without isolation recovery metadata
  cannot be resumed. Interrupted checkout writes with ambiguous file state require
  manual reconciliation; recovery preserves the files and reports the branch.
- Several command-analysis paths and tests assume POSIX behavior. Windows support is
  incomplete; see `scripts/windows-excluded.txt`.
- Verification depends on the configured command and task scope. A passing
  command is evidence for what it checks, not proof of the entire objective.
- Automatic shared-context refresh and task migration between workers are not implemented.

[Roadmap](ROADMAP.md) · [Validation](VALIDATION.md)
