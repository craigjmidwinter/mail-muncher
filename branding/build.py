#!/usr/bin/env python3
"""Generate every shipped mail-muncher brand asset from reviewed masters.

The mascot's human-reviewed GetVect SVG lives in
``branding/source/archive-beast-master.svg``. This script validates that master,
publishes it for the README/docs site, composes the wordmark lockups and social
card, and rasterizes every documented PNG size.

    python3 branding/build.py

Requires ``rsvg-convert`` (librsvg), ImageMagick, and fonttools.
"""

from __future__ import annotations

import re
import subprocess
import tempfile
from pathlib import Path


ROOT = Path(__file__).resolve().parent.parent
OUT = ROOT / "docs" / "assets" / "brand"
MASTER = ROOT / "branding" / "source" / "archive-beast-master.svg"
FONT = ROOT / "branding" / "fonts" / "Silkscreen-Bold.ttf"

FIELD = "#17130F"
PAPER = "#FBF8F5"
INK = "#1F1A17"
CREAM = "#F4EFEA"
MUTED_D = "#A79C93"
ACCENT = "#E0533D"

MASTER_SIZE = 1254
# The full figure becomes a red fleck at favicon size. This square is a crop of
# the same reviewed master, not a separately drawn character: head, eye, jaw,
# antennae, and enough envelope to retain the product story at 16px.
SMALL_X = 590
SMALL_Y = 145
SMALL_SIZE = 650

TITLE = "mail-muncher archive beast"
DESC = "A chunky red four-footed grid critter bites a cream envelope."
DESC_SMALL = "A chunky red grid critter bites a cream envelope."


def read_master() -> str:
    svg = MASTER.read_text()
    if 'viewBox="0 0 1254 1254"' not in svg:
        raise ValueError(f"{MASTER}: expected a 1254 x 1254 viewBox")
    forbidden = re.findall(
        r"<(?:script|image|foreignObject)\b|\bhref\s*=|\burl\(", svg, re.I
    )
    if forbidden:
        raise ValueError(f"{MASTER}: forbidden external/active SVG content")
    if len(re.findall(r"<g\b", svg)) != 8 or len(re.findall(r"<path\b", svg)) != 8:
        raise ValueError(f"{MASTER}: reviewed geometry changed; want 8 layers / 8 paths")
    match = re.search(r"<svg\b[^>]*>(.*)</svg>\s*$", svg, re.S)
    if not match:
        raise ValueError(f"{MASTER}: cannot find SVG body")
    inner = re.sub(
        r"<(?:title|desc)\b[^>]*>.*?</(?:title|desc)>",
        "",
        match.group(1),
        flags=re.S,
    )
    return inner.strip()


def svg_doc(width: int, height: int, view_box: str, body: str, desc: str = DESC) -> str:
    return (
        '<svg xmlns="http://www.w3.org/2000/svg" '
        f'width="{width}" height="{height}" viewBox="{view_box}" '
        'role="img" aria-labelledby="t d">\n'
        f'<title id="t">{TITLE}</title>\n'
        f'<desc id="d">{desc}</desc>\n'
        f"{body}\n</svg>\n"
    )


class Wordmark:
    """Silkscreen Bold converted to outlines for portable SVG lockups."""

    def __init__(self, path: Path):
        from fontTools.pens.svgPathPen import SVGPathPen
        from fontTools.ttLib import TTFont

        self.font = TTFont(path)
        self.upem = self.font["head"].unitsPerEm
        self.cmap = self.font.getBestCmap()
        self.glyphs = self.font.getGlyphSet()
        self.pen_type = SVGPathPen

    def paths(
        self, text: str, size: int, x: float, y: float, fill: str
    ) -> tuple[list[str], float]:
        out: list[str] = []
        pen_x = 0.0
        scale = size / self.upem
        for char in text:
            name = self.cmap[ord(char)]
            pen = self.pen_type(self.glyphs)
            self.glyphs[name].draw(pen)
            commands = pen.getCommands()
            if commands:
                out.append(
                    f'<path d="{commands}" fill="{fill}" '
                    f'transform="translate({x + pen_x * scale:g} {y}) '
                    f'scale({scale:g} {-scale:g})"/>'
                )
            pen_x += self.font["hmtx"][name][0]
        return out, pen_x * scale


