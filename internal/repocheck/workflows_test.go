package repocheck

import (
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"testing"
)

var (
	fullSHA        = regexp.MustCompile(`^[0-9a-f]{40}$`)
	dockerDigest   = regexp.MustCompile(`@sha256:[0-9a-f]{64}$`)
	versionComment = regexp.MustCompile(`^#\s*v?[0-9]+(\.[0-9]+)*`)
	stepStart      = regexp.MustCompile(`^      - `)
)

// A tag or a branch can be moved to other code after it was reviewed; a commit cannot. scripts/check-pins.sh is the same
// rule for a developer's shell.
func TestWorkflowUsesArePinned(t *testing.T) {
	n := 0
	for _, w := range workflows(t) {
		for _, u := range usesRefs(w.Text) {
			n++
			where := ".github/workflows/" + w.File + ":" + strconv.Itoa(u.Line) + ": uses: " + u.Value
			switch {
			case strings.HasPrefix(u.Value, "./") || strings.HasPrefix(u.Value, "../"):
			case strings.HasPrefix(u.Value, "docker://"):
				if !dockerDigest.MatchString(u.Value) {
					t.Errorf("%s: a docker image must be pinned by digest (docker://IMAGE@sha256:<64 hex>)", where)
				}
			default:
				name, ref, ok := strings.Cut(u.Value, "@")
				if !ok || !fullSHA.MatchString(ref) {
					t.Errorf("%s: pin it to a full 40-hex commit SHA and keep the version as a comment: %s@<sha> # vX.Y.Z (a tag or a branch can be moved)", where, name)
				} else if !versionComment.MatchString(u.Comment) {
					t.Errorf("%s: the SHA has no version comment after it (# vX.Y.Z); Dependabot and readers rely on it", where)
				}
			}
		}
	}
	if n == 0 {
		t.Fatal("found no uses: line in any workflow: the test is not reading them")
	}
}

// steps splits the text of a workflow into its steps: a line that starts a list item at the indentation of steps, and what
// follows it up to the next one.
func steps(text string) []string {
	var out []string
	var cur []string
	for _, l := range lines(text) {
		switch {
		case stepStart.MatchString(l):
			if cur != nil {
				out = append(out, strings.Join(cur, "\n"))
			}
			cur = []string{l}
		case cur != nil && !blankOrComment(l) && indentOf(l) < 6:
			out = append(out, strings.Join(cur, "\n"))
			cur = nil
		case cur != nil:
			cur = append(cur, l)
		}
	}
	if cur != nil {
		out = append(out, strings.Join(cur, "\n"))
	}
	return out
}

// A job without a timeout can hold a runner for six hours; a workflow without permissions gets the repository's default;
// a checkout that keeps its credentials leaves the token in .git/config for every step after it.
func TestWorkflowsHaveTimeoutsAndPermissions(t *testing.T) {
	for _, w := range workflows(t) {
		hasPermissions := false
		for _, l := range lines(w.Text) {
			if strings.HasPrefix(l, "permissions:") {
				hasPermissions = true
			}
		}
		if !hasPermissions {
			t.Errorf(".github/workflows/%s has no top-level permissions: (contents: read, or {} when each job declares its own)", w.File)
		}
		if len(w.Jobs) == 0 {
			t.Errorf(".github/workflows/%s: no jobs were found", w.File)
		}
		for _, j := range w.Jobs {
			if j.Uses == "" && j.Timeout == "" {
				t.Errorf(".github/workflows/%s: job %q has no timeout-minutes", w.File, j.ID)
			}
		}
		for _, s := range steps(w.Text) {
			if strings.Contains(active(s), "uses: actions/checkout@") && !strings.Contains(active(s), "persist-credentials: false") {
				t.Errorf(".github/workflows/%s: an actions/checkout step does not set persist-credentials: false:\n%s", w.File, s)
			}
		}
	}
	ci := active(read(t, ".github/workflows/ci.yml"))
	if !strings.Contains(ci, "GOTOOLCHAIN: local") {
		t.Error("ci.yml does not set GOTOOLCHAIN: local: a newer toolchain would be downloaded silently")
	}
	if !strings.Contains(ci, "cancel-in-progress: ${{ github.event_name == 'pull_request' }}") {
		t.Error("ci.yml must cancel superseded runs of pull requests only: cancel-in-progress: ${{ github.event_name == 'pull_request' }}")
	}
}

