package memory

import (
	"path"
	"path/filepath"
)

// File is one place Load looks for an instruction file, whether or not a file is there: an editor of the instruction files lists
// these, and opens or creates the one a person picks.
type File struct {
	// Path is how Load shows the file (Source.Path): relative to the project root with "/" separators, or "~/.sleipnir/...".
	Path string
	// Scope is the scope Load gives the file (ScopeUser, ScopeProject, ScopeDir or ScopeLocal).
	Scope string
	// Dir is the directory of the file's trust domain: the canonical project root, or ~/.sleipnir.
	Dir string
	// Rel is the file relative to Dir, slash-separated.
	Rel string
	// User reports that the file is the user's own (in ~/.sleipnir), not the project's.
	User bool
}

// Files lists, in Load's order, every place Load reads an instruction file from for opts: ~/.sleipnir's SLEIPNIR.md (and MEMORY.md
// unless NoNotes), then the project's files of each directory from the root down to cwd, then the local files (and the project's MEMORY.local.md at the root unless NoNotes). Nothing is read; a
// project root that cannot be opened has no project entries. Imports are not listed: they are wherever a file names them.
func Files(opts Opts) ([]File, error) {
	ld, err := newLoader(opts)
	if err != nil {
		return nil, err
	}
	defer ld.close()
	var out []File
	add := func(d *domain, rel, scope string) {
		out = append(out, File{Path: ld.display(d, rel), Scope: scope, Dir: d.dir, Rel: rel, User: d.user})
	}
	if ld.user != nil {
		add(ld.user, "SLEIPNIR.md", ScopeUser)
		if !opts.NoNotes {
			add(ld.user, "MEMORY.md", ScopeUser)
		}
	}
	if ld.proj != nil {
		for i, d := range ld.dirs {
			scope := ScopeDir
			if i == 0 {
				scope = ScopeProject
			}
			for _, name := range projectNames {
				add(ld.proj, path.Join(d, name), scope)
			}
		}
		for i, d := range ld.dirs {
			for _, name := range localNames {
				add(ld.proj, path.Join(d, name), ScopeLocal)
			}
			if i == 0 && !opts.NoNotes {
				add(ld.proj, path.Join(d, ProjectNotes), ScopeLocal) // the memory tool's notes of scope project
			}
		}
	}
	return out, nil
}

// Abs is the file's absolute path on this machine. It is never shown to a model.
func (f File) Abs() string { return filepath.Join(f.Dir, filepath.FromSlash(f.Rel)) }

// The instruction files of every project directory, in precedence order, and the local ones.
var (
	projectNames = []string{"AGENTS.md", "CLAUDE.md", "SLEIPNIR.md", ".sleipnir/SLEIPNIR.md"}
	localNames   = []string{"SLEIPNIR.local.md", ".sleipnir/SLEIPNIR.local.md"}
)
