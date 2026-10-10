package wsvc

import (
	"net/http"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/anemos-labs/sleipnir/internal/perm"
	"github.com/anemos-labs/sleipnir/internal/session"
	"github.com/anemos-labs/sleipnir/internal/web"
	"github.com/anemos-labs/sleipnir/internal/web/wire"
)

// maxComplete bounds the paths `complete` returns; defComplete is the default.
const (
	maxComplete = 50
	defComplete = 8
)

// handleComplete answers GET /api/sessions/{id}/complete?prefix=&limit=8: project paths
// containing prefix (files, and the directories above them), listed like the glob tool
// lists them (.gitignore honoured, .git skipped, no symlink followed), paths agents may
// not read left out; at most limit (50). Paths whose name starts with prefix come first.
func (s *service) handleComplete(w http.ResponseWriter, r *http.Request) {
	_, sess, ok := s.tabOf(w, r)
	if !ok {
		return
	}
	q := r.URL.Query()
	prefix := q.Get("prefix")
	if len(prefix) > maxPathBytes || !utf8.ValidString(prefix) || strings.ContainsRune(prefix, 0) {
		fail(w, errBadPath)
		return
	}
	limit := defComplete
	if v := q.Get("limit"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 {
			fail(w, werr(http.StatusBadRequest, "bad_request", "limit is a number from 1 to 50"))
			return
		}
		limit = min(n, maxComplete)
	}
	release, err := s.acquire(r.Context())
	if err != nil {
		return
	}
	defer release()
	files, _ := listFiles(r.Context(), sess.Root())
	needle := strings.ToLower(strings.TrimPrefix(prefix, "@"))
	var first, rest []string
	seen := map[string]bool{}
	consider := func(p string) {
		if seen[p] || hiddenPath(sess, p) {
			return
		}
		seen[p] = true
		low := strings.ToLower(p)
		if needle != "" && !strings.Contains(low, needle) {
			return
		}
		if s.classOf(sess, strings.TrimSuffix(p, "/")).protected != nil {
			return
		}
		base := low
		if i := strings.LastIndexByte(strings.TrimSuffix(low, "/"), '/'); i >= 0 {
			base = low[i+1:]
		}
		if strings.HasPrefix(low, needle) || strings.HasPrefix(base, needle) {
			first = append(first, p)
		} else {
			rest = append(rest, p)
		}
	}
	for _, f := range files {
		if len(first) >= limit {
			break
		}
		for d := dirOf(f); d != ""; d = dirOf(d) {
			consider(d + "/")
		}
		consider(f)
	}
	out := append(first, rest...)
	if len(out) > limit {
		out = out[:limit]
	}
	if out == nil {
		out = []string{}
	}
	_ = web.WriteJSON(w, http.StatusOK, map[string]any{"paths": out})
}

// handleCheck answers POST /api/sessions/{id}/permissions/check, the "Would it ask?"
// tester: what the tab's permission engine would do with the command or path, judged in
// the engine's own order and without asking anyone; the reason names the deciding rule
// and its origin, or the mode's default.
func (s *service) handleCheck(w http.ResponseWriter, r *http.Request) {
	_, sess, ok := s.tabOf(w, r)
	if !ok {
		return
	}
	var req wire.PermCheckRequest
	if !web.DecodeJSON(w, r, &req) {
		return
	}
	if len(req.Arg) > maxPathBytes || strings.ContainsRune(req.Arg, 0) {
		fail(w, werr(http.StatusBadRequest, "bad_request", "the command or path is too long"))
		return
	}
	c, err := sess.Classify(req.Tool, req.Arg)
	if session.IsBadTool(err) {
		fail(w, werr(http.StatusBadRequest, "bad_tool", "the tool is Bash, Edit or Read"))
		return
	}
	if err != nil {
		fail(w, err)
		return
	}
	_ = web.WriteJSON(w, http.StatusOK, verdictOf(c, sess.Perm.Mode()))
}

// verdictOf words a classification as the tester shows it.
func verdictOf(c perm.Classification, mode perm.Mode) wire.PermVerdict {
	by := func() string {
		s := c.Rule
		if c.Origin != "" {
			s += " (" + c.Origin + ")"
		}
		return s
	}
	why := c.Why
	switch {
	case c.NoOneToAsk && c.Rule != "":
		why = "an ask rule: " + by() + ", and this session has nobody to answer, so it is refused"
	case c.NoOneToAsk:
		why = strings.TrimPrefix(c.Why, "approval required: ")
	case c.Tier == perm.TierRule && c.Verdict == perm.Deny:
		why = "a deny rule: " + by()
	case c.Tier == perm.TierRule && c.Verdict == perm.Ask:
		why = "an ask rule: " + by()
	case c.Tier == perm.TierRule && c.Verdict == perm.Allow:
		why = "an allow rule: " + by()
	case c.Tier == perm.TierHard || c.Tier == perm.TierGuarded:
		why = strings.TrimPrefix(c.Why, "built-in protection: ")
		if c.Origin == perm.OriginBuiltIn {
			why = "a built-in protection (" + c.Tier + "): " + why
		}
	}
	v := wire.PermVerdict{Why: why}
	switch c.Verdict {
	case perm.Allow:
		v.D, v.Cls = "allowed", "ok"
		if c.Tier == perm.TierMode && (mode == perm.ModeBypass || mode == perm.ModeYolo) {
			v.Cls = "err" // allowed only because the mode asks nothing
		}
	case perm.Ask:
		v.D, v.Cls = "ask", "warm"
	default:
		v.D, v.Cls = "refused", "err"
	}
	return v
}
