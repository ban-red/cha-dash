# ADR-0013 · Grid engine: build our own

**Status:** proposed · **Date:** 2026-10-03 · **Roadmap:** [decision 13](../plan/04-roadmap.md#decisions) · **Evidence:** [spike 2.4 findings](../../spikes/grid-compare/README.md#findings)

## Context

"The layout engine *is* how the product feels" ([03 · Architecture](../plan/03-architecture.md#tech-stack)). [02 · Experience](../plan/02-experience.md#layout-system) asks a lot of the grid: widgets snap only to the sizes they declare, sections reflow in Z order, phone and tablet layouts are derived from one canonical layout, drops are projected from momentum onto springs, and the grid is fully keyboard-editable with reduced motion honoured, as a release gate. M0 built our own engine (`@chadash/layout`) and Vue renderer (`@chadash/layout-vue`). Spike 2.4 replayed seven scenarios on the dogfood front page against them, against gridstack 14.0.0 (released 2026-09-21) in three configurations, and briefly against dnd-kit/dom 0.5.

## Decision

**We build our own grid: `@chadash/layout` and `@chadash/layout-vue` are the engine and renderer for v0.1. We don't adopt gridstack or dnd-kit/dom.**

The evidence, from the [spike](../../spikes/grid-compare/README.md#results):
- **Packing is no longer a difference.** gridstack 14's new `compact` mode landed on the same cells as ours for the drag, the resize, the targeted insert and the 8- and 4-column derivations. Its default mode opened a 32-cell hole in one drag.
- **Declared sizes can't be expressed.** A min/max box lets the 15 dogfood widgets reach 93 sizes, 35 of them declared. Snapping is app code, after release.
- **There's no canonical layout.** One phone-only move rewrote 7 desktop widgets. `save()` keeps only the widest layout. Ours stores phone and tablet edits as overrides.
- **Order comes from positions.** Growing a back-filled Small pushed a Large tile across its section. In compact mode, 42% of random drags rearrange after release, because the preview uses a different flow from the drop.
- **Keyboard editing, ARIA and reduced motion are absent, and motion is a fixed 0.3 s CSS transition.** All four are requirements we already meet.
- **Sections are nested sub-grids** whose height is DOM-measured. In Chromium the boxes came out a row too tall, and with animation on the cells were mis-sized after a column change.
- **Cost doesn't decide it.** +33.3 kB gzip over Vue for ours (15.1 kB without motion-v, which the app already ships) against +29.1 kB for gridstack with its Vue wrapper. Both are far inside a frame at 60 widgets (ours 0.1–22 µs per operation, gridstack's engine 14–260 µs). Every licence involved (MIT, 0BSD) is compatible with AGPL-3.0-or-later.

## Consequences

- We own the hard parts gridstack has had years to harden: **touch long-press and auto-scroll** (deferred in layout-vue, due with the phone feel check, 2.5), drag-in from the gallery (`place` and `smartInsert` exist), and wall (16 columns).
- The spike's scenarios and tests stay runnable (`spikes/grid-compare`): re-run them on a new gridstack major before revisiting this, and borrow the scenario data for the engine's own regression tests.
- `@chadash/layout-vue` declares `@vueuse/core` but never imports it; dropping it removes 4 of its 10 transitive dependencies.
- The grid's quality bar is ours to keep: the property tests against CSS `row dense`, keyboard and live-region tests, and the 60 fps check still to come.

## Alternatives considered

- **gridstack 14, default** (top gravity, scaled columns): holes after a drag, sizes outside the system (3×4, 3×2) at 8 and 4 columns, derivation that depends on the path taken.
- **gridstack 14, compact with sections as sub-grids**: the best fit, matching our packing. It still lacks declared sizes, a canonical layout, stable order, keyboard, ARIA, reduced motion and springs, which we'd build around an engine that owns positions and the DOM. Its official Vue wrapper works, but the layout lives in GridStack, not in Vue state.
- **dnd-kit/dom as our drag layer**: pre-1.0 drag primitives with no layout engine, keyboard moves in 10 px steps, no springs or momentum. +32.9 kB and 6 dependencies for things layout-vue already does. Worth another look after 1.0.
