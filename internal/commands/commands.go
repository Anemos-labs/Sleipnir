package commands

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/anemos-labs/sleipnir/internal/core"
	"github.com/anemos-labs/sleipnir/internal/skills/mdfile"
)

// Aliases so that callers deal with one vocabulary.
type (
	// Warning is a problem found while loading commands; see mdfile.Warning.
	Warning = mdfile.Warning
	// Scope says who a command came from.
	Scope = mdfile.Scope
	// Extra is a caller-supplied directory of commands (a plugin's commands/).
	Extra = mdfile.Extra
)

// Scopes.
const (
	ScopeProject = mdfile.ScopeProject
	ScopeUser    = mdfile.ScopeUser
	ScopeExtra   = mdfile.ScopeExtra
)

// Limits. Every one can be lowered or raised through Opts; the zero value of an
// option means the default named here.
const (
	// MaxCommands bounds the registry.
	MaxCommands = 512
	// MaxEntriesPerDir bounds the directory entries examined in one directory.
	MaxEntriesPerDir = 1024
	// MaxDepth is how many directory levels below a commands directory are read.
	MaxDepth = 3

	DefaultMaxFileBytes    = 64 << 10
	DefaultMaxIncludeBytes = 32 << 10
	DefaultMaxIncludes     = 8
	DefaultMaxExecs        = 8
	DefaultMaxExecOutput   = 16 << 10
	DefaultExecTimeout     = 30 * time.Second
	DefaultMaxPrompt       = 256 << 10

	maxNameLen = 64
	maxDesc    = 200
	maxHint    = 120
)

var nameRules = mdfile.NameRules{Max: maxNameLen, Upper: true, Dot: true, Underscore: true}

// DefaultBuiltins are the command names reserved when Opts.Builtins is nil:
// the ones whose impersonation would confuse or endanger a user. A caller that
// knows its real built-ins passes them instead.
func DefaultBuiltins() []string {
	return []string{
		"agents", "clear", "compact", "config", "context", "cost", "doctor", "exit", "help", "hooks", "init",
		"login", "logout", "mcp", "memory", "model", "effort", "permissions", "plan", "quit", "resume", "rewind",
		"skills", "status", "trust", "usage",
	}
}

// Opts says where to look and what expansion may do.
type Opts struct {
	// Root is the project root; empty means no project commands, and no @path
	// includes (there is nothing to confine them to).
	Root string
	// Home is the user's home directory; empty means the current user's.
	Home string
	// TrustProject allows the repository's own commands to be read.
	TrustProject bool
	// Extra lists further command directories, such as installed plugins.
	Extra []Extra

	// Builtins are command names user files may not define; nil means
	// DefaultBuiltins, an empty non-nil slice reserves nothing. Matching ignores
	// case.
	Builtins []string

	// Exec runs a shell command for !`command` and returns its output. The
	// context carries the running command (see FromContext) so that the caller can
	// apply the command's allowed-tools. nil means such commands are refused: an
	// expansion that contains one fails.
	Exec func(ctx context.Context, cmd string) (string, error)
	// AllowRead is asked before an @path file is included, with the file's real
	// path; a non-nil error refuses it and is reported as a notice. nil means only
	// the built-in guard applies.
	AllowRead func(ctx context.Context, path string) error

	MaxFileBytes    int64         // one command file (default 64 KiB)
	MaxIncludeBytes int64         // one included file (default 32 KiB)
	MaxIncludes     int           // files included per expansion (default 8)
	MaxExecs        int           // shell commands per expansion (default 8)
	MaxExecOutput   int           // bytes kept of one command's output (default 16 KiB)
	ExecTimeout     time.Duration // per shell command (default 30 s)
	MaxPrompt       int           // the expanded prompt (default 256 KiB)
}

func (o Opts) withDefaults() Opts {
	if o.MaxFileBytes <= 0 {
		o.MaxFileBytes = DefaultMaxFileBytes
	}
	if o.MaxIncludeBytes <= 0 {
		o.MaxIncludeBytes = DefaultMaxIncludeBytes
	}
	if o.MaxIncludes <= 0 {
		o.MaxIncludes = DefaultMaxIncludes
	}
	if o.MaxExecs <= 0 {
		o.MaxExecs = DefaultMaxExecs
	}
	if o.MaxExecOutput <= 0 {
		o.MaxExecOutput = DefaultMaxExecOutput
	}
	if o.ExecTimeout <= 0 {
		o.ExecTimeout = DefaultExecTimeout
	}
	if o.MaxPrompt <= 0 {
		o.MaxPrompt = DefaultMaxPrompt
	}
	return o
}

// Command is one custom slash command.
type Command struct {
	// Name is the file name without ".md", with directory levels joined by ":"
	// ("frontend:lint"), prefixed "namespace:" for a namespaced plugin.
	Name string
	// Description is one line for menus; it defaults to the first line of the
	// template.
	Description  string
	ArgumentHint string
	// AllowedTools are the tool rules the command asks to pre-approve. Reported,
	// never granted here.
	AllowedTools []string
	// Model names the model to run the command with ("" means the current one).
	Model string

	Scope Scope
	// Path is the command file as shown to the user: relative to the project
	// root or "~/...".
	Path string
	// Hash identifies the template text.
	Hash core.Hash
	// Truncated says the file exceeded the size cap and the template was cut.
	Truncated bool

	body string
}

