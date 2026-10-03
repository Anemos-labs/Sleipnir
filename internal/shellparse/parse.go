package shellparse

// builder collects the words of the simple command being read.
type builder struct {
	words      []string
	env        []string
	redirs     []Redirect
	start, end int
	has        bool
	piped      bool
	bodies     []string // heredoc bodies and here-strings feeding the command's stdin
}

// parser groups a token stream into simple commands. It is deliberately not a
// full shell grammar: compound commands are flattened, and the constructs it
// cannot follow (case, function definitions, arithmetic commands) mark the
// analysis as unparsed instead of being guessed at.
type parser struct {
	st     *state
	src    string
	toks   []token
	i      int
	depth  int
	top    bool // Background is only meaningful for the outermost line
	nested bool // commands come from a substitution / eval / sh -c string

	out     []Simple
	cur     builder
	pending []Simple // commands found inside substitutions, emitted after cur

	parens     int
	groups     int
	have       bool // a complete command (or group) precedes the next operator
	needCmd    bool // an && || | is waiting for its right-hand side
	nextPiped  bool
	skipHeader bool // inside the word list of for/select
	header     []string
	headerTok  token // the "for" keyword, for Raw
	inDbl      bool  // inside [[ ... ]]
	caseState  int   // best-effort tracking of case ... esac (which is never trusted)
}

// case is not analysed (the analysis is marked unparsed) but its bodies are
// still walked so a visibly bad command inside one can be vetoed.
const (
	caseNone    = iota
	caseSubject // between "case" and "in"
	casePattern // reading patterns up to ")"
	caseBody    // reading commands up to ";;"
)

// parseTokens runs a shell parser over the supplied tokens with shared state and returns collected
// simple commands.
func parseTokens(st *state, src string, toks []token, depth int, top, nested bool) []Simple {
	p := &parser{st: st, src: src, toks: toks, depth: depth, top: top, nested: nested}
	p.run()
	return p.out
}

func (p *parser) run() {
	for p.i < len(p.toks) && !p.st.dead {
		t := p.toks[p.i]
		p.i++
		p.pending = append(p.pending, t.nested...)
		switch t.kind {
		case tkOp:
			p.op(t)
		case tkRedir:
			p.redir(t)
		default:
			p.word(t)
		}
	}
	p.finish()
	switch {
	case p.needCmd:
		p.st.problem("command line ends with an operator")
	case p.parens != 0:
		p.st.problem("unbalanced parentheses")
	case p.groups != 0:
		p.st.problem("unclosed { group")
	case p.inDbl:
		p.st.problem("unterminated [[")
	}
}

// touch records that t belongs to the current command.
func (p *parser) touch(t token) {
	if !p.cur.has {
		p.cur.has = true
		p.cur.start = t.start
		p.cur.piped = p.nextPiped
		p.nextPiped = false
		p.needCmd = false
	}
	if t.end > p.cur.end {
		p.cur.end = t.end
	}
}

func (p *parser) word(t token) {
	if p.skipHeader {
		p.header = append(p.header, t.text)
		if t.end > p.headerTok.end {
			p.headerTok.end = t.end
		}
		return
	}
	switch p.caseState {
	case caseSubject:
		if !t.quoted && t.text == "in" {
			p.caseState = casePattern
		}
		return
	case casePattern:
		if !t.quoted && t.text == "esac" {
			p.caseState = caseNone
			p.have = true
		}
		return
	}
	if p.inDbl {
		if !t.quoted && t.text == "]]" {
			p.inDbl = false
		}
		p.touch(t)
		p.cur.words = append(p.cur.words, t.text)
		return
	}
	if !p.cur.has && !t.quoted && p.reserved(t) {
		return
	}
	p.touch(t)
	if t.assign && len(p.cur.words) == 0 {
		p.cur.env = append(p.cur.env, t.text)
		return
	}
	if t.alts != nil {
		p.cur.words = append(p.cur.words, t.alts...)
		return
	}
	p.cur.words = append(p.cur.words, t.text)
}

