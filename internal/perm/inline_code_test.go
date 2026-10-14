package perm

import (
	"context"
	"testing"
)

// A program an interpreter is given on its command line is code, not a file name. In a trial, a docs worker's check
// node -e "import('./web/js/format.mjs').then(m=>{ ... '\$1,234.56' ... })" was read as a path ("m=>{" split at "=", "$1" a
// variable), which made a quoted script look like a write to an unknown place under the project and hit the config directory's
// ask rule, so a bypass session stopped for the person. The interpreter's other operands are still judged as files.
func TestAnInterpretersInlineProgramIsNotAPath(t *testing.T) {
	f := newFixture(t)
	e := f.engine(t, Config{Mode: ModeBypass, Ask: []string{"Edit(./.sleipnir/**)", "Edit(~/.sleipnir/**)"}})
	trial := "node -e \"\nimport('./web/js/format.mjs').then(m=>{\nconsole.log(m.centsToUsd(-123456), m.parseUsdToCents('\\$1,234.56'), m.shiftMonth('2025-01',-1));\n});\n\" 2>&1; node --test tests/*.mjs 2>&1 | tail -3; sed -n 25,58p web/js/api.mjs"
	for _, c := range []struct{ cmd, want string }{
		{trial, "allow"},
		{`python3 -c "import sys; x = {'a': '$1'}; print(x)"`, "allow"},
		{`ruby -e 'puts "a=>b #{$0}"'`, "allow"},
		{`node --eval="x=>{y}"`, "allow"},
		{`perl -e 'print $1 if "a=b" =~ /(=)/'`, "allow"},
		// what is not the program is still a file
		{`node -e "console.log(1)" > .sleipnir/out.txt`, "ask"},
		{`python3 -c "import sys" .sleipnir/config.json`, "ask"},
		{`node -e "x" -- .sleipnir/config.json`, "ask"},
	} {
		d := e.Check(context.Background(), Request{Agent: "dc-1", Tool: "Bash", Command: f.expand(c.cmd)})
		if got := outcome(d); got != c.want {
			t.Errorf("%s\n  = %s (%s), want %s", c.cmd, got, d.Reason, c.want)
		}
	}
}
