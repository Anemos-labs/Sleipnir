package settings

import (
	"net/http"
	"regexp"
	"strings"

	"github.com/anemos-labs/sleipnir/internal/config"
	"github.com/anemos-labs/sleipnir/internal/plugin"
	"github.com/anemos-labs/sleipnir/internal/provider"
	"github.com/anemos-labs/sleipnir/internal/web"
)

// The Plugins page (docs/PLUGINS.md, docs/WEB-API.md): the catalogue with each plugin's state, and the writes that switch one,
// change its settings, reset it or pick a profile. A write goes to the user's configuration file, or with scope "project" to the
// project's, as a minimal difference (plugin.Save); a running session keeps its plugin set until its team starts again, and the
// rows that differ from it come back in state "restart".

// pluginsView is the body of the catalogue routes.
type pluginsView struct {
	Profile  string           `json:"profile"`
	Profiles []plugin.Profile `json:"profiles"`
	Restart  bool             `json:"restart"`
	Plugins  []pluginRow      `json:"plugins"`
	Warnings []string         `json:"warnings,omitempty"`
}

// pluginRow is one plugin as the page sees it: an allow-list of the resolved status (never the plugin's directory).
type pluginRow struct {
	ID           string                `json:"id"`
	Title        string                `json:"title"`
	Description  string                `json:"description"`
	Category     string                `json:"category"`
	Core         bool                  `json:"core"`
	CoreReason   string                `json:"core_reason,omitempty"`
	Experimental bool                  `json:"experimental"`
	External     bool                  `json:"external"`
	Version      string                `json:"version,omitempty"`
	Default      bool                  `json:"default"`
	Enabled      bool                  `json:"enabled"`
	State        string                `json:"state"`
	Reason       string                `json:"reason"`
	Layer        string                `json:"layer"`
	Requires     []string              `json:"requires"`
	Contributes  []plugin.Contribution `json:"contributes"`
	ConfigSchema *plugin.Schema        `json:"config_schema,omitempty"`
	Config       map[string]any        `json:"config"`
}

// pluginWrite is the body of POST /api/plugins/{id}; every field is optional.
type pluginWrite struct {
	Enabled *bool          `json:"enabled"`
	Config  map[string]any `json:"config"`
	Scope   string         `json:"scope"`
}

// profileWrite is the body of POST /api/plugins/profile.
type profileWrite struct {
	Profile string `json:"profile"`
	Scope   string `json:"scope"`
}

// pluginReply is the answer to a write of one plugin.
type pluginReply struct {
	Plugin  pluginRow `json:"plugin"`
	Restart bool      `json:"restart"`
}

// secretKeyRe matches setting names whose values are withheld from the page.
var secretKeyRe = regexp.MustCompile(`(?i)(key|token|secret|password|passwd|credential)`)

// registerPlugins adds the Plugins page's routes.
func (s *service) registerPlugins() {
	s.srv.HandleFunc("GET /api/plugins", s.handlePlugins, web.RouteOpts{})
	s.srv.HandleFunc("GET /api/sessions/{id}/plugins", s.handlePlugins, web.RouteOpts{})
	s.srv.HandleFunc("POST /api/plugins/profile", s.handlePluginProfile, web.RouteOpts{MaxBody: 8 << 10})
	s.srv.HandleFunc("POST /api/plugins/{id}", s.handlePluginWrite, web.RouteOpts{MaxBody: 64 << 10})
	s.srv.HandleFunc("DELETE /api/plugins/{id}", s.handlePluginReset, web.RouteOpts{NoBody: true})
}

// pluginTab is the tab a plugins request is about: the {id} of the session route, ?session= on the others, else the page's
// active tab; nil when there is none (the catalogue is then the user's alone).
func (s *service) pluginTab(r *http.Request) (*tabCtx, error) {
	if strings.HasPrefix(r.URL.Path, "/api/sessions/") {
		return s.tabOf(r)
	}
	id := r.URL.Query().Get("session")
	if id == "" && s.host != nil {
		id = s.host.Active()
	}
	if id == "" {
		return nil, nil
	}
	tc, err := s.tabByID(id)
	if err != nil {
		return nil, nil // the tab went away: the catalogue is then the user's alone
	}
	return tc, nil
}