// reserved handles shell keywords at command position. It reports whether the
// token was consumed.
func (p *parser) reserved(t token) bool {
	switch t.text {
	case "!", "if", "then", "elif", "else", "while", "until", "do":
		return true
	case "fi", "done":
		p.have = true
		return true
	case "esac":
		p.caseState = caseNone
		p.have = true
		return true
	case "{":
		p.groups++
		return true
	case "}":
		if p.groups == 0 {
			p.st.problem("unmatched }")
		} else {
			p.groups--
		}
		p.have = true
		return true
	case "for", "select":
		p.skipHeader = true
		p.header = nil
		p.headerTok = t
		return true
	case "case":
		p.st.problem("case statements are not analysed")
		p.caseState = caseSubject
		return true
	case "time":
		// In bash time is a keyword and may prefix a compound command ("time {
		// a; b; }", "time ( a )"). A bare "time cmd" is left to the wrapper logic.
		j := p.i
		for j < len(p.toks) && p.toks[j].kind == tkWord && !p.toks[j].quoted && p.toks[j].text == "-p" {
			j++
		}
		if j < len(p.toks) {
			n := p.toks[j]
			if n.kind == tkWord && !n.quoted && (n.text == "{" || n.text == "[[" || n.text == "!") ||
				n.kind == tkOp && n.text == "(" {
				p.i = j
				return true
			}
		}
		return false
	case "function", "coproc":
		p.st.problem(t.text + " is not analysed")
		return true
	case "[[":
		p.inDbl = true
		p.touch(t)
		p.cur.words = append(p.cur.words, "[[")
		return true
	}
	return false
}

func (p *parser) op(t token) {
	switch p.caseState {
	case caseSubject:
		return
	case casePattern:
		if t.text == ")" {
			p.caseState = caseBody
		}
		return
	case caseBody:
		if t.text == ";;" || t.text == ";&" || t.text == ";;&" {
			p.finish()
			p.have = false
			p.caseState = casePattern
			return
		}
	}
	if p.inDbl {
		if t.text == "\n" || t.text == ";" {
			p.st.problem("unterminated [[")
			p.inDbl = false
		} else { // && || ( ) | & are operators of the test expression here
			p.touch(t)
			p.cur.words = append(p.cur.words, t.text)
			return
		}
	}
	switch t.text {
	case "\n":
		if p.needCmd { // a newline may follow && || | before the next command
			return
		}
		p.endHeader()
		p.finish()
		p.have = false
	case ";":
		if !(p.cur.has || p.have || p.skipHeader) {
			p.st.problem("empty command before ;")
		}
		p.endHeader()
		p.finish()
		p.have = false
		p.needCmd = false
	case "&&", "||", "|", "|&":
		if !(p.cur.has || p.have) {
			p.st.problem("operator " + t.text + " without a command before it")
		}
		p.finish()
		p.have = false
		p.needCmd = true
		p.nextPiped = t.text == "|" || t.text == "|&"
	case "&":
		if !(p.cur.has || p.have) {
			p.st.problem("& without a command before it")
		}
		p.finish()
		p.have = false
		p.needCmd = false
		if p.top {
			p.st.an.Background = true
		}
	case "(":
		if p.cur.has {
			p.st.problem("unexpected ( (function definition or syntax error)")
			p.finish()
		}
		if p.i < len(p.toks) && p.toks[p.i].kind == tkOp && p.toks[p.i].text == "(" && p.toks[p.i].start == t.end {
			p.st.problem("arithmetic command (( )) is not analysed")
		}
		p.st.an.HasSubshell = true
		p.parens++
		p.needCmd = false
	case ")":
		p.finish()
		if p.parens == 0 {
			p.st.problem("unmatched )")
		} else {
			p.parens--
		}
		p.have = true
	default: // ;; ;& ;;&
		p.st.problem("case statements are not analysed")
		p.finish()
		p.have = false
	}
}

func (p *parser) redir(t token) {
	if p.inDbl { // < and > are string comparisons inside [[ ]]
		p.touch(t)
		p.cur.words = append(p.cur.words, t.text)
		return
	}
	p.touch(t)
	r := Redirect{Op: t.fd + t.text}
	if p.i < len(p.toks) && p.toks[p.i].kind == tkWord {
		tg := p.toks[p.i]
		p.i++
		p.pending = append(p.pending, tg.nested...)
		p.touch(tg)
		r.Target = tg.text
		switch {
		case tg.hasBody:
			p.cur.bodies = append(p.cur.bodies, tg.body)
		case t.text == "<<<":
			p.cur.bodies = append(p.cur.bodies, tg.text)
		}
	} else {
		p.st.problem("redirection " + r.Op + " without a target")
	}
	p.cur.redirs = append(p.cur.redirs, r)
}

