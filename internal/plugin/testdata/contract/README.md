# Plugin API contract fixtures

Each directory under `v<N>/` is a plugin written against plugin API version N, using every part of that version's contract
(`internal/plugin/compat.go`). `TestPluginContract` loads each one as Sleipnir does and compares what it reads (the manifest,
the hooks, the MCP servers, the problems `plugin.Check` reports) with `golden.json` in the same directory.

A failing comparison means a change to Sleipnir reads a plugin of an existing API version differently: it would break plugins
people have installed. Do not update the fixtures or the golden files of a released version to make the test pass. Keep the
old behaviour for that version (a shim keyed on `Manifest.API`), and put the new behaviour in a new API version with fixtures of
its own (`v<N+1>/`), raising `APIVersion`.

`newer/` holds plugins written for an API this Sleipnir does not read yet: they must fail with a reason that says to update.

`go test ./internal/plugin -run TestPluginContract -update` writes the golden files of a new version's fixtures.
