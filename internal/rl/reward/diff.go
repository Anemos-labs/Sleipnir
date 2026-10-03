package reward

import (
	"strconv"
	"strings"
)

// A unified-diff parser built for adversarial input. The diff it reads is the
// harness's own `git diff` of the agent's worktree, but its *content* is chosen
// by the policy: added lines can look like diff headers, files can have CRLF
// endings or names with quotes and octal escapes, and one diff can span
// thousands of files. The parser therefore
//
//   - trusts hunk line counts (a "+++ b/x" inside a hunk is an added line, not a
//     header) but never lets bad counts swallow real headers or drop lines;
//   - understands git's extended headers (renames, copies, modes, binary
//     patches) as well as plain `diff -u` output;
//   - strips CRs so CRLF files match the same patterns as LF ones;
//   - is a single linear pass over the text without copying it: lines are
//     substrings of the input.

// diffLine is one hunk line without its +/-/space prefix.
type diffLine struct {
	op   byte // '+', '-' or ' '
	text string
}

type hunk struct {
	oldStart, newStart int
	header             string // text after the closing @@ (git's function context)
	lines              []diffLine
}

// fileDiff is one file's change.
type fileDiff struct {
	// oldRaw/newRaw are the header paths as written (unquoted, tab-trimmed),
	// oldPath/newPath the cleaned repository-relative paths ("" for /dev/null).
	oldRaw, newRaw   string
	oldPath, newPath string
	escapes          bool // a header path leaves the repository root
	status           string
	binary           bool
	similarity       int
	hunks            []hunk
	// modeOld/modeNew hold "old mode"/"new mode" values when present.
	modeOld, modeNew string
	gitStyle         bool
	// declared marks paths set by a rename/copy line, which win over the others.
	declaredOld, declaredNew bool
}

// File status values.
const (
	statusAdded    = "added"
	statusDeleted  = "deleted"
	statusModified = "modified"
	statusRenamed  = "renamed"
	statusCopied   = "copied"
	statusMode     = "mode"
)

// touched lists the distinct cleaned paths the change involves.
func (f *fileDiff) touched() []string {
	var out []string
	if f.oldPath != "" {
		out = append(out, f.oldPath)
	}
	if f.newPath != "" && f.newPath != f.oldPath {
		out = append(out, f.newPath)
	}
	return out
}

// path is the file's name after the change (or before it, for a deletion).
func (f *fileDiff) path() string {
	if f.newPath != "" {
		return f.newPath
	}
	return f.oldPath
}

// eachLine visits every retained hunk line in order with its diff operation and text.
func (f *fileDiff) eachLine(fn func(op byte, text string)) {
	for i := range f.hunks {
		for _, l := range f.hunks[i].lines {
			fn(l.op, l.text)
		}
	}
}

// addedText joins the added lines of the file with newlines.
func (f *fileDiff) addedText() string {
	var b strings.Builder
	f.eachLine(func(op byte, text string) {
		if op == '+' {
			b.WriteString(text)
			b.WriteByte('\n')
		}
	})
	return b.String()
}

// parseDiff parses unified diff text into per-file changes.
func parseDiff(text string) []*fileDiff {
	p := &diffParser{text: text}
	p.run()
	return p.files
}

type diffParser struct {
	text  string
	pos   int
	files []*fileDiff

	cur      *fileDiff
	hk       *hunk
	oldLeft  int
	newLeft  int
	lenient  bool // hunk header could not be parsed: consume until the next header
	combined int  // >0: number of parents of a combined diff hunk
	binary   bool // inside a git binary patch payload
	header   bool // the current file is still in its header (before the first @@)
}

// nextLine consumes one diff line, removes its line ending, and reports false at EOF.
func (p *diffParser) nextLine() (string, bool) {
	if p.pos >= len(p.text) {
		return "", false
	}
	rest := p.text[p.pos:]
	i := strings.IndexByte(rest, '\n')
	var line string
	if i < 0 {
		line = rest
		p.pos = len(p.text)
	} else {
		line = rest[:i]
		p.pos += i + 1
	}
	return strings.TrimSuffix(line, "\r"), true
}

