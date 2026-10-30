<script setup lang="ts">
/**
 * The read-only board renderer: @chadash/layout derives positions for the
 * board's own width (12 / 8 / 4 columns) and CSS grid places them, in the
 * board's flow:
 * - `sections`: sections flow in bands; items sit at their derived x/y inside
 *   a square-unit grid.
 * - `bento`: the board's rows are half-unit tracks (two and a gap make a
 *   unit). Each section is a block at its track area: a header track, then
 *   the same square-unit grid as in bands.
 * - `mosaic`: no section blocks. Every tile, and each section's label, sits
 *   on the board at its track area; a section is a `display: contents`
 *   region, so screen readers still hear it.
 * The board's tracks need its width, so bento and mosaic wrap it in a
 * container (`.board-flow`).
 *
 * Integration seam: its props and slots are a subset of `<LayoutBoard>` from
 * @chadash/layout-vue (`ItemSlotProps`, `SectionSlotProps`), so edit mode is a
 * swap in BoardSurface.vue:
 *
 *   <template #item="{ item, size, editing, dragging, placed }"> … </template>
 *   <template #section="{ section, editing }"> … </template>
 */
import { computed, ref } from 'vue';
import {
  BREAKPOINT_COLUMNS,
  deriveLayout,
  type Board,
  type Breakpoint,
  type Item,
  type PlacedItem,
  type PlacedSection,
  type SizeId,
  type TrackArea,
} from '@chadash/layout';
import { useBreakpoint } from './useBreakpoint';
import { isFresh } from '../import/fresh';

const { board, breakpoint: forced } = defineProps<{
  board: Board;
  /** Pin a breakpoint (tests, previews). Otherwise it follows the board's width. */
  breakpoint?: Breakpoint | undefined;
}>();

defineSlots<{
  item(props: { item: Item; size: SizeId; editing: boolean; dragging: boolean; placed: PlacedItem }): unknown;
  section(props: { section: PlacedSection; editing: boolean }): unknown;
}>();

const root = ref<HTMLElement | null>(null);
const { breakpoint: measured } = useBreakpoint(root);
const bp = computed<Breakpoint>(() => forced ?? measured.value);
// Every section gets a header track: the app's section slot always draws one.
const layout = computed(() => deriveLayout(board, bp.value, { headers: 'always' }));
const columns = computed(() => BREAKPOINT_COLUMNS[bp.value]);
/** Canonical items by id: the slot gets the item, plus its size at this breakpoint. */
const items = computed(() => new Map(board.sections.flatMap((s) => s.items.map((i) => [i.id, i] as const))));

const sectionStyle = (s: PlacedSection) => ({ gridColumn: `${s.column + 1} / span ${s.span}`, gridRow: `${s.band + 1}` });
const gridStyle = (s: PlacedSection) => ({ '--scols': String(s.span), gridTemplateRows: s.rows ? `repeat(${s.rows}, var(--u))` : undefined });
const itemStyle = (i: PlacedItem) => ({ gridColumn: `${i.x + 1} / span ${i.w}`, gridRow: `${i.y + 1} / span ${i.h}` });
const areaStyle = (a: TrackArea | undefined) => (a ? { gridColumn: `${a.column + 1} / span ${a.w}`, gridRow: `${a.track + 1} / span ${a.tracks}` } : undefined);
const flowStyle = computed(() => ({ '--cols': String(columns.value), gridTemplateRows: layout.value.tracks ? `repeat(${layout.value.tracks}, var(--track))` : undefined }));
</script>

