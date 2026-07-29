#!/usr/bin/env python3
"""Generate the mail-muncher brand assets.

Everything is authored on an exact pixel grid and emitted as SVG made of
`<rect>` elements with `shape-rendering="crispEdges"`. The SVGs under
`docs/assets/brand/` are the masters the site and the README consume; this
script regenerates them, plus the PNG rasters, from the grids below.

    python3 branding/build.py

Requires `rsvg-convert` (librsvg) for the PNGs and `fonttools` for converting
the Silkscreen wordmark to outlines so no SVG consumer needs the font file.

Every colour is one of the eleven values in the brand palette; see BRAND.md.
"""

import os
import subprocess

ROOT = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
OUT = os.path.join(ROOT, "docs", "assets", "brand")
FONT = os.path.join(ROOT, "branding", "fonts", "Silkscreen-Bold.ttf")

# --------------------------------------------------------------- palette ---
# Role -> hex. Only values from the brand palette table are permitted here.
PAL = {
    "o": "#6B625C",  # keyline around the case and the envelope   (Muted light)
    "c": "#F1EBE4",  # case cream, the main body colour           (Surface light)
    "d": "#A79C93",  # case shade, right and bottom edges         (Muted dark)
    "s": "#17130F",  # screen                                     (Paper dark)
    "t": "#FBF8F5",  # teeth and eye whites, the brightest value  (Paper light)
    "p": "#1F1A17",  # pupils and mouth interior                  (Ink light)
    "e": "#F1EBE4",  # envelope paper                             (Surface light)
    "r": "#E0533D",  # chevron, blush, envelope seams, power LED  (Accent light)
}
FIELD = "#17130F"  # badge field                                  (Paper dark)
INK = "#1F1A17"    # wordmark on light surfaces                   (Ink light)
CREAM = "#F4EFEA"  # wordmark on dark surfaces                    (Ink dark)
MUTED_D = "#A79C93"
ACCENT = "#E0533D"


# ------------------------------------------------------------ grid tools ---
def grid(w, h, fill="."):
    return [[fill] * w for _ in range(h)]


def rect(g, x0, y0, x1, y1, c):
    for y in range(y0, y1 + 1):
        for x in range(x0, x1 + 1):
            if 0 <= y < len(g) and 0 <= x < len(g[0]):
                g[y][x] = c


def rrect(g, x0, y0, x1, y1, c, cut=1):
    """Filled rect with `cut` steps knocked off each corner."""
    for y in range(y0, y1 + 1):
        for x in range(x0, x1 + 1):
            if min(x - x0, x1 - x) + min(y - y0, y1 - y) < cut:
                continue
            g[y][x] = c


def keyline(g, c="o", over=("c", "d", "s", "t", "p", "r")):
    """Wrap the current silhouette in a one-pixel keyline."""
    h, w = len(g), len(g[0])
    add = []
    for y in range(h):
        for x in range(w):
            if g[y][x] != ".":
                continue
            for nx, ny in ((x - 1, y), (x + 1, y), (x, y - 1), (x, y + 1)):
                if 0 <= nx < w and 0 <= ny < h and g[ny][nx] in over:
                    add.append((x, y))
                    break
    for x, y in add:
        g[y][x] = c


def stamp(g, x0, y0, art, c):
    cells = []
    for i, row in enumerate(art):
        for j, ch in enumerate(row):
            if ch == "#":
                g[y0 + i][x0 + j] = c
                cells.append((x0 + j, y0 + i))
    return cells


def rim(g, cells, c="o", over=("e", "r")):
    """Sink `cells` into whatever they overlap by darkening the edge around
    them. Teeth and envelope are both cream; without this they merge."""
    h, w = len(g), len(g[0])
    todo = set()
    for x, y in cells:
        for nx, ny in ((x - 1, y), (x + 1, y), (x, y - 1), (x, y + 1)):
            if 0 <= nx < w and 0 <= ny < h and g[ny][nx] in over:
                todo.add((nx, ny))
    for x, y in todo:
        g[y][x] = c