// peek reads the next diff line without advancing the parser position.
func (p *diffParser) peek() (string, bool) {
	save := p.pos
	l, ok := p.nextLine()
	p.pos = save
	return l, ok
}

// run feeds all input lines into the diff parser and finalizes the last file.
func (p *diffParser) run() {
	for {
		line, ok := p.nextLine()
		if !ok {
			break
		}
		p.feed(line)
	}
	p.finish()
}

// start finalizes the previous file, resets hunk parsing state, and registers the new file diff.
func (p *diffParser) start(f *fileDiff) {
	p.finish()
	p.cur = f
	p.header = true
	p.hk = nil
	p.oldLeft, p.newLeft, p.lenient, p.combined, p.binary = 0, 0, false, 0, false
	p.files = append(p.files, f)
}

// finish clears the active file and hunk and resolves final file paths, doing nothing when no file
// is active.
func (p *diffParser) finish() {
	if p.cur == nil {
		return
	}
	f := p.cur
	p.cur = nil
	p.hk = nil
	finalizePaths(f)
}

// finalizePaths derives cleaned paths and the status once a file is complete.
func finalizePaths(f *fileDiff) {
	oldRaw, newRaw := f.oldRaw, f.newRaw
	devOld, devNew := oldRaw == "/dev/null", newRaw == "/dev/null"
	if devOld {
		oldRaw = ""
	}
	if devNew {
		newRaw = ""
	}
	// git prefixes paths with a/ and b/; plain diffs may use anything. Strip the
	// prefixes only when both sides carry them (or the other side is /dev/null),
	// so a --no-prefix diff of a real directory named "a" is left alone.
	stripA := strings.HasPrefix(oldRaw, "a/")
	stripB := strings.HasPrefix(newRaw, "b/")
	if f.declaredOld || f.declaredNew {
		// rename/copy lines carry no a/ b/ prefixes
		stripA, stripB = false, false
	} else {
		stripA, stripB = stripA && (stripB || newRaw == ""), stripB && (stripA || oldRaw == "")
	}
	o, n := oldRaw, newRaw
	if stripA {
		o = o[2:]
	}
	if stripB {
		n = n[2:]
	}
	var e1, e2 bool
	f.oldPath, e1 = cleanRel(o)
	f.newPath, e2 = cleanRel(n)
	f.escapes = f.escapes || e1 || e2

	if f.status == "" {
		switch {
		case devOld && !devNew:
			f.status = statusAdded
		case devNew && !devOld:
			f.status = statusDeleted
		case f.oldPath != "" && f.newPath != "" && f.oldPath != f.newPath:
			f.status = statusRenamed
		case len(f.hunks) == 0 && !f.binary && f.modeOld != f.modeNew:
			f.status = statusMode
		default:
			f.status = statusModified
		}
	}
	switch f.status {
	case statusAdded:
		f.oldPath = ""
	case statusDeleted:
		f.newPath = ""
	}
}

