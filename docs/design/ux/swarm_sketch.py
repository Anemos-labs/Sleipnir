import random
from sketchlib import *
import horse as H

P = PAL
INK = {"a": "#d6336c", "b": "#1c7ed6", "c": "#2b8a3e", "d": "#e8590c", "e": "#7048e8"}
ROLE = dict(manager="#bb9af7", backend="#7aa2f7", frontend="#7dcfff", tester="#9ece6a", reviewer="#e0af68", docs="#ff9e64")
STATE = dict(think="#bb9af7", tool="#9ece6a", wait="#e0af68", idle="#565f89", stuck="#f7768e", done="#2ac3de", edit="#7aa2f7")


def mini_stack(s, x, y, w, shared, hit, tint):
    """One agent's prompt as a small bar: the inherited shared prefix (bright gradient) then its own layers."""
    g = [LAYER["G0"], LAYER["G1"], LAYER["G2"]]
    for i in range(w):
        if i < shared:
            s.put(x + i, y, "█", g[min(2, i * 3 // max(1, shared))])
        else:
            cached = (i - shared) < (w - shared) * hit
            s.put(x + i, y, "█" if cached else "░", tint, dim=not cached)


def lane(rnd, n, bias):
    """A minute of activity: runs of busy and idle with levels, deterministic."""
    out, i = [], 0
    while i < n:
        busy = rnd.random() < bias
        run = rnd.randint(2, 9)
        for _ in range(run):
            out.append(rnd.choice("▄▅▆▇█") if busy else rnd.choice(" ▁▁▂"))
        i += run
    return out[:n]


def swarm():
    cols, rows = 112, 51
    s = Screen(cols, rows)
    s.box(0, 0, cols, rows, "", P["faint"])
    s.runs(2, 0, [(" SLEIPNIR ", P["magenta"], None, "b"), ("▸ ", P["dim"]), ('swarm “add pagination to every list endpoint…” ', P["fg"])])
    stats = "◷ 02:10 · 8 agents · $0.31/$20 · ⛁ 94% "
    s.put(cols - 2 - len(stats), 0, stats, P["fg"])

    # horse
    rnd = random.Random(7)
    g = H.horse(1)
    hips = [11, 13, 16, 18, 25, 27, 30, 32]
    leg_cols = [ROLE["backend"], ROLE["backend"], ROLE["frontend"], ROLE["frontend"], ROLE["tester"], ROLE["reviewer"], ROLE["docs"], ROLE["manager"]]
    for cy, row in enumerate(H.halfblocks(g)):
        for cx, ch in enumerate(row):
            if ch == " ":
                continue
            if cy >= 5:
                k = min(range(8), key=lambda i: abs(hips[i] + (cy - 5) * 0 - (cx)))
                col = leg_cols[k]
            else:
                col = "#9d7cd8" if cx < 30 else "#bb9af7"
            s.put(3 + cx, 2 + cy, ch, col)
    s.put(3, 11, "eight legs · eight riders", P["dim"])

    # shared prefix panel
    s.box(52, 1, 58, 10, "one prefix, eight riders", P["faint"])
    s.runs(54, 2, [("shared G0–G2 ", P["fg"], None, "b"), ("41.2k tokens  ", P["dim"]), ("◕ warm 3:41 ", P["yellow"]), ("▰▰▰▰▰▰▰", P["yellow"]), ("▱", P["faint"])])
    x = 54
    for name, n in (("G0", 20), ("G1", 16), ("G2", 10)):
        for i in range(n):
            s.put(x + i, 4, "█", LAYER[name])
        s.put(x + n // 2 - 1, 5, name, LAYER[name], bold=True)
        x += n
    labels = ["m0", "w1", "w2", "w3", "w4", "w5", "w6", "w7"]
    heavy = [0, 2, 2, 1, 1, 0, 0, 1]
    for i, lab in enumerate(labels):
        cx = 56 + i * 6
        for j in range(3):
            s.put(cx + 1, 6 + j, "┃" if heavy[i] == 2 else "│" if heavy[i] else "╎", LAYER["G1"], dim=(heavy[i] == 0))
        s.put(cx, 9, lab, P["fg"], bold=True)
    s.put(54, 10, "", P["dim"])

    # agents board
    s.put(0, 12, "├" + "─" * (cols - 2) + "┤", P["faint"])
    s.put(2, 12, " agents ", P["fg"], bold=True)
    hdr = [(2, "AGENT"), (17, "STATE"), (33, "DOING"), (58, "PROMPT  inherited │ own"), (83, "TOK"), (90, "COST"), (97, "⛁"), (101, "LEASE")]
    for x0, t in hdr:
        s.put(x0, 13, t, P["dim"])
    agents = [
        ("m0 manager", "manager", "think", "⠹ thinking", "plan: split by endpoint", 12.1, .012, 96, "—", 10, .95),
        ("w1 backend", "backend", "tool", "⚙ bash", "go test ./orders/...", 31.0, .020, 97, "orders/*", 10, .97),
        ("w2 backend", "backend", "edit", "✎ edit", "internal/users/list.go", 28.4, .018, 95, "users/*", 10, .95),
        ("w3 frontend", "frontend", "wait", "✉ waits w1", "schema: GET /orders", 14.2, .009, 96, "web/api", 10, .96),
        ("w4 frontend", "frontend", "think", "⠹ thinking", "paginate <Orders>", 22.6, .014, 94, "web/ord", 10, .94),
        ("w5 tester", "tester", "idle", "◌ idle", "waits for t3 merge", 6.8, .004, 91, "—", 10, .91),
        ("w6 reviewer", "reviewer", "done", "✓ done", "approved t2", 9.9, .006, 95, "—", 10, .95),
        ("w7 docs", "docs", "stuck", "⚠ stuck ×2", "npm test — same failure", 18.0, .012, 88, "docs/*", 10, .55),
    ]
    for i, (name, role, st, state, doing, tok, usd, hit, lease, sh, hf) in enumerate(agents):
        y = 14 + i
        s.put(2, y, name.split()[0], ROLE[role], bold=True)
        s.put(5, y, name.split()[1], P["fg"])
        s.put(17, y, state, STATE[st], bold=(st in ("stuck", "tool")))
        s.put(33, y, doing, P["fg"] if st not in ("idle", "done") else P["dim"])
        mini_stack(s, 58, y, 22, 14, hf, ROLE[role])
        s.put(82, y, ("%.1fk" % tok).rjust(5), P["fg"])
        s.put(88, y, ("$%.3f" % usd).rjust(6), P["fg"])
        s.put(95, y, ("%d%%" % hit).rjust(4), P["green"] if hit >= 93 else P["yellow"] if hit >= 89 else P["red"])
        s.put(101, y, lease, P["dim"])

    # lanes + board
    s.put(0, 23, "├" + "─" * 62 + "┬" + "─" * (cols - 65) + "┤", P["faint"])
    s.put(2, 23, " swarm gantt · last 60 s ", P["fg"], bold=True)
    s.put(65, 23, " task board ", P["fg"], bold=True)
    for i, (name, role, st, *_rest) in enumerate(agents):
        y = 24 + i
        s.put(2, y, name.split()[0], ROLE[role], bold=True)
        r = random.Random(100 + i)
        cells = lane(r, 56, {"think": .7, "tool": .85, "edit": .8, "wait": .35, "idle": .15, "done": .4, "stuck": .6}[st])
        if st == "done":
            cells[-14:] = [" "] * 14
        for j, ch in enumerate(cells):
            s.put(6 + j, y, ch, ROLE[role] if ch != " " else P["faint"], dim=(ch in "▁▂"))
    s.put(6 + 38, 24 + 3, "✉", P["yellow"], bold=True)
    s.put(6 + 21, 24 + 1, "✉", P["yellow"], bold=True)
    s.put(6 + 44, 24, "◆", P["magenta"], bold=True)
    s.put(6 + 50, 24 + 7, "⚠", P["red"], bold=True)
    s.put(6, 32, "-60s", P["dim"]); s.put(6 + 26, 32, "-30s", P["dim"]); s.put(6 + 52, 32, "now", P["dim"])
    for y in range(24, 33):
        s.put(63, y, "│", P["faint"])
    s.runs(65, 24, [("todo 3", P["yellow"], None, "b")])
    s.runs(77, 24, [("running 4", P["green"], None, "b")])
    s.runs(89, 24, [("verifying 1", P["cyan"], None, "b")])
    s.runs(101, 24, [("merged 5", P["blue"], None, "b")])
    cols4 = [
        (65, ["▢ t9  docs", "▢ t10 e2e", "▢ t11 doc"], P["dim"]),
        (77, ["▣ t3  ord", "▣ t4  usr", "▣ t5  web", "▣ t6  cli"], P["fg"]),
        (89, ["◌ t7  ⠹"], P["cyan"]),
        (100, ["✓ t1 ✓ t2", "✓ t4 ✓ t5", "✓ t6"], P["green"]),
    ]
    for x0, items, col in cols4:
        for j, t in enumerate(items):
            s.put(x0, 26 + j, t, col)
    s.put(65, 31, "t7 verifying: go test ./orders/...", P["dim"])

    # bottom panels
    s.put(0, 33, "├" + "─" * 28 + "┬" + "─" * 40 + "┬" + "─" * 22 + "┬" + "─" * (cols - 94) + "┤", P["faint"])
    for x0, t in ((2, " merge queue "), (31, " mail "), (72, " governor "), (95, " at 50 agents ")):
        s.put(x0, 33, t, P["fg"], bold=True)
    for xx in (29, 70, 93):
        for y in range(34, 44):
            s.put(xx, y, "│", P["faint"])
    s.runs(2, 34, [("▸ t7 ", P["cyan"], None, "b"), ("rebase ✓ ─ verify ", P["fg"]), ("⠹", P["cyan"])])
    s.put(8, 35, "go test ./orders/...", P["dim"])
    s.runs(2, 36, [("  t8 ", P["dim"]), ("rebase ", P["dim"]), ("⠹", P["cyan"])])
    s.runs(2, 38, [("merged ", P["green"]), ("t1 t2 t4 t5 t6", P["fg"])])
    s.put(2, 39, "conflicts 0 · bounced back 1", P["dim"])
    mails = [("w1", "w3", "schema: GET /orders", "312"), ("m0", "w5", "start e2e after t3", "96"), ("w4", "m0", "done: table paged", "88"),
             ("w2", "w1", "rename ListOptions?", "41")]
    for i, (a, b, t, n) in enumerate(mails):
        s.runs(31, 34 + i, [("✉ ", P["yellow"], None, "b"), (a, P["fg"]), (" ➜ ", P["dim"]), (b + " ", P["fg"]), ("“" + t + "”", P["dim"])])
        s.put(63, 34 + i, (n + " tok").rjust(7), P["dim"])
    s.put(31, 39, "routed 14 · delivered 14 · dup 0 · ignored 1", P["dim"])
    s.runs(72, 34, [("rpm ", P["dim"]), ("212/240 ", P["fg"]), ("▰▰▰▰▰▰▰▰", P["green"]), ("▱▱", P["faint"])])
    s.runs(72, 35, [("429s ", P["dim"]), ("0", P["green"]), ("  retries ", P["dim"]), ("1", P["fg"])])
    s.runs(72, 36, [("$0.31/$20 ", P["fg"]), ("▰", P["green"]), ("▱▱▱▱▱▱▱▱▱", P["faint"])])
    s.runs(72, 37, [("shared prefix ", P["dim"]), ("3:41", P["yellow"])])
    s.runs(72, 38, [("≈ 6× faster than serial", P["dim"])])
    # 50-agent heatmap
    hm = random.Random(3)
    palette = [(STATE["think"], "■"), (STATE["tool"], "■"), (STATE["wait"], "■"), (STATE["idle"], "■"), (STATE["done"], "■"), (STATE["idle"], "■"), (STATE["tool"], "■")]
    for j in range(5):
        for i in range(10):
            col, ch = hm.choice(palette)
            s.put(97 + i * 1, 35 + j, ch, col, dim=(col == STATE["idle"]))
    for j, (l1, c1, l2, c2) in enumerate([("think", STATE["think"], "tool", STATE["tool"]), ("wait", STATE["wait"], "idle", STATE["idle"]), ("done", STATE["done"], "stuck", STATE["stuck"])]):
        s.put(95, 41 + j, "■", c1); s.put(97, 41 + j, l1, P["dim"])
        s.put(104, 41 + j, "■" if j < 2 else "⚠", c2); s.put(106, 41 + j, l2, P["dim"])
    # event feed
    s.put(0, 44, "├" + "─" * (cols - 2) + "┤", P["faint"])
    s.put(2, 44, " live feed ", P["fg"], bold=True)
    feed = [("02:10", "w1", P["green"], "✓ go test ./orders/...", "1.4 s · $0.0004 · ⛁ 97%"),
            ("02:09", "w7", P["red"], "⚠ same failure twice: npm test — nudged, stops at 8", "repeat guard"),
            ("02:08", "m0", P["magenta"], "◆ compacted 31.2k → 2.4k at a cold moment", "free · ⛁ kept 96%"),
            ("02:07", "w3", P["yellow"], "✉ waiting on w1: schema for GET /orders", "mail 312 tok")]
    for j, (t, a_, col, msg, tail) in enumerate(feed):
        s.put(2, 45 + j, t, P["dim"]); s.put(8, 45 + j, a_, col, bold=True); s.put(12, 45 + j, msg, P["fg"])
        s.put(cols - 2 - len(tail), 45 + j, tail, P["dim"])
    s.put(2, 49, "↑↓ agent · enter transcript · m mail · b board · c cache · t stack · p pause · ? help · q back to shell", P["dim"])

    notes = [
        (4, 1, INK["e"], ["Eight legs, eight workers: a leg lifts while", "its worker runs a tool; the gait quickens", "with load. Idle = a standing horse.", "(--no-anim: standing horse, no motion)"]),
        (6, 2, INK["a"], ["SPAWN = CACHE FORK. One cached prefix", "(G0–G2, 41.2k) carries every rider; a new", "worker pays a cache READ for it plus a", "two-line task card, never a full-price re-read."]),
        (15, 3, INK["c"], ["each row's bar: bright = inherited from", "the shared prefix, dimmer = the agent's own", "notes and thread. w7's went dark: it lost", "its cache, and the ⛁ column says 88%."]),
        (17, 4, INK["d"], ["states: ⠹ thinking ⚙ tool ✎ edit", "✉ waiting on mail ◌ idle ✓ done", "⚠ stuck (the repeat guard fired)"]),
        (26, 5, INK["b"], ["swarm gantt: the last minute of every agent.", "✉ mail  ◆ compaction  ⚠ stuck — the", "parallelism you paid for, made visible."]),
        (36, 6, INK["e"], ["verified merges: rebase, verify, merge;", "a failure bounces back to its worker", "with the output."]),
        (37, 7, INK["c"], ["at 50 agents the board becomes a heatmap;", "arrow keys focus one agent and open its", "live transcript."]),
    ]
    WR = 40 + int(cols * CW + 28)
    return page(s, "Sleipnir watch: a swarm at work (design sketch)",
                "full-screen cockpit for swarm runs: sleipnir watch SESSION (live) · sleipnir replay SESSION (recorded)",
                "sleipnir watch — swarm “add pagination…” — 112×51",
                place_notes(WR, [(r, n, c, l, cols - 1) for r, n, c, l in notes]), extra_w=430)
