package perm

import (
	"reflect"
	"testing"
)

// A sed that edits is an unlisted command, but its script is no file: "s/a, 5,/b, 6,/" split at its commas named the root,
// and the credentials protection refused an edit of a file in the workspace even in bypass mode.
func TestASedScriptIsNotAFileButTheFilesItNamesAre(t *testing.T) {
	f := newFixture(t)
	runCases(t, f, []tc{
		{name: "commas in s", mode: ModeBypass, req: bash(`sed -i 's/a, 5,/b, 6,/' main.go`), want: "allow"},
		{name: "two s commands", mode: ModeBypass, req: bash(`sed -i 's/pivot", 5,/pivot", -10,/; s/mid", -4,/mid", 0,/' src/a.go`), want: "allow"},
		{name: "colons in s", mode: ModeBypass, req: bash(`sed -i -e 's/a:b/c:d/' main.go`), want: "allow"},
		{name: "a w flag naming credentials", mode: ModeBypass, req: bash(`sed -i 's/a/b/w {home}/.ssh/x' main.go`), want: "deny"},
		{name: "an r command naming credentials", mode: ModeBypass, req: bash(`sed -n '1r {home}/.ssh/id_rsa' main.go`), want: "deny"},
		{name: "a w command into a system directory", mode: ModeBypass, req: bash(`sed -n '1w /etc/x' main.go`), want: "deny"},
		{name: "a label before a w command naming credentials", mode: ModeBypass, req: bash(`sed -n ':a;w {home}/.ssh/authorized_keys' main.go`), want: "deny"},
		{name: "BSD -i '' before a w flag naming credentials", mode: ModeBypass, req: bash(`sed -i '' 's/a/b/w {home}/.ssh/x' main.go`), want: "deny"},
		{name: "BSD -i '' edit", mode: ModeBypass, req: bash(`sed -i '' 's/a, 5,/b, 6,/' main.go`), want: "allow"},
		{name: "editing credentials", mode: ModeBypass, req: bash(`sed -i s/a/b/ {home}/.ssh/config`), want: "deny"},
		{name: "the default mode still asks", req: bash(`sed -i 's/a, 5,/b, 6,/' main.go`), want: "ask"},
	})
}

func TestSedScriptFiles(t *testing.T) {
	for _, c := range []struct {
		script        string
		reads, writes []string
		ok            bool
	}{
		{"s/a, 5,/b, 6,/", nil, nil, true},
		{"s|a/b|c/d|g", nil, nil, true},
		{`s/a\/b/c/2p;y/ab/cd/`, nil, nil, true},
		{"1,/x/{s/a/b/;p}", nil, nil, true},
		{"/x/r in.txt\n$a end", []string{"in.txt"}, nil, true},
		{"s/a/b/w out.txt", nil, []string{"out.txt"}, true},
		{"1W o.txt", nil, []string{"o.txt"}, true},
		{":top\ns/a/b/\nt top", nil, nil, true},
		{":a;s/a/b/w out.txt", nil, []string{"out.txt"}, true},
		{":a;w out.txt", nil, []string{"out.txt"}, true},
		{"b end;s/a/b/w o1\nt x;W o2\nT y;r in\n:end", []string{"in"}, []string{"o1", "o2"}, true},
		{"1w o1;p", nil, []string{"o1;p"}, true}, // a file name runs to the end of the line
		{"/x/{b skip};p", nil, nil, true},
		{"b a w out.txt", nil, nil, false},
		{"1e date", nil, nil, false},
		{"s/a/b/e", nil, nil, false},
		{"s/a/b", nil, nil, false},
		{"1v", nil, nil, false},
	} {
		r, w, ok := sedScriptFiles(c.script)
		if ok != c.ok || ok && (!reflect.DeepEqual(r, c.reads) || !reflect.DeepEqual(w, c.writes)) {
			t.Errorf("%q: reads %q writes %q ok %v, want %q %q %v", c.script, r, w, ok, c.reads, c.writes, c.ok)
		}
	}
}
