package mdfile

import (
	"os"
	"path/filepath"
)

// Scope says who a definition came from.
type Scope string

const (
	// ScopeProject definitions are part of the repository: untrusted until the
	// user trusts the project.
	ScopeProject Scope = "project"
	// ScopeUser definitions live under the user's home directory and are trusted.
	ScopeUser Scope = "user"
	// ScopeExtra definitions come from a directory the caller supplied (a plugin).
	ScopeExtra Scope = "extra"
)

// Extra is a caller-supplied directory of definitions, such as an installed
// plugin's skills/, commands/ or agents/ directory.
type Extra struct {
	// Dir holds the definitions directly.
	Dir string
	// Namespace, when set, prefixes every name from this directory ("plugin:name"),
	// so plugins cannot shadow each other or the user's own definitions.
	Namespace string
	// Trusted says the user vouches for this directory (a plugin they installed).
	// Untrusted extras are handled like project content: loaded only when the
	// project is trusted.
	Trusted bool
}

// Layout says where definitions may live.
type Layout struct {
	// Root is the project root; empty means there is no project scope.
	Root string
	// Home is the user's home directory; empty means the current user's.
	Home string
	// TrustProject allows the repository's own directories to be read.
	TrustProject bool
	Extra        []Extra
}

// Source is one directory to load definitions from.
type Source struct {
	// Dir is the directory as spelled (absolute); Real has its symlinks resolved.
	Dir, Real string
	Scope     Scope
	Namespace string
	// Label is how the directory is shown to the user: ".claude/skills" under
	// the project, "~/.claude/skills" under the home directory, the namespace for
	// an extra. It never holds an absolute machine path.
	Label string
	// Contain is the symlink-free directory every entry's real path must stay
	// inside: the project root for project sources, the directory itself for
	// extras, empty for user sources (whose symlinks the user made).
	Contain string
}

// brands are the directory names definitions are looked for under, native
// first: a project that has both .sleipnir/skills/x and .claude/skills/x means
// the former.
var brands = []string{".sleipnir", ".claude"}

// Sources lists the directories to read for one kind of definition ("skills",
// "commands" or "agents"), highest precedence first:
//
//	<root>/.sleipnir/<kind>, <root>/.claude/<kind>,
//	~/.sleipnir/<kind>, ~/.claude/<kind>, then the extras in the order given.
//
// A repository directory is returned only when the project is trusted;
// otherwise a Warning says it was left alone (only its existence is checked). A
// directory whose real path leaves the project (for example .claude symlinked to
// /) is refused. Nothing is read from a source here; callers list and read
// entries themselves, with ReadFile's confinement.
func Sources(kind string, l Layout) ([]Source, []Warning) {
	var out []Source
	var warns []Warning

	if l.Root != "" {
		if realRoot, err := Canonical(l.Root); err == nil {
			for _, b := range brands {
				dir := filepath.Join(realRoot, b, kind)
				label := b + "/" + kind
				if !isDir(dir) {
					continue
				}
				if !l.TrustProject {
					warns = append(warns, Warnf(label, "", "not loaded: it is part of the repository and the project is not trusted"))
					continue
				}
				real, err := filepath.EvalSymlinks(dir)
				if err != nil || !Within(realRoot, real) {
					warns = append(warns, Skipf(label, "", "resolves outside the project; not loaded"))
					continue
				}
				out = append(out, Source{Dir: dir, Real: real, Scope: ScopeProject, Label: label, Contain: realRoot})
			}
		}
	}

	home := l.Home
	if home == "" {
		home, _ = os.UserHomeDir()
	}
	if home != "" {
		if realHome, err := Canonical(home); err == nil {
			for _, b := range brands {
				dir := filepath.Join(realHome, b, kind)
				if !isDir(dir) {
					continue
				}
				real, err := filepath.EvalSymlinks(dir)
				if err != nil {
					continue
				}
				out = append(out, Source{Dir: dir, Real: real, Scope: ScopeUser, Label: "~/" + b + "/" + kind})
			}
		}
	}

	for _, e := range l.Extra {
		if e.Dir == "" {
			continue
		}
		label := e.Namespace
		if label == "" {
			label = filepath.Base(e.Dir)
		}
		if e.Namespace != "" {
			if err := CheckName(e.Namespace, NameRules{Max: 64, Upper: true, Dot: true, Underscore: true}); err != nil {
				warns = append(warns, Skipf(label, "", "invalid namespace: %v", err))
				continue
			}
		}
		real, err := Canonical(e.Dir)
		if err != nil || !isDir(real) {
			continue
		}
		if !e.Trusted && !l.TrustProject {
			warns = append(warns, Warnf(label, "", "not loaded: it is not from a trusted source and the project is not trusted"))
			continue
		}
		out = append(out, Source{Dir: e.Dir, Real: real, Scope: ScopeExtra, Namespace: e.Namespace, Label: label, Contain: real})
	}
	return out, warns
}

// isDir follows symlinks and reports whether the path names a directory.
func isDir(p string) bool {
	fi, err := os.Stat(p)
	return err == nil && fi.IsDir()
}
