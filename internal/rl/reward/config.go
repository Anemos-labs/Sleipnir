package reward

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"sort"
	"strings"

	"github.com/anemos-labs/sleipnir/internal/cost"
)

// Component names. They are the keys of rl.Reward.Components and, except where
// noted, of Config.Weights. Penalty components are stored in [-1, 0] and
// bonuses in [0, 1], so weights are always non-negative.
const (
	// Episode components (shared by every role).
	CompOutcome = "outcome"
	// CompHonestDone is +1 (done accepted and verified), -1 (claimed done and
	// the verifier failed) or 0. It has two weights: Weights["honest_done"]
	// applies to the positive side and Weights["false_done"] to the negative
	// side, which is how the table in docs/TRAINING-DATA.md gets its "0.1 / -0.2".
	CompHonestDone = "honest_done"
	CompFalseDone  = "false_done" // weight key only
	CompCost       = "cost"
	CompRequests   = "requests"
	CompTime       = "time"
	CompProtocol   = "protocol"
	// CompWaste and CompGroupITE are the efficiency components, both gated by
	// the verifier score of a passing verdict (they only tell passing runs
	// apart; a failed verdict with partial credit has none) and off by default:
	// waste is -score * min(1, rl.Waste / caps.waste); group_ite is -score times
	// the episode's ITE placed between the cheapest (0) and the dearest (1) of
	// its rollout group, and needs Config.GroupITE.
	CompWaste    = "waste"
	CompGroupITE = "group_ite"

	// Worker.
	CompEvidence = "evidence"
	CompReread   = "reread"
	CompScope    = "scope"

	// Manager.
	CompParallel  = "parallel_efficiency"
	CompDuplicate = "duplicate_work"
	CompConflicts = "conflicts"
	CompIdle      = "idle"
	CompOverSpawn = "over_spawn"

	// Compactor.
	CompValid      = "valid"
	CompSize       = "size"
	CompFidelity   = "fidelity"
	CompRebase     = "rebase_cost"
	CompDownstream = "downstream"

	// Mailman / mail use.
	CompMailUseful = "mail_useful"
	CompMailSpam   = "mail_spam"
)

// Cap names (Config.Caps). Most cap a count so that a component reaches -1 (or
// +1) at that value; the last group are detector and probe knobs kept here so
// one JSON file configures the whole scorer.
const (
	CapProtocol  = "protocol"            // protocol events at which the penalty saturates
	CapParallel  = "parallel_efficiency" // worker_steps/critical_path at which the bonus saturates
	CapDuplicate = "duplicate_work"
	CapConflicts = "conflicts"
	CapIdle      = "idle" // milliseconds of idle worker time
	CapReread    = "reread"
	CapScope     = "scope"
	CapRebase    = "rebase_cost" // ITE written by a commit's next request
	CapWaste     = "waste"       // waste events (rl.WasteSignals) at which the waste penalty saturates

	CapProbeFacts    = "probe_facts"      // facts sampled per compaction probe
	CapHardcodeLen   = "hardcode_min_len" // shortest hidden literal the hardcode detector trusts
	CapMaxNotes      = "max_notes"        // notes kept in Reward.Notes
	CapAssertRemoved = "assert_removed"   // net removed assertions in one test file that count as weakening
)

// Detector names (Config.Detectors).
const (
	DetProtected = "protected"
	DetTests     = "tests"
	DetVerifier  = "verifier"
	DetHardcoded = "hardcoded"
	DetNetwork   = "network"
	DetOutside   = "outside"
	DetShim      = "shim"
)

// DefaultTargetName is the price/cache model used when a Config names none: an
// Anthropic-like deployment (explicit breakpoints, 5 minute TTL, 1.25x writes,
// 0.1x reads).
const DefaultTargetName = "anthropic-sonnet"

// defaultWeights are the documented defaults. Episode rows come from
// docs/TRAINING-DATA.md section 5; the role rows are this package's choice
// (small shaping terms next to a broadcast episode reward of order 1) and are
// meant to be tuned per run through rewards.json.
func defaultWeights() map[string]float64 {
	return map[string]float64{
		CompOutcome:    1.0,
		CompHonestDone: 0.1,
		CompFalseDone:  0.2,
		CompCost:       0.15,
		CompRequests:   0.05,
		CompTime:       0.05,
		CompProtocol:   0.1,
		CompWaste:      0,
		CompGroupITE:   0,

		CompEvidence: 0.1,
		CompReread:   0.05,
		CompScope:    0.1,

		CompParallel:  0.1,
		CompDuplicate: 0.1,
		CompConflicts: 0.1,
		CompIdle:      0.05,
		CompOverSpawn: 0.1,

		CompValid:      0.1,
		CompSize:       0.1,
		CompFidelity:   0.2,
		CompRebase:     0.1,
		CompDownstream: 1.0,

		CompMailUseful: 0.1,
		CompMailSpam:   0.1,
	}
}

