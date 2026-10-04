# 02 · Experience & Design Language

> How Cha Dash looks, moves, and behaves.
> Status: **direction proposal** for the M0 design spike. Everything here gets tested with prototypes before it hardens into tokens.

---

## North star: *an instrument, not an app*

A well-made instrument is calm while it works, precise when you read it, honest about what it knows, and fast to respond when you touch it. Cha Dash should feel like a beautifully made instrument for your home: Teenage Engineering honesty, Braun calm, Apple-grade physics.

### What we take from Apple
- **Physics:** springs, interruptible animations, momentum, spatial consistency.
- **Glanceability rules from WidgetKit:** one idea per widget, a distinct layout for each size, never hide stale data.
- **The Home app's structure:** a status summary at the top, and the icon/tile split where tapping the icon acts and tapping the tile opens.
- **Hierarchy through restraint:** "when every element is tinted, nothing stands out."
- **Accessibility:** reduced motion, reduced transparency and increased contrast are all respected.

### What we deliberately don't take
- **Liquid Glass on content.** Data always sits on solid surfaces. Glass appears only on chrome (the command bar, sheets, toasts), and users get a frosted ↔ solid slider. This is the lesson Apple learned the hard way.
- **Rounded-bubble iOS cosplay, rainbow app icons, jiggle mode.** Our identity has its own grammar.
- **Bouncy delight for its own sake.** Motion explains change; it never decorates.

---

## Identity pillars

1. **Quiet when healthy.** Neutral is the default state. Color is a signal, not decoration.
2. **Numbers are first-class.** Tabular numerals in a dedicated face, values that roll on change, and every number carries its context (unit, normal band, trend, freshness).
3. **Hairline structure.** 1px rules and precise alignment, like an engineering drawing. No heavy cards or drop shadows on content.
4. **One signal color.** A single, ownable "needs you" color (working name **Ember**). It's an **amber** (hue ≈ 75°), not orange: the M0 palette check showed orange and the "down" red were too close to tell apart (see the color section). It is never decorative.
5. **Physical motion.** Everything moves on springs, can be interrupted, and comes from where it lives.
6. **Honest data.** Freshness is always knowable, staleness is visible, and unknown is shown as "—", never as 0.

### Three directions to prototype in M0

| Direction | Character | Risk |
|---|---|---|
| **A · Instrument** *(recommended north star)* | Warm graphite base, hairlines, tabular mono numerals, Ember signal, monochrome logos that turn colored on issue | Can feel austere. Needs warmth in the light theme. |
| **B · Paper** | Light, editorial, typographic: **literally a newspaper front page**. Masthead, a serif headline as the status sentence, grotesk figures, column rules instead of cards, photo captions on cameras, the fleet laid out like a market table | Lower density; harder to make dark mode feel intentional |
| **C · Glass** | Depth, materials, vibrant backdrops | Looks like an iOS clone, has legibility problems, and is the anti-lesson of 2025 |

**Prototype v2 is built:** [`design/prototypes/m0-v2.html`](../../design/prototypes/m0-v2.html) ([notes](../../design/prototypes/README.md)). It shows Direction A on the dogfood home, driven by [tokens v0](../../design/tokens/README.md), with:
- all six themes and a live base/accent/contrast editor that re-runs the checks;
- surface, density and Briefing toggles;
- first-run, all-quiet and hub-offline moments;
- the Inbox repairs flow;
- a motion study;
- the phone frame;
- the simplified wall level.

Prototype v1 ([`m0-directions.html`](../../design/prototypes/m0-directions.html)) keeps both directions side by side for reference.

The plan was to build A and B as static prototypes on the same demo data, then pick one or blend them (likely A's grammar with B's light theme). C stays as a reference only.

**✅ Decision 12 (2026-10-03): Direction A, built to be very themeable.**
- **A's structure is the product.** That covers the unit grid, sections, widget anatomy, status vocabulary, interaction grammar and motion physics.
- **Everything visual is a theme token.**
- **B lives on as a built-in "Paper" theme**, with ruled surfaces, the newsprint palette and serif headlines, plus an optional `headline` style for the Briefing.

