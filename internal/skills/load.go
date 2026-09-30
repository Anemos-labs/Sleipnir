package skills

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"

	"github.com/reee344/sleipnir/internal/core"
	"github.com/reee344/sleipnir/internal/skills/mdfile"
)

const (
	// maxListedFiles bounds the supporting-file list of one skill.
	maxListedFiles = 200
	// maxFileScan bounds the directory entries examined to build that list.
	maxFileScan = 2000
	// maxFileDepth bounds how deep below the skill directory files are listed.
	maxFileDepth = 4
)

// Loaded is a skill ready to be put in front of the model.
type Loaded struct {
	Name        string
	Description string
	Scope       Scope
	// Dir is the skill's base directory as an absolute path, so that the model
	// can read supporting files with its file tools.
	Dir string
	// Header is the line that names the base directory.
	Header string
	// Body is the skill text with the arguments substituted: $ARGUMENTS,
	// $ARGUMENTS[N] and $1..$9, and ${SLEIPNIR_SKILL_DIR} / ${CLAUDE_SKILL_DIR}
	// for the base directory. When the body has no placeholder and arguments
	// were given, they follow it as "ARGUMENTS: ...".
	Body string
	// Files are the supporting files, as sorted slash paths relative to Dir.
	Files []string
	// FilesTruncated says the skill has more files than are listed.
	FilesTruncated bool
	// AllowedTools, Model, Context and Agent are the skill's frontmatter, as
	// discovered. AllowedTools are requests for the permission engine, not grants.
	AllowedTools []string
	Model        string
	Context      string
	Agent        string
	// Hash identifies the unsubstituted body, for logs and replay.
	Hash core.Hash
	// Truncated says the SKILL.md was cut at the size cap.
	Truncated bool
}

// Text renders everything the model should see: the header, the body and the
// list of supporting files.
func (l Loaded) Text() string {
	var b strings.Builder
	b.WriteString(l.Header)
	b.WriteString("\n\n")
	b.WriteString(l.Body)
	if len(l.Files) > 0 {
		b.WriteString("\n\nSupporting files (relative to the base directory; read them when needed):\n")
		for _, f := range l.Files {
			b.WriteString("- ")
			b.WriteString(f)
			b.WriteByte('\n')
		}
		if l.FilesTruncated {
			b.WriteString("- ... and more\n")
		}
	}
	return strings.TrimRight(b.String(), "\n")
}

// Load returns a skill by name, with args substituted into its body. It
// applies no invocation policy: use LoadForModel for the model-facing tool and
// LoadForUser for a slash command.
func (c *Catalog) Load(name, args string) (Loaded, error) {
	return c.load(name, args, anyone)
}

// LoadForModel is Load for the model-facing Skill tool: a skill with
// DisableModelInvocation is refused, and is not named in the error.
func (c *Catalog) LoadForModel(name, args string) (Loaded, error) {
	return c.load(name, args, forModel)
}

// LoadForUser is Load for a slash command typed by the user: a skill with
// UserInvocable false is refused.
func (c *Catalog) LoadForUser(name, args string) (Loaded, error) {
	return c.load(name, args, forUser)
}

// Invocation policies. A policy returns nil to allow a skill, errUnknown to
// hide it (the caller reports it as nonexistent and leaves it out of any list)
// or another error to explain a refusal.
func anyone(*Skill) error { return nil }

func forModel(s *Skill) error {
	if s.DisableModelInvocation {
		return errUnknown
	}
	return nil
}

func forUser(s *Skill) error {
	if !s.UserInvocable {
		return fmt.Errorf("skill %q can only be used by the model, not invoked directly", s.Name)
	}
	return nil
}

var errUnknown = errors.New("unknown skill")

