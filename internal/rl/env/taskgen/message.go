package taskgen

import (
	"context"
	"fmt"
	"regexp"
	"sort"
	"strings"
)

// trailerRe matches commit-message trailers that carry people, review links or
// bookkeeping, none of which belongs in a prompt.
var trailerRe = regexp.MustCompile(`(?i)^(signed-off-by|co-authored-by|co-developed-by|change-id|reviewed-by|reviewed-on|acked-by|tested-by|reported-by|suggested-by|assisted-by|generated-by|claude-session|cc|bug|pr-url|differential revision|phabricator-rev|github-pr-number|\(cherry picked from commit [0-9a-f]+\))\b.*$`)

// upstreamLinkRe matches links to the very commit or pull request the task was
// mined from (or its siblings): a prompt must not point the agent at the
// solution's home.
var upstreamLinkRe = regexp.MustCompile(`https?://[^\s)]+/(commit|commits|pull|merge_requests|compare)/[0-9A-Za-z._-]+[^\s)]*`)

var sha40Re = regexp.MustCompile(`\b[0-9a-f]{40}\b`)

// issueRefRe finds closing references such as "Fixes #123" or "closes org/repo#7".
var issueRefRe = regexp.MustCompile(`(?i)\b(?:fix(?:es|ed)?|close[sd]?|resolve[sd]?)\s*:?\s+((?:[\w.-]+/[\w.-]+)?#\d+)`)

// IssueRefs returns the issue references a commit message closes, in order and
// without duplicates.
func IssueRefs(msg string) []string {
	var out []string
	seen := map[string]bool{}
	for _, m := range issueRefRe.FindAllStringSubmatch(msg, -1) {
		if !seen[m[1]] {
			seen[m[1]] = true
			out = append(out, m[1])
		}
	}
	return out
}

// IssueResolver fetches the text of an issue for a prompt ("#123",
// "org/repo#123"). It is a hook: the generator itself never touches the network.
type IssueResolver func(ctx context.Context, ref string) (string, error)

// CleanMessage removes trailers, upstream links and bare commit ids from a
// commit message and normalises whitespace.
func CleanMessage(msg string) string {
	var out []string
	for _, line := range strings.Split(strings.ReplaceAll(msg, "\r\n", "\n"), "\n") {
		line = strings.TrimRight(line, " \t")
		if trailerRe.MatchString(strings.TrimSpace(line)) {
			continue
		}
		line = upstreamLinkRe.ReplaceAllString(line, "")
		line = sha40Re.ReplaceAllString(line, "")
		out = append(out, line)
	}
	text := strings.Join(out, "\n")
	// Collapse runs of blank lines.
	text = regexp.MustCompile(`\n{3,}`).ReplaceAllString(text, "\n\n")
	return strings.TrimSpace(text)
}

// InferKind guesses the task kind from a commit subject.
func InferKind(subject string) string {
	s := strings.ToLower(strings.TrimSpace(subject))
	if i := strings.IndexAny(s, ":("); i > 0 && i < 12 {
		switch strings.TrimSpace(s[:i]) {
		case "fix", "bugfix", "hotfix":
			return "fix"
		case "feat", "feature":
			return "feature"
		case "refactor", "cleanup", "style":
			return "refactor"
		}
	}
	first := s
	if f := strings.Fields(s); len(f) > 0 {
		first = f[0]
	}
	switch first {
	case "fix", "fixes", "fixed", "repair", "correct", "resolve", "handle", "prevent", "avoid":
		return "fix"
	case "add", "adds", "added", "implement", "support", "introduce", "allow", "enable", "new", "feat":
		return "feature"
	case "refactor", "rename", "move", "extract", "simplify", "clean", "cleanup", "remove", "drop", "deduplicate":
		return "refactor"
	}
	switch {
	case strings.Contains(s, "panic"), strings.Contains(s, "crash"), strings.Contains(s, "regression"), strings.Contains(s, "bug"):
		return "fix"
	}
	return "fix"
}

// BuildPrompt turns a commit message (and optional issue texts) into the task
// prompt. It says what to change, never how the change was made upstream.
func BuildPrompt(message string, issues map[string]string) string {
	msg := CleanMessage(message)
	var b strings.Builder
	b.WriteString(msg)
	if len(issues) > 0 {
		refs := make([]string, 0, len(issues))
		for r := range issues {
			refs = append(refs, r)
		}
		sort.Strings(refs)
		for _, r := range refs {
			if t := strings.TrimSpace(issues[r]); t != "" {
				fmt.Fprintf(&b, "\n\nIssue %s:\n%s", r, t)
			}
		}
	}
	b.WriteString("\n\nMake the change described above. Your work will be checked by a test suite that you cannot see, so check the behaviour carefully; do not edit existing test files.")
	return b.String()
}
