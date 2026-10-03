package provider

import (
	"regexp"
	"strings"
	"sync"

	"github.com/anemos-labs/sleipnir/internal/core"
	"github.com/anemos-labs/sleipnir/internal/cost"
)

var effortOrder = []string{"none", "minimal", "low", "medium", "high", "xhigh", "max"}

// NormalizeEffort accepts common aliases; an unrecognized value selects the
// provider default rather than sending an invalid parameter.
func NormalizeEffort(value string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	switch value {
	case "off", "no":
		return "none"
	case "min":
		return "minimal"
	case "normal", "med":
		return "medium"
	case "extra-high", "extra_high", "very high":
		return "xhigh"
	case "maximum", "ultra":
		return "max"
	}
	for _, level := range effortOrder {
		if value == level {
			return level
		}
	}
	return ""
}

// ClosestEffort chooses the nearest accepted level, preferring the lower level
// on a tie. Empty supported levels mean the parameter must be omitted.
func ClosestEffort(want string, supported []string) string {
	if want == "" {
		return ""
	}
	rank := func(value string) int {
		for i, level := range effortOrder {
			if value == level {
				return i
			}
		}
		return -1
	}
	wanted, best, distance := rank(want), "", len(effortOrder)+1
	for _, level := range supported {
		r := rank(level)
		if r < 0 || wanted < 0 {
			continue
		}
		d := r - wanted
		if d < 0 {
			d = -d
		}
		if d < distance || (d == distance && r < rank(best)) {
			best, distance = level, d
		}
	}
	return best
}

// ModelEfforts describes known API effort ranges. Unknown model IDs are left
// to the endpoint; validation responses can refine these defaults per route.
// Sources: https://developers.openai.com/api/docs/guides/latest-model and
// https://api-docs.deepseek.com/api/create-chat-completion/.
func ModelEfforts(model string) ([]string, bool) {
	id := cost.Normalize(model)
	switch {
	case strings.HasPrefix(id, "deepseek-v4"), id == "deepseek-flash":
		return []string{"none", "low", "high", "max"}, true
	case strings.HasPrefix(id, "gpt-6-astra"), strings.HasPrefix(id, "gpt-6.1-sol"):
		return []string{"low", "medium", "high", "xhigh", "max"}, true
	case strings.HasPrefix(id, "gpt-6-sol"), strings.HasPrefix(id, "gpt-6-luna"):
		return []string{"none", "low", "medium", "high", "xhigh", "max"}, true
	}
	return nil, false
}

// EffortSetting shares a session's requested effort and endpoint corrections
// across current and future agents. Its zero value selects provider defaults.
type EffortSetting struct {
	mu      sync.RWMutex
	want    string
	learned map[string][]string
}

// Set changes the effort used by subsequent requests without mutating a
// request already in flight. It returns the normalized preference.
func (e *EffortSetting) Set(value string) string {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.want = NormalizeEffort(value)
	return e.want
}

// Requested returns the normalized session preference, or empty for default.
func (e *EffortSetting) Requested() string {
	e.mu.RLock()
	defer e.mu.RUnlock()
	return e.want
}

// effortRoute separates learned constraints by endpoint, dialect and model.
func effortRoute(p Provider, model string) string {
	f := p.Profile()
	return f.Name + "\x00" + f.BaseURL + "\x00" + f.Dialect + "\x00" + model
}

// Params snapshots the preference and maps it onto this model's accepted
// levels. Adapters may report a known empty range to disable the parameter.
func (e *EffortSetting) Params(p Provider, model string, base core.Params) core.Params {
	key := effortRoute(p, model)
	e.mu.RLock()
	want := e.want
	levels, known := e.learned[key]
	e.mu.RUnlock()
	if !known {
		if source, ok := p.(interface{ EffortLevels(string) ([]string, bool) }); ok {
			levels, known = source.EffortLevels(model)
		} else {
			levels, known = ModelEfforts(model)
		}
	}
	base.Effort = want
	if known {
		base.Effort = ClosestEffort(want, levels)
	}
	return base
}

var effortValue = regexp.MustCompile(`\b(none|minimal|low|medium|high|xhigh|max)\b`)

// Recover learns only from explicit effort validation errors, never auth,
// transport, context or reasoning-history failures. Without a usable allowed
// list it falls back to omitting the parameter. Callers bound retries.
func (e *EffortSetting) Recover(p Provider, model, used string, err error) bool {
	pe, ok := AsError(err)
	if !ok || pe.Kind != ErrBadRequest || (pe.Status != 400 && pe.Status != 422) || used == "" {
		return false
	}
	msg := strings.ToLower(pe.Message)
	if !strings.Contains(msg, "effort") {
		return false
	}
	var levels []string
	validation := false
	for _, marker := range []string{"supported values", "supported levels", "allowed values", "one of", "must be", "should be"} {
		if _, tail, found := strings.Cut(msg, marker); found {
			validation = true
			for _, level := range effortValue.FindAllString(tail, -1) {
				if level != used {
					levels = append(levels, level)
				}
			}
			break
		}
	}
	if !validation && !strings.Contains(msg, "unsupported") && !strings.Contains(msg, "not supported") && !strings.Contains(msg, "invalid") && !strings.Contains(msg, "unknown parameter") && !strings.Contains(msg, "unrecognized") {
		return false
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.learned == nil {
		e.learned = make(map[string][]string)
	}
	e.learned[effortRoute(p, model)] = levels
	return true
}
