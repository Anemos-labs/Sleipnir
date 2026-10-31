package perm

import (
	"bytes"
	"encoding/hex"
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
	return submoduleHelperConfig(top, own, common, 0, &budget)
}

// maxSubmoduleConfigs bounds the submodule repositories gitHelperConfig reads; a tree with more is judged as naming a program.
const maxSubmoduleConfigs = 64

// submoduleHelperConfig is gitHelperConfig for the submodules of the work tree top (whose repository is own, per worktree, and
// common, shared): the paths its .gitmodules names, every repository under own/modules and common/modules (a linked worktree keeps
// its submodules' repositories in its own directory), and, nested, theirs. A gitlink in the work tree's index that .gitmodules does
// not name, a depth or a count past the bounds, or a .gitmodules that cannot be read is judged as naming a program.
func submoduleHelperConfig(top, own, common string, depth int, budget *int) string {
	b, err := readGitMeta(filepath.Join(top, ".gitmodules"))
	switch {
	case errors.Is(err, fs.ErrNotExist):
		b = nil
	case err != nil:
		return "a .gitmodules that could not be read (" + err.Error() + ")"
	}
	paths := gitmodulesPaths(b)
	if own == "" {
		own = common
	}
	links, ok := indexGitlinks(filepath.Join(own, "index"))
	if !ok {
		return "a submodule its .gitmodules does not name"
	}
	named := map[string]bool{}
	for _, p := range paths {
		named[filepath.ToSlash(filepath.Clean(filepath.FromSlash(p)))] = true
	}
	for _, l := range links {
		if !named[l] {
			return "a submodule its .gitmodules does not name"
		}
	}
	seen := map[string]bool{common: true, own: true}
	check := func(o, c, subTop string) string {
		if seen[c] {
			return ""
		}
		seen[c] = true
		if *budget--; *budget < 0 || depth >= 4 {
			return "more submodules than are checked"
		}
		if why := repoHelperConfig(o, c); why != "" {
			return "a submodule's " + why
		}
		if subTop != "" {
			return submoduleHelperConfig(subTop, o, c, depth+1, budget)
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
	dirs := []string{own}
	if common != own {
		dirs = append(dirs, common)
	}
	for _, g := range dirs {
		if why = walkModules(filepath.Join(g, "modules"), check, budget); why != "" {
			return why
		}
	}
	return ""
}

// walkModules runs check on each repository under mods, a modules/ directory, and under theirs.
func walkModules(mods string, check func(o, c, subTop string) string, budget *int) string {
	var why string
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

// indexGitlinks are the paths of the entries of mode 160000 (a submodule) in a git index, slash-separated as git writes them, and
// whether the index could be read through. An index that is not there holds none: git status then has no submodule to look into
// either. One too large to read here, in a format not read here, or split with a shared index that holds a gitlink, is reported
// as unreadable, which the caller judges as holding a gitlink no .gitmodules names.
func indexGitlinks(p string) ([]string, bool) {
	f, err := openNoWait(p)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, true
	} else if err != nil {
		return nil, false
	}
	defer f.Close()
	if fi, err := f.Stat(); err != nil || !fi.Mode().IsRegular() {
		return nil, false
	}
	const maxIndex = 64 << 20
	b, err := io.ReadAll(io.LimitReader(f, maxIndex+1))
	if err != nil || len(b) > maxIndex {
		return nil, false
	}
	// The object IDs are SHA-1's 20 bytes or SHA-256's 32, which the index does not record: it is read with each width, and
	// only a width that reads it through to its trailing checksum is taken.
	var links []string
	ok := false
	for _, w := range []int{20, 32} {
		l, shared, good := parseIndexGitlinks(b, w)
		if !good {
			continue
		}
		if shared != "" {
			sb, err := readGitMeta(filepath.Join(filepath.Dir(p), "sharedindex."+shared))
			if err != nil || bytes.Contains(sb, []byte("\x00\x00\xe0\x00")) {
				return nil, false
			}
		}
		links, ok = append(links, l...), true
	}
	return links, ok
}

// parseIndexGitlinks reads a git index (versions 2 to 4) with object IDs w bytes long: the paths of its gitlink entries, the hex
// name of the shared index a split index points to, and whether it read through.
func parseIndexGitlinks(b []byte, w int) (links []string, shared string, ok bool) {
	be32 := func(o int) uint32 { return uint32(b[o])<<24 | uint32(b[o+1])<<16 | uint32(b[o+2])<<8 | uint32(b[o+3]) }
	if len(b) < 12+w || string(b[:4]) != "DIRC" {
		return nil, "", false
	}
	ver, n := be32(4), int(be32(8))
	if ver < 2 || ver > 4 {
		return nil, "", false
	}
	end, o, prev := len(b)-w, 12, ""
	for i := 0; i < n; i++ {
		start := o
		if o+40+w+2 > end {
			return nil, "", false
		}
		mode := be32(o + 24)
		o += 40 + w
		flags := int(b[o])<<8 | int(b[o+1])
		o += 2
		if ver >= 3 && flags&0x4000 != 0 {
			if o+2 > end {
				return nil, "", false
			}
			o += 2
		}
		var name string
		if ver == 4 { // the path is the previous one less a varint's count of bytes, then the bytes up to a NUL
			strip := 0
			for {
				if o >= end || strip > len(prev) {
					return nil, "", false
				}
				c := b[o]
				o++
				strip = strip<<7 | int(c&0x7f)
				if c&0x80 == 0 {
					break
				}
				strip++
			}
			nul := bytes.IndexByte(b[o:end], 0)
			if strip > len(prev) || nul < 0 {
				return nil, "", false
			}
			name = prev[:len(prev)-strip] + string(b[o:o+nul])
			o += nul + 1
		} else { // the path up to a NUL, padded with NULs to a multiple of 8 bytes from the entry's start
			nul := bytes.IndexByte(b[o:end], 0)
			if nul < 0 {
				return nil, "", false
			}
			name = string(b[o : o+nul])
			o = start + (o+nul+1-start+7)/8*8
			if o > end {
				return nil, "", false
			}
		}
		prev = name
		if mode == 0o160000 {
			links = append(links, name)
		}
	}
	for o < end { // extensions: a 4-byte signature and a 4-byte size
		if o+8 > end {
			return nil, "", false
		}
		sig, size := string(b[o:o+4]), int(be32(o+4))
		o += 8
		if size < 0 || size > end-o {
			return nil, "", false
		}
		if sig == "link" {
			if size < w {
				return nil, "", false
			}
			if h := b[o : o+w]; !bytes.Equal(h, make([]byte, w)) {
				shared = hex.EncodeToString(h)
			}
		}
		o += size
	}
	return links, shared, o == end
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