// The ruleset requires one check, ci-gate. It must exist, must always run, and must wait for every job that matters.
func TestCIGate(t *testing.T) {
	ci := workflowNamed(t, "ci.yml")
	var gate *job
	for _, j := range ci.Jobs {
		if j.Name == "ci-gate" {
			gate = j
		}
	}
	if gate == nil {
		t.Fatal("ci.yml has no job named ci-gate: .github/rulesets/main.json requires that check, so every pull request would wait for it forever")
	}
	if !strings.Contains(gate.If, "always()") {
		t.Errorf("the ci-gate job must run `if: always()` (got %q): when a needed job fails it is skipped otherwise, and a skipped required check counts as passed", gate.If)
	}
	needs := map[string]bool{}
	for _, n := range gate.Needs {
		needs[n] = true
		j := ci.job(n)
		switch {
		case j == nil:
			t.Errorf("ci-gate needs %q, which is not a job of ci.yml", n)
		case j.ContinueOnError:
			t.Errorf("ci-gate needs %q, which is continue-on-error: an informational job must not be part of the gate", n)
		}
	}
	for _, j := range ci.Jobs {
		if j != gate && !j.ContinueOnError && !needs[j.ID] {
			t.Errorf("the job %q of ci.yml is not in the needs of ci-gate: a failure there would not block a merge (add it, or mark it continue-on-error if it is informational)", j.ID)
		}
	}
	if !strings.Contains(gate.Text, "toJSON(needs)") {
		t.Error("ci-gate must read its verdict from toJSON(needs), through env")
	}
	if strings.Contains(blocks(gate.Text), "${{") {
		t.Error("ci-gate interpolates ${{ }} into its script: pass values through env instead")
	}
}

// blocks returns the text of the run: and script: values of a workflow or a job: what a shell or github-script executes.
func blocks(text string) string {
	var out []string
	ls := lines(text)
	for i, l := range ls {
		trimmed := strings.TrimSpace(l)
		keyIndent := indentOf(l)
		if rest, ok := strings.CutPrefix(trimmed, "- "); ok {
			trimmed, keyIndent = rest, keyIndent+2
		}
		key, rest, ok := strings.Cut(trimmed, ":")
		if !ok || (key != "run" && key != "script") {
			continue
		}
		rest = strings.TrimSpace(uncomment(rest))
		if rest != "|" && rest != ">" && rest != "|-" && rest != ">-" {
			out = append(out, rest)
			continue
		}
		for _, n := range ls[i+1:] {
			if !blankOrComment(n) && indentOf(n) <= keyIndent {
				break
			}
			out = append(out, n)
		}
	}
	return strings.Join(out, "\n")
}

// A pull_request_target workflow runs with the base repository's token on text an outsider wrote. The only one is pr.yml,
// and it must not check anything out: nothing of the pull request may ever be run there.
func TestPullRequestTargetHasNoCheckout(t *testing.T) {
	write := regexp.MustCompile(`(?m)^\s+(contents|actions|id-token|packages|issues|security-events|statuses|checks|deployments|pages|attestations): *write`)
	sawPR := false
	for _, w := range workflows(t) {
		body := active(w.Text)
		if !strings.Contains(body, "pull_request_target") {
			continue
		}
		if w.File != "pr.yml" {
			t.Errorf(".github/workflows/%s uses pull_request_target: only pr.yml may (a pull request's text runs with the base repository's token there)", w.File)
		}
		sawPR = sawPR || w.File == "pr.yml"
		if strings.Contains(body, "actions/checkout") {
			t.Errorf(".github/workflows/%s combines pull_request_target with actions/checkout: never check out code in a workflow that has the base repository's token", w.File)
		}
		if m := write.FindString(body); m != "" {
			t.Errorf(".github/workflows/%s has %q: a pull_request_target workflow may write pull requests and nothing else", w.File, strings.TrimSpace(m))
		}
		if strings.Contains(blocks(w.Text), "${{") {
			t.Errorf(".github/workflows/%s interpolates ${{ }} into a shell script or a github-script: the title and the body of a pull request are data; read them from the event inside the script", w.File)
		}
	}
	if !sawPR {
		t.Error("pr.yml (pull_request_target) is missing: titles are not validated and pull requests are not labelled")
	}
}