def write(out: Path, name: str, content: str) -> Path:
    path = out / name
    path.write_text(content)
    return path


def rasterize(out: Path, source: str, target: str, width: int, height: int | None = None) -> None:
    subprocess.run(
        [
            "rsvg-convert",
            "-w",
            str(width),
            "-h",
            str(height or width),
            str(out / source),
            "-o",
            str(out / target),
        ],
        check=True,
    )


def opaque_icon(out: Path) -> None:
    with tempfile.NamedTemporaryFile(suffix=".png") as tmp:
        subprocess.run(
            [
                "rsvg-convert",
                "-w",
                "180",
                "-h",
                "180",
                str(out / "mark.svg"),
                "-o",
                tmp.name,
            ],
            check=True,
        )
        subprocess.run(
            [
                "magick",
                tmp.name,
                "-background",
                PAPER,
                "-alpha",
                "remove",
                "-alpha",
                "off",
                "-strip",
                str(out / "apple-touch-icon.png"),
            ],
            check=True,
        )


def build(out: Path) -> None:
    out.mkdir(parents=True, exist_ok=True)
    mascot = read_master()

    full = svg_doc(MASTER_SIZE, MASTER_SIZE, f"0 0 {MASTER_SIZE} {MASTER_SIZE}", mascot)
    small = svg_doc(
        16,
        16,
        f"{SMALL_X} {SMALL_Y} {SMALL_SIZE} {SMALL_SIZE}",
        mascot,
        DESC_SMALL,
    )
    # The reviewed palette works on both surfaces: dark brown holds the edge on
    # paper; on the dark scheme the brick-red body and cream envelope carry the
    # silhouette. Separate filenames keep the README and site dark-mode wiring
    # explicit without maintaining a second drawing.
    for name, content in (
        ("mark.svg", full),
        ("mark-dark.svg", full),
        ("mark-small.svg", small),
        ("mark-small-dark.svg", small),
    ):
        write(out, name, content)

    wordmark = Wordmark(FONT)
    crop_scale = 16 / SMALL_SIZE
    crop_group = (
        f'<g transform="matrix({crop_scale:g} 0 0 {crop_scale:g} '
        f'{-SMALL_X * crop_scale:g} {-SMALL_Y * crop_scale:g})">{mascot}</g>'
    )
    for name, fill in (("lockup.svg", INK), ("lockup-dark.svg", CREAM)):
        paths, text_width = wordmark.paths("mail-muncher", 8, 20, 12, fill)
        body = crop_group + "\n" + "\n".join(paths)
        width = int(21 + text_width)
        write(out, name, svg_doc(width, 16, f"0 0 {width} 16", body, DESC_SMALL))

    rasterize(out, "mark.svg", "mark-480.png", 480)
    rasterize(out, "mark-dark.svg", "mark-dark-480.png", 480)
    for size in (16, 32, 48):
        rasterize(out, "mark-small.svg", f"favicon-{size}.png", size)
    opaque_icon(out)
    rasterize(out, "lockup.svg", "lockup-408.png", 408, 64)
    rasterize(out, "lockup-dark.svg", "lockup-dark-408.png", 408, 64)

    full_scale = 480 / MASTER_SIZE
    social_body = [
        f'<rect width="1280" height="640" fill="{FIELD}"/>',
        f'<g transform="matrix({full_scale:g} 0 0 {full_scale:g} 72 80)">{mascot}</g>',
    ]
    social_body += wordmark.paths("mail-muncher", 64, 576, 288, CREAM)[0]
    social_body += wordmark.paths("an email client for AI agents", 24, 576, 344, MUTED_D)[0]
    social_body.append(f'<rect x="576" y="376" width="192" height="8" fill="{ACCENT}"/>')
    social_body += wordmark.paths(
        "gmail.readonly — rules — files on disk", 16, 576, 432, MUTED_D
    )[0]
    write(
        out,
        "social-preview.svg",
        svg_doc(
            1280,
            640,
            "0 0 1280 640",
            "\n".join(social_body),
            "mail-muncher — an email client for AI agents.",
        ),
    )
    rasterize(out, "social-preview.svg", "social-preview.png", 1280, 640)


def main() -> None:
    build(OUT)
    print(f"wrote {OUT}")


if __name__ == "__main__":
    main()
