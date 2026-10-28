package perm

import (
	"errors"
	"io"
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
	if fi, err := os.Stat(p); err != nil {
		return nil, err
	} else if !fi.Mode().IsRegular() {
		return nil, errors.New("not a regular file")
	}
	f, err := os.Open(p)
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
	var common string
	for d := filepath.Clean(dir); ; d = filepath.Dir(d) {
		if c, ok := gitCommonDir(d); ok {
			common = c
			break
		}
		if filepath.Dir(d) == d {
			return ""
		}
	}
	b, err := readGitMeta(filepath.Join(common, "config"))
	if err != nil {
		return ""
	}
	section := ""
	for _, line := range strings.Split(string(b), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || line[0] == '#' || line[0] == ';' {
			continue
		}
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
