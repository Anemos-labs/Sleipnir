package session

import (
	"context"
	"path/filepath"
	"strings"
	"time"

	"github.com/anemos-labs/sleipnir/internal/chatgptauth"
	"github.com/anemos-labs/sleipnir/internal/cost"
	"github.com/anemos-labs/sleipnir/internal/provider"
	"github.com/anemos-labs/sleipnir/internal/provider/gateway"
)

// Where the session's description of a model came from (modelRecord.Source), and where its
// context window came from (modelRecord.ContextSource).
const (
	sourceCatalogue  = "catalogue"  // the provider's own list: an endpoint's catalogue entry, a ChatGPT plan's model list
	sourceTable      = "table"      // the built-in table
	sourceGiven      = "given"      // the caller of session.New supplied it
	sourceFallback   = "fallback"   // nothing knew the model: conservative estimates
	sourceConfigured = "configured" // the window only: the provider's options.context_window
	sourceOverride   = "override"   // the window only: Options.ContextWindow (--context-window), the main model's
	sourceServer     = "server"     // the window only: what the server of this machine that serves the model says it serves it with
)

// maxRecordedModels bounds the models one session.start lists.
const maxRecordedModels = 24

// planListTimeout bounds asking a ChatGPT plan for its model list while a session starts.
const planListTimeout = 3 * time.Second

// described is a model the session may call, with where its description (src) and its
// context window (win) came from.
type described struct {
	m        cost.Model
	src, win string
}

// modelRecord is what session.start says about a model the session may call: the numbers the
// run was priced with. A log is read elsewhere and later (the inspector, a training pipeline),
// where the endpoint's catalogue may not be at hand; without this it would price a marketplace
// model with a generic estimate hundreds of times off the gateway's bill.
//
// Source says where the prices came from, ContextSource where the context window came from: a
// model the table prices can take its window from its provider ("source": "table",
// "context_source": "catalogue").
//
// Plan marks a model reached through a subscription plan (a ChatGPT sign-in): its prices are then
// the API's list prices for the same model (zero when they are not known), which show what the
// usage would cost on the API; the plan bills none of it, and no budget counts it.
type modelRecord struct {
	Source           string  `json:"source"`
	Context          int     `json:"context"`
	ContextSource    string  `json:"context_source"`
	InputPerM        float64 `json:"input_per_m"`
	OutputPerM       float64 `json:"output_per_m"`
	CacheReadPerM    float64 `json:"cache_read_per_m"`
	CacheWrite5mPerM float64 `json:"cache_write_5m_per_m"`
	CacheWrite1hPerM float64 `json:"cache_write_1h_per_m"`
	ExplicitCache    bool    `json:"explicit_cache,omitempty"`
	TTLSeconds       int     `json:"ttl_s,omitempty"`
	Plan             bool    `json:"plan,omitempty"`
}

// describe refines the harness's description of a model of p from what its provider reports,
// unless the session is offline, and notes the answer for session.start. Every model a session
// can call goes through it: the main one, the roles', the team slots', the compactor's and the
// one /model switches to.
//
//   - A model the built-in table does not know takes the endpoint's catalogue entry (exact
//     prices and window).
//   - A model the table knows keeps the table's prices and takes the context window its
//     provider reports: the endpoint's catalogue entry, or a ChatGPT plan's model list.
//   - A model the table does not know on a server of this machine whose catalogue gives no
//     window takes the window the server says it serves the model with (servedWindow), where
//     the server says.
//   - The provider's options.context_window, when it is set, is the window whatever the
//     provider reports.
//
// A list that cannot be read leaves the table's window (or the fallback's) in place, and is not
// asked again in this session.
func (s *Session) describe(ctx context.Context, p provider.Provider, m cost.Model) cost.Model {
	src := sourceTable
	if m.Provider == "unknown" {
		src = sourceFallback
	}
	win := src
	configured := s.configuredWindow(p)
	unknown := m.Provider == "unknown"
	if !s.opts.Offline {
		base := p.Profile().BaseURL
		switch {
		case m.Plan:
			if configured == 0 {
				if n := s.planWindow(ctx, base, m.ID); n > 0 {
					m.ContextTokens, m.WindowGuessed, win = n, false, sourceCatalogue
				}
			}
		case m.Provider == "unknown":
			es := s.catalogue(ctx, base)
			if e, ok := enrichFrom(es, m); ok {
				m = e
				if publishesPrices(es, m.ID) {
					src = sourceCatalogue
				}
				if reportsWindow(es, m.ID) {
					win = sourceCatalogue
				}
			}
		case configured == 0:
			if e, ok := findEntry(s.catalogue(ctx, base), m.ID); ok && e.Model.ContextTokens > 0 {
				m.ContextTokens, m.WindowGuessed, win = e.Model.ContextTokens, false, sourceCatalogue
			}
		}
		if unknown && configured == 0 && win != sourceCatalogue && localBase(base) {
			if n := s.servedWindow(ctx, base, m.ID); n > 0 {
				m.ContextTokens, m.WindowGuessed, win = n, false, sourceServer
			}
		}
	}
	if configured > 0 {
		m.ContextTokens, m.WindowGuessed, win = configured, false, sourceConfigured
	}
	s.note(m, src, win)
	return m
}

// reportsWindow reports whether entries give model id a context window of their own.
func reportsWindow(entries []gateway.Entry, id string) bool {
	e, ok := findEntry(entries, id)
	return ok && e.Model.ContextTokens > 0
}

