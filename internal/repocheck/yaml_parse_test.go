package repocheck

import (
	"strings"
	"testing"
)

// The tests of this package read workflow files line by line. These cases pin what that reading accepts, so that a failure
// of another test means the repository drifted and not that the reader misread a file.

func TestParseWorkflow(t *testing.T) {
	text := `name: demo
on: push
permissions:
  contents: read
jobs:
  a:
    name: job a
    timeout-minutes: 5
    runs-on: ubuntu-latest
    steps:
      - run: echo a
  # a comment between jobs
  b:
    needs: [a]
    if: >-
      github.event_name == 'push' &&
      always()
    continue-on-error: true
    timeout-minutes: 10
    name: "quoted name" # trailing
  c:
    needs:
      - a
      - b # block form
    uses: ./.github/workflows/x.yml
`
	w := parseWorkflow("demo.yml", text)
	if w.Name != "demo" {
		t.Errorf("workflow name %q, want demo", w.Name)
	}
	var ids []string
	for _, j := range w.Jobs {
		ids = append(ids, j.ID)
	}
	if strings.Join(ids, ",") != "a,b,c" {
		t.Fatalf("jobs %v, want a,b,c", ids)
	}
	a, b, c := w.job("a"), w.job("b"), w.job("c")
	if a.Name != "job a" || a.Timeout != "5" || a.ContinueOnError || len(a.Needs) != 0 {
		t.Errorf("job a read as %+v", a)
	}
	if b.Name != "quoted name" || b.Timeout != "10" || !b.ContinueOnError || strings.Join(b.Needs, ",") != "a" {
		t.Errorf("job b read as %+v", b)
	}
	if b.If != "github.event_name == 'push' && always()" {
		t.Errorf("a folded if: read as %q", b.If)
	}
	if strings.Join(c.Needs, ",") != "a,b" || c.Uses != "./.github/workflows/x.yml" {
		t.Errorf("job c read as %+v", c)
	}
	if w.job("nope") != nil {
		t.Error("job() returned a job that does not exist")
	}
}

func TestUncomment(t *testing.T) {
	for in, want := range map[string]string{
		"a # b":         "a",
		`"a # b" # c`:   `"a # b"`,
		"a#b":           "a#b",
		"# whole":       "",
		`x: 'it's' # y`: `x: 'it's'`,
		"plain":         "plain",
	} {
		if got := uncomment(in); got != want {
			t.Errorf("uncomment(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestSteps(t *testing.T) {
	text := `jobs:
  j:
    steps:
      - uses: actions/checkout@3d3c42e5aac5ba805825da76410c181273ba90b1 # v7.0.1
        with:
          persist-credentials: false
      - name: two
        run: |
          echo hi
      # a comment
      - uses: other/thing@3d3c42e5aac5ba805825da76410c181273ba90b1 # v1
  k:
    steps:
      - run: echo k
`
	got := steps(text)
	if len(got) != 4 {
		t.Fatalf("got %d steps, want 4:\n%q", len(got), got)
	}
	if !strings.Contains(got[0], "persist-credentials: false") || strings.Contains(got[1], "persist-credentials") {
		t.Errorf("the credentials line belongs to the first step only: %q", got)
	}
	if strings.Contains(got[2], "run: echo k") {
		t.Errorf("a step leaked into the next job: %q", got[2])
	}
}

func TestBlocks(t *testing.T) {
	text := `jobs:
  j:
    steps:
      - run: echo one ${{ a }}
      - name: multi
        run: |
          echo two
          echo ${{ b }}
      - uses: actions/github-script@3d3c42e5aac5ba805825da76410c181273ba90b1 # v9
        with:
          script: |
            const x = 1;
      - run: |
          echo dash form
        shell: bash
      - run: echo after
`
	got := blocks(text)
	for _, want := range []string{"echo one ${{ a }}", "echo two", "echo ${{ b }}", "const x = 1;", "echo dash form", "echo after"} {
		if !strings.Contains(got, want) {
			t.Errorf("blocks() lost %q: %q", want, got)
		}
	}
	if strings.Contains(got, "uses:") || strings.Contains(got, "name: multi") || strings.Contains(got, "shell: bash") {
		t.Errorf("blocks() returned more than the run and script values: %q", got)
	}
}

func TestUsesRefs(t *testing.T) {
	text := `      - uses: actions/checkout@abc # v7
      - uses: "x/y@def"
        # uses: commented/out@1
      - run: echo uses: not/a/step@1
      - uses: ./local
`
	var got []string
	for _, u := range usesRefs(text) {
		got = append(got, u.Value+"|"+u.Comment)
	}
	want := []string{"actions/checkout@abc|# v7", "x/y@def|", "./local|"}
	if strings.Join(got, ";") != strings.Join(want, ";") {
		t.Errorf("usesRefs = %q, want %q", got, want)
	}
}