def rows(g):
    return ["".join(r) for r in g]


# ------------------------------------------------------- the 48x48 mark ---
def mark48():
    """The full mark: a cream CRT with a face, chomping an envelope.

    Body x2..x41 / y1..y37, screen x6..x37 / y5..y31, stand below, envelope
    breaking out of the right edge so it reads as half-swallowed.
    """
    g = grid(48, 48)

    # case, neck and base as one cream silhouette ---------------------------
    rrect(g, 2, 1, 41, 38, "c", cut=3)          # case
    rect(g, 18, 39, 25, 41, "c")                # neck
    rrect(g, 13, 42, 30, 42, "c", cut=0)        # base
    rrect(g, 10, 43, 33, 46, "c", cut=1)

    # case shading: light falls top-left --------------------------------------
    rect(g, 40, 3, 41, 38, "d")                 # right edge
    rect(g, 4, 37, 41, 38, "d")                 # bottom edge
    rect(g, 11, 46, 32, 46, "d")                # base front
    rect(g, 25, 39, 25, 41, "d")                # neck right

    # screen ------------------------------------------------------------------
    rrect(g, 6, 5, 37, 33, "s", cut=2)
    rect(g, 5, 5, 5, 33, "d")                   # recessed inner bezel, left
    rect(g, 6, 4, 37, 4, "d")                   # recessed inner bezel, top

    # keyline around the whole cream silhouette -------------------------------
    keyline(g)

    # ---- face ---------------------------------------------------------------
    # prompt chevron: a three-pixel-thick ">" at the left of the screen
    chevron = ["###....",
               "####...",
               ".####..",
               "..####.",
               "...####",
               "..####.",
               ".####..",
               "####...",
               "###...."]
    stamp(g, 8, 9, chevron, "r")

    # Angry, because the brow drops toward the nose. The pupil is fully
    # enclosed in white; an open notch reads as a lowercase b and d.
    eye_l = ["###....",
             "#####..",
             "#######",
             "##...##",
             "##...##",
             "#######",
             ".#####."]
    eye_r = ["....###",
             "..#####",
             "#######",
             "##...##",
             "##...##",
             "#######",
             ".#####."]
    stamp(g, 16, 8, eye_l, "t")
    stamp(g, 27, 8, eye_r, "t")

    rect(g, 18, 16, 22, 16, "r")                # blush
    rect(g, 28, 16, 32, 16, "r")

    # ---- mouth --------------------------------------------------------------
    rrect(g, 15, 19, 36, 31, "t", cut=2)        # one-pixel cream rim ...
    rrect(g, 16, 20, 36, 30, "p", cut=1)        # ... around a dark gape

    def tooth_down(x):
        return stamp(g, x, 20, ["##", ".#", ".#"], "t")

    def tooth_up(x):
        return stamp(g, x, 28, [".#", ".#", "##"], "t")

    for x in (18, 22, 26):
        tooth_down(x)
    for x in (20, 24, 28):
        tooth_up(x)

    # ---- envelope, clamped in the jaws --------------------------------------
    EX0, EY0, EX1, EY1 = 29, 20, 46, 32
    rect(g, EX0, EY0, EX1, EY1, "e")
    rect(g, EX0, EY0, EX1, EY0, "o")
    rect(g, EX0, EY1, EX1, EY1, "o")
    rect(g, EX1, EY0, EX1, EY1, "o")
    # the flap: two pixels thick, from both top corners down to a point
    for y, x in enumerate(range(30, 37), start=21):
        rect(g, x, y, x + 1, y, "r")
        rect(g, 74 - x, y, 75 - x, y, "r")      # mirror about x = 37.5

    # the jaws close over it: chunks bitten out of the leading corners, and
    # the teeth that took them drawn on top. This is the "being eaten" read.
    stamp(g, 29, 20, ["###", "###", "##."], "p")
    stamp(g, 29, 30, ["##.", "###", "###"], "p")
    tooth_down(29)
    tooth_up(31)

    # ---- power LED on the lower bezel ---------------------------------------
    rect(g, 9, 35, 13, 36, "r")

    return rows(g)


