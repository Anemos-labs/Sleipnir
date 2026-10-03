package anthropic

import (
	"strings"

	"github.com/anemos-labs/sleipnir/internal/cost"
)

// ThinkingKind says how a model family expresses "think for me" on the wire.
type ThinkingKind int

const (
	// ThinkAdaptive: {"type":"adaptive"}. Claude 4.6 and later.
	ThinkAdaptive ThinkingKind = iota
	// ThinkBudget: {"type":"enabled","budget_tokens":N}. Models before adaptive
	// thinking; the budget must be below max_tokens.
	ThinkBudget
	// ThinkNone: the model has no thinking control; the field is never sent.
	ThinkNone
)

// effortOrder lists output_config.effort levels from cheapest to deepest.
var effortOrder = []string{"low", "medium", "high", "xhigh", "max"}

// ModelInfo is what one model family accepts on the wire. The adapter uses it
// to avoid sending a parameter the endpoint answers with HTTP 400: an agent
// that dies on a rejected sampling knob has lost far more than the knob.
//
// The zero value describes an unknown model and means "pass everything the
// caller asked for through unchanged".
type ModelInfo struct {
	// Known is set for families this table describes. Unknown models get
	// pass-through behaviour, because dropping a parameter the caller
	// deliberately set is worse than letting the endpoint judge it.
	Known bool
	// NoSampling: temperature, top_p and top_k were removed from the family and
	// are a 400 when present.
	NoSampling bool
	// Thinking selects the wire form of thinking. Only meaningful when Known.
	Thinking ThinkingKind
	// AlwaysThinks: thinking cannot be switched off (omitting the field runs
	// adaptive thinking). Such models still accept an explicit adaptive object,
	// which is how thinking.block_binding is attached when the caller asked for
	// no other thinking configuration.
	AlwaysThinks bool
	// Efforts lists the accepted output_config.effort values, cheapest first.
	// Empty on a known family means effort is not supported.
	Efforts []string
	// PreservedThinking: a thinking block is valid only while everything before it
	// is unchanged (Fable 5.1, Opus 5.5, Sonnet 5.5). Editing an earlier message,
	// which is what an inline board view does on the next request, voids every
	// later thinking block: a 400 by default, a dropped block with drop_block.
	PreservedThinking bool
	// MidSystem: the family accepts role:system messages inside the conversation
	// (permanent ones, and turn-scoped ones with clear_at). Elsewhere the API
	// answers a 400, which would kill a request that only wanted to deliver a
	// board view; such a message is folded into user text instead.
	MidSystem bool
}

// midSystem reports whether a mid-conversation system message may be sent. An
// unknown model gets the benefit of the doubt: it is usually a gateway's alias
// for a model that has the feature, and Options.NoTurnScopedSystem (or the
// profile) is the switch for an endpoint that does not.
func (m ModelInfo) midSystem() bool { return !m.Known || m.MidSystem }

// ModelResolver maps a model id to its ModelInfo.
type ModelResolver func(model string) ModelInfo

var (
	allEfforts   = effortOrder
	fourEfforts  = []string{"low", "medium", "high", "max"}
	threeEfforts = []string{"low", "medium", "high"}
)

// families is ordered: the first matching prefix of the normalised model id
// wins, so more specific ids come before their parents.
//
// Sources: docs/PROVIDERS.md and the Claude API
// reference (thinking and effort tables). Keep in step with internal/cost's
// price table; that file owns economics, this one owns wire acceptance.
var families = []struct {
	prefix string
	info   ModelInfo
}{
	{"claude-fable-5-1", ModelInfo{Known: true, NoSampling: true, AlwaysThinks: true, Efforts: allEfforts, MidSystem: true, PreservedThinking: true}},
	{"claude-fable-5", ModelInfo{Known: true, NoSampling: true, AlwaysThinks: true, Efforts: allEfforts, MidSystem: true}},
	{"claude-mythos-5", ModelInfo{Known: true, NoSampling: true, AlwaysThinks: true, Efforts: allEfforts, MidSystem: true}},
	{"claude-opus-5-5", ModelInfo{Known: true, NoSampling: true, AlwaysThinks: true, Efforts: allEfforts, MidSystem: true, PreservedThinking: true}},
	{"claude-opus-5", ModelInfo{Known: true, NoSampling: true, Efforts: allEfforts, MidSystem: true}},
	{"claude-opus-4-8", ModelInfo{Known: true, NoSampling: true, Efforts: allEfforts, MidSystem: true}},
	{"claude-opus-4-7", ModelInfo{Known: true, NoSampling: true, Efforts: allEfforts}},
	{"claude-opus-4-6", ModelInfo{Known: true, Efforts: fourEfforts}},
	{"claude-opus-4-5", ModelInfo{Known: true, Thinking: ThinkBudget, Efforts: threeEfforts}},
	{"claude-sonnet-5-5", ModelInfo{Known: true, NoSampling: true, AlwaysThinks: true, Efforts: allEfforts, MidSystem: true, PreservedThinking: true}},
	{"claude-sonnet-5", ModelInfo{Known: true, NoSampling: true, Efforts: allEfforts}},
	{"claude-sonnet-4-6", ModelInfo{Known: true, Efforts: fourEfforts}},
	{"claude-sonnet-4-5", ModelInfo{Known: true, Thinking: ThinkBudget}},
	{"claude-haiku-4-5", ModelInfo{Known: true, Thinking: ThinkBudget}},
	{"claude-opus-4", ModelInfo{Known: true, Thinking: ThinkBudget}},
	{"claude-sonnet-4", ModelInfo{Known: true, Thinking: ThinkBudget}},
	{"claude-3-7-sonnet", ModelInfo{Known: true, Thinking: ThinkBudget}},
	{"claude-3", ModelInfo{Known: true, Thinking: ThinkNone}},
}

// DefaultModelInfo resolves a model id, tolerating gateway prefixes
// ("anthropic/claude-opus-5-5") and date suffixes.
func DefaultModelInfo(model string) ModelInfo {
	id := cost.Normalize(model)
	for _, f := range families {
		if strings.HasPrefix(id, f.prefix) {
			return f.info
		}
	}
	return ModelInfo{}
}

// clampEffort maps a requested effort onto what the family accepts. ok is
// false when the family has no effort control. A level this table does not know
// is passed through: a newer level should reach the endpoint, not be guessed at.
func (m ModelInfo) clampEffort(want string) (got string, clamped, ok bool) {
	if !m.Known {
		return want, false, true
	}
	if len(m.Efforts) == 0 {
		return "", false, false
	}
	rank := func(s string) int {
		for i, e := range effortOrder {
			if e == s {
				return i
			}
		}
		return -1
	}
	wr := rank(want)
	if wr < 0 {
		return want, false, true
	}
	best := -1
	for i, e := range m.Efforts {
		if e == want {
			return want, false, true
		}
		if rank(e) <= wr {
			best = i
		}
	}
	if best < 0 {
		best = 0
	}
	return m.Efforts[best], true, true
}
