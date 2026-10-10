package checkpoint

import (
	"encoding/json"
	"strings"

	"github.com/anemos-labs/sleipnir/internal/tools/fs"
)

// ProposedChange is what a file tool call that waits for approval would change, as a
// unified diff: Path is the file the call names (the first one of a patch), Unified the
// change (at most 256 KiB, "[diff truncated]" past it, as /diff), Added and Removed its
// lines.
type ProposedChange struct {
	Path           string
	Unified        string
	Added, Removed int
}

// maxProposedEdits bounds the edits of one call that are rendered.
const maxProposedEdits = 200

// Propose renders the change a write, edit or apply_patch call would make, from the
// call's JSON input and the current text of the file it names (current reports false
// for a file that does not exist or cannot be shown as text). It does not touch the
// file system. ok is false for other tools and for an input it cannot read.
//
// An edit is applied to the current text as the edit tool would (one occurrence, or
// every one with replace_all) so the diff has its context; when the text does not hold
// what an edit replaces, each replacement is shown on its own.
func Propose(tool string, input []byte, current func(path string) (string, bool)) (ProposedChange, bool) {
	if current == nil {
		current = func(string) (string, bool) { return "", false }
	}
	return ProposeFrom(tool, input, func(path string) Current {
		text, exists := current(path)
		return Current{Text: text, Exists: exists}
	})
}

// Current is what a file holds now, for a proposed change: its text when it can be shown, whether it is there, and when it is there
// but cannot be shown as text (larger than a diff reads, not UTF-8, binary, not a regular file), why.
type Current struct {
	Text    string
	Exists  bool
	Unshown string
}

// UnshownPrefix begins the line that stands for the diff of a change whose current file cannot be shown: "[current file not shown:
// <why>]". A reader that must show a change whole treats it as it treats a diff that was cut.
const UnshownPrefix = "[current file not shown: "

// unshownChange is the change of a file that is there but cannot be shown: its name and why, never the creation of a new file.
func unshownChange(path, why string) ProposedChange {
	return ProposedChange{Path: path, Unified: "--- a/" + path + "\n+++ b/" + path + "\n" + UnshownPrefix + why + "]\n"}
}

// ProposeFrom is Propose with a current-content function that tells a file that is not there from one that is there and cannot be
// shown: the change of the latter says so (UnshownPrefix) instead of being drawn as a new file.
func ProposeFrom(tool string, input []byte, current func(path string) Current) (ProposedChange, bool) {
	if current == nil {
		current = func(string) Current { return Current{} }
	}
	switch strings.ToLower(tool) {
	case "write":
		var in struct {
			Path     string  `json:"path"`
			FilePath string  `json:"file_path"`
			Content  *string `json:"content"`
		}
		if json.Unmarshal(input, &in) != nil || in.Content == nil {
			return ProposedChange{}, false
		}
		p := firstOf(in.Path, in.FilePath)
		if p == "" {
			return ProposedChange{}, false
		}
		cur := current(p)
		if cur.Exists && cur.Unshown != "" {
			return unshownChange(p, cur.Unshown), true
		}
		return diffChange(p, cur.Text, *in.Content, cur.Exists), true
	case "edit":
		type pair struct {
			Old        *string `json:"old_string"`
			New        *string `json:"new_string"`
			ReplaceAll bool    `json:"replace_all"`
		}
		var in struct {
			Path     string `json:"path"`
			FilePath string `json:"file_path"`
			pair
			Edits []pair `json:"edits"`
		}
		if json.Unmarshal(input, &in) != nil {
			return ProposedChange{}, false
		}
		p := firstOf(in.Path, in.FilePath)
		var pairs []pair
		if in.Old != nil && in.New != nil {
			pairs = append(pairs, in.pair)
		}
		for _, e := range in.Edits {
			if e.Old != nil && e.New != nil {
				pairs = append(pairs, e)
			}
		}
		if p == "" || len(pairs) == 0 {
			return ProposedChange{}, false
		}
		cur := current(p)
		if cur.Exists && cur.Unshown != "" {
			return unshownChange(p, cur.Unshown), true
		}
		old, exists := cur.Text, cur.Exists
		text, applied := old, exists
		for _, e := range pairs {
			if !applied {
				break
			}
			switch n := strings.Count(text, *e.Old); {
			case *e.Old == "" || n == 0 || (n > 1 && !e.ReplaceAll):
				applied = false
			case e.ReplaceAll:
				text = strings.ReplaceAll(text, *e.Old, *e.New)
			default:
				text = strings.Replace(text, *e.Old, *e.New, 1)
			}
		}
		if applied {
			return diffChange(p, old, text, true), true
		}
		// The text does not hold what the edit replaces (it will fail, or the file is
		// not readable here): show each replacement on its own.
		out := ProposedChange{Path: p}
		var b strings.Builder
		for i, e := range pairs {
			if i == maxProposedEdits {
				b.WriteString("[diff truncated]\n")
				break
			}
			u, a, r := unifiedDiff("a/"+p, "b/"+p, *e.Old, *e.New)
			if i > 0 {
				u = dropHeader(u)
			}
			b.WriteString(u)
			out.Added += a
			out.Removed += r
			if b.Len() > maxDiffOutput {
				b.WriteString("[diff truncated]\n")
				break
			}
		}
		out.Unified = b.String()
		return out, true
	case "apply_patch":
		var in struct {
			Patch *string `json:"patch"`
		}
		if json.Unmarshal(input, &in) != nil || in.Patch == nil {
			return ProposedChange{}, false
		}
		return patchChange(*in.Patch, current), true
	}
	return ProposedChange{}, false
}

