# Tokens v0

Cha Dash's design tokens. A **theme is a versioned token document** (YAML) that the generator compiles to CSS custom properties, the same way the hub will. Components only ever use *semantic* tokens. See [02 · Experience → Theming](../../docs/plan/02-experience.md#theming).

```
design/tokens/
  themes/*.yaml              the six built-in theme documents
  schema/theme.schema.json   JSON Schema for theme documents (format 1)
  src/core.js                the generator core: pure, no imports, also runs in the browser
  scripts/generate.ts        themes → dist/tokens.css + dist/tokens.json
  scripts/inject.ts          inlines the core, tokens.css and themes into the prototype
  scripts/report.ts          prints the validator and contrast tables below
  dist/                      generated, committed
  test/                      bun test
```

| Command (run in `design/tokens`) | What it does |
|---|---|
| `bun install` | Dev dependencies: `typescript`, `@types/bun`, `ajv` (schema test), `apca-w3` (parity test). The core itself has none |
| `bun run generate` | Writes `dist/`. Refuses to write if a theme is invalid or fails an error-level check. `--check` exits 1 if `dist/` is stale |
| `bun run inject` | Copies the core, `tokens.css` and the theme documents into `design/prototypes/m0-v2.html` |
| `bun test` | 50 tests. Add `DATAVIZ_VALIDATOR=/path/to/validate_palette.js` to also cross-check against the dataviz palette validator |
| `bun run typecheck` | `tsc --noEmit` with `checkJs` over the core, scripts and tests |
| `bun run report` | Markdown tables of status-pair and text-contrast results |

**Hand-off:** `web/` should consume `dist/tokens.css` (06 · Next steps 1.3 names `web/src/design/tokens.css`) and can import `src/core.js` for runtime theme generation and validation.

---

## The theme document

```yaml
format: 1
id: instrument-dark                 # the data-theme value
name: Instrument Dark
scheme: dark                        # optional; must agree with the base lightness
profile: standard                   # standard | dim (Night)
monochrome: false                   # true: status is glyph + word only (Night)
color:
  base: oklch(0.175 0.006 70)       # app background; its hue and chroma tint every neutral
  accent: oklch(0.78 0.07 210)      # step 9 of the accent scale: progress, selection
  contrast: 50                      # 0–100; 50 is the baseline
  ink: { hue: 80, chroma: 0.012 }   # optional: the text end of the neutral scale
  status:
    attention: { hue: 75, chroma: 0.15 }       # lightness comes from the scheme unless `l` is set
    down: { hue: 15, chroma: 0.21 }
    maintenance: { hue: 250, chroma: 0.06 }
  overrides: {}                     # optional token-by-token overrides, still validated
typography: { ui: Instrument Sans, numeral: Geist Mono, clock: Doto, headline: Instrument Sans }
figures: tabular                    # tabular | proportional (big standalone numbers)
surface: card                       # card | ruled | flat
shape: { radius: 4, hairline: 1 }
density: comfortable                # comfortable | compact
briefing: sentence                  # sentence | headline
material: { frosted: 0.78 }         # chrome opacity; 1 = solid
backdrop: solid                     # solid | grain
```