// Registry is the set of loaded commands. It is immutable and safe for
// concurrent use.
type Registry struct {
	cmds  []Command // sorted by Name
	byKey map[string]int
	opts  Opts
	root  string // symlink-free project root, "" when there is none
}

// List returns the commands sorted by name.
func (r *Registry) List() []Command {
	if r == nil {
		return nil
	}
	return append([]Command(nil), r.cmds...)
}

// Get returns a command by exact name (case-insensitively).
func (r *Registry) Get(name string) (Command, bool) {
	if r == nil {
		return Command{}, false
	}
	i, ok := r.byKey[strings.ToLower(name)]
	if !ok {
		return Command{}, false
	}
	return r.cmds[i], true
}

// knownKeys are frontmatter fields that other harnesses define and this loader
// deliberately does not act on.
var knownKeys = []string{
	"name", "disable-model-invocation", "user-invocable", "version", "license", "metadata", "effort",
	"argument-hint-long", "arguments", "when_to_use",
}

// Load discovers the commands under opts. It never fails as a whole: a file that
// cannot be loaded is skipped and reported, and the rest are returned. The
// warnings are deterministic and in discovery order.
func Load(opts Opts) (*Registry, []Warning) {
	o := opts.withDefaults()
	srcs, warns := mdfile.Sources("commands", mdfile.Layout{Root: o.Root, Home: o.Home, TrustProject: o.TrustProject, Extra: o.Extra})
	reserved := map[string]bool{}
	builtins := o.Builtins
	if builtins == nil {
		builtins = DefaultBuiltins()
	}
	for _, b := range builtins {
		reserved[strings.ToLower(b)] = true
	}
	ld := &loader{o: o, reserved: reserved, byKey: map[string]int{}}
	for _, src := range srcs {
		visited := map[string]bool{src.Real: true}
		warns = append(warns, ld.walk(src, src.Real, nil, 0, visited)...)
	}
	sort.Slice(ld.cmds, func(i, j int) bool { return ld.cmds[i].Name < ld.cmds[j].Name })
	r := &Registry{cmds: ld.cmds, byKey: make(map[string]int, len(ld.cmds)), opts: o}
	for i, c := range r.cmds {
		r.byKey[strings.ToLower(c.Name)] = i
	}
	if o.Root != "" {
		if root, err := mdfile.Canonical(o.Root); err == nil {
			r.root = root
		}
	}
	return r, warns
}

type loader struct {
	o        Opts
	reserved map[string]bool
	cmds     []Command
	byKey    map[string]int
}

// walk reads one directory of a commands source. prefix holds the directory
// names below the source root ("frontend" for frontend/lint.md).
func (l *loader) walk(src mdfile.Source, dir string, prefix []string, depth int, visited map[string]bool) []Warning {
	entries, more, err := mdfile.ReadDirLimited(dir, MaxEntriesPerDir)
	label := displayDir(src, prefix)
	if err != nil {
		return []Warning{mdfile.Warnf(label, "", "cannot read the directory: %v", err)}
	}
	var warns []Warning
	if more {
		warns = append(warns, mdfile.Warnf(label, "", "more than %d entries; the rest are ignored", MaxEntriesPerDir))
	}
	for _, e := range entries {
		name := e.Name()
		if strings.HasPrefix(name, ".") {
			continue
		}
		p := filepath.Join(dir, name)
		isDir, isFile := e.Type().IsDir(), e.Type().IsRegular()
		if e.Type()&fs.ModeSymlink != 0 {
			real, err := filepath.EvalSymlinks(p)
			if err != nil {
				continue // dangling
			}
			fi, err := os.Stat(real)
			if err != nil {
				continue
			}
			isDir, isFile = fi.IsDir(), fi.Mode().IsRegular()
			if isDir {
				if src.Contain != "" && !mdfile.Within(src.Contain, real) {
					warns = append(warns, mdfile.Skipf(label+"/"+name, "", "the directory resolves outside its allowed location; skipped"))
					continue
				}
				if visited[real] {
					continue // a cycle, or the same directory twice
				}
				visited[real] = true
			}
		}
		switch {
		case isDir:
			if depth >= MaxDepth {
				warns = append(warns, mdfile.Warnf(label+"/"+name, "", "nested more than %d directories deep; ignored", MaxDepth))
				continue
			}
			if err := mdfile.CheckName(name, nameRules); err != nil {
				warns = append(warns, mdfile.Skipf(label+"/"+name, "", "%v (directory names become part of the command name)", err))
				continue
			}
			warns = append(warns, l.walk(src, p, append(append([]string(nil), prefix...), name), depth+1, visited)...)
		case isFile && strings.HasSuffix(strings.ToLower(name), ".md"):
			warns = append(warns, l.file(src, p, prefix, name[:len(name)-3])...)
		}
	}
	return warns
}

