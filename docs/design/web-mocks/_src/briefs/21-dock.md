# V1 · DOCK: Cockpit with a workspace around it

Deliverable: `docs/design/web-mocks/v2/01-dock.html`, title `Sleipnir Dock`.

## Idea
The most direct evolution: everything the owner loves stays exactly where it was, and a proper **workspace shell** is built
around it: session tabs, a left navigation rail, and a persistent Radio rail with channels. It is the balanced,
"everything one click away" option, the one to pick if you want the safest path to a tool you can live in. Its depth is
**breadth done properly**: Settings and Tools are real, organised, and fast.

## Information architecture
* **Top**: the Cockpit HUD (compact), directly under it a **session strip** (browser-like tabs): `shop ● ?`, `orders-api`,
  `docs-sweep ⟳`, `+ new`, `↺ resume`; per-tab state glyph, cost, a `?` when a question waits; far right the global **inbox**
  (needs-you count, click for the cross-session list), global budget, connection chip. Closing a tab asks in-page.
* **Left rail** (icons + labels, collapsible, badges): Cockpit · Radio · Files · Changes · Checkpoints · Cache · Mail · Board · Replay ·
  Sessions · Tools · Settings. Keyboard: `g` then a letter (g c, g r, g f...), and the round-1 `o c m b r s`.
* **Main**: the active view. **Cockpit view** is the beloved hero layout (the horse, nine stalls: manager + 8 workers).
  **Files / Changes / Checkpoints** are three views of one *Workspace* screen (the Forge tracker restyled in Cockpit's idiom): left
  tree/list, centre diff viewer with attribution gutter and the time-travel scrubber, bottom Verify/Merge strip.
* **Right Radio rail** persists across views (collapsible, resizable): channel switcher (Manager · be-1 be-2 fe-1 sc-1 sc-2 sc-3 ts-1
  rv-1 · Mail), per-agent header with Steer/Interrupt, the approvals box on top, composer with slash/@ menus; hold-on-hover (core).
* **Settings** (real forms, grouped left list): Models (catalogue, filters, favourites) · Roles & effort · Budget · Permissions
  (mode, rule editor with origins, `tests` preset) · Trust · MCP · Skills & commands · Providers & login · Config layers
  · Run settings (swarm N workers, isolation, verify, commit, mailman) · Appearance & motion (hover behaviour, density,
  reduced motion). Every change takes effect visibly elsewhere (mode chip, HUD, drawers).
* **Tools**: a catalogue of every `sleipnir` command as cards (grouped: run & chat, observe, evaluate, set up) opening the
  runner (or a purpose-built panel for doctor, schedule, sessions).

## Signature moments
1. A tab strip that makes three live sessions feel like three real things: switch, a background question arrives (toast + badge), answer it from the inbox.
2. The rail's `Workspace` screen: tree stripes + leases, a diff with a caret typing, scrubber back to c05, Restore preview.
3. Settings > Permissions: add a rule, see it listed with origin `this session`, trigger it in the chat.
4. `Tools > doctor`: fill the form, Run, watch the probe stream, read the result card.
5. Hover a message in Radio: the hold eases in, the linked leg/stall/row stay still and lit; release catches up.

## Avoid
Hiding the Cockpit behind navigation (it is the home view, the first paint); a generic admin-panel look; flattening the glow.