func (p *diffParser) feed(line string) {
	if p.binary {
		if strings.HasPrefix(line, "diff --git ") || strings.HasPrefix(line, "diff --cc ") {
			p.binary = false
		} else {
			return
		}
	}
	// Inside a hunk with lines still owed, everything is content, whatever it
	// looks like. A line that cannot be hunk content ends the hunk early.
	if p.hk != nil && !p.lenient && p.combined == 0 && (p.oldLeft > 0 || p.newLeft > 0) {
		if p.hunkLine(line) {
			return
		}
		p.oldLeft, p.newLeft = 0, 0
	}
	if p.hk != nil && p.combined > 0 && p.combinedLine(line) {
		return
	}
	if p.hk != nil && p.lenient && p.lenientLine(line) {
		return
	}

	switch {
	case strings.HasPrefix(line, "diff --git "):
		f := &fileDiff{gitStyle: true, status: ""}
		a, b := splitGitHeader(line[len("diff --git "):])
		f.oldRaw, f.newRaw = a, b
		p.start(f)
	case strings.HasPrefix(line, "diff --cc "), strings.HasPrefix(line, "diff --combined "):
		f := &fileDiff{gitStyle: true}
		name := line[strings.IndexByte(line, ' ')+1:]
		name = name[strings.IndexByte(name, ' ')+1:]
		name, _ = unquoteGit(strings.TrimSpace(name))
		f.oldRaw, f.newRaw = name, name
		p.start(f)
	case strings.HasPrefix(line, "@@"):
		p.startHunk(line)
	case strings.HasPrefix(line, "--- "):
		if p.cur == nil || !p.header {
			// Plain `diff -u` has no diff --git line: a --- / +++ pair opens a file.
			next, ok := p.peek()
			switch {
			case ok && strings.HasPrefix(next, "+++ "):
				p.start(&fileDiff{})
			case p.hk != nil:
				p.extraLine(line)
				return
			default:
				return
			}
		}
		if !p.cur.declaredOld { // a rename/copy line already named the path without git's a/ prefix
			p.cur.oldRaw = headerPath(line[4:])
		}
	case strings.HasPrefix(line, "+++ "):
		switch {
		case p.cur != nil && p.header:
			if !p.cur.declaredNew {
				p.cur.newRaw = headerPath(line[4:])
			}
		case p.hk != nil:
			p.extraLine(line)
		}
	case p.cur != nil && p.header:
		p.headerLine(line)
	case p.hk != nil && len(line) > 0 && (line[0] == '+' || line[0] == '-' || line[0] == ' '):
		p.extraLine(line)
	}
}

// headerLine handles git's extended header lines.
func (p *diffParser) headerLine(line string) {
	f := p.cur
	switch {
	case strings.HasPrefix(line, "new file mode "):
		f.status = statusAdded
		f.modeNew = strings.TrimSpace(line[len("new file mode "):])
	case strings.HasPrefix(line, "deleted file mode "):
		f.status = statusDeleted
		f.modeOld = strings.TrimSpace(line[len("deleted file mode "):])
	case strings.HasPrefix(line, "old mode "):
		f.modeOld = strings.TrimSpace(line[len("old mode "):])
	case strings.HasPrefix(line, "new mode "):
		f.modeNew = strings.TrimSpace(line[len("new mode "):])
	case strings.HasPrefix(line, "similarity index "):
		f.similarity, _ = strconv.Atoi(strings.TrimSuffix(strings.TrimSpace(line[len("similarity index "):]), "%"))
	case strings.HasPrefix(line, "rename from "):
		f.status = statusRenamed
		f.oldRaw, _ = unquoteGit(strings.TrimSpace(line[len("rename from "):]))
		f.declaredOld = true
	case strings.HasPrefix(line, "rename to "):
		f.status = statusRenamed
		f.newRaw, _ = unquoteGit(strings.TrimSpace(line[len("rename to "):]))
		f.declaredNew = true
	case strings.HasPrefix(line, "copy from "):
		f.status = statusCopied
		f.oldRaw, _ = unquoteGit(strings.TrimSpace(line[len("copy from "):]))
		f.declaredOld = true
	case strings.HasPrefix(line, "copy to "):
		f.status = statusCopied
		f.newRaw, _ = unquoteGit(strings.TrimSpace(line[len("copy to "):]))
		f.declaredNew = true
	case strings.HasPrefix(line, "Binary files ") && strings.HasSuffix(line, " differ"):
		f.binary = true
		body := strings.TrimSuffix(strings.TrimPrefix(line, "Binary files "), " differ")
		if i := strings.Index(body, " and "); i >= 0 && f.oldRaw == "" && f.newRaw == "" {
			f.oldRaw, _ = unquoteGit(strings.TrimSpace(body[:i]))
			f.newRaw, _ = unquoteGit(strings.TrimSpace(body[i+5:]))
		}
	case strings.HasPrefix(line, "GIT binary patch"):
		f.binary = true
		p.binary = true
	}
}