func (c *Catalog) load(name, args string, allow func(*Skill) error) (Loaded, error) {
	sk, err := c.find(name, allow)
	if err != nil {
		return Loaded{}, err
	}
	args, err = mdfile.CleanArgs(args)
	if err != nil {
		return Loaded{}, fmt.Errorf("skill %q: %v", sk.Name, err)
	}
	// The base directory goes in first, and the arguments after: an argument that
	// happens to spell a variable name must stay text.
	body := strings.NewReplacer("${SLEIPNIR_SKILL_DIR}", sk.Dir, "${CLAUDE_SKILL_DIR}", sk.Dir).Replace(sk.body)
	body, used := mdfile.Substitute(body, args, mdfile.ArgsText)
	if !used && args != "" {
		body += "\n\nARGUMENTS: " + args
	}
	files, more := listFiles(sk.realDir)
	return Loaded{
		Name: sk.Name, Description: sk.Description, Scope: sk.Scope, Dir: sk.Dir,
		Header: "Base directory for this skill: " + sk.Dir,
		Body:   body, Files: files, FilesTruncated: more,
		AllowedTools: append([]string(nil), sk.AllowedTools...),
		Model:        sk.Model, Context: sk.Context, Agent: sk.Agent,
		Hash: sk.Hash, Truncated: sk.Truncated,
	}, nil
}

// find resolves a name the way people type it: case-insensitively, with an
// optional leading slash, and, for a skill from a namespaced plugin, without
// the namespace when that is unambiguous.
func (c *Catalog) find(name string, allow func(*Skill) error) (*Skill, error) {
	name = strings.TrimPrefix(strings.TrimSpace(name), "/")
	if c != nil && name != "" {
		if i, ok := c.byKey[strings.ToLower(name)]; ok {
			sk := &c.skills[i]
			switch err := allow(sk); {
			case err == nil:
				return sk, nil
			case !errors.Is(err, errUnknown):
				return nil, err
			}
		} else if !strings.Contains(name, ":") {
			var match *Skill
			n := 0
			for i := range c.skills {
				s := &c.skills[i]
				if strings.HasSuffix(strings.ToLower(s.Name), ":"+strings.ToLower(name)) && allow(s) == nil {
					match, n = s, n+1
				}
			}
			if n == 1 {
				return match, nil
			}
			if n > 1 {
				return nil, fmt.Errorf("skill name %q is ambiguous; use the full name (%s)", mdfile.OneLine(name, 40), strings.Join(c.candidates(name, allow), ", "))
			}
		}
	}
	return nil, c.unknown(name, allow)
}

func (c *Catalog) candidates(name string, allow func(*Skill) error) []string {
	var out []string
	for i := range c.skills {
		if strings.HasSuffix(strings.ToLower(c.skills[i].Name), ":"+strings.ToLower(name)) && allow(&c.skills[i]) == nil {
			out = append(out, c.skills[i].Name)
		}
	}
	return out
}

// unknown builds the error for a missing skill. It lists what exists (limited,
// and only what the caller may use) because the reader is usually a model that
// guessed a name.
func (c *Catalog) unknown(name string, allow func(*Skill) error) error {
	var names []string
	if c != nil {
		for i := range c.skills {
			if allow(&c.skills[i]) == nil {
				names = append(names, c.skills[i].Name)
			}
		}
	}
	const show = 30
	list := "there are no skills"
	if len(names) > 0 {
		more := ""
		if len(names) > show {
			names, more = names[:show], fmt.Sprintf(", and %d more", len(names)-show)
		}
		list = "available skills: " + strings.Join(names, ", ") + more
	}
	return fmt.Errorf("unknown skill %q; %s", mdfile.OneLine(mdfile.EscapeTags(name), 60), list)
}

