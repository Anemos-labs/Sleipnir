package widget

import (
	"strconv"
	"strings"

	"github.com/reee344/sleipnir/internal/tui/cell"
)

// UnifiedDiff renders a unified diff as text (what `git diff`, `git show`, `diff -u` and `git format-patch` print, or what a
// tool built) the way Diff renders an edit: a header per file with the path and the counts, hunks with line numbers taken from
// the hunk headers, full-width backgrounds for removed and added lines, the changed words highlighted in lines that replace each
// other, and a row "⋯ N unchanged lines" (with the function the hunk header names, if any) between hunks. Renames, new and
// deleted files and binary changes become a note after the path.
//
// It takes anything. The hunk headers' counts say where a hunk ends, so text after a patch that looks like diff lines ("- item")
// is not mistaken for part of it; a header that does not parse gives a hunk without line numbers that ends at the first line
// that is not a diff line. Whatever belongs to no file or hunk (a commit message, a stray line, "\ No newline at end of file" aside)
// is shown dim and wrapped, in place. Control characters are removed. An empty patch, or a width below 1, give nil.
func UnifiedDiff(patch string, width int, th Theme) []cell.Line {
	return UnifiedDiffWith(patch, width, th, DiffOptions{})
}

// UnifiedDiffWith is UnifiedDiff with options; Context has no effect (a patch has the context it has).
func UnifiedDiffWith(patch string, width int, th Theme, o DiffOptions) []cell.Line {
	if width < 1 || patch == "" {
		return nil
	}
	files := parseUnified(patch, o.tabs())
	return diffRenderFiles(files, width, th, o)
}

type udParser struct {
	tabw   int
	files  []dfFile
	cur    int // index of the file being filled, -1 before the first
	hunk   *dfHunk
	oldNo  int
	newNo  int
	remOld int // lines the hunk header still promises, -1 when it gave no counts
	remNew int
	loose  []string // lines that belong to no file
	budget int
	// where the previous hunk of this file ended, to count the lines between hunks
	prevEnd int
	prevOK  bool
}

func parseUnified(patch string, tabw int) []dfFile {
	p := &udParser{tabw: tabw, cur: -1, budget: diffWordBudget}
	lines := strings.Split(strings.TrimSuffix(patch, "\n"), "\n")
	for i := range lines {
		lines[i] = strings.TrimSuffix(lines[i], "\r")
	}
	for i := 0; i < len(lines); i++ {
		ln := lines[i]
		if p.hunk != nil {
			if p.hunkLine(ln, lines, i) {
				continue
			}
			p.endHunk()
		}
		switch {
		case strings.HasPrefix(ln, "diff --git "):
			p.startFile(gitDiffPath(ln[len("diff --git "):]))
		case strings.HasPrefix(ln, "diff "):
			p.startFile(udLastField(ln))
		case strings.HasPrefix(ln, "--- ") && i+1 < len(lines) && strings.HasPrefix(lines[i+1], "+++ "):
			p.fileHeaders(ln[4:], lines[i+1][4:])
			i++
		case strings.HasPrefix(ln, "@@") && !strings.HasPrefix(ln, "@@@"): // "@@@" is a combined diff: text
			p.startHunk(ln)
		case p.cur >= 0 && p.fileMeta(ln):
		default:
			p.loose = append(p.loose, ln)
		}
	}
	p.endHunk()
	p.flushLoose()
	return p.files
}

func udLastField(s string) string {
	f := strings.Fields(s)
	if len(f) == 0 {
		return ""
	}
	return stripDiffPrefix(strings.Trim(f[len(f)-1], `"`))
}

// gitDiffPath takes the path out of the rest of a "diff --git a/x b/x" line.
func gitDiffPath(rest string) string {
	// "a/X b/X": the two halves name the same file unless it was renamed; find the split where they do.
	for i := 0; i < len(rest); i++ {
		if rest[i] == ' ' && i+1 < len(rest) {
			a, b := rest[:i], rest[i+1:]
			if strings.HasPrefix(a, "a/") && strings.HasPrefix(b, "b/") && a[2:] == b[2:] {
				return b[2:]
			}
		}
	}
	return udLastField("x " + rest)
}

