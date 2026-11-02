package settings

import (
	"encoding/json"
	"net/http"
	"os"
	"sort"
	"strconv"
	"strings"

	"github.com/anemos-labs/sleipnir/internal/catalog"
	"github.com/anemos-labs/sleipnir/internal/config"
	"github.com/anemos-labs/sleipnir/internal/provider"
	"github.com/anemos-labs/sleipnir/internal/session"
	"github.com/anemos-labs/sleipnir/internal/swarm"
	"github.com/anemos-labs/sleipnir/internal/web"
	"github.com/anemos-labs/sleipnir/internal/web/wire"
)

// builtinRoleText is the one line the Roles page shows of each built-in role (swarm.RoleSummaries: the team card says the same).
var builtinRoleText = swarm.RoleSummaries

// roleOrder is the order the built-in worker roles are shown in (swarm.WorkerRoleOrder); roles from agent definitions follow, by name.
var roleOrder = swarm.WorkerRoleOrder

// effortLevels are the reasoning efforts a session accepts, "default" first (provider.NormalizeEffort's levels).
var effortLevels = []string{"default", "none", "minimal", "low", "medium", "high", "xhigh", "max"}

// handleModels is GET /api/models: the catalogue (cached 6 h; refresh=1 asks every provider again), with the favourites, the roles of
// the active tab (or the tab named by tab=) and its model's effort levels. all=1 also lists models that do not chat; the optional
// filters of `sleipnir models` narrow the rows: q (words), tools=1, reasoning=1, fav=1, max_price (output $/M), min_context (128k).
func (s *service) handleModels(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	f := catalog.Filter{
		Words: strings.Fields(q.Get("q")), Tools: q.Get("tools") == "1", Reasoning: q.Get("reasoning") == "1",
		OnlyFavorite: q.Get("fav") == "1", All: q.Get("all") == "1",
	}
	if v := q.Get("max_price"); v != "" {
		p, err := strconv.ParseFloat(v, 64)
		if err != nil || p < 0 {
			web.WriteError(w, fail(http.StatusBadRequest, "bad_request", "max_price is a number of dollars per million output tokens"))
			return
		}
		f.MaxOut = p
	}
	if v := q.Get("min_context"); v != "" {
		n, err := catalog.ParseTokens(v)
		if err != nil {
			web.WriteError(w, fail(http.StatusBadRequest, "bad_request", "min_context is a token count such as 128k"))
			return
		}
		f.MinContext = n
	}
	var tc *tabCtx
	if id := q.Get("tab"); id != "" || (s.host != nil && s.host.Active() != "") {
		if id == "" {
			id = s.host.Active()
		}
		if t, err := s.tabByID(id); err == nil {
			tc = t
		} else if q.Get("tab") != "" {
			web.WriteError(w, err)
			return
		}
	}
	opts := catalog.Options{Home: s.o.Home, Refresh: q.Get("refresh") == "1"}
	if tc != nil {
		opts.Home, opts.Cwd = s.homeOf(tc), tc.cwd
		if tc.info.Config != nil {
			opts.Config = tc.info.Config
		}
	}
	if opts.Refresh {
		s.fetchMu.Lock()
	}
	res := s.fetch(r.Context(), opts)
	if opts.Refresh {
		s.fetchMu.Unlock()
	}
	cfg := opts.Config
	if cfg == nil {
		cfg, _, _ = config.Load(config.LoadOpts{Home: opts.Home, Cwd: opts.Cwd, UntrustedProject: true})
	}
	v := s.modelsView(res, f, cfg, tc)
	v.Saved, v.SaveFile = s.savedModels(tc, v.RoleOrder), s.display(config.UserConfigPath(s.o.Home))
	reply(w, modelsReply{ModelsView: v, ConfigNote: s.commentsNote()}, nil)
}

// savedLoad reads the configuration a new session of tc (or, with no tab, of the server's directory) would start from, with the
// report of where each value came from.
func (s *service) savedLoad(tc *tabCtx) (*config.Config, *config.Report, error) {
	if tc != nil {
		return config.Load(s.loadOpts(tc))
	}
	return config.Load(config.LoadOpts{Home: s.o.Home, UntrustedProject: true})
}