// finish turns the command being read into a Simple and emits it, followed by
// the commands found inside its substitutions.
func (p *parser) finish() {
	if !p.cur.has {
		p.flush()
		return
	}
	c := p.cur
	p.cur = builder{}
	s := Simple{Env: c.env, Redirects: c.redirs, Nested: p.nested, Piped: c.piped}
	if c.start >= 0 && c.end <= len(p.src) && c.start <= c.end {
		s.Raw = p.src[c.start:c.end]
	}
	if len(c.words) > 0 {
		s.Program = c.words[0]
		s.Args = append([]string(nil), c.words[1:]...)
	}
	if prob := peel(&s); prob != "" {
		p.st.problem(prob)
	}
	if len(c.words) > 0 && s.Program == "" {
		// "" is a command name the shell cannot run; whatever follows it is not
		// what the line appears to say, so do not present it as a command.
		p.st.problem("empty command name")
	}
	p.scripts(&s)
	p.stdinScripts(&s, c.bodies)
	p.out = append(p.out, s)
	p.have = true
	p.flush()
}

// flush moves pending simple commands into output and clears the pending queue.
func (p *parser) flush() {
	if len(p.pending) > 0 {
		p.out = append(p.out, p.pending...)
		p.pending = nil
	}
}

// scripts extracts the command string handed to eval, sh -c, bash -c, su -c
// (also behind sudo) and parses it as part of this line: what a shell is told
// to run is exactly as relevant as what is written at the top level.
func (p *parser) scripts(s *Simple) {
	prog, args := s.EffectiveCommand()
	script, kind := inlineScript(prog, args)
	switch kind {
	case inlineNone:
		return
	case inlineMissing:
		p.st.problem("shell -c without a command string")
		return
	}
	if p.depth >= maxDepth {
		p.st.problem("nesting too deep")
		return
	}
	l := newLexer(script, p.st, p.depth+1)
	toks, _ := l.lexUntil(false)
	p.pending = append(p.pending, parseTokens(p.st, script, toks, p.depth+1, false, true)...)
}

// stdinScripts parses heredocs and here-strings that feed a shell reading its
// program from stdin (bash <<EOF, sh <<< "cmd"): the text is executed.
func (p *parser) stdinScripts(s *Simple, bodies []string) {
	if len(bodies) == 0 {
		return
	}
	prog, args := s.EffectiveCommand()
	if !posixShells[stdBase(prog)] || !readsCodeFromStdin(prog, args) {
		return
	}
	for _, b := range bodies {
		if p.depth >= maxDepth {
			p.st.problem("nesting too deep")
			return
		}
		l := newLexer(b, p.st, p.depth+1)
		toks, _ := l.lexUntil(false)
		p.pending = append(p.pending, parseTokens(p.st, b, toks, p.depth+1, false, true)...)
	}
}

// endHeader closes the word list of a for/select loop. The words after "in" are
// names the loop will iterate over, typically files (for f in ~/.ssh/*), so they
// are reported as a pseudo-command "for" whose Args they are: a caller judging
// file accesses sees them, even though the loop body only mentions $f.
func (p *parser) endHeader() {
	if !p.skipHeader {
		return
	}
	p.skipHeader = false
	words := p.header
	p.header = nil
	loopVar := ""
	for i, w := range words {
		if w == "in" && i >= 1 {
			if i == 1 && isName(words[0]) {
				loopVar = words[0]
			}
			words = words[i+1:]
			break
		}
		if i == len(words)-1 {
			words = nil // no "in": the loop runs over the positional parameters
		}
	}
	if len(words) == 0 {
		return
	}
	if p.cur.has { // cannot happen for well-formed input; keep the pieces apart
		p.finish()
	}
	raw := ""
	if p.headerTok.start >= 0 && p.headerTok.end <= len(p.src) && p.headerTok.start <= p.headerTok.end {
		raw = p.src[p.headerTok.start:p.headerTok.end]
	}
	p.out = append(p.out, Simple{
		Program: "for", Args: append([]string(nil), words...), Raw: raw, Nested: p.nested, LoopVar: loopVar,
	})
	p.have = true
	p.flush()
}

// isName reports whether s is a shell variable name: a letter or an underscore, then letters, digits and underscores.
func isName(s string) bool {
	if s == "" {
		return false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c == '_' || c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || i > 0 && c >= '0' && c <= '9' {
			continue
		}
		return false
	}
	return true
}
