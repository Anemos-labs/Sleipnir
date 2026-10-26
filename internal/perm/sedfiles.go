package perm

import "strings"

// sedUses names the files a sed that is more than line selection (sed -i 's/a, 5,/b, 6,/' x.go) reads and writes: its file
// operands, a script file, and the files its script names (r, R, w and W, and the w flag of s). The words of the script are no
// files: as an unlisted command's operands they were split at commas and colons, and "s/a, 5,/b, 6,/" named the root (its
// last piece), which the credentials protection refuses whatever the mode. ok is false when the arguments or the script are
// not understood, or the script runs a program (e, the e flag of s); the caller then keeps an unlisted command's view.
func sedUses(args []string) (uses []pathUse, ok bool) {
	var scripts, files []string
	haveScript := false
	for i := 0; i < len(args); i++ {
		a := args[i]
		switch {
		case a == "--":
			files = append(files, args[i+1:]...)
			i = len(args)
		case sedSwitches[a], a == "--debug", a == "--follow-symlinks", a == "-c", a == "--copy":
		case a == "--in-place" || strings.HasPrefix(a, "--in-place="):
		case a == "-e" || a == "--expression":
			if i+1 >= len(args) {
				return nil, false
			}
			scripts, haveScript = append(scripts, args[i+1]), true
			i++
		case strings.HasPrefix(a, "--expression="):
			scripts, haveScript = append(scripts, strings.TrimPrefix(a, "--expression=")), true
		case a == "-f" || a == "--file" || strings.HasPrefix(a, "--file="):
			return nil, false // the script is in a file: what it names cannot be seen
		case a == "-l" || a == "--line-length":
			i++
		case strings.HasPrefix(a, "--"):
			return nil, false
		case len(a) > 1 && a[0] == '-':
			for j := 1; j < len(a); j++ {
				switch c := a[j]; {
				case c == 'i':
					if j == len(a)-1 && i+1 < len(args) && args[i+1] == "" {
						i++ // -i '': BSD sed's empty backup suffix; the script follows
					}
					j = len(a) // -i, -i.bak: the rest is the backup suffix
				case c == 'e':
					script := a[j+1:]
					if script == "" {
						if i+1 >= len(args) {
							return nil, false
						}
						script = args[i+1]
						i++
					}
					scripts, haveScript = append(scripts, script), true
					j = len(a)
				case c == 'f':
					return nil, false
				case c == 'l':
					if j == len(a)-1 {
						i++ // -l N
					}
					j = len(a)
				case !sedSwitches["-"+string(c)]:
					return nil, false
				}
			}
		default:
			files = append(files, a)
		}
	}
	if !haveScript {
		if len(files) == 0 {
			return nil, false
		}
		scripts, files = []string{files[0]}, files[1:]
	}
	for _, s := range scripts {
		reads, writes, ok := sedScriptFiles(s)
		if !ok {
			return nil, false
		}
		for _, f := range reads {
			uses = append(uses, pathUse{raw: f})
		}
		for _, f := range writes {
			uses = append(uses, pathUse{raw: f, write: true})
		}
	}
	for _, f := range files {
		if f != "-" {
			uses = append(uses, pathUse{raw: f, both: true})
		}
	}
	return uses, true
}

// sedScriptFiles parses a whole sed script and returns the files its commands read (r, R) and write (w, W, the w flag of s).
// ok is false for a script it does not understand and for one that runs a program.
func sedScriptFiles(s string) (reads, writes []string, ok bool) {
	p := &sedParser{s: s}
	toEOL := func() string {
		start := p.i
		for !p.done() && p.s[p.i] != '\n' {
			p.i++
		}
		return strings.TrimSpace(s[start:p.i])
	}
	for {
		p.skip(" \t\n;}")
		if p.done() {
			return reads, writes, true
		}
		if p.s[p.i] == '#' {
			toEOL()
			continue
		}
		if p.s[p.i] == ':' {
			p.i++
			if !p.label() {
				return nil, nil, false
			}
			continue
		}
		had, why := p.address()
		if why != "" {
			return nil, nil, false
		}
		if !p.done() && p.s[p.i] == ',' {
			p.i++
			p.skip(" \t")
			if !p.done() && (p.s[p.i] == '+' || p.s[p.i] == '~') {
				p.i++
				if !p.number() {
					return nil, nil, false
				}
			} else if had, why := p.address(); why != "" || !had {
				return nil, nil, false
			}
		}
		_ = had
		p.skip(" \t")
		for !p.done() && p.s[p.i] == '!' {
			p.i++
			p.skip(" \t")
		}
		if p.done() {
			return nil, nil, false
		}
		c := p.s[p.i]
		p.i++
		switch c {
		case '{':
		case 'p', 'P', 'd', 'D', '=', 'l', 'n', 'N', 'g', 'G', 'h', 'H', 'x', 'z', 'F':
		case 'q', 'Q', 'L':
			p.skip(" \t")
			p.number()
		case 'b', 't', 'T':
			if !p.label() {
				return nil, nil, false
			}
		case 'a', 'i', 'c':
			toEOL() // text
		case 'r', 'R', 'w', 'W':
			f := toEOL()
			if f == "" {
				return nil, nil, false
			}
			if c == 'r' || c == 'R' {
				reads = append(reads, f)
			} else {
				writes = append(writes, f)
			}
		case 's', 'y':
			if p.done() || p.s[p.i] == '\\' || p.s[p.i] == '\n' {
				return nil, nil, false
			}
			delim := p.s[p.i]
			p.i++
			for part := 0; part < 2; part++ {
				closed := false
				for !p.done() && !closed {
					switch p.s[p.i] {
					case '\\':
						p.i += 2
					case delim:
						p.i++
						closed = true
					default:
						p.i++
					}
				}
				if !closed {
					return nil, nil, false
				}
			}
			if c == 'y' {
				break
			}
			for !p.done() && p.s[p.i] != ';' && p.s[p.i] != '\n' && p.s[p.i] != '}' {
				switch f := p.s[p.i]; {
				case f == 'e':
					return nil, nil, false // runs the pattern space as a command
				case f == 'w':
					p.i++
					name := toEOL()
					if name == "" {
						return nil, nil, false
					}
					writes = append(writes, name)
				case f == 'g' || f == 'p' || f == 'i' || f == 'I' || f == 'm' || f == 'M' || f >= '0' && f <= '9' || f == ' ' || f == '\t':
					p.i++
				default:
					return nil, nil, false
				}
			}
		default:
			return nil, nil, false // e runs a program; anything else is not understood
		}
	}
}

// label skips the label of :, b, t or T, which ends at a semicolon, a newline or a closing brace. A label with a blank inside
// it is refused: sed versions disagree on where it ends, and a command read as part of it (:a;w out) would go unchecked.
func (p *sedParser) label() bool {
	p.skip(" \t")
	for !p.done() && p.s[p.i] != ';' && p.s[p.i] != '\n' && p.s[p.i] != '}' {
		if p.s[p.i] == ' ' || p.s[p.i] == '\t' {
			p.skip(" \t")
			return p.done() || p.s[p.i] == ';' || p.s[p.i] == '\n' || p.s[p.i] == '}'
		}
		p.i++
	}
	return true
}