Validation happens twice:
- the **JSON Schema** (for editors, the hub's Go side, and imports);
- `validateThemeDoc()` in the core, which adds cross-field rules (scheme vs. base lightness) and returns path-qualified messages such as `typography.ui: must be a bundled font: …`.

Fonts are limited to the bundled set: Instrument Sans, Inter, IBM Plex Sans, Schibsted Grotesk, Geist Mono, JetBrains Mono, Commit Mono, Doto, Newsreader.

| Theme | Scheme | Notes |
|---|---|---|
| **Instrument Dark** (default) | dark | Warm graphite, the M0 validated pair |
| Instrument Light | light | Warm page, white cards (cards are lighter than the page) |
| OLED | dark | True black; surfaces round to `#000` in 8-bit sRGB, so cards are hairlines on black |
| Night | dark, `dim`, monochrome | Red on black for walls after dark |
| High Contrast | dark | Contrast 100: text Lc 104/90/75, firm rules, solid chrome |
| Paper | light | Direction B as a theme: newsprint, ruled surface, Newsreader headline, proportional figures, grain |

## How generation works

**Inputs: base, accent, contrast** (the Linear model). Everything else derives from them.

**The neutral scale, steps 1–12** (Radix model):
- **Steps 1–10** sit at fixed lightness offsets from the base: 1 app background, 2 surface, 3–5 component states, 6–8 borders, 9–10 solid fills.
- The offsets are multiplied by `0.8 + 0.4 × contrast / 100`, so contrast widens or narrows the spread.
- Chroma and hue interpolate from the base to the `ink`.
- **Steps 11 and 12 are solved, not picked.** A binary search finds the lightness closest to the background that reaches **APCA Lc 60 / Lc 90 and WCAG 4.5:1** on *both* step 1 and step 2. They're guaranteed by construction.
- `--text-2` is solved the same way at Lc 75. It's a semantic token, not a scale step.
- Contrast above 50 raises the targets (up to Lc 104 / 90 / 75 at 100). Below 50 it only softens surfaces and hairlines; **text never drops below its floor**.

**Chromatic scales** (accent, attention, down, maintenance):
- Step 9 is the solid colour itself.
- Steps 1–8 reuse the neutral lightness with a rising share of the solid's chroma.
- Steps 11 and 12 are solved for text contrast, like the neutrals.

**Status lightness** defaults per scheme to the M0 validated values (dark: attention 0.74, down 0.64; light: 0.66, 0.55). It's then **snapped to passing**: hold the hue, move the lightness until the colour clears 3:1 on both surfaces, within ±0.1. Instrument Light's attention moved 0.66 → 0.64 this way.

**Gamut:** colours are mapped into sRGB by reducing chroma at constant L and H, then rounded to the printed precision. The hex we test is exactly what the browser renders.

**Motion:** springs use the SwiftUI model (duration + bounce; damping ratio = 1 − bounce). The generator samples the step response into a CSS `linear()` easing. The emitted duration is the **settle time** (displacement under 0.1%), which is longer than the perceptual duration:

| Token | Spring | CSS duration |
|---|---|---|
| `press` | 0.2 s · bounce 0 | 300 ms |
| `move` | 0.35 s · bounce 0 | 520 ms |
| `lift` | 0.3 s · bounce 0.15 | 420 ms (0.6% overshoot) |
| `sheet` | 0.45 s · bounce 0.08 | 480 ms |
| `value` | 0.6 s ease-out tween | 600 ms |
| `fade` | 0.15 s | 150 ms |

Script can use `spring(duration, bounce).state(t, v0)` for interruptible, velocity-aware springs (the prototype does).

## Token reference

**Global** (`:root`, theme-independent):
- type roles on a 1.125 modular scale over a 14 px body: `--type-micro` … `--type-clock`, `--type-wall`, `--type-wall-hero`;
- leading, tracking and weights;
- `--space-*` (4 pt scale);
- layout: `--gutter`, `--section-gap`, `--tile-pad-*`, `--unit-min/max`, `--board-max`, `--target-min`;
- `--motion-*-duration` and `--motion-*-easing`.

**Per theme** (`[data-theme="…"]`; every theme defines all 122):
- scales: `--neutral-1…12`, `--accent-1…12`, `--attention-1…12`, `--down-1…12`, `--maint-1…12`;
- semantic colours: `--bg`, `--surface`, `--surface-2`, `--surface-hover`, `--surface-active`, `--border`, `--border-strong`, `--line`, `--line-2`, `--text`, `--text-2`, `--text-3`, `--text-inverse`, `--accent*`, `--on-accent`, `--progress`, `--attention*`, `--on-attention`, `--down*`, `--on-down`, `--maint*`, `--focus`, `--spark`, `--band`, `--hatch`, `--scrim`;
- type: `--font-ui`, `--font-num`, `--font-clock`, `--font-head`, `--figures-display`;
- shape: `--radius-xs…xl`, `--radius-full`, `--hairline`;
- surface: `--tile-bg`, `--tile-border-*`, `--tile-radius`, `--tile-pad-inline`, `--section-rule-*`;
- material: `--glass-bg`, `--glass-blur`, `--backdrop`, `--shadow-lift`, `--shadow-overlay`.

**Variants:** `:root[data-density="compact"]`, `[data-surface="card|ruled|flat"]`, `[data-motion="reduced|none"]` and `[data-material="solid"]`. Plus media blocks for `prefers-reduced-motion`, `prefers-reduced-transparency` and, per theme, `prefers-contrast: more` (the same document at contrast +40, emitting only the tokens that change). Variant selectors are `:root[…]`, so they beat any theme block, including a live custom theme added later in the page.

**JSON** (`dist/tokens.json`): globals, variants, motion specs, and per theme its inputs, defaults (surface, density, briefing, backdrop), CSS tokens, sRGB hex for every opaque colour (for non-CSS consumers such as the hub), and the full check results.

## Using the core in the browser

ES module imports don't load from `file://` in Chromium, so the prototype can't `import` the core.
- `bun run inject` rewrites `export function x` to `function x`, wraps the file in an IIFE (`const ChaTokens = (() => { … })()`), and writes it between `/* @generated:core begin */` and `/* @generated:core end */` in `m0-v2.html`.
- `dist/tokens.css` and the theme documents are injected the same way.
- `test/inline.test.ts` fails if any inline block differs from its source. It also runs the inline copy in a VM and checks that it produces byte-identical CSS.

---

## Validator and contrast results

### Status pairs: the dataviz palette validator

Each theme's attention/down pair was run through `validate_palette.js` (the dataviz skill's validator, unmodified) against **both** the theme's background (step 1) and surface (step 2):

