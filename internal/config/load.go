package config

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
)

// LoadOpts says where configuration comes from.
type LoadOpts struct {
	// Cwd is where root discovery starts (default: the process working
	// directory). Ignored when Root is set.
	Cwd string
	// Root is the project root. Empty means FindRoot(Cwd).
	Root string
	// Home is the user's home directory, whose .sleipnir/config.json is the user
	// layer. Empty means the current user's home (os.UserHomeDir). Tests set it
	// so they never read the real one.
	Home string
	// Overrides is the highest-precedence layer, shaped like the config file:
	// {"models": {"default": "openrouter/x"}}. Command-line flags go here.
	Overrides map[string]any
	// Environ returns the environment as KEY=VALUE strings (default os.Environ).
	Environ func() []string
	// UntrustedProject drops security-sensitive settings (see SensitivePaths)
	// found in project-level files instead of applying them. They are still
	// reported in Report.ProjectRisks.
	UntrustedProject bool
}

// LayerInfo describes one configuration layer that was considered.
type LayerInfo struct {
	Kind   string   `json:"kind"`   // defaults, user, project, local, env, overrides
	Source string   `json:"source"` // file path, or the kind for non-files
	Found  bool     `json:"found"`  // the file exists / the layer set something
	Keys   []string `json:"keys,omitempty"`
}

// Report says where the loaded configuration came from.
type Report struct {
	Root string `json:"root,omitempty"`
	Home string `json:"home,omitempty"`
	// Layers lists every layer considered, lowest precedence first.
	Layers []LayerInfo `json:"layers"`
	// Sources maps each top-level key that some layer set to the
	// highest-precedence source that supplied (part of) it.
	Sources map[string]string `json:"sources"`
	// Origins is the same at leaf granularity: field path -> source.
	Origins  map[string]string `json:"origins"`
	Warnings []Issue           `json:"warnings,omitempty"`
	// ProjectRisks lists security-sensitive settings made by project-level
	// files, whether or not they were applied. A caller that wants the user's
	// consent before honouring an unfamiliar repository's configuration keys off
	// this.
	ProjectRisks []Issue `json:"project_risks,omitempty"`
}

// UserConfigPath is the user-level file for a home directory.
func UserConfigPath(home string) string { return filepath.Join(home, ".sleipnir", "config.json") }

// ProjectConfigPath is the project-level file for a root.
func ProjectConfigPath(root string) string { return filepath.Join(root, ".sleipnir", "config.json") }

// LocalConfigPath is the project-local (git-ignored) file for a root.
func LocalConfigPath(root string) string {
	return filepath.Join(root, ".sleipnir", "config.local.json")
}

// FindRoot walks up from cwd looking for a directory that contains .git (a
// directory, or a file as in worktrees and submodules) or a .sleipnir
// directory, and returns the nearest one. The user's own ~/.sleipnir does not
// make their home directory a project. When nothing is found it returns cwd
// (made absolute) and false.
func FindRoot(cwd string) (root string, found bool) {
	home, _ := os.UserHomeDir()
	return findRoot(cwd, home)
}

func findRoot(cwd, home string) (string, bool) {
	abs, err := filepath.Abs(cwd)
	if err != nil {
		return cwd, false
	}
	for dir := abs; ; {
		if isProjectRoot(dir, home) {
			return dir, true
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return abs, false
		}
		dir = parent
	}
}

func isProjectRoot(dir, home string) bool {
	if _, err := os.Lstat(filepath.Join(dir, ".git")); err == nil {
		return true
	}
	fi, err := os.Stat(filepath.Join(dir, ".sleipnir"))
	if err != nil || !fi.IsDir() {
		return false
	}
	return !sameDir(dir, home)
}

func sameDir(a, b string) bool {
	if a == "" || b == "" {
		return false
	}
	if filepath.Clean(a) == filepath.Clean(b) {
		return true
	}
	ai, err1 := os.Stat(a)
	bi, err2 := os.Stat(b)
	return err1 == nil && err2 == nil && os.SameFile(ai, bi)
}

// readConfigFile reads a configuration file, refusing anything that is not a
// reasonably sized regular file.
func readConfigFile(path string) ([]byte, error) {
	fi, err := os.Stat(path)
	if err != nil {
		return nil, err
	}
	if !fi.Mode().IsRegular() {
		return nil, errors.New("not a regular file")
	}
	if fi.Size() > maxFileSize {
		return nil, fmt.Errorf("file is larger than %d KB", maxFileSize>>10)
	}
	return os.ReadFile(path)
}

