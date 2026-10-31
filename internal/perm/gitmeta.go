package perm

import (
	"bytes"
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

// maxGitMeta bounds what is read of one git metadata file (a .git file, HEAD, commondir, config, a loose ref).
const maxGitMeta = 1 << 20

// readGitMeta reads a git metadata file of a directory being judged, which the agent or a repository may have prepared: only a
// regular file (following links), and at most maxGitMeta bytes, so a link to a device or a pipe neither blocks the check nor fills
// memory.
func readGitMeta(p string) ([]byte, error) {
	p, err := filepath.Abs(p)
	if err != nil {
		return nil, err
	}
	p = rootedPath(p)
	if fi, err := os.Stat(p); err != nil {
		return nil, err
	} else if !fi.Mode().IsRegular() {
		return nil, errors.New("not a regular file")
	}
	f, err := openNoWait(p)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	if fi, err := f.Stat(); err != nil || !fi.Mode().IsRegular() {
		return nil, errors.New("not a regular file")
	}
	b, err := io.ReadAll(io.LimitReader(f, maxGitMeta+1))
	if err != nil {
		return nil, err
	}
	if len(b) > maxGitMeta {
		return nil, errors.New("too large")
	}
	return b, nil
}

// gitHelperConfig says why the configuration of the repository holding dir would have a read-only git command run a program the
// repository chose, or "" when it names none: an fsmonitor hook (git status), a clean filter or a diff driver's command or textconv
// (status, diff, log -p), a signature verifier, an external diff. An include is judged as naming one, since what it pulls in is not
// read here. Such a repository may have come with the files being worked on; its git commands are then asked about.
func gitHelperConfig(dir string) string {
	var common, own, top string
	for d := filepath.Clean(dir); ; d = filepath.Dir(d) {
		if o, c, ok := gitDirs(d); ok {
			common, own, top = c, o, d
			break
		}
		if filepath.Dir(d) == d {
			return ""
		}
	}
	if why := repoHelperConfig(own, common); why != "" {
		return why
	}
	// git status and git diff look into the checked-out submodules too, with each one's own configuration.
	budget := maxSubmoduleConfigs
	return submoduleHelperConfig(top, common, 0, &budget)
}

// maxSubmoduleConfigs bounds the submodule repositories gitHelperConfig reads; a tree with more is judged as naming a program.
const maxSubmoduleConfigs = 64

// submoduleHelperConfig is gitHelperConfig for the submodules of the work tree top (whose repository is common): the paths its
// .gitmodules names, every repository under common/modules, and, nested, theirs. A gitlink in the index that .gitmodules does not
// name, a depth or a count past the bounds, or a .gitmodules that cannot be read is judged as naming a program.
func submoduleHelperConfig(top, common string, depth int, budget *int) string {
	b, err := readGitMeta(filepath.Join(top, ".gitmodules"))
	switch {
	case errors.Is(err, fs.ErrNotExist):
		b = nil
	case err != nil:
		return "a .gitmodules that could not be read (" + err.Error() + ")"
	}
	paths := gitmodulesPaths(b)
	if len(paths) == 0 && indexHasGitlink(filepath.Join(common, "index")) {
		return "a submodule its .gitmodules does not name"
	}
	seen := map[string]bool{common: true}
	check := func(own, c, subTop string) string {
		if seen[c] {
			return ""
		}
		seen[c] = true
		if *budget--; *budget < 0 || depth >= 4 {
			return "more submodules than are checked"
		}
		if why := repoHelperConfig(own, c); why != "" {
			return "a submodule's " + why
		}
		if subTop != "" {
			return submoduleHelperConfig(subTop, c, depth+1, budget)
		}
		return ""
	}
	for _, p := range paths {
		p = filepath.Clean(filepath.FromSlash(p))
		if filepath.IsAbs(p) || p == ".." || strings.HasPrefix(p, ".."+string(filepath.Separator)) {
			return "a submodule path outside the work tree"
		}
		sub := filepath.Join(top, p)
		if o, c, ok := gitDirs(sub); ok {
			if why := check(o, c, sub); why != "" {
				return why
			}
		}
	}
	// The repositories git keeps for submodules, checked out or not: .git/modules/<name>, nested as modules/<name>/modules/...
	var why string
	mods := filepath.Join(common, "modules")
	_ = filepath.WalkDir(mods, func(p string, d fs.DirEntry, err error) error {
		if err != nil || why != "" {
			return filepath.SkipDir
		}
		if d.IsDir() && p != mods && d.Name() != "modules" && isGitDir(filepath.Dir(p)) {
			return filepath.SkipDir // inside a repository, only its own modules/ holds more repositories
		}
		if d.IsDir() && d.Name() != "modules" && isGitDir(p) {
			why = check(p, p, "")
		}
		if *budget < 0 {
			return filepath.SkipAll
		}
		return nil
	})
	return why
}

// isGitDir reports whether p looks like a repository directory: it holds HEAD and config.
func isGitDir(p string) bool {
	for _, n := range []string{"HEAD", "config"} {
		if fi, err := os.Lstat(filepath.Join(p, n)); err != nil || !fi.Mode().IsRegular() {
			return false
		}
	}
	return true
}

// gitmodulesPaths are the submodule.<name>.path values of a .gitmodules file.
func gitmodulesPaths(b []byte) []string {
	var out []string
	section := ""
	for _, line := range gitConfigLines(b) {
		if line[0] == '[' {
			end := strings.IndexByte(line, ']')
			if end < 0 {
				continue
			}
			section = strings.ToLower(strings.SplitN(strings.TrimSpace(line[1:end])+" ", " ", 2)[0])
			continue
		}
		k, v, ok := strings.Cut(line, "=")
		if ok && section == "submodule" && strings.EqualFold(strings.TrimSpace(k), "path") {
			if v = strings.Trim(strings.TrimSpace(v), `"`); v != "" {
				out = append(out, v)
			}
		}
	}
	return out
}

// indexHasGitlink reports whether a git index holds an entry of mode 160000 (a submodule), by its encoded mode bytes. An index that
// cannot be read holds none here: git status then has no submodule to look into either.
func indexHasGitlink(p string) bool {
	f, err := openNoWait(p)
	if err != nil {
		return false
	}
	defer f.Close()
	if fi, err := f.Stat(); err != nil || !fi.Mode().IsRegular() {
		return false
	}
	const maxIndex = 64 << 20
	b, err := io.ReadAll(io.LimitReader(f, maxIndex+1))
	if err != nil {
		return false
	}
	if len(b) > maxIndex { // too large to read here: judged as holding one, so the check asks
		return true
	}
	return bytes.Contains(b, []byte("\x00\x00\xe0\x00"))
}

// repoHelperConfig is gitHelperConfig for one repository: own is its per-worktree directory, common its shared one.
func repoHelperConfig(own, common string) string {
	// The repository's configuration, and the per-worktree files git also reads when extensions.worktreeConfig is on. A file that
	// is there but cannot be read (too large, not a regular file) may name a program as well as any other.
	files := []string{filepath.Join(common, "config"), filepath.Join(common, "config.worktree")}
	if own != "" && own != common {
		files = append(files, filepath.Join(own, "config.worktree"))
	}
	for _, f := range files {
		b, err := readGitMeta(f)
		if errors.Is(err, fs.ErrNotExist) {
			continue
		}
		if err != nil {
			return "git configuration that could not be read (" + filepath.Base(f) + ": " + err.Error() + ")"
		}
		if why := gitConfigHelper(b); why != "" {
			return why
		}
	}
	return ""
}

// gitConfigHelper is gitHelperConfig for the text of one configuration file.
func gitConfigHelper(b []byte) string {
	section := ""
	for _, line := range gitConfigLines(b) {
		if line[0] == '[' {
			end := strings.IndexByte(line, ']')
			if end < 0 {
				return "an unreadable section of its git configuration"
			}
			name := strings.Fields(line[1:end] + " ")
			if len(name) == 0 {
				return "an unreadable section of its git configuration"
			}
			section = strings.ToLower(strings.SplitN(name[0], ".", 2)[0])
			if section == "include" || section == "includeif" {
				return "an include in its git configuration"
			}
			if rest := strings.TrimSpace(line[end+1:]); rest != "" {
				line = rest // [core] fsmonitor = x
			} else {
				continue
			}
		}
		k, v, _ := strings.Cut(line, "=")
		k, v = strings.ToLower(strings.TrimSpace(k)), strings.Trim(strings.TrimSpace(v), `"`)
		lv := strings.ToLower(v)
		if v == "" || lv == "false" || lv == "no" || lv == "off" || lv == "0" {
			continue
		}
		switch {
		case section == "core" && k == "fsmonitor",
			section == "diff" && k == "external",
			section == "filter" && (k == "clean" || k == "smudge" || k == "process"),
			section == "diff" && (k == "textconv" || k == "command"),
			section == "gpg" && k == "program":
			return "core.fsmonitor, a filter, a diff driver or a gpg program in its git configuration (" + section + "." + k + ")"
		}
	}
	return ""
}

// gitHelpers is gitHelperConfig for each directory a git command may run in: the working directories cw allows, under chdir (git -C)
// when one is given.
func (ev *evaluator) gitHelpers(cw *cwdSet, chdir string) string {
	for _, d := range cw.dirs {
		if chdir != "" {
			if filepath.IsAbs(chdir) {
				d = chdir
			} else {
				d = filepath.Join(d, chdir)
			}
		}
		if why := gitHelperConfig(d); why != "" {
			return why
		}
	}
	return ""
}

// gitLineContinues reports whether raw ends in a backslash that escapes the newline, and whether a double quote is then open. A
// backslash in a comment (from an unquoted # or ;) does not: git ends a comment at the newline, so the next line is read on its own.
func gitLineContinues(raw string, quoted bool) (cont, quote bool) {
	for i := 0; i < len(raw); i++ {
		switch c := raw[i]; {
		case c == '\\':
			if i == len(raw)-1 {
				return true, quoted
			}
			i++ // the character it escapes
		case c == '"':
			quoted = !quoted
		case (c == '#' || c == ';') && !quoted:
			return false, false
		}
	}
	return false, quoted
}

// gitConfigLines are the logical lines of a git configuration file, trimmed, without blank and comment lines: a value continued
// with a backslash at the end of a line is joined with the lines it runs on to, as git reads it, so a line inside the value that
// looks like a section header is not taken for one.
func gitConfigLines(b []byte) []string {
	var out []string
	cur, quoted := "", false
	for _, raw := range strings.Split(string(b), "\n") {
		raw = strings.TrimSuffix(raw, "\r")
		if cont, q := gitLineContinues(raw, quoted); cont {
			cur, quoted = cur+raw[:len(raw)-1], q
			continue
		}
		line := strings.TrimSpace(cur + raw)
		cur, quoted = "", false
		if line == "" || line[0] == '#' || line[0] == ';' {
			continue
		}
		out = append(out, line)
	}
	if line := strings.TrimSpace(cur); line != "" && line[0] != '#' && line[0] != ';' {
		out = append(out, line)
	}
	return out
}
