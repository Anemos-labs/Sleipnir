package shellparse

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
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

// Word values matter as much as command names: path checks depend on exactly
// what bash would pass. Each snippet is a list of arguments in shell syntax;
// bash prints what it receives and Parse must report the same words.
var argSnippets = []string{
	`a b c`, `"a b" 'c d' e\ f`, `""`, `'' ""`, `"" x ''`, `a""b`, `a''b`, `""x''y`, `"a"'b'c`, `'a'"b"'c'`,
	`"a\"b"`, `"a\\b"`, `"a\$b"`, "\"a\\`b\"", `"a\b"`, `"a\nb"`, `'a\nb'`, `'a\b'`, `a\ b`, `a\\b`, `a\"b`, `a\'b`, `\a\b\c`, `\$HOME`, `\~`, `"\~"`,
	`$'a'`, `$'\x41'`, `$'\101'`, `$'\u00e9'`, `$'\U0001F600'`, `$'a\tb'`, `$'a\nb'`, `$'\''`, `$'\\'`, `$'\"'`, `$'\e[0m'`, `$'\cA'`, `$'\7'`, `$'\x2f\x65tc'`,
	`$'a\0b'`, `$'\xZ'`, `$'\u'`, `$'\q'`, `$'a'$'b'`, `$'a''b'`, `$'\n'x`, `x$'\n'`, `$"a b"`, `$'\141\142\143'`, `$'\x7e/x'`,
	"a\\\nb", "\"a\\\nb\"", "'a\\\nb'", "a \\\n b", `a\ `, `\ a`,
	`{a,b}`, `{1..3}`, `{a,b}{c,d}`, `x{,y}`, `{a}`, `{}`, `\{a,b\}`, `"{a,b}"`, `'{a,b}'`, `a{b,c}d`, `{01..03}`, `{a..c}`, `{3..1}`, `{1..10..3}`,
	`{-1..1}`, `{a,{b,c}}`, `{a,b}{1..2}`, `{,a}`, `{a,}`, `{,}`, `{a,b`, `a,b}`, `{a..}`, `{..a}`, `{1..a}`, `{a,b}"c"`, `"{a"',b}'`, `{a\,b,c}`, `{a,b\}c}`,
	`x{1..3}y`, `{1..3}{a,b}`, `{{a,b},c}`, `{a,b}{,c}`, `a={b,c}`, `--opt={a,b}`, `{a,b}=x`, `{"a b",c}`, `{a\ b,c}`, `{a,b}$'\x41'`, `$'{a,b}'`,
	`a#b`, `a\#b`, `'#'`, `"#"`, `#`, `a #b`, `-x -y --z=1`, `-- -x`, `=x`, `x=`, `x=y`, `%s`, `!x`, `a!b`, `*`, `?`, `[a]`, `~`, `~/x`, `$`, `$$`, `$1`, `${x}`,
	`é`, `日本語`, `a😀b`, `"é"`, `$'\xc3\xa9'`,
}

// bash4Only are snippets whose expansion bash 3.2 (what macOS ships) does not do:
// zero-padded and stepped brace sequences arrived in bash 4.0. Parse follows the
// current bash, so these are only compared against a bash that has them.
var bash4Only = map[string]bool{`{01..03}`: true, `{1..10..3}`: true}

// bashMajor is the major version of the bash at path, or 0 when it cannot be told.
func bashMajor(bash string) int {
	out, err := exec.Command(bash, "--noprofile", "--norc", "-c", "echo ${BASH_VERSINFO[0]}").Output()
	if err != nil {
		return 0
	}
	n, _ := strconv.Atoi(strings.TrimSpace(string(out)))
	return n
}

