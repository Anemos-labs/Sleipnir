package shellparse

import (
	"context"
	"os/exec"
	"sort"
	"strings"
	"testing"
	"time"
)

// The parser exists so that nothing bash would run goes unseen. This test checks
// that against bash itself: each snippet is run with mk1..mk9 defined as
// functions that print their own name, and every marker that actually ran must
// also appear as a command in Parse's flattened list. (Parse may list more than
// runs - branches not taken, loop bodies - which is the safe direction.)
var differentialSnippets = []string{
	// plain structure
	`mk1 a; mk2 b`, `mk1 a && mk2 b`, `true || mk1; mk2`, `mk1 | mk2`, `mk1 |& mk2`, "mk1\nmk2\nmk3", `mk1 a & wait; mk2`,
	`(mk1; mk2)`, `{ mk1; mk2; }`, `( (mk1) )`, `{ { mk1; }; }`, `(mk1) && (mk2)`, `! mk1; mk2`, `mk1 ; mk2 ;`,
	// quoting and escapes that spell command names
	`"mk1" a`, `'mk1' a`, `m\k1 a`, `mk""1 a`, `$'mk1' a`, `$'\x6dk1' a`, `$'m\153\061' a`, `"m"'k'1`, "mk1 \\\n a", "m\\\nk1",
	// env prefixes and wrappers
	`A=1 mk1`, `A=1 B=2 mk1 x`, `A="x y" mk1`, `env mk1 a`, `env A=1 mk1 a`, `env -i mk1`, `env -u X mk1`, `command mk1`, `builtin echo; mk1`,
	`nohup mk1 a`, `time mk1`, `time -p mk1`, `exec mk1`, `A=1 env B=2 command mk1`, `nice mk1`, `timeout 5 mk1`, `stdbuf -oL mk1`,
	// substitutions
	`echo $(mk1)`, "echo `mk1`", `echo "$(mk1)"`, "echo \"`mk1`\"", `echo $(echo $(mk1))`, `echo "$(echo "$(mk1)")"`,
	`echo ${x:-$(mk1)}`, `echo "${x:-$(mk1)}"`, `echo ${x:-"$(mk1)"}`, `echo $((1+$(mk1 >/dev/null; echo 2)))`, `x=$(mk1); echo $x`,
	`A=$(mk1) mk2`, `echo a$(mk1)b`, `echo $(mk1; mk2)`, `echo $(mk1 | mk2)`, `echo $( (mk1) )`, `echo $(echo a; mk1)`,
	"echo `echo \\`mk1\\``", "echo `echo a; mk1`", `echo $(( $(mk1 >/dev/null; echo 1) + 1 ))`, `echo "a $(mk1) b $(mk2)"`,
	`mk1 $(mk2) $(mk3)`, `$(echo mk1) a`, "`echo mk1` a",
	`diff <(mk1) <(mk2) >/dev/null`, `cat <(mk1) >/dev/null`, `mk1 > >(mk2)`, `tee >(mk1) </dev/null >/dev/null`,
	// redirections carrying commands
	`mk1 > /dev/null`, `mk1 2>&1`, `mk1 >&2`, `mk1 &> /dev/null`, `mk1 < /dev/null`, `mk1 <<< "x"`, `mk1 <<< "$(mk2)"`, `mk1 > $(echo /dev/null)`,
	`> /dev/null mk1`, `mk1 3>&1`, `mk1 >| /dev/null`, `mk1 <> /dev/null`,
	// heredocs
	"cat <<EOF >/dev/null\n$(mk1)\nEOF", "cat <<EOF >/dev/null\n`mk1`\nEOF", "cat <<'EOF' >/dev/null\n$(mk1)\nEOF\nmk2",
	"cat <<EOF | mk1\nbody\nEOF", "cat <<-EOF >/dev/null\n\t$(mk1)\n\tEOF", "mk1 <<EOF\nbody\nEOF\nmk2", "cat <<A <<B >/dev/null\n$(mk1)\nA\n$(mk2)\nB",
	"bash <<EOF\nmk1\nEOF", "bash <<< 'mk1'", "bash -s <<EOF\nmk1\nmk2\nEOF", "cat <<EOF >/dev/null\n${x:-$(mk1)}\nEOF",
	// control flow
	`for i in 1 2; do mk1 $i; done`, `for i in $(mk1); do mk2; done`, `for i in a b; do mk1; done; mk2`, `while true; do mk1; break; done`,
	`until false; do mk1; break; done`, `if true; then mk1; else mk2; fi`, `if mk1; then mk2; fi`, `if true; then mk1; elif true; then mk2; fi`,
	`case x in x) mk1;; esac`, `case x in y) mk1;; x) mk2;; esac`, `case $(mk1) in *) mk2;; esac`, `case x in x|y) mk1 ;; esac`,
	`[[ -n $(mk1) ]] && mk2`, `[[ a == a && b == b ]] && mk1`, `[ -n "$(mk1)" ] && mk2`, `(( $(mk1 >/dev/null; echo 1) )) && mk2`,
	`select x in a; do mk1; break; done </dev/null`, `f() { mk1; }; f`, `function f { mk1; }; f`, `f() { mk1; mk2; }; f; f`, `f(){ mk1;};f`,
	`for ((i=0;i<1;i++)); do mk1; done`, `for i in {1..2}; do mk1; done`, `{ mk1; } > /dev/null`, `(mk1) > /dev/null`,
	`while read l; do mk1; done <<< "a"`, `for f in $(mk1); do :; done; mk2`, `if [ -n "$(mk1)" ]; then mk2; fi`,
	// eval and shells
	`eval mk1`, `eval "mk1; mk2"`, `eval 'mk1' 'mk2'`, `eval "eval mk1"`, `eval "echo \$(mk1)"`, `bash -c mk1`, `bash -c 'mk1; mk2'`, `bash -lc mk1`,
	`bash -c "bash -c mk1"`, `bash -c 'echo $(mk1)'`, `bash -c "mk1" x y`, `bash -o pipefail -c mk1`, `sudo() { "$@"; }; sudo mk1`, `sudo() { "$@"; }; sudo -u root mk1`,
	`sudo() { shift 2; "$@"; }; sudo -u x mk1`, `doas() { "$@"; }; doas mk1`, `f=mk1; $f`, `xargs() { "$@"; }; xargs mk1`,
	// brace and glob and tilde
	`mk{1,2}`, `mk{1..2} a`, `echo {a,b}; mk1`, `A={x,y} mk1`, `mk1 {a,$(mk2)}`, `echo {mk1,x}$(mk2)`,
	// odd spacing and comments
	"mk1 #comment\nmk2", "mk1;#c\nmk2", "  mk1  ;  mk2  ", "mk1 # $(mk2)\nmk3", "mk1&&#c\nmk2", "mk1 ||\n mk2 &&\n mk3",
	// arithmetic and parameter forms containing substitutions
	`echo $((1+1)); mk1`, `echo ${#x}; mk1`, `echo ${x:=$(mk1)}`, `echo ${x/a/$(mk1)}`, `echo ${x:-${y:-$(mk1)}}`, `echo $[1+1]; mk1`,
	`x=(a b); mk1`, `x=($(mk1)); mk2`, `declare -a y=($(mk1)); mk2`, `local_ok=$(mk1)`, `export A=$(mk1); mk2`, `readonly B=$(mk1)`,
	// trap and friends run later or on exit, but they do run
	`trap 'mk1' EXIT; mk2`, `trap "mk1" DEBUG; mk2; trap - DEBUG`, `alias zz=mk1; shopt -s expand_aliases; eval zz`,
	// time is a keyword that may prefix compound commands
	`time { mk1; }`, `time (mk1)`, `time -p { mk1; mk2; }`, `time [[ -n x ]] && mk1`, `time ! mk1; mk2`, `time mk1`, `time -p mk1 a`, `coproc mk1`,
	// misc
	`mk1; exit 0; mk2`, `mk1 && exit 0 || mk2`, `set -e; mk1; mk2`, `mk1 a b c d e f g`, `mk1 "" ''`, `mk1 -- -x`, `command -p mk1`, `mk1 & mk2 & wait`,
}

