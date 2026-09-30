package input

import (
	"io"
	"io/fs"
	"os"
	"path"
	"sort"
	"strings"
)

// Command is a slash command for SlashCommands to offer.
type Command struct {
	Name        string // "help" or "/help"
	Args        string // a hint for what follows the name, such as "<file>"; shown dim before the description
	Description string // one line, shown dim
}

type slashCompleter struct{ cmds []Command }

// SlashCommands completes the word at the start of a line when it starts with "/", from the commands the caller supplies (the
// editor knows none). The typed text after the slash filters and orders them with Fuzzy; with nothing typed yet they come in
// the order given. A command's Text ends with a space, ready for its arguments. Names that are empty or hold whitespace or
// control characters are ignored, so a bad list cannot put a bad candidate in the menu.
func SlashCommands(cmds []Command) Completer {
	var ok []Command
	for _, c := range cmds {
		c.Name = strings.TrimPrefix(strings.TrimSpace(c.Name), "/")
		if c.Name == "" || strings.IndexFunc(c.Name, func(r rune) bool { return r <= ' ' || r == 0x7f }) >= 0 || cleanLine(c.Name) != c.Name {
			continue
		}
		ok = append(ok, c)
	}
	return slashCompleter{cmds: ok}
}

func (s slashCompleter) Complete(line string, cursor int) (int, []Candidate) {
	if cursor < 1 || cursor > len(line) {
		return 0, nil
	}
	w := line[:cursor]
	if w[0] != '/' || strings.ContainsAny(w, " \t\U0000fffc") {
		return 0, nil
	}
	var cands []Candidate
	for _, c := range rank(w[1:], s.cmds, func(c Command) string { return c.Name }) {
		detail := c.Description
		if c.Args != "" {
			detail = strings.TrimSpace(c.Args + "  " + detail)
		}
		cands = append(cands, Candidate{Text: "/" + c.Name + " ", Display: "/" + c.Name, Detail: detail})
	}
	return 0, cands
}

const (
	// PathScanLimit is how many directory entries the Paths completer looks at in one call. A directory with more is completed
	// from its first entries only, so one keystroke never costs more than a bounded read.
	PathScanLimit = 2000
	maxPathCands  = 100
)

type pathCompleter struct {
	// open gives the file system to list and the function that releases it.
	open func() (fsys fs.FS, closeFS func(), err error)
	// rooted is true when fsys refuses symbolic links that leave it (an os.Root): links inside it are followed. Otherwise
	// every symbolic link is skipped, since nothing says where it points.
	rooted bool
}

// Paths completes "@path" words against the files under root, for mentioning a file in a prompt. The path is relative to
// root; "@src/ma" lists src/ and filters by "ma" with Fuzzy. Directories end in "/" and so keep the menu open on what is inside
// them; files end in a space. Hidden files (names that start with a dot) come last unless the typed name starts with a dot.
//
// It reads directory listings and file types only, never file contents. It works through os.Root, so nothing outside root is
// reachable: a symbolic link that leaves the tree is not followed, and is not offered; one that stays inside is offered as what
// it points to. At most PathScanLimit entries of a directory are considered and 100 candidates returned. If root cannot be
// opened or a directory cannot be read there are no candidates, never an error. Names that hold a space, control characters
// or invalid UTF-8 are not offered (an @word ends at a space, and a prompt cannot name the others reliably). Among equals,
// directories come before files. The separator is "/" on every system: a word with a backslash, or an absolute path, has no
// candidates.
func Paths(root string) Completer {
	return &pathCompleter{rooted: true, open: func() (fs.FS, func(), error) {
		r, err := os.OpenRoot(root)
		if err != nil {
			return nil, nil, err
		}
		return r.FS(), func() { r.Close() }, nil
	}}
}

// PathsFS is Paths over any fs.FS (an embedded tree, a test file system). An fs.FS cannot promise that a symbolic link stays
// inside it, so a PathsFS completer skips every symbolic link; use Paths for a directory on disk.
func PathsFS(fsys fs.FS) Completer {
	return &pathCompleter{open: func() (fs.FS, func(), error) { return fsys, func() {}, nil }}
}

