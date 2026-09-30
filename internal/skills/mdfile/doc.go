// Package mdfile is the shared foundation of the three markdown-based
// extension loaders (internal/skills, internal/commands, internal/agentdefs):
// it reads a markdown file with optional frontmatter safely and turns it into
// text that is fit to put in front of a model.
//
// It lives under internal/skills only because the loaders are the sole
// consumers and share nothing else; it has no dependency on them.
//
// # What it does
//
//   - Frontmatter: a strict YAML subset (see Parse) or a leading JSON object,
//     with located errors. Values are kept generic (Value) and read through
//     Fields, which accepts the spellings the Claude Code ecosystem uses
//     (allowed-tools, allowed_tools and allowedTools are one key).
//   - Safe reads (ReadFile): regular files only, size-capped, opened without
//     blocking on FIFOs, and confined: the file's real path (symlinks resolved)
//     must stay inside a caller-named directory, and the file that was opened
//     must be the file that was checked.
//   - Sources: where definitions live (project .sleipnir/ and .claude/, the
//     user's home, extra plugin directories), in precedence order, with the
//     trust gate applied.
//   - Text hygiene: hidden Unicode (bidi controls, tag characters, zero-width
//     runs) and HTML comments are removed from anything that will reach a
//     model, because a reviewer of the markdown on GitHub sees neither.
//   - Argument substitution ($ARGUMENTS, $1..$9) in a single pass, so
//     substituted text is never re-interpreted.
//
// # Security model
//
// Repository-supplied files are untrusted until the user trusts the project:
// Sources never returns a project directory unless Layout.TrustProject is set
// (the caller learns that something was skipped through a Warning, and only
// the directory's existence is inspected, never its contents). Directories
// under the user's home are trusted by default, including symlinks the user
// made into a dotfiles checkout. Extra directories are trusted only when the
// caller says the user vouches for them.
//
// Trust is about who wrote the file, not about what the file may do: nothing
// here grants a permission. Tool lists read from frontmatter are data for the
// permission engine to intersect with its own rules.
package mdfile
