<!--
  Grid engines side by side: the dogfood front page on our engine and on
  gridstack 14, with the same controls. Built to a single static file by
  `bun run page`; open dist/page/index.html straight from disk.
-->
<script setup lang="ts">
import { computed, shallowRef, useTemplateRef } from 'vue';
import ChadashPane from './ChadashPane.vue';
import GridstackPane from './GridstackPane.vue';

const columns = shallowRef<12 | 8 | 4>(12);
const editing = shallowRef(true);
const motion = shallowRef<'full' | 'reduced'>(globalThis.matchMedia?.('(prefers-reduced-motion: reduce)').matches ? 'reduced' : 'full');
const snap = shallowRef(true);
const generation = shallowRef(0);

const ours = useTemplateRef<{ undo(): void; canUndo: boolean }>('ours');
const theirs = useTemplateRef<{ undo(): void; canUndo: boolean }>('theirs');
const canUndo = computed(() => Boolean(ours.value?.canUndo || theirs.value?.canUndo));

function undo() {
  ours.value?.undo();
  theirs.value?.undo();
}
function reset() {
  generation.value++;
}
</script>

<template>
  <div class="page">
    <header class="top">
      <div>
        <h1>Grid engines, side by side</h1>
        <p class="lede">
          The dogfood front page on our engine and on gridstack 14. Same 15 widgets, same declared sizes, same controls. Spike 2.4, for ADR-0013.
        </p>
      </div>
      <div class="controls" role="toolbar" aria-label="Both boards">
        <div class="seg" role="radiogroup" aria-label="Columns">
          <button v-for="c in [12, 8, 4] as const" :key="c" type="button" role="radio" :aria-checked="columns === c" @click="columns = c">{{ c }} col</button>
        </div>
        <button type="button" :aria-pressed="editing" @click="editing = !editing">{{ editing ? 'Editing' : 'Viewing' }}</button>
        <button type="button" :disabled="!canUndo" @click="undo">Undo both</button>
        <button type="button" @click="reset">Reset</button>
        <div class="seg" role="radiogroup" aria-label="Motion">
          <button type="button" role="radio" :aria-checked="motion === 'full'" @click="motion = 'full'">Full motion</button>
          <button type="button" role="radio" :aria-checked="motion === 'reduced'" @click="motion = 'reduced'">Reduced</button>
        </div>
      </div>
    </header>

    <main class="panes">
      <section class="pane" aria-labelledby="ours-title">
        <h2 id="ours-title">@chadash/layout + layout-vue</h2>
        <ul class="hints">
          <li>Drag anywhere on a tile; flick it and the drop is projected from the release speed.</li>
          <li>The corner handle snaps live, to declared sizes only.</li>
          <li>Keyboard: Tab to the board, Enter to pick up, arrows to move, Shift+arrows to resize, Esc to cancel.</li>
          <li>8 and 4 columns are derived; edits there are saved as overrides and leave 12 columns alone.</li>
        </ul>
        <ChadashPane ref="ours" :key="`ours-${generation}`" :columns="columns" :editing="editing" :motion="motion" />
      </section>

      <section class="pane" aria-labelledby="gs-title">
        <h2 id="gs-title">gridstack 14 · compact, sections as sub-grids</h2>
        <ul class="hints">
          <li>Drag a tile; neighbours preview with list flow and compact once you let go.</li>
          <li>Resize from the corner, within each widget's min/max box. Off-system sizes are flagged.</li>
          <li>No keyboard moves or resizes, no ARIA. Changing columns here rewrites the 12-column layout after an edit.</li>
          <li>
            <label><input v-model="snap" type="checkbox" /> Snap to a declared size on release (app code)</label>
          </li>
        </ul>
        <GridstackPane ref="theirs" :key="`gs-${generation}`" :columns="columns" :editing="editing" :motion="motion" :snap="snap" />
      </section>
    </main>
  </div>
