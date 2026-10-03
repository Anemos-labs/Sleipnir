# Getting started

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

Describe the result and how it should be verified. The default team can start
up to seven workers alongside the manager. Capacity is not the number currently
working. Use `sleipnir --swarm 0` when you want one agent.

A normal message runs one turn. A standing goal checks completion and continues
when more work is needed:

```text
/goal Add pagination, test boundary cases, and run the documented examples.
```

Use `/goal` to inspect progress, Escape or `/goal pause` to interrupt and pause,
and `/goal resume` to continue. The goal can also stop for missing user input,
lack of progress, errors, or its continuation limit.

## Review and control

- Ctrl+G or `/agents`: live team view.
- Ctrl+T or `/stats`: usage and cache panel; Escape returns to chat.
- `/model` and `/roles`: select the session and role models.
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
sleipnir swarm 4 "implement the API and client" --verify "go test {dirs}"
sleipnir swarm 4 "implement independent modules" --isolation worktree --verify "go test {dirs}"
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
Worktree-isolated teams do not support resume.

## Inspect usage

The stats panel reports tokens returned by the provider. Cache hits are the
cached share of input tokens, not the share of requests that hit. Prices may be
unknown, and estimated savings are not an invoice.

Use `sleipnir doctor --model PROVIDER/MODEL` to test an endpoint. Probes send
model requests and consume the connected account's allowance.
