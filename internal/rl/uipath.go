package rl

import (
	"path"
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
