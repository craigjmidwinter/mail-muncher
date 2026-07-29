# mail-muncher brand

mail-muncher is an email client for AI agents: it pulls Gmail with a read-only
scope, filters it with composable rules, and drops the matches on disk as files.
It is a small, sharp, terminal-native tool, and the identity should read that
way — retro-terminal, warm, precise. Not corporate SaaS, not neon cyberpunk.

Everything here is authored on an exact pixel grid. The masters are SVG built
from `<rect>` elements with `shape-rendering="crispEdges"`; they scale
losslessly, stay under 30 KB, and diff in git.

---

## Palette

Eleven values. Nothing outside this table appears in the mark, the site, or any
asset in this repo.

| Role | Light | Dark | Use |
| --- | --- | --- | --- |
| Accent | `#E0533D` | `#F2765F` | Links (dark only), logo mark, active nav, the one thing that pops |
| Accent deep | `#B33E2B` | `#E0533D` | Links (light only), hover/pressed, headings that need weight |
| Ink | `#1F1A17` | `#F4EFEA` | Body text |
| Paper | `#FBF8F5` | `#17130F` | Background — warm, never pure white or black |
| Muted | `#6B625C` | `#A79C93` | Secondary text, borders, code comments |
| Surface | `#F1EBE4` | `#221C18` | Code blocks, cards, table stripes, sidebar |

`#E0533D` was already the accent in `katra/config.yml`; this palette extends
existing project identity rather than replacing it.

### Contrast, measured

Every foreground/background pair that actually ships, computed with the WCAG 2.1
relative-luminance formula. AA needs **4.5:1** for body-size text, **3:1** for
large text (≥ 24px, or ≥ 19px bold) and for non-text UI components.

**Light scheme**

| Ratio | Pair | Where | Verdict |
| --- | --- | --- | --- |
| 16.29:1 | Ink `#1F1A17` on Paper `#FBF8F5` | body text | AAA |
| 14.56:1 | Ink on Surface `#F1EBE4` | code blocks, sidebar, table stripes | AAA |
| 5.63:1 | Muted `#6B625C` on Paper | secondary text, search previews | AA |
| 5.03:1 | Muted on Surface | secondary text on cards | AA |
| **5.45:1** | **Accent deep `#B33E2B` on Paper** | **link text** | **AA** |
| 4.87:1 | Accent deep on Surface | links inside code and sidebar | AA |
| 5.45:1 | Paper on Accent deep | primary button label | AA |
| **3.63:1** | **Accent `#E0533D` on Paper** | **never used for text** | **fails AA** |
| 3.24:1 | Accent on Surface | never used for text | fails AA |

The plain Accent on Paper is the trap. **3.63:1 passes for large text and for UI
component boundaries and fails AA for body-size text**, so on light backgrounds
the Accent is never a text colour — `$link-color` takes Accent deep instead. The
Accent still appears on light pages as a graphical element (the rule under the
social-card wordmark, the logo mark on its dark field), where 3:1 is the bar.

**Dark scheme**

| Ratio | Pair | Where | Verdict |
| --- | --- | --- | --- |
| 16.17:1 | Ink `#F4EFEA` on Paper `#17130F` | body text | AAA |
| 14.74:1 | Ink on Surface `#221C18` | code blocks, sidebar, tables | AAA |
| 6.88:1 | Muted `#A79C93` on Paper | secondary text | AA |
| 6.27:1 | Muted on Surface | secondary text on cards | AA |
| **6.63:1** | **Accent `#F2765F` on Paper** | **link text** | **AA** |
| 6.04:1 | Accent on Surface | links inside code and sidebar | AA |
| 6.63:1 | Paper on Accent | primary button label | AA |
| 4.82:1 | Accent deep `#E0533D` on Paper | hover and pressed states | AA |

The relationship inverts: on dark, the Accent is the readable one and Accent
deep is the emphasis. That is why `$link-color` differs between the two schemes.

**Inside the mark** (all measured against the dark badge field `#17130F`)

| Ratio | Pair | Note |
| --- | --- | --- |
| 15.61:1 | case cream `#F1EBE4` on field | the silhouette |
| 16.29:1 | teeth `#FBF8F5` on mouth interior `#1F1A17` | the face |
| 4.82:1 | Accent `#E0533D` on field | chevron, blush, LED, seams |
| 3.10:1 | keyline `#6B625C` on field | non-text boundary, 3:1 bar |
| **1.12:1** | **case cream on light Paper `#FBF8F5`** | **why the badge exists** |

That last row is the whole reason for the two logo variants below.

---

## The mark

A chunky cream CRT with an angry face on its dark screen — two brow-slanted
eyes, red blush, a red `>` prompt chevron — biting an envelope that is halfway
down its throat.

The concept came from a reference illustration. Three things were changed
deliberately in the redraw:

- **The prompt chevron is a real `>`, not an ambiguous shape.** It is the one
  element that ties the character to a CLI, so it is drawn as a clean
  three-pixel-thick caret rather than an arrow-like squiggle.
