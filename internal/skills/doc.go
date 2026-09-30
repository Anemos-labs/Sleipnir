// Package skills discovers Agent Skills (a directory with a SKILL.md and
// optional supporting files) and serves them the way a cache-aware harness
// must: a compact listing that lives in the shared prompt layer, and skill
// bodies loaded on demand.
//
// # Layout and precedence
//
// A skill is a directory <skills>/<name>/SKILL.md whose YAML (or JSON)
// frontmatter has name, description, allowed-tools, disable-model-invocation,
// user-invocable, argument-hint, model, context and agent (see Skill). The
// directories are searched in this order, and the first skill of a name wins:
//
//	<root>/.sleipnir/skills   <root>/.claude/skills      project
//	~/.sleipnir/skills        ~/.claude/skills           user
//	Opts.Extra                                            plugins, namespaced
//
// A name collision is reported as a Warning naming both files; the same
// directory reached twice (one brand symlinked to the other) is not a collision.
// Sibling files in a skill directory are supporting files: they are listed when a
// skill is loaded and read on demand (Catalog.ReadFile), never up front.
//
// # Progressive disclosure
//
// Only Catalog.Listing is always in context: one "name: description" line per
// skill, rendered deterministically, truncated to a token budget with a visible
// marker. Everything else waits for Catalog.Load, which the harness exposes as a
// tool. A listing is a function of the catalog alone, so it is byte-stable
// across runs and safe to put in a cached layer, provided the estimator passed
// in is itself stable (pass core.NewBytesEstimator() without feeding it
// observations).
//
// # Security model
//
// Repository-supplied skills are untrusted until the user trusts the project.
// With Opts.TrustProject false nothing under <root>/.sleipnir or <root>/.claude
// is read (a Warning says it was left alone); the user's own directories are
// trusted, including symlinks the user made into a dotfiles checkout.
//
// Trust decides who wrote the text, not what it may do. Nothing in a skill
// grants a permission: allowed-tools is reported for the permission engine to
// intersect with its own rules, and disable-model-invocation must be honoured by
// the model-facing tool through Catalog.LoadForModel.
//
// Reads are confined. A SKILL.md, or a supporting file, whose real path leaves
// the skill directory is refused, so a repository cannot commit a symlink named
// SKILL.md that points at ~/.ssh/id_ed25519; a project skill directory that
// resolves outside the project is refused; FIFOs, devices and oversized or
// binary files are rejected without blocking. Supporting files are addressed by
// relative slash paths, and ".." or absolute paths are errors.
//
// Text is cleaned before any model sees it: bidi controls, Unicode tag
// characters and zero-width runs (which no editor shows, but a model reads) and
// HTML comments (which GitHub renders invisibly) are removed, and a Warning
// reports it when what was removed has no innocent explanation. Descriptions,
// the only part that is always in context, are also forced onto one line and
// have angle-bracket markup that could pass for the harness's own framing
// neutralised.
//
// A catalog is a snapshot: bodies and permission-relevant metadata are read once
// at discovery, so a skill edited during a session (by a compromised agent, say)
// changes nothing until the harness rediscovers at a session boundary. Protect
// .sleipnir/ and .claude/ from agent writes in the permission engine.
package skills
