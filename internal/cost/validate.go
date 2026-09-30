package cost

import (
	"errors"
	"fmt"
	"math"
	"strings"
	"unicode"
	"unicode/utf8"
)

// Numbers in a Model come from outside the harness: a marketplace catalogue (a
// third party sitting in the data path), a cached copy of one on disk, a
// configuration file. Every dollar figure the harness shows and every budget it
// enforces is computed from them, and every comparison a budget makes ("spend >=
// limit") is false for NaN and wrong for a negative number. A hostile or merely
// broken catalogue could therefore make a budget unreachable, so the boundary is
// here: a Model that fails Validate is never stored in a Table, and Price.USD
// never returns a figure that cannot be compared.
const (
	// MaxPricePerM bounds any per-million-token price, in dollars. No model costs a
	// tenth of a dollar per token, and the bound keeps every product of a price and
	// a token count finite.
	MaxPricePerM = 100_000.0
	// MaxWindowTokens bounds a context window or an output limit. It is roughly a
	// thousand times the largest window on sale, and small enough that the planner's
	// percentage arithmetic cannot overflow an int.
	MaxWindowTokens = 1 << 30
	// MaxModelIDBytes bounds a model id.
	MaxModelIDBytes = 256
)

func finite(f float64) bool { return !math.IsNaN(f) && !math.IsInf(f, 0) }

// Validate reports why p cannot be used to compute spend: every price must be a
// finite number of dollars per million tokens, between zero and MaxPricePerM.
func (p Price) Validate() error {
	for _, f := range []struct {
		name string
		v    float64
	}{
		{"input", p.InputPerM},
		{"output", p.OutputPerM},
		{"cache read", p.CacheReadPerM},
		{"cache write (5m)", p.CacheWrite5mPerM},
		{"cache write (1h)", p.CacheWrite1hPerM},
	} {
		switch {
		case math.IsNaN(f.v):
			return fmt.Errorf("%s price is not a number", f.name)
		case math.IsInf(f.v, 0):
			return fmt.Errorf("%s price is infinite", f.name)
		case f.v < 0:
			return fmt.Errorf("%s price is negative (%g)", f.name, f.v)
		case f.v > MaxPricePerM:
			return fmt.Errorf("%s price %g $/M is beyond any real model (limit %g)", f.name, f.v, MaxPricePerM)
		}
	}
	return nil
}

// Validate reports why m cannot be trusted for budgeting and planning: a usable
// id, prices that Price.Validate accepts, a context window and an output limit
// between zero (unknown) and MaxWindowTokens, and non-negative cache parameters.
// The message says what is wrong, never echoes more than the id.
func (m Model) Validate() error {
	if err := validID(m.ID); err != nil {
		return err
	}
	if err := m.Price.Validate(); err != nil {
		return err
	}
	if m.ContextTokens < 0 || m.ContextTokens > MaxWindowTokens {
		return fmt.Errorf("context window %d is outside 0..%d", m.ContextTokens, MaxWindowTokens)
	}
	if m.MaxOutput < 0 || m.MaxOutput > MaxWindowTokens {
		return fmt.Errorf("output limit %d is outside 0..%d", m.MaxOutput, MaxWindowTokens)
	}
	c := m.Cache
	for _, f := range []struct {
		name string
		v    int
	}{
		{"max breakpoints", c.MaxBreakpoints}, {"lookback blocks", c.LookbackBlocks},
		{"minimum cacheable prefix", c.MinPrefixTokens}, {"cache granularity", c.Granularity},
	} {
		if f.v < 0 || f.v > MaxWindowTokens {
			return fmt.Errorf("%s %d is outside 0..%d", f.name, f.v, MaxWindowTokens)
		}
	}
	for _, ttl := range c.TTLs {
		if ttl <= 0 {
			return errors.New("cache lifetime is not positive")
		}
	}
	return nil
}

// validID accepts a printable, bounded, valid-UTF-8 model id without whitespace.
// Ids reach terminals and logs, so anything a legitimate id never contains is
// refused rather than cleaned.
func validID(id string) error {
	switch {
	case strings.TrimSpace(id) == "":
		return errors.New("empty model id")
	case len(id) > MaxModelIDBytes:
		return fmt.Errorf("model id is %d bytes long (limit %d)", len(id), MaxModelIDBytes)
	case !utf8.ValidString(id):
		return errors.New("model id is not valid UTF-8")
	}
	for _, r := range id {
		if unicode.IsSpace(r) || unicode.IsControl(r) || unicode.Is(unicode.Cf, r) || r == utf8.RuneError {
			return fmt.Errorf("model id contains an unprintable or whitespace character (U+%04X)", r)
		}
	}
	return nil
}
