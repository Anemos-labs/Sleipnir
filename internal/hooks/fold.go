package hooks

import "strings"

// maxCombinedReason caps the reason of a whole event, however many hooks
// contributed to it.
const maxCombinedReason = 4 << 10

// folder combines the answers of the hooks of one event. Hooks run in parallel
// and finish in any order; add is called in configuration order, which is what
// makes the combined Result deterministic.
type folder struct {
	event      string
	maxContext int

	deny, ask, allow bool
	blocked          bool
	blockReasons     []string // deny decisions and blocks, in hook order
	askReasons       []string
	allowReasons     []string
	contexts         []string
	updatedFrom      string
}

func (f *folder) add(res *Result, hr hookResult) {
	res.Runs = append(res.Runs, hr.run)
	res.Errors = append(res.Errors, hr.errs...)
	if hr.message != "" {
		res.Messages = append(res.Messages, hr.message)
	}
	if hr.stop {
		res.Stop = true
		if res.StopReason == "" {
			res.StopReason = hr.stopReason
		}
	}
	switch hr.decision {
	case Deny:
		f.deny = true
	case Ask:
		f.ask = true
		f.askReasons = appendNonEmpty(f.askReasons, hr.reason)
	case Allow:
		f.allow = true
		f.allowReasons = appendNonEmpty(f.allowReasons, hr.reason)
	}
	if hr.block {
		f.blocked = true
		f.blockReasons = appendNonEmpty(f.blockReasons, hr.blockReason)
	}
	f.contexts = appendNonEmpty(f.contexts, hr.context)
	if len(hr.updated) > 0 {
		if res.UpdatedInput == nil {
			res.UpdatedInput = hr.updated
			f.updatedFrom = hr.hook.String()
		} else {
			res.Errors = append(res.Errors, HookError{
				Hook:    hr.hook.String(),
				Message: "its updatedInput was ignored: " + f.updatedFrom + " already rewrote the input, and rewrites made from the same original cannot be combined",
			})
		}
	}
}

// finish computes the combined fields once every hook has been added.
func (f *folder) finish(res *Result) {
	res.Blocked = f.blocked || f.deny
	switch {
	case f.deny || (f.blocked && hasDecision(f.event)):
		res.Decision = Deny
	case f.ask:
		res.Decision = Ask
	case f.allow:
		res.Decision = Allow
	}
	switch {
	case res.Blocked:
		res.Reason = joinCapped(f.blockReasons, maxCombinedReason)
	case res.Decision == Ask:
		res.Reason = joinCapped(f.askReasons, maxCombinedReason)
	case res.Decision == Allow:
		res.Reason = joinCapped(f.allowReasons, maxCombinedReason)
	}
	if res.Blocked && res.Reason == "" {
		res.Reason = "blocked by a hook, which gave no reason"
	}
	if res.Blocked {
		res.UpdatedInput = nil // a refused call is not rewritten
	}
	res.AdditionalContext = capText(strings.Join(f.contexts, "\n\n"), f.maxContext)
}

func appendNonEmpty(list []string, s string) []string {
	if strings.TrimSpace(s) == "" {
		return list
	}
	return append(list, s)
}

func joinCapped(parts []string, max int) string {
	return capText(strings.Join(parts, "\n"), max)
}
