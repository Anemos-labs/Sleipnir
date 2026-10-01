package repocheck

import (
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"testing"
)

// module is the import path whose go.mod marks the root of the repository.
const module = "github.com/anemos-labs/sleipnir"

// root returns the module root, or skips the test when it is not run from a checkout of the repository.
func root(t *testing.T) string {
	t.Helper()
	wd, err := os.Getwd()
	if err != nil {
		t.Fatalf("cannot find the working directory: %v", err)
	}
	for dir := wd; ; {
		b, err := os.ReadFile(filepath.Join(dir, "go.mod"))
		if err == nil && strings.HasPrefix(strings.TrimSpace(string(b)), "module "+module) {
			if _, err := os.Stat(filepath.Join(dir, ".github")); err != nil {
				t.Skipf("not run from a checkout of the repository: %s has a go.mod of %s but no .github directory", dir, module)
			}
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Skipf("not run from a checkout of the repository: no go.mod of %s at or above %s", module, wd)
		}
		dir = parent
	}
}

// read returns a file of the repository, failing the test when it is missing.
func read(t *testing.T, rel string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(root(t), filepath.FromSlash(rel)))
	if err != nil {
		t.Fatalf("%s is missing or unreadable: %v", rel, err)
	}
	return strings.ReplaceAll(string(b), "\r\n", "\n")
}

var (
	filesOnce sync.Once
	filesList []string
)

// skippedDirs are not part of the repository's tree: git's own, other agents' worktrees, build output.
var skippedDirs = map[string]bool{".git": true, ".claude": true, "node_modules": true, "bin": true, "dist": true}

// treeFiles lists every file of the checkout as a slash-separated path relative to the root.
func treeFiles(t *testing.T) []string {
	t.Helper()
	r := root(t)
	filesOnce.Do(func() {
		_ = filepath.WalkDir(r, func(p string, d os.DirEntry, err error) error {
			if err != nil {
				return nil
			}
			rel, _ := filepath.Rel(r, p)
			if d.IsDir() {
				if skippedDirs[d.Name()] && rel != "." {
					return filepath.SkipDir
				}
				return nil
			}
			filesList = append(filesList, filepath.ToSlash(rel))
			return nil
		})
		sort.Strings(filesList)
	})
	return filesList
}

// lines splits text into lines, without the line ends.
func lines(text string) []string { return strings.Split(strings.ReplaceAll(text, "\r\n", "\n"), "\n") }

func indentOf(s string) int { return len(s) - len(strings.TrimLeft(s, " ")) }

func blankOrComment(s string) bool {
	t := strings.TrimSpace(s)
	return t == "" || strings.HasPrefix(t, "#")
}

// uncomment removes a trailing "# comment" (a # at the start or after a space) that is not inside quotes.
func uncomment(s string) string {
	var quote byte
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case quote != 0:
			if c == quote {
				quote = 0
			}
		case (c == '"' || c == '\'') && opensQuote(s, i):
			quote = c
		case c == '#' && (i == 0 || s[i-1] == ' ' || s[i-1] == '\t'):
			return strings.TrimRight(s[:i], " \t")
		}
	}
	return strings.TrimRight(s, " \t")
}

// opensQuote reports whether the quote character at s[i] starts a quoted scalar (it follows a space, a bracket, a comma or a
// colon) and not an apostrophe inside a word, as in README's.
func opensQuote(s string, i int) bool {
	return i == 0 || strings.IndexByte(" \t[,{:(", s[i-1]) >= 0
}

func unquote(s string) string {
	s = strings.TrimSpace(s)
	if len(s) >= 2 && (s[0] == '"' || s[0] == '\'') && s[len(s)-1] == s[0] {
		return s[1 : len(s)-1]
	}
	return s
}

// job is what the tests need to know about one job of a workflow; the file is read line by line, not parsed.
type job struct {
	ID              string
	Name            string // the name: value, or "" when the job has none
	If              string
	Timeout         string // timeout-minutes
	ContinueOnError bool
	Uses            string // a reusable workflow, when the job calls one
	Needs           []string
	Text            string // every line of the job, for what the fields above do not cover
}

type workflow struct {
	File string // base name, for example ci.yml
	Text string
	Name string // the top-level name:
	Jobs []*job
}

var jobHeader = regexp.MustCompile(`^  ([A-Za-z_][A-Za-z0-9_-]*):\s*(#.*)?$`)

