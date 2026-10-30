package settings

import (
	"context"
	"encoding/json"
	"maps"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/anemos-labs/sleipnir/internal/plugin"
	"github.com/anemos-labs/sleipnir/internal/provider"
	"github.com/anemos-labs/sleipnir/internal/session"
	"github.com/anemos-labs/sleipnir/internal/web"
)

// The programs and tool servers the plugins that are on need (plugin.Need, docs/PLUGINS.md): which are found, and the two actions
// for one that is not. Locate remembers a file the person names; Install runs one of the need's declared install methods, as the
// argv the page showed, only on a request that names it, in the background, and the catalogue reports its output until it ends.

// needRow is a need as the page sees it.
type needRow struct {
	ID          string `json:"id"`
	Plugin      string `json:"plugin"`
	Title       string `json:"title"`
	Description string `json:"description,omitempty"`
	Homepage    string `json:"homepage,omitempty"`
	Required    bool   `json:"required"`
	Server      bool   `json:"server"` // a tool server: it is connected, not located
	Found       bool   `json:"found"`
	Path        string `json:"path,omitempty"`
	Via         string `json:"via,omitempty"`
	Version     string `json:"version,omitempty"`
	// Commands are the programs an install puts on the PATH, and Paths the places it finds them in, by operating system: what
	// an install's confirmation covers besides the commands it runs.
	Commands []string            `json:"commands,omitempty"`
	Paths    map[string][]string `json:"paths,omitempty"`
	Methods  []methodRow         `json:"methods"`
	Install  *needInstallRow     `json:"install,omitempty"`
}

// methodRow is an install method with the exact command it runs.
type methodRow struct {
	ID    string `json:"id"`
	Title string `json:"title"`
	// Env are the variables the commands get, with the command each is read from.
	Env map[string][]string `json:"env,omitempty"`
	// Setup are the commands run before Argv, in order.
	Setup     [][]string `json:"setup,omitempty"`
	Argv      []string   `json:"argv,omitempty"`
	Connect   string     `json:"connect,omitempty"`
	Note      string     `json:"note,omitempty"`
	Available bool       `json:"available"`
	Reason    string     `json:"reason,omitempty"`
	// Scope is the confirmation scope of running exactly this method (docs/WEB-API.md, Confirmations).
	Scope string `json:"scope"`
}

// needInstallRow is the last install of a need since the server started.
type needInstallRow struct {
	Method  string `json:"method"`
	Running bool   `json:"running"`
	Output  string `json:"output"`
	Error   string `json:"error,omitempty"`
}

// needsView is the body of GET /api/needs.
type needsView struct {
	Needs []needRow `json:"needs"`
}

// locateWrite is the body of POST /api/needs/{id}/locate.
type locateWrite struct {
	Path string `json:"path"`
}

// installWrite is the body of POST /api/needs/{id}/install.
type installWrite struct {
	Method string `json:"method"`
}

// needJob is an install that runs or ran.
type needJob struct {
	mu      sync.Mutex
	method  string
	running bool
	out     []byte
	err     string
}

// needOutputMax bounds the output a job keeps (its tail).
const needOutputMax = 32 << 10

func (j *needJob) Write(p []byte) (int, error) {
	j.mu.Lock()
	defer j.mu.Unlock()
	j.out = append(j.out, p...)
	if len(j.out) > needOutputMax {
		j.out = append([]byte(nil), j.out[len(j.out)-needOutputMax:]...)
	}
	return len(p), nil
}

func (j *needJob) row() *needInstallRow {
	j.mu.Lock()
	defer j.mu.Unlock()
	// The output goes to the page as JSON text, shown as text: line breaks stay.
	return &needInstallRow{Method: j.method, Running: j.running, Output: strings.ToValidUTF8(string(j.out), "\uFFFD"), Error: j.err}
}

// needInstallTimeout bounds one install (a Blender download is large).
const needInstallTimeout = 30 * time.Minute

// needJobs are the installs of this server, by need id.
type needJobs struct {
	mu   sync.Mutex
	jobs map[string]*needJob
}

func (s *service) registerNeeds() {
	s.srv.HandleFunc("GET /api/needs", s.handleNeeds, web.RouteOpts{})
	s.srv.HandleFunc("POST /api/needs/{id}/locate", s.handleNeedLocate, web.RouteOpts{MaxBody: 8 << 10})
	s.srv.HandleFunc("DELETE /api/needs/{id}/locate", s.handleNeedForget, web.RouteOpts{NoBody: true})
	s.srv.HandleFunc("POST /api/needs/{id}/install", s.handleNeedInstall, web.RouteOpts{MaxBody: 8 << 10})
}

