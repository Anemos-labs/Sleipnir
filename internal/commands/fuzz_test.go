package commands

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
)

// FuzzExpand: whatever a template and its arguments contain, expansion neither
// panics nor hangs, stays within its limits, and an argument can only ever
// reach a shell command as a single-quoted word.
func FuzzExpand(f *testing.F) {
	for _, seed := range [][2]string{
		{"", ""}, {"plain", "x"}, {"Fix $ARGUMENTS", "a b"}, {"!`echo $1`", "x"}, {"@a.txt and @$1", "a.txt"},
		{"```\n!`x`\n```", ""}, {"!`a` !`b` !`c` !`d` !`e` !`f` !`g` !`h` !`i` !`j`", ""}, {"---\nmodel: x\n---\n@a.txt", "y"},
		{"$$1 $ARGUMENTS[0] $ARGUMENTS[", "1"}, {"@../../etc/passwd @/etc/passwd @~/x", ""}, {"!`", "!`x`"}, {"@", "@"},
	} {
		f.Add(seed[0], seed[1])
	}
	f.Fuzz(func(t *testing.T, tmpl, args string) {
		w := newWorld(t)
		put(t, filepath.Join(w.root, "a.txt"), "A")
		w.proj(".claude", "c.md", tmpl)
		o := w.opts()
		var calls []string
		o.Exec = func(_ context.Context, cmd string) (string, error) {
			calls = append(calls, cmd)
			return "out", nil
		}
		r, _ := Load(o)
		e, err := r.Expand(context.Background(), "c", args)
		if len(calls) > DefaultMaxExecs {
			t.Fatalf("%d shell commands", len(calls))
		}
		if err == nil {
			if len(e.Prompt) > DefaultMaxPrompt {
				t.Fatalf("prompt of %d bytes", len(e.Prompt))
			}
			if strings.ContainsRune(e.Prompt, 0) {
				t.Fatal("NUL in the prompt")
			}
		}

		// Injection through arguments: a marker in the arguments may appear in a
		// command only inside quotes.
		calls = nil
		const marker = "!`INJECTED`"
		_, _ = r.Expand(context.Background(), "c", marker)
		for _, cmd := range calls {
			if strings.Count(cmd, "INJECTED") != strings.Count(cmd, "'"+marker+"'") && !strings.Contains(tmpl, "INJECTED") {
				t.Fatalf("an argument reached a shell command unquoted: %q (template %q)", cmd, tmpl)
			}
		}
	})
}
