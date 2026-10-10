package settings

import (
	"crypto/sha256"
	"net/http"
	"strings"

	"github.com/anemos-labs/sleipnir/internal/catalog"
	"github.com/anemos-labs/sleipnir/internal/chatgptauth"
	"github.com/anemos-labs/sleipnir/internal/config"
	"github.com/anemos-labs/sleipnir/internal/harden"
	"github.com/anemos-labs/sleipnir/internal/session"
	"github.com/anemos-labs/sleipnir/internal/web"
	"github.com/anemos-labs/sleipnir/internal/web/wire"
)

// providersNote closes the Providers page.
const providersNote = "Keys are read from the environment or kept by `sleipnir login` in ~/.sleipnir/auth.json (mode 0600); a variable set in the environment wins over a stored key. Local servers need no key."

// Key states of ProviderRow.Key.
const (
	keyEnv       = "env"
	keyStored    = "stored"
	keyNone      = "none"
	keySignedIn  = "signed in"
	keyNotNeeded = "not needed"
	keyUnknown   = "unknown"
)

// providerConfig is the configuration whose providers the page lists: the active tab's (its session's own, when it runs), else the
// server's home with the working directory's project read as untrusted.
func (s *service) providerConfig() (*config.Config, *tabCtx) {
	var tc *tabCtx
	if s.host != nil && s.host.Active() != "" {
		tc, _ = s.tabByID(s.host.Active())
	}
	if tc != nil && tc.info.Config != nil {
		return tc.info.Config, tc
	}
	opts := config.LoadOpts{Home: s.o.Home, UntrustedProject: true}
	if tc != nil {
		opts.Root, opts.Cwd = tc.root, tc.cwd
	}
	cfg, _, err := config.Load(opts)
	if err != nil {
		cfg = config.Defaults()
	}
	return cfg, tc
}

// providersView lists every provider the harness knows (the built-in ones, the local servers and the configured ones) with whether
// it can be used now; keys are described, never shown.
func (s *service) providersView() wire.ProvidersView {
	cfg, tc := s.providerConfig()
	v := wire.ProvidersView{Providers: []wire.ProviderRow{}, Note: providersNote}
	used := usedBy(tc)
	for _, name := range session.ProviderNames(cfg) {
		v.Providers = append(v.Providers, s.providerRow(cfg, name, used[name]))
	}
	return v
}

// providerRow describes one provider.
func (s *service) providerRow(cfg *config.Config, name, used string) wire.ProviderRow {
	p, _ := session.LookupProvider(cfg, name)
	base, env, _ := session.ProviderInfo(cfg, name)
	row := wire.ProviderRow{
		ID: name, Name: displayName(name), Base: config.RedactURL(base), Env: env, Dialect: p.EffectiveDialect(),
		Recommended: name == "heimdall", UsedBy: used,
	}
	switch {
	case p.Auth == config.AuthChatGPTPlan:
		row.Note = "billed to the ChatGPT plan; its models have no per-token price"
		if ok, who := catalog.ChatGPTStatus(s.o.Home); ok {
			row.Key, row.State, row.SignedIn = keySignedIn, "signed in", true
			row.Who = cleanText(who)
			row.KeyWhere = s.display(chatgptauth.Path(s.o.Home)) + " (mode 0600)"
		} else {
			row.Key, row.State = keyNone, "not signed in"
		}
	case env == "":
		row.Key, row.State = keyNotNeeded, "no key needed"
	default:
		st := catalog.StatusOf(s.o.Home, catalog.KeyProvider{Name: name, Env: env})
		switch st.Source {
		case catalog.SourceEnv:
			row.Key, row.State, row.KeyWhere = keyEnv, "connected", "the environment variable "+env
		case catalog.SourceStored:
			row.Key, row.State, row.KeyWhere = keyStored, "connected", s.display(config.AuthPath(s.o.Home))+" (mode 0600)"
		case catalog.SourceUnknown:
			row.Key, row.State = keyUnknown, "a key is there; where it comes from cannot be told"
		default:
			row.Key, row.State = keyNone, "no key"
		}
	}
	return row
}