// needEnvOf is where needs are looked for, for tc's home.
func (s *service) needEnvOf(tc *tabCtx) plugin.NeedEnv {
	home := s.homeOf(tc)
	env := plugin.NeedEnv{State: session.StateRoot(home), Home: home}
	if s.needEnv != nil {
		env = s.needEnv(env)
	}
	return env
}

// needsOf are the needs of the plugins that are on for tc, found or not.
func (s *service) needsOf(r *http.Request) (*tabCtx, plugin.NeedEnv, []plugin.OwnedNeed, error) {
	tc, err := s.pluginTab(r)
	if err != nil {
		return nil, plugin.NeedEnv{}, nil, err
	}
	set, _, _, err := s.pluginsOf(tc)
	if err != nil {
		return nil, plugin.NeedEnv{}, nil, err
	}
	return tc, s.needEnvOf(tc), plugin.Needs(set), nil
}

// needRows are the page's rows of needs.
func (s *service) needRows(env plugin.NeedEnv, needs []plugin.OwnedNeed) []needRow {
	out := []needRow{}
	for _, st := range plugin.CheckNeeds(env, needs) {
		out = append(out, s.needRowOf(st))
	}
	return out
}

// needRowOf is one row; a need's text can come from an external plugin's manifest, so it is sanitised.
func (s *service) needRowOf(st plugin.NeedStatus) needRow {
	row := needRow{
		ID: st.ID, Plugin: st.Plugin, Title: provider.SanitizeText(st.Title, 120), Description: provider.SanitizeText(st.Description, 400),
		Homepage: provider.SanitizeText(st.Homepage, 200), Required: st.Required, Server: st.MCP != nil, Found: st.Found,
		Path: provider.SanitizeText(st.Path, 400), Via: st.Via, Version: provider.SanitizeText(st.Version, 120), Methods: []methodRow{},
	}
	for _, c := range st.Commands {
		row.Commands = append(row.Commands, provider.SanitizeText(c, 200))
	}
	for goos, pats := range st.Paths {
		if row.Paths == nil {
			row.Paths = map[string][]string{}
		}
		for _, p := range pats {
			row.Paths[goos] = append(row.Paths[goos], provider.SanitizeText(p, 400))
		}
	}
	for _, m := range st.Methods {
		mr := methodRow{ID: m.ID, Title: provider.SanitizeText(m.Title, 120), Connect: provider.SanitizeText(m.Connect, 400),
			Note: provider.SanitizeText(m.Note, 400), Available: m.Available, Reason: provider.SanitizeText(m.Reason, 200)}
		for name, argv := range m.Env {
			if mr.Env == nil {
				mr.Env = map[string][]string{}
			}
			for _, a := range argv {
				mr.Env[name] = append(mr.Env[name], provider.SanitizeText(a, 200))
			}
		}
		for _, step := range m.Setup {
			var clean []string
			for _, a := range step {
				clean = append(clean, provider.SanitizeText(a, 200))
			}
			mr.Setup = append(mr.Setup, clean)
		}
		for _, a := range m.Argv {
			mr.Argv = append(mr.Argv, provider.SanitizeText(a, 200))
		}
		if mr.Available && !shownWhole(st.Need, m) {
			mr.Available, mr.Reason = false, "its commands cannot be shown whole for you to confirm"
		}
		mr.Scope = installScope(st.Need, m)
		row.Methods = append(row.Methods, mr)
	}
	if j := s.job(st.ID); j != nil {
		row.Install = j.row()
	}
	return row
}

// job is the last install job of id, or nil.
func (s *service) job(id string) *needJob {
	s.jobs.mu.Lock()
	defer s.jobs.mu.Unlock()
	return s.jobs.jobs[id]
}

// startJob records a running install of id by method; nil when one is still running.
func (s *service) startJob(id, method string) *needJob {
	s.jobs.mu.Lock()
	defer s.jobs.mu.Unlock()
	if j := s.jobs.jobs[id]; j != nil {
		j.mu.Lock()
		running := j.running
		j.mu.Unlock()
		if running {
			return nil
		}
	}
	if s.jobs.jobs == nil {
		s.jobs.jobs = map[string]*needJob{}
	}
	j := &needJob{method: method, running: true}
	s.jobs.jobs[id] = j
	return j
}

