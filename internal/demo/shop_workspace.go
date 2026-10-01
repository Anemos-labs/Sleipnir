package demo

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// The shop is the project of the showcase scenario: a small Go service that a team is asked to extend. Nothing in it is compiled (the
// model is a script, and the demo must run where Go is not installed); what matters is that it is a real git repository with real files
// of a realistic size, and a check (verify.sh) that the harness runs for real, in every worker's tree and on every merge.

// shopGoal is what the person asked for.
const shopGoal = "Build the shop: a catalogue with paging, a cart that totals in cents, the web handlers for both, smoke tests and a page of documentation. Keep to the conventions in AGENTS.md."

// verifyScript is the project's check, the command the swarm is started with (--verify "sh verify.sh"). It is POSIX sh and looks
// only at the project's own files: every Go file starts with its package clause, nothing says FIXME, and the shop has one default
// port, which is a rule that two workers can each keep in their own tree and break together, the case the merge queue is for.
const verifyScript = `#!/bin/sh
# The shop's check. The harness runs it in a worker's tree before its task may leave "doing", and again on the result of every merge.
fail=0
for f in $(find shop -name '*.go' 2>/dev/null | sort); do
  head -n 1 "$f" | grep -q '^package ' || { echo "FAIL: $f does not start with a package clause"; fail=1; }
  if grep -n 'FIXME' "$f"; then echo "FAIL: $f says FIXME"; fail=1; fi
done
n=$(grep -rn 'const DefaultPort' shop 2>/dev/null | wc -l | tr -d ' ')
if [ "$n" -gt 1 ]; then
  echo "FAIL: $n definitions of DefaultPort, at most 1 allowed:"
  grep -rn 'const DefaultPort' shop
  fail=1
fi
[ "$fail" -eq 0 ] && echo "ok: the shop passes its checks"
exit "$fail"
`

// checksScript is the cart's own check, used by one worker to loop on a failing command until the repetition guard speaks.
const checksScript = `#!/bin/sh
# Cart checks: totals are whole cents.
if grep -q 'TODO(rounding)' shop/cart/cart.go 2>/dev/null; then
  echo "FAIL: cart totals are not rounded to whole cents (shop/cart/cart.go)"
  exit 1
fi
echo "ok: cart totals are whole cents"
`

const shopAgents = `# Conventions

- One package per directory under shop/, named after the directory. Every Go file starts with its package clause.
- Money is integer cents, never a float. Totals round half up.
- Handlers return JSON with snake_case keys. List endpoints are paged: limit (default 20, at most 100) and after (an opaque cursor).
- One DefaultPort for the whole shop, defined once, in shop/catalogue. Other packages use it.
- Tests live next to the code (*_test.go); smoke tests that start the service live under tests/.
- Run ` + "`sh verify.sh`" + ` before you say a task is done.
`

func shopFiles() map[string]string {
	files := map[string]string{
		"README.md":             "# Shop\n\nA small shop service: a catalogue, a cart and the web handlers for both.\n\nRun the checks with `sh verify.sh`.\n",
		"AGENTS.md":             shopAgents,
		"verify.sh":             verifyScript,
		"checks.sh":             checksScript,
		"docs/api.md":           shopAPIDoc(),
		"docs/design.md":        shopDesignDoc(),
		"data/items.json":       shopItems(),
		"data/schema.md":        shopSchemaDoc(),
		"shop/catalogue/doc.go": "package catalogue\n\n// Package catalogue holds the items the shop sells.\n",
		"shop/cart/doc.go":      "package cart\n\n// Package cart holds what a customer has chosen to buy.\n",
		"shop/web/doc.go":       "package web\n\n// Package web serves the catalogue and the cart over HTTP.\n",
		"tests/README.md":       "Smoke tests that start the service live here.\n",
	}
	return files
}

// writeShop creates the project in root, makes it a git repository with one commit (worktree isolation starts every worker's tree from a
// commit) and leaves it clean.
func writeShop(root string) error {
	for name, body := range shopFiles() {
		p := filepath.Join(root, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			return err
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			return err
		}
	}
	for _, args := range [][]string{
		{"init", "-q", "-b", "main"},
		{"add", "-A"},
		{"commit", "-q", "-m", "the shop before the team: a catalogue, a cart and web, empty", "--no-gpg-sign"},
	} {
		cmd := exec.Command("git", append([]string{"-c", "user.name=Sleipnir demo", "-c", "user.email=demo@sleipnir.invalid", "-c", "init.defaultBranch=main"}, args...)...)
		cmd.Dir = root
		cmd.Env = append(os.Environ(), "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_SYSTEM=/dev/null", "GIT_TERMINAL_PROMPT=0")
		if out, err := cmd.CombinedOutput(); err != nil {
			return fmt.Errorf("git %s: %v: %s", args[0], err, strings.TrimSpace(string(out)))
		}
	}
	return nil
}

// the documents are long enough that reading them costs real tokens: a thread that is worth folding.