// displayDir combines a command source label with optional slash-separated subdirectory segments.
func displayDir(src mdfile.Source, prefix []string) string {
	if len(prefix) == 0 {
		return src.Label
	}
	return src.Label + "/" + strings.Join(prefix, "/")
}

func (l *loader) file(src mdfile.Source, path string, prefix []string, base string) []Warning {
	display := displayDir(src, prefix) + "/" + base + ".md"
	if err := mdfile.CheckName(base, nameRules); err != nil {
		return []Warning{mdfile.Skipf(display, "", "%v", err)}
	}
	segs := append(append([]string(nil), prefix...), base)
	if src.Namespace != "" {
		segs = append([]string{src.Namespace}, segs...)
	}
	name := strings.Join(segs, ":")

	key := strings.ToLower(name)
	if l.reserved[key] {
		return []Warning{mdfile.Skipf(display, name, "the name is reserved for a built-in command")}
	}
	if at, dup := l.byKey[key]; dup {
		return []Warning{mdfile.Skipf(display, name, "shadowed by the command of the same name in %s", l.cmds[at].Path)}
	}
	if len(l.cmds) >= MaxCommands {
		return []Warning{mdfile.Skipf(display, name, "too many commands (the limit is %d)", MaxCommands)}
	}

	parsed, err := mdfile.ReadDoc(path, mdfile.ReadOpts{MaxBytes: l.o.MaxFileBytes, Contain: src.Contain})
	switch {
	case errors.Is(err, mdfile.ErrEscapes):
		return []Warning{mdfile.Skipf(display, name, "the file resolves outside its allowed location; skipped")}
	case err != nil:
		return []Warning{mdfile.Skipf(display, name, "%v", err)}
	}

	var warns []Warning
	adv := func(format string, args ...any) { warns = append(warns, mdfile.Warnf(display, name, format, args...)) }
	f, err := mdfile.NewFields(parsed.Doc.Meta)
	if err != nil {
		return []Warning{mdfile.Skipf(display, name, "%v", err)}
	}
	desc, _ := f.Str("description")
	hint, _ := f.Str("argument-hint")
	tools, _ := f.List("allowed-tools")
	model, _ := f.Str("model")
	hasHooks := f.Has("hooks")
	for _, k := range knownKeys {
		f.Has(k)
	}
	if err := f.Err(); err != nil {
		return []Warning{mdfile.Skipf(display, name, "%v", err)}
	}
	if desc == "" {
		desc = firstLine(parsed.Doc.Body)
	}
	desc = mdfile.OneLine(desc, maxDesc)
	hint = mdfile.OneLine(hint, maxHint)
	tools, bad := mdfile.FilterTools(tools)
	if len(bad) > 0 {
		adv("ignored malformed allowed-tools entries: %s", quoted(bad))
	}
	if model != "" && !plainToken(model) {
		adv("model %q is not a valid model name; ignored", mdfile.OneLine(model, 40))
		model = ""
	}
	if hasHooks {
		adv("the hooks frontmatter field is not supported and is ignored")
	}
	if un := f.Unused(); len(un) > 0 {
		adv("unknown frontmatter field(s) ignored: %s", strings.Join(un, ", "))
	}
	for _, n := range parsed.Doc.Notes {
		adv("%s", n)
	}
	if parsed.Hidden.Suspicious() {
		adv("removed %d hidden characters (bidirectional controls, tag characters or zero-width runs); check the file", parsed.Hidden.Total())
	}
	if parsed.Truncated {
		adv("the file is larger than %d KB; the template was truncated", l.o.MaxFileBytes>>10)
	}
	if strings.TrimSpace(parsed.Doc.Body) == "" {
		return append(warns, mdfile.Skipf(display, name, "the command has no text"))
	}

	l.byKey[key] = len(l.cmds)
	l.cmds = append(l.cmds, Command{
		Name: name, Description: desc, ArgumentHint: hint, AllowedTools: tools, Model: model,
		Scope: src.Scope, Path: display, Hash: core.HashString(parsed.Doc.Body), Truncated: parsed.Truncated,
		body: parsed.Doc.Body,
	})
	return warns
}

// firstLine returns the first nonblank command-body line after trimming whitespace and leading
// heading markers.
func firstLine(body string) string {
	for _, ln := range strings.Split(body, "\n") {
		if ln = strings.TrimSpace(strings.TrimLeft(strings.TrimSpace(ln), "# ")); ln != "" {
			return ln
		}
	}
	return ""
}

// quoted formats bounded single-line values as quoted strings separated by commas.
func quoted(in []string) string {
	out := make([]string, len(in))
	for i, s := range in {
		out[i] = fmt.Sprintf("%q", mdfile.OneLine(s, 40))
	}
	return strings.Join(out, ", ")
}

// plainToken reports whether s can be a model reference: short, printable ASCII,
// no white space.
func plainToken(s string) bool {
	if s == "" || len(s) > 100 {
		return false
	}
	for _, r := range s {
		if r <= ' ' || r > 0x7E {
			return false
		}
	}
	return true
}
