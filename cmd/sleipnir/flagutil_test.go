package main

import (
	"flag"
	"io"
	"reflect"
	"testing"
)

func TestParseInterspersedAllowsFlagsAfterPositionals(t *testing.T) {
	newFS := func() (*flag.FlagSet, *string, *int, *bool) {
		fs := flag.NewFlagSet("t", flag.ContinueOnError)
		fs.SetOutput(io.Discard)
		v := fs.String("verify", "", "")
		n := fs.Int("swarm", 0, "")
		q := fs.Bool("quiet", false, "")
		return fs, v, n, q
	}
	cases := []struct {
		name string
		args []string
		pos  []string
		v    string
		n    int
		q    bool
	}{
		{"flags first", []string{"--verify", "make test", "goal"}, []string{"goal"}, "make test", 0, false},
		{"flags last", []string{"goal", "--verify", "make test"}, []string{"goal"}, "make test", 0, false},
		{"flags between", []string{"--swarm", "8", "add pagination", "--verify", "make test", "--quiet"}, []string{"add pagination"}, "make test", 8, true},
		{"several words", []string{"fix", "the", "bug", "--quiet"}, []string{"fix", "the", "bug"}, "", 0, true},
		{"terminator", []string{"--quiet", "--", "--verify", "x"}, []string{"--verify", "x"}, "", 0, true},
		{"terminator after positional", []string{"goal", "--", "--quiet"}, []string{"goal", "--quiet"}, "", 0, false},
		{"none", nil, nil, "", 0, false},
		{"equals form", []string{"goal", "--verify=go test ./..."}, []string{"goal"}, "go test ./...", 0, false},
	}
	for _, c := range cases {
		fs, v, n, q := newFS()
		pos, err := parseInterspersed(fs, c.args)
		if err != nil {
			t.Fatalf("%s: %v", c.name, err)
		}
		if !reflect.DeepEqual(pos, c.pos) && !(len(pos) == 0 && len(c.pos) == 0) {
			t.Errorf("%s: positional = %q, want %q", c.name, pos, c.pos)
		}
		if *v != c.v || *n != c.n || *q != c.q {
			t.Errorf("%s: flags = %q %d %v, want %q %d %v", c.name, *v, *n, *q, c.v, c.n, c.q)
		}
	}
}

func TestParseInterspersedReportsUnknownFlags(t *testing.T) {
	fs := flag.NewFlagSet("t", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	if _, err := parseInterspersed(fs, []string{"goal", "--nope"}); err == nil {
		t.Fatal("an unknown flag after a positional argument must be an error, not part of the goal")
	}
}