func TestDifferentialAgainstBash(t *testing.T) {
	if testing.Short() {
		t.Skip("short mode")
	}
	bash, err := exec.LookPath("bash")
	if err != nil {
		t.Skip("bash not installed")
	}
	dir := t.TempDir()
	prelude := "for n in 1 2 3 4 5 6 7 8 9; do eval \"mk$n() { echo EXEC:mk$n; }; export -f mk$n\"; done\n"
	missed := 0
	for _, sn := range differentialSnippets {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		cmd := exec.CommandContext(ctx, bash, "--noprofile", "--norc", "-c", prelude+sn)
		cmd.Dir = dir
		cmd.Env = []string{"PATH=/usr/bin:/bin", "HOME=" + dir}
		out, _ := cmd.Output() // a snippet may fail after running its markers; that is fine
		cancel()
		ran := map[string]bool{}
		for _, tok := range strings.Fields(string(out)) { // echo joins the markers of a substitution
			if n, ok := strings.CutPrefix(tok, "EXEC:"); ok {
				ran[n] = true
			}
		}
		an := Parse(sn)
		seen := map[string]bool{}
		dynamicName := false
		for _, c := range an.Commands {
			dynamicName = dynamicName || strings.ContainsAny(c.Program, "$`") || c.Program == "alias"
			for _, w := range append([]string{c.Program, c.Effective()}, c.Args...) {
				seen[w] = true
			}
		}
		// A marker may be spelled by brace expansion, quoting, substitution or a
		// function argument; Parse reports its final word, which is enough unless
		// the name is only known at run time (then Parse must have said so).
		var lost []string
		for n := range ran {
			if !seen[n] {
				lost = append(lost, n)
			}
		}
		sort.Strings(lost)
		// A command name held in a variable, or an alias defined on the same line,
		// is only known at run time. Parse lists the name as written ($f, alias),
		// which callers treat as unanalysable.
		if len(lost) > 0 && an.Parsed && !an.HasCommandSubstitution && !dynamicName {
			missed++
			t.Errorf("bash ran %v but Parse (parsed=%v) lists %d commands without them:\n  %q", lost, an.Parsed, len(an.Commands), sn)
		} else if len(lost) > 0 {
			t.Logf("bash ran %v hidden in a construct Parse flags as opaque (subst=%v, parsed=%v): %q", lost, an.HasCommandSubstitution, an.Parsed, sn)
		}
	}
	if missed > 0 {
		t.Logf("%d snippets ran commands the parser did not see", missed)
	}
}

