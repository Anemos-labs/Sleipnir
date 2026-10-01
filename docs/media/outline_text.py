#!/usr/bin/env python3
"""Turns the two lines of the wordmark into outlines, so that the logo does not depend on the reader's fonts.

    pip install fonttools && python3 outline_text.py       # writes wordmark-paths.json

An SVG that is shown through <img> (as the GitHub README shows ours) is drawn with the fonts of whoever looks at it, and a bold system
font is wider than the one the file was made with: the live <text> version of the wordmark lost the end of "Sleipnir" on a Windows
machine, and in the PNG made here. The outlines below are drawn from DejaVu Sans (Bold for the name, upright slanted by 12 degrees for the
tagline), which is free to embed (the Bitstream Vera licence); make_logo.py puts them in the files and sizes the picture from their
measured width. Needed only to change the words, the sizes or the font; make_logo.py reads the result and needs no font library.
Kerning is not applied (DejaVu's pairs are small at these sizes)."""
import json, math, os, sys

from fontTools.pens.svgPathPen import SVGPathPen
from fontTools.pens.transformPen import TransformPen
from fontTools.ttLib import TTFont

HERE = os.path.dirname(os.path.abspath(__file__))
FONTS = "/usr/share/fonts/truetype/dejavu"

LINES = {
    # key: (text, font file, size in px, letter spacing in px, slant in degrees)
    "name": ("Sleipnir", "DejaVuSans-Bold.ttf", 190, 2, 0),
    "tagline": ("one manager brain, many legs", "DejaVuSans.ttf", 46, 0, 12),
}


def outline(text, path, size, spacing, slant):
    font = TTFont(path)
    cmap, glyphs, hmtx = font.getBestCmap(), font.getGlyphSet(), font["hmtx"]
    scale = size / font["head"].unitsPerEm
    shear = math.tan(math.radians(slant))
    parts, x = [], 0.0
    for ch in text:
        name = cmap[ord(ch)]
        pen = SVGPathPen(glyphs, ntos=lambda v: ("%.1f" % v).rstrip("0").rstrip("."))
        # font units (y up) to the picture (y down), the baseline at y = 0 and the pen at x
        glyphs[name].draw(TransformPen(pen, (scale, 0, scale * shear, -scale, x, 0)))
        if pen.getCommands():
            parts.append(pen.getCommands())
        x += hmtx[name][0] * scale + spacing
    return {"d": "".join(parts), "width": round(x - spacing, 1), "size": size}


def main():
    out = {"font": "DejaVu Sans (Bitstream Vera licence)"}
    for key, (text, file, size, spacing, slant) in LINES.items():
        out[key] = dict(outline(text, os.path.join(FONTS, file), size, spacing, slant), text=text)
    dest = os.path.join(HERE, "wordmark-paths.json")
    with open(dest, "w") as f:
        json.dump(out, f, indent=1)
        f.write("\n")
    print("wrote", dest, {k: out[k]["width"] for k in LINES})


if __name__ == "__main__":
    sys.exit(main())