func pathReason(err error) string {
	var pe *fs.PathError
	if errors.As(err, &pe) {
		return pe.Err.Error()
	}
	return err.Error()
}

var configType = reflect.TypeOf(Config{})

// Load builds the effective configuration from every layer. See the package
// documentation for the precedence and merge rules.
//
// On success the Config is complete and validated, and the Report says where
// each value came from and lists warnings. On failure (an unreadable or
// malformed file, a value of the wrong type, an invalid value) Load returns a
// nil Config, the Report gathered so far and an error listing every problem
// with file, line, column and field path; it never returns a partial
// configuration, because silently ignoring one layer could drop a deny rule.
func Load(opts LoadOpts) (*Config, *Report, error) {
	rep := &Report{Sources: map[string]string{}, Origins: map[string]string{}}

	home := opts.Home
	if home == "" {
		home, _ = os.UserHomeDir()
	}
	root := opts.Root
	if root == "" {
		cwd := opts.Cwd
		if cwd == "" {
			cwd, _ = os.Getwd()
		}
		if cwd != "" {
			root, _ = findRoot(cwd, home)
		}
	}
	if root != "" {
		root, _ = filepath.Abs(root)
	}
	rep.Root, rep.Home = root, home
	rep.Layers = append(rep.Layers, LayerInfo{Kind: "defaults", Source: "defaults", Found: true})

	var errs []error
	var warns []Issue
	fatal := false // some file could not be read or parsed, so there is nothing sound to validate
	byPath := map[string]*layer{}
	var layers []*layer

	type spec struct{ kind, path string }
	var specs []spec
	if home != "" {
		specs = append(specs, spec{"user", UserConfigPath(home)})
	}
	if root != "" {
		specs = append(specs, spec{"project", ProjectConfigPath(root)}, spec{"local", LocalConfigPath(root)})
	}
	var seenFiles []os.FileInfo
	for _, sp := range specs {
		// Running from the home directory makes the project file and the user file
		// the same file: apply it once.
		if fi, err := os.Stat(sp.path); err == nil {
			dup := false
			for _, s := range seenFiles {
				dup = dup || os.SameFile(s, fi)
			}
			if dup {
				continue
			}
			seenFiles = append(seenFiles, fi)
		}
		l, iss := loadFile(sp.kind, sp.path)
		for _, is := range iss {
			if is.Severity == SeverityError {
				errs = append(errs, is)
			} else {
				warns = append(warns, is)
			}
		}
		if l == nil {
			rep.Layers = append(rep.Layers, LayerInfo{Kind: sp.kind, Source: sp.path})
			fatal = fatal || hasError(iss)
			continue
		}
		if sp.kind != "user" {
			risks := l.collectRisks(opts.UntrustedProject)
			rep.ProjectRisks = append(rep.ProjectRisks, risks...)
		}
		layers = append(layers, l)
		byPath[l.source] = l
		rep.Layers = append(rep.Layers, LayerInfo{Kind: sp.kind, Source: sp.path, Found: true, Keys: layerKeys(l)})
	}

	environ := opts.Environ
	if environ == nil {
		environ = os.Environ
	}
	envLs, envIssues := envLayers(environ())
	for _, is := range envIssues {
		errs = append(errs, is)
	}
	if len(envLs) > 0 {
		info := LayerInfo{Kind: "env", Source: "env", Found: true}
		for _, l := range envLs {
			info.Keys = append(info.Keys, strings.TrimPrefix(l.source, "env:"))
		}
		rep.Layers = append(rep.Layers, info)
	}
	layers = append(layers, envLs...)

	if len(opts.Overrides) > 0 {
		l, iss := loadOverrides(opts.Overrides)
		for _, is := range iss {
			if is.Severity == SeverityError {
				errs = append(errs, is)
			} else {
				warns = append(warns, is)
			}
		}
		if l != nil {
			layers = append(layers, l)
			rep.Layers = append(rep.Layers, LayerInfo{Kind: "overrides", Source: "overrides", Found: true, Keys: layerKeys(l)})
			byPath[l.source] = l
		}
	}
	rep.Warnings = warns
	if fatal {
		return nil, rep, errors.Join(errs...)
	}

	// Values that failed their type check were dropped from their layer, so what
	// remains can still be merged and validated: that way one run reports the
	// wrong types and the invalid values together instead of one kind at a time.
	m := newMerger()
	for _, l := range layers {
		m.apply(l)
	}
	rep.Sources = m.sources
	rep.Origins = m.displayOrigins()

	cfg, err := m.decode()
	if err != nil {
		if len(errs) > 0 {
			return nil, rep, errors.Join(errs...)
		}
		return nil, rep, fmt.Errorf("config: %w", err)
	}
	var vwarns []Issue
	for _, is := range cfg.Validate() {
		if is.code == codeUnknownKey {
			continue // already reported, with positions, while checking each layer
		}
		if src := m.originFor(is.segs); src != "" {
			is.Source = src
			if l := byPath[src]; l != nil {
				l.positionAt(&is, false)
			}
		}
		if is.Severity == SeverityError {
			errs = append(errs, is)
		} else {
			vwarns = append(vwarns, is)
		}
	}
	if len(errs) > 0 {
		return nil, rep, errors.Join(errs...) // warnings about a rejected configuration are noise
	}
	rep.Warnings = append(rep.Warnings, vwarns...)
	return cfg, rep, nil
}

