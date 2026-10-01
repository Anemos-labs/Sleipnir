package skills

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/anemos-labs/sleipnir/internal/core"
	"github.com/anemos-labs/sleipnir/internal/skills/mdfile"
)

// Aliases so that callers deal with one vocabulary and need not import mdfile.
type (
	// Warning is a problem found while discovering skills; see mdfile.Warning.
	Warning = mdfile.Warning
	// Scope says who a skill came from.
	Scope = mdfile.Scope
	// Extra is a caller-supplied directory of skills (a plugin's skills/).
	Extra = mdfile.Extra
)

// Scopes.
const (
	ScopeProject = mdfile.ScopeProject
	ScopeUser    = mdfile.ScopeUser
	ScopeExtra   = mdfile.ScopeExtra
)

const (
	// FileName is the definition file of a skill directory.
	FileName = "SKILL.md"

	// MaxSkills bounds the catalog: a directory of thousands of skills is a
	// denial of service on the shared prompt layer, not a workflow.
	MaxSkills = 256
	// MaxEntriesPerSource bounds how many directory entries are examined in one
	// skills directory.
	MaxEntriesPerSource = 1024
	// DefaultMaxFileBytes caps SKILL.md. The body is read into memory once at
	// discovery and put in front of a model whole when loaded; Claude Code's own
	// guidance is under 500 lines.
	DefaultMaxFileBytes = 64 << 10

	maxNameLen        = 64
	maxDescription    = 1024 // runes kept per description
	maxHint           = 120
	maxSupportFileLen = 256 << 10
)

var nameRules = mdfile.NameRules{Max: maxNameLen, Upper: true, Dot: true, Underscore: true}

// Opts says where to look.
type Opts struct {
	// Root is the project root; empty means no project skills.
	Root string
	// Home is the user's home directory; empty means the current user's.
	Home string
	// TrustProject allows the repository's own skills to be read. Leave it false
	// for a repository the user has not decided to trust.
	TrustProject bool
	// Extra lists further skill directories, such as installed plugins.
	Extra []Extra
	// MaxFileBytes caps SKILL.md; 0 means DefaultMaxFileBytes.
	MaxFileBytes int64
}

// Skill is one discovered skill. Its body and supporting files stay on disk
// until loaded.
type Skill struct {
	// Name identifies the skill: the frontmatter name (the directory name when
	// absent), prefixed "namespace:" for skills from a namespaced Extra.
	Name string
	// Description is one line: what the skill does and when to use it. It is the
	// only text of a skill that is always in the model's context.
	Description string
	// WhenToUse is optional extra trigger guidance, listed after Description.
	WhenToUse string
	// AllowedTools are the tool rules the skill asks to pre-approve while it runs,
	// as written ("Bash(git status:*)"). Reported, never granted here.
	AllowedTools []string
	// DisableModelInvocation hides the skill from the model: only the user can
	// invoke it.
	DisableModelInvocation bool
	// UserInvocable is false for skills only the model may invoke (true by
	// default).
	UserInvocable bool
	ArgumentHint  string
	// Model names the model to run the skill with ("" means the current one).
	Model string
	// Context is "fork" to run the skill in a subagent, or "".
	Context string
	// Agent names the subagent role to use with Context "fork".
	Agent string

	Scope Scope
	// Dir is the skill directory as spelled (absolute).
	Dir string
	// Path is the SKILL.md as shown to the user: relative to the project root or
	// "~/...".
	Path string
	// Hash identifies the body: equal hashes mean equal text.
	Hash core.Hash
	// Truncated says the file exceeded the size cap and the body was cut.
	Truncated bool

	realDir string
	body    string
	rank    int // index of the source directory: lower means higher precedence
}

// Summary is Description followed by WhenToUse, the text the listing shows.
func (s Skill) Summary() string {
	switch {
	case s.WhenToUse == "":
		return s.Description
	case s.Description == "":
		return s.WhenToUse
	}
	return s.Description + " " + s.WhenToUse
}

// Catalog is the set of discovered skills. It is immutable and safe for
// concurrent use.
type Catalog struct {
	skills []Skill // sorted by Name
	byKey  map[string]int
}

// Len is the number of skills.
func (c *Catalog) Len() int {
	if c == nil {
		return 0
	}
	return len(c.skills)
}

// Skills returns the skills sorted by name.
func (c *Catalog) Skills() []Skill {
	if c == nil {
		return nil
	}
	return append([]Skill(nil), c.skills...)
}

