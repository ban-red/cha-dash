# Spike 2.4 · Grid engine comparison

Throwaway code that answers one question for [ADR-0013](../../docs/adr/0013-grid-engine.md): **do we keep our own layout engine, or adopt gridstack 14 or dnd-kit/dom?** It replays the same seven scenarios against our engine ([`@chadash/layout`](../../packages/layout/README.md) and [`@chadash/layout-vue`](../../packages/layout-vue/README.md)) and against gridstack 14, checks every state, and measures what each costs. dnd-kit/dom gets the short version, because it has no layout engine.

Not product code. The findings go into the notes below and the ADR.

**Verdict: build.** gridstack 14's new `compact` mode reproduces our packing inside a section, which we didn't expect. Everything else the experience doc asks of the grid, it doesn't do: declared sizes only, one canonical layout with derived breakpoints, keyboard editing, ARIA, reduced motion, springs and momentum. Adopting it would mean building those on top of an engine that owns the positions and the DOM.

Contents: [Method](#method) · [Run it](#run-it) · [Results](#results) · [Findings](#findings) · [Where gridstack wins](#where-gridstack-wins) · [Limits of this spike](#limits-of-this-spike) · [Files](#files)

---

## Method

### What's compared

| Option | Version | How it's driven |
|---|---|---|
| **Ours** | `@chadash/layout` + `@chadash/layout-vue` at this commit | Pure engine functions; the renderer through `<LayoutBoard>` |
| **gridstack, default** | 14.0.0 (released 2026-09-21) | `GridStack` DOM API in happy-dom. `mode: 'top'` (gravity), `moveScale` on column change |
| **gridstack, top + list** | 14.0.0 | Same, with `layout: 'list'` on column change, a common responsive setup |
| **gridstack, best fit** | 14.0.0 | v14's `mode: 'compact'` inside sections, sections flowing as a `list`. The closest gridstack gets to our model |
| **dnd-kit/dom** | 0.5.0 (pre-1.0) | Source, a happy-dom check and a bundle entry only. It has no packing, sizes or columns to run scenarios against |

Each adapter drives its engine only through the public API, the way an app would ([`adapters/`](adapters)). gridstack gets our model mapped as well as it allows:

- **Sections** are nested sub-grids with `column: 'auto'`, the only section concept gridstack has.
- **Declared sizes** become `minW/maxW/minH/maxH`, its only size constraint.
- **Undo** is app code: a stack of `save()` snapshots restored with `load()`.

### Scenarios

Codified as data in [`scenarios/`](scenarios). Every scenario starts from the dogfood front page: the shape `web/src/model/defaultLayout.ts` builds, 15 widgets in 4 sections with the sizes `web/src/model/widgets.ts` declares. Every section is packed solid, so the board has no free cell. One change from the web catalog: Now playing also declares Large, so scenario 3 has a Wide widget that can grow.

| # | Scenario | Steps |
|---|---|---|
| 1 | The dogfood front page | Load |
| 2 | Drag a Large tile across a section | Fleet (Large) to the right half of Infrastructure |
| 3 | Resize Wide → Large next to a full row | Now playing grows beside the full Sonarr/Radarr row |
| 4 | Insert into a full board | A Small with no hint, then one dropped at Infrastructure (4,2) |
| 5 | Derive the 8- and 4-column layouts | 12 → 8 → 4, plus 12 → 4 directly, back to 12, and a phone-only edit |
| 6 | Undo after 10 operations | Moves, a cross-section move, resizes and an insert, then 10 undos |
| 7 | Serialize and restore | A desktop move and a phone-only move, then serialize → restore → serialize |

### Checks

After every step, [`harness/checks.ts`](harness/checks.ts) paints cells and counts, independently of any engine:

- **Overlaps** and **out-of-bounds** widgets or sections.
- **Empty cells** inside each section's used rows. Trailing space in the last row counts too, so compare engines on the same content.
- **Undeclared sizes**, and **off-system sizes** (a w×h that isn't in the size system at all, like 3×2).
- **Overflow**: a section box shorter than its content.
- **Moved**: widgets whose cell or size changed since the last state at the same column count.

[`test/properties.test.ts`](test/properties.test.ts) adds 2,000 random sections against an independent CSS `row dense` packer and 2,000 random drags. [`test/facts.test.ts`](test/facts.test.ts) checks keyboard, ARIA, motion and dependencies in the published files and in a DOM. [`test-vue/renderer.test.ts`](test-vue/renderer.test.ts) mounts our `<LayoutBoard>` and gridstack's official Vue wrapper side by side on the dogfood page.

**What happy-dom can't do, emulated with gridstack's own code paths.** A browser's ResizeObserver tick makes each sub-grid follow its section's width (`onResize()`); we call that method directly. `sizeToContent` measures the sub-grid to size its section; we set the section's height to `subGrid.getRow()`. That's the ideal case, so the [browser check](#browser-check) below found problems the headless runs can't.

---

## Run it

```sh
cd spikes/grid-compare
bun install            # standalone: not in the root workspaces; isolated linker like the root
bun run test           # bun test (engine level, 35 tests) + vitest (renderers, 9 tests)
bun run typecheck      # vue-tsc, strict, our packages from source
bun run report         # the behaviour table below; VERBOSE=1 prints every layout
bun run bench          # 60-widget operation timings
bun run bundle         # min + gzip sizes of minimal Vue pages (vite build into dist/bundle/)
bun run deps           # runtime dependency trees and licences
bun run page           # builds the side-by-side page into dist/page/index.html
```

**The side-by-side page.** `bun run page`, then open `spikes/grid-compare/dist/page/index.html` straight from disk in any browser (it's one self-contained file; nothing is served or fetched). Both boards share the controls: 12/8/4 columns, edit mode, undo, reset, full or reduced motion. On our side, try a flick, the corner handle, and the keyboard (Tab to the board, Enter, arrows, Shift+arrows, Esc). On gridstack's, untick the snap box and resize a widget to a size it doesn't declare (the tile turns red), and switch columns with full motion on. `dist/` is git-ignored.

---

## Results

Run on 2026-10-03 with Bun 1.4.2 on an Apple-silicon laptop. Raw output: `bun run report`, `bun run bench`, `bun run bundle`.

### Behaviour on the scenarios

| Scenario | Ours | gridstack, default | gridstack, best fit (compact) |
|---|---|---|---|
| 1 · Front page | Packed, no problems | Same | Same |
| 2 · Drag a Large across Infrastructure | Lands at (4,0); 4 widgets reflow; the section stays 6 rows with 0 empty cells | 7 widgets move; a 4×8 hole opens; the section grows from 6 to 10 rows (**32 empty cells**) | **Same cells as ours** |
| 3 · Resize Wide → Large beside a full row | 3 reflow, 8 trailing empty cells | 2 move, 8 empty cells (a different, valid layout) | **Same cells as ours** |
| 4 · Insert into a full board | No hint: end of Home, nothing else moves. At (4,2): 5 reflow | Same landing cells; push-down moves 2 | Widgets land like ours, but **Media jumps beside Home and Activity below it** when Home grows |
| 5 · Derive 8 and 4 columns | Declared sizes only; 12→4 equals 12→8→4; back to 12 restores; **a phone edit leaves desktop alone** | **9 off-system sizes** (3×4, 3×2, 2×4…); 12→4 ≠ 12→8→4; **a phone edit moves 7 desktop widgets** | Same layouts as ours, but **a phone edit moves 7 desktop widgets** |
| 6 · Undo after 10 operations | 10/10 exact, built in | 10/10 exact, with app-kept snapshots | 10/10 exact; 7 of the 10 intermediate layouts differ from ours (see finding 2) |
| 7 · Serialize and restore | Identical string; the phone tweak survives | `save()` keeps only the 12-column layout; the restored phone layout differs for all 15 widgets | Identical string; the phone tweak survives only because it had already rewritten desktop |

No engine ever produced an overlap, an out-of-bounds widget or a clipped section in the headless runs. *top + list columns* behaves like the default on scenarios 2–4 and 6, keeps declared sizes on 5, and restores a different string on 7 (`bun run report`).

### Capabilities

| | Ours | gridstack 14 | dnd-kit/dom 0.5 |
|---|---|---|---|
| **Declared sizes** | Only declared sizes, everywhere: resize, the corner handle (snaps live), Shift+arrows, derivation (closest declared shape, or flagged `forced`) | A min/max box: the 15 dogfood widgets can reach **93 sizes, 35 of them declared**. Snapping is app code on `resizestop`, after release | No sizes |
| **Overlaps** | Impossible: positions are derived from order | None seen | — |
| **Gaps** | Dense Z-reflow, identical to CSS `row dense` on 2,000/2,000 random sections | Default: one drag opens a 32-cell hole. Compact: matches CSS `row dense` on 1,984/2,000 | — |
| **Breakpoints** | Derived per section, a pure function of the canonical layout; per-breakpoint overrides | Column modes. `moveScale` (default) scales to off-system sizes and depends on the path. `list`/`compact` keep sizes. One layout per column count; small-column edits rewrite the 12-column one | — |
| **Sections** | First-class: spans, bands, cross-section moves, per-section derivation, widgets stay mounted across sections | Nested sub-grids. Section height needs `sizeToContent` (DOM-measured); see the [browser check](#browser-check) | Droppable containers |
| **Keyboard** | Pick up, arrows, Shift+arrows to resize, Esc to cancel, roving tabindex | **None.** Keys only during a pointer drag: Esc cancels, R rotates | Keyboard sensor in 10 px steps, not cells |
| **ARIA** | Group, `button` with a roledescription, labels with the size, instructions, a polite live region for every step | **None** in the core or the Vue wrapper | Role, tabindex, roledescription, describedby, live region |
| **Reduced motion** | Crossfades (the `fade` token); follows `data-motion`, then the OS | **Ignored**: the 0.3 s transition stays; the app must call `setAnimation(false)` | Drop animation drops to 0 ms |
| **Momentum, springs** | Release velocity projected (the WWDC18 formula); Motion springs with the token physics; interruptible FLIP | **None**: a fixed 0.3 s CSS transition of left/top/width/height. In compact mode the drag previews with list flow and compacts on release, so **42% of random drops jump after the pointer lets go** A 250 ms keyframe drop animation (replaceable); no springs, no momentum |
| **Vue 3.6** | Vue-native: board in, `commit` out, immutable boards in a `shallowRef`, history built in | An **official Vue 3 wrapper** (since v13). GridStack owns the layout: options only seed it, Vue hears about changes through events, widgets are teleported into its DOM | `@dnd-kit/vue` 0.5 exists |
| **Status** | Ours, M0 | Mature, 14.0.0, MIT, no dependencies | Pre-1.0 (0.5.0, frequent betas), MIT |

### Cost

**Bundle**, min + gzip, of a minimal Vue page that renders the dogfood board with editing and undo ([`bundle/`](bundle), one `vite build` each, no code splitting):

| Entry | JS gzip | CSS gzip | Over Vue alone |
|---|---:|---:|---:|
| Vue 3.6 alone | 23.7 kB | — | — |
| `@chadash/layout` alone, no Vue | 6.4 kB | — | — |
| Vue + motion-v springs (what our renderer uses) | 41.9 kB | — | +18.2 kB |
| **Vue + our engine + renderer** (includes motion-v) | 55.6 kB | 1.4 kB | **+33.3 kB** |
| **Vue + gridstack 14 with its Vue wrapper** | 51.4 kB | 1.4 kB | **+29.1 kB** |
| Vue + gridstack 14, imperative | 48.7 kB | 1.4 kB | +26.5 kB |
| Vue + dnd-kit/dom sortable (drag layer only, no layout) | 56.6 kB | — | +32.9 kB |

motion-v is most of our cost, and `web/` already ships it (Inbox, dialogs). Counting it as shared, our engine and renderer add **15.1 kB**; counting it, gridstack is 4.2 kB smaller.

**Timings**, 60 widgets in 4 sections, engine level with no DOM, median µs ([`bench/timings.ts`](bench/timings.ts)). gridstack's engine mutates in place, so each unit returns to its start (there and back) for both:

| Operation | Ours | gridstack 14 engine |
|---|---:|---:|
| Load the board | 9.5 | 196 |
| Drag a Large across its section, there and back | 2.5 | 113 |
| Drag to another section, there and back | 3.9 | 91 |
| Resize Wide ↔ Small | 1.3 | 144 |
| Insert + remove a widget | 1.9 | 63 |
| Derive 4 and 8 columns (gridstack: 12→4→8→12) | 7.1 | 260 |
| Undo one step (gridstack: `save()` + rebuild) | 0.1 | 94 |
| Serialize | 5.1 | 14 |
| Restore | 22 | 69 |

Both are far inside a frame (16.7 ms) at this size, so speed doesn't decide anything.

**Dependencies and licences** ([`bench/deps.ts`](bench/deps.ts)), runtime, transitive, Vue excluded:

| Package | Licence | Runtime deps |
|---|---|---|
| `@chadash/layout` | AGPL-3.0-or-later | 0 |
| `@chadash/layout-vue` | AGPL-3.0-or-later | motion-v (5 more: framer-motion, motion-dom, motion-utils, tslib, hey-listen). Also declares `@vueuse/core` (4), which its source never imports |
| gridstack 14 | MIT | 0 |
| dnd-kit/dom 0.5 | MIT | 6 (four `@dnd-kit/*`, `@preact/signals-core`, tslib) |

All are MIT or 0BSD, so any of them could ship inside an AGPL-3.0-or-later app; licence doesn't decide anything either.

### Browser check

The built page, opened in Chromium (the preview pane, about 880 px wide), with gridstack configured as the best fit. These don't show up headless:

- **Full motion (gridstack's default `animate: true`): sub-grid cells come out the wrong size after a column change.** At 8 columns the cells were 45–46 px tall instead of 67–69, in two clean runs, so a 4×4 tile rendered about two-thirds as tall as it is wide. The sub-grids measure their width while the section's width is still animating, and nothing measures again. With motion reduced, the cells are right.
- **Section boxes are one row too tall** at 12 columns (7 rows for 6 rows of widgets, 5 for 4), because the parent and the sub-grids round their square `'auto'` cells differently (67 vs 68 px) and `sizeToContent` rounds up.
- Both look fixable with app code, for example a fixed pixel cell height we compute ourselves: more glue we'd own.
- Apart from those box sizes, the layouts match the headless results: sections keep their order through 12 → 8 → 4 → 12, and a drag of Fleet across Infrastructure (synthetic mouse events) landed on the same cells as ours.
- Our board kept square cells through 12 → 8 → 4 → 12 at full motion.

---

## Findings

1. **gridstack caught up on packing.** v14's `compact` mode is a dense row-major reflow, like ours. Inside a section it matched ours on the drag, the resize, the targeted insert and both derivations. It differs from CSS `row dense` on 16 of 2,000 random sections (0.8%), because it re-sorts by position after placing and packs again. Its default `top` mode is a different model: one drag opens a 32-cell hole.
2. **Order versus positions.** We store order and derive positions. gridstack re-derives the order from positions at every pack, so a widget that back-filled a gap jumps the queue for good. In the undo run, growing a back-filled Small (Containers) pushed the Large Fleet tile to the other side of Infrastructure; ours left Fleet where it was. Seven of the ten intermediate layouts differ, all downstream of that one step.
3. **The preview isn't the drop.** In compact mode gridstack previews with the cheaper list flow and compacts on release: 42% of random drags rearrange after the pointer lets go. Ours previews `move(origin, id, target)` and drops the same thing, plus the momentum projection.
4. **Declared sizes can't be expressed.** A min/max box lets the dogfood widgets reach 93 sizes where 35 are declared. Snapping to a declared size is app code, and only possible after release.
5. **There's no canonical layout.** gridstack keeps one layout per column count and writes a small-column edit back into the 12-column one: one phone move shifted 7 desktop widgets. `save()` writes only the widest layout, so a phone layout doesn't survive a round trip unless the app saves every column count. Our phone and tablet edits are overrides, and desktop never moves.
6. **Sections are the weak spot.** Nested sub-grids work, but the section box's height depends on DOM measuring (`sizeToContent`), which in Chromium came out a row too tall, and with animation on, the cells went wrong after a column change. In list mode, sections re-flow when one grows: one insert into Home moved Media beside it and Activity below it.
7. **Accessibility and motion are missing, and they're release gates.** No keyboard moving or resizing, no ARIA, no reduced-motion handling, and the only animation is a fixed CSS transition of layout properties. We'd build all of it on top, around an engine that owns the positions and the DOM.
8. **The Vue story is better than expected, but it's two sources of truth.** gridstack 14 ships an official Vue 3 wrapper that mounted the dogfood page with nested sections in happy-dom. The layout lives inside GridStack, though, and undo, saving and overrides would go through `save()`/`load()` instead of immutable boards.
9. **Cost is a wash.** gridstack is 4.2 kB gzip smaller than our full stack, or 14 kB bigger if motion-v counts as already shipped. Our engine is 3–900× faster at 60 widgets, but both are far inside a frame. Both are MIT-compatible.
10. **dnd-kit/dom isn't a contender for the engine.** It's pre-1.0 drag primitives: no packing, sizes, columns or sections. It has the accessible-drag basics we already built, moves in 10 px keyboard steps rather than cells, and has no springs or momentum. As a replacement for our pointer layer it would add 32.9 kB and 6 dependencies for things we have. Worth a look again after 1.0, if touch long-press and auto-scroll turn out to be a burden.

### Where gridstack wins

- **Maturity.** Years of browser and touch edge cases, drag-in from a sidebar (`setupDragIn`), cross-grid drag, rotate, lazy loading. Our renderer still defers touch long-press and auto-scroll.
- **Zero dependencies**, and 4.2 kB smaller if motion-v isn't already in the app.
- **An official Vue wrapper**, and with v14, our packing model for free.

None of these cover what the experience doc defines the grid by.

---

## Limits of this spike

- gridstack's drags are programmatic (`update()`, or `beginUpdate`/`moveNode`/`endUpdate` like its DOM layer), not pointer gestures. One real drag per engine was checked in the browser.
- The headless sections use ideal emulation of ResizeObserver and `sizeToContent`; the browser check shows the real thing is worse.
- Timings are engine-only. Neither side's DOM work is timed; layout-vue's own budget tests cover ours.
- No phone feel check (2.5) and no 60 fps measurement on a mid-range phone; that's still open for our renderer.
- dnd-kit/dom wasn't run through the scenarios: it has nothing to run them against.

---

## Files

| Path | What |
|---|---|
| [`scenarios/`](scenarios) | The dogfood board, the seven scenarios and the 60-widget board, as engine-neutral data |
| [`adapters/`](adapters) | Our engine and gridstack 14 behind one interface |
| [`harness/`](harness) | Cell-painting checks, the scenario runner, the engine list, happy-dom setup |
| [`test/`](test) | `bun test`: scenarios, random-section properties, keyboard/ARIA/motion/dependency facts |
| [`test-vue/`](test-vue) | Vitest: `<LayoutBoard>` and gridstack's Vue wrapper side by side |
| [`bench/`](bench) | Timings and the dependency report |
| [`bundle/`](bundle) | One minimal page per option, and the build script that sizes them |
| [`page/`](page) | The side-by-side page and its single-file build |
| [`report.ts`](report.ts) | Prints the behaviour table |
