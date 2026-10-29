package mdfile

import (
	"os/exec"
	"strings"
	"testing"
)

// A placeholder the template already quotes gets the argument escaped for those quotes, so the argument stays one literal
// string whatever it holds; where no spelling is known to stay literal the substitution fails.
func TestShellPlaceholdersFollowTheTemplatesQuotes(t *testing.T) {
	const evil = `x' ; touch pwned ; echo '"$(id)"` + "`id`"
	for _, tc := range []struct{ tmpl, args, want string }{
		{"echo $1", "a;b", "echo 'a;b'"},
		{"echo '$1'", `"it's"`, `echo 'it'\''s'`},
		{`echo "$1"`, `'a"b$c'`, `echo "a\"b\$c"`},
		{`git log --grep="$ARGUMENTS"`, `fix bug`, `git log --grep="fix bug"`},
		{`echo "$(printf %s $1)"`, "a b", `echo "$(printf %s 'a')"`},
		{"echo $$1 $1", "v", "echo $1 'v'"},
		{"echo '$2'", "v", "echo ''"},
		{`echo "$(printf %s ignore-case)" $1`, "v", `echo "$(printf %s ignore-case)" 'v'`},
		{"echo a # it's \"\necho \"$1\"", "v", "echo a # it's \"\necho \"v\""},
		{"echo a#'b' $1", "v", "echo a#'b' 'v'"},
		{`echo $# "$1"`, "v", `echo $# "v"`},
		{"echo x$(printf foo)#\"x\n$1\"", "v", "echo x$(printf foo)#\"x\nv\""},
	} {
		got, _, err := SubstituteMax(tc.tmpl, tc.args, ArgsShell, 0)
		if err != nil || got != tc.want {
			t.Errorf("%q with %q = %q, %v; want %q", tc.tmpl, tc.args, got, err, tc.want)
		}
	}
	// a case pattern's ")" inside $( ) does not end the substitution: no placeholder after a case there is spelled for a guess
	for _, tmpl := range []string{"echo `echo $1`", "echo $'$1'", `echo \$1`, "echo \"`$1`\"", `echo "$(case x in x) echo $1;; esac)"`, `echo "$(case x in x) :;; esac) $1"`,
		`echo "$( : ${v:-)}; case x in x) printf '%s' "$1";; esac)"`, "echo \"$( :; \\\ncase x in x) printf %s $1;; esac)\"", "echo hi # $1",
		"echo \"$( : # note\ncase x in x) printf %s $1;; esac)\""} {
		if got, _, err := SubstituteMax(tmpl, "v", ArgsShell, 0); err == nil {
			t.Errorf("%q: substituted as %q; want an error", tmpl, got)
		}
	}
	// What a POSIX shell makes of it: the argument comes out as the one literal string it was.
	sh, err := exec.LookPath("sh")
	if err != nil {
		t.Skip("no sh")
	}
	for _, tmpl := range []string{"printf %s $1", "printf %s '$1'", `printf %s "$1"`, `printf %s "pre-$1-post"`, `printf %s x$(:)#"$1"`} {
		cmd, _, err := SubstituteMax(tmpl, `"`+strings.ReplaceAll(evil, `"`, `\"`)+`"`, ArgsShell, 0)
		if err != nil {
			t.Fatalf("%q: %v", tmpl, err)
		}
		out, err := exec.Command(sh, "-c", cmd).Output()
		want := evil
		if strings.Contains(tmpl, "pre-") {
			want = "pre-" + evil + "-post"
		}
		if strings.Contains(tmpl, "#") { // the # after $( ) is part of the word, not a comment
			want = "x#" + evil
		}
		if err != nil || string(out) != want {
			t.Errorf("%q ran as %q: printed %q (%v), want %q", tmpl, cmd, out, err, want)
		}
	}
}
