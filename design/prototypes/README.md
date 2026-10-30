# M0 design prototypes

| File | What it is |
|---|---|
| [`m0-v2.html`](m0-v2.html) | **Prototype v2.** Direction A on the dogfood home, driven by tokens v0 and the live theme engine. The current reference |
| [`m0-directions.html`](m0-directions.html) | Prototype v1: Directions A and B side by side. Kept for the record; A was chosen (decision 12) |

---

## `m0-v2.html`: Direction A with the theme engine

A single static file. Open it in a browser; there's no build step and no server. Fonts come from Google Fonts. Everything else is inline, including [`design/tokens`](../tokens/README.md):
- `dist/tokens.css`;
- the generator core, the same `src/core.js` the tests run;
- the six theme documents.

After changing tokens, run `bun run inject` in `design/tokens`. A test fails if the inline copies drift.

**Deep links** set the starting state: `m0-v2.html#theme=paper&view=wall&moment=quiet&frame=phone&studio=1`. Keys:
- `theme`: `instrument-dark`, `instrument-light`, `oled`, `night`, `high-contrast`, `paper`, `neon`, `neon-tokyo`;
- `view`: `board`, `wall`, `workbench`, `motion`;
- `moment`: `incident`, `quiet`, `first`, `offline`;
- also `frame`, `surface`, `density`, `briefing`, `motion`, `material`, `studio`.

### Controls

The bottom bar holds Moment, View, the six theme dots, Desktop/Phone and **Appearance**. The ✕ collapses it, and it starts collapsed on a real phone.

| Key | Action |
|---|---|
| `1`–`4` | Front page · Wall · Workbench · Motion study |
| `T` / `⇧T` | Next / previous theme |
| `M` | Cycle the moment: today · all quiet · first run · hub offline |
| `P` | Phone frame |
| `A` | Appearance (the theme engine) |
| `I` | Inbox |
| `E` | Edit-mode look |
| `⌘K` | Command bar |
| `Esc` | Close the top layer, clear a lens, leave edit mode |

### What's in it

**Theme engine** (Appearance):
- All six built-in themes. Picking one applies its defaults for surface, density and Briefing style.
- A live **base · accent · contrast** editor, with status hues under "Status colours". Every change regenerates the theme with the real generator and re-runs every check.
- The **validation panel** shows each result: status-pair separation (colour-blind and normal vision), chroma, accent vs status, APCA and WCAG on bg and surface, marks and hairlines. Failing themes show the hub's refusal sentence. Try **"Try an orange attention"** (refused: attention and down too similar) or **"Try an amber accent"** (refused: progress would read as attention).
- The 12-step OKLCH scales, rendered live.
- **Layout:** surface (card · ruled · flat), density (comfortable · compact), Briefing (sentence · headline), material (theme · solid), motion (system · full · reduced).
- **Export:** the theme document as YAML.

**Moments:**
- **Today:** the real incidents. PBS 92% plus a failed verify and jupiter-nas memory 95% in Needs attention. Plex and Karakeep as Inbox repairs, not outages.
- **All quiet:** healthy is quiet. Empty states: "Queue clear", "Nothing playing", Inbox zero.
- **First run:** what the hub finds on its own host with nothing configured: 11 services through the socket proxy, setup cards, a locked tile that needs a key, empty weather. **Review import** opens the import sheet with a source picker (Homepage, found on pvedocker; Homarr and plain YAML in v0.2), brings in the rest of the home, and the page reflows into Today.
- **Hub offline:** a banner, every tile hatched with "as of", the cameras' last frame, actions disabled. **Retry now** reconnects.

**Inbox repairs flow:**
- **Plex** rejected its token: Reconnect → PIN sign-in with a determinate countdown → done. The Plex tile loses its hatch and its counts roll to fresh values.
- **Karakeep**'s API changed: Review → the spec diff (1.3 → 1.4, still read-only) → apply → done.
- Each repaired item collapses out of the Inbox; the Briefing badge and the thumb bar update.
- **Discoveries** include a real-data tile preview (paperless-ngx). **Suggestions** cover credential hygiene.

**Motion study** (view 4, or from the front page):
- **Expand in place:** tap a tile and an empty surface grows from the tile's rect into the sheet, carried by the `sheet` spring, while the content fades in clipped to it. Esc mid-flight sends it back from wherever it is.
- **Reflow springs:** switching moments (or surface and Briefing style) FLIPs every tile to its new home on the `move` spring. Removed items fade as ghosts, and numbers roll. Density reflows as the board width eases on the same spring.
- **Hold-to-confirm ring:** about 650 ms. The blast-radius list lights up as you hold, and letting go springs the ring back. Reboot pluto, jupiter or mars-1, or prune PBS. **T3** (type the name, then a passkey step) and a disabled **T4** are in the Workbench.
- **Lens fade:** tap a phrase in the Briefing.
- **Token cards:** each spring's curve (perceptual duration vs. settle time), an interruptible demo, a 5× slow-mo toggle and a reduced-motion toggle.
- **Under reduced motion,** everything crossfades: sheets fade, FLIP becomes an opacity blink, and rolls become instant.

**Phone:**
- A phone frame with a status bar, the Dynamic Island and a home indicator.
- The board derives to 4 columns. Hero falls back to Large.
- Sheets and the Inbox become bottom cards. **Drag the handle down: momentum projects the release, and a flick dismisses.**
- A thumb-zone bar holds Search, Inbox and the most recent action.
- No horizontal scroll at 375 px in any view or moment (checked).

**Night:**
- Monochrome red on black.
- Camera footage tinted, no shadows, solid chrome.
- State carried by ▲ / ■ shape and word alone.