# ------------------------------------------------------- the 16x16 mark ---
def mark16():
    """Favicon mark: silhouette, two angry eyes, a grin, one red LED.

    The teeth, the envelope and the prompt chevron are deliberately absent -
    at 16 device pixels they collapse into noise and take the face with them.
    """
    g = grid(16, 16)
    rrect(g, 1, 1, 14, 10, "c", cut=1)          # case
    rect(g, 6, 11, 9, 11, "c")                  # neck
    rrect(g, 3, 12, 12, 13, "c", cut=0)         # base
    rrect(g, 3, 3, 12, 8, "s", cut=1)           # screen
    keyline(g)

    stamp(g, 4, 4, ["##.", "###"], "t")          # eyes; the missing inner
    stamp(g, 9, 4, [".##", "###"], "t")          # corner is the angry brow
    rect(g, 6, 7, 9, 7, "t")                     # grin
    rect(g, 3, 9, 5, 9, "r")                     # power LED
    return rows(g)


# ------------------------------------------------------------------ SVG ---
def svg_rects(rws, dx=0, dy=0, pal=PAL):
    out = []
    w = len(rws[0])
    for y, row in enumerate(rws):
        x = 0
        while x < w:
            c = row[x]
            if c == ".":
                x += 1
                continue
            n = 1
            while x + n < w and row[x + n] == c:
                n += 1
            out.append(
                f'<rect x="{x + dx}" y="{y + dy}" width="{n}" height="1" '
                f'fill="{pal[c]}"/>'
            )
            x += n
    return out


def svg_doc(w, h, body, title, desc):
    return (
        f'<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 {w} {h}" '
        f'width="{w}" height="{h}" role="img" aria-labelledby="t d" '
        f'shape-rendering="crispEdges">\n'
        f"<title id=\"t\">{title}</title>\n<desc id=\"d\">{desc}</desc>\n"
        + "\n".join(body)
        + "\n</svg>\n"
    )


DESC = ("A cream CRT monitor with an angry face on its dark screen, biting "
        "an envelope in half.")
DESC_SM = "A cream CRT monitor with two angry eyes on its dark screen."


def badge_field(w, h, cut):
    """Rounded dark field, drawn on the same pixel grid as everything else."""
    g = grid(w, h)
    rrect(g, 0, 0, w - 1, h - 1, "s", cut=cut)
    return svg_rects(rows(g))


# ------------------------------------------------------------- wordmark ---
class Wordmark:
    """Silkscreen Bold, converted to outlines so no consumer needs the font."""

    def __init__(self, path):
        from fontTools.ttLib import TTFont
        from fontTools.pens.svgPathPen import SVGPathPen

        self.font = TTFont(path)
        self.upem = self.font["head"].unitsPerEm
        self.cmap = self.font.getBestCmap()
        self.gs = self.font.getGlyphSet()
        self.SVGPathPen = SVGPathPen

    def advance(self, ch):
        name = self.cmap[ord(ch)]
        return self.font["hmtx"][name][0]

    def paths(self, text, size, x, y, fill):
        """`y` is the baseline. `size` must be a multiple of 8 to stay crisp."""
        out = []
        pen_x = 0.0
        k = size / self.upem
        for ch in text:
            name = self.cmap[ord(ch)]
            pen = self.SVGPathPen(self.gs)
            self.gs[name].draw(pen)
            d = pen.getCommands()
            if d:
                out.append(
                    f'<path d="{d}" fill="{fill}" '
                    f'transform="translate({x + pen_x * k:g} {y}) '
                    f'scale({k:g} {-k:g})"/>'
                )
            pen_x += self.advance(ch)
        return out, pen_x * k


# --------------------------------------------------------------- outputs ---
def write(name, text):
    p = os.path.join(OUT, name)
    with open(p, "w") as f:
        f.write(text)
    return p