<template>
  <div v-if="layout.flow !== 'sections'" ref="root" class="board-flow" :data-breakpoint="bp">
    <div class="board" :data-flow="layout.flow" :style="flowStyle">
      <template v-if="layout.flow === 'bento'">
        <template v-for="s in layout.sections" :key="s.id">
          <section v-if="s.area" class="sec" :style="areaStyle(s.area)" :aria-label="s.title ?? s.id">
            <slot name="section" :section="s" :editing="false"></slot>
            <div class="sec-grid" :style="gridStyle(s)">
              <div v-for="i in s.items" :key="i.id" :class="['cell', { 'is-new': isFresh(i.id) }]" :data-item="i.id" :data-forced="i.forced || undefined" :style="itemStyle(i)">
                <slot v-if="items.get(i.id)" name="item" :item="items.get(i.id)!" :size="i.size" :editing="false" :dragging="false" :placed="i"></slot>
              </div>
            </div>
          </section>
        </template>
      </template>
      <template v-else>
        <section v-for="s in layout.sections" :key="s.id" class="sec mosaic-sec" :aria-label="s.title ?? s.id">
          <div v-if="s.header" class="mosaic-label" :style="areaStyle(s.header)">
            <slot name="section" :section="s" :editing="false"></slot>
          </div>
          <div v-for="i in s.items" :key="i.id" :class="['cell', { 'is-new': isFresh(i.id) }]" :data-item="i.id" :data-forced="i.forced || undefined" :style="areaStyle(i.area)">
            <slot v-if="items.get(i.id)" name="item" :item="items.get(i.id)!" :size="i.size" :editing="false" :dragging="false" :placed="i"></slot>
          </div>
        </section>
      </template>
    </div>
  </div>
  <div v-else ref="root" class="board" :data-breakpoint="bp" :style="{ '--cols': String(columns) }">
    <section v-for="s in layout.sections" :key="s.id" class="sec" :style="sectionStyle(s)" :aria-label="s.title ?? s.id">
      <slot name="section" :section="s" :editing="false"></slot>
      <div class="sec-grid" :style="gridStyle(s)">
        <div v-for="i in s.items" :key="i.id" :class="['cell', { 'is-new': isFresh(i.id) }]" :data-item="i.id" :data-forced="i.forced || undefined" :style="itemStyle(i)">
          <slot v-if="items.get(i.id)" name="item" :item="items.get(i.id)!" :size="i.size" :editing="false" :dragging="false" :placed="i"></slot>
        </div>
      </div>
    </section>
  </div>
</template>

<style>
/* Bento and mosaic: the board's rows are half-unit tracks, from the board's own width. */
.board-flow {
  container-type: inline-size;
}
.board-flow > .board {
  --u: calc((100cqi - (var(--cols) - 1) * var(--gutter)) / var(--cols));
  --track: calc((var(--u) - var(--gutter)) / 2);
  row-gap: var(--gutter);
  align-items: stretch;
}
/*
 * A header has one track. It sits at the bottom of its track and the gap
 * below it, as close to its tiles as in bands, and the room above it sets the
 * block apart from the one before. Long meta is cut.
 */
.board[data-flow='bento'] > .sec {
  display: grid;
  grid-template-rows: calc(var(--track) + var(--gutter)) auto;
  align-content: start;
}
.board[data-flow='bento'] > .sec > :first-child:not(.sec-grid) {
  align-self: end;
  margin-bottom: var(--space-3);
  min-width: 0;
}
.board[data-flow] .sh {
  margin: 0;
  min-width: 0;
  overflow: hidden;
}
.mosaic-sec {
  display: contents;
}
.mosaic-label {
  display: flex;
  align-items: flex-end;
  min-width: 0;
}
.mosaic-label > * {
  flex: 1;
  min-width: 0;
}

/* Tiles an import just added: a ring for a few seconds (import/fresh.ts). */
.cell.is-new {
  border-radius: var(--radius-md);
  outline: 2px solid var(--accent, var(--text-2));
  outline-offset: 2px;
}
.cell {
  display: grid;
  min-width: 0;
  min-height: 0;
}
.cell > * {
  min-width: 0;
  min-height: 0;
}
/* ⌘K → a resource: its tile scrolls into view and rings once. */
.cell.flash > * {
  animation: cell-flash 1.5s var(--motion-fade-easing);
}
@keyframes cell-flash {
  0%,
  45% {
    box-shadow: 0 0 0 2px var(--focus);
  }
  100% {
    box-shadow: 0 0 0 2px transparent;
  }
}
</style>