// headerPath cleans a ---/+++ header value: git and GNU diff append a tab and a
// timestamp, and git quotes unusual names.
func headerPath(s string) string {
	s = strings.TrimRight(s, " ")
	if strings.HasPrefix(s, `"`) {
		if end := closingQuote(s); end > 0 {
			u, _ := unquoteGit(s[:end+1])
			return u
		}
	}
	if i := strings.IndexByte(s, '\t'); i >= 0 {
		s = s[:i]
	}
	return s
}

// closingQuote finds the byte offset of the closing double quote after an opening quote, skipping
// escaped bytes; it returns -1 if none exists.
func closingQuote(s string) int {
	for i := 1; i < len(s); i++ {
		switch s[i] {
		case '\\':
			i++
		case '"':
			return i
		}
	}
	return -1
}

// splitGitHeader splits the "a/x b/y" part of a diff --git line. Names may
// contain spaces, so it first tries the split where both halves name the same
// file (the usual case; the halves then have equal length, which makes the
// candidate position unique and the check linear). Renames and copies are
// corrected by their own lines later.
func splitGitHeader(rest string) (string, string) {
	rest = strings.TrimSpace(rest)
	if strings.HasPrefix(rest, `"`) {
		if end := closingQuote(rest); end > 0 {
			a, _ := unquoteGit(rest[:end+1])
			b, _ := unquoteGit(strings.TrimSpace(rest[end+1:]))
			return a, b
		}
	}
	if i := strings.Index(rest, ` "`); i > 0 && strings.HasSuffix(rest, `"`) {
		b, _ := unquoteGit(rest[i+1:])
		return rest[:i], b
	}
	n := len(rest)
	if n >= 5 && (n-5)%2 == 0 { // "a/X b/X"
		mid := 2 + (n-5)/2
		if rest[mid] == ' ' && strings.HasPrefix(rest, "a/") && strings.HasPrefix(rest[mid+1:], "b/") && rest[2:mid] == rest[mid+3:] {
			return rest[:mid], rest[mid+1:]
		}
	}
	if n >= 3 && (n-1)%2 == 0 { // "X X" (--no-prefix)
		mid := (n - 1) / 2
		if rest[mid] == ' ' && rest[:mid] == rest[mid+1:] {
			return rest[:mid], rest[mid+1:]
		}
	}
	if i := strings.Index(rest, " b/"); i > 0 {
		return rest[:i], rest[i+1:]
	}
	if i := strings.IndexByte(rest, ' '); i > 0 {
		return rest[:i], rest[i+1:]
	}
	return rest, rest
}

// startHunk parses "@@ -a,b +c,d @@ context" (or the combined "@@@" form).
func (p *diffParser) startHunk(line string) {
	if p.cur == nil {
		// A bare hunk with no file header: attribute it to an unnamed file so its
		// lines are still analysed.
		p.start(&fileDiff{})
	}
	p.header = false
	p.cur.hunks = append(p.cur.hunks, hunk{})
	p.hk = &p.cur.hunks[len(p.cur.hunks)-1]
	p.lenient, p.combined = false, 0

	ats := 0
	for ats < len(line) && line[ats] == '@' {
		ats++
	}
	if ats >= 3 {
		p.combined = ats - 1
		p.lenient = false
		if i := strings.Index(line[ats:], strings.Repeat("@", ats)); i >= 0 {
			p.hk.header = strings.TrimSpace(line[ats+i+ats:])
		}
		return
	}
	body := strings.TrimSpace(line[2:])
	end := strings.Index(body, "@@")
	spec := body
	if end >= 0 {
		spec = strings.TrimSpace(body[:end])
		p.hk.header = strings.TrimSpace(body[end+2:])
	}
	fields := strings.Fields(spec)
	if len(fields) < 2 || !strings.HasPrefix(fields[0], "-") || !strings.HasPrefix(fields[1], "+") {
		p.lenient = true
		return
	}
	oldStart, oldCount, ok1 := parseRange(fields[0][1:])
	newStart, newCount, ok2 := parseRange(fields[1][1:])
	if !ok1 || !ok2 {
		p.lenient = true
		return
	}
	p.hk.oldStart, p.hk.newStart = oldStart, newStart
	p.oldLeft, p.newLeft = oldCount, newCount
}

