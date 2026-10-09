package config

import (
	"fmt"
	"strconv"
	"strings"
)

// renamedSetting is a setting whose old name is refused, with what to write instead. An old name that
// was only warned about would be dropped, and a ceiling the person set would be lifted without a word.
type renamedSetting struct {
	from, to []string
	// value turns the old value, as written, into the new one; "" when it cannot.
	value func(old string) string
	// unmapped says what to write when the old value is a number that has no equal in the new setting ("" when it is not such a number):
	// the refusal then names the choice the person has to make instead of a conversion that would change what they set.
	unmapped func(old string) string
	// why says how the new setting differs from the old one.
	why string
}

// renamedSettings are the old names Load refuses.
var renamedSettings = []renamedSetting{{
	from: []string{"swarm", "max_agents"}, to: []string{"swarm", "max_workers"},
	why: "it counts workers, the manager not included",
	value: func(old string) string {
		n, err := strconv.Atoi(strings.TrimSpace(old))
		switch {
		case err != nil || n < 0:
			return ""
		case n == 0:
			return "0" // no ceiling before, no ceiling now
		case n == 1:
			return "" // the manager alone: no worker count says that, and 0 would mean the default (see unmapped)
		}
		return strconv.Itoa(n - 1)
	},
	unmapped: func(old string) string {
		n, err := strconv.Atoi(strings.TrimSpace(old))
		switch {
		case err != nil:
			return ""
		case n < 0:
			return "a worker count of 0 or more (a negative ceiling is not valid)"
		case n == 1:
			return "a worker count of 1 or more (the old value 1 allowed no workers, and 0 means no ceiling; --swarm 0 runs a single agent)"
		}
		return ""
	},
}}

// renamedAt returns the renamed setting at segs, if there is one.
func renamedAt(segs []string) (renamedSetting, bool) {
	for _, r := range renamedSettings {
		if fmtPath(r.from) == fmtPath(segs) {
			return r, true
		}
	}
	return renamedSetting{}, false
}

// placeholder is what the refusal says to write when value cannot give the number: the rule for a number with no equal, else the general one.
func (r renamedSetting) placeholder(old string) string {
	if r.unmapped != nil {
		if u := r.unmapped(old); u != "" {
			return u
		}
	}
	return "the old value minus one"
}

// fileMessage is the refusal of the old key in a file: the new key and, when the old value is a
// number, the value to give it.
func (r renamedSetting) fileMessage(old string) string {
	to := r.to[len(r.to)-1]
	v := r.value(old)
	if v == "" {
		v = r.placeholder(old)
	}
	return fmt.Sprintf("renamed to %s (%s): write \"%s\": %s instead", fmtPath(r.to), r.why, to, v)
}

// envName is the variable that sets the setting at segs.
func envName(segs []string) string { return "SLEIPNIR_" + strings.ToUpper(strings.Join(segs, "_")) }

// envMessage is the refusal of the old variable.
func (r renamedSetting) envMessage(old string) string {
	v := r.value(old)
	if v == "" {
		v = "<" + r.placeholder(old) + ">"
	}
	return fmt.Sprintf("renamed to %s (%s): set %s=%s instead", envName(r.to), r.why, envName(r.to), v)
}
