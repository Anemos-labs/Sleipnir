// Package settings serves the Settings pages of `sleipnir web`: the model catalogue and the
// favourites, the permission rules in force with the origin of each, trust in a project's own files, the MCP servers, skills,
// commands and hooks, providers and their sign-in state, and the configuration layers with the provenance of every value.
//
// What it reads is the person's own configuration and state, and what it returns goes to a browser page, so every DTO is an
// allow-list: provider keys, stored or held credentials and ChatGPT tokens are never read into a response (only whether a key is
// there and where it comes from), and configuration values go through config.Redact's rules (header, environment and URL-query
// values withheld, secret-shaped strings and tokens inside command lines withheld). Untrusted text (a file name, an MCP server's
// error) is sent as JSON and escaped by the page.
//
// Writes that raise privilege need a confirmation whose scope names what it covers (docs/WEB-API.md): trusting a project's files
// (`trust:<d16 of {dir, digest}>`, issued by the trust challenge) and approving an MCP server (`mcp.approve:<d16 of {root, name,
// fingerprint}>`). Writes to the person's files (the user configuration, auth.json, trust.json, mcp-approvals.json) are serialised
// per file within the process and read the file again before they write it (config.WriteLock); config.Save writes canonical JSON,
// so the comments of a configuration file that a page changes are not kept.
//
// The routes that take a provider key from the browser or start a ChatGPT sign-in are not served: sign-in stays in the terminal
// (`sleipnir login`) and the page never holds a provider key, so KeyRoutes is false. The service they would call is built and
// tested (catalog.StoreKey, catalog.CheckKey, the handler of POST /api/providers/{name}/key).
package settings

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/anemos-labs/sleipnir/internal/catalog"
	"github.com/anemos-labs/sleipnir/internal/config"
	"github.com/anemos-labs/sleipnir/internal/session"
	"github.com/anemos-labs/sleipnir/internal/trust"
	"github.com/anemos-labs/sleipnir/internal/web"
	"github.com/anemos-labs/sleipnir/internal/web/seam"
	"github.com/anemos-labs/sleipnir/internal/web/wire"
)

// Options configure the settings routes: the user's home, the program version (update checks) and the executable (runs).
type Options struct {
	Home    string
	Version string
	Self    string
}

// KeyRoutes says whether POST /api/providers/{name}/key (a provider key typed in the browser) is served. It is false: sign-in stays
// in the terminal and the page never holds a provider key. The handler and its tests remain, so that serving it is one constant.
const KeyRoutes = false

// keyMaxBody is the body cap of the key route.
const keyMaxBody = 8 << 10

// mcpTester starts MCP servers to list their tools (session.MCPTest).
type mcpTester func(ctx context.Context, home, dir string, names []string, trustProject bool) (*session.MCPTestResult, error)

// service holds what the routes share: the server (confirmations, logging), the host (tabs), the options, and the hooks that tests
// replace (the clock, the catalogue fetch, the MCP test, the ChatGPT sign-out).
type service struct {
	srv  *web.Server
	host seam.Host
	o    Options

	now            func() time.Time
	fetch          func(ctx context.Context, o catalog.Options) catalog.Result
	mcpTest        mcpTester
	chatgptSignOut func(r *http.Request, home string) error

	fetchMu sync.Mutex // one catalogue refresh at a time
	mcpSem  chan struct{}

	challenges *challenges
	storedMu   sync.Mutex
	stored     map[string][32]byte // auth.json as last seen: variable name -> SHA-256 of the stored key (never the key)
}

// Register adds the settings routes to srv, resolving tabs through h.
func Register(srv *web.Server, h seam.Host, o Options) {
	newService(srv, h, o).register()
}

// newService builds the service with the real dependencies.
func newService(srv *web.Server, h seam.Host, o Options) *service {
	if o.Home == "" {
		o.Home, _ = os.UserHomeDir()
	}
	s := &service{
		srv: srv, host: h, o: o, now: time.Now,
		mcpSem: make(chan struct{}, 2), challenges: newChallenges(),
	}
	s.fetch = catalog.FetchResult
	s.mcpTest = session.MCPTest
	s.chatgptSignOut = func(r *http.Request, home string) error { return catalog.ChatGPTSignOut(r.Context(), home) }
	s.rememberStored()
	return s
}