// savedModels are the default models a new session reads (models.default, then the manager and each role of order), with where each
// comes from; nil when the configuration does not load (the page then shows nothing rather than a guess).
func (s *service) savedModels(tc *tabCtx, order []string) map[string]wire.SavedModel {
	cfg, rep, err := s.savedLoad(tc)
	if err != nil {
		return nil
	}
	out := map[string]wire.SavedModel{}
	for _, role := range append([]string{"default", "manager"}, order...) {
		out[role] = s.savedOf(catalog.DefaultOf(cfg, rep, role))
	}
	for _, role := range []string{"default", "manager"} {
		if d := out[role]; s.o.Model != "" && d.Key == "models.default" {
			out[role] = wire.SavedModel{Key: d.Key, Ref: s.o.Model, From: "--model", Layer: "flag"}
		}
	}
	return out
}

// savedOf is d as the page shows it: a file in the page's form of paths.
func (s *service) savedOf(d catalog.SavedDefault) wire.SavedModel {
	from := d.Source
	if !strings.HasPrefix(from, "env:") {
		from = s.display(from)
	}
	return wire.SavedModel{Key: d.Key, Ref: d.Ref, From: from, Layer: d.Kind}
}

// handleDefault is POST /api/models/default: keep a model as the default of a role in the user's configuration, so that a start of
// the harness, a restarted team and a new session read it. It answers with the default the next start reads for that role and, when a
// layer above the user's file (a project's file, SLEIPNIR_MODEL) still outranks it, a sentence that says so. Repeating it changes nothing.
func (s *service) handleDefault(w http.ResponseWriter, r *http.Request) {
	var req wire.DefaultRequest
	if !web.DecodeJSON(w, r, &req) {
		return
	}
	req.Ref, req.Role = strings.TrimSpace(req.Ref), strings.TrimSpace(req.Role)
	if _, _, ok := config.SplitModelRef(req.Ref); !ok || len(req.Ref) > 300 || strings.ContainsAny(req.Ref, " \t\n\x00") {
		web.WriteError(w, fail(http.StatusBadRequest, "bad_request", "a model is named provider/model (see the catalogue)"))
		return
	}
	if !catalog.ValidRole(req.Role) {
		web.WriteError(w, fail(http.StatusBadRequest, "bad_request", "a role is named in lower case: manager, backend, judge..."))
		return
	}
	if req.Role == "" {
		req.Role = "default"
	}
	var tc *tabCtx
	if s.host != nil && s.host.Active() != "" {
		tc, _ = s.tabByID(s.host.Active())
	}
	note := s.commentsNote()
	path, err := catalog.SaveDefault(s.o.Home, req.Role, req.Ref)
	if err != nil {
		web.Logf(r, "default model not saved")
		web.WriteError(w, fail(http.StatusUnprocessableEntity, "rejected", "your configuration file could not be changed (it does not load: see `sleipnir config`)"))
		return
	}
	web.Logf(r, "default model of %s saved", req.Role)
	if s.commentsNote() != "" {
		note = "" // nothing was written: the comments are still there
	}
	out := wire.DefaultReply{OK: true, Role: req.Role, File: s.display(path), Note: note}
	if cfg, rep, err := s.savedLoad(tc); err == nil {
		out.Saved = s.savedOf(catalog.DefaultOf(cfg, rep, req.Role))
		switch {
		case out.Saved.Ref != req.Ref:
			out.Over = "saved, but " + firstNonEmpty(out.Saved.From, "another layer") + " sets " + out.Saved.Key + " to " + out.Saved.Ref + ", and it wins over your file: change or remove it there"
		case s.o.Model != "" && s.o.Model != req.Ref && out.Saved.Key == "models.default":
			out.Over = "saved; this server was started with --model " + s.o.Model + ", which its new sessions start on: start it without --model to use your file"
		}
	}
	reply(w, out, nil)
}

// modelsReply is wire.ModelsView with ConfigNote: when the user's configuration file has comments, the sentence that warns that a
// change made from this page (a star) rewrites the file without them.
type modelsReply struct {
	wire.ModelsView
	ConfigNote string `json:"configNote,omitempty"`
}

