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


def horse(phase=0):
    """phase 0..3: the gait. Eight legs: four behind, four in front; each leg swings between a tucked and a stretched foot,
    offset from its neighbours so the gallop reads as a wave."""
    g = [[0] * W for _ in range(H)]
    ellipse(g, 20, 7, 11.5, 3.3)                                        # body
    poly(g, [(28, 5), (31, 6), (36, 2), (34, 0)])                       # neck
    poly(g, [(34, 0), (39, 0), (43, 3), (42, 5), (39, 4), (36, 3)])     # head
    g[0][35] = g[0][36] = 1                                             # ear
    for i in range(5):                                                  # mane down the back of the neck
        g[4 - i][27 + i] = 1
        g[5 - i][27 + i] = 1
    line(g, 9, 5, 5, 5); line(g, 5, 5, 3, 8); line(g, 3, 8, 4, 12); line(g, 9, 6, 6, 8); line(g, 6, 8, 6, 11)   # tail
    back = [(11, (5, 16), (13, 12)), (13, (9, 17), (15, 13)), (16, (14, 16), (17, 13)), (18, (19, 14), (19, 12))]
    front = [(24, (23, 17), (26, 13)), (26, (31, 16), (28, 12)), (29, (35, 14), (31, 12)), (31, (40, 12), (33, 11))]
    for k, (hx, stretched, tucked) in enumerate(back + front):
        t = (math.sin(phase * math.pi / 2 + k * 0.9) + 1) / 2
        fx = round(tucked[0] + (stretched[0] - tucked[0]) * t)
        fy = round(tucked[1] + (stretched[1] - tucked[1]) * t)
        line(g, hx, 9, fx, fy)
        g[min(H - 1, fy)][min(W - 1, fx)] = 1
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