// register adds the routes.
func (s *service) register() {
	srv := s.srv
	srv.HandleFunc("GET /api/models", s.handleModels, web.RouteOpts{WriteTimeout: 60 * time.Second})
	srv.HandleFunc("POST /api/models/fav", s.handleFav, web.RouteOpts{})
	srv.HandleFunc("GET /api/sessions/{id}/permissions", s.handlePermissions, web.RouteOpts{})
	srv.HandleFunc("GET /api/sessions/{id}/trust", s.handleTrustView, web.RouteOpts{})
	srv.HandleFunc("GET /api/trust/challenge", s.handleTrustChallenge, web.RouteOpts{})
	srv.HandleFunc("POST /api/trust", s.handleTrust, web.RouteOpts{})
	srv.HandleFunc("GET /api/sessions/{id}/mcp", s.handleMCPView, web.RouteOpts{})
	srv.HandleFunc("POST /api/sessions/{id}/mcp/{name}/approve", s.handleMCPApprove, web.RouteOpts{NoBody: true})
	srv.HandleFunc("POST /api/sessions/{id}/mcp/{name}/revoke", s.handleMCPRevoke, web.RouteOpts{NoBody: true})
	srv.HandleFunc("POST /api/sessions/{id}/mcp/{name}/test", s.handleMCPTest, web.RouteOpts{NoBody: true, WriteTimeout: 60 * time.Second})
	srv.HandleFunc("POST /api/sessions/{id}/mcp/{name}/reconnect", s.handleMCPReconnect, web.RouteOpts{NoBody: true})
	srv.HandleFunc("GET /api/sessions/{id}/skills", s.handleSkills, web.RouteOpts{})
	srv.HandleFunc("GET /api/providers", s.handleProviders, web.RouteOpts{})
	srv.HandleFunc("POST /api/providers/recheck", s.handleRecheck, web.RouteOpts{NoBody: true})
	srv.HandleFunc("POST /api/providers/{name}/signout", s.handleSignOut, web.RouteOpts{NoBody: true})
	srv.HandleFunc("GET /api/sessions/{id}/config", s.handleConfig, web.RouteOpts{})
	if KeyRoutes {
		s.registerKeyRoute()
	}
}

// registerKeyRoute adds POST /api/providers/{name}/key (KeyRoutes).
func (s *service) registerKeyRoute() {
	s.srv.HandleFunc("POST /api/providers/{name}/key", s.handleKey, web.RouteOpts{MaxBody: keyMaxBody})
}

// ---- errors and parameters --------------------------------------------------------------------------------------------------

// fail is a refusal in the shape of the API's error body (wire.Error).
func fail(status int, code, msg string) error {
	return &wire.Error{Status: status, Code: code, Msg: msg}
}

// failDetail is a refusal with structured detail.
func failDetail(status int, code, msg string, detail any) error {
	return &wire.Error{Status: status, Code: code, Msg: msg, Detail: detail}
}

// reply writes v as a 200 JSON answer, or the error.
func reply(w http.ResponseWriter, v any, err error) {
	if err != nil {
		web.WriteError(w, err)
		return
	}
	_ = web.WriteJSON(w, http.StatusOK, v)
}

var (
	tabIDRe = regexp.MustCompile(`^[a-z0-9-]{1,40}$`)
	nameRe  = regexp.MustCompile(`^[A-Za-z0-9_.-]{1,64}$`)
)

// tabCtx is a tab and what the settings pages need of it: its session (nil while it restarts), the session's settings, and the
// directory, project root and trust the read models use.
type tabCtx struct {
	tab     seam.Tab
	sess    *session.Session
	info    session.SettingsInfo
	cwd     string
	root    string
	trusted bool
}

// tabOf resolves the tab of the request's {id}: 400 for an id of the wrong shape, 404 for one that is not live.
func (s *service) tabOf(r *http.Request) (*tabCtx, error) { return s.tabByID(r.PathValue("id")) }

