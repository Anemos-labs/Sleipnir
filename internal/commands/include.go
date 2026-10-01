package commands

import (
	"errors"
	"fmt"
	"io/fs"
	"path"
	"path/filepath"
	"strings"

	"github.com/anemos-labs/sleipnir/internal/skills/mdfile"
)

// includeSet collects the files an expansion refers to. They are appended after
// the prompt rather than spliced into it, so the sentence that mentions a file
// still reads as written, and so that file contents can never be mistaken for
// part of the template.
type includeSet struct {
	items []includedFile
	seen  map[string]bool // real paths already included
}

type includedFile struct {
	display string // path relative to the project root, slash separated
	text    string
}

// section renders the appended block ("" when nothing was included).
func (s *includeSet) section() string {
	if len(s.items) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString("\n\nReferenced files (contents inserted by the harness):")
	for _, f := range s.items {
		fence := fenceFor(f.text)
		fmt.Fprintf(&b, "\n\nContents of %s:\n%s\n%s\n%s", f.display, fence, f.text, fence)
	}
	return b.String()
}

// fenceFor returns a backtick fence longer than any run of backticks in text,
// so that no content can close the block early.
func fenceFor(text string) string {
	longest, run := 0, 0
	for i := 0; i < len(text); i++ {
		if text[i] == '`' {
			run++
			longest = max(longest, run)
		} else {
			run = 0
		}
	}
	return strings.Repeat("`", max(3, longest+1))
}

// include reads the file an "@spec" mention names, if it may. Every refusal is
// a notice and leaves the mention as plain text: a template that mentions
// "@alice" or a file that has since been deleted must still expand.
func (ex *expansion) include(spec string) {
	spec = strings.TrimSpace(spec)
	if spec == "" || strings.ContainsRune(spec, 0) {
		return
	}
	root := ex.r.root
	pathy := strings.ContainsAny(spec, "/.\\")
	refuse := func(format string, args ...any) {
		ex.notice("@%s: not included: %s", mdfile.OneLine(spec, 60), fmt.Sprintf(format, args...))
	}
	switch {
	case root == "":
		if pathy {
			refuse("there is no project root to read files from")
		}
		return
	case strings.HasPrefix(spec, "~"):
		refuse("only files inside the project can be included")
		return
	}

	native := filepath.FromSlash(spec)
	cand := filepath.Join(root, native)
	if filepath.IsAbs(native) {
		cand = filepath.Clean(native)
	}
	real, err := filepath.EvalSymlinks(cand)
	if err != nil {
		if pathy || !errors.Is(err, fs.ErrNotExist) {
			refuse("no such file")
		}
		return
	}
	if !mdfile.Within(root, real) {
		refuse("it is outside the project")
		return
	}
	rel, err := filepath.Rel(root, real)
	if err != nil {
		return
	}
	display := filepath.ToSlash(rel)
	if why := sensitivePath(display); why != "" {
		refuse("%s", why)
		return
	}
	if ex.includes.seen[real] {
		return
	}
	if ex.r.opts.AllowRead != nil {
		if err := ex.r.opts.AllowRead(ex.ctx, real); err != nil {
			refuse("%s", mdfile.OneLine(err.Error(), 120))
			return
		}
	}
	if len(ex.includes.items) >= ex.r.opts.MaxIncludes {
		refuse("more than %d files are referenced", ex.r.opts.MaxIncludes)
		return
	}
	f, err := mdfile.ReadFile(real, mdfile.ReadOpts{MaxBytes: ex.r.opts.MaxIncludeBytes, Contain: root})
	switch {
	case errors.Is(err, mdfile.ErrNotRegular):
		refuse("it is not a regular file")
		return
	case errors.Is(err, mdfile.ErrBinary):
		refuse("it is a binary file")
		return
	case err != nil:
		refuse("%s", mdfile.OneLine(err.Error(), 80))
		return
	}
	text, _ := mdfile.Sanitize(mdfile.Normalize(f.Data))
	text = strings.TrimRight(text, "\n")
	if f.Truncated {
		if i := strings.LastIndexByte(text, '\n'); i > 0 && i >= len(text)-1024 {
			text = text[:i]
		}
		text += "\n[... truncated: the file is larger than the include limit ...]"
		ex.notice("@%s: truncated to %d KB", display, ex.r.opts.MaxIncludeBytes>>10)
	}
	if ex.includes.seen == nil {
		ex.includes.seen = map[string]bool{}
	}
	ex.includes.seen[real] = true
	ex.includes.items = append(ex.includes.items, includedFile{display: display, text: text})
}

// sensitivePath returns why a project-relative slash path must not be included
// automatically, or "". It is a backstop for the obvious cases (credential
// files, repository internals) so that "@.env" in a template or an argument does
// not put secrets in a prompt even where the caller supplied no AllowRead; the
// permission engine's own policy is the real one.
func sensitivePath(rel string) string {
	segs := strings.Split(strings.ToLower(rel), "/")
	for _, s := range segs[:len(segs)-1] {
		switch s {
		case ".git", ".ssh", ".aws", ".gnupg", ".kube", ".docker":
			return "it is inside a credentials or repository-internal directory"
		}
	}
	base := segs[len(segs)-1]
	switch {
	case base == ".env" || strings.HasPrefix(base, ".env.") && !strings.HasSuffix(base, ".example") && !strings.HasSuffix(base, ".sample") && !strings.HasSuffix(base, ".template"):
		return "it is an environment file that may hold secrets"
	case base == ".git" || base == ".npmrc" || base == ".netrc" || base == ".pypirc" || base == ".git-credentials" || base == ".htpasswd":
		return "it is a credentials file"
	case strings.HasPrefix(base, "id_rsa") || strings.HasPrefix(base, "id_dsa") || strings.HasPrefix(base, "id_ecdsa") || strings.HasPrefix(base, "id_ed25519"):
		if strings.HasSuffix(base, ".pub") {
			return ""
		}
		return "it looks like a private key"
	}
	switch path.Ext(base) {
	case ".pem", ".key", ".p12", ".pfx", ".jks", ".keystore", ".kdbx", ".ppk":
		return "it looks like a private key or keystore"
	}
	return ""
}
