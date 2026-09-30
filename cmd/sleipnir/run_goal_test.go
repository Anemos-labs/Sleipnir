package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// pipeWith is a pipe that has data in it and is closed: what `producer | sleipnir run` hands over.
func pipeWith(t *testing.T, data string) *os.File {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { r.Close() })
	go func() { w.WriteString(data); w.Close() }()
	return r
}

func TestReadGoalComposesTheWordsAndThePipedInput(t *testing.T) {
	devnull, err := os.Open(os.DevNull)
	if err != nil {
		t.Fatal(err)
	}
	defer devnull.Close()
	const wrapped = "<stdin>\nthe log\n</stdin>\n\nwhy did it fail"
	for _, tc := range []struct {
		name  string
		words []string
		stdin *os.File
		want  string
	}{
		{"words and nothing piped", []string{"why", "did", "it", "fail"}, devnull, "why did it fail"},
		{"words and input: the input first, in a block, the words last", []string{"why did it fail"}, pipeWith(t, "the log\n"), wrapped},
		{"a trailing - says the input goes with the words", []string{"why did it fail", "-"}, pipeWith(t, "the log\n"), wrapped},
		{"a lone - is the input", []string{"-"}, pipeWith(t, "  only this \n"), "only this"},
		{"no words: the input is the goal", nil, pipeWith(t, "only this\n"), "only this"},
		{"words and an input that is only white space", []string{"go"}, pipeWith(t, " \n\n"), "go"},
		{"words and an empty pipe", []string{"go"}, pipeWith(t, ""), "go"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, note, err := readGoal(tc.words, tc.stdin)
			if err != nil || note != "" || got != tc.want {
				t.Fatalf("readGoal = %q, %q, %v; want %q", got, note, err, tc.want)
			}
		})
	}
}

// A pipe that nobody writes to and nobody closes must not hold a run that has its goal in words; one that was asked for with - is
// waited for, however long it takes.
func TestReadGoalWaitsForAnExplicitPipeButNotForAnIdleOne(t *testing.T) {
	prev := stdinGrace
	stdinGrace = 20 * time.Millisecond
	t.Cleanup(func() { stdinGrace = prev })

	idleR, idleW, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer idleR.Close()
	defer idleW.Close()
	got, note, err := readGoal([]string{"go"}, idleR)
	if err != nil || got != "go" || !strings.Contains(note, "no data came on standard input") {
		t.Fatalf("an idle pipe: readGoal = %q, %q, %v", got, note, err)
	}

	slowR, slowW, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer slowR.Close()
	started := make(chan struct{})
	go func() {
		<-started
		time.Sleep(10 * stdinGrace) // well after the grace period a bare prompt would have given up at
		slowW.WriteString("late data\n")
		slowW.Close()
	}()
	close(started)
	got, note, err = readGoal([]string{"-"}, slowR)
	if err != nil || note != "" || got != "late data" {
		t.Fatalf("an explicit -: readGoal = %q, %q, %v; it waits for the input", got, note, err)
	}
}

func TestReadGoalRefusesAnInputThatIsTooLarge(t *testing.T) {
	path := filepath.Join(t.TempDir(), "big")
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := f.Truncate(maxPromptInput + 1); err != nil {
		t.Fatal(err)
	}
	f.Close()
	in, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer in.Close()
	if _, _, err := readGoal([]string{"summarise"}, in); err == nil || !strings.Contains(err.Error(), "larger than 8 MiB") {
		t.Fatalf("err = %v, want a refusal that says how large an input may be", err)
	}
}

func TestUnfinishedErrorIsStatusThree(t *testing.T) {
	err := unfinishedError("running: be-1 (T1)")
	var code int
	if ee, ok := err.(*exitError); ok {
		code = ee.code
	}
	if code != exitUnfinished || !strings.Contains(err.Error(), "running: be-1 (T1)") {
		t.Fatalf("unfinishedError = %v (status %d), want status %d and what was left", err, code, exitUnfinished)
	}
	if got := reportError(&strings.Builder{}, err); got != 3 {
		t.Fatalf("the process would exit %d, want 3", got)
	}
}
