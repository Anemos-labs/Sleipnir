package runner

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"math"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/anemos-labs/sleipnir/internal/web/clispec"
	"github.com/anemos-labs/sleipnir/internal/web/wire"
)

// Plan is a run request turned into a command: its argument vector, its mode with the flags applied, whether it gets the held keys,
// and where it runs.
type Plan struct {
	Path    []string
	Args    []string
	Cmdline string
	Mode    string
	Net     bool
	Dir     string
	Command *wire.CLICommand
}

// maxValue bounds one value of a flag or a positional, in bytes.
const maxValue = 4096

// forcedYes are the commands that would ask on a terminal: the confirmation in the page is the person's yes, so --yes is passed.
var forcedYes = map[string]bool{"trust add": true, "mcp approve": true}

// textPositionals take the remaining words as one text (a goal): the others that take several values split them at white space.
var textPositionals = map[string]bool{"PROMPT": true, "GOAL": true}

// errBadFlags is the 400 of a request whose flags or arguments the command does not take.
func errBadFlags(format string, args ...any) error {
	return &wire.Error{Status: http.StatusBadRequest, Code: "bad_flags", Msg: fmt.Sprintf(format, args...)}
}

// Plan checks a run request against the spec and builds the command: an unknown command is 404 unknown_command, a flag or an
// argument it does not take 400 bad_flags, a command that needs a terminal 403 tty_only (with the page's equivalent), a scheduled
// bypass or yolo run 403 dangerous_mode, an unknown tab 404 no_session.
func (r *Runs) Plan(_ context.Context, path []string, pos map[string]string, flags map[string]any, tab string) (*Plan, error) {
	if len(path) == 0 || len(path) > 4 {
		return nil, errBadFlags("a command is required")
	}
	if strings.Join(path, " ") == "web" {
		// web is in the spec as a refusal; it is checked by name too so that no spec can make the page start another server
		return nil, &wire.Error{Status: http.StatusForbidden, Code: "tty_only", Msg: "you are in it: sleipnir web serves this page"}
	}
	c, ok := clispec.Lookup(path)
	if !ok {
		return nil, &wire.Error{Status: http.StatusNotFound, Code: "unknown_command", Msg: "sleipnir has no command " + quoteShort(strings.Join(path, " "))}
	}
	key := strings.Join(c.Path, " ")
	args := append([]string{}, c.Path...)
	known := map[string]*wire.CLIFlag{}
	for i := range c.Flags {
		known[c.Flags[i].Name] = &c.Flags[i]
	}
	for name := range flags {
		if known[name] == nil {
			return nil, errBadFlags("sleipnir %s has no flag --%s", key, quoteShort(name))
		}
	}
	set := map[string][]string{} // the values given, by flag, as the command line carries them
	var flagArgs []string
	for _, f := range c.Flags { // in the spec's order: the same request is the same vector
		v, ok := flags[f.Name]
		if !ok {
			continue
		}
		vals, err := flagValues(&f, v)
		if err != nil {
			return nil, err
		}
		if len(vals) == 0 {
			continue
		}
		set[f.Name] = vals
		if f.Arg == "bool" {
			if vals[0] == "true" {
				flagArgs = append(flagArgs, "--"+f.Name)
			} else {
				flagArgs = append(flagArgs, "--"+f.Name+"=false")
			}
			continue
		}
		for _, x := range vals {
			flagArgs = append(flagArgs, "--"+f.Name+"="+x)
		}
	}
	if forcedYes[key] && set["yes"] == nil {
		flagArgs = append(flagArgs, "--yes")
		set["yes"] = []string{"true"}
	}
	// the mode, with the flags that change it
	mode := c.Mode
	for _, rule := range c.When {
		vals := set[rule.Flag]
		if len(vals) == 0 || vals[0] == "false" {
			continue
		}
		if len(rule.Values) > 0 && !slices.Contains(rule.Values, vals[0]) {
			continue
		}
		mode = rule.Mode
	}
	if mode == "tty_only" {
		if c.Mode != "tty_only" && key == "schedule add" {
			return nil, &wire.Error{Status: http.StatusForbidden, Code: "dangerous_mode", Msg: "bypass and yolo cannot be scheduled from here"}
		}
		return nil, &wire.Error{Status: http.StatusForbidden, Code: "tty_only", Msg: c.Why}
	}
	// positionals
	for name := range pos {
		found := false
		for _, p := range c.Positional {
			found = found || p.Name == name
		}
		if !found {
			return nil, errBadFlags("sleipnir %s takes no argument %s", key, quoteShort(name))
		}
	}
	var lead, tail []string
	for i, p := range c.Positional {
		v := strings.TrimSpace(pos[p.Name])
		if v == "" {
			if p.Required {
				return nil, errBadFlags("%s is required", p.Name)
			}
			continue
		}
		if err := checkValue(p.Name, v); err != nil {
			return nil, err
		}
		var words []string
		switch {
		case p.Variadic && !textPositionals[p.Name]:
			words = strings.Fields(v)
		default:
			words = []string{v}
		}
		for _, w := range words {
			if strings.HasPrefix(w, "-") {
				return nil, errBadFlags("%s cannot start with -", p.Name)
			}
		}
		if key == "swarm" && i == 0 { // swarm N ...: the number of workers comes first
			if n, err := strconv.Atoi(v); err != nil || n < 1 {
				return nil, errBadFlags("N is the number of workers: 1 or more")
			}
			lead = append(lead, words...)
			continue
		}
		tail = append(tail, words...)
	}
	args = append(append(append(args, lead...), flagArgs...), tail...)
	dir := r.o.Cwd
	if tab != "" {
		if r.host == nil {
			return nil, &wire.Error{Status: http.StatusNotFound, Code: "no_session", Msg: "no such session"}
		}
		t, ok := r.host.Tab(tab)
		if !ok {
			return nil, &wire.Error{Status: http.StatusNotFound, Code: "no_session", Msg: "no such session"}
		}
		if cwd := t.Summary().Cwd; cwd != "" {
			dir = cwd
		}
	}
	return &Plan{Path: c.Path, Args: args, Cmdline: Cmdline(args), Mode: mode, Net: c.Mode == "net", Dir: dir, Command: c}, nil
}

