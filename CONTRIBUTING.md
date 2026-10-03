# Contributing

Use Go 1.25 or newer. See [building](docs/BUILDING.md) for dependencies and code
conventions, [testing](docs/TESTING.md) for test coverage, and
[repository setup](docs/REPO-SETUP.md) for CI and release configuration.

```sh
go build ./...
go test -race -count=1 ./...
go vet ./...
make check
```

Submit changes by pull request with a conventional title such as
`fix(cache): preserve response message boundaries`. Include the user-visible
behavior, relevant validation, and any compatibility or cost implications.

Use [AGENTS.md](AGENTS.md) for the branch-to-merge workflow. Read bot findings and
CI results on the latest commit, fix valid issues, and resolve discussions with
evidence. Required checks and main protection apply equally to human and agent
changes. Reviews do not authorize unrelated edits or paid services.

- Add regression coverage for behavior changes. Test state and outcomes rather
  than timing or prose that mirrors the implementation.
- Keep dependencies small. Changes to `go.mod` or `go.sum` need a concrete reason.
- Treat stable prompt bytes as a cache contract. Declare changes and their cost
  in [CHANGELOG.md](CHANGELOG.md); see [cache design](docs/CACHE-DESIGN.md).
- Run changed terminal screens and inspect their rendered colors and layout.
- Keep documentation impersonal and focused on use, behavior, contracts, and
  limitations. Development journals and agent handoffs do not belong in the repository.
- Regenerate CLI reference and simulator output when their inputs change.

Report security vulnerabilities using the process in [SECURITY.md](docs/SECURITY.md).
