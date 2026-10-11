package taskgen

import (
	"context"
	"encoding/json"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/anemos-labs/sleipnir/internal/events"
	"github.com/anemos-labs/sleipnir/internal/rl"
	"github.com/anemos-labs/sleipnir/internal/rl/env"
)

func TestReadPicks(t *testing.T) {
	in := `# picks
abc123 trailing words are ignored

{"commit":"def456","parent":"abc123","issue":{"ref":"#9","title":"Clamp is wrong","body":"above the range"},"source":{"pr":"https://example.com/pr/1"}}
`
	got, err := ReadPicks(strings.NewReader(in))
	if err != nil {
		t.Fatal(err)
	}
	want := []Pick{
		{Commit: "abc123"},
		{Commit: "def456", Parent: "abc123", Issue: &Issue{Ref: "#9", Title: "Clamp is wrong", Body: "above the range"},
			Source: map[string]string{"pr": "https://example.com/pr/1"}},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %+v\nwant %+v", got, want)
	}
	for name, bad := range map[string]string{
		"unknown field": `{"commit":"a","pull":"1"}`,
		"no commit":     `{"parent":"a"}`,
		"empty issue":   `{"commit":"a","issue":{"title":" "}}`,
		"option-like":   `--all`,
		"option parent": `{"commit":"a","parent":"-x"}`,
		"broken json":   `{"commit":`,
		"nothing":       "# only a comment\n\n",
	} {
		if _, err := ReadPicks(strings.NewReader(bad)); err == nil {
			t.Errorf("%s: %q was accepted", name, bad)
		}
	}
}

func TestBuildIssuePrompt(t *testing.T) {
	sha := strings.Repeat("ab", 20)
	p := BuildIssuePrompt(Issue{
		Ref:   "org/repo#42",
		Title: "  Clamp returns\nthe lower bound ",
		Body: "<!-- Please fill in the template -->\r\nBug: Clamp(10, 0, 5) returns 0.\r\n\r\n\r\n\r\nSee https://github.com/org/repo/pull/43 and " +
			sha + ".\r\nhttps://github.com/org/repo/issues/41 is related.",
	})
	for _, want := range []string{"Issue: Clamp returns the lower bound\n\n", "Bug: Clamp(10, 0, 5) returns 0.\n\nSee", "issues/41 is related", "cannot see"} {
		if !strings.Contains(p, want) {
			t.Errorf("prompt lacks %q:\n%s", want, p)
		}
	}
	for _, bad := range []string{"<!--", "template", "pull/43", sha, "#42", "\r", "\n\n\n"} {
		if strings.Contains(p, bad) {
			t.Errorf("prompt contains %q:\n%s", bad, p)
		}
	}

	long := strings.Repeat("log line é\n", maxIssueBytes/8)
	p = BuildIssuePrompt(Issue{Title: "Crash", Body: long})
	if !strings.Contains(p, "[The rest of the issue is omitted.]") || len(p) > maxIssueBytes+500 {
		t.Errorf("long body: %d bytes, marker present %v", len(p), strings.Contains(p, "omitted"))
	}
	p = BuildIssuePrompt(Issue{Body: strings.Repeat("é", maxIssueBytes)}) // no line break to cut at
	if !strings.HasPrefix(p, "Issue:\n\n") || !strings.Contains(p, "omitted") || !utf8Valid(p) {
		t.Errorf("unbroken body: %q...", p[:40])
	}
}

func utf8Valid(s string) bool { return strings.ToValidUTF8(s, "\uFFFD") == s }

