package swarm

import (
	"os"
	"path/filepath"
	"sort"
)

// scopeHintDirs bounds how many directories of the project root scopeHint looks into.
const scopeHintDirs = 64

// scopeHint tells the manager, in the answer to the create or spawn that set a scope, when an entry names a directory that does
// not exist under the project root but does under one of the root's own directories: scopes are relative to the project root, and
// a project made in a subdirectory of the session's directory needs that subdirectory in every scope. A manager scoped tasks to
// internal/store/** for a project in sleipnir-harness-check/, and every write of its workers was refused. It returns "" when every
// entry's directory exists, when none can be found one level down, or when no root is known; at most one entry is named.
func (s *Swarm) scopeHint(files []string) string {
	root := s.deps.Root
	if root == "" {
		root = s.deps.Workdir
	}
	if root == "" || len(files) == 0 {
		return ""
	}
	var subdirs []string
	listed := false
	for _, f := range files {
		dir := docsDirOf(f)
		if dir == "" || dir == "." || exists(filepath.Join(root, filepath.FromSlash(dir))) {
			continue
		}
		if !listed {
			listed = true
			entries, err := os.ReadDir(root)
			if err != nil {
				return ""
			}
			for _, e := range entries {
				if e.IsDir() && e.Name()[0] != '.' {
					subdirs = append(subdirs, e.Name())
				}
			}
			sort.Strings(subdirs)
			if len(subdirs) > scopeHintDirs {
				subdirs = subdirs[:scopeHintDirs]
			}
		}
		for _, sub := range subdirs {
			if exists(filepath.Join(root, sub, filepath.FromSlash(dir))) {
				return " Note: scope " + cleanText(f, 80) + " names " + cleanText(dir, 80) + ", which is not under the project root " + displayPath(root) +
					", but " + cleanText(sub+"/"+dir, 120) + " is. Scopes are relative to the project root: if the work is there, revise the scope to " +
					cleanText(sub+"/"+f, 120) + " (task revise), or its writes are refused."
			}
		}
	}
	return ""
}

// exists reports whether a path exists.
func exists(p string) bool {
	_, err := os.Stat(p)
	return err == nil
}