func hasError(issues []Issue) bool {
	for _, is := range issues {
		if is.Severity == SeverityError {
			return true
		}
	}
	return false
}

// loadFile reads, parses and checks one configuration file. A missing file is
// not a problem: it yields no layer and no issues.
func loadFile(kind, path string) (*layer, []Issue) {
	data, err := readConfigFile(path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, nil
		}
		return nil, []Issue{{Severity: SeverityError, Source: path, Message: "cannot read: " + pathReason(err)}}
	}
	root, issues, perr := parseJSONC(path, data)
	if perr != nil {
		var is Issue
		if errors.As(perr, &is) {
			return nil, append(issues, is)
		}
		return nil, append(issues, Issue{Severity: SeverityError, Source: path, Message: perr.Error()})
	}
	c := &checker{file: path, src: data}
	tree, _ := c.convert(root, configType, nil, true).(map[string]any)
	issues = append(issues, c.issues...)
	issues = append(issues, scanSecrets(path, data, root)...)
	sortIssues(issues)
	l := &layer{kind: kind, source: path, found: true, src: data, root: root, tree: tree}
	if l.tree == nil {
		l.tree = map[string]any{}
	}
	return l, issues
}

// loadOverrides checks the explicit overrides map like any other layer.
func loadOverrides(overrides map[string]any) (*layer, []Issue) {
	root, err := nodeFromValue(overrides)
	if err != nil {
		return nil, []Issue{{Severity: SeverityError, Source: "overrides", Message: "cannot be encoded as JSON: " + err.Error()}}
	}
	c := &checker{file: "overrides"}
	tree, _ := c.convert(root, configType, nil, true).(map[string]any)
	if tree == nil {
		tree = map[string]any{}
	}
	return &layer{kind: "overrides", source: "overrides", found: true, root: root, tree: tree}, c.issues
}

// sortIssues orders issues by position so output is stable.
func sortIssues(issues []Issue) {
	sort.SliceStable(issues, func(i, j int) bool {
		a, b := issues[i], issues[j]
		if a.Line != b.Line {
			return a.Line < b.Line
		}
		return a.Col < b.Col
	})
}

// String renders the report for a "config show" command: the layers, then where
// each top-level key came from, then warnings.
func (r *Report) String() string {
	var b strings.Builder
	b.WriteString("layers (lowest precedence first):\n")
	for _, l := range r.Layers {
		state := "found"
		if !l.Found {
			state = "not found"
		}
		if l.Kind == "defaults" || l.Kind == "env" || l.Kind == "overrides" {
			state = "active"
			if !l.Found {
				state = "unused"
			}
		}
		fmt.Fprintf(&b, "  %-9s %s (%s)\n", l.Kind, l.Source, state)
	}
	if len(r.Sources) > 0 {
		b.WriteString("keys:\n")
		for _, k := range sortedKeys(r.Sources) {
			fmt.Fprintf(&b, "  %-12s %s\n", k, r.Sources[k])
		}
	}
	if len(r.Warnings) > 0 {
		b.WriteString("warnings:\n")
		for _, w := range r.Warnings {
			fmt.Fprintf(&b, "  %s\n", w.Error())
		}
	}
	if len(r.ProjectRisks) > 0 {
		b.WriteString("security-sensitive settings from project files:\n")
		for _, w := range r.ProjectRisks {
			fmt.Fprintf(&b, "  %s\n", w.Error())
		}
	}
	return b.String()
}