- **The pupils are fully enclosed in white.** The reference has an open notch,
  which at pixel scale reads as a lowercase `b` and `d`. Enclosing them keeps
  them eyes.
- **The envelope's leading corners are bitten off**, with the teeth that took
  them drawn on top and keylined so they do not merge into the cream envelope.
  In the reference the envelope reads as adjacent to the mouth; here it reads as
  being eaten, which was the point of the drawing.

### Grid: 48 × 48

32 × 32 was tried first and does not hold this concept. It was drawn, rendered
and looked at: at 32 the bezel collapses to one pixel, the teeth become
single-pixel spikes, and the eye — which now has a brow slope *and* an enclosed
pupil — cannot be built at all without reading as a letterform. The new concept
carries strictly more than the old one (a face, blush, a chevron, a toothed
mouth, an envelope with seams), and 48 is the smallest grid that gives the bezel
three pixels and every face feature two.

Going to 48 costs nothing at small sizes, because a 48-grid drawing was never
going to survive a 16px render anyway. That is what the second mark is for.

### Two marks

| | File | Grid | Use at |
| --- | --- | --- | --- |
| **Full mark** | `mark.svg` / `mark-badge.svg` | 48 × 48 | **48px and up** |
| **Small mark** | `mark-small.svg` / `mark-small-badge.svg` | 16 × 16 | **below 48px** |

The full mark at 16px is mush — the teeth, the envelope and the chevron all
collapse into noise and take the silhouette with them. This was rendered and
looked at, not assumed. The small mark drops the teeth, the envelope, the
chevron and the case shading, and keeps what still reads at one device pixel per
grid cell: the chunky CRT silhouette, two eyes with the brow slant built into
their shape, a grin, and one red LED as the single spot of accent.

Both are the same character. Do not scale the full mark down past 48px, and do
not scale the small mark up past 48px — at 64px and above its coarseness reads
as a mistake rather than as a decision.

### Two fields: free mark and badge

The case is cream. On the dark background it came from, that is the whole point.
On our light Paper it is **1.12:1** — invisible.

Three fixes were considered: outline the case in Ink and keep it cream; re-tint
the case to Muted so it becomes a grey monitor; or put the character on a dark
field. The badge won.

- **Outlining** leaves a big cream mass on a cream page. The mark becomes a line
  drawing with one dark blob (the screen) floating in it — a different object.
- **Re-tinting to grey** changes the character's complexion, and worse, it makes
  the dark screen fight a mid-grey case on a light page, which flattens the face.
  The face is the identity; anything that flattens it is the wrong trade.
- **The badge** keeps every colour relationship in the drawing exactly as
  designed, guarantees contrast on any surface, and reads as a terminal window —
  which reinforces what the tool is.

So:

| Surface | Use | File |
| --- | --- | --- |
| Light (or unknown) | **badge** — character on a rounded `#17130F` field | `mark-badge.svg`, `mark-small-badge.svg` |
| Dark, known | free mark — transparent background | `mark.svg`, `mark-small.svg` |

The badge is the safe default. When in doubt, use it.

---

## Wordmark and type

**Silkscreen Bold**, by Jason Kottke, under the SIL Open Font License 1.1.
`branding/fonts/OFL.txt` and `docs/assets/fonts/OFL.txt` carry the licence text
next to the font files it covers, as the OFL requires. The files are
redistributed unmodified; no derivative is made and no Reserved Font Name is
declared in the licence as shipped, so the naming clause is not engaged.

Silkscreen is a bitmap face drawn on an **8px grid**. Two consequences that are
not optional:

1. **Only set it at whole multiples of 8px.** 16, 24, 32, 48. At 18px or 1.125rem
   it goes soft, which defeats the point of pairing it with pixel art.
2. **It has no lowercase forms.** `mail-muncher` and `MAIL-MUNCHER` render
   identically. Every heading set in Silkscreen is capitals whether you type
   them that way or not.

Because of (2), a `text-transform` is never needed and code inside a heading is
given back the monospace stack — reference headings here are frequently a bare
config key or tool name, and those are case-sensitive identifiers that a
capitals-only face would misstate.

**Glyph coverage checked** (the font is 226 codepoints, so this matters): em
dash `—`, en dash `–`, curly quotes `‘ ’ “ ”`, middot `·`, bullet `•`, ellipsis
`…`, degree `°`, `© × ™ € & > ? / : \` -`, and the Latin-1 accented set are all
present. **Arrows (`→`, U+2192) are not**, so a heading containing one falls
through to the fallback stack mid-line. Avoid arrows in h1 and h2.

Where it may be used:

| Yes | No |
| --- | --- |
| the wordmark and lockups | body copy |
| the social card | `h3` and below |
| `h1`, `h2` | nav lists, tables, code blocks |
| the sidebar site title (via the lockup) | anything below 16px |

Body text keeps a normal system stack. Headings get letter-spacing (2px at 32px,
1px at 24px) and a whole-pixel line-height (40px and 32px) — pixel faces are set
solid by default and read as a wall without it.