---

## Theming

The structure stays fixed and the look is fully swappable. **A theme is a versioned token document** (YAML/JSON) that the hub validates and compiles to CSS custom properties.
- Components only ever use *semantic* tokens, never raw values.
- Switching themes is instant, with no reload.

| Layer | What it controls | Options |
|---|---|---|
| **Color** | 12-step OKLCH scales for light and dark | Generated from **base · accent · contrast** (the Linear model), or overridden token by token |
| **Status** | Attention, down, progress, maintenance | Overridable, but **must pass the palette validator** (colorblind and normal-vision separation, plus contrast). Failing themes are refused with a specific reason, e.g. "attention and down are too similar for colorblind users" |
| **Typography** | UI face · numeral face · clock face · headline face | Bundled open-source fonts: Instrument Sans, Inter, IBM Plex Sans, Schibsted Grotesk; Geist Mono, JetBrains Mono, Commit Mono; Doto; Newsreader. Self-hosted user fonts come later |
| **Surface** | How tiles separate | `card` (A) · `ruled` (column rules, as in B) · `flat` |
| **Shape** | Radius scale, hairline weight | Tokens |
| **Density** | Unit size, padding | `comfortable` · `compact` |
| **Briefing** | Status line style | `sentence` (A) · `headline` (B's serif headline plus deck) |
| **Material** | Chrome translucency | Frosted ↔ solid slider; reduced transparency forces solid |
| **Motion** | Animation | `full` · `reduced` · `none` (follows the OS by default) |
| **Backdrop** | Behind the board | Solid, or a subtle texture (paper grain). **Never content over a wallpaper photo** |

**Built-in themes:**
- **Instrument Dark** (default)
- Instrument Light
- OLED
- Night (monochrome red)
- High Contrast
- **Paper** (Direction B)

**Theme selection is per board and per device.** For example, a wall tablet can switch to Night on a schedule.

**Sharing:** themes export and import as files. A community gallery comes later, following the same pattern as integration specs, and every imported theme is validated before it can be applied.

**Custom CSS** is an admin-only escape hatch that isn't a supported API, so it may break between versions. CSP still blocks loads from other origins, so custom CSS can restyle the page but can't send data anywhere.

---

## Visual system

### Color

**Structure**
- Tokens are defined in **OKLCH**, with **12-step semantic scales** (Radix model):
  - 1–2: app background
  - 3–5: component states
  - 6–8: borders
  - 9–10: solid fills
  - 11–12: text
- Steps 11 and 12 are guaranteed to hit APCA **Lc 60** and **Lc 90** on step 2.
- Themes are generated Linear-style from three inputs (**base, accent, contrast**), so high-contrast and custom themes come free and stay accessible.
- Neutrals are **warm graphite**, slightly toward amber, not blue-gray. That gives warmth without color.

**Status vocabulary.** Status is always shape + word + color, never color alone.

| State | Glyph | Color | Word example | Behavior on page |
|---|---|---|---|---|
| Healthy | none (or hairline ○ in lists) | neutral | "OK" | recedes |
| Intentionally off | ○ hollow | neutral, dimmed | "Stopped (expected)" | quiet. **Not a problem.** |
| In progress | ◔ determinate arc | accent | "Updating · 40%" | shows progress, then settles |
| Needs attention | ▲ | **Ember** (amber) | "2 updates", "Disk 91%" | rises within its section |
| Down / critical | ■ | red (desaturated in dark mode) | "Down since 14:02" | rises to the top of the page |
| Stale / unknown | dashed outline + hatch | neutral | "Last seen 4m ago" | shows the last-known value, hatched |
| Maintenance | ‖ | muted blue | "Maintenance until 22:00" | quiet; alerts silenced |

**Validated status pairs** (from the dataviz palette validator, run in M0)
- **Orange vs red failed in every theme:** normal-vision ΔE was 11–12.5, below the floor of 15, and colorblind separation was as low as 7.9.
- **Amber vs crimson passes.** The colorblind ΔE is the worst case of deutan and tritan:

| Theme | Attention (Ember) | Down | Colorblind ΔE | Normal-vision ΔE |
|---|---|---|---|---|
| A dark | `oklch(.74 .15 75)` | `oklch(.64 .21 15)` | 12.2 | 21.2 |
| A light | `oklch(.64 .139 70)` | `oklch(.55 .21 15)` | 10.5 | 19.3 |
| B light | `oklch(.64 .14 72)` | `oklch(.53 .2 18)` | 11.7 | 19.4 |
| B dark | `oklch(.74 .14 78)` | `oklch(.64 .2 18)` | 11.4 | 20.3 |

The dark-theme pairs sit above the validator's *categorical* lightness band. That's deliberate: these are status colors, which always come with a glyph and a word, not chart series.

A light's attention was snapped from L .66 to .64 by the token generator, so the mark clears contrast on Instrument Light's surfaces. Every built-in theme's numbers are in [tokens v0](../../design/tokens/README.md). Two rules the generator enforces on top of the validator:
- **The accent never reads as status.** A theme is refused if its accent sits within ΔE 15 of either status color, because progress would read as attention.
- **Night is dim by design.** It's monochrome red, so state is carried by glyph and word only. Its text reaches Lc 38 / 30 / 24, because red on black can't physically reach Lc 60.

**Modes**
- Light, Dark, **OLED black** (for kiosks), and **Night** (monochrome red, StandBy-style).
- Each mode honors `prefers-contrast` and `prefers-reduced-transparency`.

### Typography

All fonts are open-source, self-hosted and bundled in the binary. Nothing loads from a CDN.

| Role | Candidates (pick in M0) | Notes |
|---|---|---|
| UI sans | **Instrument Sans** · Inter · IBM Plex Sans | Instrument Sans fits the concept and is less ubiquitous than Inter. Plex has the most industrial character. |
| Numerals / mono | **Geist Mono** · JetBrains Mono · Commit Mono | Used for *every* live value. Tabular, with a clear slashed zero. |
| Display accent (optional) | Departure Mono (pixel) | Only for wall clocks and kiosk hero numbers. Our equivalent of Nothing's Ndot. |

**Pairings in prototype v1:**
- **A:** Instrument Sans (UI), Geist Mono (every value), and **Doto**, a dot-matrix face, for the clock only.
- **B:** **Newsreader** (serif, with optical sizes) for the headline and section heads, and **Schibsted Grotesk**, a grotesk designed for news publishing, for UI and figures. Big standalone numbers use proportional figures; lists use tabular figures.
- The prototype loads these from Google Fonts for convenience. The product bundles them.

**Number formatting rules**
- Tabular figures everywhere.
- Units are set smaller, in a lighter weight and secondary color: `71`<sub>%</sub>, `3.2`<sub>TB</sub>.
- Three significant digits by default.
- Bytes use IEC or SI per user setting, consistently across the whole app.
- Relative time on display ("4m ago"), absolute on hover or focus.
- Unknown is "—". Never fake a 0.

### Shape, depth, material
- **Radii:** 4pt base, with concentric nesting so inner radius = outer radius − padding.
- **Depth:** elevation comes from **lighter surfaces in dark mode**, not shadows. Content has no drop shadows. Only lifted, dragged elements get a soft shadow.
- **Hairlines:** 1px at 8–12% contrast define structure. Section titles are small caps with a rule.
- **Material:** glass only on chrome (the ⌘K palette, sheets over the page, toasts). There's a user slider from frosted to solid, and reduced transparency maps to solid.

### Iconography
- **UI:** Lucide (or Phosphor, whose duotone and fill weights can encode state).
- **Service logos:** selfh.st/icons, dashboard-icons and Simple Icons, resolved by name (`sh:`, `di:`, `si:`). They are **fetched once and cached locally**, never hot-linked at runtime.
- Logos render **monochrome by default** and switch to full color on hover, focus, or when the service has an issue. Color becomes information.

### Motion

Springs are defined by **duration + bounce**, per WWDC23. Every animation can be interrupted and redirected.

| Token | Spring | Used for |
|---|---|---|
| `press` | 0.2s · bounce 0 | button and tile press, toggles |
| `move` | 0.35s · bounce 0 | layout reflow, list reorder |
| `lift` | 0.3s · bounce 0.15 | edit-mode pick-up, drop settle |
| `sheet` | 0.45s · bounce 0.08 | expand in place, dismiss with momentum |
| `value` | 0.6s ease-out tween | numeral roll, sparkline extend (data change ≤ 2s, per HIG) |
| `fade` | 0.15s | reduced-motion replacement for all of the above |

**Rules**
- Motion only explains change.
- **No infinite loops.** No pulsing "live" dots. They cause burn-in on walls and fatigue everywhere else.
- Things come from and return to where they live (spatial consistency).
- A drop projects its landing spot from the gesture's momentum.
- Under `prefers-reduced-motion`, everything crossfades.

### Data visualization
- **Bars with threshold ticks** beat gauges. **No donuts, dials or radar charts.**
- **Sparklines (Tufte):**
  - word-sized, with slopes averaging about 45°;
  - a gray band for the normal range;
  - a dot on the last point, matched to the number beside it;
  - no frame, no grid.
- Single-hue charts. Status colors appear only when a value crosses a threshold.
- Charts can be linked: scrubbing one moves the time cursor on all of them.

---

## Layout system

### The grid
- A square unit **U** with a **12px gutter**, on a 4pt spacing scale.
- U is roughly 76–96px and scales with the viewport. Widgets **re-lay out by size class; they never scale**.

| Context | Columns | Notes |
|---|---|---|
| Phone | 4 | Hero widgets fall back to Large automatically |
| Tablet | 8 | |
| Desktop | 12 | |
| Wall / TV | 16 | Simplified level of detail by default |

### Widget sizes

Every size except Glyph has an even column count, so everything tiles cleanly on 4 columns.

| Name | U | Typical use | iOS analog |
|---|---|---|---|
| **Glyph** | 1×1 | A single toggle or status | accessory circular |
| **Strip** | 4×1 · 8×1 | Status sentence, pill row | accessory inline |
| **Small** | 2×2 | One metric plus a sparkline, or one control | systemSmall |
| **Wide** | 4×2 | Metric with chart, or a list of 3–4 items | systemMedium |
| **Tall** | 2×4 | Container list, download queue | — |
| **Large** | 4×4 | Proxmox node, Unraid array | systemLarge |
| **Hero** | 8×4 | Atlas topology, cameras, a big chart | systemExtraLarge |

Each widget **declares which sizes it supports**. The resize handle snaps only to those sizes.

### Sections and reflow
- The page is a column of **sections**, each with an internal column count. Sections flow in Z order.
- When the viewport narrows, a section keeps its internal structure and wraps as a unit. This is Home Assistant's approach, chosen over masonry for predictability and muscle memory.
- **One canonical layout.** Phone, tablet and wall layouts are derived from it automatically. Users can add per-device overrides, but never *have* to maintain a second layout.
- **Needs attention** is a system section. It appears at the top **only when it has something in it**:
  - Tiles in an attention or down state are mirrored into it (not moved), so your layout never reshuffles.
  - When the problem clears, the section collapses away with a `move` spring.
  - This is how "healthy is quiet" works without the page jumping around.

### Levels of detail

Each widget renders four variants plus a night tint.

| Level | Where | Character |
|---|---|---|
| **Full** | Desk, large sizes | Everything |
| **Compact** | Small sizes, phone | Primary value, context, one action |
| **Simplified** | Wall, TV, glance-from-across-the-room | Bigger type, no controls, the one number that matters |
| **Ink** | E-ink, print | One color, no motion, absolute timestamps ("as of 14:05") |

---

## Widget anatomy and states

```
┌───────────────────────────────────────────┐
│ ◧ Sonarr                         ·4m  ⋯   │ ← identity: mono logo · name · freshness (only when stale) · menu
│                                           │
│ 3 queued          ▁▂▂▃▅▃▂▂      ▲ 1 failed│ ← primary value · context (sparkline w/ normal band) · attention
│ 128 GB free on media · next: Severance 21:00│ ← secondary facts
│ on unraid · container ↑ update available  │ ← graph context (free, no config)
└───────────────────────────────────────────┘
```

**Interaction grammar** (the same everywhere)

| Gesture | Effect |
|---|---|
| Tap the **icon or primary control** | The primary action (toggle, open service) |
| Tap the **tile** | Expand in place into the detail sheet |
| **Long-press or right-click** | Context menu: actions, resize (supported sizes, live preview), configure, hide, move to section |
| Hover (desktop) | Logo colors in. Inline secondary actions appear. Absolute timestamps show. |

**Every widget must design all of these states.** They are part of the definition of done.

| State | Treatment |
|---|---|
| First load (no data ever) | Skeleton matching the final layout, with no spinner. Should almost never be seen, because the server inlines the last-known snapshot. |
| Live | Normal |
| Stale | Last-known value with a hatch pattern and "as of 14:02" |
| Error / auth failed | A **Repair** card with a plain-language cause and a fix button ("Reconnect Pi-hole") |
| Empty | A helpful empty state ("No downloads, queue is clear") |
| Unauthorized | Visible but locked, with "Ask an admin" |
| Hub offline | The whole page shows "Cha Dash hub unreachable since 10:02" and keeps the last-known values |

---

## Editing model

**Light edits need no mode.** Long-press or right-click gives you resize (supported sizes only, live preview), configure, hide and move to section.

**Edit mode**
- Enter it by long-pressing empty space, pressing `E`, or via ⌘K → "Edit layout".
- Widgets **lift** slightly with a scale and soft shadow, instead of jiggling. Their content dims to about 60% and the unit grid fades in.
- Drag anywhere on a widget. The dragged tile lifts on the `lift` spring, neighbors reflow on the `move` spring (as in the Motion table), and the landing spot is projected from momentum ([`@chadash/layout-vue`](../../packages/layout-vue/README.md)).
- A **single corner resize handle** snaps to supported sizes and shows a "4×2" label as it snaps.
- Keyboard: arrows move, Shift+arrows resize, Enter picks up or drops, Esc reverts the current drag.

**Adding widgets**
- The gallery previews **with your real data**, not lorem ipsum.
- Drag a widget from the gallery to a spot, or drag the + button to the place you want it (Things' Magic Plus).
- Otherwise it lands in the nearest gap that fits, near the viewport.

**Saving and history**
- Changes **auto-save** with history. ⌘Z works.
- "Layout history" lets you restore any previous version.
- Deleting a widget never asks for confirmation; it shows an Undo toast instead.
- **YAML round-trip.** Every layout change is reflected in the exportable config, and every config import shows a visual diff before it's applied.

---

## Surfaces and navigation

Cha Dash is a single page. Everything else is a **layer on top of it**, not a separate admin app.

| Surface | What it is | How you get there |
|---|---|---|
| **Front page** | The board: Briefing and sections | Home |
| **Sheet** | A detail view expanded in place. Tabs: Overview · Logs · Metrics · Actions · Related · History. Deep-linkable URL. | Tap a tile, ⌘K |
| **Lens** | The page filtered to a context (a host, a tag, "problems", a phrase from the Briefing) | Click a phrase, host or tag |
| **Atlas** | Graph and topology canvas with layer toggles | Hero widget, ⌘K, lens |
| **Command bar** | Search and act on anything | ⌘K, `/` |
| **Inbox** | Discoveries, repairs, proposals, approvals | Badge in the Briefing |
| **Settings** | Integrations, nodes, users, notifications, appearance, export | ⌘K, menu |

On a phone, sheets come up as full-height cards with momentum dismiss. A thumb-zone bar holds Search, Inbox, and the most recent action.

---

## The action safety ladder

| Tier | Examples | Interaction | Extra |
|---|---|---|---|
| **T0 · Instant** | Open, copy IP, view logs | Just do it | — |
| **T1 · Reversible** | Stop or start a container, pause Pi-hole for 5 minutes, pause downloads | Act immediately → **Undo toast (10s)** | Audit |
| **T2 · Disruptive** | Restart a stack, reboot a VM, update a container, open a console | **Hold-to-confirm ring** (~600ms) with a **blast-radius list** built while you hold | Audit. Optional step-up |
| **T3 · Destructive** | Delete a VM or snapshot, remove a volume, stop the array, roll back a snapshot, prune | **Type the name** + **step-up auth** (passkey re-verify) | Audit. Shows exactly what will be lost |
| **T4 · Critical** | Power off the host running Cha Dash, format a disk, wipe | Disabled by default. Enabled per scope in settings. T3 flow plus a cooldown. | Audit plus a notification to all admins |

- **Plan → apply** for multi-target operations: you see the list of steps and a diff, approve, then watch progress.
- **AI or API-originated actions** above T1 always require human approval (later milestone).
- **Rate limits** apply to household and kiosk roles.

---

## Contexts

| Context | Specifics |
|---|---|
| **Desk** | Full detail, keyboard-first, hover affordances, ⌘K everywhere |
| **Phone (PWA)** | 4-column derived layout; thumb-zone bar; sheets; 44pt minimum targets; Web Push (requires installing to the Home Screen); safe-area insets; `theme-color` |
| **Tablet / wall** | Kiosk token; Screen Wake Lock; auto-dim on schedule, sun position, or a Home Assistant lux sensor; **night red tint**; **burn-in care** (1–2px shift every few minutes, no static bright blocks, no loops, optional page rotation); quiet-when-healthy by default |
| **TV** | 16 columns; simplified level of detail; D-pad focus rings; ≥23pt type |
| **E-ink** | Ink render; server-rendered snapshot endpoint; refresh every 15 minutes; absolute timestamps |

---

## Accessibility (a release gate, not a nice-to-have)

- WCAG 2.2 AA, plus APCA targets: body Lc 75+ (90 preferred), labels Lc 60+, large numerals Lc 45+.
- **Keyboard parity:** every action is reachable through ⌘K and has visible focus. The grid is fully keyboard-editable.
- **Screen readers:**
  - Status changes are announced politely and throttled ("Immich is down").
  - Widgets have a semantic structure (heading, value, context).
  - Charts provide text summaries ("CPU 34%, normal range 10–40%, rising").
- **Color independence:** status is always glyph plus word plus color. Palettes are tested against deuteranopia, protanopia and tritanopia.
- **System preferences** for motion, transparency and contrast are honored, and in-app overrides exist.

---

## Voice and copy

- **Symptom first, plain words.** "Sonarr can't reach qBittorrent since 03:12", not "Error 502 Bad Gateway".
- **A broken integration is not a service outage.** If Cha Dash can't read Plex's API (bad token, API change), the problem goes to the **Inbox as a Repair**: "Plex rejected its token. Reconnect." The tile keeps showing last-known values plus an "as of" time. Never dump raw error HTML onto the page.
- **Specific, with time.** Say "since 14:02", not "recently".
- **Calm, never alarmist.** No exclamation marks, no ALL CAPS warnings.
- **Actionable.** Every problem sentence ends with what you can do: [Restart] [View logs] [Ignore for 1h].
- **Friendly on the Family board.** "Movies are working ✓" and "Internet is slow right now. We're on it."

---

## Anti-patterns we will not ship

- Grids of logos with no state.
- Gauges or donuts for CPU and RAM.
- Rainbow tiles, or brand color used everywhere.
- Red/green dots as the only signal.
- Spinners that hide the last-known value, or widgets that pop in one at a time.
- Glass over wallpaper as a content surface.
- Configuration that only works as YAML, or only in the GUI.
- Layouts you have to maintain separately for each breakpoint, or masonry that reshuffles.
- Alerting on causes ("CPU 90%") or on every flap.
- Confirming everything, or an unguarded one-tap host reboot.
- Infinite animations.
- An empty first run.
- Tiny targets, or controls that move position between visits.
- API keys in the browser.
