package perm

import (
	"sort"
	"strings"
)

// argSpec describes the command line of one read-only program well enough to
// tell its operands (files) from its options, and to refuse the options that
// would make it write or run something.
type argSpec struct {
	short    string   // short options that take a value: "nc" for head -n N -c N
	long     []string // long options that take a value (when not written --opt=value)
	deny     []string // options that disqualify the command; "--foo*" is a prefix
	pathVals []string // options whose value is a file the command reads
	noPaths  bool     // operands are not files (echo, tr, basename)
	maxPos   int      // most operands allowed (0 = unlimited); uniq's 2nd operand is an output file

	// Pattern-taking programs (grep, rg): the first operand is a pattern unless
	// one of patFlags supplies it.
	takesPattern bool
	patFlags     []string
	noPatFlags   []string // options after which there is no pattern operand (rg --files)

	// Walking directories.
	recFlags   []string // options that turn on recursion
	recDefault bool     // recursive unless told otherwise (rg)
	content    bool     // the walk reads file contents (grep, rg, diff) not just names
	tree       bool     // the walk is recursive by nature (du, tree): names only
	// namesOnly: the command looks at names and metadata, never at contents
	// (ls, stat, du, df, tree).
	namesOnly bool

	// check runs last, over the operands and the options seen.
	check func(pos []string, s *scanned) string
}

// scanned is the result of splitting a command line into operands and options.
type scanned struct {
	pos   []string
	flags map[string][]string // option -> values it was given ("" for switches)
}

// has reports whether any named flag was encountered, regardless of its values.
func (s *scanned) has(names ...string) bool {
	for _, n := range names {
		if _, ok := s.flags[n]; ok {
			return true
		}
	}
	return false
}

// values concatenates recorded flag values in the requested flag-name order.
func (s *scanned) values(names ...string) []string {
	var out []string
	for _, n := range names {
		out = append(out, s.flags[n]...)
	}
	return out
}

// inList checks for an exact string in a list.
func inList(list []string, x string) bool {
	for _, l := range list {
		if l == x {
			return true
		}
	}
	return false
}

// scan splits args. It understands clusters (-rn), attached values (-n5,
// --max=5), separate values (-n 5) for the options the spec lists, "--" and "-".
func (sp *argSpec) scan(args []string) scanned {
	s := scanned{flags: map[string][]string{}}
	endOpts := false
	for i := 0; i < len(args); i++ {
		a := args[i]
		switch {
		case endOpts || a == "-" || !strings.HasPrefix(a, "-"):
			s.pos = append(s.pos, a)
		case a == "--":
			endOpts = true
		case strings.HasPrefix(a, "--"):
			name, val, hasVal := strings.Cut(a, "=")
			if !hasVal && inList(sp.long, name[2:]) && i+1 < len(args) {
				val = args[i+1]
				i++
			}
			s.flags[name] = append(s.flags[name], val)
		default:
			for j := 1; j < len(a); j++ {
				flag := "-" + string(a[j])
				if strings.IndexByte(sp.short, a[j]) >= 0 {
					val := a[j+1:]
					if val == "" && i+1 < len(args) {
						val = args[i+1]
						i++
					}
					s.flags[flag] = append(s.flags[flag], val)
					break
				}
				s.flags[flag] = append(s.flags[flag], "")
			}
		}
	}
	return s
}

// denied returns the first disqualifying option that was given.
func (sp *argSpec) denied(s *scanned) string {
	for _, d := range sp.deny {
		if strings.HasSuffix(d, "*") {
			for f := range s.flags {
				if strings.HasPrefix(f, strings.TrimSuffix(d, "*")) {
					return f
				}
			}
		} else if s.has(d) {
			return d
		}
	}
	return ""
}

// pathUse is a file operand of a command.
type pathUse struct {
	raw   string
	write bool
	both  bool // may read and write (operands of unlisted commands)
	// noFollow: the command acts on a symlink itself (rm, rmdir), not on what
	// it points to, so only the directory part is resolved.
	noFollow bool
	tree     bool
	content  bool
	// nameOnly: only names and metadata are looked at, not contents.
	nameOnly bool
}

