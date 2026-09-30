from sketchlib import *

P = PAL
INK = {"a": "#d6336c", "b": "#1c7ed6", "c": "#2b8a3e", "d": "#e8590c", "e": "#7048e8"}


def bar(s, x, y, parts, bright_to, total=None):
    """parts: [(layer, cells)]. Cells before bright_to are served from cache (bright), the rest dim; one ▓ at the edge."""
    used = 0
    for name, n in parts:
        for i in range(n):
            pos = used + i
            if pos < bright_to - 1:
                s.put(x + pos, y, "█", LAYER[name])
            elif pos == bright_to - 1:
                s.put(x + pos, y, "▓", LAYER[name])
            else:
                s.put(x + pos, y, "░", LAYER[name], dim=True)
        used += n
    return used


def story():
    cols, rows = 112, 38
    s = Screen(cols, rows)
    PARTS = [("G0", 10), ("G1", 13), ("G2", 7), ("G3", 4), ("G4", 3), ("G5", 10), ("G6", 1)]   # 48 cells
    # 1. cold -> warm
    s.box(0, 0, 55, 18, "1  the first request warms the prefix", P["faint"])
    s.runs(2, 2, [("request 1", P["fg"], None, "b"), ("   prefix cold: nothing to read", P["dim"])])
    s.put(2, 3, "▕", P["dim"]); bar(s, 3, 3, PARTS, 0); s.put(51, 3, "▏", P["dim"])
    s.runs(3, 4, [("  0% cached · writing the cache…", P["dim"])])
    s.runs(2, 6, [("request 2", P["fg"], None, "b"), ("   the prefix matches: a light sweeps it", P["dim"])])
    s.put(2, 7, "▕", P["dim"]); bar(s, 3, 7, PARTS, 27); s.put(51, 7, "▏", P["dim"])
    s.put(28, 7, "▶", P["yellow"], bold=True); s.put(29, 7, "▷", P["yellow"]); s.put(30, 7, "▹", P["yellow"], dim=True)
    s.runs(3, 8, [("  the match length is the lit part", P["dim"])])
    s.runs(2, 10, [("request 2", P["fg"], None, "b"), ("   done", P["dim"])])
    s.put(2, 11, "▕", P["dim"]); bar(s, 3, 11, PARTS, 45); s.put(51, 11, "▏", P["dim"])
    s.runs(3, 12, [("  ⛁ 93% cached", P["green"], None, "b"), ("   saved ≈ ", P["dim"]), ("$0.012", P["green"]), (" ← counts up", P["dim"])])
    s.runs(2, 14, [("A 48k-token prompt, read from cache: you see the", P["dim"])])
    s.runs(2, 15, [("light arrive, then the number. Nothing to interpret.", P["dim"])])
    # 2. compaction fold
    s.box(57, 0, 55, 18, "2  compaction is a fold you can watch", P["faint"])
    s.put(59, 2, "thread", P["dim"]); s.put(66, 2, "▓" * 34, LAYER["G5"]); s.put(101, 2, "31.2k", P["fg"])
    s.put(59, 4, "fold ↓", P["dim"]); s.put(66, 3, "▓" * 22, LAYER["G5"])
    s.put(66, 4, "▓" * 12, LAYER["G5"]); s.put(66, 5, "▓" * 5, LAYER["G5"])
    s.put(66, 6, "▒▒", LAYER["G4"]); s.put(69, 6, "2.4k", P["fg"])
    s.put(59, 8, "spine", P["dim"])
    s.runs(66, 8, [("▏", LAYER["G4"]), ("resume #7", P["fg"], None, "b"), (" “fixed offset; test green”", P["dim"])])
    s.runs(59, 10, [("◆ compacted 31.2k → 2.4k", P["magenta"], None, "b")])
    s.runs(59, 11, [("cache rewritten at a ", P["dim"]), ("cold moment", P["cyan"]), (": free", P["dim"])])
    s.runs(59, 13, [("the harness only commits a patch when the", P["dim"])])
    s.runs(59, 14, [("cache economics say so, and shows you why", P["dim"])])
    # 3. spawn = cache fork
    s.box(0, 19, 55, 18, "3  spawn = cache fork", P["faint"])
    x = 3
    s.put(2, 21, "shared", P["dim"])
    for name, n in (("G0", 14), ("G1", 18), ("G2", 10)):
        s.put(x + 6, 21, "█" * n, LAYER[name]); s.put(x + 6 + n // 2 - 1, 22, name, LAYER[name], bold=True); x += n
    s.put(2, 22, "41.2k", P["fg"])
    for i in range(3):
        s.put(12 + i * 14, 23, "╎", LAYER["G1"])
    s.runs(10, 24, [("↓ inherited at the cache READ price", P["green"], None, "b")])
    s.put(2, 26, "w3 new", P["frontend"] if False else P["cyan"], bold=True)
    s.put(9, 26, "█" * 42, LAYER["G1"]); s.put(9, 26, "█" * 14, LAYER["G0"]); s.put(23, 26, "█" * 18, LAYER["G1"]); s.put(41, 26, "█" * 10, LAYER["G2"])
    s.put(51, 26, "▒", P["yellow"])
    s.runs(9, 27, [("inherits 41.2k", P["green"]), ("  ·  pays 0.8k", P["yellow"]), (" (the task card)", P["dim"])])
    s.runs(2, 29, [("t0 the row appears · t1 its bar fills in the", P["dim"])])
    s.runs(2, 30, [("shared colours · t2 only the task card is paid", P["dim"])])
    s.runs(2, 32, [("Eight workers do not read the project eight times.", P["fg"])])
    s.runs(2, 33, [("You watch them not do it.", P["fg"], None, "b")])
    # 4. cache break alarm
    s.box(57, 19, 55, 18, "4  a cache break is an alarm, not a mystery", P["red"])
    names = ["G0", "G1", "G2", "G3", "G4", "G5", "G6"]
    xx = 59
    for nm in names:
        ok = nm in ("G0", "G1", "G2")
        bad = nm == "G3"
        s.runs(xx, 21, [(nm + " ", LAYER[nm], None, "b"), ("✓" if ok else "✗" if bad else "·", P["green"] if ok else P["red"] if bad else P["dim"], None, "b")])
        xx += 7
    s.put(59, 23, "▕", P["dim"]); bar(s, 60, 23, PARTS, 27); s.put(108, 23, "▏", P["dim"])
    s.put(60 + 27, 22, "⚠", P["red"], bold=True)
    s.runs(59, 25, [("⚠ prefix broke at G3 (notes)", P["red"], None, "b")])
    s.runs(59, 26, [("a fact was promoted in the middle of a turn", P["dim"])])
    s.runs(59, 27, [("+6.1k tokens re-read at full price ", P["fg"]), ("≈ $0.004", P["yellow"])])
    s.runs(59, 29, [("hint: ", P["cyan"], None, "b"), ("promote at the next cold moment;", P["dim"])])
    s.runs(59, 30, [("Sleipnir has queued it for you.", P["dim"])])
    s.runs(59, 32, [("the status line flashes red for one second,", P["dim"])])
    s.runs(59, 33, [("the sparkline gets a ⚠, /cache explains it.", P["dim"])])

    notes = [
        (3, 1, INK["b"], ["the match length IS the lit part:", "a 48k prompt, read from cache,", "arrives as a sweep of light"]),
        (5, 2, INK["e"], ["a compaction is animated as a fold;", "what it saved and when it was", "committed are printed beside it"]),
        (24, 3, INK["c"], ["the signature picture: a spawned", "worker starts already filled with the", "inherited prefix"]),
        (26, 4, INK["a"], ["silent cache regressions are the", "expensive bug; here they light the", "exact layer, with the price"]),
    ]
    WR = 40 + int(cols * CW + 28)
    return page(s, "Sleipnir: the cache and the swarm, animated (storyboard)",
                "each panel is a few frames of a real animation, driven by the session's event log; all of it is off with --no-anim",
                "sleipnir — animation storyboard — 112×38",
                place_notes(WR, [(r, n, c, l, cols - 1) for r, n, c, l in notes]), extra_w=430)
