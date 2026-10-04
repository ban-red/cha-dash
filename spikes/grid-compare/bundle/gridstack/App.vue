<!--
  The same page on gridstack 14's official Vue wrapper: sections as sub-grids,
  sizes boxed with min/max, and the app keeping its own undo stack of save()s.
-->
<script setup lang="ts">
import type { GridStack as GridStackInstance, GridStackOptions, GridStackWidget } from 'gridstack';
import { GridStack } from 'gridstack/dist/vue';
import 'gridstack/dist/gridstack.css';
import { defineComponent, h, shallowRef, useTemplateRef } from 'vue';
import { constraints } from '../../adapters/gridstack.ts';
import { DOGFOOD, SIZE_UNITS } from '../../scenarios/index.ts';

const Tile = defineComponent({ props: { name: { type: String, default: '' } }, setup: (p) => () => h('div', { class: 'tile' }, p.name) });

const options: GridStackOptions = {
  column: 12,
  cellHeight: 'auto',
  mode: 'list',
  staticGrid: true,
  subGridOpts: { column: 'auto', mode: 'compact', acceptWidgets: true },
  children: DOGFOOD.sections.map((s) => ({
    id: s.id,
    w: s.span,
    h: 6,
    sizeToContent: true,
    subGridOpts: { children: s.items.map((i) => ({ id: i.id, ...SIZE_UNITS[i.size], ...constraints(i.supportedSizes), component: 'Tile', props: { name: i.id } })) },
  })),
};

const gs = useTemplateRef<{ getGrid(): GridStackInstance | null }>('gs');
const editing = shallowRef(false);
const past: GridStackWidget[][] = [];
let last: GridStackWidget[] | undefined;

function toggle() {
  editing.value = !editing.value;
  gs.value?.getGrid()?.setStatic(!editing.value, true, true);
}
function remember() {
  const grid = gs.value?.getGrid();
  if (!grid) return;
  if (last) past.push(last);
  last = grid.save(false) as GridStackWidget[];
}
function undo() {
  const prev = past.pop();
  if (prev) {
    gs.value?.getGrid()?.load(prev);
    last = prev;
  }
}
</script>

<template>
  <button type="button" @click="toggle">Edit</button>
  <button type="button" @click="undo">Undo</button>
  <GridStack ref="gs" :options="options" :components="{ Tile }" @change="remember" />
</template>