// flagValues turns the value the page sent for a flag into the values of the command line: none (the flag is left out), one, or
// several for a repeatable flag. A bool is "true" or "false" ("false" only when the flag's default is true: --mailman=false).
func flagValues(f *wire.CLIFlag, v any) ([]string, error) {
	bad := func(want string) error { return errBadFlags("--%s takes %s", f.Name, want) }
	if v == nil {
		return nil, nil
	}
	if f.Arg == "bool" {
		on := false
		switch x := v.(type) {
		case bool:
			on = x
		case string:
			switch strings.TrimSpace(x) {
			case "", "false":
			case "true":
				on = true
			default:
				return nil, bad("on or off")
			}
		default:
			return nil, bad("on or off")
		}
		if on {
			return []string{"true"}, nil
		}
		if d, ok := f.Default.(bool); ok && d {
			return []string{"false"}, nil
		}
		return nil, nil
	}
	var raw []string
	switch x := v.(type) {
	case string:
		if f.Repeatable {
			raw = strings.Split(x, ",")
		} else {
			raw = []string{x}
		}
	case float64:
		if math.IsNaN(x) || math.IsInf(x, 0) {
			return nil, bad("a number")
		}
		raw = []string{strconv.FormatFloat(x, 'f', -1, 64)}
	case bool:
		return nil, bad("a value")
	case []any:
		if !f.Repeatable {
			return nil, bad("one value")
		}
		for _, e := range x {
			s, ok := e.(string)
			if !ok {
				return nil, bad("text values")
			}
			raw = append(raw, s)
		}
	default:
		return nil, bad("a value")
	}
	var out []string
	for _, s := range raw {
		s = strings.TrimSpace(s)
		if s == "" {
			continue
		}
		if err := checkValue("--"+f.Name, s); err != nil {
			return nil, err
		}
		switch f.Arg {
		case "int":
			if _, err := strconv.ParseInt(s, 10, 64); err != nil {
				return nil, bad("a whole number")
			}
		case "uint":
			if _, err := strconv.ParseUint(s, 10, 64); err != nil {
				return nil, bad("a whole number of 0 or more")
			}
		case "float":
			if n, err := strconv.ParseFloat(s, 64); err != nil || math.IsNaN(n) || math.IsInf(n, 0) {
				return nil, bad("a number")
			}
		case "duration":
			if _, err := time.ParseDuration(s); err != nil {
				return nil, bad("a duration (30s, 5m, 1h)")
			}
		}
		if !f.Repeatable && f.Default != nil && s == fmt.Sprint(f.Default) {
			continue // the default: the command line leaves it out, as the page's does
		}
		out = append(out, s)
	}
	if len(out) > 1 && !f.Repeatable {
		return nil, bad("one value")
	}
	return out, nil
}