def png(src, dst, w, h=None):
    subprocess.run(
        ["rsvg-convert", "-w", str(w), "-h", str(h or w),
         os.path.join(OUT, src), "-o", os.path.join(OUT, dst)],
        check=True,
    )


def png_padded(src, dst, target, multiple, grid_size, bg):
    """Render at an exact integer multiple, then pad to `target` on `bg`.

    Bitmap art only stays crisp at whole-number scales, so a 180px icon is a
    144px mark on a 180px field rather than a 3.75x smear.
    """
    inner = grid_size * multiple
    tmp = os.path.join(OUT, ".tmp.png")
    subprocess.run(
        ["rsvg-convert", "-w", str(inner), "-h", str(inner),
         os.path.join(OUT, src), "-o", tmp], check=True)
    subprocess.run(
        ["magick", tmp, "-background", bg, "-gravity", "center",
         "-extent", f"{target}x{target}", os.path.join(OUT, dst)], check=True)
    os.remove(tmp)


def main():
    os.makedirs(OUT, exist_ok=True)
    m48 = mark48()
    m16 = mark16()

    # 1. free marks (transparent) - for dark surfaces --------------------------
    write("mark.svg", svg_doc(48, 48, svg_rects(m48), "mail-muncher", DESC))
    write("mark-small.svg",
          svg_doc(16, 16, svg_rects(m16), "mail-muncher", DESC_SM))

    # 2. badge marks - a dark field so the cream case survives light pages -----
    write("mark-badge.svg",
          svg_doc(64, 64, badge_field(64, 64, 4) + svg_rects(m48, 8, 8),
                  "mail-muncher", DESC))
    write("mark-small-badge.svg",
          svg_doc(16, 16, badge_field(16, 16, 1) + svg_rects(m16),
                  "mail-muncher", DESC_SM))

    # 3. lockups: small mark + Silkscreen Bold wordmark ------------------------
    wm = Wordmark(FONT)
    for name, fill, field in (("lockup.svg", INK, True),
                              ("lockup-dark.svg", CREAM, False)):
        body = badge_field(16, 16, 1) if field else []
        body += svg_rects(m16)
        # font-size 8 units => one font pixel is one mark pixel
        paths, tw = wm.paths("mail-muncher", 8, 20, 12, fill)
        write(name, svg_doc(int(20 + tw + 1), 16, body + paths,
                            "mail-muncher", DESC_SM))

    # 4. rasters ---------------------------------------------------------------
    png("mark-badge.svg", "mark-512.png", 512)          # 64 * 8
    png("mark.svg", "mark-dark-480.png", 480)           # 48 * 10
    png_padded("mark-badge.svg", "apple-touch-icon.png", 180, 2, 64, FIELD)
    png("mark-small-badge.svg", "favicon-16.png", 16)
    png("mark-small-badge.svg", "favicon-32.png", 32)
    png("mark-small-badge.svg", "favicon-48.png", 48)
    # 4x the 16-unit lockup grid; a PNG fallback for anywhere SVG is awkward.
    png("lockup.svg", "lockup-408.png", 408, 64)
    png("lockup-dark.svg", "lockup-dark-408.png", 408, 64)

    # 5. social preview --------------------------------------------------------
    body = [
        f'<rect width="1280" height="640" fill="{FIELD}"/>',
        '<g transform="translate(96 112) scale(9)">'
        + "".join(svg_rects(m48)) + "</g>",
    ]
    body += wm.paths("mail-muncher", 64, 576, 288, CREAM)[0]
    body += wm.paths("an email client for AI agents", 24, 576, 344, MUTED_D)[0]
    body.append(f'<rect x="576" y="376" width="192" height="8" fill="{ACCENT}"/>')
    body += wm.paths("gmail.readonly — rules — files on disk",
                     16, 576, 432, MUTED_D)[0]
    write("social-preview.svg",
          svg_doc(1280, 640, body, "mail-muncher",
                  "mail-muncher — an email client for AI agents."))
    png("social-preview.svg", "social-preview.png", 1280, 640)

    print("wrote", OUT)


if __name__ == "__main__":
    main()