// diffChange is the unified diff of a file's text from old to new.
func diffChange(path, old, new string, existed bool) ProposedChange {
	from := "a/" + path
	if !existed {
		from, old = "/dev/null", ""
	}
	u, a, r := unifiedDiff(from, "b/"+path, old, new)
	return ProposedChange{Path: path, Unified: u, Added: a, Removed: r}
}

// dropHeader removes the ---/+++ lines of a unified diff.
func dropHeader(u string) string {
	for range 2 {
		if strings.HasPrefix(u, "--- ") || strings.HasPrefix(u, "+++ ") {
			if i := strings.IndexByte(u, '\n'); i >= 0 {
				u = u[i+1:]
			}
		}
	}
	return u
}

// UnreadPatchPrefix begins the line that stands for the diff of a patch that the apply_patch tool cannot read: "[patch not read:
// <why>]". A reader that must show a change whole treats it as it treats a diff that was cut.
const UnreadPatchPrefix = "[patch not read: "

// patchChange renders an apply_patch patch as a unified diff from the operations the apply_patch tool itself reads in it
// (fs.ParsePatch: the same normalization of a heredoc wrapper, uniform indentation, line ends and blank lines): an added file as
// all-new, a deleted file with the lines it holds now (current), an update with its hunks, a move with "rename from"/"rename to". A
// patch the tool cannot read is a single UnreadPatchPrefix line.
func patchChange(patch string, current func(path string) Current) ProposedChange {
	ops, err := fs.ParsePatch(patch)
	if err != nil {
		return ProposedChange{Unified: UnreadPatchPrefix + strings.ReplaceAll(err.Error(), "\n", " ") + "]\n"}
	}
	var out ProposedChange
	var b strings.Builder
	for _, op := range ops {
		if out.Path == "" {
			out.Path = op.Path
		}
		if b.Len() > maxDiffOutput {
			b.WriteString("[diff truncated]\n")
			break
		}
		switch op.Kind {
		case "add":
			b.WriteString("--- /dev/null\n+++ b/" + op.Path + "\n")
			b.WriteString("@@ -0,0 +1," + itoa(len(op.Add)) + " @@\n")
			for _, l := range op.Add {
				b.WriteString("+" + l + "\n")
			}
			out.Added += len(op.Add)
		case "delete":
			cur := current(op.Path)
			switch {
			case cur.Exists && cur.Unshown != "":
				// Deleting it is the whole of the change, whatever it holds.
				b.WriteString("--- a/" + op.Path + "\n+++ /dev/null\n[deleted whole; what it holds is not shown: " + cur.Unshown + "]\n")
			case cur.Exists && cur.Text != "":
				u, _, r := unifiedDiff("a/"+op.Path, "/dev/null", cur.Text, "")
				b.WriteString(u)
				out.Removed += r
			default: // empty, or not there (the tool then fails and writes nothing)
				b.WriteString("--- a/" + op.Path + "\n+++ /dev/null\n")
			}
		case "update":
			to := op.Path
			if op.Move != "" {
				to = op.Move
				b.WriteString("rename from " + op.Path + "\nrename to " + op.Move + "\n")
			}
			b.WriteString("--- a/" + op.Path + "\n+++ b/" + to + "\n")
			for _, h := range op.Hunks {
				if len(h.Anchors) == 0 {
					b.WriteString("@@\n")
				}
				for _, a := range h.Anchors {
					b.WriteString("@@ " + a + "\n")
				}
				for _, l := range h.Lines {
					b.WriteString(string(l.Op) + l.Text + "\n")
					switch l.Op {
					case '+':
						out.Added++
					case '-':
						out.Removed++
					}
				}
			}
		}
	}
	out.Unified = b.String()
	return out
}

// firstOf returns the first non-empty string.
func firstOf(ss ...string) string {
	for _, s := range ss {
		if s != "" {
			return s
		}
	}
	return ""
}

// itoa formats a non-negative count.
func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var d []byte
	for n > 0 {
		d = append([]byte{byte('0' + n%10)}, d...)
		n /= 10
	}
	return string(d)
}

// RewindNote is what the agents are told after a person's restore put files back: the
// files no longer hold the edits they remember making. how names the action ("/rewind",
// "the web workspace"). It is empty for a dry run and when nothing was written.
func RewindNote(rep RestoreReport, how string) string {
	if rep.DryRun {
		return ""
	}
	var files []string
	for _, f := range rep.Files {
		if f.Outcome == OutcomeDone && f.Action != ActionRmdir {
			files = append(files, cleanText(f.Path, 300))
		}
	}
	if len(files) == 0 {
		return ""
	}
	return "Notice from the harness: the person rewound the project's files to checkpoint " + rep.ID + " (" + cleanText(how, 60) + "). These were put back as they were before the edits you made after it, so they no longer hold those edits: " +
		strings.Join(files, ", ") + ". Read them again before you rely on what you remember of them."
}