// tabByID resolves a tab by its id, as tabOf does.
func (s *service) tabByID(id string) (*tabCtx, error) {
	if !tabIDRe.MatchString(id) {
		return nil, fail(http.StatusBadRequest, "bad_request", "the session id is not valid")
	}
	if s.host == nil {
		return nil, fail(http.StatusNotFound, "no_session", "there is no such session")
	}
	t, ok := s.host.Tab(id)
	if !ok || t == nil {
		return nil, fail(http.StatusNotFound, "no_session", "there is no such session")
	}
	tc := &tabCtx{tab: t}
	if acc := t.Access(); acc != nil {
		tc.sess = acc.Session()
	}
	if tc.sess != nil {
		tc.info = tc.sess.SettingsInfo()
		tc.cwd, tc.root, tc.trusted = tc.info.Cwd, tc.info.Root, tc.info.TrustProject
	}
	if tc.cwd == "" {
		tc.cwd = t.Summary().Cwd
	}
	if tc.root == "" && tc.cwd != "" {
		tc.root, _ = config.FindRoot(tc.cwd)
	}
	if tc.sess == nil && tc.cwd != "" {
		tc.trusted = s.ledgerTrusts(tc.cwd)
	}
	return tc, nil
}

// home is the home directory of a tab's session, or the server's.
func (s *service) homeOf(tc *tabCtx) string {
	if tc != nil && tc.info.Home != "" {
		return tc.info.Home
	}
	return s.o.Home
}

// loadOpts are the configuration options of a tab's project: its root and directory, the home, and the project's settings applied
// only when the session trusts the project.
func (s *service) loadOpts(tc *tabCtx) config.LoadOpts {
	return config.LoadOpts{Root: tc.root, Cwd: tc.cwd, Home: s.homeOf(tc), UntrustedProject: !tc.trusted}
}

// ledgerTrusts reports whether the trust ledger holds a yes for exactly the current files of the project around cwd.
func (s *service) ledgerTrusts(cwd string) bool {
	fp, err := session.ProjectFootprint(s.o.Home, cwd)
	if err != nil || fp.Empty() {
		return false
	}
	st, _ := s.ledger().Check(cwd, fp)
	return st == trust.Trusted
}

// ledger opens the trust ledger of the server's home.
func (s *service) ledger() *trust.Ledger { return trust.OpenLedger(session.TrustLedgerPath(s.o.Home)) }

// ---- confirmation scopes ----------------------------------------------------------------------------------------------------

// d16 is the first 16 hex characters of the SHA-256 of the canonical JSON of v (sorted keys, no spaces, no HTML escaping): the hash
// that confirmation scopes carry.
func d16(v map[string]string) string {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	_ = enc.Encode(v) // a map[string]string always encodes; the keys are sorted
	sum := sha256.Sum256(bytes.TrimRight(buf.Bytes(), "\n"))
	return hex.EncodeToString(sum[:])[:16]
}

// trustScope is the confirmation scope of trusting dir's files with the given digest.
func trustScope(dir, digest string) string {
	return "trust:" + d16(map[string]string{"dir": dir, "digest": digest})
}

// mcpScope is the confirmation scope of approving an MCP entry.
func mcpScope(root, name, fingerprint string) string {
	return "mcp.approve:" + d16(map[string]string{"root": root, "name": name, "fingerprint": fingerprint})
}

// ---- paths shown to the page ------------------------------------------------------------------------------------------------

// display shows a path of the person's own files: under the home directory as ~/..., otherwise as it is.
func (s *service) display(p string) string {
	if p == "" || s.o.Home == "" || !filepath.IsAbs(p) {
		return p
	}
	if rel, err := filepath.Rel(s.o.Home, p); err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		if rel == "." {
			return "~"
		}
		return "~/" + filepath.ToSlash(rel)
	}
	return p
}

// expand turns a directory the page sent back (absolute, or ~/... as display shows it) into a clean absolute path; ok is false for
// anything else.
func (s *service) expand(dir string) (string, bool) {
	if len(dir) == 0 || len(dir) > 4096 || strings.ContainsRune(dir, 0) {
		return "", false
	}
	switch {
	case dir == "~":
		dir = s.o.Home
	case strings.HasPrefix(dir, "~/"):
		dir = filepath.Join(s.o.Home, filepath.FromSlash(dir[2:]))
	}
	if !filepath.IsAbs(dir) || s.o.Home == "" && strings.HasPrefix(dir, "~") {
		return "", false
	}
	return filepath.Clean(dir), true
}
