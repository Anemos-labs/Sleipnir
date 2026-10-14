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
		// the files curl touches are judged
		{`curl -s localhost:8099/ -o .sleipnir/page.html`, "ask"},
		{`curl -s -d @secrets/token localhost:8099/api`, "deny"},
		{`curl -s -F "f=@secrets/token" localhost:8099/api`, "deny"},
		{`curl -s -c .sleipnir/jar localhost:8099/`, "ask"},
		{`curl -s --some-new-option .sleipnir/x localhost:8099/`, "ask"}, // an unknown option: every word may be a file, as before
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
