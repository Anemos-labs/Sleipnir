"""Pixel art of the eight-legged horse, drawn from shapes and converted to half-block characters (two pixel rows per cell)."""
import math

W, H = 46, 18


def line(g, x0, y0, x1, y1, v=1):
    dx, dy = abs(x1 - x0), -abs(y1 - y0)
    sx, sy = (1 if x0 < x1 else -1), (1 if y0 < y1 else -1)
    err = dx + dy
    while True:
        if 0 <= x0 < W and 0 <= y0 < H:
            g[y0][x0] = v
        if x0 == x1 and y0 == y1:
            break
        e2 = 2 * err
        if e2 >= dy:
            err += dy
            x0 += sx
        if e2 <= dx:
            err += dx
            y0 += sy


def ellipse(g, cx, cy, rx, ry):
    for y in range(H):
        for x in range(W):
            if ((x - cx) / rx) ** 2 + ((y - cy) / ry) ** 2 <= 1:
                g[y][x] = 1


def poly(g, pts):
    minx, maxx = int(min(p[0] for p in pts)), int(max(p[0] for p in pts))
    miny, maxy = int(min(p[1] for p in pts)), int(max(p[1] for p in pts))
    for y in range(miny, maxy + 1):
        for x in range(minx, maxx + 1):
            inside, j = False, len(pts) - 1
            for i in range(len(pts)):
                xi, yi, xj, yj = pts[i][0], pts[i][1], pts[j][0], pts[j][1]
                if (yi > y) != (yj > y) and x < (xj - xi) * (y - yi) / (yj - yi + 1e-9) + xi:
                    inside = not inside
                j = i
            if inside and 0 <= x < W and 0 <= y < H:
                g[y][x] = 1


HIPS = [10, 13, 16, 19, 22, 25, 28, 31]     # eight legs, evenly spread along the whole belly: none missing in the middle


def horse(phase=0):
    """phase 0..3: the gait. Eight legs, one every three pixels under the belly, fanned like a galloping stride: the hind ones lean
    back, the front ones reach forward, the middle ones stay closer to upright; each swings with its own offset so the cycle reads
    as a wave, and a leg that is lifted is shorter. Every leg has a knee."""
    g = [[0] * W for _ in range(H)]
    ellipse(g, 20, 7, 11.5, 3.3)                                        # body
    poly(g, [(28, 5), (31, 6), (36, 2), (34, 0)])                       # neck
    poly(g, [(34, 0), (39, 0), (43, 3), (42, 5), (39, 4), (36, 3)])     # head
    g[0][35] = g[0][36] = 1                                             # ear
    for i in range(5):                                                  # mane down the back of the neck
        g[4 - i][27 + i] = 1
        g[5 - i][27 + i] = 1
    # the tail streams back from the rump at the height of the back, rising a little, in two wisps; it must never hang down into the
    # legs' rows (it read as two more legs)
    line(g, 9, 5, 6, 4); line(g, 6, 4, 3, 4); line(g, 3, 4, 1, 6)
    line(g, 9, 6, 6, 6); line(g, 6, 6, 3, 7); line(g, 3, 7, 1, 9)
    for k, hx in enumerate(HIPS):
        wave = phase * math.pi / 2 + k * math.pi / 4
        ang = (k - 3.5) * 0.16 + 0.30 * math.sin(wave)                  # radians from the vertical, + is forward
        lift = max(0.0, math.sin(wave + math.pi / 2))                   # 0..1
        upper, lower = 4.0, 5.0 - 2.2 * lift
        kx = hx + round(upper * math.sin(ang * 0.6))
        ky = 9 + round(upper * math.cos(ang * 0.6))
        fx = kx + round(lower * math.sin(ang * 1.5 - 0.25 * lift))
        fy = ky + round(lower * math.cos(ang * 1.4))
        line(g, hx, 9, kx, ky)
        line(g, kx, ky, fx, fy)
    return g


def halfblocks(g):
    rows = []
    for y in range(0, H, 2):
        row = ""
        for x in range(W):
            t, b = g[y][x], g[y + 1][x] if y + 1 < H else 0
            row += "█" if t and b else "▀" if t else "▄" if b else " "
        rows.append(row)
    return rows


if __name__ == "__main__":
    for r in halfblocks(horse(0)):
        print(r)
