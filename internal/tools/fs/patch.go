package fs

import (
	"fmt"
	"regexp"
	"strings"
)

// The apply_patch format is the one OpenAI's Codex tooling uses (models have
// been trained on it):
//
//	*** Begin Patch
//	*** Add File: path            every following line starts with +
//	*** Delete File: path
//	*** Update File: path
//	*** Move to: newpath          optional, right after Update File
//	@@ optional context line      starts a hunk; the line locates it in the file
//	 context                      lines of a hunk start with ' ', '-' or '+'
//	-old
//	+new
//	*** End of File               optional: the hunk must match at end of file
//	*** End Patch
//
// The parser is lenient about things models get wrong without changing what
// they meant (heredoc wrappers, blank lines between operations, unified-diff
// style "@@ -1,3 +1,4 @@" headers, "\ No newline" markers) and precise about
// everything else: every error names the patch line.

type patchOpKind int

const (
	opAdd patchOpKind = iota
	opDelete
	opUpdate
)

type hunkLine struct {
	op   byte // ' ', '-', '+'
	text string
}

type patchHunk struct {
	anchors []string
	lines   []hunkLine
	eof     bool
	line    int // patch line where the hunk starts
}

type patchOp struct {
	kind  patchOpKind
	path  string
	move  string
	add   []string
	hunks []patchHunk
	line  int
}

var (
	heredocStart = regexp.MustCompile(`^\s*(?:cat\s+)?(?:apply_patch\s*)?<<-?\s*['"]?([A-Za-z0-9_]+)['"]?\s*$`)
	unifiedHdr   = regexp.MustCompile(`^@@\s+-\d+(?:,\d+)?\s+\+\d+(?:,\d+)?\s+@@\s*(.*)$`)
)

const (
	hdrBegin  = "*** Begin Patch"
	hdrEnd    = "*** End Patch"
	hdrAdd    = "*** Add File:"
	hdrDelete = "*** Delete File:"
	hdrUpdate = "*** Update File:"
	hdrMove   = "*** Move to:"
	hdrEOF    = "*** End of File"
)

// isFileHeader recognizes add, delete, and update headers in the patch protocol.
func isFileHeader(l string) bool {
	return strings.HasPrefix(l, hdrAdd) || strings.HasPrefix(l, hdrDelete) || strings.HasPrefix(l, hdrUpdate)
}

// parsePatch parses the patch text. The error string is model-visible.
func parsePatch(text string) ([]patchOp, string) {
	raw := strings.Split(text, "\n")
	for i, l := range raw {
		raw[i] = strings.TrimSuffix(l, "\r")
	}
	first, last := 0, len(raw)
	for first < last && strings.TrimSpace(raw[first]) == "" {
		first++
	}
	for last > first && strings.TrimSpace(raw[last-1]) == "" {
		last--
	}
	if first == last {
		return nil, "patch is empty"
	}
	if m := heredocStart.FindStringSubmatch(raw[first]); m != nil {
		first++
		if last > first && strings.TrimSpace(raw[last-1]) == m[1] {
			last--
		}
		for last > first && strings.TrimSpace(raw[last-1]) == "" {
			last--
		}
	}
	if first < last && strings.TrimSpace(raw[first]) == hdrBegin {
		// A patch pasted from an indented block: remove the indentation of the
		// Begin line from every line. (Hunk lines start with a space of their own,
		// so this is only safe when the whole patch is uniformly indented.)
		if indent := raw[first][:len(raw[first])-len(strings.TrimLeft(raw[first], " \t"))]; indent != "" {
			for i := first; i < last; i++ {
				raw[i] = strings.TrimPrefix(raw[i], indent)
			}
		}
	}
	if first >= last || strings.TrimSpace(raw[first]) != hdrBegin {
		got := ""
		if first < last {
			got = raw[first]
		}
		return nil, fmt.Sprintf("patch must start with %q on its own line (found %q)", hdrBegin, clip(got, 60))
	}
	if strings.TrimSpace(raw[last-1]) != hdrEnd {
		return nil, fmt.Sprintf("patch must end with %q on its own line (found %q)", hdrEnd, clip(raw[last-1], 60))
	}
	body := raw[first+1 : last-1]
	lineNo := func(i int) int { return first + 1 + 1 + i } // 1-based number in the patch text

	var ops []patchOp
	for i := 0; i < len(body); {
		l := strings.TrimRight(body[i], " \t")
		switch {
		case strings.TrimSpace(l) == "":
			i++
		case strings.HasPrefix(l, hdrAdd):
			op := patchOp{kind: opAdd, path: strings.TrimSpace(l[len(hdrAdd):]), line: lineNo(i)}
			if op.path == "" {
				return nil, fmt.Sprintf("patch line %d: %q needs a path", op.line, hdrAdd)
			}
			i++
			for i < len(body) {
				if strings.HasPrefix(body[i], "+") {
					op.add = append(op.add, body[i][1:])
					i++
					continue
				}
				// A blank line between two + lines is an empty line the model
				// forgot to prefix; a blank line before the next operation is a
				// separator.
				j := i
				for j < len(body) && strings.TrimSpace(body[j]) == "" {
					j++
				}
				if j > i && j < len(body) && strings.HasPrefix(body[j], "+") {
					for ; i < j; i++ {
						op.add = append(op.add, "")
					}
					continue
				}
				break
			}
			ops = append(ops, op)
		case strings.HasPrefix(l, hdrDelete):
			op := patchOp{kind: opDelete, path: strings.TrimSpace(l[len(hdrDelete):]), line: lineNo(i)}
			if op.path == "" {
				return nil, fmt.Sprintf("patch line %d: %q needs a path", op.line, hdrDelete)
			}
			ops = append(ops, op)
			i++
		case strings.HasPrefix(l, hdrUpdate):
			op := patchOp{kind: opUpdate, path: strings.TrimSpace(l[len(hdrUpdate):]), line: lineNo(i)}
			if op.path == "" {
				return nil, fmt.Sprintf("patch line %d: %q needs a path", op.line, hdrUpdate)
			}
			i++
			if i < len(body) && strings.HasPrefix(strings.TrimRight(body[i], " \t"), hdrMove) {
				op.move = strings.TrimSpace(strings.TrimRight(body[i], " \t")[len(hdrMove):])
				if op.move == "" {
					return nil, fmt.Sprintf("patch line %d: %q needs a path", lineNo(i), hdrMove)
				}
				i++
			}
			var msg string
			op.hunks, i, msg = parseHunks(body, i, lineNo)
			if msg != "" {
				return nil, msg
			}
			if len(op.hunks) == 0 && op.move == "" {
				return nil, fmt.Sprintf("patch line %d: Update File %s has no hunks (start each with @@)", op.line, op.path)
			}
			ops = append(ops, op)
		default:
			return nil, fmt.Sprintf("patch line %d: unexpected %q; expected %q, %q or %q (every line inside an Add File block must start with '+')",
				lineNo(i), clip(l, 60), hdrAdd, hdrDelete, hdrUpdate)
		}
	}
	if len(ops) == 0 {
		return nil, "patch contains no operations"
	}
	return ops, ""
}

