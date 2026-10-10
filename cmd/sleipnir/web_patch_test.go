package main

import (
	"context"
	"encoding/json"
	"fmt"
	"math/rand/v2"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"testing"

	"github.com/anemos-labs/sleipnir/internal/events"
	"github.com/anemos-labs/sleipnir/internal/perm"
	"github.com/anemos-labs/sleipnir/internal/tools"
	"github.com/anemos-labs/sleipnir/internal/tools/fs"
)

// askRecorder is the permission checker of a patch that is applied: it draws the change the question would show at the moment the
// tool asks (before anything is written), and allows it.
type askRecorder struct {
	input []byte
	cwd   string
	path  string
	diff  string
	ok    bool
	asked int
}

// Check implements perm.Requester.
func (a *askRecorder) Check(_ context.Context, r perm.Request) perm.Decision {
	a.asked++
	a.path, a.diff, a.ok = proposedChange(r, a.input, a.cwd)
	return perm.Decision{Allow: true}
}

// pageCounts counts the added and removed lines of a unified diff as the page does (85-ui-approvals.js: a line that starts with
// + or - and is not a +++ or --- header).
func pageCounts(diff string) (added, removed int) {
	for _, l := range strings.Split(diff, "\n") {
		switch {
		case strings.HasPrefix(l, "+") && !strings.HasPrefix(l, "+++ "):
			added++
		case strings.HasPrefix(l, "-") && !strings.HasPrefix(l, "--- "):
			removed++
		}
	}
	return added, removed
}

// diffFiles are the files a unified diff names.
func diffFiles(diff string) []string {
	seen := map[string]bool{}
	for _, l := range strings.Split(diff, "\n") {
		for _, p := range []string{"--- a/", "+++ b/", "rename from ", "rename to "} {
			if f, ok := strings.CutPrefix(l, p); ok {
				seen[f] = true
			}
		}
	}
	out := make([]string, 0, len(seen))
	for f := range seen {
		out = append(out, f)
	}
	sort.Strings(out)
	return out
}

// patchCase is a random patch of a few files and what its application changes.
type patchCase struct {
	files   map[string][]string // the files before, as lines
	text    string              // the patch as a model would write it, before the variants
	deleted int                 // lines of the files it deletes
}

// randomPatch builds a patch that applies to files of distinct lines: added files, deletions, updates of one to three hunks
// (with context, anchors and unified-diff headers) and moves.
func randomPatch(rng *rand.Rand, n int) patchCase {
	pc := patchCase{files: map[string][]string{}}
	word := func() string { return fmt.Sprintf("w%d_%x", n, rng.Uint32()) }
	var b strings.Builder
	b.WriteString("*** Begin Patch\n")
	ops := 1 + rng.IntN(4)
	// A blank line right after a hunk is read as one more empty line of context: the tool drops trailing empty context only while the
	// hunk keeps a line to find, and such a line matches the file's empty line when that comes next. None is written after a hunk that
	// ends with the file's empty line or is followed by it.
	endsEmpty := false
	for i := range ops {
		endsEmpty = false
		name := fmt.Sprintf("f%d_%d.txt", n, i)
		switch kind := rng.IntN(4); {
		case kind == 0: // add
			b.WriteString("*** Add File: " + name + "\n")
			for j := range 1 + rng.IntN(5) {
				if j > 0 && rng.IntN(5) == 0 {
					b.WriteString("\n") // an empty line the model forgot to prefix
				}
				b.WriteString("+" + word() + "\n")
			}
		case kind == 1: // delete
			lines := make([]string, 1+rng.IntN(6))
			for j := range lines {
				lines[j] = word()
			}
			pc.files[name] = lines
			pc.deleted += len(lines)
			b.WriteString("*** Delete File: " + name + "\n")
		default: // update, sometimes with a move
			lines := make([]string, 12+rng.IntN(12))
			for j := range lines {
				lines[j] = word()
			}
			if rng.IntN(2) == 0 {
				lines[rng.IntN(len(lines))] = "" // one empty line
			}
			pc.files[name] = lines
			b.WriteString("*** Update File: " + name + "\n")
			if kind == 3 {
				b.WriteString("*** Move to: moved_" + name + "\n")
			}
			at := 0
			for h := 0; h < 1+rng.IntN(3) && at < len(lines)-4; h++ {
				before, gone, after := rng.IntN(3), rng.IntN(3), rng.IntN(3)
				if before+gone+after == 0 {
					after = 1
				}
				start := at + rng.IntN(max(1, (len(lines)-at)/3))
				if start+before+gone+after > len(lines) {
					break
				}
				switch rng.IntN(3) {
				case 0:
					b.WriteString("@@\n")
				case 1:
					fmt.Fprintf(&b, "@@ -%d,%d +%d,%d @@\n", start+1, before+gone+after, start+1, before+after+1)
				default:
					if start > at && lines[start-1] != "" {
						b.WriteString("@@ " + lines[start-1] + "\n")
					} else {
						b.WriteString("@@\n")
					}
				}
				ctx := func(l string) {
					endsEmpty = l == ""
					if l == "" && rng.IntN(2) == 0 {
						b.WriteString("\n") // an empty context line without its space
						return
					}
					b.WriteString(" " + l + "\n")
				}
				for _, l := range lines[start : start+before] {
					ctx(l)
				}
				for _, l := range lines[start+before : start+before+gone] {
					b.WriteString("-" + l + "\n")
				}
				for range 1 + rng.IntN(2) {
					b.WriteString("+" + word() + "\n")
				}
				endsEmpty = false
				for _, l := range lines[start+before+gone : start+before+gone+after] {
					ctx(l)
				}
				at = start + before + gone + after
				endsEmpty = endsEmpty || (at < len(lines) && lines[at] == "")
				if !endsEmpty && rng.IntN(4) == 0 {
					b.WriteString("\n") // a blank line between hunks
				}
			}
			if at == 0 { // no hunk fitted: change the first line
				b.WriteString("@@\n-" + lines[0] + "\n+" + word() + "\n")
				endsEmpty = false
			}
		}
		if !endsEmpty && rng.IntN(3) == 0 {
			b.WriteString("\n") // a blank line between operations
		}
	}
	b.WriteString("*** End Patch\n")
	pc.text = b.String()
	return pc
}