// listFiles lists the supporting files of a skill directory: regular files
// below it, as sorted slash paths, never SKILL.md itself, never dot files, and
// never anything whose real path is outside the directory (a symlink to a file
// elsewhere is not listed; a symlinked directory is not entered). The scan is
// bounded in depth, entries and results.
func listFiles(realDir string) (files []string, more bool) {
	scanned := 0
	var walk func(dir, rel string, depth int)
	walk = func(dir, rel string, depth int) {
		entries, _, err := mdfile.ReadDirLimited(dir, maxFileScan)
		if err != nil {
			return
		}
		for _, e := range entries {
			scanned++
			if scanned > maxFileScan || len(files) >= maxListedFiles {
				more = true
				return
			}
			name := e.Name()
			if strings.HasPrefix(name, ".") {
				continue
			}
			p := filepath.Join(dir, name)
			r := path.Join(rel, name)
			switch t := e.Type(); {
			case t.IsDir():
				if depth < maxFileDepth {
					walk(p, r, depth+1)
				}
			case t.IsRegular():
				if rel == "" && strings.EqualFold(name, FileName) {
					continue
				}
				files = append(files, r)
			case t&fs.ModeSymlink != 0:
				real, err := filepath.EvalSymlinks(p)
				if err != nil || !mdfile.Within(realDir, real) {
					continue
				}
				isSkillFile := rel == "" && strings.EqualFold(name, FileName)
				if fi, err := os.Stat(real); err == nil && fi.Mode().IsRegular() && !isSkillFile {
					files = append(files, r)
				}
			}
		}
	}
	walk(realDir, "", 0)
	sort.Strings(files)
	return files, more
}

// SupportFile is a supporting file read from a skill.
type SupportFile struct {
	// Path is the requested path, cleaned, relative to the skill directory.
	Path string
	// Text is the file's content, normalised and with hidden characters removed.
	Text string
	// Truncated says the file exceeded the size cap.
	Truncated bool
}

// ReadFile reads one supporting file of a skill by its path relative to the
// skill directory, for the model-facing tool: like LoadForModel it refuses a
// skill with DisableModelInvocation. The path must be relative, use forward
// slashes and stay inside the directory; the file's real path (symlinks
// resolved) must too. A file that is not text, or is not a regular file, is an
// error.
func (c *Catalog) ReadFile(name, rel string) (SupportFile, error) {
	sk, err := c.find(name, forModel)
	if err != nil {
		return SupportFile{}, err
	}
	clean, err := cleanRel(rel)
	if err != nil {
		return SupportFile{}, fmt.Errorf("skill %q: %v", sk.Name, err)
	}
	f, err := mdfile.ReadFile(filepath.Join(sk.realDir, filepath.FromSlash(clean)), mdfile.ReadOpts{MaxBytes: maxSupportFileLen, Contain: sk.realDir})
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return SupportFile{}, fmt.Errorf("skill %q has no file %q; the supporting files are listed when the skill is loaded", sk.Name, clean)
	case errors.Is(err, mdfile.ErrEscapes):
		return SupportFile{}, fmt.Errorf("skill %q: %q resolves outside the skill directory and cannot be read", sk.Name, clean)
	case errors.Is(err, mdfile.ErrNotRegular):
		return SupportFile{}, fmt.Errorf("skill %q: %q is not a regular file", sk.Name, clean)
	case errors.Is(err, mdfile.ErrBinary):
		return SupportFile{}, fmt.Errorf("skill %q: %q is a binary file", sk.Name, clean)
	case err != nil:
		return SupportFile{}, fmt.Errorf("skill %q: cannot read %q: %v", sk.Name, clean, err)
	}
	text, _ := mdfile.Sanitize(mdfile.Normalize(f.Data))
	return SupportFile{Path: clean, Text: text, Truncated: f.Truncated}, nil
}

// cleanRel validates a supporting-file path and returns it in cleaned slash
// form. It is deliberately stricter than the file system: the path comes from a
// model, and "..", absolute paths, drive letters, backslashes and NULs are
// refused outright rather than resolved.
func cleanRel(rel string) (string, error) {
	switch {
	case rel == "":
		return "", errors.New("the file path is empty")
	case strings.ContainsRune(rel, 0):
		return "", errors.New("the file path contains a NUL byte")
	case strings.Contains(rel, "\\"):
		return "", errors.New("use forward slashes in file paths")
	case strings.HasPrefix(rel, "/") || strings.HasPrefix(rel, "~") || (len(rel) >= 2 && rel[1] == ':'):
		return "", errors.New("the file path must be relative to the skill directory")
	}
	clean := path.Clean(rel)
	if clean == "." || clean == ".." || strings.HasPrefix(clean, "../") {
		return "", errors.New("the file path must stay inside the skill directory")
	}
	return clean, nil
}
