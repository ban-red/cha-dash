# ADR-0030 · Board flows: bands, bento and mosaic

**Status:** accepted · **Date:** 2026-10-29 · Adds to [ADR-0013](0013-grid-engine.md); the engine's decision 6 (sparse bands) becomes the default rather than the only flow.

## Context

Sections flow across the board in sparse bands: each band is as tall as its tallest section, and nothing back-fills ([packages/layout](../../packages/layout/README.md#derived-breakpoints)). On the dogfood board a tall Servers section sat beside two short ones (Activity, Home), leaving holes under them as tall as Servers. People asked for a "bento" board that fills those holes, without losing the predictability that made us reject masonry ([02 · Experience](../plan/02-experience.md#layout-system)).

## Decision

**A board has a `flow`: `sections` (bands, the default), `bento` or `mosaic`. It's stored in the layout document and chosen in edit mode, for everyone who views the board.**

- **Bento:** each section is a block (a header track, then two tracks a row), packed by the same dense first-fit tiles already use. A block is only as wide as its tiles, and it may narrow from its span down to its widest tile, its tiles repacked at that width, when that lands it earlier. Ties go to the wider block.
- **Mosaic:** no blocks. Every tile is packed onto the board, section after section. A section's header is a label glued to its first tile, so the two pack as one block and the label can't drift.
- **Half-row tracks.** Two tracks and a gap are exactly one unit, so tiles keep their square units and line up, and a header costs a track rather than a row.
- **Little else moves.** Tile sizes are the same in every flow, and overrides still apply. Positions inside a section change only when a bento block narrows to fit.
- **A mosaic is edited as bento**, so a drag stays inside the sections the editor already understands. In edit mode, bento blocks keep their spans and stored tile positions, so drops map onto the stored layout. Done shows the fitted blocks again.

## Consequences

- Both flows are pure functions of order and sizes, like the bands. The same board always gives the same picture, a change to one section never moves an earlier one, and tablet and phone are still derived. Property tests check every invariant in every flow.
- Reading order can jump: a later section, or in mosaic a later tile, can sit above an earlier one. DOM and focus order stay the stored order, as they already were for dense tiles.
- In mosaic, tiles from one section can spread across the board. Bento is the choice for people who want groups to stay together.
- A header has one track, about 28–40 px. Themes with tall headers, such as the ruled surface, have to fit their header into it in these flows.
- `flow` is written only when it isn't `sections`, so existing documents are unchanged. An unknown flow reads as `sections`. The hub keeps it when an import adds to a board.

## Alternatives considered

- **Masonry** (shortest column first, by measured height): it reshuffles when content or width changes, which is what 02 · Experience rules out.
- **Back-filling bands** (pack sections into whole unit rows): a header would cost a whole unit row, about 90 px for a 20 px label.
- **Dense packing with free labels in mosaic:** labels are one track tall, so they fell into small gaps far from their tiles. The first draft did exactly this.
