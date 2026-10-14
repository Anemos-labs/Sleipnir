package perm

import (
	"context"
	"testing"
)

// curl's words are URLs, data and headers; the files it touches are named by a few options. Judged as an unlisted command, a smoke
// test's JSON body holding a variable was a write to an unknown place, and a team demo's bypass session stopped for the person on
// curl -X POST localhost:8099/api/transactions -d "{...\"category_id\":$CID}". A file curl writes or reads is still judged.
func TestCurlsDataIsNotAFile(t *testing.T) {
	f := newFixture(t)
	e := f.engine(t, Config{Mode: ModeBypass, Ask: []string{"Edit(./.sleipnir/**)", "Edit(~/.sleipnir/**)"}, Deny: []string{"Read(./secrets/**)"}})
	for _, c := range []struct{ cmd, want string }{
		{`CID=$(curl -s -X POST localhost:8099/api/categories -d '{"name":"Food"}' | python3 -c 'import sys,json;print(json.load(sys.stdin)["id"])'); curl -s -X POST localhost:8099/api/transactions -H 'Content-Type: application/json' -d "{\"date\":\"2025-03-01\",\"category_id\":$CID}" >/dev/null`, "allow"},
		{`curl -s -X POST localhost:8099/api/budgets -d "{\"month\":\"2025-03\",\"category_id\":$CID}"`, "allow"},
		{`curl -fsS "http://127.0.0.1:8099/api/summary?month=$M" -w '%{http_code}'`, "allow"},
		{`curl -sS -o /dev/null -w '%{http_code}' -X DELETE "localhost:8099/api/categories/$CID"`, "allow"},
		{`curl -s -F "file=@tests/fixtures/ok.csv" localhost:8099/api/import`, "allow"},
		{`curl -s localhost:8099/api/export.csv -o export.csv`, "allow"},
		{`B=http://127.0.0.1:8000; F="$TMPDIR/import.csv"; printf 'date\n' > "$F"; curl -s -F file=@"$F" -w '\n-> %{http_code}\n' $B/api/import; curl -s -D - -o /dev/null "$B/api/export.csv?month=2025-05"`, "allow"},
		{`for p in / /js/app.js; do curl -s -o /dev/null -w "%{http_code} /$p\n" "localhost:8099$p"; done`, "allow"},
		// the files curl touches are judged
		{`curl -s localhost:8099/ -o .sleipnir/page.html`, "ask"},
		{`curl -s -d @secrets/token localhost:8099/api`, "deny"},
		{`curl -s -F "f=@secrets/token" localhost:8099/api`, "deny"},
		{`curl -s -c .sleipnir/jar localhost:8099/`, "ask"},
		{`curl -s --some-new-option .sleipnir/x localhost:8099/`, "ask"}, // an unknown option: every word may be a file, as before
		{`curl -s -H 'X-Token: abc' -H @secrets/headers localhost:8099/`, "deny"},
		{`curl -s -F "f=x;type=text/plain;headers=@secrets/headers" localhost:8099/api`, "deny"},
		{`curl -s file://{root}/secrets/key.txt`, "deny"},
		{`curl -s --url FILE://localhost{root}/secrets/key.txt -o /dev/null`, "deny"},
		{`curl -s file://{home}/.ssh/id_rsa`, "deny"},
		{`D={root}/secrets; curl -s "file://$D/key.txt"`, "deny"}, // a variable the line set is put in
		{`curl -s -T main.go file://{root}/.sleipnir/x`, "ask"},   // an upload to a file: URL writes it
		{`curl -s -O localhost:8099/app.js`, "allow"},
		{`curl -s -O "localhost:8099/x/.env?v=1"`, "deny"}, // -O writes ./.env, the name the URL ends in
		{`curl -s --output-dir .sleipnir -O localhost:8099/settings.json`, "ask"},
		{`curl -s --output-dir .sleipnir -o /page.html localhost:8099/`, "ask"}, // below --output-dir, even with a leading /
		// what curl alone cannot name keeps the analysis of an unlisted command, in which a word holding $CID may be any file
		{`curl -s -K curl.cfg -d "{\"id\":$CID}" localhost:8099/api`, "not allow"},  // a config file holds options of its own
		{`curl -s -J -O -d "{\"id\":$CID}" localhost:8099/api/export`, "not allow"}, // the server names the file
		{`curl -s "$U" -d "{\"id\":$CID}"`, "not allow"},                            // the URL's scheme is only known when it runs
		{`curl -s "file://$P/key.txt" -d "{\"id\":$CID}"`, "not allow"},
		{`curl -s "localhost:8099/{a,b}.js" -o "out_#1.js" -d "{\"id\":$CID}"`, "not allow"},
		{`B=localhost:8099; printf -v B '%s' file://{root}/secrets/key.txt; curl -s "$B" -d "{\"id\":$CID}"`, "not allow"}, // printf -v sets B
	} {
		d := e.Check(context.Background(), Request{Agent: "fe-1", Tool: "Bash", Command: f.expand(c.cmd)})
		got := outcome(d)
		if c.want == "not allow" && got != "allow" {
			continue
		}
		if got != c.want {
			t.Errorf("%s\n  = %s (%s), want %s", c.cmd, got, d.Reason, c.want)
		}
	}
}
