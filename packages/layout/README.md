# @chadash/layout

The Cha Dash layout engine: boards made of sections, sections made of sized widgets, one canonical 12-column layout, and tablet and phone layouts derived from it.

- **Pure TypeScript.** Framework-agnostic, ESM, strict, zero runtime dependencies.
- **Immutable.** Every operation takes a board and returns a new one. When nothing changed, it returns the *same object*, so history and renderers can skip no-ops by reference.
- **Structurally shared.** Sections and items that didn't change keep their identity.

Status: M0 spike (track 2.1–2.2 in [06 · Next steps](../../docs/plan/06-next-steps.md)). The grid-engine decision (ADR-0013) is still open. This package is the "build our own" side of that comparison, so the API is kept small and every operation is measurable on its own.

Contents: [Quick start](#quick-start) · [Model](#model) · [How layout works](#how-layout-works) · [API](#api) · [Serialization](#serialization) · [Decisions](#decisions) · [Deferred](#deferred) · [Development](#development)

---

## Quick start

```ts
import { createBoard, move, resize, smartInsert, deriveLayout, createHistory, commit, undo } from '@chadash/layout';

let board = createBoard({
  sections: [
    {
      id: 'infra',
      title: 'Infrastructure',
      span: 8,
      items: [
        { id: 'pve-1', type: 'proxmox.node', size: 'large', supportedSizes: ['wide', 'large'] },
        { id: 'nas', type: 'unraid.array', size: 'wide', supportedSizes: ['small', 'wide', 'large'] },
      ],
    },
    { id: 'clock', span: 4, items: [{ id: 'clock', type: 'clock', size: 'small', supportedSizes: ['small', 'wide'] }] },
  ],
});

let history = createHistory(board);

board = move(board, 'nas', { x: 0, y: 0 });        // drop target: the desired top-left cell
board = resize(board, 'pve-1', 'wide');            // only supported sizes
board = smartInsert(board, { id: 'cams', type: 'camera', size: 'hero', supportedSizes: ['hero', 'large'] });
history = commit(history, board);

const phone = deriveLayout(board, 'phone');        // 4 columns, derived, never stored
history = undo(history);                           // history.present is the exact previous board
```

The package exports TypeScript source (`"exports": "./src/index.ts"`). Bun and Vite compile it directly, so there is no build step.

---

## Model

```
Board
├── flow?                         sections (default), bento or mosaic: how sections share the board
└── sections: Section[]          Z order (reading order) on the board
    ├── id, title?, span          span = width in board columns at desktop = internal columns
    ├── items: Item[]             Z order inside the section — the source of truth
    │   └── id, type, size, supportedSizes, x, y
    └── overrides?                per derived breakpoint (tablet, phone)
        └── span?, order?, sizes?
```

| Type | Notes |
|---|---|
| `Board` | `{ sections, flow? }`. An absent `flow` is `sections`. |
| `Section` | `span` is 1–12. The section's internal grid has `span` columns. |
| `Item` | `id` is unique across the whole board. `type` is the widget type and is opaque to the engine. `size` is always one of `supportedSizes`, and always fits the section. `x`/`y` are the canonical position inside the section, **derived from item order**; you never set them. |
| `SectionOverride` | Optional per-breakpoint adjustments: a `span`, an item `order`, and per-item `sizes`. An absent field means "derive it". |
| `DerivedLayout` | Output of `deriveLayout`: its `flow` and `tracks`, placed sections (`column`, `band`, `span`, `rows`, and in bento and mosaic `area` and `header`) with placed items (`x`, `y`, `w`, `h`, `size`, `forced`, and in bento and mosaic `area`). |
| `TrackArea` | Bento and mosaic: a box on the board, `{ column, track, w, tracks }`, in board columns and half-row tracks. |

### Sizes

From [02 · Experience → Widget sizes](../../docs/plan/02-experience.md#widget-sizes), in grid units (columns × rows):

| `SizeId` | U | | `SizeId` | U |
|---|---|---|---|---|
| `glyph` | 1×1 | | `tall` | 2×4 |
| `strip` | 4×1 | | `column` | 3×4 |
| `strip-long` | 8×1 | | `large` | 4×4 |
| `small` | 2×2 | | `hero` | 8×4 |
| `wide` | 4×2 | | | |
| `panel` | 4×3 | | | |

`SIZES[id]` gives `{ id, name, w, h }`, and `sizeLabel(id)` gives the "4×2" label the resize handle shows.

### Breakpoints

| `Breakpoint` | Board columns | Layout |
|---|---|---|
| `desktop` | 12 | Canonical, stored |
| `tablet` | 8 | Derived, plus optional overrides |
| `phone` | 4 | Derived, plus optional overrides |

---

## How layout works

### Dense Z-reflow

Inside a section, items are placed **in order**, each at the first free cell in reading order (top to bottom, then left to right) where it fits. This is the same algorithm as CSS `grid-auto-flow: row dense`, and a property test checks it against a reference implementation of that algorithm.

The order is what's stored. Positions are always recomputed from it, so a layout can never contain an overlap or an out-of-bounds item. Two properties follow, and both are tested:

- **Prefix stability.** An item's position depends only on the items before it. An edit at order index *k* never moves anything before *k*, and untouched items keep their relative order.
- **Idempotence.** `reflow(board)` on a board from this package returns the same object.

### Drop targets

`place` and `move` take a target cell: the top-left the user is aiming at, for example the pointer position projected from momentum. The engine tries every insertion index, takes the one whose landing cell is nearest the target (Euclidean distance; ties go to the earlier cell), and reflows. Targets outside the grid are clamped, never rejected.

Several neighbouring indices often land on the same cell. They all produce the same picture, so the engine picks among them by two rules:
1. The item's **current index**, if it's among them. Dropping an item back where it was is a true no-op.
2. Otherwise, the index that keeps the order closest to **visual reading order**, so derived layouts read the way the desktop looks.

### Derived breakpoints

For each section at a breakpoint with *C* board columns:

1. **Columns:** the override `span` if set, else `min(span, C)`. A section that still fits keeps its internal structure **exactly**. Only sections wider than the screen are narrowed.
2. **Order:** the override `order` if set, else the canonical order.
3. **Sizes:** the override size if it's supported and fits. Otherwise the canonical size, if it fits. Otherwise the supported size closest in shape (**same height first, then closest width**), so a hero becomes large and a long strip becomes a strip. If no supported size fits, the closest size in the whole system is used and the item is flagged `forced: true`. A glyph fits any grid, so there is always an answer.
4. **Positions:** dense Z-reflow of that order and those sizes.

Sections then flow across the board in Z order, like CSS grid's default *sparse* auto-placement. A section that doesn't fit in the rest of the current band starts a new band, and nothing back-fills. Each placed section reports its `band`, its `column` and its height in `rows`.

Derivation is a pure function of the board, so it's deterministic. Results are never stored.

### Flows

The board's `flow` decides where sections and tiles sit on the board. It never changes a tile's size, so switching flows resizes nothing. Positions inside a section change only in a bento block that narrowed to fit.

| Flow | What it does |
|---|---|
| `sections` (default) | The sparse bands above. Each band is as tall as its tallest section, so a short section beside a tall one leaves a hole under it. |
| `bento` | Each section is a block: one header track, then two tracks per row. Blocks are packed by the same dense first-fit as tiles, in Z order, so a later section moves up into a hole that fits it. A block is only as wide as its tiles, and it may narrow, from its span down to its widest tile, with its tiles repacked at that width: it takes the width that lands earliest (ties go to the wider). |
| `mosaic` | No blocks. Every tile is packed onto the whole board by the same rule, section after section. A section's header is a label glued to the top of its first tile: the two pack as one block, as wide as the tile or `MOSAIC_LABEL_COLUMNS` (2), so a label never drifts into a gap of its own. |

Bento and mosaic work on **tracks**, each half a unit row (`TRACKS_PER_ROW` is 2). Two tracks and the gap between them are exactly one unit, so a tile `h` rows tall covers `2h` tracks and lines up with every other tile, and a header takes one track instead of a whole row. `deriveLayout` reports each section's `area` (bento), `header` (bento and mosaic) and each item's `area`, plus the board's total `tracks`.

This is not masonry. Masonry places by measured heights and reshuffles when content or width changes. Both flows are pure functions of order and sizes, like everything else here: the same board gives the same picture, and a change to one section never moves a section before it (property-tested). See [ADR-0030](../../docs/adr/0030-board-flows.md).

`deriveLayout(board, breakpoint, options)` takes options for renderers:
- `flow`: lay out with another flow than the board's. The editor shows a mosaic as bento, so tiles stay in their sections while you move them.
- `headers`: `auto` (the default) gives a header track to titled sections only, `always` to every section, for a renderer that always draws a header.
- `minRows`: the fewest rows a bento block keeps, so an empty section can still take a drop while editing.
- `fit`: whether bento blocks may narrow and trim (default `true`). An editor passes `false`, so tiles sit at their stored positions and drops map onto them.

---

## API

Every operation is pure. Programmer errors throw `LayoutError` with a `code` (see [Errors](#errors)).

### Construction and queries

| Function | Description |
|---|---|
| `createBoard(input?)` | Build a board from `{ sections: SectionInput[] }`. Items don't need positions, and any `x`/`y` you pass is ignored. Validates everything and throws `INVALID_BOARD` listing every problem. Stale override references (unknown ids, unsupported sizes) are dropped; partial override orders are completed. |
| `findItem(board, id)` | `{ section, sectionIndex, item, index }`, or `undefined`. |
| `getItem(board, id)` / `getSection(board, id)` | Lookups. |
| `reflow(board)` | Recompute every canonical position from order. Only needed for boards assembled by hand. |
| `checkInvariants(board)` | Every invariant, checked from scratch. Returns a list of problems (empty means valid). Covers ids, sizes, positions equal to the reflow, overrides in normal form, and every derived breakpoint in bounds and overlap-free. |
| `checkLayout(board, layout)` | The same checks for one `DerivedLayout`. |

### Item operations

All of these accept `{ breakpoint }` where noted. **`desktop` (the default) edits the canonical layout; `tablet`/`phone` write overrides and never touch the canonical layout.**

#### `place(board, item, { sectionId, x, y })`
Add a new item as close as possible to a target cell, for example a drop from the widget gallery. If its size is too wide for the section, the closest supported size that fits is used.

```ts
board = place(board, { id: 'dns', type: 'pihole', size: 'small', supportedSizes: ['small', 'wide'] }, { sectionId: 'infra', x: 4, y: 0 });
```
Throws: `DUPLICATE_ID`, `UNKNOWN_SECTION`, `UNSUPPORTED_SIZE` (size not in `supportedSizes`), `NO_FIT`, `INVALID_ARGUMENT`.

#### `smartInsert(board, item, { sectionId?, near? })`
Add a new item without a target and let the engine find the best spot.
- **No hints:** the first section where it fits, in the earliest gap that fits (or at the end). **Nothing else moves.**
- **`sectionId`:** only that section.
- **`near: { sectionId, y }`:** a viewport hint, meaning the section and canonical row the user is looking at. The item lands as close to that row as possible; among equally close spots, it takes the one that displaces the fewest items. If that section can't take the item, the nearest section that can is used.

```ts
board = smartInsert(board, newItem, { near: { sectionId: 'media', y: 6 } });
```
Throws: `NO_SECTION`, `NO_FIT`, `UNKNOWN_SECTION`, `DUPLICATE_ID`, `UNSUPPORTED_SIZE`, `INVALID_ARGUMENT`.

#### `move(board, id, { sectionId?, x, y }, { breakpoint? })`
Move an item so its top-left lands as close as possible to the target; neighbours reflow. At `desktop`, `sectionId` may name another section, and the item adopts the closest supported size that fits there. At `tablet`/`phone`, the move stays inside the section and is stored as an override order; moving everything back to the derived order clears the override.

```ts
board = move(board, 'nas', { x: 8, y: 0 });                         // reorder
board = move(board, 'nas', { sectionId: 'storage', x: 0, y: 0 });   // another section
board = move(board, 'cams', { x: 0, y: 0 }, { breakpoint: 'phone' }); // phone-only order
```
Throws: `UNKNOWN_ITEM`, `UNKNOWN_SECTION`, `NO_FIT` (no supported size fits the destination), `CROSS_SECTION_AT_BREAKPOINT`, `INVALID_ARGUMENT` (NaN).

#### `resize(board, id, size, { breakpoint? })`
Resize to one of the item's supported sizes. The item keeps its place in the order and the section reflows. At `tablet`/`phone` it stores a size override, or clears it when the size equals what derivation would pick anyway.

```ts
board = resize(board, 'pve-1', 'large');
board = resize(board, 'cams', 'wide', { breakpoint: 'phone' });
```
Throws: `UNSUPPORTED_SIZE` (not declared by the widget), `NO_FIT` (wider than the section at that breakpoint), `UNKNOWN_ITEM`.

For the corner resize handle, map the pointer to a free-form w×h and snap it first: `snapSize(item.supportedSizes, w, h, columns)`. For Shift+arrows, use `stepSize(item.supportedSizes, item.size, 'wider' | 'narrower' | 'taller' | 'shorter', columns)`. Both return only supported sizes that fit, or `undefined`. `sectionColumns(section, breakpoint)` gives `columns`.

#### `remove(board, id)`
Remove an item, along with any override entries that mention it. Throws `UNKNOWN_ITEM`.

#### `nudge(board, id, direction, { breakpoint? })`
One keyboard step (arrows).
- `left`/`right` swap the item with its neighbour in Z order.
- `up`/`down` move it to the nearest spot in the row above or below.
- At a section's edge on `desktop`, it crosses into the previous section (appended) or the next one (first), if it fits there. At derived breakpoints it stops at the edge.

Returns the same board when there is nowhere to go.

### Section operations

| Function | Description |
|---|---|
| `addSection(board, input, index?)` | Insert a section (at the end by default; the index is clamped). Throws `DUPLICATE_ID` or `INVALID_ARGUMENT`. |
| `removeSection(board, id)` | Remove a section and its items. |
| `moveSection(board, id, toIndex)` | Move a section to an index (clamped). |
| `setSectionTitle(board, id, title?)` | Set or clear (with `undefined` or `''`) the title. |
| `setFlow(board, flow)` | Set the board's flow (`sections`, `bento`, `mosaic`). `sections` is stored as no flow at all. Returns the same board when nothing changes; throws `INVALID_ARGUMENT` for an unknown flow. |
| `setSectionSpan(board, id, span, { breakpoint? })` | At `desktop`, change the canonical span (1–12) and repack. Refuses with `NO_FIT` if an item would no longer fit; resize it first. At `tablet`/`phone`, store a span override (1–*C*), or clear it when it equals the derived default. |
| `clearOverrides(board, { sectionId?, breakpoint? })` | Drop overrides so those layouts are fully derived again. |

### Derivation

| Function | Description |
|---|---|
| `deriveLayout(board, breakpoint, options?)` | The full placed layout at a breakpoint, in the board's flow. `desktop` returns the canonical layout. Options: `flow`, `headers`, `minRows` (see [Flows](#flows)). |
| `deriveAll(board, options?)` | `{ desktop, tablet, phone }`. |
| `sectionColumns(section, breakpoint)` | Internal columns of a section at a breakpoint. |
| `sectionOrder(section, breakpoint)` | Items in Z order at a breakpoint. |
| `itemSize(section, item, breakpoint)` | `{ size, forced }` at a breakpoint. |

**Renderers:** use each placed section's `items` order as DOM and focus order; it's the same order `nudge` walks. With dense back-fill, a later item can sit visually before an earlier one, but the order stays stable and predictable.

### History

A generic, immutable undo/redo stack with a cap. Each entry is a board reference, so with structural sharing an entry costs only what changed.

```ts
let h = createHistory(board, { limit: 100 }); // limit = max undo steps (default 100)
h = commit(h, next);       // clears redo; committing the same reference is a no-op
h = undo(h);               // h.present is the exact previous object
h = redo(h);
canUndo(h); canRedo(h);
```

Drag previews don't need history. Keep the preview board in the renderer and `commit` once on drop.

### Sizes

| Function | Description |
|---|---|
| `SIZES`, `SIZE_IDS` | The size table and its canonical order. |
| `isSizeId(value)`, `sizeFits(size, columns)`, `sizeLabel(size)` | Helpers. |
| `snapSize(supported, w, h, columns)` | The nearest supported size that fits (for the resize handle). Ties go to the smaller size. |
| `stepSize(supported, current, step, columns)` | The next supported size in a direction (for the keyboard). |
| `closestFit(candidates, preferred, columns)` | The closest shape that fits (same height first, then width). |
| `autoSize(size, supported, columns)` | The narrow-grid fallback, `{ size, forced }`. |

### Errors

`LayoutError` has a `code`:

| Code | When |
|---|---|
| `INVALID_BOARD` | `createBoard` input is malformed (the message lists every problem). |
| `INVALID_DOCUMENT` | `fromDocument`/`deserialize` input is malformed. |
| `UNSUPPORTED_VERSION` | The document is newer than the engine, or no migration exists for its version. |
| `INVALID_ARGUMENT` | A NaN target, an out-of-range span, a malformed new item. |
| `DUPLICATE_ID` | The item or section id is already on the board. |
| `UNKNOWN_ITEM` / `UNKNOWN_SECTION` | The id isn't on the board. |
| `UNSUPPORTED_SIZE` | The widget doesn't declare that size. |
| `NO_FIT` | No (supported) size fits the section at that breakpoint. |
| `NO_SECTION` | `smartInsert` on a board without sections. |
| `CROSS_SECTION_AT_BREAKPOINT` | A move to another section at `tablet`/`phone`. Section membership is canonical. |

---

## Serialization

The hub stores layouts in SQLite as JSON. The format is versioned:

```json
{
  "version": 1,
  "columns": 12,
  "flow": "bento",
  "sections": [
    {
      "id": "infra",
      "title": "Infrastructure",
      "span": 8,
      "items": [
        { "id": "pve-1", "type": "proxmox.node", "size": "large", "supportedSizes": ["wide", "large"], "x": 0, "y": 0 },
        { "id": "nas", "type": "unraid.array", "size": "wide", "supportedSizes": ["small", "wide", "large"], "x": 4, "y": 0 }
      ],
      "overrides": { "phone": { "order": ["nas", "pve-1"], "sizes": { "pve-1": "wide" } } }
    }
  ]
}
```

| Function | Description |
|---|---|
| `toDocument(board)` / `serialize(board, { space? })` | Write the current version. Key order is fixed and override sizes are sorted by item order, so **equal boards produce identical strings**. |
| `fromDocument(value, { migrations? })` / `deserialize(json, { migrations? })` | Read any supported version. Throws `INVALID_DOCUMENT` or `UNSUPPORTED_VERSION`. |
| `LAYOUT_VERSION` | The current version (1). |
| `MIGRATIONS` | Built-in migrations, keyed by the version they upgrade *from*. Empty while version 1 is the only format. |

- **Round trip:** `deserialize(serialize(b))` deep-equals `b`, and serializing again gives the identical string. Property-tested.
- **Positions are informational.** `x`/`y` are written for readers that don't run the engine, such as server-side snapshots. On load they're recomputed from item order, which is authoritative.
- **Migration hook.** A migration receives a plain object with `version: N` and must return one with `version: N + 1`. Pass extra or replacement migrations through `options.migrations`; they're consulted before the built-in ones. Documents from a newer version are refused rather than guessed at.
- **Flow** is written only when it isn't `sections`, so a board in the default flow serializes exactly as before. An unknown flow (a newer app's) reads as `sections` rather than failing the board.
- Unknown keys are ignored. Stale override references are dropped on load.

---

## Decisions

Non-obvious choices made in this spike. Each one is easy to revisit.

1. **Item order is the source of truth, not positions.** A dense packing can't be reconstructed from its positions: re-packing items sorted by reading order changes about 2% of random layouts (measured while designing this). So the engine stores order and derives positions, and overlaps are impossible by construction.
2. **Dense first-fit, identical to CSS `row dense`.** This gives predictable reflow, prefix stability, and a renderer that could fall back to plain CSS grid and get the same picture. The trade-off is **no intentional gaps**. A spacer widget can add them later if people ask. A new item without a target always lands in the earliest gap that fits; a target or a `near` hint is how you put it elsewhere.
3. **Drop-target semantics:** nearest landing cell by Euclidean distance to the requested top-left. Ties within a run of equal cells keep the current index, or else follow reading order (see [Drop targets](#drop-targets)).
4. **Size names.** The experience doc calls both 4×1 and 8×1 "Strip". They're `strip` and `strip-long` here.
5. **Narrow-grid fallback** keeps the height first, then the closest width (hero → large, long strip → strip), using supported sizes first. When nothing supported fits, the engine uses a system size and flags it `forced` rather than failing; the renderer decides how to show that. **Recommendation:** widgets that support `hero` should also support `large`, and `strip-long` should come with `strip`.
6. **Sections:** `span` is both the desktop width and the internal column count. Derived span is `min(span, C)`, so sections that fit are untouched and only too-wide ones are re-flowed. By default sections flow in sparse bands (no back-fill), like CSS grid's default auto-placement; the `bento` and `mosaic` flows pack them densely instead ([Flows](#flows), ADR-0030).
7. **Overrides** live per section and per derived breakpoint: span, order and sizes. Stored orders are complete permutations, and items added later slot in after their canonical predecessor. Writing an override that equals the derived default clears it. An order that merely *becomes* equal to the canonical order after canonical edits is kept, so a phone customization is never silently lost.
8. **Section membership is canonical.** Cross-section moves only happen at `desktop`. They adopt the closest supported size that fits the destination. `setSectionSpan` at `desktop` refuses rather than resizing items behind your back.
9. **Errors vs clamping.** Unknown ids, unsupported sizes and malformed input throw. Pointer-ish input (targets, indices) is clamped. No-ops return the same object, so `commit` ignores them.
10. **Item ids are unique across the whole board**, not just within a section, so moves and overrides never collide.
11. **History is generic**: `limit` counts undo steps, with a default of 100. "Layout history" (restoring any past saved version) belongs to the hub, which stores versions; this stack is the in-session ⌘Z.
12. **Tooling:** source-only package (no build step), `.ts` import extensions (`allowImportingTsExtensions`), and strict flags including `exactOptionalPropertyTypes` and `noUncheckedIndexedAccess`. `bun add` resolved TypeScript 7.0.2 (the native compiler).

### For the ADR-0013 comparison

What the comparison can measure against this package:
- **Per-operation latency.** Run `LAYOUT_BENCH=1 bun test test/perf.test.ts`. On the development machine, every 60-item operation took well under 1 ms (a smart insert with a viewport hint, the worst case, took about 0.2 ms), against a test budget of a quarter frame (4.2 ms).
- **Size.** About 2,100 lines of source and zero dependencies. Bundle size needs a build, so it's measured with the renderer in 2.3.
- **Behaviour.** The scenarios in `test/operations.test.ts` and `test/derive.test.ts` (full row, wider-than-phone items, cross-section moves, phone overrides) are written so the same cases can be replayed against any other engine.

---

## Deferred

- **Vue renderer** (2.3): pointer drag with momentum projection, springs, the corner handle, edit mode. This package already provides the pieces the renderer needs: `move` for previews, `snapSize`/`stepSize`, `nudge`, and `deriveLayout`.
- **Wall/TV (16 columns).** Adding it means one entry in `BREAKPOINT_COLUMNS` plus the derived-breakpoint list. Sections would keep their canonical structure, since `min(span, 16) = span`, and flow across wider bands.
- **The "Needs attention" system section.** It mirrors tiles at view time and isn't part of the stored layout.
- **Per-device overrides**, beyond the per-breakpoint ones here.
- **Intentional gaps or spacer widgets.**
- **YAML export.** It would map 1:1 to the JSON document.

---

## Development

```sh
bun install                         # from the repo root
bun test                            # in packages/layout (or `bun run test` at the root)
bunx tsc --noEmit -p tsconfig.json  # in packages/layout (or `bun run typecheck` at the root)
LAYOUT_RUNS_SCALE=25 bun test       # deeper property soak
LAYOUT_BENCH=1 bun test test/perf.test.ts
```

| Test file | What it covers |
|---|---|
| `test/properties.test.ts` | fast-check properties over random boards and random operation sequences, checked against independent oracles: cell painting for overlaps, and a reference CSS dense packer |
| `test/*.test.ts` | Unit tests for each module |
| `test/helpers.ts` | Builders, arbitraries and the random-operation runner |
