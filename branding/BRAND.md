# mail-muncher brand

mail-muncher is an email client for AI agents: it reads mail through one of two
provider seams, evaluates user-written rules, and archives the matches as files.
The identity turns that literal action into a character: a warm, chunky archive
beast that bites an envelope while moving forward. It is terminal-native and
playful without making the read-only and security claims feel casual.

The voice is **retro-terminal, warm, precise**. It refuses corporate SaaS
language, neon-cyberpunk decoration, empty superlatives, and jokes that obscure
a failure or security boundary.

## Name treatment

The product name is always lowercase and hyphenated: **mail-muncher**. Use that
form in prose, CLI output, documentation, the site, and the wordmark. Sentence
position does not change the casing. The executable and repository use the same
spelling.

## Interface palette

The UI, site, wordmark, and social card use these six roles. The mascot has its
own closed eight-colour image palette below; the two systems meet at the warm
red family but are not interchangeable.

| Role | Light | Dark | Use |
| --- | --- | --- | --- |
| Accent | `#E0533D` | `#F2765F` | graphical emphasis; links on dark |
| Accent deep | `#B33E2B` | `#E0533D` | links on light; pressed state |
| Ink | `#1F1A17` | `#F4EFEA` | body text and wordmark |
| Paper | `#FBF8F5` | `#17130F` | page background |
| Muted | `#6B625C` | `#A79C93` | secondary text and borders |
| Surface | `#F1EBE4` | `#221C18` | code, cards, table stripes |

`#E0533D` predates this identity in `katra/config.yml`; the palette extends that
project colour rather than importing a look from another fleet project.

### Measured text contrast

Ratios use the WCAG 2.1 relative-luminance formula. Body text needs 4.5:1;
large text and non-text UI boundaries need 3:1.

| Scheme | Pair | Ratio | Shipped use |
| --- | --- | ---: | --- |
| Light | Ink on Paper | 16.29:1 | body text, AAA |
| Light | Ink on Surface | 14.56:1 | code and tables, AAA |
| Light | Muted on Paper | 5.63:1 | secondary text, AA |
| Light | Muted on Surface | 5.03:1 | secondary text on cards, AA |
| Light | Accent deep on Paper | 5.45:1 | links and button labels, AA |
| Light | Accent deep on Surface | 4.87:1 | links in surfaces, AA |
| Light | Accent on Paper | 3.63:1 | graphics only; never body text |
| Dark | Ink on Paper | 16.17:1 | body text, AAA |
| Dark | Ink on Surface | 14.74:1 | code and tables, AAA |
| Dark | Muted on Paper | 6.88:1 | secondary text, AA |
| Dark | Muted on Surface | 6.27:1 | secondary text on cards, AA |
| Dark | Accent on Paper | 6.63:1 | links and button labels, AA |
| Dark | Accent on Surface | 6.04:1 | links in surfaces, AA |
| Dark | Accent deep on Paper | 4.82:1 | hover and pressed states, AA |

The light-scheme trap is deliberate and documented: Accent on Paper fails the
4.5:1 body-text threshold, so `$link-color` uses Accent deep on light pages.

## The archive beast

The mark is an original squat, four-footed grid critter in three-quarter
profile. It has a low red body, uneven antenna nubs, one square eye, an
asymmetrical mail-slot jaw, and a cream envelope halfway through the bite. The
rearward tail and forward feet imply movement; the envelope makes the product
verb legible without a caption.

The behavioral seed is the old **Number Munchers** loop—move through a grid and
consume the target—and nothing else. The shipped character does not borrow a
MECC character's likeness, name, body plan, face, costume, silhouette, palette,
classroom board, numeric tiles, interface, or trade dress. It is not a frog,
screen, arcade cabinet, or transcription of an existing reference. No MECC
artwork was supplied to the generator or used during the vector review.

### Closed mascot palette

GetVect resolved the generated raster into eight layers. These are image
colours, not text tokens.

| Value | Role |
| --- | --- |
| `#19100D` | deepest mouth and eye ink |
| `#482716` | outer keyline and envelope edge |
| `#B33B27` | body shadow |
| `#D84930` | dominant brick-red body |
| `#F05F43` | lit red planes |
| `#BD905C` | deep tan blocks |
| `#DAB483` | light tan blocks |
| `#F4E0BE` | envelope, eye, and highlights |

The relationships that carry the drawing were measured too:

| Pair | Ratio | Why it matters |
| --- | ---: | --- |
| keyline `#482716` on light Paper | 12.58:1 | holds the light-surface silhouette |
| body `#D84930` on dark Paper | 4.33:1 | body itself holds the dark-surface silhouette |
| body on light Paper | 4.04:1 | exceeds the 3:1 non-text boundary floor |
| keyline on body | 3.12:1 | the block outline survives inside the figure |
| envelope cream on body | 3.31:1 | mail stays distinct from the critter |
| keyline on envelope cream | 10.30:1 | the envelope reads from its own interior |
| envelope cream on dark Paper | 14.30:1 | the bite stays visible in dark mode |

The same reviewed palette therefore works on light and dark surfaces. The
`-dark` files remain explicit so README and site wiring can select by scheme,
but they do not maintain a second drawing or a speculative recolour.

## Provenance and conversion receipt

The source chain is deliberately committed and inspectable:

1. `branding/source/archive-beast-prompt.txt` is the exact clean-room prompt.
   OpenAI's built-in image-generation tool created the raster on 2026-08-21
   with no input or reference images. It returned transparent RGBA directly;
   no chroma-key removal or hand repainting was required.
2. `branding/source/archive-beast-generated.png` is that unmodified generated
   source: **1254 × 1254, 639,365 bytes, RGBA**.
3. The local GetVect 0.1.1 engine converted it offline. The raw trace was
   **1254 × 1254, 52,540 bytes**. The engine reported **8 colour layers, 8
   compound path shapes, and 110 subpaths**, with no backdrop and no canvas
   overflow. The machine-readable receipt is
   `branding/source/archive-beast-getvect.json`.
4. Human review accepted the silhouette, eye, four-footed stance, antennae,
   jaw/envelope overlap, grid edges, transparent canvas, and all eight layers.
   The SVG was checked for scripts, images, external references, `url()` loads,
   and overflow; none exist. The only edit was accessible title/description
   metadata. `branding/source/archive-beast-master.svg` is the reviewed master:
   **1254 × 1254, 52,775 bytes**.

### GetVect settings

| Setting | Value | Setting | Value |
| --- | --- | --- | --- |
| preset | `clipart` | detail level | `maximum` |
| colours | `8` | detail | `100` |
| smoothing | `0` | despeckle | `20` |
| noise reduction | `low` | anti-aliasing | `smart` |
| enhance | `false` | palette | automatic |
| roundness | `0` | minimum area | `90 px²` |
| overlap | `high` | circle detection | `false` |
| result style | `filled` | merge threshold | `0` |
| disabled colours | none | sort order | `coverage` |

The high-detail, zero-smoothing combination preserves the deliberately blocky
edge language. Smart anti-aliasing and low noise reduction collapse generated
edge halos without inventing a soft illustration style.

## Mark and lockup use

The full mark uses the complete 1254-square master and is for **48px and up**.
At favicon scale the whole body becomes a red fleck, so `mark-small.svg` uses a
650-square crop from `(590,145)` of the same master: head, eye, antennae, jaw,
and enough envelope to keep the bite. It is a crop, not a separately drawn or
redesigned character.

Use the small mark below 48px and the full mark at 48px or larger. Both SVGs
are transparent. `apple-touch-icon.png` is the only opaque mark because iOS
composites touch icons unpredictably; it uses light Paper.

The lockup places the small head-and-envelope crop beside the outlined
Silkscreen wordmark. Scale lockups uniformly. Never stretch the wordmark or
detach the crop and claim it is a second mascot.

### Render review at every documented size

All shipped raster sizes were regenerated from the reviewed master and visually
inspected on 2026-08-21.

| Output | Pixels | Bytes | Review |
| --- | ---: | ---: | --- |
| `favicon-16.png` | 16 × 16 | 851 | eye, jaw, and envelope separate |
| `favicon-32.png` | 32 × 32 | 2,138 | antennae and bite read clearly |
| `favicon-48.png` | 48 × 48 | 3,467 | crop remains crisp; no clipped envelope |
| `apple-touch-icon.png` | 180 × 180 | 14,112 | full stance reads on opaque Paper |
| `mark-480.png` | 480 × 480 | 38,248 | all feet, tail, and envelope retained |
| `mark-dark-480.png` | 480 × 480 | 38,248 | silhouette and bite hold on dark Paper |
| `lockup-408.png` | 408 × 64 | 5,558 | mark and dark wordmark balance on light |
| `lockup-dark-408.png` | 408 × 64 | 5,563 | cream wordmark balances on dark |
| `social-preview.png` | 1280 × 640 | 38,866 | full mark, headline, subtitle, and rule fit |

