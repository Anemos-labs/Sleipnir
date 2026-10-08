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
	// why says how the new setting differs from the old one.
	why string
}

// renamedSettings are the old names Load refuses.
var renamedSettings = []renamedSetting{{
	from: []string{"swarm", "max_agents"}, to: []string{"swarm", "max_workers"},
	why: "it counts workers, the manager not included",
	value: func(old string) string {
		n, err := strconv.Atoi(strings.TrimSpace(old))
		if err != nil {
			return ""
		}
		return strconv.Itoa(max(n-1, 0))
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

// fileMessage is the refusal of the old key in a file: the new key and, when the old value is a
// number, the value to give it.
func (r renamedSetting) fileMessage(old string) string {
	to := r.to[len(r.to)-1]
	v := r.value(old)
	if v == "" {
		v = "the old value minus one"
	}
	return fmt.Sprintf("renamed to %s (%s): write \"%s\": %s instead", fmtPath(r.to), r.why, to, v)
}

// envName is the variable that sets the setting at segs.
func envName(segs []string) string { return "SLEIPNIR_" + strings.ToUpper(strings.Join(segs, "_")) }

// envMessage is the refusal of the old variable.
func (r renamedSetting) envMessage(old string) string {
	v := r.value(old)
	if v == "" {
		v = "<the old value minus one>"
	}
	return fmt.Sprintf("renamed to %s (%s): set %s=%s instead", envName(r.to), r.why, envName(r.to), v)
}
