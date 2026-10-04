<script setup lang="ts">
/**
 * The read-only board renderer: @chadash/layout derives positions for the
 * board's own width (12 / 8 / 4 columns) and CSS grid places them. Sections
 * flow in bands; items sit at their derived x/y inside a square-unit grid.
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
} from '@chadash/layout';
import { useBreakpoint } from './useBreakpoint';

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
const layout = computed(() => deriveLayout(board, bp.value));
const columns = computed(() => BREAKPOINT_COLUMNS[bp.value]);
/** Canonical items by id: the slot gets the item, plus its size at this breakpoint. */
const items = computed(() => new Map(board.sections.flatMap((s) => s.items.map((i) => [i.id, i] as const))));

const sectionStyle = (s: PlacedSection) => ({ gridColumn: `${s.column + 1} / span ${s.span}`, gridRow: `${s.band + 1}` });
const gridStyle = (s: PlacedSection) => ({ '--scols': String(s.span), gridTemplateRows: s.rows ? `repeat(${s.rows}, var(--u))` : undefined });
const itemStyle = (i: PlacedItem) => ({ gridColumn: `${i.x + 1} / span ${i.w}`, gridRow: `${i.y + 1} / span ${i.h}` });
</script>

<template>
  <div ref="root" class="board" :data-breakpoint="bp" :style="{ '--cols': String(columns) }">
    <section v-for="s in layout.sections" :key="s.id" class="sec" :style="sectionStyle(s)" :aria-label="s.title ?? s.id">
      <slot name="section" :section="s" :editing="false"></slot>
      <div class="sec-grid" :style="gridStyle(s)">
        <div v-for="i in s.items" :key="i.id" class="cell" :data-item="i.id" :data-forced="i.forced || undefined" :style="itemStyle(i)">
          <slot v-if="items.get(i.id)" name="item" :item="items.get(i.id)!" :size="i.size" :editing="false" :dragging="false" :placed="i"></slot>
        </div>
      </div>
    </section>
  </div>
</template>

<style>
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
