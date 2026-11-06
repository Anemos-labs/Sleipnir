package shell

import (
	"strings"
	"testing"
)

// A command that signals processes by a pattern the harness's own command line also matches would end the harness and every
// agent with it: it is not run. One whose pattern cannot match the harness, or that signals nothing, runs as before.
func TestACommandThatWouldEndTheHarnessIsNamed(t *testing.T) {
	nest := `$($($($($($($($($($($($($(pkill -f kvlite)))))))))))))`
	const pid, line, name = 4242, "/usr/local/bin/sleipnir web --cwd /home/u/kvlite --mode bypass", "sleipnir"
	for _, tc := range []struct {
		cmd  string
		want string // part of what endsHarness names; "" for nothing
	}{
		{`ps -eo pid,args | grep '[k]vlite' | awk '{print $1}' | xargs -r kill -9`, `"[k]vlite" of grep`},
		{`kill $(pgrep -f kvlite)`, `"kvlite" of pgrep`},
		{`pkill -f "/home/u/kvlite"`, `of pkill`},
		{`pkill -9 -f -i KVLITE`, `of pkill`},
		{`pkill sleipnir`, `of pkill`},
		{`killall sleipnir`, `of killall`},
		{`kill -9 4242`, `pid 4242`},
		{`ps aux | grep -F 'kvlite' | awk '{print $2}' | xargs kill`, `of grep`},
		{`pkill -f "go run ./cmd/kvlite"`, ""},                         // the pattern is not in the harness's line
		{`pkill kvlite`, ""},                                           // without -f, only the process name is matched
		{`ps aux | grep kvlite`, ""},                                   // nothing is signalled
		{`ps aux | grep -v kvlite | grep '[s]erver' | xargs kill`, ""}, // an inverted grep picks the others; [s]erver is not in the line
		{`kill 4243; grep -r kvlite .`, ""},                            // the grep does not pick what is signalled
		{`kill $(ps -eo pid,args | grep kvlite | cut -d" " -f1)`, `of grep`},
		{`echo "$(pgrep -f kvlite)"; kill 4243`, ""}, // the substitution does not feed the kill
		{`sudo pkill -f kvlite`, `of pkill`},
		{`echo x | xargs -r pkill -f kvlite`, `of pkill`},
		{`echo x | xargs -r pkill -f "go run"`, ""},
		{`pkill -f kvlite -u root`, `"kvlite" of pkill`}, // -u takes a value, which is not the pattern
		{`pkill -fi KVLITE`, `of pkill`},
		{`pkill --full --ignore-case KVLITE`, `of pkill`},
		{`pkill -x -f kvlite`, ""}, // -x matches the whole line only
		{`pkill -SIGTERM -f kvlite`, `of pkill`},
		{`ps -eo pid,args | grep -ekvlite | xargs kill`, `"kvlite" of grep`},
		{`ps -eo pid,args | grep --regexp=kvlite | xargs kill`, `"kvlite" of grep`},
		{`ps -eo pid,args | grep -m 1 kvlite | xargs kill`, `"kvlite" of grep`}, // -m takes a value
		{`ps -eo pid,args | grep -x kvlite | xargs kill`, ""},                   // -x matches whole lines
		{`ps -eo pid,args | grep -w kvl | xargs kill`, ""},                      // -w matches whole words
		{`ps -eo pid,args | grep 'kv\|nothing' | xargs kill`, `of grep`},        // a basic regex's \| is an alternation
		{`ps -eo pid,args | grep 'kvlite|nothing' | xargs kill`, ""},            // and its | is literal
		{`ps -eo pid,args | grep '^4242 ' | xargs kill`, `of grep`},             // ps puts the pid first
		{`ps -eo pid,args | grep -f pats.txt | xargs kill`, `from a file`},
		{`ps -eo pid,args | grep -v -f pats.txt | xargs kill`, `from a file`},
		{`ps -eo pid,args | grep '\(kv\).*\1' | xargs kill`, `cannot be checked`}, // a back-reference Go does not read
		{"echo " + nest, "too complex"},                                           // nested deeper than the parser reads
		{`kill %1`, ""},
		{`pgrep -f kvlite; kill 4243`, ""}, // the pgrep only lists
	} {
		got := endsHarness(tc.cmd, pid, line, name)
		if tc.want == "" && got != "" || tc.want != "" && !strings.Contains(got, tc.want) {
			t.Errorf("%s: %q, want %q", tc.cmd, got, tc.want)
		}
	}
}
