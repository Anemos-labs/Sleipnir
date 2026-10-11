package rl

import (
	"path"
	"sort"
	"strings"
)

// uiExtensions are the file extensions of user-interface sources: markup, style
// sheets and component files.
var uiExtensions = map[string]bool{
	".html": true, ".htm": true,
	".css": true, ".scss": true, ".sass": true, ".less": true,
	".jsx": true, ".tsx": true, ".vue": true, ".svelte": true, ".astro": true,
}

// uiDirs are directory names whose contents are user-interface code or assets.
var uiDirs = map[string]bool{
	"templates": true, "static": true, "public": true, "web": true, "ui": true,
	"frontend": true, "components": true, "styles": true,
}

// IsUIPath reports whether a file path names user-interface work: a file whose
// extension is .html, .htm, .css, .scss, .sass, .less, .jsx, .tsx, .vue, .svelte or
// .astro, or any file below a directory named templates, static, public, web, ui,
// frontend, components or styles. Only directory components count for the
// directory names (web/server.go is UI work, web.go is not). Matching ignores case,
// accepts relative and absolute paths, and treats a backslash as a separator. The
// export drop rules use it (export.DropRule.EditsUI), and task generators can use
// it to tag front-end tasks the same way.
func IsUIPath(p string) bool {
	p = strings.ToLower(strings.ReplaceAll(strings.TrimSpace(p), `\`, "/"))
	if p == "" {
		return false
	}
	if uiExtensions[path.Ext(p)] {
		return true
	}
	parts := strings.Split(p, "/")
	for _, d := range parts[:len(parts)-1] {
		if uiDirs[d] {
			return true
		}
	}
	return false
}

// UIGlobs returns IsUIPath's rules as globs in Verifier.Protected's syntax, sorted:
// "*.css" and the like for the extensions, and "web/" and the like for the directory
// names (a directory of that name at any depth, and everything below it). Matched
// as that syntax matches, without regard to case, they cover every repository path
// IsUIPath accepts; they also cover the files below a directory whose own name ends
// in a UI extension, which IsUIPath does not. Task generators use them as the paths
// the frontend role owns (Team.Owns).
func UIGlobs() []string {
	out := make([]string, 0, len(uiExtensions)+len(uiDirs))
	for ext := range uiExtensions {
		out = append(out, "*"+ext)
	}
	for d := range uiDirs {
		out = append(out, d+"/")
	}
	sort.Strings(out)
	return out
}
