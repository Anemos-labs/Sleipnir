package reward

import (
	"strings"
	"unicode"
)

// A deliberately small shell tokenizer. The detectors only need to know which
// words a command runs and where its output goes; they do not execute or expand
// anything. It understands quoting, operators, redirections and heredocs well
// enough that text inside a quoted string or a heredoc body is never mistaken
// for a command, and it is linear in the input.

type redirection struct {
	op     string // ">", ">>", "&>", ">|", "<>" (writes) or "<" (reads)
	target string
}

type shellCmd struct {
	words  []string // command name first
	redirs []redirection
}

// name is the command word after env assignments and wrappers such as sudo.
func (c shellCmd) name() (string, int) {
	i := 0
	for i < len(c.words) {
		w := c.words[i]
		switch {
		case strings.Contains(w, "=") && !strings.HasPrefix(w, "-") && isAssignment(w):
			i++
		case w == "sudo" || w == "env" || w == "time" || w == "nice" || w == "nohup" || w == "command" || w == "exec" || w == "xargs" || w == "doas":
			i++
			// skip wrapper flags
			for i < len(c.words) && strings.HasPrefix(c.words[i], "-") {
				i++
			}
		default:
			return baseName(w), i
		}
	}
	return "", i
}

func isAssignment(w string) bool {
	eq := strings.IndexByte(w, '=')
	if eq <= 0 {
		return false
	}
	for _, r := range w[:eq] {
		if !(r == '_' || unicode.IsLetter(r) || unicode.IsDigit(r)) {
			return false
		}
	}
	return true
}

func baseName(w string) string {
	if i := strings.LastIndexByte(w, '/'); i >= 0 && i+1 < len(w) {
		return w[i+1:]
	}
	return w
}

// args returns the words after the command name.
func (c shellCmd) args() []string {
	_, i := c.name()
	if i+1 > len(c.words) {
		return nil
	}
	return c.words[i+1:]
}

const maxShellBytes = 4 << 20

// parseShell splits a command line (or script) into simple commands.
func parseShell(src string) []shellCmd {
	if len(src) > maxShellBytes {
		src = src[:maxShellBytes]
	}
	var cmds []shellCmd
	var cur shellCmd
	var word strings.Builder
	inWord := false
	var heredocs []string // pending delimiters, consumed at the next newline

	flushWord := func() {
		if !inWord {
			return
		}
		cur.words = append(cur.words, word.String())
		word.Reset()
		inWord = false
	}
	flushCmd := func() {
		flushWord()
		if len(cur.words) > 0 || len(cur.redirs) > 0 {
			cmds = append(cmds, cur)
		}
		cur = shellCmd{}
	}
	pendingRedir := ""

	addWord := func(w string) {
		if pendingRedir != "" {
			cur.redirs = append(cur.redirs, redirection{op: pendingRedir, target: w})
			pendingRedir = ""
			return
		}
		cur.words = append(cur.words, w)
	}
	finishWord := func() {
		if !inWord {
			return
		}
		w := word.String()
		word.Reset()
		inWord = false
		addWord(w)
	}

	i, n := 0, len(src)
	for i < n {
		c := src[i]
		switch {
		case c == '\\' && i+1 < n:
			if src[i+1] == '\n' { // line continuation
				i += 2
				continue
			}
			word.WriteByte(src[i+1])
			inWord = true
			i += 2
		case c == '\'':
			inWord = true
			j := strings.IndexByte(src[i+1:], '\'')
			if j < 0 {
				word.WriteString(src[i+1:])
				i = n
				break
			}
			word.WriteString(src[i+1 : i+1+j])
			i += j + 2
		case c == '"':
			inWord = true
			i++
			for i < n && src[i] != '"' {
				if src[i] == '\\' && i+1 < n {
					i++
				}
				word.WriteByte(src[i])
				i++
			}
			i++
		case c == '#' && !inWord:
			// comment to end of line
			for i < n && src[i] != '\n' {
				i++
			}
		case c == '\n':
			finishWord()
			flushCmd()
			pendingRedir = ""
			i++
			// skip heredoc bodies
			for len(heredocs) > 0 {
				delim := heredocs[0]
				heredocs = heredocs[1:]
				for i < n {
					end := strings.IndexByte(src[i:], '\n')
					var line string
					if end < 0 {
						line = src[i:]
						i = n
					} else {
						line = src[i : i+end]
						i += end + 1
					}
					if strings.TrimSpace(strings.TrimSuffix(line, "\r")) == delim {
						break
					}
				}
			}
		case c == ';' || c == '|' || c == '&' || c == '(' || c == ')' || c == '{' || c == '}':
			// '&>' and '>&' are redirections, not separators.
			if c == '&' && i+1 < n && src[i+1] == '>' {
				finishWord()
				i++
				continue // the '>' branch reads the operator
			}
			finishWord()
			if c == '|' || c == '&' || c == ';' {
				flushCmd()
				pendingRedir = ""
			}
			for i+1 < n && (src[i+1] == c) && (c == '|' || c == '&') {
				i++
			}
			i++
		case c == '>' || c == '<':
			// The redirection operator, possibly prefixed by a file descriptor that
			// is already sitting in the current word.
			fd := ""
			if inWord && isDigits(word.String()) {
				fd = word.String()
				word.Reset()
				inWord = false
			}
			finishWord()
			op := string(c)
			j := i + 1
			if c == '>' && j < n && (src[j] == '>' || src[j] == '|') {
				op += string(src[j])
				j++
			}
			if c == '<' && j < n && src[j] == '<' {
				// heredoc / here-string
				j++
				if j < n && src[j] == '<' { // <<< here-string
					i = j + 1
					continue
				}
				if j < n && src[j] == '-' {
					j++
				}
				for j < n && (src[j] == ' ' || src[j] == '\t') {
					j++
				}
				k := j
				quote := byte(0)
				if k < n && (src[k] == '\'' || src[k] == '"') {
					quote = src[k]
					k++
					j = k
				}
				for k < n && !unicode.IsSpace(rune(src[k])) && src[k] != ';' && src[k] != '|' && src[k] != '&' && src[k] != ')' && (quote == 0 || src[k] != quote) {
					k++
				}
				if k > j {
					heredocs = append(heredocs, src[j:k])
				}
				if quote != 0 && k < n {
					k++
				}
				i = k
				continue
			}
			if c == '>' && j < n && src[j] == '&' {
				// fd duplication such as 2>&1: not a file write. Swallow the target.
				j++
				for j < n && !unicode.IsSpace(rune(src[j])) && src[j] != ';' && src[j] != '|' && src[j] != '&' {
					j++
				}
				i = j
				continue
			}
			_ = fd
			pendingRedir = op
			i = j
		case c == ' ' || c == '\t' || c == '\r':
			finishWord()
			i++
		default:
			word.WriteByte(c)
			inWord = true
			i++
		}
	}
	finishWord()
	flushCmd()
	return cmds
}

func isDigits(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}
