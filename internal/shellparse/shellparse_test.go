package shellparse

import (
	"reflect"
	"slices"
	"strconv"
	"strings"
	"testing"
)

// cmd is the expected shape of one simple command. A nil args means "do not
// check the arguments".
type cmd struct {
	prog string
	args []string
}

func c(prog string, args ...string) cmd {
	if args == nil {
		args = []string{}
	}
	return cmd{prog: prog, args: args}
}

// anyArgs matches a program whatever its arguments.
func anyArgs(prog string) cmd { return cmd{prog: prog} }

type flag uint16

const (
	fSub flag = 1 << iota
	fCS
	fPS
	fPipe
	fBG
	fHD
	fBad // Parsed == false
)

func (f flag) has(x flag) bool { return f&x != 0 }

type parseCase struct {
	in    string
	cmds  []cmd
	flags flag
}

var parseCases = []parseCase{
	// --- plain commands and separators ---
	{in: ``, cmds: nil},
	{in: `   `, cmds: nil},
	{in: "\n\n", cmds: nil},
	{in: `# only a comment`, cmds: nil},
	{in: `ls`, cmds: []cmd{c("ls")}},
	{in: `ls -la /tmp`, cmds: []cmd{c("ls", "-la", "/tmp")}},
	{in: "  \t ls \t -l  ", cmds: []cmd{c("ls", "-l")}},
	{in: `ls # trailing comment`, cmds: []cmd{c("ls")}},
	{in: `echo a#b`, cmds: []cmd{c("echo", "a#b")}},
	{in: "echo a\n# comment\necho b", cmds: []cmd{c("echo", "a"), c("echo", "b")}},
	{in: `a; b; c`, cmds: []cmd{c("a"), c("b"), c("c")}},
	{in: `a;b`, cmds: []cmd{c("a"), c("b")}},
	{in: `a && b || c`, cmds: []cmd{c("a"), c("b"), c("c")}},
	{in: `a | b | c`, cmds: []cmd{c("a"), c("b"), c("c")}},
	{in: `a |& b`, cmds: []cmd{c("a"), c("b")}},
	{in: "a\nb\nc", cmds: []cmd{c("a"), c("b"), c("c")}},
	{in: "a &&\n  b", cmds: []cmd{c("a"), c("b")}},
	{in: "a |\n b", cmds: []cmd{c("a"), c("b")}},
	{in: "a ||\n\n b", cmds: []cmd{c("a"), c("b")}},
	{in: "ls \\\n -la", cmds: []cmd{c("ls", "-la")}},
	{in: "ec\\\nho hi", cmds: []cmd{c("echo", "hi")}},
	{in: `a &`, cmds: []cmd{c("a")}, flags: fBG},
	{in: `a & b`, cmds: []cmd{c("a"), c("b")}, flags: fBG},
	{in: `a &b`, cmds: []cmd{c("a"), c("b")}, flags: fBG},
	{in: `a && b &`, cmds: []cmd{c("a"), c("b")}, flags: fBG},
	{in: `sleep 1 & wait`, cmds: []cmd{c("sleep", "1"), c("wait")}, flags: fBG},
	{in: `! grep x f`, cmds: []cmd{c("grep", "x", "f")}},
	{in: `a;`, cmds: []cmd{c("a")}},
	{in: `a ;`, cmds: []cmd{c("a")}},
	{in: "a\n", cmds: []cmd{c("a")}},

	// --- quoting ---
	{in: `echo "a b"`, cmds: []cmd{c("echo", "a b")}},
	{in: `echo 'a b'`, cmds: []cmd{c("echo", "a b")}},
	{in: `echo "a 'b' c"`, cmds: []cmd{c("echo", "a 'b' c")}},
	{in: `echo 'a "b" c'`, cmds: []cmd{c("echo", `a "b" c`)}},
	{in: `echo a\ b`, cmds: []cmd{c("echo", "a b")}},
	{in: `echo "a\"b"`, cmds: []cmd{c("echo", `a"b`)}},
	{in: `echo 'a\b'`, cmds: []cmd{c("echo", `a\b`)}},
	{in: `echo "a\\b"`, cmds: []cmd{c("echo", `a\b`)}},
	{in: `echo "a\b"`, cmds: []cmd{c("echo", `a\b`)}},
	{in: `echo "a\$b"`, cmds: []cmd{c("echo", `a$b`)}},
	{in: `echo $'a\tb'`, cmds: []cmd{c("echo", "a\tb")}},
	{in: `echo $'\x41\101\u0041'`, cmds: []cmd{c("echo", "AAA")}},
	{in: `echo $'a\0b'`, cmds: []cmd{c("echo", "a")}},
	{in: `echo $'it\'s'`, cmds: []cmd{c("echo", "it's")}},
	{in: `echo ""`, cmds: []cmd{c("echo", "")}},
	{in: `echo '' ""`, cmds: []cmd{c("echo", "", "")}},
	{in: `echo a"b"'c'd`, cmds: []cmd{c("echo", "abcd")}},
	{in: `echo "$HOME"`, cmds: []cmd{c("echo", "$HOME")}},
	{in: `echo '$HOME'`, cmds: []cmd{c("echo", "$HOME")}},
	{in: `echo ${HOME}/x`, cmds: []cmd{c("echo", "${HOME}/x")}},
	{in: `echo ~ ~root ~/x`, cmds: []cmd{c("echo", "~", "~root", "~/x")}},
	{in: `echo "a
b"`, cmds: []cmd{c("echo", "a\nb")}},
	{in: `echo "a\
b"`, cmds: []cmd{c("echo", "ab")}},
	{in: `echo $"hello"`, cmds: []cmd{c("echo", "hello")}},
	{in: `echo "unterminated`, cmds: []cmd{c("echo", "unterminated")}, flags: fBad},
	{in: `echo 'unterminated`, cmds: []cmd{c("echo", "unterminated")}, flags: fBad},
	{in: `echo $'unterminated`, cmds: []cmd{anyArgs("echo")}, flags: fBad},
	{in: `echo a\`, cmds: []cmd{c("echo", `a\`)}},

	// --- quoting tricks that spell a program or path ---
	{in: `r\m -rf /`, cmds: []cmd{c("rm", "-rf", "/")}},
	{in: `'rm' -rf /`, cmds: []cmd{c("rm", "-rf", "/")}},
	{in: `"rm" -rf /`, cmds: []cmd{c("rm", "-rf", "/")}},
	{in: `r'm' -rf /`, cmds: []cmd{c("rm", "-rf", "/")}},
	{in: `r""m -rf /`, cmds: []cmd{c("rm", "-rf", "/")}},
	{in: `$'r\x6d' -rf /`, cmds: []cmd{c("rm", "-rf", "/")}},
	{in: `$'\162\155' -rf /`, cmds: []cmd{c("rm", "-rf", "/")}},
	{in: `c'a't ~/.s""sh/id_rs\a`, cmds: []cmd{c("cat", "~/.ssh/id_rsa")}},
	{in: `cat $'\x7e/.ssh/id_rsa'`, cmds: []cmd{c("cat", "~/.ssh/id_rsa")}},
	{in: `cat ~/.{ssh,aws}/credentials`, cmds: []cmd{c("cat", "~/.ssh/credentials", "~/.aws/credentials")}},
	{in: `cat "~/.{ssh,aws}/credentials"`, cmds: []cmd{c("cat", "~/.{ssh,aws}/credentials")}},
	{in: `cat /e"t"c/pass\wd`, cmds: []cmd{c("cat", "/etc/passwd")}},
	{in: `cat $'/etc/pass\x77d'`, cmds: []cmd{c("cat", "/etc/passwd")}},
	{in: `cat "$HOME"/.ssh/id_rsa`, cmds: []cmd{c("cat", "$HOME/.ssh/id_rsa")}},
	{in: `cat ${HOME}/.ssh/id_rsa`, cmds: []cmd{c("cat", "${HOME}/.ssh/id_rsa")}},
	{in: `cat ~/.ssh/../.ssh/id_rsa`, cmds: []cmd{c("cat", "~/.ssh/../.ssh/id_rsa")}},

	// --- env assignments ---
	{in: `FOO=1 cmd`, cmds: []cmd{c("cmd")}},
	{in: `FOO=1 BAR=2 cmd a`, cmds: []cmd{c("cmd", "a")}},
	{in: `FOO="a b" cmd`, cmds: []cmd{c("cmd")}},
	{in: `FOO='a b'c cmd`, cmds: []cmd{c("cmd")}},
	{in: `FOO+=x cmd`, cmds: []cmd{c("cmd")}},
	{in: `FOO=`, cmds: []cmd{c("")}},
	{in: `FOO=bar`, cmds: []cmd{c("")}},
	{in: `FOO=$(x) cmd`, cmds: []cmd{c("cmd"), c("x")}, flags: fCS},
	{in: `1FOO=x cmd`, cmds: []cmd{c("1FOO=x", "cmd")}},
	{in: `"FOO=1" cmd`, cmds: []cmd{c("FOO=1", "cmd")}},
	{in: `cmd FOO=1`, cmds: []cmd{c("cmd", "FOO=1")}},
	{in: `x=1; y=2 cmd`, cmds: []cmd{c(""), c("cmd")}},

	// --- wrappers ---
	{in: `env FOO=1 rm -rf /`, cmds: []cmd{c("rm", "-rf", "/")}},
	{in: `env -i FOO=1 rm x`, cmds: []cmd{c("rm", "x")}},
	{in: `env -u HOME rm x`, cmds: []cmd{c("rm", "x")}},
	{in: `env -- rm x`, cmds: []cmd{c("rm", "x")}},
	{in: `/usr/bin/env FOO=1 rm x`, cmds: []cmd{c("rm", "x")}},
	{in: `./env FOO=1 rm x`, cmds: []cmd{c("./env", "FOO=1", "rm", "x")}},
	{in: `/tmp/env FOO=1 rm x`, cmds: []cmd{c("/tmp/env", "FOO=1", "rm", "x")}},
	{in: `env`, cmds: []cmd{c("env")}},
	{in: `env FOO=1`, cmds: []cmd{c("env", "FOO=1")}},
	{in: `env -S 'a b'`, cmds: []cmd{anyArgs("env")}, flags: fBad},
	{in: `env -C /tmp ls`, cmds: []cmd{anyArgs("env")}, flags: fBad},
	{in: `env --bogus ls`, cmds: []cmd{anyArgs("env")}, flags: fBad},
	{in: `nohup rm x &`, cmds: []cmd{c("rm", "x")}, flags: fBG},
	{in: `nohup env A=1 time rm -f x`, cmds: []cmd{c("rm", "-f", "x")}},
	{in: `time ls`, cmds: []cmd{c("ls")}},
	{in: `time { a; b; }`, cmds: []cmd{c("a"), c("b")}},
	{in: `time -p { a; }`, cmds: []cmd{c("a")}},
	{in: `time ( a )`, cmds: []cmd{c("a")}, flags: fSub},
	{in: `time ! a`, cmds: []cmd{c("a")}},
	{in: `time [[ -f x ]] && b`, cmds: []cmd{c("[[", "-f", "x", "]]"), c("b")}},
	{in: `time -p ls`, cmds: []cmd{c("ls")}},
	{in: `command ls`, cmds: []cmd{c("ls")}},
	{in: `command -p ls`, cmds: []cmd{c("ls")}},
	{in: `command -v git`, cmds: []cmd{c("command", "-v", "git")}},
	{in: `builtin echo hi`, cmds: []cmd{c("echo", "hi")}},
	{in: `exec ls`, cmds: []cmd{c("ls")}},
	{in: `exec -a name ls`, cmds: []cmd{c("ls")}},
	{in: `exec > log`, cmds: []cmd{c("exec")}},
	{in: `nice -n 10 make`, cmds: []cmd{c("make")}},
	{in: `nice -10 make`, cmds: []cmd{c("make")}},
	{in: `ionice -c 3 make`, cmds: []cmd{c("make")}},
	{in: `timeout 5 sleep 10`, cmds: []cmd{c("sleep", "10")}},
	{in: `timeout -s KILL 5 sleep 10`, cmds: []cmd{c("sleep", "10")}},
	{in: `timeout --signal=KILL 5 sleep 10`, cmds: []cmd{c("sleep", "10")}},
	{in: `stdbuf -oL make`, cmds: []cmd{c("make")}},
	{in: `stdbuf -o L make`, cmds: []cmd{c("make")}},
	{in: `setsid -f make`, cmds: []cmd{c("make")}},
	{in: `nohup nice -n 5 timeout 9 env A=1 make all`, cmds: []cmd{c("make", "all")}},
	{in: `a=1 b=2 env c=3 prog`, cmds: []cmd{c("prog")}},

	// --- sudo and friends keep Program (see TestEffective) ---
	{in: `sudo rm -rf /`, cmds: []cmd{c("sudo", "rm", "-rf", "/")}},
	{in: `sudo -u root ls`, cmds: []cmd{c("sudo", "-u", "root", "ls")}},
	{in: `doas -u root ls`, cmds: []cmd{c("doas", "-u", "root", "ls")}},
	{in: `sudo bash -c 'rm x'`, cmds: []cmd{c("sudo", "bash", "-c", "rm x"), c("rm", "x")}},

	// --- redirections ---
	{in: `echo hi > out.txt`, cmds: []cmd{c("echo", "hi")}},
	{in: `echo hi >out.txt`, cmds: []cmd{c("echo", "hi")}},
	{in: `echo hi>out.txt`, cmds: []cmd{c("echo", "hi")}},
	{in: `echo hi >> out.txt`, cmds: []cmd{c("echo", "hi")}},
	{in: `cat < in`, cmds: []cmd{c("cat")}},
	{in: `cat <in >out 2>err`, cmds: []cmd{c("cat")}},
	{in: `cmd 2>&1`, cmds: []cmd{c("cmd")}},
	{in: `cmd >&2`, cmds: []cmd{c("cmd")}},
	{in: `cmd &>all`, cmds: []cmd{c("cmd")}},
	{in: `cmd &>>all`, cmds: []cmd{c("cmd")}},
	{in: `cmd >|f`, cmds: []cmd{c("cmd")}},
	{in: `cmd <>f`, cmds: []cmd{c("cmd")}},
	{in: `cmd 3>&-`, cmds: []cmd{c("cmd")}},
	{in: `cmd 10>f`, cmds: []cmd{c("cmd")}},
	{in: `cmd 2> /dev/null`, cmds: []cmd{c("cmd")}},
	{in: `> f cmd a`, cmds: []cmd{c("cmd", "a")}},
	{in: `cmd > f1 > f2`, cmds: []cmd{c("cmd")}},
	{in: `> file`, cmds: []cmd{c("")}},
	{in: `echo 2>file`, cmds: []cmd{c("echo")}},
	{in: `echo a2>file`, cmds: []cmd{c("echo", "a2")}},
	{in: `cmd <<< "here string"`, cmds: []cmd{c("cmd")}},
	{in: `echo > "quoted name"`, cmds: []cmd{c("echo")}},
	{in: `echo >`, cmds: []cmd{c("echo")}, flags: fBad},
	{in: `echo > ;`, cmds: []cmd{c("echo")}, flags: fBad},
	{in: `exec 3<file`, cmds: []cmd{c("exec")}},

	// --- heredocs ---
	{in: "cat <<EOF\nhello\nEOF", cmds: []cmd{c("cat")}, flags: fHD},
	{in: "cat <<EOF\nhello\nEOF\nls", cmds: []cmd{c("cat"), c("ls")}, flags: fHD},
	{in: "cat <<'EOF' > out\n$(not run)\nEOF\nls", cmds: []cmd{c("cat"), c("ls")}, flags: fHD},
	{in: "cat <<\"EOF\"\n$(not run)\nEOF", cmds: []cmd{c("cat")}, flags: fHD},
	{in: "cat <<\\EOF\n$(not run)\nEOF", cmds: []cmd{c("cat")}, flags: fHD},
	{in: "cat <<EOF\nhello $(whoami)\nEOF", cmds: []cmd{c("cat"), c("whoami")}, flags: fHD | fCS},
	{in: "cat <<EOF\nhello `whoami`\nEOF", cmds: []cmd{c("cat"), c("whoami")}, flags: fHD | fCS},
	{in: "cat <<EOF\nescaped \\$(whoami)\nEOF", cmds: []cmd{c("cat")}, flags: fHD},
	{in: "cat <<-EOF\n\tbody\n\tEOF\nls", cmds: []cmd{c("cat"), c("ls")}, flags: fHD},
	{in: "cat <<EOF | grep x\nbody\nEOF", cmds: []cmd{c("cat"), c("grep", "x")}, flags: fHD},
	{in: "cat <<A <<B\n1\nA\n2\nB\nls", cmds: []cmd{c("cat"), c("ls")}, flags: fHD},
	{in: "cat <<EOF\nunterminated", cmds: []cmd{c("cat")}, flags: fHD | fBad},
	{in: "cat <<", cmds: []cmd{c("cat")}, flags: fHD | fBad},
	{in: "bash <<EOF\nrm -rf /\nEOF", cmds: []cmd{c("bash"), c("rm", "-rf", "/")}, flags: fHD},
	{in: `bash <<< "rm -rf /"`, cmds: []cmd{c("bash"), c("rm", "-rf", "/")}},
	{in: "sh -s <<EOF\nls\nEOF", cmds: []cmd{c("sh", "-s"), c("ls")}, flags: fHD},
	{in: "cat <<EOF\nrm -rf /\nEOF", cmds: []cmd{c("cat")}, flags: fHD},

	// --- substitutions ---
	{in: `echo $(date)`, cmds: []cmd{c("echo", "$(date)"), c("date")}, flags: fCS},
	{in: "echo `date`", cmds: []cmd{c("echo", "`date`"), c("date")}, flags: fCS},
	{in: `echo "$(date)"`, cmds: []cmd{c("echo", "$(date)"), c("date")}, flags: fCS},
	{in: "echo \"`date`\"", cmds: []cmd{c("echo", "`date`"), c("date")}, flags: fCS},
	{in: `echo '$(date)'`, cmds: []cmd{c("echo", "$(date)")}},
	{in: `echo \$\(date\)`, cmds: []cmd{c("echo", "$(date)")}},
	{in: `echo "\$(date)"`, cmds: []cmd{c("echo", "$(date)")}},
	{in: `echo \$(date)`, cmds: []cmd{c("echo", "$"), c("date")}, flags: fSub | fBad},
	{in: `echo $(cat ~/.ssh/id_rsa)`, cmds: []cmd{anyArgs("echo"), c("cat", "~/.ssh/id_rsa")}, flags: fCS},
	{in: "echo `cat ~/.ssh/id_rsa`", cmds: []cmd{anyArgs("echo"), c("cat", "~/.ssh/id_rsa")}, flags: fCS},
	{in: `echo $(echo $(echo deep))`, cmds: []cmd{anyArgs("echo"), anyArgs("echo"), c("echo", "deep")}, flags: fCS},
	{in: `echo "$(echo "nested $(echo deep)")"`, cmds: []cmd{anyArgs("echo"), anyArgs("echo"), c("echo", "deep")}, flags: fCS},
	{in: `echo $(a; b | c)`, cmds: []cmd{anyArgs("echo"), c("a"), c("b"), c("c")}, flags: fCS},
	{in: `echo $(a) $(b)`, cmds: []cmd{anyArgs("echo"), c("a"), c("b")}, flags: fCS},
	{in: `a $(b); c`, cmds: []cmd{anyArgs("a"), c("b"), c("c")}, flags: fCS},
	{in: `echo ${x:-$(rm -rf /)}`, cmds: []cmd{anyArgs("echo"), c("rm", "-rf", "/")}, flags: fCS},
	{in: `echo ${x:-"$(rm x)"}`, cmds: []cmd{anyArgs("echo"), c("rm", "x")}, flags: fCS},
	{in: `echo ${x}`, cmds: []cmd{c("echo", "${x}")}},
	{in: `echo ${x:-a b}`, cmds: []cmd{c("echo", "${x:-a b}")}},
	{in: `echo $((1+2))`, cmds: []cmd{c("echo", "$((1+2))")}},
	{in: `echo $((1 + (2*3)))`, cmds: []cmd{c("echo", "$((1 + (2*3)))")}},
	{in: `echo $(( $(x) + 1 ))`, cmds: []cmd{anyArgs("echo"), c("x")}, flags: fCS},
	{in: `echo $((a);(b))`, cmds: []cmd{anyArgs("echo"), c("a"), c("b")}, flags: fCS | fSub},
	{in: `echo $(`, cmds: []cmd{anyArgs("echo")}, flags: fCS | fBad},
	{in: "echo `", cmds: []cmd{anyArgs("echo")}, flags: fCS | fBad},
	{in: `echo ${`, cmds: []cmd{anyArgs("echo")}, flags: fBad},
	{in: `echo $(echo hi`, cmds: []cmd{anyArgs("echo"), c("echo", "hi")}, flags: fCS | fBad},
	{in: `diff <(ls a) <(ls b)`, cmds: []cmd{anyArgs("diff"), c("ls", "a"), c("ls", "b")}, flags: fPS},
	{in: `tee >(gzip > out.gz)`, cmds: []cmd{anyArgs("tee"), c("gzip")}, flags: fPS},
	{in: `cat <(`, cmds: []cmd{anyArgs("cat")}, flags: fPS | fBad},
	{in: `x=$(date) y=2`, cmds: []cmd{c(""), c("date")}, flags: fCS},
	{in: `echo $$ $! $? $# $@ $* $0 $1 ${1} ${#x} $-`, cmds: []cmd{anyArgs("echo")}},
	{in: `echo $`, cmds: []cmd{c("echo", "$")}},
	{in: `echo $ x`, cmds: []cmd{c("echo", "$", "x")}},
	{in: `$(echo ls) -la`, cmds: []cmd{anyArgs("$(echo ls)"), c("echo", "ls")}, flags: fCS},
	{in: "`echo rm` -rf /", cmds: []cmd{anyArgs("`echo rm`"), c("echo", "rm")}, flags: fCS},
	{in: `$CMD -rf /`, cmds: []cmd{c("$CMD", "-rf", "/")}},
	{in: `"$CMD" -rf /`, cmds: []cmd{c("$CMD", "-rf", "/")}},

	// --- brace expansion ---
	{in: `echo {1..3}`, cmds: []cmd{c("echo", "1", "2", "3")}},
	{in: `echo {3..1}`, cmds: []cmd{c("echo", "3", "2", "1")}},
	{in: `echo {1..10..3}`, cmds: []cmd{c("echo", "1", "4", "7", "10")}},
	{in: `echo {01..03}`, cmds: []cmd{c("echo", "01", "02", "03")}},
	{in: `echo {a..c}`, cmds: []cmd{c("echo", "a", "b", "c")}},
	{in: `echo {a,b}{c,d}`, cmds: []cmd{c("echo", "ac", "ad", "bc", "bd")}},
	{in: `echo x{,y}`, cmds: []cmd{c("echo", "x", "xy")}},
	{in: `echo {,a}`, cmds: []cmd{c("echo", "a")}},
	{in: `echo {a,}`, cmds: []cmd{c("echo", "a")}},
	{in: `echo {,}`, cmds: []cmd{c("echo")}},
	{in: `echo {,}""`, cmds: []cmd{c("echo", "", "")}},
	{in: `echo "{,}"`, cmds: []cmd{c("echo", "{,}")}},
	{in: `{,cat} x`, cmds: []cmd{c("cat", "x")}},
	{in: `echo {a,{b,c}}`, cmds: []cmd{c("echo", "a", "b", "c")}},
	{in: `echo pre{a,b}post`, cmds: []cmd{c("echo", "preapost", "prebpost")}},
	{in: `echo "{a,b}" '{a,b}' \{a,b\}`, cmds: []cmd{c("echo", "{a,b}", "{a,b}", "{a,b}")}},
	{in: `echo {a,b`, cmds: []cmd{c("echo", "{a,b")}},
	{in: `echo {a}`, cmds: []cmd{c("echo", "{a}")}},
	{in: `echo {}`, cmds: []cmd{c("echo", "{}")}},
	{in: `echo {1..1000}`, cmds: []cmd{anyArgs("echo")}, flags: fBad},
	{in: `echo {a,b}{c,d}{e,f}{g,h}{i,j}{k,l}{m,n}{o,p}{q,r}`, cmds: []cmd{anyArgs("echo")}, flags: fBad},
	{in: `echo a={b,c}`, cmds: []cmd{c("echo", "a=b", "a=c")}},
	{in: `a={b,c} cmd`, cmds: []cmd{c("cmd")}},
	{in: `rm -rf /{etc,usr}`, cmds: []cmd{c("rm", "-rf", "/etc", "/usr")}},

	// --- compound commands ---
	{in: `(cd sub && make)`, cmds: []cmd{c("cd", "sub"), c("make")}, flags: fSub},
	{in: `(cd sub && make) &`, cmds: []cmd{c("cd", "sub"), c("make")}, flags: fSub | fBG},
	{in: `(a) && (b)`, cmds: []cmd{c("a"), c("b")}, flags: fSub},
	{in: `( a ; b ) | c`, cmds: []cmd{c("a"), c("b"), c("c")}, flags: fSub},
	{in: `{ a; b; }`, cmds: []cmd{c("a"), c("b")}},
	{in: `{ a; b; } > f`, cmds: []cmd{c("a"), c("b"), c("")}},
	{in: `{ a; b`, cmds: []cmd{c("a"), c("b")}, flags: fBad},
	{in: `a; }`, cmds: []cmd{c("a")}, flags: fBad},
	{in: `echo {`, cmds: []cmd{c("echo", "{")}},
	{in: `echo }`, cmds: []cmd{c("echo", "}")}},
	{in: `(a`, cmds: []cmd{c("a")}, flags: fSub | fBad},
	{in: `a)`, cmds: []cmd{c("a")}, flags: fBad},
	{in: `()`, cmds: nil, flags: fSub},
	// The word list of a for loop is reported as a pseudo-command "for", so a
	// caller sees the files a loop is going to walk.
	{in: `for f in *.go; do echo $f; done`, cmds: []cmd{c("for", "*.go"), c("echo", "$f")}},
	{in: `for f in $(ls); do rm $f; done`, cmds: []cmd{c("for", "$(ls)"), c("ls"), c("rm", "$f")}, flags: fCS},
	{in: `for f in a b c; do echo $f; done > out`, cmds: []cmd{c("for", "a", "b", "c"), c("echo", "$f"), c("")}},
	{in: `for f in ~/.ssh/*; do cat "$f"; done`, cmds: []cmd{c("for", "~/.ssh/*"), c("cat", "$f")}},
	{in: "for f in a\\\n b; do :; done", cmds: []cmd{c("for", "a", "b"), c(":")}},
	{in: "for f in x y\ndo echo $f\ndone", cmds: []cmd{c("for", "x", "y"), c("echo", "$f")}},
	{in: `for f; do echo $f; done`, cmds: []cmd{c("for"), c("echo", "$f")}}, // the positional parameters
	{in: `for f in; do echo $f; done`, cmds: []cmd{c("for"), c("echo", "$f")}},
	// for NAME do: the do is the keyword, and the body is commands, not words of the loop
	{in: `for X do rm -rf ~; done`, cmds: []cmd{c("for"), c("rm", "-rf", "~")}},
	{in: `select X do cat ~/.ssh/id_rsa; done`, cmds: []cmd{c("for"), c("cat", "~/.ssh/id_rsa")}},
	{in: "for X\ndo rm x; done", cmds: []cmd{c("for"), c("rm", "x")}},
	{in: `for X Y; do echo; done`, cmds: []cmd{c("echo")}, flags: fBad},
	// mapfile -C: the callback runs in this shell with the index and the line it read appended
	{in: `mapfile -t -C 'rm -f' -c 1 a < f`, cmds: []cmd{anyArgs("mapfile"), c("rm", "-f", "0", "$@")}},
	{in: `readarray -tC 'echo x' a < f`, cmds: []cmd{anyArgs("readarray"), c("echo", "x", "0", "$@")}},
	{in: `mapfile -t a < f`, cmds: []cmd{anyArgs("mapfile")}},
	{in: `select x in a b; do echo $x; done`, cmds: []cmd{c("for", "a", "b"), c("echo", "$x")}},
	{in: `for x in {1..3}; do echo $x; done`, cmds: []cmd{c("for", "1", "2", "3"), c("echo", "$x")}},
	{in: `while read l; do echo "$l"; done < in`, cmds: []cmd{c("read", "l"), c("echo", "$l"), c("")}},
	{in: `until false; do :; done`, cmds: []cmd{c("false"), c(":")}},
	{in: `if a; then b; elif c; then d; else e; fi`, cmds: []cmd{c("a"), c("b"), c("c"), c("d"), c("e")}},
	{in: "if a\nthen\n  b\nfi", cmds: []cmd{c("a"), c("b")}},
	{in: `if [ -f a ]; then echo yes; fi`, cmds: []cmd{c("[", "-f", "a", "]"), c("echo", "yes")}},
	{in: `[[ -f a && -f b ]] && echo ok`, cmds: []cmd{c("[[", "-f", "a", "&&", "-f", "b", "]]"), c("echo", "ok")}},
	{in: `[[ $x =~ ^(a|b)$ ]] && echo ok`, cmds: []cmd{anyArgs("[["), c("echo", "ok")}},
	{in: `[[ a < b ]]`, cmds: []cmd{c("[[", "a", "<", "b", "]]")}},
	{in: `[[ -f a`, cmds: []cmd{anyArgs("[[")}, flags: fBad},
	{in: `while true; do sleep 1; done &`, cmds: []cmd{c("true"), c("sleep", "1")}, flags: fBG},
	{in: `case x in a) echo a;; b) echo b;; esac`, cmds: []cmd{c("echo", "a"), c("echo", "b")}},
	{in: `case x in a) cat ~/.ssh/id_rsa ;; esac`, cmds: []cmd{c("cat", "~/.ssh/id_rsa")}},
	{in: "case \"$f\" in\n  *.tar.gz|*.tgz) tar -tzf \"$f\" ;;\n  (*.zip) unzip -l \"$f\" ;&\n  *) echo other\nesac; echo done", cmds: []cmd{c("tar", "-tzf", "$f"), c("unzip", "-l", "$f"), c("echo", "other"), c("echo", "done")}},
	{in: `case $(id -u) in 0) echo root;; esac`, cmds: []cmd{c("echo", "root"), c("id", "-u")}, flags: fCS},
	{in: `case x in a) case y in b) echo b;; esac;; esac && echo ok`, cmds: []cmd{c("echo", "b"), c("echo", "ok")}},
	{in: `case x in a) ;; b) esac`, cmds: []cmd{}},
	{in: `case x in a) echo esac;; esac`, cmds: []cmd{c("echo", "esac")}},
	{in: `case x in a) echo a;;`, cmds: []cmd{c("echo", "a")}, flags: fBad},              // no esac
	{in: `case x a) echo a;; esac`, cmds: []cmd{}, flags: fBad},                          // no in
	{in: `case x in a) (echo a;; esac`, cmds: []cmd{c("echo", "a")}, flags: fBad | fSub}, // a body that leaves a ( open
	{in: `case x in a > f) echo a;; esac`, cmds: []cmd{c("echo", "a")}, flags: fBad},
	{in: `echo a;; echo b`, cmds: []cmd{c("echo", "a"), c("echo", "b")}, flags: fBad},
	{in: `esac`, cmds: []cmd{}, flags: fBad},
	{in: `true && case x in esac`, cmds: []cmd{c("true")}},
	// a compound after && || | ends its first line at the newline: the next line's command is a command, not a word of the loop
	{in: "true && for f in x\ndo rm -rf ~; done", cmds: []cmd{c("true"), c("for", "x"), c("rm", "-rf", "~")}},
	{in: "a | select f in x\ndo b; done", cmds: []cmd{c("a"), c("for", "x"), c("b")}},
	{in: `function f { echo hi; }; f`, cmds: []cmd{anyArgs("f"), anyArgs("f")}, flags: fBad},
	{in: `f() { echo hi; }; f`, cmds: []cmd{anyArgs("f"), anyArgs("echo"), anyArgs("f")}, flags: fBad | fSub},
	{in: `:(){ :|:& };:`, cmds: []cmd{anyArgs(":"), anyArgs(":"), anyArgs(":"), anyArgs(":")}, flags: fBad | fSub | fBG},
	{in: `(( x++ ))`, cmds: []cmd{anyArgs("x++")}, flags: fBad | fSub},
	{in: `for ((i=0;i<3;i++)); do echo $i; done`, cmds: []cmd{anyArgs("i"), anyArgs("i++"), c("echo", "$i")}, flags: fBad | fSub},
	{in: `arr=(a b c)`, cmds: []cmd{c(""), anyArgs("a")}, flags: fBad | fSub},
	{in: `a && `, cmds: []cmd{c("a")}, flags: fBad},
	{in: `a | `, cmds: []cmd{c("a")}, flags: fBad},
	{in: `&& a`, cmds: []cmd{c("a")}, flags: fBad},
	{in: `| a`, cmds: []cmd{c("a")}, flags: fBad},
	{in: `a ; ; b`, cmds: []cmd{c("a"), c("b")}, flags: fBad},
	{in: `; a`, cmds: []cmd{c("a")}, flags: fBad},
	{in: `a ;; b`, cmds: []cmd{c("a"), c("b")}, flags: fBad},

	// --- inline scripts ---
	{in: `bash -c "rm -rf /"`, cmds: []cmd{anyArgs("bash"), c("rm", "-rf", "/")}},
	{in: `sh -c 'a; b'`, cmds: []cmd{anyArgs("sh"), c("a"), c("b")}},
	{in: `bash -lc 'a'`, cmds: []cmd{anyArgs("bash"), c("a")}},
	{in: `bash -o pipefail -c 'a'`, cmds: []cmd{anyArgs("bash"), c("a")}},
	{in: `bash -c`, cmds: []cmd{c("bash", "-c")}, flags: fBad},
	{in: `bash script.sh`, cmds: []cmd{c("bash", "script.sh")}},
	{in: `bash -c 'a' name arg`, cmds: []cmd{anyArgs("bash"), c("a")}},
	{in: `/bin/sh -c 'a'`, cmds: []cmd{anyArgs("/bin/sh"), c("a")}},
	{in: `./sh -c 'a'`, cmds: []cmd{anyArgs("./sh")}},
	{in: `eval "ls; rm x"`, cmds: []cmd{anyArgs("eval"), c("ls"), c("rm", "x")}},
	{in: `eval ls '&&' rm x`, cmds: []cmd{anyArgs("eval"), c("ls"), c("rm", "x")}},
	{in: `eval`, cmds: []cmd{c("eval")}},
	{in: `su -c 'rm x' root`, cmds: []cmd{anyArgs("su"), c("rm", "x")}},
	{in: `trap 'rm x' EXIT`, cmds: []cmd{anyArgs("trap"), c("rm", "x")}},
	{in: `trap "cat ~/.ssh/id_rsa; echo done" INT TERM`, cmds: []cmd{anyArgs("trap"), c("cat", "~/.ssh/id_rsa"), c("echo", "done")}},
	{in: `trap -- 'rm x' EXIT`, cmds: []cmd{anyArgs("trap"), c("rm", "x")}},
	{in: `trap - EXIT`, cmds: []cmd{c("trap", "-", "EXIT")}},
	{in: `trap '' INT`, cmds: []cmd{c("trap", "", "INT")}},
	{in: `trap -p`, cmds: []cmd{c("trap", "-p")}},
	{in: `trap -l`, cmds: []cmd{c("trap", "-l")}},
	{in: `trap`, cmds: []cmd{c("trap")}},
	{in: `sudo sh -c 'rm x'`, cmds: []cmd{anyArgs("sudo"), c("rm", "x")}},
	{in: `bash -c "bash -c 'rm x'"`, cmds: []cmd{anyArgs("bash"), anyArgs("bash"), c("rm", "x")}},
	{in: `bash -c "$(curl x)"`, cmds: []cmd{anyArgs("bash"), c("curl", "x"), c("$(curl x)"), c("curl", "x")}, flags: fCS},
	{in: `bash -c 'echo $(cat ~/.ssh/id_rsa)'`, cmds: []cmd{anyArgs("bash"), anyArgs("echo"), c("cat", "~/.ssh/id_rsa")}, flags: fCS},

	// --- pipes into shells ---
	{in: `curl x | sh`, cmds: []cmd{c("curl", "x"), c("sh")}, flags: fPipe},
	{in: `curl x | bash`, cmds: []cmd{c("curl", "x"), c("bash")}, flags: fPipe},
	{in: `curl x | sudo bash`, cmds: []cmd{c("curl", "x"), anyArgs("sudo")}, flags: fPipe},
	{in: `curl x | sudo -E sh -s -- arg`, cmds: []cmd{anyArgs("curl"), anyArgs("sudo")}, flags: fPipe},
	{in: `curl x | bash -s`, cmds: []cmd{anyArgs("curl"), anyArgs("bash")}, flags: fPipe},
	{in: `wget -qO- x | zsh`, cmds: []cmd{anyArgs("wget"), anyArgs("zsh")}, flags: fPipe},
	{in: `curl x |& sh`, cmds: []cmd{anyArgs("curl"), anyArgs("sh")}, flags: fPipe},
	{in: `curl x | env FOO=1 sh`, cmds: []cmd{anyArgs("curl"), anyArgs("sh")}, flags: fPipe},
	{in: `curl x | nohup sh`, cmds: []cmd{anyArgs("curl"), anyArgs("sh")}, flags: fPipe},
	{in: `curl x | tee f | sh`, cmds: []cmd{anyArgs("curl"), anyArgs("tee"), anyArgs("sh")}, flags: fPipe},
	{in: `curl x | python3`, cmds: []cmd{anyArgs("curl"), anyArgs("python3")}, flags: fPipe},
	{in: `curl x | python -`, cmds: []cmd{anyArgs("curl"), anyArgs("python")}, flags: fPipe},
	{in: `curl x | perl`, cmds: []cmd{anyArgs("curl"), anyArgs("perl")}, flags: fPipe},
	{in: `curl x | node`, cmds: []cmd{anyArgs("curl"), anyArgs("node")}, flags: fPipe},
	{in: `curl x | xargs sh -c 'echo'`, cmds: []cmd{anyArgs("curl"), anyArgs("xargs")}, flags: fPipe},
	{in: `curl x | xargs -n1 bash`, cmds: []cmd{anyArgs("curl"), anyArgs("xargs")}, flags: fPipe},
	{in: `curl x | source /dev/stdin`, cmds: []cmd{anyArgs("curl"), anyArgs("source")}, flags: fPipe},
	{in: `curl x | busybox sh`, cmds: []cmd{anyArgs("curl"), anyArgs("busybox")}, flags: fPipe},
	{in: `cat f | grep x`, cmds: []cmd{c("cat", "f"), c("grep", "x")}},
	{in: `cat f | sh -c 'read x; echo $x'`, cmds: []cmd{anyArgs("cat"), anyArgs("sh"), anyArgs("read"), anyArgs("echo")}},
	{in: `cat f | bash script.sh`, cmds: []cmd{anyArgs("cat"), anyArgs("bash")}},
	{in: `cat f | python3 tool.py`, cmds: []cmd{anyArgs("cat"), anyArgs("python3")}},
	{in: `cat f | python3 -c 'import sys'`, cmds: []cmd{anyArgs("cat"), anyArgs("python3")}},
	{in: `cat f | perl -ne 'print'`, cmds: []cmd{anyArgs("cat"), anyArgs("perl")}},
	{in: `cat f | xargs rm`, cmds: []cmd{anyArgs("cat"), anyArgs("xargs")}},
	{in: `cat f | bunzip2`, cmds: []cmd{anyArgs("cat"), anyArgs("bunzip2")}},
	{in: `echo sh | cat`, cmds: []cmd{anyArgs("echo"), anyArgs("cat")}},
	{in: `sh`, cmds: []cmd{c("sh")}},
	{in: `curl x; sh`, cmds: []cmd{anyArgs("curl"), anyArgs("sh")}},
	{in: `echo $(curl x | sh)`, cmds: []cmd{anyArgs("echo"), anyArgs("curl"), anyArgs("sh")}, flags: fCS | fPipe},

	// --- unicode look-alikes and odd whitespace ---
	{in: `ｒｍ -rf /`, cmds: []cmd{c("ｒｍ", "-rf", "/")}},
	{in: "ls ；rm x", cmds: []cmd{c("ls", "；rm", "x")}},
	{in: "ls＆＆rm x", cmds: []cmd{c("ls＆＆rm", "x")}},
	{in: "cat\u00a0~/.ssh/id_rsa", cmds: []cmd{c("cat\u00a0~/.ssh/id_rsa")}},
	{in: "r\u200bm -rf /", cmds: []cmd{c("r\u200bm", "-rf", "/")}},
	{in: "сat x", cmds: []cmd{c("сat", "x")}}, // Cyrillic es, not the Latin c
	{in: "echo \u202eevil", cmds: []cmd{c("echo", "\u202eevil")}},
	{in: "echo a\rb", cmds: []cmd{c("echo", "a\rb")}},
	{in: "ls\r\nrm x\r\n", cmds: []cmd{c("ls\r"), c("rm", "x\r")}},
	{in: "echo é😀 日本語", cmds: []cmd{c("echo", "é😀", "日本語")}},
	{in: "echo \\é", cmds: []cmd{c("echo", "é")}},
	{in: "echo '\xff\xfe'", cmds: []cmd{c("echo", "\xff\xfe")}},
	{in: "\xff\xfe x", cmds: []cmd{c("\xff\xfe", "x")}},

	// --- misc real-world one-liners ---
	{in: `git commit -m "fix: it's" && git push`, cmds: []cmd{c("git", "commit", "-m", "fix: it's"), c("git", "push")}},
	{in: `go test ./... 2>&1 | tail -n 20`, cmds: []cmd{c("go", "test", "./..."), c("tail", "-n", "20")}},
	{in: `find . -name '*.go' -exec rm {} \;`, cmds: []cmd{c("find", ".", "-name", "*.go", "-exec", "rm", "{}", ";")}},
	{in: `ls | wc -l`, cmds: []cmd{c("ls"), c("wc", "-l")}},
	{in: `grep -rn "TODO" . | head`, cmds: []cmd{c("grep", "-rn", "TODO", "."), c("head")}},
	{in: `cd /tmp && ls -la`, cmds: []cmd{c("cd", "/tmp"), c("ls", "-la")}},
	{in: `mkdir -p a/b/c && touch a/b/c/f`, cmds: []cmd{c("mkdir", "-p", "a/b/c"), c("touch", "a/b/c/f")}},
	{in: `export PATH=/x:$PATH`, cmds: []cmd{c("export", "PATH=/x:$PATH")}},
	{in: `echo "a" > /dev/null 2>&1 &`, cmds: []cmd{c("echo", "a")}, flags: fBG},
	{in: `docker run -v ~/.ssh:/root/.ssh img`, cmds: []cmd{c("docker", "run", "-v", "~/.ssh:/root/.ssh", "img")}},
	{in: `dd if=/dev/zero of=/dev/sda`, cmds: []cmd{c("dd", "if=/dev/zero", "of=/dev/sda")}},
	{in: `echo 'a' 'b' > /dev/sda`, cmds: []cmd{c("echo", "a", "b")}},
	{in: `git log --format='%H %s' -n 5`, cmds: []cmd{c("git", "log", "--format=%H %s", "-n", "5")}},
	{in: `sed -i 's/a/b/g' file.txt`, cmds: []cmd{c("sed", "-i", "s/a/b/g", "file.txt")}},
	{in: `awk '{print $1}' file`, cmds: []cmd{c("awk", "{print $1}", "file")}},
	{in: `tar czf - dir | ssh host 'cat > x.tgz'`, cmds: []cmd{anyArgs("tar"), c("ssh", "host", "cat > x.tgz")}},
}

func TestParseTable(t *testing.T) {
	for _, tc := range parseCases {
		t.Run(name(tc.in), func(t *testing.T) {
			a := Parse(tc.in)
			if got, want := !a.Parsed, tc.flags.has(fBad); got != want {
				t.Errorf("Parsed=%v (problem %q), want unparsed=%v", a.Parsed, a.Problem, want)
			}
			if a.Parsed != (a.Problem == "") {
				t.Errorf("Parsed=%v but Problem=%q", a.Parsed, a.Problem)
			}
			for _, f := range []struct {
				name string
				got  bool
				bit  flag
			}{
				{"HasSubshell", a.HasSubshell, fSub},
				{"HasCommandSubstitution", a.HasCommandSubstitution, fCS},
				{"HasProcessSubstitution", a.HasProcessSubstitution, fPS},
				{"PipesToShell", a.PipesToShell, fPipe},
				{"Background", a.Background, fBG},
				{"HasHeredoc", a.HasHeredoc, fHD},
			} {
				if f.got != tc.flags.has(f.bit) {
					t.Errorf("%s = %v, want %v", f.name, f.got, tc.flags.has(f.bit))
				}
			}
			if len(a.Commands) != len(tc.cmds) {
				t.Fatalf("got %d commands, want %d:\n%s", len(a.Commands), len(tc.cmds), dump(a))
			}
			for i, want := range tc.cmds {
				got := a.Commands[i]
				if got.Program != want.prog {
					t.Errorf("command %d: Program = %q, want %q\n%s", i, got.Program, want.prog, dump(a))
				}
				if want.args != nil && !reflect.DeepEqual(nonNil(got.Args), want.args) {
					t.Errorf("command %d (%s): Args = %q, want %q", i, got.Program, got.Args, want.args)
				}
			}
			checkInvariants(t, tc.in, a)
		})
	}
}

func name(s string) string {
	s = strings.Map(func(r rune) rune {
		if r == '\n' || r == '\r' || r == '\t' || r == '/' {
			return '_'
		}
		return r
	}, s)
	if len(s) > 60 {
		s = s[:60]
	}
	if s == "" {
		s = "empty"
	}
	return s
}

func nonNil(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}

func dump(a Analysis) string {
	var b strings.Builder
	for i, c := range a.Commands {
		b.WriteString("  ")
		b.WriteString(strings.Repeat("-", 0))
		b.WriteString(string(rune('0' + i%10)))
		b.WriteString(": ")
		b.WriteString(Join(append([]string{c.Program}, c.Args...)))
		b.WriteString("  raw=")
		b.WriteString(Quote(c.Raw))
		b.WriteString("\n")
	}
	return b.String()
}

// checkInvariants asserts properties that must hold for every input.
func checkInvariants(t *testing.T, in string, a Analysis) {
	t.Helper()
	if a.Parsed != (a.Problem == "") {
		t.Errorf("Parsed=%v Problem=%q", a.Parsed, a.Problem)
	}
	for i, c := range a.Commands {
		if !c.Nested && !strings.Contains(in, c.Raw) {
			t.Errorf("command %d: Raw %q is not a substring of the input", i, c.Raw)
		}
		// A trusted analysis never contains a nameless command that carries
		// arguments, and a nameless command is only an assignment or a
		// redirection (an empty command name marks the analysis unparsed).
		if a.Parsed && c.Program == "" && (len(c.Args) > 0 || (len(c.Env) == 0 && len(c.Redirects) == 0)) {
			t.Errorf("command %d: nameless command in a trusted analysis: %+v", i, c)
		}
	}
	if b := Parse(in); !reflect.DeepEqual(a, b) {
		t.Errorf("Parse is not deterministic")
	}
}

func TestEffective(t *testing.T) {
	for _, tc := range []struct {
		in       string
		eff      string
		effArgs  []string
		elevated bool
	}{
		{`ls -la`, "ls", []string{"-la"}, false},
		{`sudo rm -rf /`, "rm", []string{"-rf", "/"}, true},
		{`sudo -u root rm x`, "rm", []string{"x"}, true},
		{`sudo -uroot rm x`, "rm", []string{"x"}, true},
		{`sudo --user=root rm x`, "rm", []string{"x"}, true},
		{`sudo --user root rm x`, "rm", []string{"x"}, true},
		{`sudo -g adm -u root rm x`, "rm", []string{"x"}, true},
		{`sudo -E -H rm x`, "rm", []string{"x"}, true},
		{`sudo -EHu root rm x`, "rm", []string{"x"}, true},
		{`sudo -- rm x`, "rm", []string{"x"}, true},
		{`sudo FOO=1 rm x`, "rm", []string{"x"}, true},
		{`sudo -n -S rm x`, "rm", []string{"x"}, true},
		{`sudo env FOO=1 rm x`, "rm", []string{"x"}, true},
		{`sudo nohup nice -n 3 rm x`, "rm", []string{"x"}, true},
		{`sudo sudo rm x`, "rm", []string{"x"}, true},
		{`sudo -i`, "", nil, true},
		{`sudo -l`, "", nil, true},
		{`sudo`, "", nil, true},
		{`sudo -s ls`, "ls", []string{}, true}, // -s takes no value; ls is the command
		{`doas -u root rm x`, "rm", []string{"x"}, true},
		{`doas rm x`, "rm", []string{"x"}, true},
		{`pkexec --user root rm x`, "rm", []string{"x"}, true},
		{`/usr/bin/sudo rm x`, "rm", []string{"x"}, true},
		{`./sudo rm x`, "./sudo", []string{"rm", "x"}, false},
		{`su -c 'rm x'`, "su", []string{"-c", "rm x"}, true},
		{`env sudo rm x`, "rm", []string{"x"}, true},
	} {
		a := Parse(tc.in)
		if len(a.Commands) == 0 {
			t.Fatalf("%q: no commands", tc.in)
		}
		s := a.Commands[0]
		prog, args := s.EffectiveCommand()
		if prog != tc.eff || (tc.effArgs != nil && !reflect.DeepEqual(nonNil(args), tc.effArgs)) {
			t.Errorf("%q: EffectiveCommand = %q %q, want %q %q", tc.in, prog, args, tc.eff, tc.effArgs)
		}
		if s.Effective() != tc.eff {
			t.Errorf("%q: Effective() = %q, want %q", tc.in, s.Effective(), tc.eff)
		}
		if s.Elevated() != tc.elevated {
			t.Errorf("%q: Elevated() = %v, want %v", tc.in, s.Elevated(), tc.elevated)
		}
	}
}

func TestWrapperMetadata(t *testing.T) {
	a := Parse(`FOO=1 nohup env BAR=2 time -p rm -rf x`)
	if len(a.Commands) != 1 {
		t.Fatalf("commands: %s", dump(a))
	}
	s := a.Commands[0]
	if s.Program != "rm" || !reflect.DeepEqual(s.Args, []string{"-rf", "x"}) {
		t.Errorf("program/args = %q %q", s.Program, s.Args)
	}
	if !reflect.DeepEqual(s.Env, []string{"FOO=1", "BAR=2"}) {
		t.Errorf("Env = %q", s.Env)
	}
	if !reflect.DeepEqual(s.Wrappers, []string{"nohup", "env", "time"}) {
		t.Errorf("Wrappers = %q", s.Wrappers)
	}
	if s.Raw != `FOO=1 nohup env BAR=2 time -p rm -rf x` {
		t.Errorf("Raw = %q", s.Raw)
	}

	// time -o FILE writes FILE: surfaced as a redirect so path rules see it.
	a = Parse(`/usr/bin/time -o out.txt ls`)
	s = a.Commands[0]
	if s.Program != "ls" || len(s.Redirects) != 1 || s.Redirects[0] != (Redirect{Op: ">", Target: "out.txt"}) {
		t.Errorf("time -o: %+v", s)
	}
}

func TestRedirectShapes(t *testing.T) {
	for _, tc := range []struct {
		in   string
		want []Redirect
	}{
		{`c > f`, []Redirect{{">", "f"}}},
		{`c >> f`, []Redirect{{">>", "f"}}},
		{`c < f`, []Redirect{{"<", "f"}}},
		{`c 2> f`, []Redirect{{"2>", "f"}}},
		{`c 2>> f`, []Redirect{{"2>>", "f"}}},
		{`c &> f`, []Redirect{{"&>", "f"}}},
		{`c &>> f`, []Redirect{{"&>>", "f"}}},
		{`c >| f`, []Redirect{{">|", "f"}}},
		{`c <> f`, []Redirect{{"<>", "f"}}},
		{`c 2>&1`, []Redirect{{"2>&", "1"}}},
		{`c >&2`, []Redirect{{">&", "2"}}},
		{`c >&f`, []Redirect{{">&", "f"}}},
		{`c 3<&-`, []Redirect{{"3<&", "-"}}},
		{`c <<< "a b"`, []Redirect{{"<<<", "a b"}}},
		{"c <<EOF\nx\nEOF", []Redirect{{"<<", "EOF"}}},
		{"c <<-'EOF'\nx\nEOF", []Redirect{{"<<-", "EOF"}}},
		{`c > "a b" 2> 'c d'`, []Redirect{{">", "a b"}, {"2>", "c d"}}},
		{`c > $HOME/x`, []Redirect{{">", "$HOME/x"}}},
		{`c > ~/x`, []Redirect{{">", "~/x"}}},
		{`c > .git/config`, []Redirect{{">", ".git/config"}}},
		{`c > $(x)`, []Redirect{{">", "$(x)"}}},
	} {
		a := Parse(tc.in)
		if len(a.Commands) == 0 {
			t.Errorf("%q: no commands", tc.in)
			continue
		}
		if got := a.Commands[0].Redirects; !reflect.DeepEqual(got, tc.want) {
			t.Errorf("%q: redirects = %v, want %v", tc.in, got, tc.want)
		}
	}
}

func TestNestedAreFlaggedAndOrdered(t *testing.T) {
	a := Parse(`a $(b $(c)) d; e`)
	var got []string
	var nested []bool
	for _, c := range a.Commands {
		got = append(got, c.Program)
		nested = append(nested, c.Nested)
	}
	if !reflect.DeepEqual(got, []string{"a", "b", "c", "e"}) {
		t.Errorf("order = %q", got)
	}
	if !reflect.DeepEqual(nested, []bool{false, true, true, false}) {
		t.Errorf("nested flags = %v", nested)
	}
	if !a.HasCommandSubstitution {
		t.Error("HasCommandSubstitution not set")
	}
}

func TestPipedFlag(t *testing.T) {
	a := Parse(`a | b && c | d | e; f`)
	var piped []bool
	for _, c := range a.Commands {
		piped = append(piped, c.Piped)
	}
	if want := []bool{false, true, false, true, true, false}; !reflect.DeepEqual(piped, want) {
		t.Errorf("Piped = %v, want %v", piped, want)
	}
}

func TestFieldsAndQuote(t *testing.T) {
	for _, tc := range []struct {
		in   string
		want []string
		ok   bool
	}{
		{`git status`, []string{"git", "status"}, true},
		{`git commit -m 'a b'`, []string{"git", "commit", "-m", "a b"}, true},
		{`  a   "b c"  d\ e `, []string{"a", "b c", "d e"}, true},
		{``, []string{}, true},
		{`a | b`, nil, false},
		{`a; b`, nil, false},
		{`a $(b)`, nil, false},
		{`a > b`, nil, false},
		{`'unterminated`, nil, false},
		{`a&b`, nil, false},
	} {
		got, ok := Fields(tc.in)
		if ok != tc.ok || (ok && !reflect.DeepEqual(got, tc.want)) {
			t.Errorf("Fields(%q) = %q, %v; want %q, %v", tc.in, got, ok, tc.want, tc.ok)
		}
	}
	for _, w := range []string{"", "a", "a b", "it's", `a"b`, "$x", "~", "a\nb", "*", "é", "a;b", `\`, "'", "''"} {
		got, ok := Fields(Quote(w))
		if !ok || len(got) != 1 || got[0] != w {
			t.Errorf("Fields(Quote(%q)) = %q, %v", w, got, ok)
		}
	}
	if got := Join([]string{"git", "commit", "-m", "a b"}); got != `git commit -m 'a b'` {
		t.Errorf("Join = %q", got)
	}
}

func TestALoopsVariableIsReportedWithItsWordList(t *testing.T) {
	an := Parse(`for f in p1/a.go p2/b.go; do cat "$f"; done`)
	if len(an.Commands) < 2 || an.Commands[0].Program != "for" || an.Commands[0].LoopVar != "f" || len(an.Commands[0].Args) != 2 {
		t.Fatalf("%+v", an.Commands)
	}
	for _, line := range []string{`for ((i=0;i<3;i++)); do echo $i; done`, `echo f in a b`} {
		for _, c := range Parse(line).Commands {
			if c.LoopVar != "" {
				t.Errorf("%q: %+v", line, c)
			}
		}
	}
	// A loop with no word list sets its variable from the positional parameters: it is reported, with no words.
	for _, line := range []string{`for f; do echo $f; done`, `for f do echo $f; done`, "for f\ndo echo $f; done", `select f; do break; done`} {
		if c := Parse(line).Commands; len(c) < 2 || c[0].Program != "for" || c[0].LoopVar != "f" || len(c[0].Args) != 0 {
			t.Errorf("%q: %+v", line, c)
		}
	}
}

// Cond marks a command whose assignments may not reach the commands after it, and HasArithmetic an arithmetic expansion, which may
// assign: the permission engine keeps a line's variables known from these, not from scanning its text.
func TestCondAndArithmeticAreWhatTheShellDoes(t *testing.T) {
	conds := func(src string) []bool {
		var out []bool
		for _, s := range Parse(src).Commands {
			out = append(out, s.Cond)
		}
		return out
	}
	for src, want := range map[string][]bool{
		"a=1; b=2":                               {false, false},
		"a=1 && b=2":                             {false, true},
		"a=1 || b=2; c=3":                        {false, true, false},
		"a | b; c":                               {true, true, false},
		"a & b":                                  {true, false},
		"a=1 && b &; c":                          {true, true, false}, // the whole list runs in the background
		"a=1; b && c & d":                        {false, true, true, false},
		"if a; then b=1; fi; c=2":                {true, true, false},
		"for f in x; do b=1; done; c=2":          {false, true, false}, // the loop header always runs; its variable is a reassignment
		"{ a=1; }; b=2":                          {true, false},
		"(a=1); b=2":                             {true, false},
		"cat > f <<'EOF'\nf() { }\nEOF\nd=1":     {false, false},
		"case $x in a) b=1;; *) c=2;; esac; d=3": {true, true, false}, // a body runs when its pattern matches
		// & after a compound command sends the whole list to the background, what came before the compound included
		"a=1 && case x in *) b;; esac &":  {true, true},
		"a=1 && if x; then b; fi & c=2":   {true, true, true, false},
		"a=1 && while x; do b; done &":    {true, true, true},
		"a=1 && { b; c; } &":              {true, true, true},
		"a=1 && (b; c) &":                 {true, true, true},
		"a=1 && for f in x; do b; done &": {true, true, true},
		"a=1 && for f in x; do b; done":   {false, true, true},       // the header runs only when a=1 succeeded
		"for f in x; do b; done | c; d=1": {true, true, true, false}, // a pipe's writing side is the whole loop
		"if a; then b; fi > out | c; d=1": {true, true, true, true, false},
		"x | for f in y; do b; done; d=1": {true, true, true, false},
		"if a; then b=1 & fi; c=2":        {true, true, false},
	} {
		got := conds(src)
		if len(got) != len(want) {
			t.Errorf("%q: %d commands %v, want %v", src, len(got), got, want)
			continue
		}
		for i := range want {
			if got[i] != want[i] {
				t.Errorf("%q: Cond %v, want %v", src, got, want)
				break
			}
		}
	}
	for src, want := range map[string]bool{
		"echo $((n=5))":                true,
		`echo "$((n=5))"`:              true,
		"echo '$((n=5))'":              false,
		"cat <<EOF\n$((n=5))\nEOF\n":   true,
		"cat <<'EOF'\n$((n=5))\nEOF\n": false,
		"echo $(date)":                 false,
	} {
		if got := Parse(src).HasArithmetic; got != want {
			t.Errorf("%q: HasArithmetic %v, want %v", src, got, want)
		}
	}
}

// AndChain groups the links of an and-or list of the line's own shell that its first link joins by && alone, with every command such
// a link holds; ChainLink marks the links that are simple commands of this shell. An || ends the chain for the rest of its list, a ;,
// & or newline begins another, a background list is in none, and the lists inside a compound command or a substitution are no chains.
func TestAndChainIsTheLinksJoinedByAndAlone(t *testing.T) {
	chains := func(src string) []string {
		var out []string
		for _, s := range Parse(src).Commands {
			c := strconv.Itoa(s.AndChain)
			if s.ChainLink {
				c += "L"
			}
			out = append(out, c)
		}
		return out
	}
	for src, want := range map[string][]string{
		"a && P=1 && b || d; e":                  {"1L", "1L", "1L", "0", "2L"},
		"a; b\nc && d":                           {"1L", "2L", "3L", "3L"},
		"a || P=1 && b":                          {"1L", "0", "0"},
		"a &&\n P=1 &&\n b":                      {"1L", "1L", "1L"}, // a newline after && continues the list
		"a && P=1 | b && c":                      {"1L", "1", "1", "1L"},
		"a | b && c":                             {"1", "1", "1L"},
		"a && P=1 & c":                           {"0", "0", "2L"},
		"a && if b; then P=1; fi && c":           {"1L", "1", "1", "1L"},
		"if a; then b || c; fi && d":             {"1", "1", "1", "1L"}, // an || inside the if is not one of the line's list
		"a && { P=1; } > out && c":               {"1L", "1", "1", "1L"},
		"a && (P=1) && c":                        {"1L", "1", "1L"},
		"a && for f in x; do b; done && c":       {"1L", "1", "1", "1L"},
		"case x in *) a && b;; esac && c":        {"1", "1", "1L"},
		"a && echo $(P=1 && b) && c":             {"1L", "1L", "1", "1", "1L"},
		"a && sh -c 'b && c' && d":               {"1L", "1L", "1", "1", "1L"},
		"a && trap 'b' EXIT":                     {"1L", "1L", "1"},
		"a; P=1 && b || c $(d)":                  {"1L", "2L", "2L", "0", "0"},
		"a && b; case $(c) in *) d;; esac && e":  {"1L", "1L", "2", "2", "2L"},
		"a && b & c; d":                          {"0", "0", "2L", "3L"},
		"if a; then b & fi && c":                 {"1", "1", "1L"}, // a background list inside a link stays in its chain
		"a && { b & wait; } && c":                {"1L", "1", "1", "1L"},
		"a || { b & wait; } && c":                {"1L", "0", "0", "0"},
		"a && while b; do c; done | d && e || f": {"1L", "1", "1", "1", "1L", "0"},
	} {
		if got := chains(src); !slices.Equal(got, want) {
			t.Errorf("%q: AndChain %v, want %v", src, got, want)
		}
	}
	// A ChainLink is Cond only for the && before it; its other reasons (a pipe, a group, a subshell, a branch, the background) make
	// it no link.
	for _, src := range []string{"a && P=1 && b | c && (d) && { e; } && if f; then g; fi && h", "a; b && c & d && e"} {
		for _, s := range Parse(src).Commands {
			if s.ChainLink && (s.Piped || s.Nested || s.AndChain == 0) {
				t.Errorf("%q: %q is a ChainLink: %+v", src, s.Raw, s)
			}
		}
	}
}

// InAssign names the assignment whose value holds the substitution a command came from, counted in the Env of the command before it:
// every command of that substitution, those of a substitution inside it too. The substitutions of words, redirections and heredocs,
// a declaration's operands among them, and the commands of a string run by a new shell or eval, are in none.
func TestInAssignIsTheAssignmentASubstitutionIsIn(t *testing.T) {
	for src, want := range map[string][]int{
		"X=$(a) D=1 Y=$(b; c) >$(d)":     {0, 1, 3, 3, 0},
		"X=$(echo $(a)) D=1":             {0, 1, 1},
		"export X=$(a) D=1":              {0, 0},
		"A=$(a) cmd $(b)":                {0, 1, 0},
		"X=`a` Y=\"$(b)\"":               {0, 1, 2},
		"D=1 <<EOF\n$(a)\nEOF":           {0, 0},
		"sh -c 'X=$(a)'":                 {0, 0, 0},
		"if t; then X=$(a); fi; Y=$(b)":  {0, 0, 1, 0, 1},
		"a && X=$(b) || Y=$(c) | d=$(e)": {0, 0, 1, 0, 1, 0, 1},
	} {
		var got []int
		for _, s := range Parse(src).Commands {
			got = append(got, s.InAssign)
		}
		if !slices.Equal(got, want) {
			t.Errorf("%q: InAssign %v, want %v", src, got, want)
		}
	}
}

// A heredoc with a quoted delimiter read by a program as its own data (python3 - <<'PY') is Literal: nothing in it can assign this
// shell's variables or run in it. A body a shell reads as code, a builtin brings into the shell, a program spelled by a variable or
// a script path reads, or one inside a substitution, is not; nor is an unquoted body, which the shell expands.
func TestLiteralHeredocBodiesAreAProgramsData(t *testing.T) {
	for src, want := range map[string]bool{
		"python3 - <<'PY'\nbase = os.environ[\"TMPDIR\"]\nPY\necho done": true,
		"python3 - x.json <<\"EOF\"\no[\"k\"]=1\nEOF":                    true,
		"cat <<'E' > f.json\n{\"TMPDIR\": 1}\nE":                         true,
		"node - <<-'JS'\n\tprocess.env.HOME\n\tJS":                       true,
		"python3 - <<PY\nos.environ[\"TMPDIR\"]\nPY":                     false, // unquoted: expanded by the shell
		"sh <<'SH'\nTMPDIR=/etc\nSH":                                     false,
		"bash -s <<'SH'\nTMPDIR=/etc\nSH":                                false,
		"read x <<'E'\nTMPDIR=/etc\nE":                                   false,
		"source /dev/stdin <<'E'\nTMPDIR=/etc\nE":                        false,
		"$PY - <<'E'\nTMPDIR=/etc\nE":                                    false,
		"./run.sh <<'E'\nTMPDIR=/etc\nE":                                 false,
		"while read l; do :; done <<'E'\nTMPDIR=/etc\nE":                 false,
		"echo \"$(python3 - <<'E'\nTMPDIR\nE\n)\"":                       false,
	} {
		an := Parse(src)
		if got := len(an.Literal) > 0; got != want {
			t.Errorf("%q: Literal %v, want %v", src, an.Literal, want)
			continue
		}
		code := an.Code(src)
		if len(code) != len(src) || strings.Count(code, "\n") != strings.Count(src, "\n") {
			t.Errorf("%q: Code changed the length or the lines: %q", src, code)
		}
		if want && (strings.Contains(code, "TMPDIR") || strings.Contains(code, "HOME") || strings.Contains(code, `o["k"]`)) {
			t.Errorf("%q: Code still holds the body: %q", src, code)
		}
		if !want && code != src {
			t.Errorf("%q: Code blanked text that is not literal: %q", src, code)
		}
	}
}

// Loop names the outermost loop a command is in: a loop runs all of its commands again, those of a loop or a substitution inside it
// too, and an if, a group or a case is no loop.
func TestLoopIsTheOutermostLoopACommandIsIn(t *testing.T) {
	loops := func(src string) []int {
		var out []int
		for _, s := range Parse(src).Commands {
			out = append(out, s.Loop)
		}
		return out
	}
	got := loops("a; while x; do b; for y in z; do c; done; echo $(d); done; e; until f; do g; done; if h; then i; fi; { j; }")
	// a, x, b, for, c, echo, d, e, f, g, h, i, j
	if len(got) != 13 {
		t.Fatalf("%v", got)
	}
	l1, l2 := got[1], got[9]
	want := []int{0, l1, l1, l1, l1, l1, l1, 0, l2, l2, 0, 0, 0}
	if l1 == 0 || l2 == 0 || l1 == l2 || !slices.Equal(got, want) {
		t.Errorf("Loop = %v, want %v with two different loops", got, want)
	}
}

// Arith holds each arithmetic expansion on the command it belongs to: in its words and redirections, the bodies of its unquoted
// heredocs and a loop's list. One the parser places on no command (a case subject) leaves ArithKnown false; quoted text holds none.
func TestArithIsOnTheCommandItBelongsTo(t *testing.T) {
	for src, want := range map[string]struct {
		arith [][]string
		known bool
	}{
		"n=1; n=$((n+1))":                  {[][]string{nil, {"n+1"}}, true},
		`echo "t=$(( $(date +%s) - S0 ))"`: {[][]string{{" $(date +%s) - S0 "}, nil}, true},
		"cat <<EOF > f\n$((D=0))\nEOF":     {[][]string{{"D=0"}}, true},
		"echo $(echo $((a=1)))":            {[][]string{nil, {"a=1"}}, true},
		"for i in $((1+1)) b; do :; done":  {[][]string{{"1+1"}, nil}, true},
		"x=$(( $((1)) + 2 ))":              {[][]string{{"1", " $((1)) + 2 "}}, true},
		"echo '$((x=1))'":                  {[][]string{nil}, true},
		"case $((n=1)) in *) :;; esac":     {[][]string{nil}, false},
		"cat <<'EOF'\n$((D=0))\nEOF":       {[][]string{nil}, true},
	} {
		an := Parse(src)
		var got [][]string
		for _, s := range an.Commands {
			got = append(got, s.Arith)
		}
		if !reflect.DeepEqual(got, want.arith) || an.ArithKnown != want.known {
			t.Errorf("%q: Arith %q known %v, want %q known %v", src, got, an.ArithKnown, want.arith, want.known)
		}
	}
}

// npx installs the package it is asked for when it is not there, so the program it runs is the word after it only when the install is
// forbidden (--no, --no-install): then it is peeled like a wrapper. Without that, or with an option that names, installs or builds what
// runs, npx stays the program.
func TestNpxIsPeeledOnlyWhenItInstallsNothing(t *testing.T) {
	for _, c := range []struct{ in, prog string }{
		{`npx --no-install tsc --noEmit`, "tsc"},
		{`npx --no eslint src`, "eslint"},
		{`npx -q --no tsc --noEmit`, "tsc"},
		{`npx --no -- tsc --noEmit`, "tsc"},
		{`npx tsc --noEmit`, "npx"},
		{`npx --yes tsc`, "npx"},
		{`npx -y tsc`, "npx"},
		{`npx --no -p typescript tsc`, "npx"},
		{`npx --no -c 'tsc --noEmit'`, "npx"},
		{`npx --no`, "npx"},
	} {
		a := Parse(c.in)
		if len(a.Commands) == 0 {
			t.Fatalf("%q: no commands", c.in)
		}
		if got := a.Commands[0].Program; got != c.prog {
			t.Errorf("%q: program %q, want %q", c.in, got, c.prog)
		}
	}
}