## Wordmark and typography

The wordmark uses **Silkscreen Bold** by Jason Kottke under the SIL Open Font
License 1.1. The TTF source and OFL live in `branding/fonts/`; the WOFF2 and a
second adjacent OFL live in `docs/assets/fonts/` for the site. SVG lockups turn
the glyphs into outlines so consumers do not need the font.

Silkscreen is an 8px-grid face. Set it only at whole multiples of 8px: 16, 24,
32, or 48. It has no lowercase forms, so `mail-muncher` and `MAIL-MUNCHER`
render identically; never use it where case distinguishes a config key or tool
name. Use it for the wordmark and `h1`/`h2`, not body text, nav lists, tables,
or code. Its glyph set includes the punctuation used by the site but not `→`,
so avoid arrows in display headings.

## Files and build

| File | Purpose |
| --- | --- |
| `branding/source/archive-beast-prompt.txt` | exact generation prompt and provenance |
| `branding/source/archive-beast-generated.png` | unmodified generated raster source |
| `branding/source/archive-beast-getvect.json` | GetVect settings and measured output |
| `branding/source/archive-beast-master.svg` | human-reviewed vector master |
| `branding/build.py` | validates the master and regenerates all shipped assets |
| `docs/assets/brand/mark.svg` / `mark-dark.svg` | full transparent mark variants |
| `docs/assets/brand/mark-small.svg` / `mark-small-dark.svg` | favicon crop variants |
| `docs/assets/brand/lockup.svg` / `lockup-dark.svg` | mark plus outlined wordmark |
| `docs/assets/brand/mark-480.png` / `mark-dark-480.png` | full-mark raster fallbacks |
| `docs/assets/brand/favicon-16.png`, `favicon-32.png`, `favicon-48.png` | browser icons |
| `docs/assets/brand/apple-touch-icon.png` | opaque 180px touch icon |
| `docs/assets/brand/lockup-408.png` / `lockup-dark-408.png` | lockup raster fallbacks |
| `docs/assets/brand/social-preview.svg` / `.png` | 1280 × 640 GitHub/OG card |

Regenerate from the committed master:

```bash
python3 branding/build.py
```

The build requires `rsvg-convert`, ImageMagick, and fonttools. It refuses a
master with active/external SVG content, the wrong viewBox, or geometry that no
longer matches the reviewed eight-layer/eight-path receipt.

## On the docs site

The light palette is in `docs/_sass/color_schemes/mail-muncher.scss`; display
type, logo sizing, and the full dark override are in
`docs/_sass/custom/custom.scss`; `docs/_sass/custom/setup.scss` retints theme
callouts. `docs/_includes/head_custom.html` carries the favicons, theme colour,
and Open Graph metadata.

Both schemes ship in one stylesheet under `prefers-color-scheme`, avoiding a
theme swap and flash. The explicit dark overrides correspond to
just-the-docs 0.12.0; re-enumerate palette-bearing declarations if that version
changes.

## What not to do

- Do not redraw the archive beast from a Number Munchers or MECC screenshot.
- Do not add a frog body, numeric tile, classroom grid, game-board frame,
  costume, face, palette, name treatment, or other borrowed trade dress.
- Do not describe the mark as a Number Munchers character. Only the
  move-and-consume behavior inspired the concept.
- Do not edit derived SVGs or PNGs by hand. Review and edit the committed master,
  then run `branding/build.py`.
- Do not discard or overwrite the generated raster, prompt, or GetVect receipt;
  they are the provenance chain.
- Do not use the full figure below 48px. Use the small crop.
- Do not place a decorative badge or container behind the transparent marks.
- Do not resample a committed PNG to make a new size; render from the master.
- Do not use Accent `#E0533D` as body text on light Paper.
- Do not invent interface or mascot colours outside the two closed palettes.
- Do not set Silkscreen at fractional or non-multiple-of-8 sizes, and do not use
  it for case-sensitive identifiers.
- Do not stretch, skew, rotate, add gradients, add shadows, or mirror the mark.

## Credits

- Silkscreen by Jason Kottke, distributed under SIL Open Font License 1.1.
- Raster-to-vector conversion by GetVect 0.1.1, run locally and offline.
- Syntax highlighting uses the accessible-pygments themes bundled with
  just-the-docs; those syntax-token colours sit outside the interface palette
  because preserving code distinctions is the higher-order requirement.