// Get returns the skill with exactly this name (case-insensitively).
func (c *Catalog) Get(name string) (Skill, bool) {
	if c == nil {
		return Skill{}, false
	}
	i, ok := c.byKey[strings.ToLower(name)]
	if !ok {
		return Skill{}, false
	}
	return c.skills[i], true
}

// knownKeys are frontmatter fields that other harnesses define and this loader
// deliberately does not act on; naming them keeps them out of the "unknown key"
// warning. "hooks" is not here: it does something in Claude Code, so ignoring it
// is reported.
var knownKeys = []string{
	"license", "compatibility", "metadata", "version", "effort", "paths", "shell", "disallowed-tools",
	"arguments", "background", "author", "tags", "category",
}

// Discover finds the skills under opts. It never fails as a whole: a skill that
// cannot be loaded is skipped and reported, and the rest are returned. The
// warnings are in discovery order and are deterministic.
func Discover(opts Opts) (*Catalog, []Warning) {
	srcs, warns := mdfile.Sources("skills", mdfile.Layout{Root: opts.Root, Home: opts.Home, TrustProject: opts.TrustProject, Extra: opts.Extra})
	limit := opts.MaxFileBytes
	if limit <= 0 {
		limit = DefaultMaxFileBytes
	}
	d := &discovery{limit: limit, byKey: map[string]int{}}
	for rank, src := range srcs {
		entries, more, err := mdfile.ReadDirLimited(src.Real, MaxEntriesPerSource)
		if err != nil {
			warns = append(warns, mdfile.Warnf(src.Label, "", "cannot read the directory: %v", err))
			continue
		}
		if more {
			warns = append(warns, mdfile.Warnf(src.Label, "", "more than %d entries; the rest are ignored", MaxEntriesPerSource))
		}
		for _, e := range entries {
			if strings.HasPrefix(e.Name(), ".") {
				continue
			}
			warns = append(warns, d.load(src, rank, e.Name())...)
		}
	}
	sort.Slice(d.skills, func(i, j int) bool { return d.skills[i].Name < d.skills[j].Name })
	c := &Catalog{skills: d.skills, byKey: make(map[string]int, len(d.skills))}
	for i, s := range c.skills {
		c.byKey[strings.ToLower(s.Name)] = i
	}
	return c, warns
}

type discovery struct {
	limit  int64
	skills []Skill
	byKey  map[string]int // lower-cased name -> index in skills
}

