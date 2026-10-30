<script setup lang="ts">
/**
 * The planned board, read-only: each section and its tiles in order, as
 * placeholders (name, live dot). A picture of the list beside it, not a
 * second list: the tiles are buttons that move focus to their row, and the
 * list is what a screen reader reads. Tiles the board already had are
 * quiet; the import's own are marked.
 */
import Glyph from '../ui/marks/Glyph.vue';
import type { PreviewSection } from '../model/importPlan';

const { sections, ours, highlight } = defineProps<{
  sections: PreviewSection[];
  /** Item IDs this import adds; others were on the board already. */
  ours: ReadonlySet<string>;
  highlight?: string;
}>();
const emit = defineEmits<{ pick: [itemId: string] }>();
</script>

<template>
  <div class="bp" aria-hidden="true">
    <div v-for="s in sections" :key="s.id" class="bp-sec">
      <span class="bp-title">{{ s.title }}</span>
      <div class="bp-tiles">
        <button
          v-for="t in s.tiles"
          :key="t.id"
          type="button"
          tabindex="-1"
          :class="['bp-tile', { ours: ours.has(t.id), hl: highlight === t.id }]"
          :title="t.label"
          @click="emit('pick', t.id)"
        >
          <Glyph v-if="t.live" kind="ok" /><span>{{ t.label }}</span>
        </button>
        <span v-if="!s.tiles.length" class="bp-empty">empty</span>
      </div>
    </div>
    <p v-if="!sections.length" class="bp-empty">No tiles: connections only.</p>
  </div>
</template>

<style>
.bp {
  display: flex;
  flex-direction: column;
  gap: var(--space-3);
  padding: var(--space-3);
  border: var(--hairline) solid var(--line);
  border-radius: var(--radius-md);
  background: color-mix(in oklch, var(--text) 2%, transparent);
}
.bp-sec {
  display: flex;
  flex-direction: column;
  gap: 4px;
}
.bp-title {
  font: var(--weight-medium) var(--type-micro) / 1 var(--font-num);
  letter-spacing: var(--tracking-caps);
  text-transform: uppercase;
  color: var(--text-3);
}
.bp-tiles {
  display: grid;
  grid-template-columns: repeat(auto-fill, minmax(6.5rem, 1fr));
  gap: 4px;
}
.bp-tile {
  display: flex;
  align-items: center;
  gap: 4px;
  min-height: 2rem;
  padding: 4px 6px;
  border: var(--hairline) solid var(--line);
  border-radius: var(--radius-sm);
  background: var(--surface, transparent);
  color: var(--text-3);
  font-size: var(--type-small);
  text-align: left;
  overflow: hidden;
}
.bp-tile span {
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}
.bp-tile.ours {
  color: var(--text);
  border-color: var(--line-2);
}
.bp-tile.hl {
  outline: 2px solid var(--focus);
  outline-offset: 1px;
}
.bp-tile .g {
  width: 8px;
  height: 8px;
  flex: none;
}
.bp-empty {
  font-size: var(--type-small);
  color: var(--text-3);
}
</style>
