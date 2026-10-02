package main

import (
	"bytes"
	"reflect"
	"strings"
	"testing"

	"github.com/anemos-labs/sleipnir/internal/perm"
	"github.com/anemos-labs/sleipnir/internal/session"
)

func TestSuggestAllowNamesTheSubcommandAndSkipsWhatIsHarmless(t *testing.T) {
	for _, c := range []struct {
		command string
		want    []string
	}{
		{`go test -race -timeout 60s ./... 2>&1; echo "exit: $?"`, []string{"Bash(go test:*)"}},
		{`cd /workspace && go test ./...`, []string{"Bash(go test:*)"}},
		{`go vet ./... && go test ./...`, []string{"Bash(go vet:*)", "Bash(go test:*)"}},
		{`npm install left-pad`, []string{"Bash(npm install:*)"}},
		{`cargo build --release`, []string{"Bash(cargo build:*)"}},
		{`CGO_ENABLED=0 make -j4 test`, []string{"Bash(make:*)"}}, // the first argument is a flag: no subcommand to name
		{`python -c 'print(1)'`, nil},                             // a rule for an interpreter is a rule to run anything
		{`python wordfreq.py sample.txt 3`, []string{"Bash(python wordfreq.py:*)"}},
		{`python3 - <<'EOF'
print(1)
EOF`, nil},
		{`rm -rf build && go build ./...`, []string{"Bash(go build:*)"}},
		{`sudo make install`, nil},
		{`pytest -x`, []string{"Bash(pytest:*)"}},
		{`ls -la | grep foo`, nil},
		{`./scripts/run.sh`, nil}, // a program that is not the one of that name is not one a rule can name
		{`echo $(`, nil},          // unreadable
	} {
		if got := suggestAllow(c.command); !reflect.DeepEqual(got, c.want) {
			t.Errorf("%q: got %q, want %q", c.command, got, c.want)
		}
	}
}

// Every rule of the tests preset is one the engine accepts, and none lets a program run what it is given: no bare interpreter, no
// install, no download.
func TestTestsPresetIsValidAndNamesNoInterpreterOrInstaller(t *testing.T) {
	for _, r := range testsAllow {
		if _, err := perm.ParseRule(perm.Allow, r); err != nil {
			t.Errorf("%s: %v", r, err)
		}
		for _, bad := range []string{"Bash(python:", "Bash(python3:", "Bash(node:", "Bash(npx:", "Bash(go:", "Bash(go run", "install", "curl", "wget", "pip", "go get", "go generate"} {
			if strings.Contains(r, bad) {
				t.Errorf("the tests preset holds %s, which runs more than the project's build and tests", r)
			}
		}
	}
	got := expandAllow([]string{"Edit(docs/**)", "tests", " tests "})
	if len(got) != 1+2*len(testsAllow) || got[0] != "Edit(docs/**)" {
		t.Errorf("expandAllow: %d rules, first %q", len(got), got[0])
	}
}

func TestPrintRefusalsSaysWhatWasRefusedAndHowToAllowIt(t *testing.T) {
	var out bytes.Buffer
	printRefusals(&out, nil, "/w")
	if out.Len() != 0 {
		t.Fatalf("nothing was refused: %q", out.String())
	}
	printRefusals(&out, []session.RefusedCommand{{Command: "go test -race ./...", Times: 2}, {Command: "go vet ./...", Times: 1}}, "/w")
	for _, want := range []string{"refused, because this run had no one to ask:", "go test -race ./... (2 times)", "go vet ./...\n", "--allow 'Bash(go test:*)' --allow 'Bash(go vet:*)'", "--allow tests"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("the hint lacks %q:\n%s", want, out.String())
		}
	}
}

// An edit that was refused for want of anyone to ask is listed too, and what lets it through is the mode that accepts edits, not a rule for
// a command (a run that was told only about `go test` was refused its edit again the next time).
func TestPrintRefusalsNamesARefusedEditAndTheModeThatAcceptsEdits(t *testing.T) {
	var out bytes.Buffer
	printRefusals(&out, []session.RefusedCommand{{Path: "/w/pkg/slug.go", Times: 1}, {Command: "go test ./...", Times: 2}}, "/w")
	for _, want := range []string{"  edit pkg/slug.go\n", "  go test ./... (2 times)\n", "--mode accept-edits --allow 'Bash(go test:*)'", "--allow tests"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("the hint lacks %q:\n%s", want, out.String())
		}
	}
	out.Reset()
	printRefusals(&out, []session.RefusedCommand{{Path: "/w/a.go", Times: 3}, {Path: "/elsewhere/b.go", Times: 1}}, "/w")
	if want := "refused, because this run had no one to ask:\n  edit a.go (3 times)\n  edit /elsewhere/b.go\nto let them through next time: --mode accept-edits\n"; out.String() != want {
		t.Errorf("only edits:\n%q\nwant\n%q", out.String(), want)
	}
}
