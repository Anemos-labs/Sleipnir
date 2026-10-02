package main

import (
	"bytes"
	"reflect"
	"strings"
	"testing"
)

func TestSplitArgsHonoursQuotesAndNothingElse(t *testing.T) {
	for in, want := range map[string][]string{
		``:          nil,
		`--swarm 8`: {"--swarm", "8"},
		`--verify "go test {dirs}" --isolation worktree`: {"--verify", "go test {dirs}", "--isolation", "worktree"},
		`--verify 'make test' x`:                         {"--verify", "make test", "x"},
		`  a   b  `:                                      {"a", "b"},
		`--x ""`:                                         {"--x", ""},
		`a$HOME \n`:                                      {`a$HOME`, `\n`},
	} {
		if got := splitArgs(in); !reflect.DeepEqual(got, want) {
			t.Errorf("%q: %q, want %q", in, got, want)
		}
	}
}

func TestParseBudget(t *testing.T) {
	for in, want := range map[string]float64{"5": 5, "$5": 5, "0.50": 0.5, "off": 0, "NONE": 0, "0": 0} {
		if got, err := parseBudget(in); err != nil || got != want {
			t.Errorf("%q: %v %v", in, got, err)
		}
	}
	for _, bad := range []string{"", "lots", "-1", "NaN", "Inf"} {
		if _, err := parseBudget(bad); err == nil {
			t.Errorf("%q was accepted", bad)
		}
	}
}

func TestProgramCommandsAnswerWithoutASession(t *testing.T) {
	h := &sessionHost{} // /verbose, /anim and the usage of /swarm never touch the session
	var out bytes.Buffer
	res, ok := h.programCommand("/verbose", &out)
	if !ok || res.Verbose != "on" {
		t.Errorf("/verbose: %+v %v", res, ok)
	}
	res, _ = h.programCommand("/verbose off", &out)
	if res.Verbose != "off" {
		t.Errorf("/verbose off: %+v", res)
	}
	res, _ = h.programCommand("/anim off", &out)
	if res.Anim != "off" {
		t.Errorf("/anim off: %+v", res)
	}
	out.Reset()
	if res, _ = h.programCommand("/anim maybe", &out); res.Anim != "" || !strings.Contains(out.String(), "usage: /anim [on|off]") {
		t.Errorf("/anim maybe: %+v %q", res, out.String())
	}
	out.Reset()
	if res, _ = h.programCommand("/swarm", &out); res.Restart != nil || !strings.Contains(out.String(), "usage: /swarm <workers>") {
		t.Errorf("/swarm alone: %+v %q", res, out.String())
	}
	if _, ok := h.programCommand("/cost", &out); ok {
		t.Error("/cost is not a program command")
	}
}