func TestArgsDifferentialAgainstBash(t *testing.T) {
	if testing.Short() {
		t.Skip("short mode")
	}
	bash, err := exec.LookPath("bash")
	if err != nil {
		t.Skip("bash not installed")
	}
	oldBash := bashMajor(bash) < 4
	dir := t.TempDir()
	checked := 0
	for _, sn := range argSnippets {
		if oldBash && bash4Only[sn] {
			continue
		}
		// Globs, tildes and variables depend on the environment; Parse leaves them
		// textual by design, so only compare the deterministic ones.
		if strings.ContainsAny(strings.ReplaceAll(sn, `\$`, ""), "*?[$~") && !strings.Contains(sn, "$'") && !strings.Contains(sn, `$"`) {
			continue
		}
		if strings.Contains(sn, "$") && !strings.Contains(sn, "$'") && !strings.Contains(sn, `$"`) && !strings.Contains(sn, `\$`) {
			continue
		}
		script := "showargs() { printf '<%s>' \"$@\"; }; showargs " + sn
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		cmd := exec.CommandContext(ctx, bash, "--noprofile", "--norc", "-c", script)
		cmd.Dir = dir
		cmd.Env = []string{"PATH=/usr/bin:/bin", "HOME=" + dir, "LC_ALL=C.UTF-8"}
		out, err := cmd.Output()
		cancel()
		if err != nil {
			continue // a syntax error or NUL in this bash; nothing to compare
		}
		if strings.Contains(sn, `\u`) || strings.Contains(sn, `\U`) {
			if strings.Contains(string(out), `\u`) || strings.Contains(string(out), `\U`) {
				continue // no UTF-8 locale here: bash leaves \u escapes undecoded
			}
		}
		an := Parse("showargs " + sn)
		if len(an.Commands) == 0 {
			t.Errorf("no command parsed for %q", sn)
			continue
		}
		var b strings.Builder
		for _, a := range an.Commands[0].Args {
			b.WriteString("<" + a + ">")
		}
		if len(an.Commands[0].Args) == 0 {
			b.WriteString("<>") // printf with no arguments still prints its format once
		}
		checked++
		if b.String() != string(out) {
			t.Errorf("args of %q:\n  bash  %q\n  Parse %q (parsed=%v)", sn, string(out), b.String(), an.Parsed)
		}
	}
	if checked < 100 {
		t.Errorf("only %d snippets were compared", checked)
	}
	t.Logf("compared the words of %d snippets with bash", checked)
}