// The release workflow is the only one that can publish, and only behind its conditions.
func TestReleaseIsGated(t *testing.T) {
	rel := workflowNamed(t, "release.yml")
	body := active(rel.Text)
	ci := workflowNamed(t, "ci.yml")
	m := regexp.MustCompile(`(?m)^\s+workflows:\s*\[([^\]]*)\]`).FindStringSubmatch(body)
	if m == nil {
		t.Fatal("release.yml has no `workflows: [ci]` under workflow_run: a merge would not release")
	}
	for _, name := range strings.Split(m[1], ",") {
		if name = unquote(strings.TrimSpace(name)); name != ci.Name {
			t.Errorf("release.yml follows the workflow %q, but ci.yml is named %q: rename one, or the release never starts", name, ci.Name)
		}
	}
	plan := rel.job("plan")
	if plan == nil {
		t.Fatal("release.yml has no plan job")
	}
	for _, cond := range []string{
		"github.event.workflow_run.conclusion == 'success'",
		"github.event.workflow_run.event == 'push'",
		"github.event.workflow_run.head_repository.full_name == github.repository",
	} {
		if !strings.Contains(plan.If, cond) {
			t.Errorf("the plan job of release.yml lost a guard of its workflow_run trigger: %s (a fork's pull request from a branch named main would pass the branch filter)", cond)
		}
	}
	if !strings.Contains(body, "scripts/release-plan.sh") {
		t.Error("release.yml does not run scripts/release-plan.sh: the decision to release would not be the tested one")
	}
	if !strings.Contains(body, "AUTO_RELEASE: ${{ vars.AUTO_RELEASE }}") {
		t.Error("release.yml does not pass the repository variable AUTO_RELEASE to the plan: the off switch would not work")
	}
	var writers []string
	for _, j := range rel.Jobs {
		if strings.Contains(active(j.Text), "contents: write") {
			writers = append(writers, j.ID)
		}
	}
	if strings.Join(writers, ",") != "publish" {
		t.Errorf("only the publish job of release.yml may have contents: write; found it in %v", writers)
	}
	pub := rel.job("publish")
	if pub == nil {
		t.Fatal("release.yml has no publish job")
	}
	for _, cond := range []string{"needs.plan.outputs.release == 'true'", "github.ref == 'refs/heads/main'"} {
		if !strings.Contains(pub.If, cond) {
			t.Errorf("the publish job of release.yml lost a condition: %s", cond)
		}
	}
	if !strings.Contains(active(pub.Text), "gh release view") {
		t.Error("the publish job must do nothing when the release exists (gh release view): a re-run would fail or publish twice")
	}
	for _, line := range strings.Split(blocks(rel.Text), "\n") {
		if strings.Contains(line, "git push") {
			t.Errorf("release.yml pushes with git (%q): the release API creates the tag", strings.TrimSpace(line))
		}
	}
}

// One pinned version of a tool, in every workflow that runs it.
func TestToolVersionsAgree(t *testing.T) {
	re := regexp.MustCompile(`golang\.org/x/vuln/cmd/govulncheck@(\S+)`)
	seen := map[string][]string{}
	for _, w := range workflows(t) {
		for _, m := range re.FindAllStringSubmatch(active(w.Text), -1) {
			seen[m[1]] = append(seen[m[1]], w.File)
		}
	}
	if len(seen) == 0 {
		t.Fatal("no workflow runs govulncheck: known vulnerabilities would go unnoticed")
	}
	if len(seen) > 1 {
		t.Errorf("govulncheck is pinned to different versions in different workflows: %v", seen)
	}
}

// Dependabot is what keeps the pins and the modules from going stale.
func TestDependabotCoversWorkflowsAndModules(t *testing.T) {
	d := active(read(t, ".github/dependabot.yml"))
	for _, eco := range []string{"gomod", "github-actions"} {
		if !regexp.MustCompile(`package-ecosystem:\s*"?` + eco + `"?`).MatchString(d) {
			t.Errorf(".github/dependabot.yml has no %s ecosystem: its pins would go stale unnoticed", eco)
		}
	}
	if strings.Count(d, `prefix: "build(deps)"`) < 2 {
		t.Error(`.github/dependabot.yml: every ecosystem needs commit-message prefix: "build(deps)" (pr.yml labels it, release.yml sorts it)`)
	}
	if strings.Count(d, "- dependencies") < 2 {
		t.Error(".github/dependabot.yml: every ecosystem needs the label dependencies")
	}
}