// load reads one entry of a skills directory.
func (d *discovery) load(src mdfile.Source, rank int, entry string) []Warning {
	display := src.Label + "/" + entry
	realDir, err := filepath.EvalSymlinks(filepath.Join(src.Real, entry))
	if err != nil {
		return []Warning{mdfile.Skipf(display, "", "cannot resolve the directory: %v", err)}
	}
	if fi, err := os.Stat(realDir); err != nil || !fi.IsDir() {
		return nil // a stray file in a skills directory is not a skill
	}
	if src.Contain != "" && !mdfile.Within(src.Contain, realDir) {
		return []Warning{mdfile.Skipf(display, "", "the directory resolves outside its allowed location; skipped")}
	}

	parsed, err := mdfile.ReadDoc(filepath.Join(realDir, FileName), mdfile.ReadOpts{MaxBytes: d.limit, Contain: realDir})
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return nil // a directory without SKILL.md is not a skill
	case errors.Is(err, mdfile.ErrEscapes):
		return []Warning{mdfile.Skipf(display+"/"+FileName, "", "%s resolves outside the skill directory; skipped", FileName)}
	case err != nil:
		return []Warning{mdfile.Skipf(display+"/"+FileName, "", "%v", err)}
	}

	path := display + "/" + FileName
	var warns []Warning
	adv := func(name, format string, args ...any) {
		warns = append(warns, mdfile.Warnf(path, name, format, args...))
	}

	f, err := mdfile.NewFields(parsed.Doc.Meta)
	if err != nil {
		return []Warning{mdfile.Skipf(path, "", "%v", err)}
	}
	fmName, _ := f.Str("name")
	desc, _ := f.Str("description")
	when, _ := f.Str("when_to_use")
	tools, _ := f.List("allowed-tools")
	disable, _ := f.Bool("disable-model-invocation")
	userInv, hasUI := f.Bool("user-invocable")
	if !hasUI {
		userInv = true
	}
	hint, _ := f.Str("argument-hint")
	model, _ := f.Str("model")
	ctxMode, _ := f.Str("context")
	agentName, _ := f.Str("agent")
	hasHooks := f.Has("hooks")
	for _, k := range knownKeys {
		f.Has(k)
	}
	if err := f.Err(); err != nil {
		return []Warning{mdfile.Skipf(path, "", "%v", err)}
	}

	name := fmName
	if name == "" {
		name = entry
	}
	if err := mdfile.CheckName(name, nameRules); err != nil {
		return []Warning{mdfile.Skipf(path, "", "%v", err)}
	}
	if fmName != "" && fmName != entry {
		adv(name, "the name differs from the directory name %q; the name is used", entry)
	}
	if src.Namespace != "" {
		name = src.Namespace + ":" + name
	}
	key := strings.ToLower(name)
	if at, dup := d.byKey[key]; dup {
		if d.skills[at].realDir == realDir {
			return nil // the same directory reached through two names
		}
		return []Warning{mdfile.Skipf(path, name, "shadowed by the skill of the same name in %s", d.skills[at].Path)}
	}
	if len(d.skills) >= MaxSkills {
		return []Warning{mdfile.Skipf(path, name, "too many skills (the limit is %d)", MaxSkills)}
	}

	if desc == "" && when == "" {
		desc = firstParagraph(parsed.Doc.Body)
		if desc == "" {
			adv(name, "no description: the model cannot tell when to use this skill")
		}
	}
	desc, cut := oneLine(desc, maxDescription)
	when, cut2 := oneLine(when, maxDescription)
	if cut || cut2 {
		adv(name, "the description is longer than %d characters and was truncated", maxDescription)
	}

	tools, badTools := mdfile.FilterTools(tools)
	if len(badTools) > 0 {
		adv(name, "ignored malformed allowed-tools entries: %s", strings.Join(quoteAll(badTools), ", "))
	}
	if ctxMode != "" && !strings.EqualFold(ctxMode, "fork") {
		adv(name, "context %q is not supported (only \"fork\"); ignored", ctxMode)
		ctxMode = ""
	}
	ctxMode = strings.ToLower(ctxMode)
	if agentName != "" && mdfile.CheckName(agentName, nameRules) != nil {
		adv(name, "agent %q is not a valid name; ignored", agentName)
		agentName = ""
	}
	if model != "" && !plainToken(model) {
		adv(name, "model %q is not a valid model name; ignored", model)
		model = ""
	}
	hint, _ = oneLine(hint, maxHint)
	if hasHooks {
		adv(name, "the hooks frontmatter field is not supported and is ignored")
	}
	if un := f.Unused(); len(un) > 0 {
		adv(name, "unknown frontmatter field(s) ignored: %s", strings.Join(un, ", "))
	}
	for _, n := range parsed.Doc.Notes {
		adv(name, "%s", n)
	}
	if parsed.Hidden.Suspicious() {
		adv(name, "removed %d hidden characters (bidirectional controls, tag characters or zero-width runs); check the file", parsed.Hidden.Total())
	}
	if parsed.Truncated {
		adv(name, "the file is larger than %d KB; the body was truncated", d.limit>>10)
	}

	d.byKey[key] = len(d.skills)
	d.skills = append(d.skills, Skill{
		Name: name, Description: desc, WhenToUse: when, AllowedTools: tools,
		DisableModelInvocation: disable, UserInvocable: userInv, ArgumentHint: hint,
		Model: model, Context: ctxMode, Agent: agentName,
		Scope: src.Scope, Dir: filepath.Join(src.Dir, entry), Path: path,
		Hash: core.HashString(parsed.Doc.Body), Truncated: parsed.Truncated,
		realDir: realDir, body: parsed.Doc.Body, rank: rank,
	})
	return warns
}

// firstParagraph is the fallback description: the first paragraph of the body,
// without markdown heading marks.
func firstParagraph(body string) string {
	for _, para := range strings.Split(body, "\n\n") {
		para = strings.TrimSpace(strings.TrimLeft(strings.TrimSpace(para), "# "))
		if para != "" {
			return para
		}
	}
	return ""
}

// oneLine collapses s to one line of at most maxRunes runes; cut reports
// truncation.
func oneLine(s string, maxRunes int) (out string, cut bool) {
	full := mdfile.OneLine(s, 0)
	out = mdfile.OneLine(s, maxRunes)
	return out, out != full
}

func quoteAll(in []string) []string {
	out := make([]string, len(in))
	for i, s := range in {
		out[i] = "\"" + mdfile.OneLine(s, 40) + "\""
	}
	return out
}

// plainToken reports whether s can be a model reference: no white space or
// control characters, and short.
func plainToken(s string) bool {
	if s == "" || len(s) > 100 {
		return false
	}
	for _, r := range s {
		if r <= ' ' || r == 0x7F || r > 0x7E {
			return false
		}
	}
	return true
}
