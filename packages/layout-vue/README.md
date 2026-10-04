# @chadash/layout-vue

The Vue 3.6 renderer for [`@chadash/layout`](../layout/README.md): one component, `<LayoutBoard>`, that renders a board and edits it, plus two composables.

- **Renders, never fetches.** Widgets come from your `#item` slot. The package draws the grid, the edit-mode chrome and the motion, nothing else.
- **Edits through the engine.** Every board it emits is made by `@chadash/layout` operations (`move`, `nudge`, `resize`), so a gesture can't produce an invalid board.
- **Feels physical.** Pointer drag with momentum projection, a live reflow preview, FLIP on Motion springs with the token physics, a lifted drag style, a drop that settles with the release velocity.
- **Keyboard and screen reader first-class.** Roving tabindex, pick-up/drop with Enter and Space, arrows to move, Shift+arrows to resize, Escape to cancel, polite live announcements.

Status: M0 (track 2.3 in [06 · Next steps](../../docs/plan/06-next-steps.md)). Source-only like the engine: `"exports": "./src/index.ts"`, compiled by the app's Vite.

Contents: [Quick start](#quick-start) · [Integrating in web/](#integrating-in-web) · [API](#api) · [Behaviour](#behaviour) · [Keyboard](#keyboard) · [Accessibility](#accessibility) · [Motion](#motion) · [Styling](#styling) · [Decisions](#decisions) · [Deferred](#deferred) · [Development](#development)

---

## Quick start

```vue
<script setup lang="ts">
import { LayoutBoard, useLayoutHistory } from '@chadash/layout-vue';
import { shallowRef } from 'vue';

const { board, commit, undo, redo } = useLayoutHistory(initialBoard);
const editing = shallowRef(false);
</script>

<template>
  <LayoutBoard :board="board" :editing="editing" @commit="commit">
    <template #item="{ item, size, editing, dragging }">
      <MyWidget :type="item.type" :size="size" :dimmed="editing" :lifted="dragging" />
    </template>
  </LayoutBoard>
</template>
```

During a gesture the board previews internally, so `@commit` is all you need. If you also want every preview step (to mirror it elsewhere), bind `v-model:board`; both work together.

[`demo/DemoBoard.vue`](demo/DemoBoard.vue) is a complete page with an Edit button and undo/redo. It isn't served or built.

---

## Integrating in web/

A front page that renders widgets in the `#item` slot and persists on `commit`, against the hub's layout API (`GET`/`PUT /api/boards/{id}/layout`, optimistic on `revision`; see `internal/README.md`):

```vue
<!-- web/src/pages/FrontPage.vue -->
<script setup lang="ts">
import { LAYOUT_VERSION, createBoard, fromDocument, toDocument, type Board } from '@chadash/layout';
import { LayoutBoard, useLayoutHistory } from '@chadash/layout-vue';
import { onKeyStroke } from '@vueuse/core';
import { onMounted, shallowRef } from 'vue';
import WidgetHost from '@/widgets/WidgetHost.vue'; // renders a widget type at a size
import { widgetName } from '@/widgets/registry';   // (item: Item) => "Plex"
import { starterBoard } from '@/boards/starter';   // the first layout for a board never saved

const props = defineProps<{ boardId: string }>();

// shallowRef: boards are immutable, deep reactivity buys nothing.
const loaded = shallowRef<Board>(createBoard());
const { board, commit, undo, redo } = useLayoutHistory(loaded); // a new `loaded` starts a fresh history
const editing = shallowRef(false);
let revision = 0;

async function load() {
  const res = await fetch(`/api/boards/${props.boardId}/layout`).then((r) => r.json());
  revision = res.revision;
  loaded.value = res.layout === null ? starterBoard() : fromDocument(res.layout);
}

async function persist(next: Board) {
  commit(next); // ⌘Z history
  const res = await fetch(`/api/boards/${props.boardId}/layout`, {
    method: 'PUT',
    headers: { 'content-type': 'application/json' },
    body: JSON.stringify({ revision, format_version: LAYOUT_VERSION, layout: toDocument(next) }),
  });
  if (res.ok) revision = (await res.json()).revision;
  else if (res.status === 409) await load(); // another tab saved first: take its layout
}

onMounted(load);
onKeyStroke('e', (e) => { if (!(e.target instanceof HTMLInputElement)) editing.value = !editing.value; });
onKeyStroke('z', (e) => { if (e.metaKey || e.ctrlKey) { e.preventDefault(); e.shiftKey ? redo() : undo(); } });
</script>

<template>
  <LayoutBoard :board="board" :editing="editing" :item-label="widgetName" @commit="persist">
    <template #item="{ item, size, editing, dragging }">
      <WidgetHost :item="item" :size="size" :editing="editing" :dragging="dragging" />
    </template>
  </LayoutBoard>
</template>
```

Wiring notes:

- **Load the tokens once**, globally: `import '@chadash/tokens/tokens.css'` (or the copy at `web/src/design/tokens.css`). The component only reads custom properties.
- **Use the same Vue.** The package declares `vue` as a peer (`^3.6.0-rc.10`) and develops against exactly `3.6.0-rc.10`. Bun's isolated installs key a package's store entry on its resolved peers, so two workspaces with different TypeScript versions can end up with two copies of the same Vue. Add `resolve: { dedupe: ['vue'] }` to `web/vite.config.ts` so the app always has one runtime.
- **Undo after a save:** `undo()` returns the previous board; save it the same way if undo should reach the hub. A reload that lands mid-gesture (the 409 path, or a realtime update) is safe: the board drops the gesture without emitting.
- **Widgets render per size.** `size` is the size at the current breakpoint (a hero becomes large on a phone). `placed.forced` is true when no supported size fit and the engine borrowed one; show a compact fallback.

---

## API

```ts
import {
  LayoutBoard,
  useLayoutHistory, useBreakpoint, breakpointForWidth, BREAKPOINT_MIN_WIDTHS,
  defaultMessages, MOMENTUM, projectMomentum, projectionTime, VelocityTracker,
  SPRINGS, springTransition, motionPreference, LIFT_SCALE, EDIT_SCALE,
  type LayoutBoardProps, type ItemSlotProps, type SectionSlotProps, type LayoutMessages,
} from '@chadash/layout-vue';
```

### `<LayoutBoard>` props

| Prop | Type | Default | |
|---|---|---|---|
| `board` | `Board` | required | The board to render. Never mutated. Reactive proxies are unwrapped with `toRaw`, but prefer `shallowRef`. |
| `editing` | `boolean` | `false` | Edit mode: items lift and dim, the unit grid shows, drag/resize/keyboard editing are on. |
| `breakpoint` | `'desktop' \| 'tablet' \| 'phone'` | from width | Which layout to render and edit. Omitted: a ResizeObserver on the board picks 12, 8 or 4 columns from its width. Edits at `tablet`/`phone` become engine overrides. |
| `itemLabel` | `(item: Item) => string` | `item.id` | The name in labels and announcements ("Plex"). |
| `sectionLabel` | `(section: PlacedSection) => string` | title, then id | The section name in the default header and announcements. |
| `messages` | `Partial<LayoutMessages>` | English | Override any string (see [`messages.ts`](src/messages.ts)). |
| `motion` | `'auto' \| 'full' \| 'reduced' \| 'none'` | `'auto'` | `auto` follows `data-motion` on `<html>`, then `prefers-reduced-motion`. |

### Events

| Event | Payload | When |
|---|---|---|
| `update:board` | `Board` | After each preview step or operation: every drag preview, nudge, pointer or keyboard resize, and the restore on cancel. Supports `v-model:board`. |
| `commit` | `Board` | Once when a gesture ends with a changed board (drop, end of resize), or after a one-shot Shift+arrow resize. Record it in history and persist it. Not emitted for no-op gestures or cancels. |
| `select` | `string` (item id) | An item received focus (click in edit mode, arrow navigation, or focus entering a widget). Not re-emitted when focus is restored after a reorder. |

### Slots

**`#item`** (required) renders a widget. Props (`ItemSlotProps`):

| Prop | Type | |
|---|---|---|
| `item` | `Item` | The canonical item: `id`, `type`, canonical `size`, `supportedSizes`. |
| `size` | `SizeId` | The size shown at the current breakpoint. Lay out for this. |
| `editing` | `boolean` | Edit mode. The slot content is `inert` while editing (drag anywhere). |
| `dragging` | `boolean` | This item is picked up, dragged or resized. |
| `placed` | `PlacedItem` | `x`, `y`, `w`, `h`, `size` and `forced` at this breakpoint. |

**`#section`** (optional) replaces a section header. Props: `section` (`PlacedSection`), `editing`. With the slot, every section gets a header row; without it, bands with a titled section get the default small-caps header (an `<h2>`; use the slot for another heading level).

### `useLayoutHistory(initial, { limit? })`

Undo/redo over the engine's immutable history, as Vue state.

| Member | |
|---|---|
| `board` | `ComputedRef<Board>`: the present. Bind to `:board`. |
| `commit(next)` | Record a board (wire to `@commit`). Committing the present is a no-op. |
| `undo()` / `redo()` | Step and return the new present. |
| `canUndo` / `canRedo` | `ComputedRef<boolean>`. |
| `reset(board)` | A fresh history. |
| `history` | `ComputedRef<History<Board>>`, the raw engine history. |

`initial` may be a board, a ref or a getter. With a ref or getter, a new board that isn't the present (a layout loaded from the hub) starts a fresh history. `limit` defaults to the engine's 100 undo steps.

### `useBreakpoint(target, { minWidths?, initial? })`

`{ breakpoint, width }` for an element (template ref or getter), from a ResizeObserver, falling back to `window` resize. `breakpointForWidth(width, minWidths?)` is the pure mapping. `BREAKPOINT_MIN_WIDTHS` is `{ tablet: 641, desktop: 1041 }`, the prototype's container queries (`max-width: 640px` and `1040px`). 1040 px is about where 12 columns reach the minimum unit (12 × 76 px + 11 gutters).

### Physics exports

| Export | |
|---|---|
| `MOMENTUM` | The projection constants (see [Decisions](#decisions)). |
| `projectMomentum(velocity, pitch, options?)` | Displacement in px for a release velocity in px/s. |
| `projectionTime(rate?)` | Seconds of travel a deceleration rate projects (0.199 s for 0.995). |
| `VelocityTracker` | Least-squares release velocity over the last 100 ms of samples. |
| `SPRINGS`, `springTransition(name, velocity?)` | The motion tokens as Motion spring transitions. |
| `motionPreference()` | `'full' \| 'reduced' \| 'none'` from `data-motion`, then the OS. |
| `LIFT_SCALE`, `EDIT_SCALE` | 1.03 for a lifted item, 0.982 for items resting in edit mode. |

---

## Behaviour

### Rendering

The derived layout (`deriveLayout(board, breakpoint)`) maps onto **one flat CSS grid**. A section's internal columns are exactly the board columns it covers, so an item sits at board column `section.column + x`. Each band of sections gets an optional header track, its unit rows, and a spacer track, so the gap between bands is `--section-gap` while everything else is `--gutter`. Unit rows are square: `--layout-unit` is the column width, computed with container query units on the board.

Because every item is a direct child of the same grid, keyed by id, **an item that moves to another section keeps its DOM node and its mounted widget**. DOM and focus order is the engine's: section by section, each section in its Z order.

### Pointer drag

1. Press anywhere on an item in edit mode. After 4 px of travel it lifts (scale 1.03, `--shadow-overlay`) and follows the pointer exactly.
2. The cell under the item's top-left (in the section nearest its centre) is the preview target. Each new target computes `move(origin, id, target)` **from the board the gesture started on**, so previews never accumulate drift. Neighbours reflow on springs, and a dashed outline shows the landing slot.
3. On release, the velocity of the last 100 ms is **projected** ([constants](#decisions)) and the projected cell is the drop. The item springs from where it is into its slot on `lift`, starting with the release velocity, so a throw carries through.
4. Sections: at `desktop` an item can move into any section where one of its supported sizes fits (it adopts the closest size). At `tablet`/`phone` the drag stays in its section, because section membership is canonical (`CROSS_SECTION_AT_BREAKPOINT`).
5. If the engine refuses a target, the last good preview stays. Escape, `pointercancel` or losing the window cancel: `update:board` restores the origin and nothing is committed.

### Corner resize

The handle (bottom right, labelled "4×2") maps pointer travel to a free-form `w × h` in cells and snaps it with `snapSize(item.supportedSizes, w, h, columns)`. Each snap previews `resize(origin, id, size)`; a dashed outline follows the pointer between snaps. Release commits; Escape restores.

### External changes

If the `board` prop changes to something other than the component's own last emission during a gesture (another tab saved, an undo), the gesture is dropped without emitting anything and the change is announced.

---

## Keyboard

Edit mode only. Items are one tab stop (roving tabindex); the board is a labelled group.

| Keys | Nothing picked up | Item picked up |
|---|---|---|
| ←/→ | Focus the previous/next item (DOM order, the order `nudge` walks) | `nudge` left/right (swap in Z order; crosses sections at the edge on desktop) |
| ↑/↓ | Focus the nearest item entirely above/below | `nudge` up/down (nearest spot in the row above/below) |
| Shift+→ / Shift+← | Resize wider/narrower now (`update:board` + `commit`) | Resize as part of the pick-up |
| Shift+↓ / Shift+↑ | Resize taller/shorter now | Resize as part of the pick-up |
| Enter / Space | Pick up | Drop (`commit` if changed) |
| Escape | Not handled (left to the app, e.g. to leave edit mode) | Cancel: restore the board from pick-up |
| Home / End | Focus the first/last item | — |
| Tab away | — | Drop |

Resizing uses `stepSize(supportedSizes, currentSize, step, columns)`, so it only reaches supported sizes that fit.

---

## Accessibility

- **Roles.** While editing, the board is `role="group"` named "Board layout"; items are `role="button"` with `aria-roledescription="movable widget"`, a name like "Plex, Wide", `aria-pressed` while picked up, and `aria-describedby` pointing at the instructions. Outside edit mode the wrapper adds no roles: the widget owns its semantics.
- **Focus.** Arrow navigation moves real focus; after a reorder moves the focused node in the DOM, focus is restored to it. Pointer presses focus the item too. The focus ring is `--focus` with a `--bg` gap.
- **Inert content.** Slot content is `inert` while editing, so widget controls neither take focus nor steal the drag.
- **Live region.** A polite `role="status"` region speaks each step: "Picked up Plex at row 3, column 7. …", "Moved Plex to row 2, column 5", "Moved Plex to Today, row 1, column 1" (section named when it changes), "Resized to Wide", "Dropped Plex at …", "Cancelled. Plex is back at …", "Plex can't move further left", "Plex can't get any wider". Each message is a new node, so a repeated message is announced again. Rows and columns are 1-based inside the section.
- **Targets.** On coarse pointers the resize handle grows to `--target-min` (44 px).
- **Reduced motion** is honoured; see below.

---

## Motion

Springs follow the token model (duration + bounce; ω = 2π/duration, ζ = 1 − bounce). `springTransition` hands Motion the exact stiffness and damping, because Motion's own `visualDuration` scales the duration by 1.2. `SPRINGS` mirrors `MOTION` in `@chadash/tokens`, and a test keeps them equal.

| Moment | Token | How |
|---|---|---|
| Neighbours reflow, list reorder | `move` (0.35 s, bounce 0) | FLIP: offset from the old slot, sprung to zero; an interrupted reflow keeps its velocity |
| A resize changes an item's box | `move` | width/height sprung from the old box to the new slot |
| Pick-up and set-down (scale 0.982 ↔ 1.03) | `lift` (0.3 s, bounce 0.15) | scale spring |
| Drop settle | `lift` | offset spring with the release velocity |
| Entering/leaving edit mode | `lift` | every item's resting scale |
| Reduced motion | `fade` (150 ms) | no movement: items jump, moved ones crossfade from 0.3 opacity |
| CSS transitions (shadow, content dim, unit-grid fade) | `--motion-press-*`, `--motion-move-*`, `--motion-fade-*` | custom properties, which `tokens.css` already maps to the fade under reduced motion |

`press` and `sheet` aren't used here. Nothing loops.

**Preference:** `data-motion="full|reduced|none"` on `<html>` wins (the same switch `tokens.css` reads), then `prefers-reduced-motion: reduce`. The `motion` prop forces one.

---

## Styling

Tokens used (CSS custom properties from `tokens.css`; no colour is hard-coded):

| Group | Tokens |
|---|---|
| Layout | `--gutter`, `--section-gap`, `--target-min` |
| Spacing | `--space-0-5`, `--space-1-5`, `--space-2`, `--space-3`, `--space-4` |
| Radius and lines | `--tile-radius`, `--radius-xs`, `--radius-sm`, `--hairline`, `--section-rule-width`, `--section-rule-color` |
| Colour | `--bg`, `--text`, `--text-2`, `--text-3`, `--line`, `--line-2`, `--surface-2`, `--focus`, `--accent`, `--accent-bg`, `--accent-line` |
| Depth | `--shadow-lift` (items in edit mode), `--shadow-overlay` (the lifted item) |
| Type | `--type-micro`, `--font-num`, `--weight-medium`, `--weight-semibold`, `--tracking-caps` |
| Motion | `--motion-press-*`, `--motion-move-*`, `--motion-fade-*` |

Hooks for the app: classes `layout-board`, `layout-grid`, `layout-section-header`, `layout-item` (`is-lifted`, `is-picked`, `is-resizing`), `layout-item-content`, `layout-area` (the unit grid), `layout-drop`, `layout-handle`, `layout-resize-ghost`. Data attributes: `data-breakpoint` and `data-editing` on the board; `data-item-id`, `data-section`, `data-size`, `data-x`, `data-y`, `data-w`, `data-h`, `data-column`, `data-band`, `data-forced` on items (handy for end-to-end tests). Computed properties: `--layout-unit`, `--layout-gap`, `--layout-columns`. Styles are unscoped so the app can override them. The motion controller owns each item's `transform`, `opacity` and (while a resize animates) `width`/`height`; don't set those on `.layout-item`.

---

## Decisions

1. **Momentum projection constants** (`MOMENTUM`):
   - **Formula:** Apple's projection from "Designing Fluid Interfaces" (WWDC18): distance = v / 1000 × r / (1 − r). It models a body decelerating by a factor `r` per millisecond, so the throw feels like it has mass rather than being a fixed multiplier.
   - **`decelerationRate` 0.995 → 0.2 s of travel.** UIKit's `.normal` (0.998, 0.5 s) is tuned for scrolling and throws a tile across the whole board; `.fast` (0.99, 0.1 s) barely registers. 0.2 s also matches the prototype's sheet dismissal (`vy × 0.22`), so drops and sheets share one feel.
   - **`minSpeed` 250 px/s, subtracted rather than gated.** Deliberate placement moves at well under 250 px/s, and those drops land exactly where the pointer is. Above it only the excess projects, so there's no jump at the threshold.
   - **`maxCells` 4.** A flick never projects more than one Large widget (4 cells), so a throw stays on screen and predictable.
   - **`velocityWindow` 100 ms, least squares.** Steadier than first-to-last on jittery input, and a pointer that rested before letting go has no samples left in the window, so it doesn't project.
   - **`dragThreshold` 4 px** before a press becomes a drag.
   - Example at a 100 px pitch: 1000 px/s projects 149 px (1.5 cells); 3000 px/s projects 547 px, capped at 400 px (4 cells).
2. **Previews are local, the board stays controlled.** During a gesture the component shows its own preview and emits `update:board`; when the gesture ends it emits `commit` and goes back to the prop. A parent that only handles `@commit` (history) and one that binds `v-model:board` both work, and a parent that ignores both sees the board snap back.
3. **Every preview is computed from the gesture's origin board**, never from the previous preview: deterministic, no drift, and cancelling is just "emit the origin".
4. **One flat grid** rather than nested section grids, so cross-section moves don't remount widgets (Vue can't move a keyed node between parents).
5. **Arrow keys move focus until you pick up** (Enter/Space), then they nudge. This is the standard accessible drag pattern; it keeps roving focus and makes Escape's "revert" well defined. Shift+arrows resize immediately when nothing is picked up, because a resize is a single, already-reversible step (⌘Z).
6. **Reflow uses `move`, settle uses `lift`.** The Motion table in 02 · Experience assigns reflow to `move`; its Editing section says "neighbours reflow on lift springs". The table won, since it defines the tokens; worth reconciling in the doc.
7. **Resizes animate the box, not a scale.** Widgets re-lay out by size class and never scale, so the tile's width and height spring to the new slot while its content is already in the new layout.
8. **TypeScript 6.0.3 is pinned in this package** for vue-tsc 3.3, which resolves `typescript/lib/tsc`; TypeScript 7 (the native compiler) doesn't ship it, so vue-tsc fails with `ERR_PACKAGE_PATH_NOT_EXPORTED`. The engine and tokens packages keep TypeScript 7.
9. **Strict templates** (`strictTemplates`, with `data-*` attributes allowed) on top of the repo's strict TypeScript flags.

---

## Deferred

- **Touch long-press to lift.** Items use `touch-action: none` in edit mode, so on a phone the page scrolls from gaps and headers, not tiles. A press-and-hold lift with scroll passthrough comes with the phone feel check (2.5).
- **Auto-scroll** when dragging near the viewport edge.
- **Drag from the gallery / Magic Plus** (`place` and `smartInsert` with a viewport hint).
- **Section editing** (move, span, title) and per-breakpoint override reset UI.
- **Wall/TV (16 columns)**, once the engine adds the breakpoint.
- **Long-press/right-click context menu** with size previews (light edits without edit mode).
- **Expand in place** (sheets) and the hold-to-confirm ring: they belong to the app shell, not the grid.
- **A library build and bundle-size number** for ADR-0013: the package exports source, so size is measured in `web/`'s build.

---

## Development

```sh
bun install          # from the repo root
bun run test         # in packages/layout-vue: Vitest + happy-dom
bun run typecheck    # in packages/layout-vue: vue-tsc (TypeScript 6), strict templates
```

| Test file | Covers |
|---|---|
| `test/render.test.ts` | Rendered positions match `deriveLayout` at each breakpoint; band tracks; breakpoint from container width |
| `test/keyboard.test.ts` | Roving focus, pick-up/nudge/drop, cross-section nudge, Shift resize, phone overrides, Escape, drop on blur |
| `test/pointer.test.ts` | Live reflow preview, slow drop, flick projection, resting release, cross-section move without remount, derived breakpoints, Escape/pointercancel, resize snapping |
| `test/announce.test.ts` | Live region messages, localisation, instructions; external board changes |
| `test/history.test.ts` | `useLayoutHistory` undo/redo with the board, limits, ref sources |
| `test/motion.test.ts` | FLIP inversion and settle on motion-v, pointer follow, reduced-motion crossfade, preference |
| `test/units.test.ts` | Springs mirror the tokens, momentum maths, velocity tracking, breakpoint thresholds |
| `test/demo.test.ts` | The demo page edits, commits and undoes |

happy-dom has no layout engine, so `test/helpers.ts` describes one (100 px pitch, 12 px gutter) through `getBoundingClientRect` and the `offset*` properties.
