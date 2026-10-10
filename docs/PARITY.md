# Parity of the terminal and web interfaces

The terminal interface (the commands and flags, the line chat, the chat program and its views) and the web interface (`sleipnir web`
and the page it serves) run the same sessions. What a person can do and see in one is meant to be possible in the other, and the
repository fails a change that makes them differ without saying so. `internal/parity` holds the guard.

## The rule

A change that adds, removes or alters something a person does or sees in one interface has one of three outcomes in the same
change:

1. it is built in the other interface too;
2. it is listed in the contract (`internal/parity/contract/<dimension>.json`) as `terminal_only` or `web_only`, with the reason the
   interfaces differ in it;
3. it is listed under `gaps`: a known difference that is to be closed. A gap is reported on every run, and the test fails as soon
   as it is closed (the entry is then removed) or when a new one appears, so the set of differences can only shrink on purpose.

An entry whose item no longer differs fails as stale. An entry needs a reason of at least a sentence. A contract file is read with
unknown fields refused.

## Dimensions

Each dimension lists the items of both interfaces from their code, never from a hand-kept list, and compares them
(`parity.Differences.Check`).

| Dimension | Terminal side | Web side | Test |
|---|---|---|---|
| Commands | the commands `main.go` dispatches and the generated CLI spec | what the runner, a dedicated screen or the New session dialog does for each | `cmd/sleipnir` `TestParityCommands` |
| Flags | every flag and positional of the spec; the chat flag parser | the controls of the command runner's forms and of the New session dialog | `internal/parity` `TestParityFlags`, `cmd/sleipnir` `TestParityChatFlagsAreTheCodes` |
| Slash commands | `chatCommands` | the route `GET /api/sessions/{id}/slash` of a real tab and the page's handler for each | `cmd/sleipnir` `TestParitySlash` |
| Views | `app.View` and the chat program's full-screen pages | the page's registered views, rail items and palette entries | `internal/parity` `TestParityViews` |
| Settings | the keys of `config.Config`, read by reflection | the keys the Settings controls change | `internal/parity` `TestParitySettings` |
| Events | the event types the state package and the chat program handle | the event types the translator handles, and those it shows through the state it shares with the terminal | `internal/parity` `TestEventsParity` |
| Facts | numbers folded from recorded sessions by `internal/tui/state` | the same numbers from the page's reducer over the recording's page stream | `internal/parity` `TestTerminalFacts`, `internal/web/uidev/test/parity-facts.test.mjs` |
| Keys | every key pressed in the states of the chat program | every key dispatched in the states of the page | `internal/tui/app` `TestKeysParityTerminal`, `internal/web/uidev/test/keys-parity.test.mjs`, `internal/parity` `TestKeysTerminalAndWebAgree` |

Lists cannot see a fix: an event read differently, a total that includes one more thing. The facts dimension covers that. The same
recordings (`internal/web/translate/testdata`, `internal/parity/testdata/team.ui.jsonl`) go through both interfaces, and the facts
(agents and their final states, tasks by status, tokens, cost, cache hit ratio, merge counters, goal, stalls, mail and others)
must come out equal. A difference is a failure naming the fact, the recording and both values.

The keys the page tells its users about are held to the same record. The Help sheet and Settings › Look › Keys are read as the page draws them
(the rows they build themselves and the rows of `SHORTCUTS` in `internal/web/ui/js/11-data-live.js`), and
`internal/web/uidev/test/help-keys.test.mjs` fails when a row names a key that `internal/parity/testdata/keys-web.json` does not list as
doing something on the page. The first column of a row holds keys only (the grammar is at the head of the test); what a key does goes in the
second. The other direction is held too: every key that opens a view (the `g` chords of the views rail and the single keys, found by pressing
each single-character key of `keys-web.json` on the page) is named by the Help sheet and by the Settings card, except a key listed in the test's
`NOT_ADVERTISED` with its reason.

## The page's inventory

The page's registries (views, rail, palette, slash handlers, Settings controls, dialogs, the runner's forms) are JavaScript, so
`internal/web/uidev/inventory.mjs` runs the page's own scripts and writes what they declare to `internal/parity/webui.json`. The Go
tests read that checked-in file and need no Node. `internal/web/uidev/test/inventory.test.mjs` fails when the file is stale.

```sh
sh scripts/gen-webui-inventory.sh           # rewrite internal/parity/webui.json
sh scripts/gen-webui-inventory.sh --check   # exit 1 when it is out of date
```

The inventory reads `internal/web/clispec/clispec.json`; regenerate that first (`sh scripts/gen-clispec.sh`) when a command or a
flag changed.

## Failures and what to do

| The failure says | Do |
|---|---|
| `X exists in the terminal interface and not in the web interface` | build X in the page, or list it under `terminal_only` in the dimension's contract file, with the reason |
| `X exists in the web interface and not in the terminal interface` | build X in the terminal, or list it under `web_only`, with the reason |
| `X under <list> is stale` | remove the entry: the difference is gone |
| `webui.json is out of date` | `sh scripts/gen-webui-inventory.sh`, read the diff |
| a fact differs | decide which interface is right, fix the other, then run `go test ./internal/parity -run TestTerminalFacts -update` and `go test ./internal/parity -run TestFactsStreamsAreCurrent -update`, and keep both halves equal |
| `the Help sheet: the row ... names ..., which the page does nothing with` | give the row the keys the page has (`SHORTCUTS` in `internal/web/ui/js/11-data-live.js`), keep words in its second column, or take the row out |
| `the Help sheet does not name "g q", which opens a view on the page` | add the key to the sheet's row of the keys of the views (`internal/web/ui/js/87-ui-sheets.js`, and the card in `98-ui-settings.js`), or list it in `NOT_ADVERTISED` of `help-keys.test.mjs` with the reason it is hidden |
| a key differs | build the key in the other interface, or list it in `contract/keys.json`; record the terminal's keys with `go test ./internal/tui/app -run TestKeysParityTerminal -update` and the page's with `node internal/web/uidev/test/keys-parity.test.mjs --update` |

Every guard has a test that proves it fails: a synthetic extra item on either side makes the comparison report it, and a contract
entry removed in turn makes its dimension fail.

## What the guard does not decide

It decides that a difference is stated, not that the stated reason is good: the contract entries are read in review. It compares
what each interface can do, not how it looks (`scripts/web-parity.mjs` compares the page with its reference scenes). Configuration
that has no control on the page (the cache and tool limits, provider definitions, hooks) is `terminal_only` in the settings
contract: it is edited in the configuration files, which the page shows read-only on its Config layers page.