// checkValue refuses a value no command line should carry: too long, a NUL, invalid UTF-8.
func checkValue(name, v string) error {
	switch {
	case len(v) > maxValue:
		return errBadFlags("%s is longer than %d bytes", name, maxValue)
	case strings.ContainsRune(v, 0):
		return errBadFlags("%s holds a NUL", name)
	case !utf8.ValidString(v):
		return errBadFlags("%s is not valid UTF-8", name)
	}
	return nil
}

// quoteShort quotes s for a message, cut at 64 bytes.
func quoteShort(s string) string {
	if len(s) > 64 {
		s = strings.ToValidUTF8(s[:64], "") + "..."
	}
	return strconv.Quote(s)
}

// Cmdline is an argument vector as a person would type it after "sleipnir": words with spaces or quotes in JSON quotes.
func Cmdline(args []string) string {
	parts := []string{"sleipnir"}
	for _, a := range args {
		if a == "" || strings.ContainsAny(a, " \t\n\"'\\$`") {
			a = JSONString(a)
		}
		parts = append(parts, a)
	}
	return strings.Join(parts, " ")
}

// D16 is the first 16 hex digits of the SHA-256 of a list of strings as JSON, written as JavaScript's JSON.stringify writes it
// (the confirmation scopes of CONTRACT.md 20: a page computes the same digest).
func D16(list []string) string {
	var b strings.Builder
	b.WriteByte('[')
	for i, s := range list {
		if i > 0 {
			b.WriteByte(',')
		}
		b.WriteString(JSONString(s))
	}
	b.WriteByte(']')
	sum := sha256.Sum256([]byte(b.String()))
	return hex.EncodeToString(sum[:])[:16]
}

// JSONString quotes s as JSON.stringify does: a backslash before " and \, the short escapes of \b \f \n \r \t, \u00XX (lower
// case) for the other control characters, and every other character as it is.
func JSONString(s string) string {
	var b strings.Builder
	b.WriteByte('"')
	for _, r := range s {
		switch r {
		case '"':
			b.WriteString(`\"`)
		case '\\':
			b.WriteString(`\\`)
		case '\b':
			b.WriteString(`\b`)
		case '\f':
			b.WriteString(`\f`)
		case '\n':
			b.WriteString(`\n`)
		case '\r':
			b.WriteString(`\r`)
		case '\t':
			b.WriteString(`\t`)
		default:
			if r < 0x20 {
				fmt.Fprintf(&b, `\u%04x`, r)
			} else {
				b.WriteRune(r)
			}
		}
	}
	b.WriteByte('"')
	return b.String()
}

// joinPath is a command path as one string.
func joinPath(p []string) string { return strings.Join(p, " ") }
