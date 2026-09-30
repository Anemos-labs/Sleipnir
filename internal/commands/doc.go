// Package commands loads custom slash commands: markdown files whose body is a
// prompt template, in the layout Claude Code users already have.
//
// # Layout
//
// A command is a file <commands>/<name>.md; each directory level below the
// commands directory adds a "dir:" prefix, so .claude/commands/frontend/lint.md
// is "frontend:lint". The directories are searched in this order, and the
// first command of a name wins (a collision is reported as a Warning):
//
//	<root>/.sleipnir/commands   <root>/.claude/commands      project
//	~/.sleipnir/commands        ~/.claude/commands           user
//	Opts.Extra                                                plugins, namespaced
//
// Optional frontmatter gives description, argument-hint, allowed-tools and
// model. Names that Opts.Builtins reserves (help, clear, compact, ... unless the
// caller supplies its own list) cannot be redefined: a repository that shipped
// its own /help or /permissions could otherwise impersonate the harness.
//
// # Expansion
//
// Registry.Expand turns a command and its arguments into a prompt, in one pass
// over the template:
//
//   - $ARGUMENTS, $ARGUMENTS[N] and $1..$9 insert the arguments; without any
//     placeholder the arguments follow the text as "ARGUMENTS: ...".
//   - @path includes the file's contents, appended after the prompt as a fenced
//     block. The path is confined to the project root (symlinks resolved), the
//     file is size-capped and must be text, and a small built-in guard refuses
//     obviously sensitive names (.env, private keys); Opts.AllowRead lets the
//     caller apply the real permission policy. A file that cannot be included is
//     reported in Expanded.Notices and the mention is left as plain text.
//   - !`command` runs a shell command and inserts its output. This happens only
//     through Opts.Exec, which the caller routes through its permission engine;
//     without one the expansion fails rather than run anything. Arguments
//     substituted into a command are single-quoted word by word, so an argument
//     cannot add an operator to it.
//
// Fenced code blocks are left alone: neither includes nor commands run inside
// them, so a command that documents the syntax does not execute it.
//
// Included files and command output are inserted as data. They are never
// scanned for placeholders, includes or commands themselves, which is also why
// an include cycle cannot exist: nothing an included file says is interpreted.
// The arguments get the same treatment. Everything a model may read has been
// through the shared text hygiene (hidden Unicode and HTML comments removed).
//
// # Security model
//
// Repository-supplied commands are untrusted until the user trusts the
// project: with Opts.TrustProject false nothing under <root>/.sleipnir or
// <root>/.claude is read, and only the user's own directories are. Trust decides
// who wrote the template, not what it may do: allowed-tools is reported for the
// permission engine to intersect with its own rules and grants nothing here; a
// command's shell commands and file reads still go through the caller's
// permission checks (Opts.Exec, Opts.AllowRead).
//
// Like skills, a registry is a snapshot taken at Load: an agent that edits a
// command file mid-session changes nothing until the harness reloads.
package commands
