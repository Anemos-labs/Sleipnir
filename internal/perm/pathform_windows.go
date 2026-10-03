package perm

import (
	"os"
	"path/filepath"
	"strings"
)

// pathLink includes junctions, which Go reports as irregular reparse points.
// Readlink distinguishes readable targets from unrelated irregular entries.
func pathLink(fi os.FileInfo) bool { return fi.Mode()&(os.ModeSymlink|os.ModeIrregular) != 0 }

// canonicalPath recovers on-disk case and short-name aliases for existing Windows
// prefixes, without folding distinct names in case-sensitive directories. Missing
// components retain their spelling so new files can still be checked.
func canonicalPath(p string) string {
	if real, err := filepath.EvalSymlinks(p); err == nil {
		return filepath.ToSlash(real)
	}
	return p
}

// pathProblem rejects Windows names whose device, stream, or normalization
// semantics cannot be represented by ordinary filesystem permission patterns.
// Callers must reject these before expanding wildcards or appending a workspace.
func pathProblem(p string) string {
	p = filepath.ToSlash(p)
	if strings.HasPrefix(p, "//?/") || strings.HasPrefix(p, "//./") {
		return "Windows device namespaces are not supported; use an ordinary absolute path"
	}
	v := filepath.VolumeName(p)
	if !filepath.IsAbs(p) && (v != "" || strings.HasPrefix(p, "/")) {
		return "Windows drive-relative and rooted paths require an explicit drive"
	}
	for _, part := range strings.Split(p[len(v):], "/") {
		if part == "" || part == "." || part == ".." {
			continue
		}
		if strings.Contains(part, ":") || strings.TrimRight(part, ". ") != part || !filepath.IsLocal(part) {
			return "Windows streams, device names, and names ending in dots or spaces are not supported"
		}
	}
	return ""
}
