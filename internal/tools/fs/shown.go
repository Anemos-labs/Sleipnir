package fs

import (
	"bytes"
	"os"
	"strings"

	"github.com/anemos-labs/sleipnir/internal/tools"
)

// RecordShown counts as read, for env's agent, each file named by one of words (relative to dir) that output, the text a shell
// command returned to the agent, shows whole and as the file is now: `cat a.go b.go` is the read of both that a model used to
// shells makes before it edits them, and refusing that edit for want of a read tool call costs a request and the change written
// out twice. The test is the content itself, so nothing is taken on trust: a file that output shows only in part (head, cat -n, a
// clipped or paged result), one changed since, a long one (as long as a refused change would show) and one not text are left
// unread. It returns how many files it recorded.
func RecordShown(env *tools.Env, dir string, words []string, output string) int {
	if env == nil || env.Files == nil || len(words) == 0 || output == "" {
		return 0
	}
	e := *env
	e.Cwd = dir
	k := &call{env: &e}
	n := 0
	seen := map[string]bool{}
	for _, w := range words {
		canon, err := k.resolve(w)
		if err != nil || seen[canon] {
			continue
		}
		seen[canon] = true
		fi, err := os.Lstat(canon)
		if err != nil || !fi.Mode().IsRegular() || fi.Size() < minShown || fi.Size() > maxShownOnRefusal {
			continue
		}
		data, err := os.ReadFile(canon)
		if err != nil || bytes.IndexByte(data, 0) >= 0 || !showsWhole(output, string(data)) {
			continue
		}
		env.Files.RecordRead(env.Agent, canon, data)
		n++
	}
	return n
}

// minShown is the smallest file RecordShown counts: a few bytes can turn up in any output by chance.
const minShown = 16

// showsWhole says whether content appears in output from the start of a line.
func showsWhole(output, content string) bool {
	return strings.HasPrefix(output, content) || strings.Contains(output, "\n"+content)
}
