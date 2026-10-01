package agentdefs

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/anemos-labs/sleipnir/internal/core"
	"github.com/anemos-labs/sleipnir/internal/perm"
	"github.com/anemos-labs/sleipnir/internal/skills/mdfile"
)

// Aliases so that callers deal with one vocabulary.
type (
	// Warning is a problem found while loading definitions; see mdfile.Warning.
	Warning = mdfile.Warning
	// Scope says who a definition came from.
	Scope = mdfile.Scope
	// Extra is a caller-supplied directory of definitions (a plugin's agents/).
	Extra = mdfile.Extra
)

// Scopes.
const (
	ScopeProject = mdfile.ScopeProject
	ScopeUser    = mdfile.ScopeUser
	ScopeExtra   = mdfile.ScopeExtra
)

// Request priorities, mirroring agent.Prio*: a lower number is served first
// when the governor must choose. They are duplicated here because this package
// must not import the agent runtime.
const (
	PriorityInteractive = 0
	PriorityWorker      = 1
	PriorityBackground  = 2
)

const (
	// DefaultMaxSteps bounds one assignment of a role that does not say.
	DefaultMaxSteps = 150
	// MaxStepsCap is the largest step budget a definition may ask for.
	MaxStepsCap = 1000

	// DefaultWarnPinTokens and DefaultMaxPinTokens are the pin size limits. The
	// pin is part of every request of every agent of the role, so what it costs is
	// its size times the requests, not its size once.
	DefaultWarnPinTokens = 800
	DefaultMaxPinTokens  = 4000

	// MaxDefs bounds the number of definitions.
	MaxDefs = 128
	// MaxFileBytes caps one definition file.
	MaxFileBytes = 64 << 10
	// MaxEntries bounds the directory entries examined in one agents directory.
	MaxEntries = 1024

	maxNameLen = 40
	maxDesc    = 200
	maxShort   = 4
)

var nameRules = mdfile.NameRules{Max: maxNameLen, Underscore: true}

// Opts says where to look and how strictly to judge.
type Opts struct {
	// Root is the project root; empty means no project definitions.
	Root string
	// Home is the user's home directory; empty means the current user's.
	Home string
	// TrustProject allows the repository's own definitions to be read.
	TrustProject bool
	// Extra lists further definition directories, such as installed plugins.
	Extra []Extra

	// Est measures pins. nil means a fresh core.NewBytesEstimator, which keeps the
	// size warnings stable from run to run.
	Est core.Estimator
	// WarnPinTokens and MaxPinTokens override the pin size limits (0 means the
	// defaults).
	WarnPinTokens, MaxPinTokens int

	// Reserved are role names definitions may not take, typically the built-in
	// roles. Matching ignores case.
	Reserved []string
	// ReservedShorts are agent-id prefixes already in use ("mgr", "be", ...), so
	// that prefixes derived for definitions avoid them.
	ReservedShorts []string
	// DefaultMaxSteps is the step budget of a definition without maxTurns; 0 means
	// DefaultMaxSteps.
	DefaultMaxSteps int
}

// Def is one loaded definition.
type Def struct {
	// Name is the role name.
	Name string
	// Description says when to use the role. One line.
	Description string
	// Short is the agent-id prefix ("cr" gives cr-1, cr-2, ...), unique among the
	// loaded definitions and Opts.ReservedShorts.
	Short string
	// Tools is the allowlist, as written ("Read", "Bash(git diff:*)"); nil means
	// the definition does not restrict tools.
	Tools []string
	// DisallowedTools are tool rules the role must never use.
	DisallowedTools []string
	// Model names the model to run the role with ("" means the session's).
	Model string
	// PermissionMode is "", "default", "accept-edits" or "plan". A definition can
	// only tighten the session's mode; see Profile.
	PermissionMode string
	// ReadOnly is computed as described in the package documentation.
	ReadOnly bool
	// MaxSteps bounds one assignment.
	MaxSteps int
	// Priority orders the role's requests under contention (Priority* constants).
	Priority int
	// Skills names skills the role is meant to have preloaded (informational).
	Skills []string
	// Pin is the role's instructions, cleaned.
	Pin string
	// PinTokens is Pin's size as measured by Opts.Est.
	PinTokens int

	Scope Scope
	// Path is the file as shown to the user: relative to the project root or "~/...".
	Path string
	// Hash identifies the pin.
	Hash core.Hash
}

