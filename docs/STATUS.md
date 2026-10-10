# Capabilities and limitations

Sleipnir provides interactive chat, single-agent and team runs, standing goals,
file and shell tools, permission rules, checkpoints, session logs, MCP, skills,
hooks, and trajectory export. `sleipnir web` serves the same sessions in a browser.

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
- The browser interface runs on the machine that runs the sessions and listens on loopback only; reaching it from another
  machine needs an SSH forward. A model provider is signed in on a terminal, not in the page. A team's budget changed in the
  page takes effect when the team starts again. A page that is closed does not stop the sessions: they run until the server
  stops, and a question nobody answers is refused after the grace period.
- Automatic shared-context refresh is not implemented. Tasks move between workers only
  when a worker or the manager hands one over; the stall sweep names stalled work but
  does not move or requeue it on its own.

[Roadmap](ROADMAP.md) · [Validation](VALIDATION.md)
