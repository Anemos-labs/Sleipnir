package fs

import (
	"bytes"
	"fmt"
	"os"
	"strconv"
	"strings"

	"github.com/anemos-labs/sleipnir/internal/tools"
)

// RecordShown counts as read, for env's agent, each file named by one of words (relative to dir) that output, the text a shell
// command returned to the agent, shows whole and as the file is now, as cat prints it or cat -n: `cat a.go b.go` is the read of
// both that a model used to shells makes before it edits them, and refusing that edit for want of a read tool call costs a
// request and the change written out twice. The test is the content itself, so nothing is taken on trust: a file that output
// shows only in part (head, a clipped or paged result), one changed since, a long one (as long as a refused change would show)
// and one not text are left unread. It returns how many files it recorded.
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
		if err != nil || bytes.IndexByte(data, 0) >= 0 || !showsWhole(output, string(data)) && !showsNumbered(output, data) {
			continue
		}
		env.Files.RecordRead(env.Agent, canon, data)
		n++
	}
	return n
}

// minShown is the smallest file RecordShown counts: a few bytes can turn up in any output by chance.
const minShown = 16

// showsNumbered says whether output shows data whole as cat -n prints it: every line, blank ones too, after its number
// right-aligned in six columns and a tab, the numbers counting on from wherever they start (cat -n a b numbers b's lines after
// a's).
func showsNumbered(output string, data []byte) bool {
	first, _, _ := strings.Cut(string(data), "\n")
	for from, tries := 0, 0; tries < maxNumberedTries; tries++ {
		i := strings.Index(output[from:], "\t"+first)
		if i < 0 {
			return false
		}
		i += from
		from = i + 1
		start := strings.LastIndexByte(output[:i], '\n') + 1
		n, err := strconv.Atoi(strings.TrimLeft(output[start:i], " "))
		if err == nil && n > 0 && strings.HasPrefix(output[start:], catNumbered(data, n)) {
			return true
		}
	}
	return false
}

// maxNumberedTries bounds the places showsNumbered looks at for a file's first line.
const maxNumberedTries = 16

// catNumbered is data as cat -n prints it, its first line numbered first.
func catNumbered(data []byte, first int) string {
	var b strings.Builder
	for i, line := range strings.SplitAfter(string(data), "\n") {
		if line == "" {
			break // after the final newline
		}
		fmt.Fprintf(&b, "%6d\t%s", first+i, line)
	}
	return b.String()
}

// showsWhole says whether content appears in output from the start of a line.
func showsWhole(output, content string) bool {
	return strings.HasPrefix(output, content) || strings.Contains(output, "\n"+content)
}
