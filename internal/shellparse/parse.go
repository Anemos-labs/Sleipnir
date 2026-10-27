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
	literal    []Span   // where the bodies of its heredocs with a quoted delimiter are in the source
	arith      []string // the arithmetic expansions of its words (Simple.Arith)
}

// parser groups a token stream into simple commands. It is deliberately not a
// full shell grammar: compound commands are flattened, and the constructs it
// cannot follow (function definitions, arithmetic commands) mark the analysis
// as unparsed instead of being guessed at.
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
	blocks     int  // open if/while/until/for/select compounds
	andOr      bool // an && or || came before the command being read, in its list
	last       int  // index in out of the last command finish emitted, -1 before any
	listStart  int  // index in out of the first command of the current and-or list (after the last ;, & or newline)
	have       bool // a complete command (or group) precedes the next operator
	needCmd    bool // an && || | is waiting for its right-hand side
	nextPiped  bool
	skipHeader bool // inside the word list of for/select
	header     []string
	headerTok  token       // the "for" keyword, for Raw
	headerAri  []string    // the arithmetic expansions of the loop's words
	inDbl      bool        // inside [[ ... ]]
	cases      []caseFrame // the open case statements, innermost last
	lists      []listFrame // the and-or lists of the open compound commands, innermost last
	headerCond bool        // the for/select being read runs only conditionally or in a shell of its own
	loops      int         // open while, until, for and select loops
	curLoop    int         // Simple.Loop of the commands being read: the outermost open loop, 0 outside any
	chain      int         // Simple.AndChain of the commands being read (the top-level parser only): 0 after an || of the line's own list
	chains     int         // the chains numbered so far
}

// listFrame is the and-or list a compound command (if, while, until, for, select, case, { }, ( )) belongs to, set aside while
// its body is read, and where the compound's own commands begin in out. At the compound's end the list is the enclosing one
// again: an & after "fi" sends the whole list to the background, the commands before the compound included, and a | after it
// runs the whole compound in a shell of its own.
type listFrame struct {
	start, body int
	andOr       bool
	loop        bool // the compound is a while, until, for or select loop
}

// caseFrame is one open case statement: where its reading is (the word to match, a pattern list, a body) and the nesting of
// parentheses, groups and loops at its "case", which a body must close before its ";;" or "esac".
type caseFrame struct {
	at                    int
	words                 int // words of the pattern list being read
	parens, groups, block int
}

// Where a case statement is being read. The subject and the patterns are words the shell matches, never commands; only
// their substitutions run (and are reported, as every substitution is). The commands of a body run when its pattern
// matches, so they are Cond.
const (
	caseSubject = iota // between "case" and "in"
	casePattern        // reading a pattern list up to ")"
	caseBody           // reading commands up to ";;", ";&", ";;&" or "esac"
)

// parseTokens runs a shell parser over the supplied tokens with shared state and returns collected
// simple commands.
func parseTokens(st *state, src string, toks []token, depth int, top, nested bool) []Simple {
	p := &parser{st: st, src: src, toks: toks, depth: depth, top: top, nested: nested, last: -1}
	p.newChain()
	p.run()
	return p.out
}

// own reports whether the parser is reading the line's own and-or list: the top-level parser, outside any compound command, group,
// subshell or case statement. Only there do the operators begin and end && chains (Simple.AndChain).
func (p *parser) own() bool {
	return p.top && p.parens == 0 && p.groups == 0 && p.blocks == 0 && len(p.cases) == 0
}