// Redirections write files; a write the parser misses is a permission hole. Run
// snippets in a scratch directory and require that every file bash created is
// the target of some write redirection Parse reported.
func TestRedirectsDifferentialAgainstBash(t *testing.T) {
	if testing.Short() {
		t.Skip("short mode")
	}
	bash, err := exec.LookPath("bash")
	if err != nil {
		t.Skip("bash not installed")
	}
	forms := []string{
		`echo x > FILE`, `echo x >FILE`, `echo x>FILE`, `echo x >> FILE`, `echo x >| FILE`, `echo x &> FILE`, `echo x &>> FILE`, `echo x 2> FILE`, `echo x 1> FILE`,
		`echo x 3> FILE`, `echo x 12> FILE`, `echo x 2>> FILE`, `> FILE echo x`, `>FILE`, `: > FILE`, `echo x > FILE 2>&1`, `echo x 2>&1 > FILE`, `echo x <> FILE`,
		`echo x > "FILE"`, `echo x > 'FILE'`, `echo x > F"IL"E`, `echo x > FIL\E`, `echo x > $'FILE'`, `cat <<EOF > FILE` + "\nbody\nEOF", `cat > FILE <<EOF` + "\nbody\nEOF",
		`cat <<< body > FILE`, `exec 5> FILE`, `exec > FILE`, `echo x | tee FILE`, `echo x | cat > FILE`, `echo x > {FILE,FILE}`, `echo x > FILE; echo y > FILE`,
		`{ echo x; } > FILE`, `( echo x ) > FILE`, `for i in 1; do echo x; done > FILE`, `while false; do :; done > FILE`, `if true; then echo x; fi > FILE`,
		`echo x > FILE &`, `echo x | cat | cat > FILE`, `echo $(echo x > FILE)`, "echo `echo x > FILE`", `cat <(echo x > FILE)`, `bash -c 'echo x > FILE'`,
		`eval 'echo x > FILE'`, `eval "echo x > FILE"`, `echo x > FILE || true`, `true && echo x > FILE`, `f() { echo x > FILE; }; f`, `echo x > "F ILE"`,
		`FOO=1 echo x > FILE`, `env echo x > FILE`, `nohup echo x > FILE`, `time echo x > FILE`, `echo x >FILE&&:`, `echo x 2>FILE 1>&2`,
		`echo x > FILE >> FILE2`, `echo x > FILE 2> FILE2`, `echo x > FILE1 > FILE`, `[[ -n x ]] > FILE`, `case x in x) echo y > FILE;; esac`,
		`trap 'echo x > FILE' EXIT`, `echo x > FILE # comment`, "echo x \\\n > FILE", `echo x > ./FILE`, `echo x > sub/FILE`,
	}
	names := []string{"out", "a b", "f'x", "-dash", "ünï", "sub/deep"}
	total := 0
	for _, form := range forms {
		for ni, name := range names {
			if ni > 2 && (len(form)+ni)%3 != 0 {
				continue // sample the odd names
			}
			if strings.Contains(form, `$'FILE'`) || strings.Contains(form, `F"IL"E`) || strings.Contains(form, `FIL\E`) {
				if name != "out" {
					continue // the spelling tricks only make sense for the plain name
				}
			}
			snippet := spell(form, name)
			dir := t.TempDir()
			if err := os.MkdirAll(filepath.Join(dir, "sub", "deep"), 0o755); err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			cmd := exec.CommandContext(ctx, bash, "--noprofile", "--norc", "-c", snippet)
			cmd.Dir = dir
			cmd.Env = []string{"PATH=/usr/bin:/bin", "HOME=" + dir, "LC_ALL=C.UTF-8"}
			_ = cmd.Run()
			cancel()
			created := map[string]bool{}
			_ = filepath.Walk(dir, func(p string, fi os.FileInfo, err error) error {
				if err == nil && fi.Mode().IsRegular() {
					rel, _ := filepath.Rel(dir, p)
					created[rel] = true
				}
				return nil
			})
			if len(created) == 0 {
				continue
			}
			an := Parse(snippet)
			targets := map[string]bool{}
			for _, c := range an.Commands {
				for _, r := range c.Redirects {
					op := strings.TrimLeft(r.Op, "0123456789")
					switch op {
					case ">", ">>", ">|", "&>", "&>>", "<>":
						targets[filepath.Clean(r.Target)] = true
					case ">&":
						if !allDigitsOrDash(r.Target) {
							targets[filepath.Clean(r.Target)] = true
						}
					}
				}
				// tee and cat > are separate commands; their file operands are seen as arguments
				for _, a := range c.Args {
					targets[filepath.Clean(a)] = true
				}
			}
			total++
			for f := range created {
				if !targets[f] {
					t.Errorf("bash created %q but Parse reports no write to it (targets %v, parsed=%v):\n  %q", f, keys(targets), an.Parsed, snippet)
				}
			}
		}
	}
	t.Logf("compared %d redirect snippets that created files", total)
	if total < 100 {
		t.Errorf("only %d snippets produced files; the test is not testing much", total)
	}
}

// spell substitutes a file name into a redirect form. Forms that already put
// FILE inside their own quoting get the raw name; the others get it shell-quoted
// when it needs that.
func spell(form, name string) string {
	form = strings.ReplaceAll(strings.ReplaceAll(form, "FILE2", "second"), "FILE1", "first")
	for _, own := range []string{`"FILE"`, `'FILE'`, `$'FILE'`, `F"IL"E`, `FIL\E`, `"F ILE"`} {
		if strings.Contains(form, own) {
			return strings.ReplaceAll(form, "FILE", name)
		}
	}
	if strings.ContainsAny(name, " '") {
		name = "'" + strings.ReplaceAll(name, "'", `'\''`) + "'"
	}
	return strings.ReplaceAll(form, "FILE", name)
}

func allDigitsOrDash(s string) bool {
	if s == "-" {
		return true
	}
	if s == "" {
		return false
	}
	for _, c := range s {
		if c < '0' || c > '9' {
			return false
		}
	}
	return true
}

func keys(m map[string]bool) []string {
	var out []string
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