// pluginsOf resolves the plugin set the configuration now gives for tc's project (the user's alone without a tab), marked against
// the running session's set.
func (s *service) pluginsOf(tc *tabCtx) (*plugin.Set, bool, []string, error) {
	home, cwd, root, trusted := s.o.Home, "", "", false
	if tc != nil {
		home, cwd, root, trusted = s.homeOf(tc), tc.cwd, tc.root, tc.trusted
	}
	if cwd == "" {
		cwd = home // no project: only the user's file is read
		root = home
	}
	set, _, warns, err := plugin.LoadSet(cwd, root, home, trusted)
	if err != nil {
		return nil, false, nil, fail(http.StatusUnprocessableEntity, "bad_config", "your configuration does not load: see `sleipnir config`")
	}
	restart := false
	if tc != nil && tc.sess != nil {
		set, restart = plugin.MarkRestart(tc.sess.Plugins(), set)
	}
	return set, restart, warns, nil
}

// handlePlugins is GET /api/plugins and GET /api/sessions/{id}/plugins: the catalogue with each plugin's state.
func (s *service) handlePlugins(w http.ResponseWriter, r *http.Request) {
	tc, err := s.pluginTab(r)
	if err != nil {
		reply(w, nil, err)
		return
	}
	set, restart, warns, err := s.pluginsOf(tc)
	if err != nil {
		reply(w, nil, err)
		return
	}
	reply(w, s.pluginsView(set, restart, warns), nil)
}

// pluginsView builds the catalogue body of a set.
func (s *service) pluginsView(set *plugin.Set, restart bool, warns []string) pluginsView {
	v := pluginsView{Profile: set.Profile, Profiles: plugin.Profiles(), Restart: restart, Plugins: []pluginRow{}}
	if v.Profile == "" {
		v.Profile = plugin.DefaultProfile
	}
	for _, st := range set.List() {
		v.Plugins = append(v.Plugins, rowOf(st))
	}
	for _, w := range warns {
		v.Warnings = append(v.Warnings, provider.SanitizeText(w, 400))
	}
	return v
}

// rowOf is the page's row of a status. Text from an external plugin's manifest is sanitised; settings whose names look secret
// are withheld.
func rowOf(st *plugin.Status) pluginRow {
	row := pluginRow{
		ID: st.ID, Title: provider.SanitizeText(st.Title, 120), Description: provider.SanitizeText(st.Description, 400),
		Category: st.Category, Core: st.Core, CoreReason: st.CoreReason, Experimental: st.Experimental, External: st.External,
		Version: provider.SanitizeText(st.Version, 40), Default: st.Default, Enabled: st.Enabled, State: st.State,
		Reason: provider.SanitizeText(st.Reason, 400), Layer: st.Layer, Requires: append([]string{}, st.Requires...),
		Contributes: append([]plugin.Contribution{}, st.Manifest.Contributes...), ConfigSchema: st.Manifest.Config,
		Config: map[string]any{},
	}
	for k, v := range st.Config {
		if secretKeyRe.MatchString(k) {
			row.Config[k] = "(withheld)"
			continue
		}
		row.Config[k] = v
	}
	return row
}

// pluginTarget is the configuration file a write of scope goes to, and the profile its minimal difference is measured against.
func (s *service) pluginTarget(tc *tabCtx, scope string) (string, string, error) {
	home := s.homeOf(tc)
	switch scope {
	case "", "user":
		return config.UserConfigPath(home), plugin.ConfiguredProfile(home, home, home), nil
	case "project":
		if tc == nil || tc.root == "" {
			return "", "", fail(http.StatusBadRequest, "no_project", "there is no project to write to: open a session in one first")
		}
		return config.ProjectConfigPath(tc.root), plugin.ConfiguredProfile(tc.cwd, tc.root, home), nil
	}
	return "", "", fail(http.StatusBadRequest, "bad_request", `scope is "user" or "project"`)
}

