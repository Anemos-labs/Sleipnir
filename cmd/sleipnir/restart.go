package main

import (
	"errors"
	"os"
	"os/exec"
	"os/signal"
	"strings"

	"github.com/anemos-labs/sleipnir/internal/session"
)

// splitArgs splits a line the way a shell would, for the quotes only: words are separated by spaces, "double" and 'single' quotes keep
// theirs together, and there is no other syntax (no variables, no escapes, no operators).
func splitArgs(s string) []string {
	var out []string
	var cur strings.Builder
	var quote rune
	started := false
	for _, r := range s {
		switch {
		case quote != 0:
			if r == quote {
				quote = 0
			} else {
				cur.WriteRune(r)
			}
		case r == '"' || r == '\'':
			quote, started = r, true
		case r == ' ' || r == '\t':
			if started {
				out = append(out, cur.String())
				cur.Reset()
				started = false
			}
		default:
			cur.WriteRune(r)
			started = true
		}
	}
	if started {
		out = append(out, cur.String())
	}
	return out
}

// restartArgs are the arguments to start `sleipnir chat` again with, from the flags a person typed after /restart: what they did not say
// stays as it is now (the model, the permission mode), and the conversation comes along (--resume) unless the new shape is a swarm,
// which cannot resume, the person said what to resume themselves, or fresh says to start it empty (/new).
func restartArgs(s *session.Session, typed []string, fresh bool) ([]string, error) {
	has := func(name string) bool {
		for _, a := range typed {
			if a == name || a == "-"+name || strings.HasPrefix(a, name+"=") || strings.HasPrefix(a, "-"+name+"=") {
				return true
			}
		}
		return false
	}
	for _, a := range typed {
		if !strings.HasPrefix(a, "-") && len(typed) > 0 && !looksLikeFlagValue(typed, a) {
			return nil, errors.New("only flags go after /restart, like --swarm 8 or --no-mcp (a goal is typed at the prompt)")
		}
	}
	args := append([]string(nil), typed...)
	// What the session was started with stays, unless the line says otherwise: a restart changes one thing and keeps the rest (a team stays a team).
	flags := s.StartFlags()
	for i := 0; i < len(flags); i++ {
		name := strings.SplitN(flags[i], "=", 2)[0]
		takesValue := !strings.Contains(flags[i], "=") && i+1 < len(flags) && !strings.HasPrefix(flags[i+1], "-")
		if name == "--role-model" { // repeatable: the line's own role wins over the same role named before
			if role, _, _ := strings.Cut(flags[i+1], "="); !roleNamed(typed, role) {
				args = append(args, flags[i], flags[i+1])
			}
			i++
			continue
		}
		if !has(name) {
			args = append(args, flags[i])
			if takesValue {
				args = append(args, flags[i+1])
			}
		}
		if takesValue {
			i++
		}
	}
	if !has("--model") && s.ModelRef() != "" {
		args = append(args, "--model", s.ModelRef())
	}
	if !has("--mode") {
		args = append(args, "--mode", string(s.Perm.Mode()))
	}
	if !fresh && !has("--swarm") && !has("--resume") && !has("--continue") && s.Swarm == nil && session.Resumable(s.Dir) {
		args = append(args, "--resume", s.ID)
	}
	return args, nil
}

// roleNamed reports whether typed already names a role's model (--role-model role=...).
func roleNamed(typed []string, role string) bool {
	for i, a := range typed {
		if a == "--role-model" && i+1 < len(typed) && strings.HasPrefix(typed[i+1], role+"=") {
			return true
		}
	}
	return false
}

// looksLikeFlagValue reports whether word is the value of the flag before it in typed (a number, a quoted command): anything that is not
// itself a flag and follows one.
func looksLikeFlagValue(typed []string, word string) bool {
	for i, a := range typed {
		if a == word && i > 0 && strings.HasPrefix(typed[i-1], "-") && !strings.Contains(typed[i-1], "=") {
			return true
		}
	}
	return false
}

// runAgain starts `sleipnir chat` with args as a child that has this process's terminal, waits for it, and ends this process with its
// status. The child gets the provider keys this process holds out of the environment (jobEnv). Ctrl-C is the child's: it is ignored here.
func runAgain(args []string) {
	self, err := os.Executable()
	if err != nil {
		reportError(os.Stderr, err)
		os.Exit(1)
	}
	signal.Ignore(os.Interrupt)
	cmd := exec.Command(self, append([]string{"chat"}, args...)...)
	cmd.Env = jobEnv(os.Environ())
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
	if err := cmd.Run(); err != nil {
		var ee *exec.ExitError
		if errors.As(err, &ee) {
			os.Exit(ee.ExitCode())
		}
		reportError(os.Stderr, err)
		os.Exit(1)
	}
	os.Exit(0)
}