// defaultCaps returns an independent map of penalty, probe, and diagnostic limits.
func defaultCaps() map[string]float64 {
	return map[string]float64{
		CapProtocol:  10,
		CapParallel:  4,
		CapDuplicate: 10,
		CapConflicts: 5,
		CapIdle:      120_000,
		CapReread:    5,
		CapScope:     3,
		CapRebase:    60_000,
		CapWaste:     10,

		CapProbeFacts:    8,
		CapHardcodeLen:   6,
		CapMaxNotes:      24,
		CapAssertRemoved: 1,
	}
}

// defaultDetectors returns a new map enabling every supported reward-hacking detector.
func defaultDetectors() map[string]bool {
	m := map[string]bool{}
	for _, n := range detectorNames() {
		m[n] = true
	}
	return m
}

// detectorNames returns the supported reward-hacking detectors in configuration order.
func detectorNames() []string {
	return []string{DetProtected, DetTests, DetVerifier, DetHardcoded, DetNetwork, DetOutside, DetShim}
}

// RepriceOptions tunes the deterministic cache model beyond what a
// cost.CacheModel can express. The zero value means: one engine, no capacity
// limit while a TTL is modelled (a default LRU capacity when it is not), the
// simulator's time-to-first-byte model, and 1-hour writes for shared prefixes
// when the target offers a longer TTL.
type RepriceOptions struct {
	// Engines is the number of independent caches (marketplace replicas).
	// Requests carrying the same shared prefix are routed to the same engine when
	// the target honours routing keys, and spread round-robin otherwise.
	Engines int `json:"engines,omitempty"`
	// CapacityTokens bounds one engine's cache; entries are evicted least
	// recently used first. Zero: unbounded with a TTL, DefaultLRUCapacity without.
	CapacityTokens int `json:"capacity_tokens,omitempty"`
	// TTFBBaseMs and TTFBPerKTokMs model the time to first byte of a request:
	// base + per thousand uncached-or-written prompt tokens. Cache entries become
	// readable at the first byte, so this decides whether parallel requests can
	// share a fresh write. Defaults match internal/kv/sim's Anthropic model.
	TTFBBaseMs    int64   `json:"ttfb_base_ms,omitempty"`
	TTFBPerKTokMs float64 `json:"ttfb_per_ktok_ms,omitempty"`
	// SharedTokens pins the token size of shared prefixes by Prompt.SharedPrefix
	// id when the recorded steps do not reveal it.
	SharedTokens map[string]int `json:"shared_tokens,omitempty"`
	// NoSharedLongTTL prices shared prefixes with the default TTL instead of the
	// longest one the target offers.
	NoSharedLongTTL bool `json:"no_shared_long_ttl,omitempty"`
}

// Config is the scoring configuration. The zero value scores with the
// documented defaults (except that fidelity probes are off); DefaultConfig
// spells them out. Missing weight and cap keys always fall back to the default,
// so a rewards.json only lists what it changes; set a weight to 0 to switch a
// component off.
type Config struct {
	// Weights maps component name to weight. See the Comp* constants.
	Weights map[string]float64 `json:"weights,omitempty"`
	// Caps maps a cap name to its value. See the Cap* constants.
	Caps map[string]float64 `json:"caps,omitempty"`
	// TargetName names the price/cache model episodes are repriced under: a
	// preset (see Targets) or any id of cost.Defaults(). Ignored when Target is set.
	TargetName string `json:"target,omitempty"`
	// Target is the resolved model. Set it directly to price under a model that
	// is not in the tables; LoadConfig fills it from TargetName.
	Target cost.Model `json:"-"`
	// Clip bounds every scalar reward (episode, agent, compactor step) to
	// [Clip[0], Clip[1]]; components stay raw. {0, 0} disables clipping.
	Clip [2]float64 `json:"clip"`
	// Probes enables compaction fidelity probes. They need prompt text: see
	// Prompts. Without a source they are skipped and noted, not scored as zero.
	Probes bool `json:"probes"`
	// Reprice tunes the counterfactual cache model.
	Reprice RepriceOptions `json:"reprice,omitempty"`
	// Detectors switches individual hack detectors off (by default all run).
	Detectors map[string]bool `json:"detectors,omitempty"`
	// WorkspaceRoots are the absolute directories an agent may write under. When
	// empty the outside-worktree detector falls back to a deny list of system and
	// dotfile locations (see hack_escape.go).
	WorkspaceRoots []string `json:"workspace_roots,omitempty"`

	// GroupITE is the [lowest, highest] repriced ITE of the episode's rollout
	// group (see GroupITE), set by a caller that scores whole groups. Without it
	// the group_ite component is 0, with a note when its weight is not.
	GroupITE *[2]float64 `json:"-"`

	// Prompts resolves a step's prompt text for fidelity probes. When nil, Score
	// falls back to a DiffSource that also implements PromptSource, then to
	// Step.Inline.
	Prompts PromptText `json:"-"`
}