| Theme | Pair | Surfaces tested | Lightness band | Chroma | CVD separation (worst protan/deutan) | Normal-vision floor | Contrast vs surface |
|---|---|---|---|---|---|---|---|
| Instrument Dark | `#e19b1b` `#f04162` | `#12100e`, `#1a1714` | FAIL (L 0.74)* | PASS | PASS · ΔE 12.2 (deutan) · tritan 16.5 | PASS · 21.2 | PASS |
| Instrument Light | `#c17a00` `#cf1748` | `#f2f0eb`, `#fcfaf5` | PASS | PASS | PASS · ΔE 10.5 · tritan 11.8 | PASS · 19.3 | PASS |
| OLED | `#e19b1b` `#f04162` | `#000000` | FAIL (L 0.74)* | PASS | PASS · ΔE 12.2 · tritan 16.5 | PASS · 21.2 | PASS |
| High Contrast | `#ffbe3d` `#fb5668` | `#080706`, `#100f0e` | FAIL (L 0.84, 0.68)* | PASS | PASS · ΔE 17.5 · tritan 21.7 | PASS · 24.6 | PASS |
| Paper | `#be7c00` `#c5193d` | `#f9f5ee`, `#ffffff` | PASS | PASS | PASS · ΔE 11.8 · tritan 13.1 | PASS · 19.7 | PASS |
| Night | `#f14638` (both) | — | — | — | not applicable: monochrome | — | 5.67:1 |

\* The **lightness band** is a chart-series rule (dark band L 0.48–0.67). Dark-theme status colours sit above it on purpose, as recorded in 02 · Experience: they always ship with a glyph and a word. The core reports the band as `info`, never as a refusal. Every other check passes on every surface. Our own implementation reproduces the validator's numbers to 0.1 ΔE (`bun test` with `DATAVIZ_VALIDATOR` set), and it reproduces the M0 table: A dark 12.2 / 21.2.

**Our checks are stricter than the validator's in three ways:**
- **CVD includes tritan.** The worst of protan, deutan and tritan must be ≥ 8.
- **Progress can't read as status.** The accent must be ≥ 15 ΔE from both attention and down, or the theme is refused ("the accent looks too much like attention").
- **Contrast is checked on both surfaces**, plus the focus ring.

### Full status table

| Theme | Attention | Down | Protan ΔE | Deutan ΔE | Tritan ΔE | Normal ΔE | Attention on bg / surface | Down on bg / surface | Accent vs attention / down |
|---|---|---|---|---|---|---|---|---|---|
| Instrument Dark | `oklch(0.74 0.15 75)` | `oklch(0.64 0.21 15)` | 20.8 | 12.2 | 16.5 | 21.2 | 8.05 / 7.57 | 5.10 / 4.79 | 20.9 / 31.1 |
| Instrument Light | `oklch(0.64 0.1386 70)` | `oklch(0.55 0.21 15)` | 19.1 | 10.5 | 11.8 | 19.3 | 3.05 / 3.33 | 4.77 / 5.20 | 24.5 / 28.6 |
| OLED | `oklch(0.74 0.15 75)` | `oklch(0.64 0.21 15)` | 20.8 | 12.2 | 16.5 | 21.2 | 8.91 / 8.91 | 5.64 / 5.64 | 20.9 / 31.1 |
| Night (monochrome) | `oklch(0.64 0.21 29)` | same | — | — | — | — | 5.67 / 5.67 | 5.67 / 5.67 | — |
| High Contrast | `oklch(0.84 0.1551 80)` | `oklch(0.68 0.2 18)` | 25.6 | 17.5 | 21.7 | 24.6 | 12.16 / 11.57 | 6.36 / 6.05 | 23.7 / 33.7 |
| Paper | `oklch(0.64 0.137 72)` | `oklch(0.53 0.2 18)` | 20.1 | 11.8 | 13.1 | 19.7 | 3.18 / 3.46 | 5.40 / 5.87 | 25.6 / 27.4 |

### Text contrast (APCA Lc / WCAG ratio)

Targets: text Lc 90, text-2 Lc 75, text-3 Lc 60 (labels), and WCAG AA 4.5:1 for all three, on both bg and surface.