---

## Files

Masters and derived rasters both live in `docs/assets/brand/`, because that is
the one directory the site can serve from. `branding/` holds this document, the
generator, and the font's build-time source.

| File | What |
| --- | --- |
| `docs/assets/brand/mark.svg` | full mark, 48 grid, transparent |
| `docs/assets/brand/mark-badge.svg` | full mark on the dark field, 64 viewBox |
| `docs/assets/brand/mark-small.svg` | small mark, 16 grid, transparent |
| `docs/assets/brand/mark-small-badge.svg` | small mark on the dark field |
| `docs/assets/brand/lockup.svg` | small mark + wordmark, for light surfaces |
| `docs/assets/brand/lockup-dark.svg` | same, cream wordmark, for dark surfaces |
| `docs/assets/brand/lockup-408.png` | lockup at 4x (408 x 64), for anywhere SVG is awkward |
| `docs/assets/brand/lockup-dark-408.png` | same, dark-surface variant |
| `docs/assets/brand/mark-512.png` | 512px badge (64 × 8) |
| `docs/assets/brand/mark-dark-480.png` | 480px free mark (48 × 10) |
| `docs/assets/brand/apple-touch-icon.png` | 180px; a 128px badge padded to 180 |
| `docs/assets/brand/favicon-16/32/48.png` | small mark at 1×, 2×, 3× |
| `docs/assets/brand/social-preview.png` | 1280 × 640 GitHub / OG card |
| `docs/assets/fonts/Silkscreen-Bold.woff2` | the webfont the site serves |
| `branding/build.py` | regenerates every one of the above |

Rasters are always rendered at a **whole multiple** of the grid and then padded
to the target size if the target is not a multiple. A 180px icon is a 128px mark
on a 180px field, not a 3.75× smear.

To regenerate:

```bash
python3 branding/build.py     # needs rsvg-convert, ImageMagick, fonttools
```

---

## On the docs site

| File | What |
| --- | --- |
| `docs/_sass/color_schemes/mail-muncher.scss` | the light scheme, selected by `color_scheme: mail-muncher` |
| `docs/_sass/custom/custom.scss` | the display face, the logo sizing, and the whole dark scheme |
| `docs/_sass/custom/setup.scss` | retints the theme's colour ramp so callouts stay on palette |
| `docs/_includes/head_custom.html` | favicons, `theme-color`, Open Graph image metadata |

Both schemes ship in **one** stylesheet. just-the-docs compiles one scheme per
file and switches by swapping the `<link>`, which flashes; instead the dark
values are a `prefers-color-scheme` block in the same file, so the browser
resolves them before first paint. No JavaScript, no toggle, no flash.

The dark block is explicit overrides rather than a re-import of the theme's
modules. That is not a style preference: GitHub Pages' Jekyll runs Ruby Sass,
which refuses a nested `@import` of any file carrying a root-only directive, and
just-the-docs' `content.scss` opens with `@charset`. The override list was
produced by compiling the light stylesheet and enumerating every declaration
that carries a palette colour — 66 rules. **If the theme is ever unpinned from
just-the-docs v0.12.0, re-run that enumeration**, or a new rule will be
light-only in dark mode.

One deliberate exception to the palette rule: the theme derives a few hover and
pressed states with `darken($link-color, 2%)` and similar, which produces values
that are not in the table. They are derivations of a palette colour rather than
new colours, and removing them would mean forking the theme.

---

## What not to do

- **Do not put the free mark on a light background.** Cream on Paper is 1.12:1.
  Use the badge.
- **Do not use Accent `#E0533D` as a text colour on a light background.** 3.63:1
  fails AA. Accent deep `#B33E2B` is the light-mode text accent.
- **Do not scale the full mark below 48px**, and do not scale the small mark
  above it.
- **Do not resample the PNGs.** Re-render from the SVG at a whole multiple.
- **Do not set Silkscreen at a fractional or non-multiple-of-8 size**, and never
  at body-copy size.
- **Do not modify the font files.** They are redistributed under the OFL as
  received.
- **Do not invent a colour.** Six roles, eleven values, and `rgba()` of one of
  them when a border needs to be quieter than Muted. Nothing else.
- **Do not add a drop shadow, a gradient, or an anti-aliased edge to the mark.**
  It is pixel art on an exact grid; every one of those breaks the grid.
- **Do not stretch the lockup.** Its aspect is fixed; scale it uniformly, and
  prefer whole multiples of its 16-unit height.

---

## Credits

- Silkscreen by Jason Kottke — <https://github.com/googlefonts/silkscreen> —
  SIL Open Font License 1.1.
- Syntax highlighting on the docs site uses
  [accessible-pygments](https://github.com/Quansight-Labs/accessible-pygments)
  (`github-light` / `github-dark`), which ships with just-the-docs. Its token
  colours are outside this palette by design — they are tuned for contrast
  against the code background, and reducing them to six hues would lose the
  distinctions that make highlighting worth having.