// DefaultConfig returns the documented defaults with fresh maps.
func DefaultConfig() Config {
	return Config{
		Weights:    defaultWeights(),
		Caps:       defaultCaps(),
		TargetName: DefaultTargetName,
		Probes:     true,
		Detectors:  defaultDetectors(),
	}
}

// maxConfigBytes bounds LoadConfig; a rewards file is a few hundred bytes.
const maxConfigBytes = 1 << 20

// LoadConfig reads a rewards.json. Fields absent from the file keep their
// defaults; unknown fields, unknown component/cap/detector names and invalid
// values are rejected with an error naming the field.
func LoadConfig(path string) (Config, error) {
	f, err := os.Open(path)
	if err != nil {
		return Config{}, fmt.Errorf("reward config: %w", err)
	}
	defer f.Close()
	b, err := io.ReadAll(io.LimitReader(f, maxConfigBytes+1))
	if err != nil {
		return Config{}, fmt.Errorf("reward config %s: %w", path, err)
	}
	if len(b) > maxConfigBytes {
		return Config{}, fmt.Errorf("reward config %s: larger than %d bytes", path, maxConfigBytes)
	}
	cfg, err := ParseConfig(b)
	if err != nil {
		return Config{}, fmt.Errorf("%s: %w", path, err)
	}
	return cfg, nil
}

// ParseConfig decodes and validates a rewards.json document over the defaults.
func ParseConfig(b []byte) (Config, error) {
	b = bytes.TrimPrefix(b, []byte("\xef\xbb\xbf"))
	if len(bytes.TrimSpace(b)) == 0 {
		return Config{}, errors.New("reward config: empty document")
	}
	cfg := DefaultConfig()
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&cfg); err != nil {
		return Config{}, describeJSONError(err, b)
	}
	if _, err := dec.Token(); err != io.EOF {
		return Config{}, errors.New("reward config: unexpected data after the JSON object")
	}
	if err := cfg.Validate(); err != nil {
		return Config{}, err
	}
	m, err := cfg.resolveTarget()
	if err != nil {
		return Config{}, err
	}
	cfg.Target = m
	return cfg, nil
}

func describeJSONError(err error, doc []byte) error {
	var se *json.SyntaxError
	var te *json.UnmarshalTypeError
	switch {
	case errors.As(err, &se):
		return fmt.Errorf("reward config: invalid JSON at byte %d: %v", se.Offset, se)
	case errors.As(err, &te):
		// encoding/json reports the struct field ("weights") but not the map key
		// inside it; recover the full path from the document.
		field := jsonPathAt(doc, te.Offset)
		if field == "" {
			field = te.Field
		}
		if field == "" {
			field = "(document)"
		}
		return fmt.Errorf("reward config: field %q: expected %s, got JSON %s", field, te.Type, te.Value)
	case strings.HasPrefix(err.Error(), "json: unknown field"):
		return fmt.Errorf("reward config: %s (unknown field)", strings.TrimPrefix(err.Error(), "json: "))
	}
	return fmt.Errorf("reward config: %w", err)
}