**Wall** (the simplified level of detail):
- 16 columns, wall-size type, no controls.
- **Cameras** (one big frame, two small, the last detection as a caption), **Backups** (the one number, a thick bar, "Full in about 3 weeks") and **Hosts** (one sentence plus thick memory bars).
- Burn-in care: a 1 px shift every 2 minutes, with no animation.
- Pair it with Night.

**Workbench** (view 3): a static preview of `/dev/workbench`.
- The Unraid widget in all 7 sizes.
- Sonarr in the 8 required states plus maintenance and off-expected.
- The four levels of detail for Backups.
- The status vocabulary.
- The safety ladder, T0–T4.

The full list is [`docs/design/workbench.md`](../../docs/design/workbench.md).

Nothing is executed. Every action ends in a toast.

### Decisions recorded in v2

- **Word values use the UI face; numbers use the numeral face.** "Nothing playing" in Geist Mono read like an error code.
- **Section headers read label · meta · rule,** with the rule running to the section edge. With right-aligned meta, neighbouring sections collided ("2 people · 3 cameras INFRASTRUCTURE").
- **Permission-limited values are "—", never blurred.** The hub doesn't send what the role can't read, so there's nothing to blur.
- **The ruled surface keeps the unit gutter.** A wider column gap broke square-unit alignment between sections.
- **Expand in place grows an empty surface, then reveals the content clipped to it.** It doesn't scale the tile. Scaling distorts text, and a clip from outside the sheet's box can't start at a tile that lies outside it.
- **No staggered intro.** "Widgets that pop in one at a time" is on the anti-pattern list; the page arrives whole, as it would from an inlined snapshot.
- **The thumb bar sits under sheets.** The phone status bar gets a gradient backdrop so content doesn't show through it.

### Known limits

- Fonts load from Google Fonts. The product bundles them, per the Pact.
- No drag or resize in edit mode; that's the layout-engine track. Layouts derived for 8 columns can leave a gap, such as under Home Assistant.
- Data is static and seeded, shaped like the real home. Logs, metrics and history tabs are placeholders.
- Service logos are monograms; the product resolves real icons by name and caches them locally.
- `requestAnimationFrame` pauses in hidden tabs, so springs only run while the page is visible.

---

## `m0-directions.html`: two visual directions, one dogfood home

This is a single self-contained HTML file. Open it in a browser; there's no build step. It renders the [dogfood home](../../docs/plan/05-dogfood-home.md) with today's real incidents (PBS 92% plus a failed task, jupiter-nas memory at 95%, Plex and Karakeep needing repair) in two directions:

| | A · Instrument | B · Paper |
|---|---|---|
| Idea | A precision instrument panel | The literal *front page* of your home |
| Base | Warm graphite, hairlines, flat tiles | Newsprint, column rules, no cards |
| Type | Instrument Sans · Geist Mono for every value · Doto (dot-matrix) clock | Newsreader serif headlines · Schibsted Grotesk (UI and figures) |
| Status sentence | One line of prose with tappable phrases | A headline with a deck underneath |
| Cameras | Footage with surveillance-style timestamps | Grayscale "photos" with italic captions |
| Fleet | Instrument table | Market-table layout |

**Controls** (in the bottom bar, or from the keyboard):

| Key | Action |
|---|---|
| `1` / `2` | Switch direction |
| `H` | Today ↔ all quiet |
| `T` | Cycle theme: light, dark, night |
| `P` | Phone frame |
| `W` | Widget sheet |
| `E` | Edit-mode look |
| `⌘K` | Command palette |
| `Esc` | Close the top layer, or clear a lens |

**Things to try:**
- **Tap a phrase in the status sentence** to filter the page to that topic (a "lens"). Unrelated tiles fade out.
- **Hover any sparkline.** Every sparkline on the page scrubs to the same moment.
- **Click a tile** to expand it into a detail sheet. Open **Fleet → Reboot** on `pluto` or `jupiter`, then **hold**. The blast radius builds while you hold: rebooting pluto takes down the router.
- **Open the Inbox:** repairs (Plex, Karakeep), label-discovered services, and credential-hygiene hints.
- **⌘K:** fuzzy-search services and actions, e.g. `rebjup` → "Reboot jupiter".
- **Widget sheet:** the Unraid widget in all 7 sizes (each a different layout, never scaled), and the Sonarr widget in all 9 states.

Nothing is executed. Every action ends in a "prototype" toast.

### What to decide from it
1. **Direction:** A, B, or a blend (e.g. A's structure with B's headline and light theme). **Decided: A, built to be very themeable; B became the Paper theme.**
2. **Density:** is the front page too much or too little at desktop and phone sizes?
3. **Signal color:** does amber Ember read as "needs you" without feeling alarming?
4. **Status sentence:** a terse line (A) or a headline (B)? **Both survive as the Briefing style setting.**

### Findings recorded so far
- **Orange vs red is not distinguishable enough** for colorblind readers, or even for normal vision (validator ΔE 11–12.5, below the floor of 15). Ember moved to amber and "down" to crimson; both pairs pass. See [02 · Experience → Color](../../docs/plan/02-experience.md#color).
- Long host names in prose need non-breaking hyphens (`jupiter‑nas`).
- Interactive phrases in a headline have to be wrappable inline elements, not buttons, or the headline breaks badly.

### Known limits
- Fonts load from Google Fonts. The product bundles them, per the Pact.
- No drag or resize in edit mode yet; that's the separate layout-engine spike.
- Data is static and seeded, but shaped like the real home.
