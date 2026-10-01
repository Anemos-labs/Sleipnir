package input

import (
	"sort"
	"strings"
)

// Choice is one thing Choices offers: Text is what is inserted, Detail the dim words after it.
type Choice struct {
	Text   string
	Detail string
}

// Choices completes the argument of one slash command ("/model qw") from a list the caller supplies, called on each use and
// expected to answer from memory (it may be empty while the list is still being fetched). Every space-separated word of the
// argument must match the text (Fuzzy), so "qwen flash" finds "heimdall/qwen/qwen3.8-flash-next" and ordering is by the summed
// scores; choices that tie keep the order given, so the caller puts favorites first. With nothing typed, the list comes in the
// order given. Text with whitespace or control characters is never offered.
func Choices(command string, list func() []Choice) Completer {
	prefix := "/" + strings.TrimPrefix(command, "/") + " "
	return CompleterFunc(func(line string, cursor int) (int, []Candidate) {
		if cursor < len(prefix) || cursor > len(line) || !strings.HasPrefix(line, prefix) || strings.ContainsAny(line[:cursor], "\U0000fffc") {
			return 0, nil
		}
		words := strings.Fields(line[len(prefix):cursor])
		type scored struct {
			c     Choice
			score int
		}
		var keep []scored
	next:
		for _, c := range list() {
			if c.Text == "" || cleanLine(c.Text) != c.Text || strings.IndexFunc(c.Text, func(r rune) bool { return r <= ' ' || r == 0x7f }) >= 0 {
				continue
			}
			total := 0
			for _, w := range words {
				s, ok := Fuzzy(w, c.Text)
				if !ok {
					continue next
				}
				total += s
			}
			keep = append(keep, scored{c, total})
		}
		sort.SliceStable(keep, func(i, j int) bool { return keep[i].score > keep[j].score })
		cands := make([]Candidate, len(keep))
		for i, k := range keep {
			cands[i] = Candidate{Text: k.c.Text + " ", Display: k.c.Text, Detail: k.c.Detail}
		}
		return len(prefix), cands
	})
}