// publishesPrices reports whether entries give model id prices of their own.
func publishesPrices(entries []gateway.Entry, id string) bool {
	e, ok := findEntry(entries, id)
	return ok && !e.Unpriced
}

// configuredWindow is the window the configuration of p's provider sets
// (options.context_window), 0 when it sets none.
func (s *Session) configuredWindow(p provider.Provider) int {
	cp, ok := lookupProvider(s.cfg, p.Profile().Name)
	if !ok {
		return 0
	}
	return max(optInt(cp.Options, "context_window"), 0)
}

// cacheDir is where the providers' model lists are kept: the state directory's cache/.
func (s *Session) cacheDir() string { return filepath.Join(stateRoot(s.opts.Home), "cache") }

// catalogue is the catalogue of the endpoint at base (loadCatalog), nil when it may not be read
// (catalogueReadable), when it could not be, and when it could not be earlier in this session.
func (s *Session) catalogue(ctx context.Context, base string) []gateway.Entry {
	key := "catalogue " + base
	if !catalogueReadable(base) || s.unlisted[key] {
		return nil
	}
	es := loadCatalog(ctx, s.cacheDir(), base)
	if len(es) == 0 {
		s.markUnlisted(key)
	}
	return es
}

// planWindow is the context window the model list of the ChatGPT plan at base gives model id,
// 0 when it gives none (or there is no list to read; see planList).
func (s *Session) planWindow(ctx context.Context, base, id string) int {
	key := "plan " + base
	if !catalogueReadable(base) || s.unlisted[key] {
		return 0
	}
	ms := planList(ctx, s.cacheDir(), base)
	if len(ms) == 0 {
		s.markUnlisted(key)
		return 0
	}
	m, ok := findPlanModel(ms, id)
	if !ok || m.ContextWindow <= 0 || m.ContextWindow > cost.MaxWindowTokens {
		return 0
	}
	return m.ContextWindow
}

// markUnlisted notes that the list named key could not be read.
func (s *Session) markUnlisted(key string) {
	if s.unlisted == nil {
		s.unlisted = map[string]bool{}
	}
	s.unlisted[key] = true
}

// findPlanModel is the model of a plan's list for id: the one with that slug, else the first whose
// slug is the same once normalised (cost.Normalize).
func findPlanModel(ms []chatgptauth.Model, id string) (chatgptauth.Model, bool) {
	for _, m := range ms {
		if m.Slug == id {
			return m, true
		}
	}
	n := cost.Normalize(id)
	for _, m := range ms {
		if cost.Normalize(m.Slug) == n {
			return m, true
		}
	}
	return chatgptauth.Model{}, false
}

// planList is the model list of the ChatGPT plan signed in on this machine, as the endpoint at
// base gives it (<base>/models): the copy kept under cacheDir when it is younger than catalogTTL;
// else the list asked again (planListTimeout at most) and kept; else, when it cannot be asked, an
// older copy. It is asked only with an access token that needs no renewal
// (chatgptauth.Store.ModelsNoRenewal): a renewal cut off by the deadline could lose the rotated
// refresh token, and the session's first request renews the token anyway. nil when there is no
// sign-in and no list.
func planList(ctx context.Context, cacheDir, base string) []chatgptauth.Model {
	st, err := chatgptauth.Open(chatgptauth.Options{Path: chatgptPath(), Models: strings.TrimRight(base, "/") + "/models"})
	if err != nil {
		return nil
	}
	path := st.ListPath(cacheDir)
	kept, at, kerr := chatgptauth.LoadList(path)
	if kerr == nil && len(kept) > 0 && time.Since(at) < catalogTTL {
		return kept
	}
	fctx, cancel := context.WithTimeout(ctx, planListTimeout)
	defer cancel()
	ms, err := st.ModelsNoRenewal(fctx)
	if err != nil || len(ms) == 0 {
		return kept
	}
	_ = chatgptauth.SaveList(path, ms)
	return ms
}

// noteModel retains the first description per model ID until the recording limit is reached;
// its window came from where the description did.
func (s *Session) noteModel(m cost.Model, src string) { s.note(m, src, src) }

// note is noteModel with the source of the window (win) given apart.
func (s *Session) note(m cost.Model, src, win string) {
	if s.described == nil {
		s.described = map[string]described{}
	}
	if _, seen := s.described[m.ID]; seen || len(s.described) >= maxRecordedModels {
		return
	}
	s.described[m.ID] = described{m: m, src: src, win: win}
}

// modelRecords is the models table of session.start.
func (s *Session) modelRecords() map[string]modelRecord {
	out := make(map[string]modelRecord, len(s.described))
	for id, d := range s.described {
		m, win := d.m, d.win
		if id == s.Model.ID {
			m.ContextTokens = s.Model.ContextTokens // the main model's window may have been overridden
			if s.opts.ContextWindow > 0 {
				win = sourceOverride
			}
		}
		ttl := m.Cache.DefaultTTL()
		p := m.Price
		if m.Plan {
			p = m.ListPrice
		}
		out[id] = modelRecord{
			Source: d.src, Context: m.ContextTokens, ContextSource: win,
			InputPerM: p.InputPerM, OutputPerM: p.OutputPerM, CacheReadPerM: p.CacheReadPerM,
			CacheWrite5mPerM: p.CacheWrite5mPerM, CacheWrite1hPerM: p.CacheWrite1hPerM,
			ExplicitCache: m.Cache.Explicit, TTLSeconds: int(ttl / time.Second), Plan: m.Plan,
		}
	}
	return out
}