// commentsNote warns that config.Save does not keep the comments of the user's configuration file, when it has any; "" otherwise.
func (s *service) commentsNote() string {
	path := config.UserConfigPath(s.o.Home)
	b, err := os.ReadFile(path)
	if err != nil || len(b) == 0 || json.Valid(b) {
		return ""
	}
	return s.display(path) + " has comments (or trailing commas); a change made from this page rewrites it as plain JSON, without them"
}

// modelsView builds the page's view of a catalogue read: the rows the filter keeps (favourites first, then by reference), the
// favourites, why some catalogues could not be read, and the roles, role models and effort levels of the tab.
func (s *service) modelsView(res catalog.Result, f catalog.Filter, cfg *config.Config, tc *tabCtx) wire.ModelsView {
	fav := catalog.Favorites(cfg)
	v := wire.ModelsView{Models: []wire.ModelRow{}, Favs: []string{}, RoleModels: map[string]string{}, Embedders: []string{}, FetchedAt: res.AsOf.UnixMilli()}
	if cfg != nil {
		v.Favs = append(v.Favs, cfg.Models.Favorites...)
	}
	keyless := map[string]bool{}
	for i, e := range res.Entries {
		row := res.Rows[i]
		if s.needsKey(cfg, row.Provider, keyless) {
			continue
		}
		if e.IsEmbedding() {
			v.Embedders = append(v.Embedders, row.Ref)
		}
		if !f.Keep(e, fav) {
			continue
		}
		v.Models = append(v.Models, wire.ModelRow{
			Ref: row.Ref, Provider: row.Provider, Ctx: row.Context, In: row.In, Out: row.Out, Cached: row.Cached,
			Tools: row.Tools, Reasoning: row.Reasoning, Fav: fav[row.Ref], PriceKnown: row.PriceKnown(), Plan: row.Plan,
		})
	}
	sort.SliceStable(v.Models, func(i, j int) bool {
		if v.Models[i].Fav != v.Models[j].Fav {
			return v.Models[i].Fav
		}
		return v.Models[i].Ref < v.Models[j].Ref
	})
	sort.Strings(v.Embedders)
	refs := make([]string, 0, len(v.Models))
	for _, m := range v.Models {
		refs = append(refs, m.Ref)
	}
	v.Suggested = session.SuggestedModel(cfg, refs)
	for _, err := range res.Errors {
		v.Errors = append(v.Errors, cleanError(err))
	}
	for _, name := range sortedKeys(keyless) {
		if keyless[name] {
			v.Errors = append(v.Errors, name+": no key, so its models are not offered; add one in Settings › Providers")
		}
	}
	roles := swarm.BuiltinRoles()
	if tc != nil && tc.sess != nil && len(tc.sess.Roles) > 0 {
		roles = tc.sess.Roles
	}
	v.Roles, v.RoleOrder = roleInfos(roles)
	model := ""
	if tc != nil && tc.sess != nil {
		for _, rm := range tc.sess.RoleModels() {
			v.RoleModels[rm.Role] = rm.Model
		}
		model = tc.sess.ModelRef()
		if def, ok := v.RoleModels["default"]; ok {
			v.RoleModels["manager"] = firstNonEmpty(v.RoleModels["manager"], def)
		}
	} else if cfg != nil {
		for role, m := range cfg.Models.Roles {
			v.RoleModels[role] = m
		}
		if cfg.Models.Default != "" {
			v.RoleModels["default"] = cfg.Models.Default
			v.RoleModels["manager"] = firstNonEmpty(v.RoleModels["manager"], cfg.Models.Default)
		}
		model = cfg.Models.Default
	}
	v.Efforts = effortsFor(model)
	return v
}

// needsKey reports whether provider takes a key and none is held, so that its models (a public catalogue lists them all the same)
// are not offered for a session that could not use them. seen caches the answer per provider; a provider left out is marked true.
func (s *service) needsKey(cfg *config.Config, provider string, seen map[string]bool) bool {
	if v, ok := seen[provider]; ok {
		return v
	}
	missing := false
	if cfg != nil && provider != "" {
		if p, ok := session.LookupProvider(cfg, provider); ok && p.Auth != config.AuthChatGPTPlan {
			if _, env, _ := session.ProviderInfo(cfg, provider); env != "" {
				missing = !catalog.StatusOf(s.o.Home, catalog.KeyProvider{Name: provider, Env: env}).Present
			}
		}
	}
	seen[provider] = missing
	return missing
}