// variant writes a patch as models paste it: uniformly indented (spaces, tabs or both, on blank lines or not), with CRLF line ends,
// inside a heredoc, with blank lines around it.
func variant(rng *rand.Rand, text string) (string, string) {
	var name []string
	lines := strings.Split(strings.TrimSuffix(text, "\n"), "\n")
	if rng.IntN(3) > 0 {
		indent := strings.Repeat(" ", 1+rng.IntN(8))
		switch rng.IntN(3) {
		case 0:
			indent = strings.Repeat("\t", 1+rng.IntN(3))
		case 1:
			indent = "\t" + strings.Repeat(" ", rng.IntN(4))
		}
		blanks := rng.IntN(2) == 0
		for i, l := range lines {
			if l != "" || blanks {
				lines[i] = indent + l
			}
		}
		name = append(name, fmt.Sprintf("indent %q (blank lines too: %v)", indent, blanks))
	}
	if rng.IntN(4) == 0 {
		lines = append(append([]string{"apply_patch <<'EOF'"}, lines...), "EOF")
		name = append(name, "heredoc")
	}
	if rng.IntN(3) == 0 {
		lines = append(append([]string{"", ""}, lines...), "", "")
		name = append(name, "blank lines around")
	}
	eol := "\n"
	if rng.IntN(2) == 0 {
		eol = "\r\n"
		name = append(name, "CRLF")
	}
	return strings.Join(lines, eol) + eol, strings.Join(name, ", ")
}

// A patch's question shows the change that the apply_patch tool makes, read by the tool's own parser: for random patches, uniformly
// indented with spaces or tabs, with CRLF line ends, blank lines and heredoc wrappers, the files the question names and its added and
// removed lines (as the page counts them) are those of the tool's application.
func TestPatchQuestionsShowWhatTheToolApplies(t *testing.T) {
	rng := rand.New(rand.NewPCG(7, 11))
	for n := range 400 {
		pc := randomPatch(rng, n)
		text, how := variant(rng, pc.text)
		dir, err := filepath.EvalSymlinks(t.TempDir())
		if err != nil {
			t.Fatal(err)
		}
		for name, lines := range pc.files {
			if err := os.WriteFile(filepath.Join(dir, name), []byte(strings.Join(lines, "\n")+"\n"), 0o644); err != nil {
				t.Fatal(err)
			}
		}
		input, _ := json.Marshal(map[string]string{"patch": text})
		rec := &askRecorder{input: input, cwd: dir}
		env := &tools.Env{Agent: "a1", Cwd: dir, Root: dir, Files: tools.NewFileState(), Blobs: events.NewMemBlobs(), Perm: rec}
		res, err := fs.ApplyPatch{}.Run(context.Background(), &tools.Call{ID: "c1", Name: "apply_patch", Input: input, Env: env})
		if err != nil || res.IsError {
			t.Fatalf("patch %d (%s) did not apply: %v %s\n%s", n, how, err, res.Text, text)
		}
		if rec.asked != 1 || !rec.ok {
			t.Fatalf("patch %d (%s): asked %d times, change shown: %v", n, how, rec.asked, rec.ok)
		}
		var files []string
		for _, f := range res.Meta["files"].([]string) {
			files = append(files, filepath.ToSlash(f))
		}
		sort.Strings(files)
		wantAdd, wantDel := res.Meta["added"].(int), res.Meta["removed"].(int)+pc.deleted
		gotAdd, gotDel := pageCounts(rec.diff)
		if got := diffFiles(rec.diff); !slices.Equal(got, files) || gotAdd != wantAdd || gotDel != wantDel {
			t.Fatalf("patch %d (%s): the question shows %q +%d -%d; the tool changed %q +%d -%d\npatch:\n%s\nquestion:\n%s",
				n, how, got, gotAdd, gotDel, files, wantAdd, wantDel, text, rec.diff)
		}
	}
}