// The labels that pr.yml sets and the categories of the release notes must be the same set.
func TestPullRequestLabelsMatchReleaseNotes(t *testing.T) {
	set := map[string]bool{}
	for _, m := range regexp.MustCompile(`(?m)^\s+([a-z-]+): \{color: '[0-9a-f]{6}', description:`).FindAllStringSubmatch(read(t, ".github/workflows/pr.yml"), -1) {
		set[m[1]] = true
	}
	notes := map[string]bool{}
	excluded := map[string]bool{}
	section := ""
	for _, l := range lines(read(t, ".github/release.yml")) {
		s := strings.TrimSpace(uncomment(l))
		switch {
		case s == "exclude:" || s == "categories:":
			section = s
		case strings.HasPrefix(s, "- title:"):
		case strings.HasPrefix(s, "- ") && section == "exclude:":
			excluded[unquote(strings.TrimPrefix(s, "- "))] = true
		case strings.HasPrefix(s, "- ") && section == "categories:":
			if label := unquote(strings.TrimPrefix(s, "- ")); label != "*" {
				notes[label] = true
			}
		}
	}
	if len(set) == 0 || len(notes) == 0 {
		t.Fatalf("read %d labels from pr.yml and %d from release.yml: the test is not reading them", len(set), len(notes))
	}
	for l := range notes {
		if !set[l] {
			t.Errorf(".github/release.yml sorts by the label %q, which pr.yml never sets: those pull requests would all land in \"Other changes\"", l)
		}
	}
	for l := range set {
		if !notes[l] {
			t.Errorf("pr.yml sets the label %q, which .github/release.yml has no category for", l)
		}
	}
	if !excluded["skip-changelog"] {
		t.Error(".github/release.yml must exclude the label skip-changelog")
	}
}

// A script that a developer, a hook or CI runs must run: executable, or started with sh by the workflows that use it. The
// Unix execute bit does not exist on Windows, where the file modes say nothing.
func TestScriptsAreExecutableOrInvokedWithSh(t *testing.T) {
	paths, _ := filepath.Glob(filepath.Join(root(t), "scripts", "*.sh"))
	if len(paths) == 0 {
		t.Fatal("scripts/ has no .sh files")
	}
	wf := workflows(t)
	for _, p := range paths {
		ref := "scripts/" + filepath.Base(p)
		info, err := os.Stat(p)
		if err != nil {
			t.Fatal(err)
		}
		if info.Mode()&0o111 != 0 || runtime.GOOS == "windows" {
			continue
		}
		withSh := regexp.MustCompile(`\b(sh|bash)\s+(-\S+\s+)*` + regexp.QuoteMeta(ref))
		used := false
		for _, w := range wf {
			for i, l := range lines(w.Text) {
				if blankOrComment(l) || !strings.Contains(l, ref) {
					continue
				}
				used = true
				if !withSh.MatchString(l) {
					t.Errorf(".github/workflows/%s:%d runs %s without sh, and the file is not executable: git update-index --chmod=+x %s, or run it as `sh %s`", w.File, i+1, ref, ref, ref)
				}
			}
		}
		if !used {
			t.Errorf("%s is not executable and no workflow runs it with sh: git update-index --chmod=+x %s", ref, ref)
		}
	}
}

// What a developer runs (scripts/check.sh) and what CI runs must not drift apart: every drift check and the tests of the
// scripts are in both.
func TestDriftChecksAreWired(t *testing.T) {
	ci := active(read(t, ".github/workflows/ci.yml"))
	check := active(read(t, "scripts/check.sh"))
	paths, _ := filepath.Glob(filepath.Join(root(t), "scripts", "check-*.sh"))
	var names []string
	for _, p := range paths {
		if n := filepath.Base(p); !strings.HasSuffix(n, "_test.sh") {
			names = append(names, n)
		}
	}
	sort.Strings(names)
	if len(names) == 0 {
		t.Fatal("scripts/ has no check-*.sh")
	}
	for _, n := range names {
		if !strings.Contains(ci, "scripts/"+n) {
			t.Errorf("scripts/%s is not run by ci.yml (the lint job): a developer's check would not be CI's", n)
		}
		if !strings.Contains(check, "scripts/"+n) {
			t.Errorf("scripts/%s is not run by scripts/check.sh: CI would fail on something a developer's check does not see", n)
		}
	}
	for _, both := range []string{"scripts/*_test.sh", "scripts/gen-cli-docs.sh --check", "go mod tidy -diff", "go mod verify"} {
		if !strings.Contains(ci, both) {
			t.Errorf("ci.yml does not run %q", both)
		}
		if !strings.Contains(check, both) {
			t.Errorf("scripts/check.sh does not run %q", both)
		}
	}
}