func shopAPIDoc() string {
	type ep struct{ method, path, what, detail string }
	eps := []ep{
		{"GET", "/items", "a page of catalogue items", "Query: q (a word of the name or a tag), limit, after. The page says next: the cursor of the following one, or null at the end. Items come back in the order of their ids, so a cursor is stable while the catalogue changes."},
		{"GET", "/items/{id}", "one item", "404 with code not_found when the id is unknown. The stock field is what could be sold now, not what is on the shelf."},
		{"POST", "/items", "add an item", "Body: id, name, price_cents, stock, tags. 409 with code exists when the id is taken. Only the owner may do this; the handler checks the role before it reads the body."},
		{"PATCH", "/items/{id}", "change an item", "Any of name, price_cents, stock, tags. A price change does not touch the carts that already hold the item: they keep the price they were filled at until they are checked out."},
		{"GET", "/cart", "the caller's cart", "The cart is found by the session cookie. A cart nobody has touched for a day is forgotten. The lines come back in the order they were added."},
		{"POST", "/cart/lines", "put an item in the cart", "Body: id, qty. Adding an item that is already there adds to its qty. 422 with code out_of_stock when qty is more than the stock."},
		{"DELETE", "/cart/lines/{id}", "take an item out", "Idempotent: taking out a line that is not there answers 204 as well."},
		{"GET", "/cart/total", "what the cart costs", "total_cents is the sum of price_cents times qty over the lines, in whole cents, rounded half up once at the end, never per line."},
		{"POST", "/checkout", "turn the cart into an order", "Body: the address. The stock is taken in the same step; when any line is short nothing is taken and the answer says which."},
		{"GET", "/orders/{id}", "an order", "Only its owner may read it. An order never changes after checkout; a refund is a new order with negative lines."},
		{"GET", "/healthz", "is the service up", "Answers 200 with the version. It does not touch the catalogue, so it says the process is alive and nothing else."},
	}
	var b strings.Builder
	b.WriteString("# Shop API\n\nJSON over HTTP, snake_case keys, errors as {\"code\": \"...\", \"message\": \"...\"}. Money is integer cents everywhere.\n\n")
	for _, e := range eps {
		fmt.Fprintf(&b, "## %s %s\n\n%s.\n\n%s\n\n", e.method, e.path, strings.ToUpper(e.what[:1])+e.what[1:], e.detail)
		fmt.Fprintf(&b, "Errors: 400 bad_request when the body or the query does not parse; 401 unauthorized without a session; 429 too_many_requests with Retry-After when a client is too fast; 500 internal with no detail of what failed.\n\n")
	}
	return b.String()
}

func shopDesignDoc() string {
	paras := []string{
		"The catalogue is read far more than it is written, so it is held in memory, loaded at start from data/items.json and saved back after every change. A crash loses at most the change in flight.",
		"Paging is by cursor, not by offset. The cursor is the id of the last item of the page, encoded, so that an item added or removed while a customer pages does not make a page repeat or skip an item.",
		"A cart is a list of lines, each an item id and a quantity. It never stores a price: the total is computed from the catalogue every time it is asked for, so a price change shows at once in carts that have not been checked out.",
		"The total is the sum over the lines of price times quantity, in whole cents, and it is rounded once, at the end, half up. Rounding each line would make a cart of ten cheap items cost a cent or two more than the sum of its parts.",
		"The web package is thin: it parses, calls the catalogue or the cart, and writes JSON. It has no rules of its own, so a rule is in one place and a handler cannot disagree with it.",
		"DefaultPort is defined once, in the catalogue package, because the service is one process and its neighbours (the smoke tests, the docs) must all say the same number.",
		"Smoke tests start the service on the default port, add an item, put it in a cart, read the total and check it out. They live under tests/ and need nothing but sh and curl.",
		"Nothing in the shop is concurrent by itself: a mutex guards the catalogue and another the carts, and no handler holds both at once, which is the whole locking story.",
	}
	var b strings.Builder
	b.WriteString("# Shop design\n\n")
	for i, p := range paras {
		fmt.Fprintf(&b, "## %d. %s\n\n%s\n\n%s\n\n", i+1, strings.SplitN(p, ",", 2)[0], p, "Why it matters here: the team works on this at the same time, so each of these rules is also a boundary between two workers' files, and a change that crosses it should be said in mail before it is written.")
	}
	return b.String()
}

func shopItems() string {
	names := []string{"mug", "kettle", "teapot", "whisk", "spatula", "colander", "grater", "peeler", "ladle", "tongs", "skillet", "saucepan", "baking tray", "rolling pin", "cutting board", "tea towel", "oven glove", "apron", "salt mill", "pepper mill"}
	colours := []string{"blue", "red", "green", "white", "black", "yellow"}
	var b strings.Builder
	b.WriteString("[\n")
	n := 0
	for _, c := range colours {
		for i, name := range names {
			n++
			if n > 48 {
				break
			}
			price := 450 + (i*137+len(c)*91)%4200
			stock := (i*7 + len(c)*3) % 60
			sep := ","
			if n == 48 {
				sep = ""
			}
			fmt.Fprintf(&b, "  {\"id\": \"sku-%03d\", \"name\": \"%s %s\", \"price_cents\": %d, \"stock\": %d, \"tags\": [\"kitchen\", \"%s\"]}%s\n", n, c, name, price, stock, c, sep)
		}
	}
	b.WriteString("]\n")
	return b.String()
}

func shopSchemaDoc() string {
	fields := []struct{ name, typ, rule string }{
		{"id", "string", "sku- and three digits; never reused, even after an item is removed"},
		{"name", "string", "what the customer sees; at most 80 characters; no markup"},
		{"price_cents", "integer", "whole cents, at least 1; the shop sells nothing for free"},
		{"stock", "integer", "what could be sold now; never negative; checkout takes it"},
		{"tags", "array of string", "at most 8; lower case; the first is the department"},
		{"qty", "integer", "on a cart line: at least 1; more than the stock is refused when the line is added"},
		{"cursor", "string", "opaque to clients: the encoded id of the last item of the page"},
	}
	var b strings.Builder
	b.WriteString("# Data schema\n\nThe fields the shop's JSON uses, and the rule each one keeps.\n\n| field | type | rule |\n|---|---|---|\n")
	for _, f := range fields {
		fmt.Fprintf(&b, "| %s | %s | %s |\n", f.name, f.typ, f.rule)
	}
	b.WriteString("\nValues that break a rule are refused at the edge (the handler), never stored and never repaired later.\n")
	return b.String()
}
