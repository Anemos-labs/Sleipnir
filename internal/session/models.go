package session

import (
	"context"
	"path/filepath"
	"time"

	"github.com/reee344/sleipnir/internal/cost"
	"github.com/reee344/sleipnir/internal/provider"
)

// Where the session's description of a model came from.
const (
	sourceCatalogue = "catalogue" // the endpoint's own entry: exact prices and window
	sourceTable     = "table"     // the built-in table
	sourceGiven     = "given"     // the caller of session.New supplied it
	sourceFallback  = "fallback"  // nothing knew the model: conservative estimates
)

// maxRecordedModels bounds the models one session.start lists.
const maxRecordedModels = 24

type described struct {
	m   cost.Model
	src string
}

// modelRecord is what session.start says about a model the session may call: the numbers the
// run was priced with. A log is read elsewhere and later (the inspector, a training pipeline),
// where the endpoint's catalogue may not be at hand; without this it would price a marketplace
// model with a generic estimate hundreds of times off the gateway's bill.
type modelRecord struct {
	Source           string  `json:"source"`
	Context          int     `json:"context"`
	InputPerM        float64 `json:"input_per_m"`
	OutputPerM       float64 `json:"output_per_m"`
	CacheReadPerM    float64 `json:"cache_read_per_m"`
	CacheWrite5mPerM float64 `json:"cache_write_5m_per_m"`
	CacheWrite1hPerM float64 `json:"cache_write_1h_per_m"`
	ExplicitCache    bool    `json:"explicit_cache,omitempty"`
	TTLSeconds       int     `json:"ttl_s,omitempty"`
}

// describe refines the harness's description of a model of p from the endpoint's own catalogue
// (exact prices and context window) when nothing better is known, unless the session is
// offline, and notes the answer for session.start. Every model a session can call goes through
// it: the main one and the roles'.
func (s *Session) describe(ctx context.Context, p provider.Provider, m cost.Model) cost.Model {
	src := sourceTable
	if m.Provider == "unknown" {
		src = sourceFallback
		if !s.opts.Offline {
			if e, ok := enrichModel(ctx, filepath.Join(stateRoot(s.opts.Home), "cache"), p.Profile().BaseURL, m); ok {
				m, src = e, sourceCatalogue
			}
		}
	}
	s.noteModel(m, src)
	return m
}

func (s *Session) noteModel(m cost.Model, src string) {
	if s.described == nil {
		s.described = map[string]described{}
	}
	if _, seen := s.described[m.ID]; seen || len(s.described) >= maxRecordedModels {
		return
	}
	s.described[m.ID] = described{m: m, src: src}
}

// modelRecords is the models table of session.start.
func (s *Session) modelRecords() map[string]modelRecord {
	out := make(map[string]modelRecord, len(s.described))
	for id, d := range s.described {
		m := d.m
		if id == s.Model.ID {
			m.ContextTokens = s.Model.ContextTokens // the main model's window may have been overridden
		}
		ttl := m.Cache.DefaultTTL()
		out[id] = modelRecord{
			Source: d.src, Context: m.ContextTokens,
			InputPerM: m.Price.InputPerM, OutputPerM: m.Price.OutputPerM, CacheReadPerM: m.Price.CacheReadPerM,
			CacheWrite5mPerM: m.Price.CacheWrite5mPerM, CacheWrite1hPerM: m.Price.CacheWrite1hPerM,
			ExplicitCache: m.Cache.Explicit, TTLSeconds: int(ttl / time.Second),
		}
	}
	return out
}
