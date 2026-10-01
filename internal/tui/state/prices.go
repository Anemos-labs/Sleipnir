package state

import (
	"sort"
	"strings"

	"github.com/anemos-labs/sleipnir/internal/cost"
)

// modelState is what is known about one model id: its price, where the price came from and the lifetime its cache entries have.
type modelState struct {
	info     ModelInfo
	recorded bool // it came from session.start, so it is never evicted to make room for a looked-up one
}

// modelWire is one entry of session.start's "models" map: the numbers the run was priced with (internal/session/models.go).
type modelWire struct {
	Source           string  `json:"source"`
	InputPerM        float64 `json:"input_per_m"`
	OutputPerM       float64 `json:"output_per_m"`
	CacheReadPerM    float64 `json:"cache_read_per_m"`
	CacheWrite5mPerM float64 `json:"cache_write_5m_per_m"`
	CacheWrite1hPerM float64 `json:"cache_write_1h_per_m"`
	TTLSeconds       int     `json:"ttl_s"`
	Context          int64   `json:"context"`
}

// recordModels takes the prices a session.start carries. A recorded price is what the run was billed by, so it wins over the
// table; "fallback" is the harness's guess for a model nothing knew, which is not a price to compute a saving from; an entry
// whose prices are not sane numbers (cost.Price.Validate) is not used at all. At most 24 models are taken, the smallest ids
// first, so that the result does not depend on the order a map is read in.
func (s *State) recordModels(m map[string]modelWire) {
	ids := make([]string, 0, len(m))
	for id := range m {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	n := 0
	for _, id := range ids {
		if n >= 24 {
			break
		}
		w := m[id]
		id = clip(id, cost.MaxModelIDBytes)
		if id == "" {
			continue
		}
		p := cost.Price{InputPerM: w.InputPerM, OutputPerM: w.OutputPerM, CacheReadPerM: w.CacheReadPerM,
			CacheWrite5mPerM: w.CacheWrite5mPerM, CacheWrite1hPerM: w.CacheWrite1hPerM}
		if p.Validate() != nil || w.TTLSeconds < 0 || w.TTLSeconds > 30*24*3600 {
			continue
		}
		n++
		info := ModelInfo{ID: id, Source: "session", Known: true, TTLSeconds: w.TTLSeconds, ContextTokens: int(clampTokens(w.Context)),
			Price: Price{InputPerM: p.InputPerM, OutputPerM: p.OutputPerM, CacheReadPerM: p.CacheReadPerM,
				CacheWrite5mPerM: p.CacheWrite5mPerM, CacheWrite1hPerM: p.CacheWrite1hPerM}}
		if w.Source == "fallback" {
			info.Source, info.Known = "fallback", false
		}
		s.models[id] = &modelState{info: info, recorded: true}
	}
}

// model resolves a model id to what is known about it. The prices the run recorded come first, exactly and then up to the
// spellings cost.Normalize folds together (gateway prefixes, date suffixes); then the built-in list-price table; a model nothing
// knows is "unknown", and no saving is computed for it. The answer is remembered, up to MaxModels entries.
func (s *State) model(id string) *modelState {
	id = clip(id, cost.MaxModelIDBytes)
	if id == "" {
		return nil
	}
	if m := s.models[id]; m != nil {
		return m
	}
	m := &modelState{info: ModelInfo{ID: id, Source: "unknown"}}
	if r := s.recordedLike(id); r != nil {
		m.info = *r
		m.info.ID = id
	} else if tm, ok := s.prices.Lookup(id); ok {
		m.info.Source, m.info.Known, m.info.ContextTokens = "table", true, int(clampTokens(int64(tm.ContextTokens)))
		p := tm.Price
		m.info.Price = Price{InputPerM: p.InputPerM, OutputPerM: p.OutputPerM, CacheReadPerM: p.CacheReadPerM,
			CacheWrite5mPerM: p.CacheWrite5mPerM, CacheWrite1hPerM: p.CacheWrite1hPerM}
	}
	if len(s.models) < MaxModels {
		s.models[id] = m
	}
	return m
}

// recordedLike finds the recorded model that id is another spelling of, the smallest id if several are.
func (s *State) recordedLike(id string) *ModelInfo {
	want := cost.Normalize(id)
	var best *ModelInfo
	for _, m := range s.models {
		if !m.recorded || cost.Normalize(m.info.ID) != want {
			continue
		}
		if best == nil || strings.Compare(m.info.ID, best.ID) < 0 {
			info := m.info
			best = &info
		}
	}
	return best
}

// priceOf picks the model to price a response by: the first candidate with a known price, else the first that is named (whose
// unknown price is then what the saving reports), else nil. The candidates are the model the request was made for, the model the
// response says it came from and the agent's own.
func (s *State) priceOf(ids ...string) *modelState {
	var first *modelState
	for _, id := range ids {
		m := s.model(id)
		if m == nil {
			continue
		}
		if m.info.Known {
			return m
		}
		if first == nil {
			first = m
		}
	}
	return first
}

// saving is what reading read tokens from cache saved instead of paying the input price for them: read times the difference
// between the model's input price and its cache-read price, per million tokens. ok is false when the model's price is unknown
// (a saving of nothing is then not a known saving); reading nothing saves nothing, known.
func saving(m *modelState, read int64) (usd float64, ok bool) {
	if read <= 0 {
		return 0, true
	}
	if m == nil || !m.info.Known {
		return 0, false
	}
	return float64(read) * (m.info.Price.InputPerM - m.info.Price.CacheReadPerM) / 1e6, true
}

// sortedModels lists what is known about the models, by id.
func (s *State) sortedModels() []ModelInfo {
	out := make([]ModelInfo, 0, len(s.models))
	for _, m := range s.models {
		out = append(out, m.info)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}