func stripDiffPrefix(p string) string {
	if i := strings.IndexByte(p, '\t'); i >= 0 {
		p = p[:i] // `diff -u` puts a timestamp after a tab
	}
	p = strings.TrimSpace(p)
	if strings.HasPrefix(p, "a/") || strings.HasPrefix(p, "b/") {
		return p[2:]
	}
	return p
}

func (p *udParser) flushLoose() {
	for len(p.loose) > 0 && strings.TrimSpace(p.loose[0]) == "" {
		p.loose = p.loose[1:]
	}
	for len(p.loose) > 0 && strings.TrimSpace(p.loose[len(p.loose)-1]) == "" {
		p.loose = p.loose[:len(p.loose)-1]
	}
	if len(p.loose) > 0 {
		p.files = append(p.files, dfFile{text: p.loose})
		p.cur = -1
	}
	p.loose = nil
}

func (p *udParser) startFile(path string) {
	p.endHunk()
	p.flushLoose()
	p.files = append(p.files, dfFile{path: strings.TrimSpace(safeOneLine(path))})
	p.cur = len(p.files) - 1
	p.prevOK = false
}

// fileHeaders handles the "--- old" and "+++ new" pair that opens a file's hunks.
func (p *udParser) fileHeaders(oldP, newP string) {
	if p.cur < 0 || len(p.files[p.cur].hunks) > 0 {
		p.startFile("")
	}
	f := &p.files[p.cur]
	oldP, newP = stripDiffPrefix(oldP), stripDiffPrefix(newP)
	switch {
	case newP == "/dev/null":
		f.path, f.note = strings.TrimSpace(safeOneLine(oldP)), "deleted"
	case oldP == "/dev/null":
		f.path, f.note = strings.TrimSpace(safeOneLine(newP)), "new file"
	default:
		f.path = strings.TrimSpace(safeOneLine(newP))
	}
}

// fileMeta handles the extended header lines of git and reports whether it knew the line.
func (p *udParser) fileMeta(ln string) bool {
	f := &p.files[p.cur]
	switch {
	case strings.HasPrefix(ln, "new file mode"):
		f.note = "new file"
	case strings.HasPrefix(ln, "deleted file mode"):
		f.note = "deleted"
	case strings.HasPrefix(ln, "rename from "):
		f.note = "renamed from " + strings.TrimSpace(safeOneLine(ln[len("rename from "):]))
	case strings.HasPrefix(ln, "rename to "), strings.HasPrefix(ln, "copy to "):
		f.path = strings.TrimSpace(safeOneLine(ln[strings.LastIndex(ln, " to ")+4:]))
	case strings.HasPrefix(ln, "copy from "):
		f.note = "copied from " + strings.TrimSpace(safeOneLine(ln[len("copy from "):]))
	case strings.HasPrefix(ln, "Binary files "), strings.HasPrefix(ln, "GIT binary patch"):
		f.note = "binary file changed"
	case strings.HasPrefix(ln, "old mode "):
		f.note = "mode " + strings.TrimSpace(safeOneLine(ln[len("old mode "):]))
	case strings.HasPrefix(ln, "new mode "):
		to := strings.TrimSpace(safeOneLine(ln[len("new mode "):]))
		if old, ok := strings.CutPrefix(f.note, "mode "); ok && !strings.Contains(old, " ") {
			f.note = "mode " + old + " -> " + to
		} else {
			f.note = "mode " + to
		}
	case strings.HasPrefix(ln, "index "), strings.HasPrefix(ln, "similarity index "), strings.HasPrefix(ln, "dissimilarity index "):
	default:
		return false
	}
	return true
}

// udParseHunkHeader reads "@@ -a,b +c,d @@ section". Counts default to 1, as in the format.
func udParseHunkHeader(ln string) (oldStart, oldCount, newStart, newCount int, section string, ok bool) {
	if strings.HasPrefix(ln, "@@@") { // a combined diff: not supported, shown as text
		return
	}
	rest := strings.TrimLeft(strings.TrimPrefix(ln, "@@"), " ")
	end := strings.Index(rest, "@@")
	if end < 0 {
		return
	}
	fields := strings.Fields(rest[:end])
	if len(fields) != 2 || fields[0][0] != '-' || fields[1][0] != '+' {
		return
	}
	var okA, okB bool
	oldStart, oldCount, okA = udParseRange(fields[0][1:])
	newStart, newCount, okB = udParseRange(fields[1][1:])
	return oldStart, oldCount, newStart, newCount, strings.TrimSpace(rest[end+2:]), okA && okB
}