// newChain begins the && chain of a new and-or list of the line's own shell, at the line's start and after its ;, & and newlines.
func (p *parser) newChain() {
	if p.own() {
		p.chains++
		p.chain = p.chains
	}
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
	case len(p.cases) != 0:
		p.st.problem("case without esac")
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
		if !t.quoted && t.text == "do" && len(p.header) == 1 {
			// for NAME do ... done: no word list, and none of the ";" or newline a list needs before its do. The do is the
			// keyword, and what follows it is the body, not words of the loop.
			p.endHeader()
			return
		}
		p.header = append(p.header, t.text)
		p.headerAri = append(p.headerAri, t.arith...)
		if t.end > p.headerTok.end {
			p.headerTok.end = t.end
		}
		return
	}
	if c := p.caseTop(); c != nil {
		switch c.at {
		case caseSubject:
			if c.words++; !t.quoted && t.text == "in" && c.words > 1 {
				c.at, c.words = casePattern, 0
			} else if c.words > 1 {
				p.st.problem("case without in")
			}
			return
		case casePattern:
			if !t.quoted && t.text == "esac" && c.words == 0 {
				p.endCase(c)
				return
			}
			c.words++
			return
		}
	}
	if p.inDbl {
		if !t.quoted && t.text == "]]" {
			p.inDbl = false
		}
		p.touch(t)
		p.cur.words = append(p.cur.words, t.text)
		p.cur.arith = append(p.cur.arith, t.arith...)
		return
	}
	if !p.cur.has && !t.quoted && p.reserved(t) {
		return
	}
	p.touch(t)
	p.cur.arith = append(p.cur.arith, t.arith...)
	if t.assign && len(p.cur.words) == 0 {
		p.cur.env = append(p.cur.env, t.text)
		if p.top { // run put t's substitution commands last in pending
			for i := max(len(p.pending)-len(t.nested), 0); i < len(p.pending); i++ {
				p.pending[i].InAssign = len(p.cur.env)
			}
		}
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
	case "if", "while", "until":
		p.open(t.text != "if")
		p.blocks++
		return true
	case "!", "then", "elif", "else", "do":
		return true
	case "fi", "done":
		if p.blocks > 0 {
			p.blocks--
			p.close()
		}
		p.have = true
		return true
	case "esac":
		if c := p.caseTop(); c != nil && c.at == caseBody {
			p.endCase(c)
		} else {
			p.st.problem("esac without case")
		}
		return true
	case "{":
		p.open(false)
		p.groups++
		return true
	case "}":
		if p.groups == 0 {
			p.st.problem("unmatched }")
		} else {
			p.groups--
			p.close()
		}
		p.have = true
		return true
	case "for", "select":
		p.headerCond = p.andOr || p.nextPiped || p.parens > 0 || p.groups > 0 || p.blocks > 0 || len(p.cases) > 0
		p.open(true)
		p.blocks++
		p.skipHeader = true
		p.header = nil
		p.headerTok = t
		return true
	case "case":
		p.open(false)
		p.cases = append(p.cases, caseFrame{at: caseSubject, parens: p.parens, groups: p.groups, block: p.blocks})
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
	if c := p.caseTop(); c != nil {
		switch c.at {
		case caseSubject:
			if t.text != "\n" {
				p.st.problem("case without in")
			}
			return
		case casePattern:
			switch {
			case t.text == "\n" && c.words == 0, t.text == "(" && c.words == 0, t.text == "|" && c.words > 0:
			case t.text == ")" && c.words > 0:
				c.at = caseBody
				p.have = false
			default:
				p.st.problem("unexpected " + t.text + " in a case pattern")
			}
			return
		case caseBody:
			if t.text == ";;" || t.text == ";&" || t.text == ";;&" {
				p.endHeader()
				p.finish()
				if !p.closed(c) {
					p.st.problem("unexpected " + t.text)
				}
				c.at, c.words = casePattern, 0
				p.have, p.needCmd, p.andOr = false, false, false
				p.listStart = len(p.out)
				return
			}
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
		p.andOr = false
		p.listStart = len(p.out)
		p.newChain()
	case ";":
		if !(p.cur.has || p.have || p.skipHeader) {
			p.st.problem("empty command before ;")
		}
		p.endHeader()
		p.finish()
		p.have = false
		p.needCmd = false
		p.andOr = false
		p.listStart = len(p.out)
		p.newChain()
	case "&&", "||", "|", "|&":
		if !(p.cur.has || p.have) {
			p.st.problem("operator " + t.text + " without a command before it")
		}
		p.finish()
		p.have = false
		p.needCmd = true
		p.nextPiped = t.text == "|" || t.text == "|&"
		if p.nextPiped {
			p.markLast() // the writing side of a pipe runs in a shell of its own too
		} else {
			p.andOr = true
		}
		if t.text == "||" && p.own() {
			p.chain = 0 // what follows may run although a link before it did not: the rest of the list is in no chain
		}
	case "&":
		if !(p.cur.has || p.have) {
			p.st.problem("& without a command before it")
		}
		p.finish()
		p.markList() // the whole and-or list before & runs in the background, in a shell of its own (a=1 && b &)
		p.have = false
		p.needCmd = false
		p.andOr = false
		p.listStart = len(p.out)
		p.newChain()
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
		p.open(false)
		p.parens++
		p.needCmd = false
	case ")":
		p.finish()
		if p.parens == 0 {
			p.st.problem("unmatched )")
		} else {
			p.parens--
			p.close()
		}
		p.have = true
	default: // ;; ;& ;;& outside a case body
		p.st.problem(t.text + " outside a case statement")
		p.finish()
		p.have = false
	}
}

// caseTop is the innermost open case statement, or nil.
func (p *parser) caseTop() *caseFrame {
	if len(p.cases) == 0 {
		return nil
	}
	return &p.cases[len(p.cases)-1]
}

// closed reports whether everything opened inside the body of c has been closed.
func (p *parser) closed(c *caseFrame) bool {
	return p.parens == c.parens && p.groups == c.groups && p.blocks == c.block && !p.inDbl && !p.skipHeader && !p.needCmd
}

// endCase closes the innermost case statement at its esac; the statement is then a complete command.
func (p *parser) endCase(c *caseFrame) {
	p.endHeader()
	p.finish()
	if !p.closed(c) {
		p.st.problem("unexpected esac")
	}
	p.cases = p.cases[:len(p.cases)-1]
	p.close()
	p.have = true
}

// open sets the current and-or list aside at the start of a compound command; its body's commands begin a list of their own.
// The compound is the command an && || | was waiting for, so a newline after its first line ends that line ("a && for f in x"
// then "do b; done" on the next: the do is not a word of the loop's list, and b is a command).
//
// loop is set for a while, until, for or select loop: the commands read until it ends are in it (Simple.Loop), and in the loop
// that encloses it, if any, since that one runs them all again.
func (p *parser) open(loop bool) {
	p.lists = append(p.lists, listFrame{start: p.listStart, body: len(p.out), andOr: p.andOr, loop: loop})
	p.listStart = len(p.out)
	p.needCmd = false
	if loop {
		if p.loops == 0 {
			p.st.loops++
			p.curLoop = p.st.loops
		}
		p.loops++
	}
}

// close ends the innermost compound command: the enclosing and-or list is current again, and the compound is the last
// command of it (what a following | runs in a shell of its own).
func (p *parser) close() {
	if len(p.lists) == 0 {
		return
	}
	f := p.lists[len(p.lists)-1]
	p.lists = p.lists[:len(p.lists)-1]
	p.listStart, p.andOr = f.start, f.andOr
	p.last = f.body
	if f.loop {
		if p.loops--; p.loops == 0 {
			p.curLoop = 0
		}
	}
}

func (p *parser) redir(t token) {
	if c := p.caseTop(); c != nil && c.at != caseBody {
		p.st.problem("redirection " + t.text + " in a case pattern")
	}
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
		p.cur.arith = append(p.cur.arith, tg.arith...)
		r.Target = tg.text
		switch {
		case tg.hasBody:
			p.cur.bodies = append(p.cur.bodies, tg.body)
			if tg.quoted {
				p.cur.literal = append(p.cur.literal, Span{Start: tg.bodyStart, End: tg.bodyE})
			}
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
	s := Simple{Env: c.env, Redirects: c.redirs, Nested: p.nested, Piped: c.piped,
		Cond: c.piped || p.andOr || p.parens > 0 || p.groups > 0 || p.blocks > 0 || len(p.cases) > 0, Loop: p.curLoop, Arith: c.arith,
		// "} > out" is the redirection of the group before it, which is the link
		AndChain: p.chain, ChainLink: p.chain != 0 && !c.piped && p.own() && (len(c.words) > 0 || !p.have)}
	p.st.arithOn += len(c.arith)
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
	if len(c.words) == 0 && len(c.bodies) > 0 && p.last >= 0 {
		// "{ bash; } <<EOF" and "while ...; do sh; done <<<cmd": the body feeds the
		// compound command that ended last, and a command in it may run it as code.
		for i := p.last; i < len(p.out); i++ {
			if prog, args := p.out[i].EffectiveCommand(); readsCodeFromStdin(prog, args) {
				p.st.problem("a heredoc or here-string feeds a compound command that may run it as shell code")
				break
			}
		}
	}
	if p.depth == 0 && !p.nested && len(c.literal) > 0 && readsAsData(s) {
		p.st.an.Literal = append(p.st.an.Literal, c.literal...)
	}
	p.out = append(p.out, s)
	if len(c.words) > 0 || !p.have { // "done > out" is the compound's redirection: the compound stays the last command
		p.last = len(p.out) - 1
	}
	p.have = true
	p.flush()
}

// markLast marks the last command of the list as Cond (it ran in a shell of its own: a pipe's writing side): the command finish
// emitted last, or every command of the compound command that ended last. It stays in its && chain, but is no ChainLink.
func (p *parser) markLast() {
	for i := max(p.last, 0); p.last >= 0 && i < len(p.out); i++ {
		// Pipes after nested compounds visit the same commands again; each visit
		// is charged to the input's work budget.
		if !p.st.tick() {
			return
		}
		p.out[i].Cond = true
		p.out[i].ChainLink = false
	}
}

// markList marks every command of the current and-or list as Cond and no ChainLink: the list runs in the background. A list of the
// line's own shell leaves its && chain; one inside a link (a && { b & } && c) stays in the chain of that link, which starts it.
func (p *parser) markList() {
	own := p.own()
	for i := p.listStart; i < len(p.out); i++ {
		p.out[i].Cond = true
		p.out[i].ChainLink = false
		if own {
			p.out[i].AndChain = 0
		}
	}
}

// flush moves pending simple commands into output and clears the pending queue. Inside a loop, the commands of a substitution are
// in it as well, whatever loops of their own they have; and they are in the && chain of the link being read (the top-level parser
// only), since they run as part of it.
func (p *parser) flush() {
	if p.curLoop != 0 {
		for i := range p.pending {
			p.pending[i].Loop = p.curLoop
		}
	}
	if p.top {
		for i := range p.pending {
			p.pending[i].AndChain, p.pending[i].ChainLink = p.chain, false
		}
	}
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
	inner := parseTokens(p.st, script, toks, p.depth+1, false, true)
	switch base := stdBase(prog); {
	case base == "trap":
		mark(inner, func(c *Simple) { c.Deferred = true })
	case base == "mapfile" || base == "readarray":
		mark(inner, func(c *Simple) { c.Cond = true }) // in this shell, as often as the input has quanta of lines: maybe never
	case base != "eval":
		mark(inner, func(c *Simple) { c.Child = true }) // sh -c, su -c: a new shell
	}
	p.pending = append(p.pending, inner...)
}

// mark applies f to every command of cmds.
func mark(cmds []Simple, f func(*Simple)) {
	for i := range cmds {
		f(&cmds[i])
	}
}

// stdinScripts parses heredocs and here-strings that feed a shell reading its
// program from stdin (bash <<EOF, sh <<< "cmd"): the text is executed.
func (p *parser) stdinScripts(s *Simple, bodies []string) {
	if len(bodies) == 0 {
		return
	}
	prog, args := s.EffectiveCommand()
	base := stdBase(prog)
	source := base == "source" || base == "."
	if !(posixShells[base] || source) || !readsCodeFromStdin(prog, args) {
		return
	}
	for _, b := range bodies {
		if p.depth >= maxDepth {
			p.st.problem("nesting too deep")
			return
		}
		l := newLexer(b, p.st, p.depth+1)
		toks, _ := l.lexUntil(false)
		inner := parseTokens(p.st, b, toks, p.depth+1, false, true)
		if !source { // source runs it in this shell
			mark(inner, func(c *Simple) { c.Child = true })
		}
		p.pending = append(p.pending, inner...)
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
	words, arith := p.header, p.headerAri
	p.header, p.headerAri = nil, nil
	loopVar, in := "", false
	for i, w := range words {
		if w == "in" && i >= 1 {
			if i == 1 && isName(words[0]) {
				loopVar = words[0]
			}
			words, in = words[i+1:], true
			break
		}
	}
	if !in {
		// for NAME; do / for NAME do: the loop walks the positional parameters, which are only known when the line runs. It is
		// reported with no words, and its variable, which it sets (set -- / before it makes NAME "/").
		if len(words) == 1 && isName(words[0]) {
			loopVar = words[0]
		} else {
			p.st.problem("for without in")
		}
		words = nil
	}
	if len(words) == 0 && loopVar == "" {
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
		Program: "for", Args: append([]string(nil), words...), Raw: raw, Nested: p.nested, LoopVar: loopVar, Cond: p.headerCond, Loop: p.curLoop, Arith: arith,
		AndChain: p.chain,
	})
	p.st.arithOn += len(arith)
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