// installScope is the confirmation scope of running method m of need n: it covers the commands and the server entry it adds, the
// entry as the structured name, command and arguments that are saved (the words shown lose where one argument ends). Every list
// is hashed as its JSON encoding, which keeps where each command and each argument ends whatever bytes the arguments hold: two
// different plans never share a scope.
//
// It also covers what the install links once it ends: the commands whose launchers it puts on the PATH, the default locations it
// finds them in, and the arguments that check a found file, so a manifest changed after the confirmation needs another.
func installScope(n plugin.Need, m plugin.MethodStatus) string {
	fields := map[string]string{"need": n.ID, "method": m.ID, "argv": jsonWords(m.Argv), "connect": m.Connect,
		"commands": jsonWords(n.Commands), "paths": jsonWords(n.Paths), "version": jsonWords(n.VersionArgs)}
	if m.Connect != "" && n.MCP != nil {
		fields["mcp"] = jsonWords(append([]string{n.MCP.Name, n.MCP.Command}, n.MCP.Args...))
	}
	if len(m.Setup) > 0 {
		fields["setup"] = jsonWords(m.Setup)
	}
	for _, name := range plugin.EnvNames(m.Env) {
		fields["env:"+name] = jsonWords(m.Env[name])
	}
	return "need.install:" + d16(fields)
}

// shownWhole reports whether every argument of m's commands, the tool server entry it adds, and every command and default location
// of n that the install links, reads on the page exactly as it is used: none is cut short or loses a character to sanitising.
func shownWhole(n plugin.Need, m plugin.MethodStatus) bool {
	if provider.SanitizeText(m.Connect, 400) != m.Connect { // the server entry it saves, which a later session starts
		return false
	}
	words := slices.Clone(m.Argv)
	for _, c := range n.Commands {
		if provider.SanitizeText(c, 200) != c {
			return false
		}
	}
	for _, pats := range n.Paths {
		for _, p := range pats {
			if provider.SanitizeText(p, 400) != p {
				return false
			}
		}
	}
	for _, step := range m.Setup {
		words = append(words, step...)
	}
	for _, argv := range m.Env {
		words = append(words, argv...)
	}
	for _, a := range words {
		if provider.SanitizeText(a, 200) != a {
			return false
		}
	}
	return true
}

// shownWords is a command as the person confirms it: its arguments apart, one that is empty or holds a space or a quote quoted,
// so the words say where each argument ends.
func shownWords(argv []string) string {
	out := make([]string, len(argv))
	for i, a := range argv {
		if a == "" || strings.ContainsAny(a, " \t\"'\\") {
			a = strconv.Quote(a)
		}
		out[i] = a
	}
	return strings.Join(out, " ")
}

// pathsText is where an install looks for the commands it links, by operating system (" (looked for in darwin: …; unix: …)"),
// or "" when the need names no default location.
func pathsText(paths map[string][]string) string {
	var parts []string
	for _, goos := range slices.Sorted(maps.Keys(paths)) {
		if len(paths[goos]) > 0 {
			parts = append(parts, goos+": "+strings.Join(paths[goos], ", "))
		}
	}
	if len(parts) == 0 {
		return ""
	}
	return " (looked for in " + strings.Join(parts, "; ") + ")"
}

// jsonWords is the JSON encoding of a list of arguments (or of commands), unambiguous where a joined string is not.
func jsonWords(v any) string {
	b, _ := json.Marshal(v) // string slices always encode
	return string(b)
}

// confirmNeed checks the confirmation of a need's action; without one it answers 428 with the scope and what it does.
func (s *service) confirmNeed(w http.ResponseWriter, r *http.Request, scope string, reasons []string) bool {
	if r.Header.Get(web.ConfirmHeader) == "" {
		w.Header().Set("X-Confirm-Scope", scope)
		web.ErrorDetail(w, http.StatusPreconditionRequired, "confirm_required", "this runs a program on this machine: confirm it, then send it again with the confirmation",
			map[string]any{"scope": scope, "reasons": reasons})
		return false
	}
	return s.srv.RequireConfirm(w, r, scope)
}

// handleNeeds is GET /api/needs.
func (s *service) handleNeeds(w http.ResponseWriter, r *http.Request) {
	_, env, needs, err := s.needsOf(r)
	if err != nil {
		reply(w, nil, err)
		return
	}
	reply(w, needsView{Needs: s.needRows(env, needs)}, nil)
}

// needFor resolves the {id} of a write among the needs of the plugins that are on.
func (s *service) needFor(r *http.Request) (plugin.NeedEnv, plugin.OwnedNeed, error) {
	_, env, needs, err := s.needsOf(r)
	if err != nil {
		return env, plugin.OwnedNeed{}, err
	}
	n, ok := plugin.FindNeed(needs, r.PathValue("id"))
	if !ok {
		return env, n, fail(http.StatusNotFound, "no_need", "no plugin that is on needs this")
	}
	return env, n, nil
}

