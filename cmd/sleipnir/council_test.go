package main

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCouncilRefusesWhatItCannotJudge(t *testing.T) {
	dir := t.TempDir()
	shot := filepath.Join(dir, "a.png")
	if err := os.WriteFile(shot, []byte("\x89PNG\r\n\x1a\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	for name, c := range map[string]struct {
		args []string
		want string
	}{
		"no screenshot":     {[]string{"--model", "x/y"}, "pass the screenshots"},
		"not an image":      {[]string{"--model", "x/y", filepath.Join(dir, "notes.txt")}, "not a PNG"},
		"bad reference":     {[]string{"--model", "x/y", "--ref", "nameless", shot}, "is not NAME=PATH"},
		"no member":         {[]string{shot}, "name its members"},
		"repeated category": {[]string{"--model", "x/y", "--categories", "a,A", shot}, "repeated"},
	} {
		var out, errb bytes.Buffer
		err := cmdCouncil(context.Background(), c.args, &out, &errb)
		if err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: err %v, want %q", name, err, c.want)
		}
	}
}

func TestMemberName(t *testing.T) {
	if got := memberName("qwen/qwen3.8-flash"); got != "qwen3.8-flash" {
		t.Fatal(got)
	}
}
