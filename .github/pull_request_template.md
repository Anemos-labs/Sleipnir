<!-- Title: type(scope)!: subject. Types: feat fix perf refactor docs test build ci chore revert security. The "!" marks a change that breaks users. It becomes the commit subject on main and a line of the release notes; this description becomes the commit body. -->

## What changes, and why

## Checklist

- [ ] The title is a conventional commit.
- [ ] A test that failed before this change (or why none can).
- [ ] `make check` passes (`scripts/check.sh`).
- [ ] Docs regenerated if a flag or command changed (`sh scripts/gen-cli-docs.sh`).
- [ ] Prompt bytes or tool specs changed (a golden file)? `CHANGELOG.md` has an entry priced with `sleipnir sim`.
- [ ] Security impact considered: what a hostile repository, endpoint or model output can now do.
- [ ] No secrets, keys or credential-shaped literals, not even fake ones.
