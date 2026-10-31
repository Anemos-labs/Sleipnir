package scan

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"path"
	"strings"
	"sync"
	"unicode/utf8"

	"github.com/anemos-labs/sleipnir/internal/gitx"
)

// Code is the repository's files at the scan's commit. Every cite is checked against it.
type Code interface {
	Read(ctx context.Context, file string) ([]byte, error)
}

// gitCode reads files at a commit through the hardened git runner, keeping the last files read.
type gitCode struct {
	repo   *gitx.Repo
	commit string

	mu    sync.Mutex
	cache map[string][]byte
	order []string
}

// CodeAt is the repository's files at commit.
func CodeAt(repo *gitx.Repo, commit string) Code {
	return &gitCode{repo: repo, commit: commit, cache: map[string][]byte{}}
}

const codeCacheFiles = 256

func (g *gitCode) Read(ctx context.Context, file string) ([]byte, error) {
	g.mu.Lock()
	if b, ok := g.cache[file]; ok {
		g.mu.Unlock()
		return b, nil
	}
	g.mu.Unlock()
	b, err := g.repo.Show(ctx, g.commit, file)
	if err != nil {
		return nil, fmt.Errorf("%s is not a file of the scanned commit", file)
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	if len(g.order) >= codeCacheFiles {
		delete(g.cache, g.order[0])
		g.order = g.order[1:]
	}
	g.cache[file] = b
	g.order = append(g.order, file)
	return b, nil
}

// Quote bounds.
const (
	maxQuoteLines = 15
	maxQuoteRunes = 1200
	minQuoteRunes = 6
	// citeWindow is how far from the named line the harness looks for a quote before it looks in the whole file.
	citeWindow = 6
)

// CleanPath is a cite's file as the ledger keeps it: slash-separated, relative to the repository root.
func CleanPath(p string) (string, error) {
	p = strings.TrimSpace(strings.ReplaceAll(p, "\\", "/"))
	p = strings.TrimPrefix(p, "./")
	c := path.Clean("/" + p)[1:]
	if c == "" || c == "." || strings.HasPrefix(p, "/") || strings.Contains(p, "..") {
		return "", fmt.Errorf("file %q: name a file of the repository by its path from the root (src/app.js)", p)
	}
	return c, nil
}

// CheckCite checks that the quote is in the file at the commit, at or near the line named, and returns the cite as the ledger
// keeps it: the line set to where the quote starts, and a note when that is not the line the agent named. A quote that is not
// in the file is refused with the lines around the one named, so the agent can copy them.
func CheckCite(ctx context.Context, code Code, c Cite) (Cite, string, error) {
	file, err := CleanPath(c.File)
	if err != nil {
		return Cite{}, "", err
	}
	c.File = file
	quote := strings.Trim(c.Quote, "\n")
	if strings.TrimSpace(quote) == "" {
		return Cite{}, "", fmt.Errorf("%s:%d: quote the code, copied from the file (1 to %d lines)", file, c.Line, maxQuoteLines)
	}
	want := normLines(quote)
	if len(want) > maxQuoteLines || utf8.RuneCountInString(quote) > maxQuoteRunes {
		return Cite{}, "", fmt.Errorf("%s:%d: the quote is too long; quote the %d lines that matter at most", file, c.Line, maxQuoteLines)
	}
	if utf8.RuneCountInString(strings.Join(want, "")) < minQuoteRunes {
		return Cite{}, "", fmt.Errorf("%s:%d: the quote is too short to place; quote the whole line", file, c.Line)
	}
	data, err := code.Read(ctx, file)
	if err != nil {
		return Cite{}, "", err
	}
	lines := strings.Split(string(data), "\n")
	if c.Line < 1 || c.Line > len(lines) {
		if at := findQuote(lines, want, 0, len(lines)); at >= 0 {
			return Cite{File: file, Line: at + 1, Quote: quote}, fmt.Sprintf("(%s: the quote is at line %d)", file, at+1), nil
		}
		return Cite{}, "", fmt.Errorf("%s has %d lines and the quote is not in it", file, len(lines))
	}
	if at := findQuote(lines, want, c.Line-1-citeWindow, c.Line-1+citeWindow); at >= 0 {
		note := ""
		if at+1 != c.Line {
			note = fmt.Sprintf("(%s: the quote starts at line %d, not %d: kept at %d)", file, at+1, c.Line, at+1)
		}
		return Cite{File: file, Line: at + 1, Quote: quote}, note, nil
	}
	if at := findQuote(lines, want, 0, len(lines)); at >= 0 {
		return Cite{File: file, Line: at + 1, Quote: quote}, fmt.Sprintf("(%s: the quote is at line %d, not near %d: kept at %d)", file, at+1, c.Line, at+1), nil
	}
	if bytes.IndexByte(data[:min(len(data), 8000)], 0) >= 0 {
		return Cite{}, "", fmt.Errorf("%s is a binary and the quote is not in it: quote a string it holds, exactly as strings prints it (6 characters or more), or cite the file that builds or loads it", file)
	}
	return Cite{}, "", errors.New(notThere(file, lines, c.Line))
}

// notThere is the refusal of a quote that is not in the file: the lines around the one named, numbered.
func notThere(file string, lines []string, line int) string {
	var b strings.Builder
	fmt.Fprintf(&b, "%s: the quote is not in the file at the scanned commit. Lines %d-%d say:\n", file, max(line-3, 1), min(line+3, len(lines)))
	for i := max(line-4, 0); i < min(line+3, len(lines)); i++ {
		fmt.Fprintf(&b, "%6d  %s\n", i+1, clip(lines[i], 200))
	}
	b.WriteString("Copy the code as it is there (read the file again if it changed since you read it).")
	return b.String()
}

// normLines is text's non-blank lines with runs of white space made one space.
func normLines(text string) []string {
	var out []string
	for _, l := range strings.Split(text, "\n") {
		if n := strings.Join(strings.Fields(l), " "); n != "" {
			out = append(out, n)
		}
	}
	return out
}

// findQuote is the index of the first line from lo to hi (clamped) where the quote starts: its lines, white space aside, are
// in the file's non-blank lines from there on, the first and last of them allowed to be part of a line (an agent quotes the part of a long line that matters). -1 when it is not.
func findQuote(lines, want []string, lo, hi int) int {
	lo, hi = max(lo, 0), min(hi, len(lines)-1)
	for i := lo; i <= hi; i++ {
		if matchAt(lines, want, i) {
			return i
		}
	}
	return -1
}

func matchAt(lines, want []string, i int) bool {
	j := i
	for k, w := range want {
		for j < len(lines) && strings.TrimSpace(lines[j]) == "" {
			if k == 0 {
				return false
			}
			j++
		}
		if j >= len(lines) {
			return false
		}
		got := strings.Join(strings.Fields(lines[j]), " ")
		if k == 0 || k == len(want)-1 {
			if !strings.Contains(got, w) {
				return false
			}
		} else if got != w {
			return false
		}
		j++
	}
	return true
}

func clip(s string, n int) string {
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	r := []rune(s)
	return string(r[:n]) + "…"
}
