package shell

import "regexp"

// fullLineMatch finds a pkill or pgrep that matches whole command lines (-f, --full): the pattern is then also found in the
// command line of the shell that runs it, which holds the same text.
var fullLineMatch = regexp.MustCompile(`\bp(?:kill|grep)\b[^;&|\n]*?(?:\s-[a-zA-Z]*f[a-zA-Z]*\b|\s--full\b)`)

// selfKillNote explains a command that ended on SIGTERM or SIGKILL (exit code 143 or 137) the harness did not send, when it
// ran pkill -f or pgrep -f: the pattern it was given is in its own shell's command line, so it most likely ended itself
// before doing the rest of its work. It is empty otherwise.
func selfKillNote(command string, code int, reason killReason) string {
	if reason != killNone || (code != 128+15 && code != 128+9) || !fullLineMatch.MatchString(command) {
		return ""
	}
	return "[note: the command ended on a signal the harness did not send. pkill -f and pgrep -f match whole command lines, " +
		"and the shell running this command has the pattern in its own, so it most likely ended itself. Use a pattern " +
		"that cannot match itself, such as '[s]erver.rb' for 'server.rb', or kill by pid]"
}