// displayName is a provider's name as a title: its first letter upper-cased.
func displayName(name string) string {
	if name == "" {
		return name
	}
	return strings.ToUpper(name[:1]) + name[1:]
}

// usedBy says, per provider, which roles of the tab's session run on it: "the manager (ref)", "<role> (ref)".
func usedBy(tc *tabCtx) map[string]string {
	out := map[string]string{}
	if tc == nil || tc.sess == nil {
		return out
	}
	for _, rm := range tc.sess.RoleModels() {
		prov, _, ok := config.SplitModelRef(rm.Model)
		if !ok {
			continue
		}
		who := rm.Role
		switch rm.Role {
		case "default", "manager":
			who = "the manager"
		case session.CompactorRole:
			who = "the compactor"
		}
		item := who + " (" + rm.Model + ")"
		if strings.Contains(out[prov], item) {
			continue
		}
		if out[prov] != "" {
			out[prov] += ", "
		}
		out[prov] += item
	}
	return out
}

// handleProviders is GET /api/providers.
func (s *service) handleProviders(w http.ResponseWriter, r *http.Request) {
	reply(w, s.providersView(), nil)
}

// handleRecheck is POST /api/providers/recheck (the sign-in dialog's "I ran it: check again", decision D-01): the keys `sleipnir login`
// stored or `sleipnir logout` removed in a terminal since are taken up by this process, then the providers are listed again.
func (s *service) handleRecheck(w http.ResponseWriter, r *http.Request) {
	s.reloadStored()
	reply(w, s.providersView(), nil)
}

// rememberStored records which keys auth.json holds now, by a hash of each (never the key itself).
func (s *service) rememberStored() {
	m, _ := config.StoredKeys(s.o.Home)
	seen := make(map[string][32]byte, len(m))
	for name, v := range m {
		seen[name] = sha256.Sum256([]byte(strings.TrimSpace(v)))
	}
	s.storedMu.Lock()
	s.stored = seen
	s.storedMu.Unlock()
}

// reloadStored takes up the changes made to auth.json since it was last read: new or changed keys are handed to harden.Provide (a
// variable of the same name in the environment still wins), and a key that was removed is forgotten when this process holds exactly
// the removed value.
func (s *service) reloadStored() {
	m, err := config.StoredKeys(s.o.Home)
	if err != nil {
		return
	}
	s.storedMu.Lock()
	before := s.stored
	s.storedMu.Unlock()
	for name, v := range m {
		v = strings.TrimSpace(v)
		if h, ok := before[name]; ok && h == sha256.Sum256([]byte(v)) {
			continue
		}
		harden.Provide(name, v)
	}
	for name, h := range before {
		if _, still := m[name]; still {
			continue
		}
		if held := strings.TrimSpace(harden.Secret(name)); held != "" && sha256.Sum256([]byte(held)) == h {
			harden.Provide(name, "")
		}
	}
	s.rememberStored()
}