// parseRange parses a nonnegative unified-diff range, using a count of one when the comma and
// count are omitted.
func parseRange(s string) (start, count int, ok bool) {
	a, b, hasCount := strings.Cut(s, ",")
	st, err := strconv.Atoi(a)
	if err != nil || st < 0 {
		return 0, 0, false
	}
	if !hasCount {
		return st, 1, true
	}
	c, err := strconv.Atoi(b)
	if err != nil || c < 0 {
		return 0, 0, false
	}
	return st, c, true
}

// hunkLine consumes one line of a counted hunk. It returns false when the line
// cannot be hunk content.
func (p *diffParser) hunkLine(line string) bool {
	switch {
	case line == "":
		// Some tools strip the trailing space of an empty context line.
		p.hk.lines = append(p.hk.lines, diffLine{op: ' '})
		p.oldLeft--
		p.newLeft--
	case line[0] == ' ':
		p.hk.lines = append(p.hk.lines, diffLine{op: ' ', text: line[1:]})
		p.oldLeft--
		p.newLeft--
	case line[0] == '-':
		p.hk.lines = append(p.hk.lines, diffLine{op: '-', text: line[1:]})
		p.oldLeft--
	case line[0] == '+':
		p.hk.lines = append(p.hk.lines, diffLine{op: '+', text: line[1:]})
		p.newLeft--
	case line[0] == '\\':
		// "\ No newline at end of file"
	default:
		return false
	}
	return true
}

// combinedLine consumes a line of a combined-diff hunk (two prefix columns for a
// merge of two parents). It returns false at the next header.
func (p *diffParser) combinedLine(line string) bool {
	if strings.HasPrefix(line, "diff ") || strings.HasPrefix(line, "@@") {
		return false
	}
	if line == "" || line[0] == '\\' {
		return true
	}
	n := p.combined
	if len(line) < n {
		return true
	}
	prefix, text := line[:n], line[n:]
	switch {
	case strings.Contains(prefix, "+"):
		p.hk.lines = append(p.hk.lines, diffLine{op: '+', text: text})
	case strings.Contains(prefix, "-"):
		p.hk.lines = append(p.hk.lines, diffLine{op: '-', text: text})
	default:
		p.hk.lines = append(p.hk.lines, diffLine{op: ' ', text: text})
	}
	return true
}

// lenientLine consumes hunk lines when the hunk header had no usable counts:
// everything that looks like content until the next header.
func (p *diffParser) lenientLine(line string) bool {
	if strings.HasPrefix(line, "diff ") || strings.HasPrefix(line, "@@") {
		return false
	}
	if strings.HasPrefix(line, "--- ") {
		if next, ok := p.peek(); ok && strings.HasPrefix(next, "+++ ") {
			return false
		}
	}
	if line == "" {
		p.hk.lines = append(p.hk.lines, diffLine{op: ' '})
		return true
	}
	switch line[0] {
	case ' ', '+', '-':
		p.hk.lines = append(p.hk.lines, diffLine{op: line[0], text: line[1:]})
		return true
	case '\\':
		return true
	}
	return false
}

// extraLine keeps a line that follows an exhausted hunk. A well-formed diff has
// none; a miscounted one does, and dropping it would hide changes.
func (p *diffParser) extraLine(line string) {
	if line == "" {
		return
	}
	p.hk.lines = append(p.hk.lines, diffLine{op: line[0], text: line[1:]})
}