| Theme | Profile | text on bg | text on surface | text-2 on bg | text-2 on surface | text-3 on bg | text-3 on surface |
|---|---|---|---|---|---|---|---|
| Instrument Dark | standard | 90.8 / 15.11 | 90.2 / 14.21 | 76.0 / 11.98 | 75.4 / 11.26 | 60.9 / 9.08 | 60.3 / 8.54 |
| Instrument Light | standard | 90.1 / 11.26 | 96.0 / 12.29 | 75.0 / 6.06 | 81.0 / 6.62 | 66.5 / 4.50 | 72.4 / 4.92 |
| OLED | standard | 90.1 / 16.44 | 90.1 / 16.44 | 75.3 / 12.98 | 75.3 / 12.98 | 60.3 / 9.83 | 60.3 / 9.83 |
| Night | dim | 38.1 / 5.64 | 38.1 / 5.64 | 30.0 / 4.41 | 30.0 / 4.41 | 24.1 / 3.61 | 24.1 / 3.61 |
| High Contrast | standard | 104.6 / 19.30 | 104.2 / 18.36 | 90.4 / 15.85 | 90.1 / 15.08 | 75.7 / 12.55 | 75.4 / 11.94 |
| Paper | standard | 91.4 / 10.89 | 97.2 / 11.83 | 76.6 / 6.00 | 82.4 / 6.52 | 68.3 / 4.52 | 74.0 / 4.91 |

In the light themes, text-3 lands at Lc 66–68 rather than 60. That's because **WCAG AA is the binding constraint there**: Lc 60 dark-on-light is only about 3.8:1.

Reported, not gated:
- **Hairlines** are measured as OKLab lightness difference after compositing, because WCAG ratios compress near black. They must be visible (≥ 5) and, below contrast 70, quiet (≤ 20).
- **text-2 and text-3 on surface-2** (hover and raised rows) are about 1 Lc below their floors in Instrument Dark: 74.3 and 59.2.

All six themes pass: 31 checks each, and 25 for Night.

---

## Decisions

1. **The core is plain JavaScript with JSDoc types, `checkJs` and strict mode, in one import-free file.** That lets the browser run the identical code without a build step. Bun can't hand the browser TypeScript without a transpile step, and builds are out of scope here.
2. **We reimplemented the validator's math instead of vendoring its script.** We use the same Machado 2009 matrices, OKLab ΔE ×100 and thresholds (CVD ≥ 8, normal ≥ 15, marks ≥ 3:1, chroma ≥ 0.10). The hub needs this logic anyway, to refuse themes. A parity test runs against the real script when `DATAVIZ_VALIDATOR` points at it.
3. **APCA is implemented in-house** (APCA-W3 0.0.98G constants). It matches the published reference values exactly (`#888` on `#fff` = Lc 63.056) and agrees with `apca-w3` to 6 decimals over 2,000 random pairs.
4. **Three text levels mapped onto Radix.** Step 11 = `--text-3` (Lc 60, labels), step 12 = `--text` (Lc 90), and `--text-2` is solved separately at Lc 75 (body secondary). Prototype v1's tertiary text was about Lc 40, below the plan's own label target.
5. **WCAG AA is enforced alongside APCA,** because 02 · Experience requires both.
6. **In light themes, cards are lighter than the page** (step 2 > step 1), carried over from v1's warm light theme. Component states go darker from there.
7. **Status lightness snaps to passing,** holding the hue, as the dataviz method prescribes. Each snap is recorded in the theme's `notes`.
8. **Night is `profile: dim` and `monochrome: true`.** Pure `#ff0000` on black is only Lc 38, so red-on-black can't reach Lc 60 at all. Night therefore uses dim targets (Lc 38 / 30 / 24), and its contrast results are warnings, not refusals. State is carried by glyph shape and word. **Night is for walls with the simplified level of detail,** not for reading body text. Only monochrome themes may skip the pair-separation checks (a test enforces this).
9. **Mid-lightness bases are refused, not silently fixed.** A light base below about L 0.92, or a dark base above about L 0.4, can't reach Lc 90. The editor allows it, so the refusal can be seen: "Text on bg is too low-contrast to read (Lc 87.6, needs Lc 90)".
10. **Hairlines firm up above contrast 70.** That gives High Contrast and `prefers-contrast: more` clearly ruled tiles. Below 70 they stay at 8–12% alpha.
11. **The type scale is global and role-named.** Themes change faces and figures, not sizes, so layouts stay predictable across themes.
12. **Surface, density, briefing and backdrop are theme defaults, not theme tokens.** The theme block carries its default surface tokens; `:root[data-surface]` overrides them. Picking a theme applies its defaults, and the user can still toggle them.
13. **`--board-max` is derived** (12 units at `--unit-max`), so compact density narrows the board without new tokens.
14. **`bun.lock` is committed per package** until the root Bun workspace exists. Remove it when `design/tokens` joins the workspace.