// analyse applies the spec to args: it returns the files the command reads, or
// a reason it is not a safe read-only invocation.
func (sp *argSpec) analyse(args []string) (uses []pathUse, why string) {
	s := sp.scan(args)
	if d := sp.denied(&s); d != "" {
		return nil, "option " + d + " can write files or run programs"
	}
	pos := s.pos
	recursive := sp.recDefault
	for _, f := range sp.recFlags {
		if s.has(f) {
			recursive = true
		}
	}
	if sp.takesPattern {
		supplied := false
		for _, f := range sp.patFlags {
			supplied = supplied || s.has(f)
		}
		for _, f := range sp.noPatFlags {
			supplied = supplied || s.has(f)
		}
		if !supplied {
			if len(pos) == 0 {
				return nil, "missing pattern"
			}
			pos = pos[1:]
		}
	}
	if sp.maxPos > 0 && len(pos) > sp.maxPos {
		return nil, "extra operand is an output file"
	}
	tree := recursive || sp.tree
	content := recursive && sp.content
	if !sp.noPaths {
		for _, p := range pos {
			if p == "-" {
				continue
			}
			uses = append(uses, pathUse{raw: p, tree: tree, content: content, nameOnly: sp.namesOnly})
		}
		if len(pos) == 0 && recursive {
			uses = append(uses, pathUse{raw: ".", tree: true, content: content, nameOnly: sp.namesOnly})
		}
	}
	for _, f := range sp.pathVals {
		for _, v := range s.values(f) {
			if v != "" {
				uses = append(uses, pathUse{raw: v})
			}
		}
	}
	// Whatever else is written as --option=value might name a file (a program's
	// undocumented "--file=~/.ssh/id_rsa"); look at those values too. Short
	// options are left alone: their values are usually delimiters ("cut -d/").
	if !sp.noPaths {
		names := make([]string, 0, len(s.flags))
		for f := range s.flags {
			names = append(names, f)
		}
		sort.Strings(names)
		for _, f := range names {
			if !strings.HasPrefix(f, "--") || inList(sp.pathVals, f) {
				continue
			}
			for _, v := range s.flags[f] {
				for _, c := range pathCandidates(v) {
					uses = append(uses, pathUse{raw: c})
				}
			}
		}
	}
	if sp.check != nil {
		if w := sp.check(pos, &s); w != "" {
			return nil, w
		}
	}
	return uses, ""
}