// Role has exactly the fields of swarm.Role, in the same order, so that
// swarm.Role(def.ToRole()) is a valid conversion without this package importing
// the swarm.
type Role struct {
	Name string
	// Short is the id prefix ("be" gives be-1, be-2, ...).
	Short string
	// Pin is the role-context text.
	Pin string
	// ReadOnly roles may not write files or run mutating commands.
	ReadOnly bool
	// Priority orders this role's requests under contention.
	Priority int
	// MaxSteps bounds one assignment.
	MaxSteps int
}

// ToRole returns the definition as a swarm role.
func (d Def) ToRole() Role {
	return Role{Name: d.Name, Short: d.Short, Pin: d.Pin, ReadOnly: d.ReadOnly, Priority: d.Priority, MaxSteps: d.MaxSteps}
}

// knownKeys are frontmatter fields that other harnesses define and this loader
// recognises without acting on them.
var knownKeys = []string{
	"mcpServers", "memory", "background", "omitClaudeMd", "effort", "isolation", "color", "initialPrompt",
	"experimental", "kind", "temperature", "timeout_mins", "version", "license",
}

// Load reads the definitions under opts, sorted by name. It never fails as a
// whole: a definition that cannot be loaded is skipped and reported, and the
// rest are returned. The warnings are deterministic and in discovery order.
func Load(opts Opts) ([]Def, []Warning) {
	srcs, warns := mdfile.Sources("agents", mdfile.Layout{Root: opts.Root, Home: opts.Home, TrustProject: opts.TrustProject, Extra: opts.Extra})
	ld := &loader{o: opts, byKey: map[string]int{}, reserved: map[string]bool{}}
	if ld.o.Est == nil {
		ld.o.Est = core.NewBytesEstimator()
	}
	if ld.o.WarnPinTokens <= 0 {
		ld.o.WarnPinTokens = DefaultWarnPinTokens
	}
	if ld.o.MaxPinTokens <= 0 {
		ld.o.MaxPinTokens = DefaultMaxPinTokens
	}
	if ld.o.DefaultMaxSteps <= 0 {
		ld.o.DefaultMaxSteps = DefaultMaxSteps
	}
	for _, r := range opts.Reserved {
		ld.reserved[strings.ToLower(r)] = true
	}
	for _, src := range srcs {
		entries, more, err := mdfile.ReadDirLimited(src.Real, MaxEntries)
		if err != nil {
			warns = append(warns, mdfile.Warnf(src.Label, "", "cannot read the directory: %v", err))
			continue
		}
		if more {
			warns = append(warns, mdfile.Warnf(src.Label, "", "more than %d entries; the rest are ignored", MaxEntries))
		}
		for _, e := range entries {
			warns = append(warns, ld.entry(src, e)...)
		}
	}
	sort.Slice(ld.defs, func(i, j int) bool { return ld.defs[i].Name < ld.defs[j].Name })
	warns = append(warns, assignShorts(ld.defs, opts.ReservedShorts)...)
	return ld.defs, warns
}

type loader struct {
	o        Opts
	defs     []Def
	byKey    map[string]int
	reserved map[string]bool
}

