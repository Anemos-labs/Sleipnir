package env

import (
	"fmt"
	"sort"
)

// Ownership is a compiled team.owns (rl.Team.Owns): for each role, the globs of
// the repository paths that only agents of that role may write. The globs have
// Verifier.Protected's syntax and matching (see CompileGlobs), so "*.css" is
// every style sheet at any depth and "/web/" the top-level web directory. The RL
// harness hands it to the swarm, whose write guard asks OwnersOf before a write.
type Ownership struct {
	roles []string   // sorted
	globs []*Matcher // globs[i] are the paths roles[i] owns
}

// CompileOwnership compiles team.owns. It returns nil when owns names no role,
// and an error naming the role for the first glob that does not compile.
func CompileOwnership(owns map[string][]string) (*Ownership, error) {
	if len(owns) == 0 {
		return nil, nil
	}
	o := &Ownership{}
	for r := range owns {
		o.roles = append(o.roles, r)
	}
	sort.Strings(o.roles)
	for _, r := range o.roles {
		m, err := CompileGlobs(owns[r])
		if err != nil {
			return nil, fmt.Errorf("team.owns[%q]: %w", r, err)
		}
		o.globs = append(o.globs, m)
	}
	return o, nil
}

// OwnersOf returns, sorted, the roles that own the slash-separated
// repository-relative path rel: those with a glob that matches it. None means
// that any role may write it. A nil Ownership owns nothing.
func (o *Ownership) OwnersOf(rel string) []string {
	if o == nil {
		return nil
	}
	var out []string
	for i, m := range o.globs {
		if m.Match(rel) {
			out = append(out, o.roles[i])
		}
	}
	return out
}

// Roles lists, sorted, the roles that own paths.
func (o *Ownership) Roles() []string {
	if o == nil {
		return nil
	}
	return append([]string(nil), o.roles...)
}