func udParseRange(s string) (start, count int, ok bool) {
	a, b, hasCount := strings.Cut(s, ",")
	start, err := strconv.Atoi(a)
	if err != nil || start < 0 || start > 1<<30 {
		return 0, 0, false
	}
	count = 1
	if hasCount {
		if count, err = strconv.Atoi(b); err != nil || count < 0 || count > 1<<30 {
			return 0, 0, false
		}
	}
	return start, count, true
}

func (p *udParser) startHunk(ln string) {
	if p.cur < 0 {
		p.flushLoose()
		p.files = append(p.files, dfFile{})
		p.cur = len(p.files) - 1
	}
	h := &dfHunk{skipped: -1}
	os, oc, ns, nc, section, ok := udParseHunkHeader(ln)
	if ok {
		h.section = section
		p.oldNo, p.newNo, p.remOld, p.remNew = os, ns, oc, nc
		first := os // the old line the hunk begins at; for a hunk with no old lines the header names the line before
		if oc == 0 {
			first, p.oldNo = os+1, os+1
		}
		if nc == 0 {
			p.newNo++
		}
		if p.prevOK {
			h.skipped = max(0, first-p.prevEnd)
		}
		p.prevEnd, p.prevOK = first+oc, true
	} else {
		h.section = strings.TrimSpace(safeOneLine(strings.Trim(ln, "@ ")))
		p.oldNo, p.newNo, p.remOld, p.remNew = 0, 0, -1, -1
		p.prevOK = false
	}
	p.hunk = h
}

// hunkLine adds ln to the open hunk and reports whether it belonged there.
func (p *udParser) hunkLine(ln string, lines []string, i int) bool {
	bounded := p.remOld >= 0
	switch {
	case strings.HasPrefix(ln, `\`): // "\ No newline at end of file": not counted, belongs to the line before
		p.hunk.lines = append(p.hunk.lines, dfLine{kind: '\\', text: diffText(strings.TrimSpace(ln[1:]), p.tabw)})
		return true
	case strings.HasPrefix(ln, "diff --git "):
		return false
	case strings.HasPrefix(ln, "--- ") && i+2 < len(lines) && strings.HasPrefix(lines[i+1], "+++ ") && strings.HasPrefix(lines[i+2], "@@"):
		return false // the next file's header, even if the counts promised more lines
	case bounded && p.remOld == 0 && p.remNew == 0:
		return false
	case ln == "": // a context line whose one space was stripped by an editor
		if !bounded || p.remOld == 0 || p.remNew == 0 {
			return false
		}
		p.add(' ', "")
	case ln[0] == ' ':
		if bounded && (p.remOld == 0 || p.remNew == 0) {
			return false
		}
		p.add(' ', ln[1:])
	case ln[0] == '-':
		if bounded && p.remOld == 0 {
			return false
		}
		p.add('-', ln[1:])
	case ln[0] == '+':
		if bounded && p.remNew == 0 {
			return false
		}
		p.add('+', ln[1:])
	default:
		return false
	}
	return true
}

func (p *udParser) add(kind byte, text string) {
	l := dfLine{kind: kind, text: diffText(text, p.tabw)}
	f := &p.files[p.cur]
	numbered := p.remOld >= 0
	if kind != '+' {
		if numbered {
			l.old = p.oldNo
			p.oldNo++
			p.remOld--
		}
		if kind == '-' {
			f.dels++
		}
	}
	if kind != '-' {
		if numbered {
			l.new = p.newNo
			p.newNo++
			p.remNew--
		}
		if kind == '+' {
			f.adds++
		}
	}
	p.hunk.lines = append(p.hunk.lines, l)
}

func (p *udParser) endHunk() {
	if p.hunk == nil {
		return
	}
	pairWords(p.hunk.lines, &p.budget)
	f := &p.files[p.cur]
	f.hunks = append(f.hunks, *p.hunk)
	p.hunk = nil
}
