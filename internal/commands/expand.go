package commands

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/reee344/sleipnir/internal/core"
	"github.com/reee344/sleipnir/internal/skills/mdfile"
)

// Expanded is a command turned into a prompt.
type Expanded struct {
	// Name is the command's full name.
	Name string
	// Prompt is the text to send to the model.
	Prompt string
	// AllowedTools and Model are the command's frontmatter. AllowedTools are
	// requests for the permission engine, not grants.
	AllowedTools []string
	Model        string
	// Notices explain what expansion could not do without failing: a file that was
	// not included, and why. They are for the user; the prompt already says
	// nothing about them.
	Notices []string
	// Hash identifies the unexpanded template.
	Hash core.Hash
}

// Running describes the command being expanded; it is available to Opts.Exec
// and Opts.AllowRead through FromContext.
type Running struct {
	Name         string
	AllowedTools []string
	Scope        Scope
}

type ctxKey struct{}

// FromContext returns the command that is being expanded, for hooks called
// during Expand. It is how a caller applies a command's allowed-tools to the
// shell commands the command runs.
func FromContext(ctx context.Context) (Running, bool) {
	r, ok := ctx.Value(ctxKey{}).(Running)
	return r, ok
}

// Expand renders a command with its arguments. name may carry a leading slash
// and is matched case-insensitively; a command inside a plugin namespace can be
// named without it when that is unambiguous.
//
// The template is read in one pass; see the package documentation for what is
// interpreted. Expand fails when the command needs to run a shell command and
// Opts.Exec is nil or refuses, or when a limit is exceeded; a file that cannot
// be included is a notice, not an error.
func (r *Registry) Expand(ctx context.Context, name, args string) (Expanded, error) {
	cmd, err := r.find(name)
	if err != nil {
		return Expanded{}, err
	}
	args, err = mdfile.CleanArgs(args)
	if err != nil {
		return Expanded{}, fmt.Errorf("command /%s: %v", cmd.Name, err)
	}
	ex := &expansion{
		r: r, cmd: cmd, args: args,
		ctx: context.WithValue(ctx, ctxKey{}, Running{Name: cmd.Name, AllowedTools: append([]string(nil), cmd.AllowedTools...), Scope: cmd.Scope}),
	}
	out, err := ex.render(cmd.body)
	if err != nil {
		return Expanded{}, fmt.Errorf("command /%s: %w", cmd.Name, err)
	}
	if !ex.usedArgs && args != "" {
		out += "\n\nARGUMENTS: " + args
	}
	out += ex.includes.section()
	if len(out) > r.opts.MaxPrompt {
		return Expanded{}, fmt.Errorf("command /%s: the expanded prompt is %d bytes; the limit is %d", cmd.Name, len(out), r.opts.MaxPrompt)
	}
	return Expanded{
		Name: cmd.Name, Prompt: out, AllowedTools: append([]string(nil), cmd.AllowedTools...), Model: cmd.Model,
		Notices: ex.notices, Hash: cmd.Hash,
	}, nil
}

// find resolves a command name the way people type it.
func (r *Registry) find(name string) (*Command, error) {
	name = strings.TrimPrefix(strings.TrimSpace(name), "/")
	if r != nil && name != "" {
		lower := strings.ToLower(name)
		if i, ok := r.byKey[lower]; ok {
			return &r.cmds[i], nil
		}
		if !strings.Contains(name, ":") {
			var match *Command
			var all []string
			for i := range r.cmds {
				if strings.HasSuffix(strings.ToLower(r.cmds[i].Name), ":"+lower) {
					match = &r.cmds[i]
					all = append(all, r.cmds[i].Name)
				}
			}
			if len(all) == 1 {
				return match, nil
			}
			if len(all) > 1 {
				return nil, fmt.Errorf("command name %q is ambiguous; use the full name (%s)", mdfile.OneLine(name, 40), strings.Join(all, ", "))
			}
		}
	}
	return nil, fmt.Errorf("unknown command %q", mdfile.OneLine(mdfile.EscapeTags(name), 60))
}

// expansion is the state of one Expand call.
type expansion struct {
	r    *Registry
	cmd  *Command
	args string
	ctx  context.Context

	usedArgs bool
	execs    int
	includes includeSet
	notices  []string
}

func (ex *expansion) notice(format string, args ...any) {
	ex.notices = append(ex.notices, fmt.Sprintf(format, args...))
}