// safeSpecs lists the read-only programs of the default-mode allowlist that are
// understood by the generic scanner. git, go, find and the version probes have
// dedicated analysers in cmds.go.
var safeSpecs = map[string]*argSpec{
	"ls": {short: "IwT", long: []string{"ignore", "width", "tabsize", "time-style", "sort", "format",
		"time", "block-size", "hide", "indicator-style", "quoting-style"}, recFlags: []string{"-R", "--recursive"}, tree: false, namesOnly: true},
	"cat":  {},
	"head": {short: "nc", long: []string{"lines", "bytes"}},
	"tail": {short: "ncs", long: []string{"lines", "bytes", "pid", "sleep-interval"}},
	"wc":   {long: []string{"files0-from"}, pathVals: []string{"--files0-from"}},
	"stat": {short: "c", long: []string{"format", "printf"}, namesOnly: true},
	"file": {short: "mFePf", long: []string{"magic-file", "separator", "exclude", "parameter", "files-from"},
		deny: []string{"-C", "--compile"}, pathVals: []string{"-f", "--files-from"}},
	"du": {short: "dtBX", long: []string{"max-depth", "threshold", "block-size", "exclude", "exclude-from",
		"files0-from", "time-style"}, pathVals: []string{"-X", "--exclude-from", "--files0-from"}, tree: true, namesOnly: true},
	"df": {short: "txB", long: []string{"type", "exclude-type", "block-size"}, namesOnly: true},
	"tree": {short: "LPIo", long: []string{"charset", "filelimit", "sort", "timefmt", "fromfile"},
		deny: []string{"-o", "-l"}, tree: true, namesOnly: true},
	"sort": {short: "kotTS", long: []string{"key", "output", "field-separator", "temporary-directory",
		"buffer-size", "parallel", "batch-size", "compress-program", "files0-from", "random-source"},
		deny: []string{"-o", "--output", "--compress-program", "-T", "--temporary-directory"}},
	"uniq": {short: "fsw", long: []string{"skip-fields", "skip-chars", "check-chars"}, maxPos: 1},
	"cut":  {short: "bcdf", long: []string{"bytes", "characters", "delimiter", "fields", "output-delimiter"}},
	"tr":   {noPaths: true},
	"diff": {short: "UCLIxXWDF", long: []string{"label", "ignore-matching-lines", "exclude", "exclude-from",
		"width", "show-function-line", "ifdef", "line-format", "new-line-format", "old-line-format",
		"unchanged-line-format", "to-file", "from-file"}, pathVals: []string{"-X", "--exclude-from", "--to-file", "--from-file"},
		recFlags: []string{"-r", "--recursive"}, content: true},
	"cmp":       {short: "in", long: []string{"ignore-initial", "bytes"}},
	"basename":  {short: "s", long: []string{"suffix"}, noPaths: true},
	"dirname":   {noPaths: true},
	"realpath":  {long: []string{"relative-to", "relative-base"}, pathVals: []string{"--relative-to", "--relative-base"}},
	"readlink":  {},
	"which":     {noPaths: true},
	"whoami":    {noPaths: true},
	"pwd":       {noPaths: true},
	"uname":     {noPaths: true},
	"id":        {noPaths: true},
	"nproc":     {noPaths: true},
	"echo":      {noPaths: true},
	"printf":    {noPaths: true},
	"sleep":     {noPaths: true},
	"seq":       {short: "fst", noPaths: true},
	"true":      {noPaths: true},
	"false":     {noPaths: true},
	":":         {noPaths: true},
	"type":      {noPaths: true},
	"test":      {noPaths: true},
	"[":         {noPaths: true},
	"[[":        {noPaths: true},
	"wait":      {noPaths: true},
	"tac":       {},
	"rev":       {},
	"nl":        {short: "bfhilnsvw"},
	"fold":      {short: "w"},
	"paste":     {short: "d"},
	"sha1sum":   {},
	"sha256sum": {},
	"sha512sum": {},
	"md5sum":    {},
	"cksum":     {},
	"shasum":    {short: "a"},
	"date": {short: "drfIR", long: []string{"date", "reference", "file"}, deny: []string{"-s", "--set"},
		pathVals: []string{"-r", "--reference", "-f", "--file"}, noPaths: true,
		check: func(pos []string, _ *scanned) string {
			for _, p := range pos {
				if !strings.HasPrefix(p, "+") {
					return "date with an operand sets the clock"
				}
			}
			return ""
		}},
	"grep": {short: "efmABCdD", long: []string{"regexp", "file", "max-count", "after-context", "before-context",
		"context", "directories", "devices", "include", "exclude", "exclude-dir", "exclude-from", "label", "binary-files"},
		takesPattern: true, patFlags: []string{"-e", "-f", "--regexp", "--file"},
		pathVals: []string{"-f", "--file", "--exclude-from"}, deny: []string{"-R", "--dereference-recursive"},
		recFlags: []string{"-r", "--recursive"}, content: true,
		check: func(_ []string, s *scanned) string {
			for _, v := range append(s.values("-d"), s.values("--directories")...) {
				if v == "recurse" {
					return "grep -d recurse walks directories: use -r"
				}
			}
			return ""
		}},
	"rg": {short: "efgtTABCmjdMErs", long: []string{"regexp", "file", "glob", "iglob", "type", "type-not",
		"type-add", "type-clear", "after-context", "before-context", "context", "max-count", "threads",
		"max-depth", "maxdepth", "max-filesize", "max-columns", "encoding", "replace", "sort", "sortr", "colors",
		"context-separator", "field-context-separator", "field-match-separator", "path-separator", "ignore-file",
		"pre-glob", "engine", "dfa-size-limit", "regex-size-limit", "hyperlink-format"},
		takesPattern: true, patFlags: []string{"-e", "-f", "--regexp", "--file"},
		noPatFlags: []string{"--files", "--type-list"}, pathVals: []string{"-f", "--file", "--ignore-file"},
		deny:       []string{"--pre", "--hostname-bin", "-L", "--follow", "-z", "--search-zip", "--generate"},
		recDefault: true, content: true},
}