// pluginFor resolves the {id} of a write against the current catalogue: 404 for an unknown plugin, 409 for a core one.
func (s *service) pluginFor(r *http.Request, tc *tabCtx, switching bool) (*plugin.Status, error) {
	id := r.PathValue("id")
	if !plugin.ValidID(id) {
		return nil, fail(http.StatusBadRequest, "bad_request", "the plugin id is not valid")
	}
	set, _, _, err := s.pluginsOf(tc)
	if err != nil {
		return nil, err
	}
	st, ok := set.Get(id)
	if !ok {
		return nil, fail(http.StatusNotFound, "no_plugin", "there is no such plugin")
	}
	if switching && st.Core {
		return nil, fail(http.StatusConflict, "core", "this plugin is core and always on: "+st.CoreReason)
	}
	return st, nil
}

// handlePluginWrite is POST /api/plugins/{id}: switch a plugin on or off and/or replace its settings.
func (s *service) handlePluginWrite(w http.ResponseWriter, r *http.Request) {
	var req pluginWrite
	if !web.DecodeJSON(w, r, &req) {
		return
	}
	if req.Enabled == nil && req.Config == nil {
		reply(w, nil, fail(http.StatusBadRequest, "bad_request", "send enabled, config or both"))
		return
	}
	s.writePlugin(w, r, req.Scope, req.Enabled, req.Config, false)
}

// handlePluginReset is DELETE /api/plugins/{id}?scope=: the row goes back to the profile's default (its settings stay).
func (s *service) handlePluginReset(w http.ResponseWriter, r *http.Request) {
	s.writePlugin(w, r, r.URL.Query().Get("scope"), nil, nil, true)
}

// writePlugin saves one plugin's choice and answers with its new row.
func (s *service) writePlugin(w http.ResponseWriter, r *http.Request, scope string, enabled *bool, settings map[string]any, reset bool) {
	tc, err := s.pluginTab(r)
	if err != nil {
		reply(w, nil, err)
		return
	}
	st, err := s.pluginFor(r, tc, enabled != nil || reset)
	if err != nil {
		reply(w, nil, err)
		return
	}
	path, profile, err := s.pluginTarget(tc, scope)
	if err != nil {
		reply(w, nil, err)
		return
	}
	if reset {
		def := st.Default
		enabled = &def
	}
	if err := plugin.Save(path, profile, st.ID, enabled, settings); err != nil {
		web.Logf(r, "plugin choice not saved")
		reply(w, nil, fail(http.StatusUnprocessableEntity, "rejected", "the configuration file could not be changed (it does not load: see `sleipnir config`)"))
		return
	}
	set, restart, _, err := s.pluginsOf(tc)
	if err != nil {
		reply(w, nil, err)
		return
	}
	after, _ := set.Get(st.ID)
	reply(w, pluginReply{Plugin: rowOf(after), Restart: restart}, nil)
}

// handlePluginProfile is POST /api/plugins/profile: pick the profile; the answer is the whole catalogue.
func (s *service) handlePluginProfile(w http.ResponseWriter, r *http.Request) {
	var req profileWrite
	if !web.DecodeJSON(w, r, &req) {
		return
	}
	if _, ok := plugin.ProfileByID(req.Profile); !ok && req.Profile != "" {
		reply(w, nil, fail(http.StatusBadRequest, "bad_request", "no such profile"))
		return
	}
	tc, err := s.pluginTab(r)
	if err != nil {
		reply(w, nil, err)
		return
	}
	path, _, err := s.pluginTarget(tc, req.Scope)
	if err != nil {
		reply(w, nil, err)
		return
	}
	if err := plugin.SaveProfile(path, req.Profile); err != nil {
		web.Logf(r, "plugin profile not saved")
		reply(w, nil, fail(http.StatusUnprocessableEntity, "rejected", "the configuration file could not be changed (it does not load: see `sleipnir config`)"))
		return
	}
	set, restart, warns, err := s.pluginsOf(tc)
	if err != nil {
		reply(w, nil, err)
		return
	}
	reply(w, s.pluginsView(set, restart, warns), nil)
}