// render interprets a template: literal text gets its arguments substituted,
// !`command` is replaced by the command's output, and @path is noted for
// inclusion. Fenced code blocks are literal (arguments are still substituted).
func (ex *expansion) render(tmpl string) (string, error) {
	var out strings.Builder
	var fence mdfile.Fence
	lines := strings.SplitAfter(tmpl, "\n")
	for _, line := range lines {
		if line == "" {
			continue
		}
		if fence.Next(line) {
			s, used := mdfile.Substitute(line, ex.args, mdfile.ArgsText)
			ex.usedArgs = ex.usedArgs || used
			out.WriteString(s)
			continue
		}
		if err := ex.line(line, &out); err != nil {
			return "", err
		}
	}
	return out.String(), nil
}

// line interprets one line outside a code fence.
func (ex *expansion) line(line string, out *strings.Builder) error {
	var lit strings.Builder
	flush := func() {
		if lit.Len() == 0 {
			return
		}
		s, used := mdfile.Substitute(lit.String(), ex.args, mdfile.ArgsText)
		ex.usedArgs = ex.usedArgs || used
		out.WriteString(s)
		lit.Reset()
	}
	for i := 0; i < len(line); {
		c := line[i]
		switch {
		case c == '!' && i+1 < len(line) && line[i+1] == '`':
			if end := strings.IndexByte(line[i+2:], '`'); end > 0 && strings.TrimSpace(line[i+2:i+2+end]) != "" {
				flush()
				res, err := ex.run(line[i+2 : i+2+end])
				if err != nil {
					return err
				}
				out.WriteString(res)
				i += 2 + end + 1
				continue
			}
		case c == '@' && (i == 0 || boundaryBefore(line[i-1])):
			if tok := pathToken(line[i+1:]); tok != "" {
				flush()
				spec, used := mdfile.Substitute(tok, ex.args, mdfile.ArgsText)
				ex.usedArgs = ex.usedArgs || used
				ex.include(spec)
				out.WriteString("@" + spec)
				i += 1 + len(tok)
				continue
			}
		}
		lit.WriteByte(c)
		i++
	}
	flush()
	return nil
}

// boundaryBefore reports whether a mention may start right after b.
func boundaryBefore(b byte) bool {
	switch b {
	case ' ', '\t', '(', '[', '{', '"', '\'':
		return true
	}
	return false
}

// pathToken reads the path that follows an "@": up to white space or a
// character that ends a path in prose, without trailing punctuation.
func pathToken(s string) string {
	end := 0
scan:
	for end < len(s) {
		switch s[end] {
		case ' ', '\t', '\n', '\r', '"', '\'', '`', '<', '>', '|':
			break scan
		}
		end++
	}
	return strings.TrimRight(s[:end], ".,;:!?)]}")
}

// run executes a !`command` through Opts.Exec.
func (ex *expansion) run(cmd string) (string, error) {
	cmd = strings.TrimSpace(cmd)
	if ex.r.opts.Exec == nil {
		return "", fmt.Errorf("it runs the shell command %q, but shell commands are not available (no executor is configured)", mdfile.OneLine(cmd, 60))
	}
	if ex.execs >= ex.r.opts.MaxExecs {
		return "", fmt.Errorf("it runs more than %d shell commands", ex.r.opts.MaxExecs)
	}
	ex.execs++
	cmd, used := mdfile.Substitute(cmd, ex.args, mdfile.ArgsShell)
	cmd = strings.TrimSpace(cmd) // a missing argument leaves no trailing space behind
	ex.usedArgs = ex.usedArgs || used

	ctx, cancel := context.WithTimeout(ex.ctx, ex.r.opts.ExecTimeout)
	defer cancel()
	type result struct {
		out string
		err error
	}
	done := make(chan result, 1)
	go func() {
		out, err := ex.r.opts.Exec(ctx, cmd)
		done <- result{out, err}
	}()
	select {
	case res := <-done:
		if res.err != nil {
			return "", fmt.Errorf("the shell command %q failed: %w", mdfile.OneLine(cmd, 60), res.err)
		}
		return ex.clip(res.out), nil
	case <-ctx.Done():
		// The executor is expected to honour ctx; if it does not, the expansion
		// still ends on time and the executor's goroutine is left to finish alone.
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			return "", fmt.Errorf("the shell command %q timed out after %v", mdfile.OneLine(cmd, 60), ex.r.opts.ExecTimeout)
		}
		return "", ctx.Err()
	}
}

// clip cleans and bounds command output.
func (ex *expansion) clip(out string) string {
	out, _ = mdfile.Sanitize(mdfile.Normalize([]byte(out)))
	out = strings.TrimRight(out, " \t\n")
	if cut, ok := mdfile.CutLines(out, ex.r.opts.MaxExecOutput); ok {
		out = cut + "\n[... output truncated ...]"
	}
	return out
}