// TestDifferentialGenerated composes command forms with syntactic contexts (a
// substitution inside a heredoc inside a function, a wrapper inside a bash -c
// string, ...) and checks each composition the same way.
func TestDifferentialGenerated(t *testing.T) {
	if testing.Short() {
		t.Skip("short mode")
	}
	bash, err := exec.LookPath("bash")
	if err != nil {
		t.Skip("bash not installed")
	}
	forms := []string{
		`mk1`, `mk1 a b`, `A=1 mk1`, `env mk1`, `command mk1`, `nohup mk1`, `"mk1"`, `m\k1`, `$'mk1'`, `mk1 >/dev/null`,
		`mk1 2>&1`, `mk1 <<< x`, `! mk1`, `time mk1`, `{ mk1; }`, `(mk1)`, `if true; then mk1; fi`, `for i in a; do mk1; done`,
		`while true; do mk1; break; done`, `case x in x) mk1;; esac`, `mk1 | cat`, `env A=1 nohup mk1`, `mk{1,1}`, `[[ -n x ]] && mk1`,
	}
	single := func(s string) string { return strings.ReplaceAll(s, "'", `'\''`) }
	contexts := []func(string) string{
		func(s string) string { return s },
		func(s string) string { return "true && " + s },
		func(s string) string { return s + " || true" },
		func(s string) string { return "true; " + s },
		func(s string) string { return "{ " + s + "; }" },
		func(s string) string { return "( " + s + " )" },
		func(s string) string { return "echo $( " + s + " )" },
		func(s string) string { return `echo "$( ` + s + ` )"` },
		func(s string) string { return "echo `" + strings.ReplaceAll(s, "`", "\\`") + "`" },
		func(s string) string { return "x=$( " + s + " )" },
		func(s string) string { return "cat <(" + s + ") >/dev/null" },
		func(s string) string { return "bash -c '" + single(s) + "'" },
		func(s string) string { return "eval '" + single(s) + "'" },
		func(s string) string { return "eval \"$(echo '" + single(s) + "')\"" },
		func(s string) string { return s + " | cat" },
		func(s string) string { return "cat </dev/null | " + s },
		func(s string) string { return "for j in 1; do " + s + "; done" },
		func(s string) string { return "if true; then " + s + "; fi" },
		func(s string) string { return "f() { " + s + "; }; f" },
		func(s string) string { return "while true; do " + s + "; break; done" },
		func(s string) string { return s + " &\nwait" },
		func(s string) string { return "\n" + s + "\n" },
		func(s string) string { return "cat <<EOF >/dev/null\n$( " + s + " )\nEOF" },
		func(s string) string { return "mk2 $( " + s + " )" },
		func(s string) string { return "echo ${x:-$( " + s + " )}" },
		func(s string) string { return "echo \"${x:-$( " + s + " )}\"" },
		func(s string) string { return "echo $(( $( " + s + " >/dev/null; echo 1 ) + 1 ))" },
		func(s string) string { return "time { " + s + "; }" },
		func(s string) string { return "! { " + s + "; }" },
		func(s string) string { return "trap '" + single(s) + "' EXIT" },
		func(s string) string { return "bash <<EOF\n" + s + "\nEOF" },
		func(s string) string { return "bash <<< '" + single(s) + "'" },
	}
	dir := t.TempDir()
	prelude := "for n in 1 2 3 4 5 6 7 8 9; do eval \"mk$n() { echo EXEC:mk$n; }; export -f mk$n\"; done\n"
	total, bad := 0, 0
	for _, f := range forms {
		for ci, ctx := range contexts {
			sn := ctx(f)
			total++
			cctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			cmd := exec.CommandContext(cctx, bash, "--noprofile", "--norc", "-c", prelude+sn)
			cmd.Dir = dir
			cmd.Env = []string{"PATH=/usr/bin:/bin", "HOME=" + dir}
			out, _ := cmd.Output()
			cancel()
			if !strings.Contains(string(out), "EXEC:mk1") {
				continue // the composition did not run the marker; nothing to compare
			}
			an := Parse(sn)
			found := false
			for _, c := range an.Commands {
				for _, w := range append([]string{c.Program, c.Effective()}, c.Args...) {
					found = found || w == "mk1"
				}
			}
			if !found && an.Parsed && !an.HasCommandSubstitution || !found && ci == 0 {
				bad++
				t.Errorf("bash ran mk1 but Parse lists no such command (parsed=%v):\n  %q", an.Parsed, sn)
			}
		}
	}
	t.Logf("compared %d generated compositions", total)
	if bad > 0 {
		t.Fail()
	}
}