// parseWorkflow reads the top-level name and the jobs (id, name, needs, if, timeout-minutes, continue-on-error, uses) of a
// workflow written in block style with two-space indentation, as every workflow of this repository is.
func parseWorkflow(file, text string) *workflow {
	w := &workflow{File: file, Text: text}
	var cur *job
	var curLines []string
	inJobs := false
	flush := func() {
		if cur != nil {
			cur.Text = strings.Join(curLines, "\n")
			fillJob(cur)
			w.Jobs = append(w.Jobs, cur)
		}
		cur, curLines = nil, nil
	}
	for _, l := range lines(text) {
		if indentOf(l) == 0 && !blankOrComment(l) {
			flush()
			inJobs = strings.HasPrefix(l, "jobs:")
			if strings.HasPrefix(l, "name:") && w.Name == "" {
				w.Name = unquote(uncomment(strings.TrimPrefix(l, "name:")))
			}
			continue
		}
		if !inJobs {
			continue
		}
		if m := jobHeader.FindStringSubmatch(l); m != nil {
			flush()
			cur = &job{ID: m[1]}
			continue
		}
		if cur != nil {
			curLines = append(curLines, l)
		}
	}
	flush()
	return w
}

func fillJob(j *job) {
	ls := lines(j.Text)
	for i, l := range ls {
		if indentOf(l) != 4 || blankOrComment(l) {
			continue
		}
		key, val, ok := strings.Cut(strings.TrimSpace(l), ":")
		if !ok {
			continue
		}
		val = strings.TrimSpace(uncomment(val))
		switch key {
		case "name":
			j.Name = unquote(val)
		case "if":
			j.If = val
			// a folded or literal block: take the lines that follow
			if val == ">-" || val == ">" || val == "|" || val == "|-" {
				var parts []string
				for _, n := range ls[i+1:] {
					if !blankOrComment(n) && indentOf(n) <= 4 {
						break
					}
					parts = append(parts, strings.TrimSpace(n))
				}
				j.If = strings.Join(parts, " ")
			}
		case "timeout-minutes":
			j.Timeout = val
		case "continue-on-error":
			j.ContinueOnError = val == "true"
		case "uses":
			j.Uses = val
		case "needs":
			j.Needs = listValue(val, ls[i+1:], 4)
		}
	}
}

// listValue reads a YAML list that is either inline ([a, b]), a single scalar, or a block of "- item" lines under a key
// at the given indentation.
func listValue(val string, rest []string, keyIndent int) []string {
	switch {
	case strings.HasPrefix(val, "["):
		inner := strings.TrimSuffix(strings.TrimPrefix(val, "["), "]")
		var out []string
		for _, p := range strings.Split(inner, ",") {
			if p = unquote(strings.TrimSpace(p)); p != "" {
				out = append(out, p)
			}
		}
		return out
	case val != "":
		return []string{unquote(val)}
	}
	var out []string
	for _, l := range rest {
		if blankOrComment(l) {
			continue
		}
		if indentOf(l) <= keyIndent {
			break
		}
		if item, ok := strings.CutPrefix(strings.TrimSpace(l), "- "); ok {
			out = append(out, unquote(uncomment(item)))
		}
	}
	return out
}

// workflows returns every workflow of the repository, by file name.
func workflows(t *testing.T) []*workflow {
	t.Helper()
	r := root(t)
	var out []*workflow
	for _, pat := range []string{"*.yml", "*.yaml"} {
		paths, _ := filepath.Glob(filepath.Join(r, ".github", "workflows", pat))
		for _, p := range paths {
			out = append(out, parseWorkflow(filepath.Base(p), read(t, ".github/workflows/"+filepath.Base(p))))
		}
	}
	if len(out) == 0 {
		t.Fatal(".github/workflows has no workflow files")
	}
	sort.Slice(out, func(i, j int) bool { return out[i].File < out[j].File })
	return out
}

func workflowNamed(t *testing.T, file string) *workflow {
	t.Helper()
	return parseWorkflow(file, read(t, ".github/workflows/"+file))
}

func (w *workflow) job(id string) *job {
	for _, j := range w.Jobs {
		if j.ID == id {
			return j
		}
	}
	return nil
}

// active returns the lines of text that are not comments, with trailing comments removed.
func active(text string) string {
	var out []string
	for _, l := range lines(text) {
		if !blankOrComment(l) {
			out = append(out, uncomment(l))
		}
	}
	return strings.Join(out, "\n")
}

// usesRef is one `uses:` line of a workflow.
type usesRef struct {
	Line    int
	Value   string // without quotes and without the trailing comment
	Comment string // the trailing comment, with its #
}

var usesLine = regexp.MustCompile(`^\s*(?:-\s+)?uses:\s*(.*)$`)

func usesRefs(text string) []usesRef {
	var out []usesRef
	for i, l := range lines(text) {
		if blankOrComment(l) {
			continue
		}
		m := usesLine.FindStringSubmatch(l)
		if m == nil {
			continue
		}
		val := uncomment(m[1])
		comment := strings.TrimSpace(strings.TrimPrefix(m[1], val))
		out = append(out, usesRef{Line: i + 1, Value: unquote(val), Comment: comment})
	}
	return out
}