</template>

<style>
html,
body {
  margin: 0;
  background: var(--bg);
  color: var(--text);
  font-family: var(--font-ui);
  font-size: var(--type-body);
}

.page {
  max-width: 1880px;
  margin: 0 auto;
  padding: var(--space-6) var(--space-4) var(--space-12);
}

.top {
  display: flex;
  flex-wrap: wrap;
  gap: var(--space-4) var(--space-8);
  align-items: end;
  justify-content: space-between;
  margin-bottom: var(--space-8);
}

h1 {
  margin: 0 0 var(--space-1);
  font-size: var(--type-title-2);
  font-weight: var(--weight-semibold);
  letter-spacing: var(--tracking-display);
}

.lede {
  margin: 0;
  max-width: 62ch;
  color: var(--text-2);
}

.controls {
  display: flex;
  flex-wrap: wrap;
  gap: var(--space-2);
}

.controls button {
  min-height: 32px;
  padding: 0 var(--space-3);
  border: var(--hairline) solid var(--border);
  border-radius: var(--radius-sm);
  background: var(--surface);
  color: var(--text);
  font: inherit;
  cursor: pointer;
}

.controls button[aria-pressed='true'],
.controls button[aria-checked='true'] {
  border-color: var(--accent-line);
  background: var(--accent-bg);
  color: var(--accent-text);
}

.controls button:disabled {
  opacity: 0.45;
  cursor: default;
}

.controls button:focus-visible {
  outline: 2px solid var(--focus);
  outline-offset: 2px;
}

.seg {
  display: inline-flex;
}

.seg button + button {
  margin-left: -1px;
}

.panes {
  display: grid;
  grid-template-columns: minmax(0, 1fr);
  gap: var(--space-12);
}

@media (min-width: 1500px) {
  .panes {
    grid-template-columns: repeat(2, minmax(0, 1fr));
    gap: var(--space-8);
  }
}

.pane h2 {
  margin: 0 0 var(--space-2);
  font-size: var(--type-callout);
  font-weight: var(--weight-semibold);
}

.hints {
  margin: 0 0 var(--space-6);
  padding-left: var(--space-4);
  color: var(--text-2);
  font-size: var(--type-small);
  line-height: var(--leading-normal);
}

.hints label {
  display: inline-flex;
  gap: var(--space-1-5);
  align-items: center;
  color: var(--text);
}

/* the narrower layouts get narrower frames, so units stay about the same size */
.frame[data-columns='8'] {
  max-width: 67%;
}

.frame[data-columns='4'] {
  max-width: max(34%, 300px);
}

.tile {
  display: flex;
  flex-direction: column;
  gap: var(--space-0-5); /* name and size at the top, clear of our corner handle */
  box-sizing: border-box;
  height: 100%;
  padding: var(--tile-pad-y) var(--tile-pad-inline);
  border: var(--tile-border-width) solid var(--tile-border-color);
  border-radius: var(--tile-radius);
  background: var(--tile-bg);
  overflow: hidden;
}

.tile-name {
  font-weight: var(--weight-medium);
}

.tile-size {
  color: var(--text-3);
  font-family: var(--font-num);
  font-size: var(--type-caption);
}

.tile[data-forced],
.tile[data-off] {
  border-color: var(--down-line);
  background: var(--down-bg);
}

.tile[data-off] .tile-size {
  color: var(--down-text);
}

/* gridstack: sections are plain containers; the sub-grid's tiles carry the look.
   Its content box is absolutely inset by the margin, so it keeps its own height. */
.gs-board > .grid-stack-item > .grid-stack-item-content {
  overflow: visible;
}

.gs-board .grid-stack-item-content.tile {
  height: auto;
}

.gs-board .grid-stack-placeholder > .placeholder-content {
  border: 1px dashed var(--accent-line);
  border-radius: var(--tile-radius);
  background: transparent;
}
</style>