// handleNeedLocate is POST /api/needs/{id}/locate: remember the program's file.
func (s *service) handleNeedLocate(w http.ResponseWriter, r *http.Request) {
	var req locateWrite
	if !web.DecodeJSON(w, r, &req) {
		return
	}
	env, n, err := s.needFor(r)
	if err != nil {
		reply(w, nil, err)
		return
	}
	if strings.TrimSpace(req.Path) == "" {
		reply(w, nil, fail(http.StatusBadRequest, "bad_request", "send the program's path"))
		return
	}
	path := strings.TrimSpace(req.Path)
	reasons := []string{"runs " + path + versionArgs(n.Need) + " to check it is " + n.Title,
		"puts it on the PATH of the commands every session runs, as " + strings.Join(n.Commands, ", ")}
	if !s.confirmNeed(w, r, locateScope(n.Need, path), reasons) {
		return
	}
	st, err := plugin.LocateNeed(r.Context(), env, n.Need, path)
	if err != nil {
		reply(w, nil, fail(http.StatusUnprocessableEntity, "not_located", provider.SanitizeText(err.Error(), 400)))
		return
	}
	st.Plugin = n.Plugin
	reply(w, s.needRowOf(st), nil)
}

// locateScope is the confirmation scope of locating need n at path: it covers the check that runs (the path and its version
// arguments) and the commands whose launchers it puts on the PATH, so a manifest changed after the confirmation needs another.
func locateScope(n plugin.Need, path string) string {
	return "need.locate:" + d16(map[string]string{"need": n.ID, "path": path,
		"version": strings.Join(n.VersionArgs, "\x00"), "commands": strings.Join(n.Commands, "\x00")})
}

// handleNeedForget is DELETE /api/needs/{id}/locate: forget a located file.
func (s *service) handleNeedForget(w http.ResponseWriter, r *http.Request) {
	env, n, err := s.needFor(r)
	if err != nil {
		reply(w, nil, err)
		return
	}
	if err := plugin.ForgetNeed(env, n.Need); err != nil {
		reply(w, nil, fail(http.StatusUnprocessableEntity, "rejected", "the located path could not be forgotten"))
		return
	}
	reply(w, s.needRows(env, []plugin.OwnedNeed{n})[0], nil)
}

// handleNeedInstall is POST /api/needs/{id}/install: start one declared install method. The answer is the row with the running
// install; GET /api/needs reports it until it ends.
func (s *service) handleNeedInstall(w http.ResponseWriter, r *http.Request) {
	var req installWrite
	if !web.DecodeJSON(w, r, &req) {
		return
	}
	env, n, err := s.needFor(r)
	if err != nil {
		reply(w, nil, err)
		return
	}
	st := plugin.CheckNeeds(env, []plugin.OwnedNeed{n})[0]
	var method *plugin.MethodStatus
	for i := range st.Methods {
		if st.Methods[i].ID == req.Method {
			method = &st.Methods[i]
		}
	}
	switch {
	case method == nil:
		reply(w, nil, fail(http.StatusBadRequest, "bad_request", "no such install method for this machine"))
		return
	case !method.Available:
		reply(w, nil, fail(http.StatusConflict, "unavailable", provider.SanitizeText(method.Reason, 200)))
		return
	case !shownWhole(n.Need, *method):
		// what is confirmed is what runs: a command the page would show cut short or with characters left out is not run
		reply(w, nil, fail(http.StatusConflict, "unavailable", "its commands cannot be shown whole for you to confirm"))
		return
	}
	reasons := []string{}
	for _, name := range plugin.EnvNames(method.Env) {
		reasons = append(reasons, "runs, as you, to set "+name+": "+shownWords(method.Env[name]))
	}
	for _, step := range method.Setup {
		reasons = append(reasons, "runs, as you: "+shownWords(step))
	}
	if len(method.Argv) > 0 {
		reasons = append(reasons, "runs, as you: "+shownWords(method.Argv))
	}
	if method.Connect != "" {
		reasons = append(reasons, method.Connect)
	}
	if len(n.Commands) > 0 {
		reasons = append(reasons, "then puts on the PATH of the commands every session runs: "+strings.Join(n.Commands, ", ")+pathsText(n.Paths))
	}
	if !s.confirmNeed(w, r, installScope(n.Need, *method), reasons) {
		return
	}
	j := s.startJob(n.ID, req.Method)
	if j == nil {
		reply(w, nil, fail(http.StatusConflict, "running", "an install of this is already running"))
		return
	}
	web.Logf(r, "installing a plugin need")
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), needInstallTimeout)
		defer cancel()
		_, err := plugin.InstallNeed(ctx, env, n.Need, req.Method, j)
		j.mu.Lock()
		j.running = false
		if err != nil {
			j.err = provider.SanitizeText(err.Error(), 400)
		}
		j.mu.Unlock()
	}()
	reply(w, s.needRowOf(st), nil)
}

// versionArgs is " --version" for a need that checks a located file with it.
func versionArgs(n plugin.Need) string {
	if len(n.VersionArgs) == 0 {
		return ""
	}
	return " " + strings.Join(n.VersionArgs, " ")
}
