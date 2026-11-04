package main

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/anemos-labs/sleipnir/internal/tooleval"
)

func TestToolevalRefusesWhatItCannotRun(t *testing.T) {
	dir := t.TempDir()
	bad := filepath.Join(dir, "variants.json")
	if err := os.WriteFile(bad, []byte(`{"current":{}}`), 0o644); err != nil {
		t.Fatal(err)
	}
	for name, c := range map[string]struct {
		args []string
		want string
	}{
		"no model":        {nil, "name the models"},
		"no such case":    {[]string{"--model", "x/y", "--case", "nothing-like-it"}, "no case matches"},
		"min pass":        {[]string{"--model", "x/y", "--min-pass", "1.5"}, "rate from 0 to 1"},
		"stray argument":  {[]string{"--model", "x/y", "extra"}, "unexpected argument"},
		"variant current": {[]string{"--model", "x/y", "--variants", bad}, "names the build's own"},
		"missing cases":   {[]string{"--model", "x/y", "--cases", filepath.Join(dir, "none.json")}, "no such file"},
		"harvest no log":  {[]string{"--harvest", filepath.Join(dir, "nosession")}, "nosession"},
	} {
		var out, errb bytes.Buffer
		err := cmdTooleval(context.Background(), c.args, &out, &errb)
		if err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: err %v, want %q", name, err, c.want)
		}
	}
}

func TestBelowMinPassCountsUnansweredSamplesAgainstTheModel(t *testing.T) {
	r := &tooleval.Report{Models: []string{"a", "b", "c"}, Samples: []tooleval.Sample{
		{Model: "a", Variant: tooleval.Current, Pass: true}, {Model: "a", Variant: tooleval.Current, Pass: true},
		{Model: "b", Variant: tooleval.Current, Pass: true}, {Model: "b", Variant: tooleval.Current, Error: "no answer"},
		{Model: "c", Variant: "shorter", Pass: true},
	}}
	if got := belowMinPass(r, 0.75); strings.Join(got, ",") != "b,c" {
		t.Fatalf("got %v", got)
	}
	if got := belowMinPass(r, 0); got != nil {
		t.Fatalf("no bound: %v", got)
	}
}