// parseHunks reads the hunks of one Update File block starting at body[i].
func parseHunks(body []string, i int, lineNo func(int) int) ([]patchHunk, int, string) {
	var hunks []patchHunk
	for i < len(body) && !isFileHeader(strings.TrimRight(body[i], " \t")) {
		l := body[i]
		if strings.TrimSpace(l) == "" {
			// A separator between hunks. Blank lines inside a hunk are consumed
			// by the loop below as empty context lines.
			i++
			continue
		}
		h := patchHunk{line: lineNo(i)}
		switch {
		case strings.HasPrefix(l, "@@"):
			for i < len(body) && strings.HasPrefix(body[i], "@@") {
				if a := anchorText(body[i]); a != "" {
					h.anchors = append(h.anchors, a)
				}
				i++
			}
		case len(hunks) == 0 && (l[0] == ' ' || l[0] == '-' || l[0] == '+'):
			// The first hunk of a file may omit its @@ line.
		default:
			return nil, i, fmt.Sprintf("patch line %d: expected a hunk starting with @@ but found %q", lineNo(i), clip(l, 60))
		}
	body:
		for i < len(body) {
			l := body[i]
			switch {
			case strings.HasPrefix(l, "@@"):
				break body
			case strings.TrimRight(l, " \t") == hdrEOF:
				h.eof = true
				i++
				break body
			case isFileHeader(strings.TrimRight(l, " \t")):
				break body
			case l == "":
				h.lines = append(h.lines, hunkLine{' ', ""})
			case l[0] == ' ' || l[0] == '-' || l[0] == '+':
				h.lines = append(h.lines, hunkLine{l[0], l[1:]})
			case l[0] == '\\':
				// "\ No newline at end of file" is diff syntax this format lacks.
			default:
				return nil, i, fmt.Sprintf("patch line %d: unexpected %q inside a hunk; hunk lines must start with ' ' (context), '-' (remove) or '+' (add)",
					lineNo(i), clip(l, 60))
			}
			i++
		}
		if len(h.lines) == 0 {
			return nil, i, fmt.Sprintf("patch line %d: hunk has no lines", h.line)
		}
		hunks = append(hunks, h)
	}
	return hunks, i, ""
}

// anchorText extracts optional anchor text from a unified hunk header or trims a simple @@ marker.
func anchorText(l string) string {
	if m := unifiedHdr.FindStringSubmatch(l); m != nil {
		return strings.TrimSpace(m[1])
	}
	return strings.TrimSpace(strings.TrimPrefix(l, "@@"))
}