// jsonPathAt returns the dotted key path of the scalar value that ends at or
// after offset ("weights.cost"), or "" when it cannot be determined.
func jsonPathAt(doc []byte, offset int64) string {
	dec := json.NewDecoder(bytes.NewReader(doc))
	type frame struct {
		obj       bool
		key       string
		expectKey bool
	}
	var stack []frame
	path := func() string {
		var parts []string
		for _, f := range stack {
			if f.obj && f.key != "" {
				parts = append(parts, f.key)
			}
		}
		return strings.Join(parts, ".")
	}
	for {
		tok, err := dec.Token()
		if err != nil {
			return ""
		}
		switch v := tok.(type) {
		case json.Delim:
			switch v {
			case '{':
				stack = append(stack, frame{obj: true, expectKey: true})
			case '[':
				stack = append(stack, frame{})
			default:
				if len(stack) > 0 {
					stack = stack[:len(stack)-1]
				}
				if n := len(stack); n > 0 && stack[n-1].obj {
					stack[n-1].expectKey = true
				}
			}
		default:
			n := len(stack) - 1
			if n >= 0 && stack[n].obj && stack[n].expectKey {
				stack[n].key, stack[n].expectKey = fmt.Sprint(v), false
				continue
			}
			if dec.InputOffset() >= offset {
				return path()
			}
			if n >= 0 && stack[n].obj {
				stack[n].expectKey = true
			}
		}
	}
}

// Validate checks names and values. It returns every problem at once, each
// naming its field.
func (c Config) Validate() error {
	var errs []error
	bad := func(format string, args ...any) { errs = append(errs, fmt.Errorf("reward config: "+format, args...)) }

	wk, ck := defaultWeights(), defaultCaps()
	for _, k := range sortedKeys(c.Weights) {
		v := c.Weights[k]
		if _, ok := wk[k]; !ok {
			bad("weights.%s: unknown component (known: %s)", k, strings.Join(sortedKeys(wk), ", "))
			continue
		}
		if !finite(v) || v < 0 {
			bad("weights.%s: must be a finite number >= 0 (penalties are stored negative), got %v", k, v)
		}
	}
	for _, k := range sortedKeys(c.Caps) {
		v := c.Caps[k]
		if _, ok := ck[k]; !ok {
			bad("caps.%s: unknown cap (known: %s)", k, strings.Join(sortedKeys(ck), ", "))
			continue
		}
		if !finite(v) || v < 0 {
			bad("caps.%s: must be a finite number >= 0, got %v", k, v)
		}
	}
	known := map[string]bool{}
	for _, n := range detectorNames() {
		known[n] = true
	}
	for _, k := range sortedKeys(c.Detectors) {
		if !known[k] {
			bad("detectors.%s: unknown detector (known: %s)", k, strings.Join(detectorNames(), ", "))
		}
	}
	lo, hi := c.Clip[0], c.Clip[1]
	switch {
	case !finite(lo) || !finite(hi):
		bad("clip: bounds must be finite, got [%v, %v]", lo, hi)
	case lo == 0 && hi == 0:
	case lo >= hi:
		bad("clip: lower bound must be below upper bound, got [%v, %v]", lo, hi)
	}
	r := c.Reprice
	if r.Engines < 0 {
		bad("reprice.engines: must be >= 0, got %d", r.Engines)
	}
	if r.CapacityTokens < 0 {
		bad("reprice.capacity_tokens: must be >= 0, got %d", r.CapacityTokens)
	}
	if r.TTFBBaseMs < 0 {
		bad("reprice.ttfb_base_ms: must be >= 0, got %d", r.TTFBBaseMs)
	}
	if !finite(r.TTFBPerKTokMs) || r.TTFBPerKTokMs < 0 {
		bad("reprice.ttfb_per_ktok_ms: must be a finite number >= 0, got %v", r.TTFBPerKTokMs)
	}
	for _, k := range sortedKeys(r.SharedTokens) {
		if r.SharedTokens[k] < 0 {
			bad("reprice.shared_tokens.%s: must be >= 0, got %d", k, r.SharedTokens[k])
		}
	}
	for i, root := range c.WorkspaceRoots {
		if !isAbsPath(root) {
			bad("workspace_roots[%d]: must be an absolute path, got %q", i, root)
		}
	}
	if _, err := c.resolveTarget(); err != nil {
		errs = append(errs, err)
	}
	return errors.Join(errs...)
}

// resolveTarget returns the model episodes are repriced under.
func (c Config) resolveTarget() (cost.Model, error) {
	if !modelIsZero(c.Target) {
		return c.Target, nil
	}
	name := strings.TrimSpace(c.TargetName)
	if name == "" {
		name = DefaultTargetName
	}
	m, ok := LookupTarget(name)
	if !ok {
		return cost.Model{}, fmt.Errorf("reward config: target: unknown price model %q (presets: %s; or any id of the built-in cost table)",
			name, strings.Join(Targets(), ", "))
	}
	return m, nil
}