type pathItem struct {
	name   string
	dir    bool
	hidden bool
}

func (p *pathCompleter) Complete(line string, cursor int) (int, []Candidate) {
	if cursor < 1 || cursor > len(line) {
		return 0, nil
	}
	ws := strings.LastIndexAny(line[:cursor], " \t\U0000fffc") + 1
	word := line[ws:cursor]
	if word == "" || word[0] != '@' {
		return 0, nil
	}
	q := word[1:]
	dir, base := "", q
	if i := strings.LastIndexByte(q, '/'); i >= 0 {
		dir, base = q[:i+1], q[i+1:]
	}
	if strings.HasPrefix(dir, "/") || strings.Contains(q, "\\") {
		return 0, nil // absolute paths and backslashes are not ours
	}
	listed := "."
	if dir != "" {
		listed = path.Clean(strings.TrimSuffix(dir, "/"))
	}
	if !fs.ValidPath(listed) { // "..", or a path that climbs out
		return 0, nil
	}
	fsys, closeFS, err := p.open()
	if err != nil {
		return 0, nil
	}
	defer closeFS()
	items := p.list(fsys, listed)
	wantHidden := strings.HasPrefix(base, ".")

	type scored struct {
		pathItem
		score int
		last  bool // sorts after the rest: a hidden name the user did not ask for
	}
	var keep []scored
	for _, it := range items {
		s, ok := Fuzzy(base, it.name)
		if !ok {
			continue
		}
		keep = append(keep, scored{it, s, it.hidden && !wantHidden})
	}
	sort.SliceStable(keep, func(i, j int) bool {
		a, b := keep[i], keep[j]
		switch {
		case a.last != b.last:
			return b.last
		case a.score != b.score:
			return a.score > b.score
		case a.dir != b.dir:
			return a.dir
		}
		return a.name < b.name
	})
	if len(keep) > maxPathCands {
		keep = keep[:maxPathCands]
	}
	cands := make([]Candidate, len(keep))
	for i, k := range keep {
		if k.dir {
			cands[i] = Candidate{Text: "@" + dir + k.name + "/", Display: k.name + "/"}
		} else {
			cands[i] = Candidate{Text: "@" + dir + k.name + " ", Display: k.name}
		}
	}
	return ws, cands
}

// list reads up to PathScanLimit entries of one directory, sorted by name.
func (p *pathCompleter) list(fsys fs.FS, dir string) []pathItem {
	if fi, err := fs.Stat(fsys, dir); err != nil || !fi.IsDir() {
		return nil // never open a file, not even to find out that it is not a directory
	}
	f, err := fsys.Open(dir)
	if err != nil {
		return nil
	}
	defer f.Close()
	rd, ok := f.(fs.ReadDirFile)
	if !ok {
		return nil
	}
	entries, err := rd.ReadDir(PathScanLimit)
	if err != nil && err != io.EOF && len(entries) == 0 {
		return nil
	}
	var items []pathItem
	for _, de := range entries {
		name := de.Name()
		if name == "" || strings.ContainsAny(name, "/ ") || cleanLine(name) != name {
			continue // a name with a space would end the @word at the space; one with control characters is not safe to offer
		}
		isDir := de.IsDir()
		typ := de.Type()
		switch {
		case typ&fs.ModeSymlink != 0:
			if !p.rooted {
				continue
			}
			fi, err := fs.Stat(fsys, path.Join(dir, name)) // follows the link, but only inside the root
			if err != nil {
				continue // it leaves the root, or points at nothing
			}
			isDir = fi.IsDir()
			if !isDir && !fi.Mode().IsRegular() {
				continue
			}
		case !isDir && !typ.IsRegular():
			continue // sockets, pipes, devices
		}
		items = append(items, pathItem{name: name, dir: isDir, hidden: strings.HasPrefix(name, ".")})
	}
	sort.Slice(items, func(i, j int) bool { return items[i].name < items[j].name })
	return items
}
