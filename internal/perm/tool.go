package perm

import "encoding/json"

// tool judges a request from a tool other than the shell: its declared paths
// are file accesses (writes when the request says it writes), and its network
// and write flags decide the rest.
func (ev *evaluator) tool() verdict {
	r := ev.r
	u := &unit{tool: true, req: &r, label: r.Tool}
	cw := &cwdSet{dirs: []string{ev.startDir()}}
	paths := r.Paths
	if len(paths) == 0 {
		paths = inputPaths(r)
	}
	tree, content := toolWalks(r.Tool)
	if len(paths) > maxOperands {
		paths = paths[:maxOperands]
		u.dyn = "too many paths to check"
	}
	for _, p := range paths {
		u.accesses = append(u.accesses, ev.resolve(pathUse{raw: p, write: r.Writes, tree: tree && !r.Writes, content: content && !r.Writes}, cw)...)
	}
	u.network = r.Network || classOf(r.Tool) == classWeb
	return ev.judge(u)
}

// toolWalks reports whether a file tool searches below its path argument (and
// reads contents while doing so), which is what makes a search rooted at the
// home directory a search of ~/.ssh.
func toolWalks(tool string) (tree, content bool) {
	switch normTool(tool) {
	case "grep", "search", "searchfiles", "codesearch", "ripgrep", "rg":
		return true, true
	case "glob", "find", "findfiles":
		return true, false
	}
	return false, false
}

// inputPaths recovers paths from the JSON input of a file tool that did not
// fill Request.Paths, so a forgetful tool cannot bypass path checks.
func inputPaths(r Request) []string {
	if c := classOf(r.Tool); c != classRead && c != classWrite {
		return nil
	}
	var m map[string]json.RawMessage
	if len(r.Input) == 0 || json.Unmarshal(r.Input, &m) != nil {
		return nil
	}
	var out []string
	for _, k := range []string{"path", "file_path", "filepath", "file", "filename", "notebook_path", "directory", "dir", "paths", "files"} {
		raw, ok := m[k]
		if !ok {
			continue
		}
		var s string
		var ss []string
		switch {
		case json.Unmarshal(raw, &s) == nil && s != "":
			out = append(out, s)
		case json.Unmarshal(raw, &ss) == nil:
			out = append(out, ss...)
		}
	}
	return out
}