// handleSignOut is POST /api/providers/{name}/signout: the stored key is removed from auth.json and from this process; a key from the
// environment is refused (409 env: it is not the page's to remove); the ChatGPT plan's sign-in is ended (the issuer is asked to revoke
// it, best effort). Signing out of a provider without a key changes nothing.
func (s *service) handleSignOut(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	if !nameRe.MatchString(name) {
		web.WriteError(w, fail(http.StatusBadRequest, "bad_name", "the provider name is not valid"))
		return
	}
	cfg, _ := s.providerConfig()
	p, ok := session.LookupProvider(cfg, name)
	if !ok {
		web.WriteError(w, fail(http.StatusNotFound, "not_found", "there is no provider of that name"))
		return
	}
	if p.Auth == config.AuthChatGPTPlan {
		if connected, _ := catalog.ChatGPTStatus(s.o.Home); connected {
			if err := s.chatgptSignOut(r, s.o.Home); err != nil {
				web.Logf(r, "chatgpt sign-out: the issuer was not told")
			}
		}
		reply(w, s.providerRow(cfg, name, ""), nil)
		return
	}
	_, env, _ := session.ProviderInfo(cfg, name)
	if env == "" {
		web.WriteError(w, fail(http.StatusConflict, "nothing", "this provider takes no key"))
		return
	}
	st := catalog.StatusOf(s.o.Home, catalog.KeyProvider{Name: name, Env: env})
	switch st.Source {
	case catalog.SourceEnv:
		web.WriteError(w, fail(http.StatusConflict, "env", "the key comes from the environment variable "+env+": unset it there"))
		return
	case catalog.SourceStored, catalog.SourceUnknown:
		if err := catalog.ForgetKey(s.o.Home, env); err != nil {
			web.Logf(r, "auth.json not written")
			web.WriteError(w, fail(http.StatusInternalServerError, "internal", "the stored key could not be removed"))
			return
		}
		s.rememberStored()
	}
	reply(w, s.providerRow(cfg, name, ""), nil)
}

// handleKey is POST /api/providers/{name}/key (served only when KeyRoutes is true; decision D-01 keeps it off): store a provider's
// key typed in the browser, with the confirmation key:<name>; with check, one small request tries it and a refused key is not kept
// (422 rejected). The key is read from the body only, never logged, never returned.
func (s *service) handleKey(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	if !nameRe.MatchString(name) {
		web.WriteError(w, fail(http.StatusBadRequest, "bad_name", "the provider name is not valid"))
		return
	}
	if !s.srv.RequireConfirm(w, r, "key:"+name) {
		return
	}
	var req wire.KeySaveRequest
	if !web.DecodeJSON(w, r, &req) {
		return
	}
	key := strings.TrimSpace(req.Key)
	if key == "" || len(key) > 4096 || strings.ContainsAny(key, " \t\r\n") || (req.Provider != "" && req.Provider != name) {
		web.WriteError(w, fail(http.StatusBadRequest, "bad_key", "a key is one word, as the provider shows it"))
		return
	}
	cfg, _ := s.providerConfig()
	_, env, ok := session.ProviderInfo(cfg, name)
	if !ok || env == "" {
		web.WriteError(w, fail(http.StatusNotFound, "not_found", "that provider takes no key"))
		return
	}
	if err := catalog.StoreKey(s.o.Home, env, key); err != nil {
		web.Logf(r, "auth.json not written")
		web.WriteError(w, fail(http.StatusInternalServerError, "internal", "the key could not be stored"))
		return
	}
	s.rememberStored()
	if req.Check {
		ref := firstToolModel(r, cfg, name, s)
		if ref != "" {
			if kc := catalog.CheckKey(r.Context(), cfg, ref); kc.Refused {
				if catalog.StatusOf(s.o.Home, catalog.KeyProvider{Name: name, Env: env}).Source == catalog.SourceStored {
					_ = catalog.ForgetKey(s.o.Home, env)
					s.rememberStored()
				}
				web.WriteError(w, fail(http.StatusUnprocessableEntity, "rejected", name+" did not accept that key ("+kc.Reason+"); it was not kept"))
				return
			}
		}
	}
	reply(w, s.providerRow(cfg, name, ""), nil)
}

// firstToolModel is the first model of a provider's catalogue that takes tools: the one a key check asks (the catalogue is often
// public and says nothing about the key).
func firstToolModel(r *http.Request, cfg *config.Config, name string, s *service) string {
	base, _, _ := session.ProviderInfo(cfg, name)
	es, _ := catalog.FetchEntries(r.Context(), s.o.Home, []catalog.Source{{Name: name, Base: base}})
	for _, e := range es {
		if e.SupportsTools() {
			return e.Ref
		}
	}
	return ""
}
