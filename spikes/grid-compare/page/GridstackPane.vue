<!--
  gridstack 14 on the same page, configured as close to our model as it goes:
  v14 compact packing inside sections, sections as sub-grids flowing as a list,
  min/max around each widget's declared sizes, square cells, a 12 px gutter.
  Driven directly (GridStack.init); the official Vue wrapper renders the same
  grid and is covered by test-vue/ and the bundle entry.

  App code we had to write, because gridstack has no equivalent:
  - snapping to a declared size, only possible after the pointer lets go;
  - the undo stack (save() before each gesture, load() to undo);
  - turning animation off for reduced motion.
-->
<script setup lang="ts">
import { snapSize } from '@chadash/layout';
import { GridStack, type GridItemHTMLElement, type GridStackNode, type GridStackOptions, type GridStackWidget } from 'gridstack';
import { onBeforeUnmount, onMounted, shallowRef, useTemplateRef, watch } from 'vue';
import { constraints } from '../adapters/gridstack.ts';
import { DOGFOOD, SIZE_UNITS, type SizeName } from '../scenarios/index.ts';
import { NAMES, SIZE_NAMES, sizeOf } from './names.ts';

const props = defineProps<{ columns: 12 | 8 | 4; editing: boolean; motion: 'full' | 'reduced'; snap: boolean }>();

const root = useTemplateRef<HTMLElement>('root');
const supported = new Map(DOGFOOD.sections.flatMap((s) => s.items).map((i) => [i.id, i.supportedSizes]));
const past: GridStackWidget[][] = [];
const canUndo = shallowRef(false);
let grid: GridStack | null = null;

/** Name and size on each tile; sizes outside what the widget declares are flagged. */
function paint(el: HTMLElement, n: GridStackNode): void {
  const content = el.querySelector<HTMLElement>('.grid-stack-item-content');
  if (!content || n.subGridOpts || n.subGrid) return;
  const id = String(n.id);
  const size = sizeOf(n.w ?? 1, n.h ?? 1);
  const ok = size !== undefined && (supported.get(id) ?? []).includes(size);
  content.classList.add('tile');
  content.toggleAttribute('data-off', !ok);
  const name = document.createElement('span');
  name.className = 'tile-name';
  name.textContent = NAMES[id] ?? id;
  const label = document.createElement('span');
  label.className = 'tile-size';
  label.textContent = `${size ? SIZE_NAMES[size] : 'Not a size'} · ${n.w}×${n.h}${ok ? '' : ' · not declared'}`;
  content.replaceChildren(name, label);
}

function options(): GridStackOptions {
  const animate = props.motion === 'full';
  return {
    column: 12,
    cellHeight: 'auto',
    margin: 6,
    animate,
    mode: 'list',
    staticGrid: !props.editing,
    subGridOpts: { column: 'auto', cellHeight: 'auto', margin: 6, animate, mode: 'compact', acceptWidgets: true },
    children: DOGFOOD.sections.map((s) => ({
      id: s.id,
      w: s.span,
      h: 6,
      noResize: true,
      noMove: true,
      sizeToContent: true,
      subGridOpts: { children: s.items.map((i) => ({ id: i.id, ...SIZE_UNITS[i.size], ...constraints(i.supportedSizes) })) },
    })),
  };
}

function allGrids(): GridStack[] {
  return grid ? [grid, ...grid.engine.nodes.flatMap((n) => (n.subGrid ? [n.subGrid] : []))] : [];
}

onMounted(() => {
  GridStack.renderCB = (el, w) => paint(el.parentElement ?? el, w as GridStackNode);
  const g = GridStack.init(options(), root.value!);
  if (!g) return;
  grid = g;
  // nested grids report to the main grid (gridstack 12.1+)
  g.on('change', (_e, nodes) => nodes.forEach((n) => n.el && paint(n.el, n)));
  g.on('added', (_e, nodes) => nodes.forEach((n) => n.el && paint(n.el, n)));
  g.on('dragstart resizestart', () => {
    past.push(g.save(false) as GridStackWidget[]);
    canUndo.value = true;
  });
  g.on('resizestop', (_e, el: GridItemHTMLElement) => {
    const n = el.gridstackNode;
    if (!props.snap || !n?.grid) return;
    const to = snapSize((supported.get(String(n.id)) ?? []) as SizeName[], n.w ?? 1, n.h ?? 1, n.grid.getColumn());
    if (to && (SIZE_UNITS[to].w !== n.w || SIZE_UNITS[to].h !== n.h)) n.grid.update(el, { ...SIZE_UNITS[to] });
  });
});

onBeforeUnmount(() => {
  grid?.destroy(true);
  grid = null;
});

watch(
  () => props.columns,
  // sections re-flow as a list (gridstack's default for list mode); each sub-grid
  // follows its section's width on the next resize tick and re-packs compact
  (c) => grid?.column(c),
);
watch(
  () => props.editing,
  (on) => grid?.setStatic(!on, true, true),
);
watch(
  () => props.motion,
  (m) => allGrids().forEach((g) => g.setAnimation(m === 'full')),
);

function undo(): void {
  const prev = past.pop();
  if (prev && grid) grid.load(prev);
  canUndo.value = past.length > 0;
}

defineExpose({ undo, canUndo });
</script>

<template>
  <div class="frame" :data-columns="columns">
    <div ref="root" class="grid-stack gs-board" />
  </div>
</template>