// modelIsZero reports whether model identity, prices, cache modes, and cache TTLs are all unset.
func modelIsZero(m cost.Model) bool {
	return m.ID == "" && m.Price == (cost.Price{}) && !m.Cache.Explicit && !m.Cache.Auto && len(m.Cache.TTLs) == 0
}

// resolved is a Config with every default filled in, computed once per Score.
type resolved struct {
	weights   map[string]float64
	caps      map[string]float64
	detectors map[string]bool
	target    cost.Model
	clip      [2]float64
	probes    bool
	reprice   RepriceOptions
	roots     []string
	prompts   PromptText
	groupITE  *[2]float64
}

func (c Config) resolve() (*resolved, error) {
	if err := c.Validate(); err != nil {
		return nil, err
	}
	t, err := c.resolveTarget()
	if err != nil {
		return nil, err
	}
	r := &resolved{
		weights: defaultWeights(), caps: defaultCaps(), detectors: defaultDetectors(),
		target: t, clip: c.Clip, probes: c.Probes, reprice: c.Reprice, prompts: c.Prompts, groupITE: c.GroupITE,
	}
	for k, v := range c.Weights {
		r.weights[k] = v
	}
	for k, v := range c.Caps {
		r.caps[k] = v
	}
	for k, v := range c.Detectors {
		r.detectors[k] = v
	}
	for _, root := range c.WorkspaceRoots {
		// cleanAbs expresses every root Validate accepts (slash-rooted, drive-letter and UNC paths). A
		// root it cannot is left out, so a Config that skipped Validate is judged by the deny list alone,
		// as it is without roots.
		if cr := cleanAbs(root); cr != "" {
			r.roots = append(r.roots, cr)
		}
	}
	sort.Strings(r.roots)
	return r, nil
}

// Total is the weighted sum of components under this config. Keys without a
// weight (informational ones such as "role/worker" or per-compaction entries)
// are ignored, so it can be applied to any stored Components map.
func (c Config) Total(components map[string]float64) float64 {
	r := &resolved{weights: defaultWeights()}
	for k, v := range c.Weights {
		r.weights[k] = v
	}
	return r.total(components)
}

func (r *resolved) total(components map[string]float64) float64 {
	sum := 0.0
	for _, k := range sortedKeys(components) { // sorted: float addition order must not depend on map order
		v := components[k]
		if !finite(v) {
			continue
		}
		if k == CompHonestDone {
			if v >= 0 {
				sum += r.weights[CompHonestDone] * v
			} else {
				sum += r.weights[CompFalseDone] * v
			}
			continue
		}
		if k == CompFalseDone {
			continue
		}
		if w, ok := r.weights[k]; ok {
			sum += w * v
		}
	}
	return sum
}

// clipTotal applies Config.Clip and scrubs non-finite values.
func (r *resolved) clipTotal(v float64) float64 {
	if !finite(v) {
		return 0
	}
	if r.clip[0] < r.clip[1] {
		v = math.Max(r.clip[0], math.Min(r.clip[1], v))
	}
	return v
}

// cap returns the resolved cap for a detector, or zero if no entry exists.
func (r *resolved) cap(name string) float64 { return r.caps[name] }

// capDiv returns x/cap saturated to [0, 1]; a zero cap saturates on the first
// event, so "cap: 0" means "any occurrence is the maximum penalty".
func (r *resolved) frac(x float64, capName string) float64 {
	if !finite(x) || x <= 0 {
		return 0
	}
	c := r.caps[capName]
	if c <= 0 {
		return 1
	}
	return math.Min(1, x/c)
}

// ---- small shared helpers ----------------------------------------------------------

// finite excludes NaN and both infinities from numeric configuration values.
func finite(v float64) bool { return !math.IsNaN(v) && !math.IsInf(v, 0) }

// sortedKeys returns lexical map-key order for deterministic scoring output.
func sortedKeys[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// clamp01 bounds a score to [0, 1], treating NaN as zero.
func clamp01(v float64) float64 {
	switch {
	case math.IsNaN(v):
		return 0
	case v < 0:
		return 0
	case v > 1:
		return 1
	}
	return v
}