// Picks mine exactly the listed changes, in order, with the issue as the
// prompt; a pick that is not a candidate is rejected with its reason.
func TestFromGitPicks(t *testing.T) {
	f, ids := buildCalcRepo(t)
	m := newManager(t)
	hidden, gold := events.NewMemBlobs(), events.NewMemBlobs()
	picks := []Pick{
		{Commit: ids["clamp"], Issue: &Issue{Ref: "#7", Title: "Clamp returns the lower bound above the range",
			Body: "Clamp(10, 0, 5) returns 0 instead of 5."}, Source: map[string]string{"pr": "https://example.com/calc/pull/8"}},
		{Commit: ids["docs"]},
		{Commit: ids["testonly"]},
		{Commit: "0123456789abcdef0123456789abcdef01234567"},
		{Commit: ids["max"][:12]}, // any revision, and without an issue the commit message is the prompt
	}
	tasks, rep, err := FromGit(context.Background(), f.Dir, GitOptions{
		Workspaces: m, HiddenBlobs: hidden, GoldBlobs: gold, Timeout: time.Minute, IDPrefix: "calc", Picks: picks,
		Since: time.Date(2030, 1, 1, 0, 0, 0, 0, time.UTC), // dates do not filter picks
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := env.ValidateTasks(tasks); err != nil {
		t.Fatal(err)
	}
	if len(tasks) != 2 || tasks[0].ID != "calc-"+ids["clamp"][:8] || tasks[1].ID != "calc-"+ids["max"][:8] {
		t.Fatalf("tasks %v\nreport %+v", taskIDs(tasks), rep)
	}
	wantRejected := map[string]int{ReasonSourceMinimum: 2, ReasonUnknownCommit: 1}
	if !reflect.DeepEqual(rep.Rejected, wantRejected) || rep.Examined != 5 || rep.Candidates != 2 {
		t.Errorf("report: rejected %v examined %d candidates %d\n%+v", rep.Rejected, rep.Examined, rep.Candidates, rep.Rejections)
	}

	fix := tasks[0]
	if !strings.HasPrefix(fix.Prompt, "Issue: Clamp returns the lower bound above the range\n\nClamp(10, 0, 5) returns 0") ||
		strings.Contains(fix.Prompt, "Fixes #7") || strings.Contains(fix.Prompt, "Fix Clamp returning") {
		t.Errorf("the issue is not the prompt:\n%s", fix.Prompt)
	}
	if fix.Kind != rl.TaskFix || fix.Repo.Commit != f.git("rev-parse", ids["clamp"]+"^") {
		t.Errorf("kind %s start %s", fix.Kind, fix.Repo.Commit)
	}
	var meta Meta
	if err := json.Unmarshal(fix.Meta, &meta); err != nil {
		t.Fatal(err)
	}
	if meta.Issue != "#7" || meta.Source["pr"] != "https://example.com/calc/pull/8" || meta.Commit != ids["clamp"] {
		t.Errorf("meta: %+v", meta)
	}
	if !strings.Contains(tasks[1].Prompt, "Max returns the larger of two ints.") || tasks[1].Kind != rl.TaskFeature {
		t.Errorf("commit-message prompt: %s\n%s", tasks[1].Kind, tasks[1].Prompt)
	}
}

// A pull request merged with a merge commit is one pick: its first parent is the
// start, so the change and its test, made in separate commits on the branch,
// form one task. An explicit parent covers a range the same way.
func TestFromGitPicksMergedBranches(t *testing.T) {
	f := newFixture(t)
	f.write("go.mod", calcGoMod)
	f.write("calc.go", calcV1)
	f.write("calc_test.go", calcTestV1)
	f.write("LICENSE", mitLicense)
	base := f.commit("initial")
	f.git("checkout", "-q", "-b", "fix")
	f.write("calc.go", strings.Replace(calcV1, "return lo // BUG: should return hi", "return hi", 1))
	f.commit("return the upper bound")
	f.write("calc_test.go", calcTestV1+"\nfunc TestClampUpper(t *testing.T) {\n\tif Clamp(10, 0, 5) != 5 {\n\t\tt.Fatal()\n\t}\n}\n")
	branchTip := f.commit("test the upper bound")
	f.git("checkout", "-q", "main")
	f.git("merge", "-q", "--no-ff", "-m", "Merge pull request #2 from dev/fix", "fix")
	merge := f.head()

	m := newManager(t)
	issue := &Issue{Title: "Clamp is wrong above the range"}
	tasks, rep, err := FromGit(context.Background(), f.Dir, GitOptions{
		Workspaces: m, HiddenBlobs: events.NewMemBlobs(), Timeout: time.Minute, IDPrefix: "calc",
		Picks: []Pick{{Commit: merge, Issue: issue}, {Commit: branchTip, Parent: base, Issue: issue}},
	})
	if err != nil {
		t.Fatal(err)
	}
	// The second pick has the same reference solution and tests: a duplicate.
	if len(tasks) != 1 || rep.Rejected[ReasonDuplicate] != 1 {
		t.Fatalf("tasks %v report %+v", taskIDs(tasks), rep)
	}
	var meta Meta
	_ = json.Unmarshal(tasks[0].Meta, &meta)
	if tasks[0].Repo.Commit != base || !reflect.DeepEqual(meta.Files, []string{"calc.go", "calc_test.go"}) {
		t.Errorf("start %s files %v", tasks[0].Repo.Commit, meta.Files)
	}

	// Without the duplicate, the explicit range alone gives the same task.
	tasks, _, err = FromGit(context.Background(), f.Dir, GitOptions{
		NoValidate: true, IDPrefix: "calc", Picks: []Pick{{Commit: branchTip, Parent: base, Issue: issue}},
	})
	if err != nil || len(tasks) != 1 || tasks[0].Repo.Commit != base {
		t.Fatalf("range pick: %v %v", err, taskIDs(tasks))
	}
}

func taskIDs(ts []rl.Task) []string {
	out := make([]string, len(ts))
	for i := range ts {
		out[i] = ts[i].ID
	}
	return out
}