func (l *loader) entry(src mdfile.Source, e os.DirEntry) []Warning {
	fileName := e.Name()
	if strings.HasPrefix(fileName, ".") || !strings.HasSuffix(strings.ToLower(fileName), ".md") {
		return nil
	}
	display := src.Label + "/" + fileName
	path := filepath.Join(src.Real, fileName)
	if e.Type()&fs.ModeSymlink != 0 {
		fi, err := os.Stat(path)
		if err != nil || !fi.Mode().IsRegular() {
			return nil
		}
	} else if !e.Type().IsRegular() {
		return nil
	}
	stem := fileName[:len(fileName)-3]

	parsed, err := mdfile.ReadDoc(path, mdfile.ReadOpts{MaxBytes: MaxFileBytes, Contain: src.Contain})
	switch {
	case errors.Is(err, mdfile.ErrEscapes):
		return []Warning{mdfile.Skipf(display, "", "the file resolves outside its allowed location; skipped")}
	case err != nil:
		return []Warning{mdfile.Skipf(display, "", "%v", err)}
	}

	var warns []Warning
	fail := func(format string, args ...any) []Warning {
		return append(warns, mdfile.Skipf(display, "", format, args...))
	}
	f, err := mdfile.NewFields(parsed.Doc.Meta)
	if err != nil {
		return fail("%v", err)
	}
	fmName, _ := f.Str("name")
	desc, _ := f.Str("description")
	tools, hasTools := f.List("tools")
	disallowed, _ := f.List("disallowedTools")
	model, _ := f.Str("model")
	pmode, _ := f.Str("permissionMode")
	maxTurns, hasTurns := f.Int("maxTurns")
	skillNames, _ := f.List("skills")
	readonly, _ := f.Bool("readonly")
	short, _ := f.Str("short")
	prio, hasPrio := f.Int("priority")
	hasHooks := f.Has("hooks")
	for _, k := range knownKeys {
		f.Has(k)
	}
	if err := f.Err(); err != nil {
		return fail("%v", err)
	}

	name := fmName
	if name == "" {
		name = stem
	}
	if err := mdfile.CheckName(name, nameRules); err != nil {
		return fail("%v", err)
	}
	adv := func(format string, args ...any) { warns = append(warns, mdfile.Warnf(display, name, format, args...)) }
	if src.Namespace != "" {
		name = src.Namespace + ":" + name
	}
	key := strings.ToLower(name)
	if l.reserved[key] {
		return []Warning{mdfile.Skipf(display, name, "the name is reserved for a built-in role")}
	}
	if at, dup := l.byKey[key]; dup {
		return []Warning{mdfile.Skipf(display, name, "shadowed by the definition of the same name in %s", l.defs[at].Path)}
	}
	if len(l.defs) >= MaxDefs {
		return []Warning{mdfile.Skipf(display, name, "too many definitions (the limit is %d)", MaxDefs)}
	}

	pin := mdfile.EscapeFraming(parsed.Doc.Body)
	if strings.TrimSpace(pin) == "" {
		return append(warns, mdfile.Skipf(display, name, "the definition has no instructions"))
	}
	tokens := l.o.Est.Tokens(pin)
	switch {
	case tokens > l.o.MaxPinTokens:
		return append(warns, mdfile.Skipf(display, name, "the instructions are about %d tokens; the limit is %d. They are read on every request of every %s agent", tokens, l.o.MaxPinTokens, name))
	case tokens > l.o.WarnPinTokens:
		adv("the instructions are about %d tokens (above %d); they are read on every request of every %s agent, so every token here is paid many times over", tokens, l.o.WarnPinTokens, name)
	}
	if parsed.Truncated {
		adv("the file is larger than %d KB; the instructions were truncated", MaxFileBytes>>10)
	}

	if desc == "" {
		desc = firstLine(parsed.Doc.Body)
	}
	desc = mdfile.OneLine(mdfile.EscapeFraming(desc), maxDesc)

	tools, badTools := mdfile.FilterTools(tools)
	if len(badTools) > 0 {
		adv("ignored malformed tools entries: %s", quoted(badTools))
	}
	for _, t := range tools {
		if t == "*" { // "every tool", spelled out: the same as no allowlist
			tools, hasTools = nil, false
			break
		}
	}
	if hasTools && len(tools) == 0 {
		// An allowlist whose entries were all malformed must not silently become
		// "no restriction": that would widen the role.
		return append(warns, mdfile.Skipf(display, name, "the tools list has no usable entry; refusing to treat it as unrestricted"))
	}
	var dis []string
	for _, d := range disallowed {
		if _, err := perm.ParseRule(perm.Deny, d); err != nil || !mdfile.ValidTool(d) {
			adv("ignored malformed disallowedTools entry %q", mdfile.OneLine(d, 40))
			continue
		}
		dis = append(dis, d)
	}
	if model != "" && (strings.EqualFold(model, "inherit") || !plainToken(model)) {
		if !strings.EqualFold(model, "inherit") {
			adv("model %q is not a valid model name; ignored", mdfile.OneLine(model, 40))
		}
		model = ""
	}
	mode, ok := normalizeMode(pmode)
	if !ok {
		adv("permissionMode %q cannot loosen the session's permissions and is ignored", mdfile.OneLine(pmode, 30))
	}
	steps := l.o.DefaultMaxSteps
	if hasTurns {
		switch {
		case maxTurns < 1:
			adv("maxTurns %d is not positive; the default of %d is used", maxTurns, steps)
		case maxTurns > MaxStepsCap:
			adv("maxTurns %d is above the cap of %d; the cap is used", maxTurns, MaxStepsCap)
			steps = MaxStepsCap
		default:
			steps = maxTurns
		}
	}
	priority := PriorityWorker
	if hasPrio {
		if prio < PriorityInteractive || prio > PriorityBackground {
			adv("priority %d is not %d, %d or %d; the worker priority is used", prio, PriorityInteractive, PriorityWorker, PriorityBackground)
		} else {
			priority = prio
		}
	}
	if short != "" && !validShort(short) {
		adv("short %q is not 2-%d lower-case letters or digits starting with a letter; one is derived", mdfile.OneLine(short, 20), maxShort)
		short = ""
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

	var skillsList []string
	for _, s := range skillNames {
		if mdfile.CheckName(s, mdfile.NameRules{Max: 64, Upper: true, Dot: true, Underscore: true}) == nil {
			skillsList = append(skillsList, s)
		}
	}

	d := Def{
		Name: name, Description: desc, Short: short, Tools: tools, DisallowedTools: dis, Model: model,
		PermissionMode: mode, MaxSteps: steps, Priority: priority, Skills: skillsList,
		Pin: pin, PinTokens: tokens, Scope: src.Scope, Path: display, Hash: core.HashString(pin),
	}
	d.ReadOnly = readonly || mode == "plan" || (hasTools && allReadOnly(tools))
	l.byKey[key] = len(l.defs)
	l.defs = append(l.defs, d)
	return warns
}

// normalizeMode maps a Claude Code permissionMode to a Sleipnir one. ok is
// false for values that would loosen permissions; they map to "".
func normalizeMode(s string) (mode string, ok bool) {
	switch strings.ToLower(strings.NewReplacer("-", "", "_", "").Replace(s)) {
	case "", "default":
		return "", true
	case "acceptedits":
		return string(perm.ModeAcceptEdits), true
	case "plan":
		return string(perm.ModePlan), true
	}
	return "", false
}

func firstLine(body string) string {
	for _, ln := range strings.Split(body, "\n") {
		if ln = strings.TrimSpace(strings.TrimLeft(strings.TrimSpace(ln), "# ")); ln != "" {
			return ln
		}
	}
	return ""
}

func quoted(in []string) string {
	out := make([]string, len(in))
	for i, s := range in {
		out[i] = fmt.Sprintf("%q", mdfile.OneLine(s, 40))
	}
	return strings.Join(out, ", ")
}

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
