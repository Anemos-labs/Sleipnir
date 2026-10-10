package settings

import (
	"encoding/json"
	"fmt"
	"net/http"
	"path/filepath"
	"strings"

	"github.com/anemos-labs/sleipnir/internal/config"
	"github.com/anemos-labs/sleipnir/internal/provider"
	"github.com/anemos-labs/sleipnir/internal/web/wire"
)

// configPrecedence is the order of the layers, lowest first.
const configPrecedence = "lowest first: defaults, user file, project file, local file, environment, flags"

// configIssue is a warning about the configuration, located: the file (as the page shows it), line and column, the setting's path
// and the message.
type configIssue struct {
	File     string `json:"file,omitempty"`
	Line     int    `json:"line,omitempty"`
	Col      int    `json:"col,omitempty"`
	Path     string `json:"path,omitempty"`
	Message  string `json:"message"`
	Severity string `json:"severity"`
}

// configView is wire.ConfigView with the warnings located (PARITY A19): Issues are the warnings of `sleipnir config`, Risks the
// project's security-sensitive settings and whether they were left out, Valid the closing line ("configuration is valid", with the
// number of warnings, or why it is not).
type configView struct {
	wire.ConfigView
	Issues []configIssue `json:"issues"`
	Risks  []configIssue `json:"risks"`
	Valid  string        `json:"valid"`
	OK     bool          `json:"ok"`
}

// handleConfig is GET /api/sessions/{id}/config: the layers of the tab's project, lowest first, with the state of each; every
// effective value (redacted: config.Redact's rules) with the layer and file that supplied it and what lower layers said; and the
// warnings, as `sleipnir config` prints them.
func (s *service) handleConfig(w http.ResponseWriter, r *http.Request) {
	tc, err := s.tabOf(r)
	if err != nil {
		reply(w, nil, err)
		return
	}
	values, rep, lerr := config.Values(s.loadOpts(tc))
	v := configView{ConfigView: wire.ConfigView{Layers: []wire.ConfigLayer{}, Precedence: configPrecedence, Effective: []wire.ConfigValue{}}, Issues: []configIssue{}, Risks: []configIssue{}}
	if rep != nil {
		for _, l := range rep.Layers {
			v.Layers = append(v.Layers, s.layerOf(l, tc))
		}
		for _, is := range rep.Warnings {
			v.Issues = append(v.Issues, s.issueOf(is, tc))
		}
		for _, is := range rep.ProjectRisks {
			v.Risks = append(v.Risks, s.issueOf(is, tc))
		}
	}
	for _, val := range values {
		cv := wire.ConfigValue{Key: val.Key, Value: val.Value, Layer: val.Layer, File: s.layerFile(val.File, tc)}
		if cv.Layer == "defaults" {
			cv.File = "built-in"
		}
		if cv.Layer == "overrides" {
			cv.Layer = "flag"
		}
		for k, b := range val.Below {
			if cv.Below == nil {
				cv.Below = map[string]string{}
			}
			cv.Below[k] = shortJSON(b)
		}
		v.Effective = append(v.Effective, cv)
	}
	for _, is := range v.Issues {
		v.Warnings = append(v.Warnings, issueLine(is))
	}
	n := 0
	if rep != nil {
		n = len(rep.Warnings)
	}
	switch {
	case lerr != nil:
		v.Valid = "configuration is not valid: " + cleanText(s.scrubPaths(lerr.Error(), tc))
	case n == 0:
		v.Valid, v.OK = "configuration is valid", true
	default:
		v.Valid, v.OK = fmt.Sprintf("configuration is valid, with %d %s", n, plural(n, "warning", "warnings")), true
	}
	reply(w, v, nil)
}

// layerOf describes a layer as the page lists it.
func (s *service) layerOf(l config.LayerInfo, tc *tabCtx) wire.ConfigLayer {
	state := "found"
	if !l.Found {
		state = "not found"
	}
	out := wire.ConfigLayer{Kind: l.Kind, Source: s.layerFile(l.Source, tc), State: state}
	switch l.Kind {
	case "defaults", "overrides":
		out.State = "active"
		if !l.Found {
			out.State = "unused"
		}
		if l.Kind == "overrides" {
			out.Kind = "flags"
		}
	case "env":
		out.State = "active"
		if !l.Found {
			out.State = "unused"
		}
		out.Source = "SLEIPNIR_* variables"
		if len(l.Keys) > 0 {
			out.Source += " (" + strings.Join(l.Keys, ", ") + ")"
		}
	case "project", "local":
		if l.Found {
			if tc.trusted {
				out.Trusted = "trusted: its security-sensitive settings apply"
			} else {
				out.Trusted = "not trusted: its security-sensitive settings are left out"
			}
		}
	case "user":
		out.Trusted = true
	}
	return out
}

// layerFile shows a layer's source: a project file relative to the root, a user file as ~/..., an environment variable by name.
func (s *service) layerFile(src string, tc *tabCtx) string {
	switch {
	case src == "" || src == "defaults" || src == "overrides" || src == "env":
		return src
	case strings.HasPrefix(src, "env:"):
		return strings.TrimPrefix(src, "env:")
	}
	if tc.root != "" {
		if rel, ok := relUnder(tc.root, src); ok {
			return rel
		}
	}
	return s.display(src)
}

// issueOf locates a configuration issue for the page.
func (s *service) issueOf(is config.Issue, tc *tabCtx) configIssue {
	return configIssue{
		File: s.layerFile(is.Source, tc), Line: is.Line, Col: is.Col, Path: cleanText(is.Path), Message: cleanText(s.scrubPaths(is.Message, tc)),
		Severity: is.Severity.String(),
	}
}

// issueLine is an issue on one line, as `sleipnir config` prints it: file:line:col: path: message.
func issueLine(is configIssue) string {
	var b strings.Builder
	if is.File != "" {
		b.WriteString(is.File)
		if is.Line > 0 {
			fmt.Fprintf(&b, ":%d", is.Line)
			if is.Col > 0 {
				fmt.Fprintf(&b, ":%d", is.Col)
			}
		}
		b.WriteString(": ")
	}
	if is.Path != "" {
		b.WriteString(is.Path + ": ")
	}
	b.WriteString(is.Message)
	return b.String()
}

// scrubPaths shows the absolute paths of a message the way the page shows paths (the home directory as ~).
func (s *service) scrubPaths(msg string, tc *tabCtx) string {
	if tc.root != "" {
		msg = strings.ReplaceAll(msg, tc.root+string(filepath.Separator), "")
	}
	if s.o.Home != "" {
		msg = strings.ReplaceAll(msg, s.o.Home+string(filepath.Separator), "~/")
	}
	return msg
}

// shortJSON is a value as one short line of JSON.
func shortJSON(v any) string {
	if s, ok := v.(string); ok {
		return s
	}
	b, err := json.Marshal(v)
	if err != nil {
		return ""
	}
	return provider.SanitizeText(string(b), 200)
}

// cleanText is untrusted or stored text as one line for the page: credentials withheld (config.ScrubText), control characters removed,
// at most 500 characters.
func cleanText(s string) string { return provider.SanitizeText(config.ScrubText(s), 500) }

// relUnder returns p relative to dir, with / separators, when p is inside dir.
func relUnder(dir, p string) (string, bool) {
	if dir == "" || !filepath.IsAbs(p) {
		return "", false
	}
	rel, err := filepath.Rel(dir, p)
	if err != nil || rel == "." || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", false
	}
	return filepath.ToSlash(rel), true
}
