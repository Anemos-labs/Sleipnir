package main

import (
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
)

// webAPIRow matches a row of a route table in docs/WEB-API.md: a method and a path in the first two cells, each optionally in
// backticks, e.g. | `GET` | `/api/hello` | ... |.
var webAPIRow = regexp.MustCompile("^\\|\\s*`?(GET|POST|PUT|PATCH|DELETE)`?\\s*\\|\\s*`?(/[^`\\s|]*)`?\\s*\\|")

// documentedRoutes reads the route tables of docs/WEB-API.md as "METHOD /path" patterns, each with the number of rows that list it.
func documentedRoutes(t *testing.T) map[string]int {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("..", "..", "docs", "WEB-API.md"))
	if err != nil {
		t.Fatal(err)
	}
	rows := map[string]int{}
	for _, line := range strings.Split(string(b), "\n") {
		if m := webAPIRow.FindStringSubmatch(line); m != nil {
			rows[m[1]+" "+m[2]]++
			if cells := strings.Count(line, "|") - strings.Count(line, "\\|"); cells < 5 {
				t.Errorf("the row %q has fewer than the four columns of a route table", line)
			}
		}
	}
	return rows
}

// The route tables of docs/WEB-API.md list exactly the routes the host registers: a route added or removed in code must be added to
// or removed from the document, and a route must not be listed twice.
func TestWebAPIDocumentListsEveryRegisteredRoute(t *testing.T) {
	project := webWorld(t)
	_, base := newWebModel(t, sayScript("ok"))
	r := newWebRig(t, soloDefaults(project), base)

	registered := map[string]bool{}
	for _, p := range r.srv.Routes() {
		registered[p] = true
	}
	documented := documentedRoutes(t)
	if len(registered) < 50 || len(documented) < 50 {
		t.Fatalf("%d routes registered and %d documented: the host or the document is not being read", len(registered), len(documented))
	}

	var missing, stale, repeated []string
	for p := range registered {
		if documented[p] == 0 {
			missing = append(missing, p)
		}
	}
	for p, n := range documented {
		if !registered[p] {
			stale = append(stale, p)
		}
		if n > 1 {
			repeated = append(repeated, p)
		}
	}
	for _, list := range [][]string{missing, stale, repeated} {
		sort.Strings(list)
	}
	if len(missing) > 0 {
		t.Errorf("registered but not in docs/WEB-API.md:\n  %s", strings.Join(missing, "\n  "))
	}
	if len(stale) > 0 {
		t.Errorf("in docs/WEB-API.md but not registered:\n  %s", strings.Join(stale, "\n  "))
	}
	if len(repeated) > 0 {
		t.Errorf("listed more than once in docs/WEB-API.md:\n  %s", strings.Join(repeated, "\n  "))
	}
}

// The parser reads the forms a route table may take and ignores every other row.
func TestWebAPIDocumentRowParser(t *testing.T) {
	for _, tc := range []struct {
		line string
		want string
	}{
		{"| `GET` | `/api/hello` | text | - |", "GET /api/hello"},
		{"| POST | /api/sessions/{id}/stop | text | - |", "POST /api/sessions/{id}/stop"},
		{"| `DELETE` | `/api/runs/{run}` | text | - |", "DELETE /api/runs/{run}"},
		{"| Status | Codes |", ""},
		{"| `403` | `bad_host` |", ""},
		{"| Interrupts `GET /api/x` | - |", ""},
	} {
		got := ""
		if m := webAPIRow.FindStringSubmatch(tc.line); m != nil {
			got = m[1] + " " + m[2]
		}
		if got != tc.want {
			t.Errorf("%q parsed as %q, want %q", tc.line, got, tc.want)
		}
	}
}