// sortedKeys returns the keys of m in order.
func sortedKeys(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// firstNonEmpty returns a when it is not empty, else b.
func firstNonEmpty(a, b string) string {
	if a != "" {
		return a
	}
	return b
}

// effortsFor lists the effort levels the page offers for a model: "default" and the levels the model is known to accept
// (provider.ModelEfforts), or every level when that is not known (the harness then maps a level to the closest the endpoint takes).
func effortsFor(ref string) []string {
	_, id, ok := config.SplitModelRef(ref)
	if !ok {
		id = ref
	}
	if lv, known := provider.ModelEfforts(id); known {
		return append([]string{"default"}, lv...)
	}
	return append([]string(nil), effortLevels...)
}

// roleInfos describes the roles: their name, short code, whether they are read-only, and a line; the order is the built-in roles in
// roleOrder, then the others by name (the manager and the mailman are not in the order: the page adds the manager first, and the
// mailman is a service).
func roleInfos(rs swarm.Roles) ([]wire.RoleInfo, []string) {
	names := rs.Names()
	sort.Strings(names)
	var infos []wire.RoleInfo
	for _, n := range names {
		role := rs[n]
		desc := builtinRoleText[n]
		if desc == "" {
			desc = firstSentence(role.Pin)
		}
		infos = append(infos, wire.RoleInfo{Name: n, Code: role.Short, RO: role.ReadOnly, Desc: desc})
	}
	var order []string
	seen := map[string]bool{}
	for _, n := range roleOrder {
		if _, ok := rs[n]; ok {
			order = append(order, n)
			seen[n] = true
		}
	}
	for _, n := range names {
		if !seen[n] && n != "manager" && n != swarm.MailmanRoleName {
			order = append(order, n)
		}
	}
	return infos, order
}

// firstSentence is the first sentence of a text, on one line and at most 80 characters, sanitized.
func firstSentence(s string) string {
	s = strings.Join(strings.Fields(s), " ")
	if i := strings.Index(s, ". "); i >= 0 {
		s = s[:i+1]
	}
	return provider.SanitizeText(s, 80)
}

// cleanError is an error of a catalogue or a server as one line the page can show: credentials withheld, control characters removed,
// at most 300 characters.
func cleanError(err error) string {
	if err == nil {
		return ""
	}
	return provider.SanitizeText(config.ScrubText(err.Error()), 300)
}

// favReply is the answer of POST /api/models/fav: the model, its state now and the favourites.
type favReply struct {
	OK   bool     `json:"ok"`
	Ref  string   `json:"ref"`
	On   bool     `json:"on"`
	Favs []string `json:"favs"`
	// Note says that the change rewrote the configuration file without the comments it had.
	Note string `json:"note,omitempty"`
}

// handleFav is POST /api/models/fav: star (on) or unstar a model in the user's configuration. Repeating a request changes nothing.
func (s *service) handleFav(w http.ResponseWriter, r *http.Request) {
	var req wire.FavRequest
	if !web.DecodeJSON(w, r, &req) {
		return
	}
	if _, _, ok := config.SplitModelRef(req.Ref); !ok || len(req.Ref) > 300 {
		web.WriteError(w, fail(http.StatusBadRequest, "bad_request", "a model is named provider/model (see the catalogue)"))
		return
	}
	note := s.commentsNote()
	favs, err := catalog.SetFavorite(s.o.Home, req.Ref, req.On)
	if err != nil {
		web.Logf(r, "favourites not saved")
		web.WriteError(w, fail(http.StatusUnprocessableEntity, "rejected", "your configuration file could not be changed (it does not load: see `sleipnir config`)"))
		return
	}
	if favs == nil {
		favs = []string{}
	}
	if s.commentsNote() != "" {
		note = "" // nothing was written: the comments are still there
	}
	reply(w, favReply{OK: true, Ref: req.Ref, On: req.On, Favs: favs, Note: note}, nil)
}
