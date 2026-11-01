<script setup lang="ts">
/**
 * The Atlas drawn: nodes as buttons on rows, relations as curves beneath
 * them, in one world that pans and zooms (drag, the wheel in the lens or
 * with ⌘/Ctrl on a tile, + and −, 0 to fit). Health is a glyph's shape and
 * word as well as colour (ui/marks/Glyph.vue).
 *
 * Keyboard: one node is in the Tab order; the arrows move along the map
 * (model/atlas.ts `neighbour`), Enter opens what's focused. Screen readers
 * get the same nodes as a list (AtlasList.vue); this canvas follows the
 * selection. `lit` dims everything but a cause's path.
 */
import { computed, nextTick, onBeforeUnmount, onMounted, ref, useTemplateRef, watch } from 'vue';
import Glyph from '../ui/marks/Glyph.vue';
import UiButton from '../ui/Button.vue';
import { glyphFor } from '../model/status';
import { KIND_WORDS, NODE_H, NODE_W, neighbour, type Atlas, type AtlasNode, type Direction } from '../model/atlas';
import './atlas.css';

const {
  atlas,
  selected,
  lit,
  zoom = 'fit',
  wheel = true,
  label = 'Atlas map',
} = defineProps<{
  atlas: Atlas;
  selected?: string | undefined;
  lit?: { nodes: Set<string>; edges: Set<string> } | undefined;
  /** The starting zoom: fit the whole map, or a scale. */
  zoom?: 'fit' | number;
  /** Zoom on the wheel without a modifier (the lens; a tile leaves the wheel to the page). */
  wheel?: boolean;
  label?: string;
}>();

const emit = defineEmits<{ select: [id: string]; open: [id: string]; expand: [group: string] }>();

const view = useTemplateRef<HTMLDivElement>('view');
const scale = ref(1);
const tx = ref(0);
const ty = ref(0);
/** The node in the Tab order: the selection, else the first. */
const focusId = ref<string | undefined>(selected);
watch(
  () => selected,
  (id) => {
    if (id && atlas.nodes.has(id)) {
      focusId.value = id;
      reveal(id);
    }
  },
);
const roving = computed(() => (focusId.value && atlas.nodes.has(focusId.value) ? focusId.value : atlas.order[0]));

const MIN = 0.2;
const MAX = 2;
const clamp = (s: number) => Math.min(MAX, Math.max(MIN, s));

function size(): { w: number; h: number } {
  const el = view.value;
  return { w: el?.clientWidth || 800, h: el?.clientHeight || 480 };
}

function fit(): void {
  const { w, h } = size();
  scale.value = clamp(Math.min(w / atlas.width, h / atlas.height, 1));
  tx.value = (w - atlas.width * scale.value) / 2;
  ty.value = (h - atlas.height * scale.value) / 2;
}

function centreOn(id: string): void {
  const n = atlas.nodes.get(id);
  if (!n) return;
  const { w, h } = size();
  tx.value = w / 2 - n.x * scale.value;
  ty.value = h / 2 - n.y * scale.value;
}

/** Bring a node into view if it isn't. */
function reveal(id: string): void {
  const n = atlas.nodes.get(id);
  if (!n) return;
  const { w, h } = size();
  const x = n.x * scale.value + tx.value;
  const y = n.y * scale.value + ty.value;
  const m = 48;
  if (x < m || x > w - m || y < m || y > h - m) centreOn(id);
}

function zoomBy(f: number, cx?: number, cy?: number): void {
  const { w, h } = size();
  const px = cx ?? w / 2;
  const py = cy ?? h / 2;
  const next = clamp(scale.value * f);
  // Keep the point under the cursor (or the centre) where it is.
  tx.value = px - ((px - tx.value) * next) / scale.value;
  ty.value = py - ((py - ty.value) * next) / scale.value;
  scale.value = next;
}

function start(): void {
  if (zoom === 'fit') fit();
  else scale.value = clamp(zoom);
  if (zoom !== 'fit' || selected) {
    const id = selected && atlas.nodes.has(selected) ? selected : atlas.roots[0];
    if (id) centreOn(id);
  }
}

onMounted(start);
watch(
  () => [atlas, zoom] as const,
  () => void nextTick(start),
);

// Drag to pan, from anywhere but a node.
let drag: { x: number; y: number; tx: number; ty: number; id: number } | undefined;
function down(e: PointerEvent): void {
  if (e.button !== 0 || (e.target as HTMLElement).closest('.atlas-node, .atlas-tools')) return;
  drag = { x: e.clientX, y: e.clientY, tx: tx.value, ty: ty.value, id: e.pointerId };
  (e.currentTarget as HTMLElement).setPointerCapture?.(e.pointerId);
}
function move(e: PointerEvent): void {
  if (!drag || e.pointerId !== drag.id) return;
  tx.value = drag.tx + e.clientX - drag.x;
  ty.value = drag.ty + e.clientY - drag.y;
}
function up(e: PointerEvent): void {
  if (drag?.id === e.pointerId) drag = undefined;
}
function onWheel(e: WheelEvent): void {
  if (!wheel && !e.ctrlKey && !e.metaKey) return;
  e.preventDefault();
  const r = view.value!.getBoundingClientRect();
  zoomBy(e.deltaY < 0 ? 1.15 : 1 / 1.15, e.clientX - r.left, e.clientY - r.top);
}

const ARROWS: Record<string, Direction> = { ArrowUp: 'up', ArrowDown: 'down', ArrowLeft: 'left', ArrowRight: 'right' };
function key(e: KeyboardEvent, n: AtlasNode): void {
  const dir = ARROWS[e.key];
  if (dir) {
    e.preventDefault();
    const to = neighbour(atlas, n.id, dir);
    if (to) {
      focusId.value = to;
      emit('select', to);
      reveal(to);
      void nextTick(() => view.value?.querySelector<HTMLElement>(`[data-id="${CSS.escape(to)}"]`)?.focus());
    }
    return;
  }
  if (e.key === '+' || e.key === '=') zoomBy(1.25);
  else if (e.key === '-') zoomBy(1 / 1.25);
  else if (e.key === '0') fit();
  else return;
  e.preventDefault();
}

function activate(n: AtlasNode): void {
  focusId.value = n.id;
  if (n.folded) emit('expand', n.id);
  else emit('open', n.id);
}

/** A relation as a curve from one node's edge to the other's, leaving and arriving vertically. */
function curve(from: AtlasNode, to: AtlasNode): string {
  const down = to.y >= from.y;
  const y1 = from.y + (down ? NODE_H / 2 : -NODE_H / 2);
  const y2 = to.y + (down ? -NODE_H / 2 : NODE_H / 2);
  if (from.y === to.y) {
    const dir = to.x > from.x ? 1 : -1;
    const x1 = from.x + (dir * NODE_W) / 2;
    const x2 = to.x - (dir * NODE_W) / 2;
    const bow = Math.min(40, Math.abs(x2 - x1) / 4);
    return `M${x1} ${from.y} C${x1 + dir * bow} ${from.y - bow * 1.5}, ${x2 - dir * bow} ${to.y - bow * 1.5}, ${x2} ${to.y}`;
  }
  const mid = (y1 + y2) / 2;
  return `M${from.x} ${y1} C${from.x} ${mid}, ${to.x} ${mid}, ${to.x} ${y2}`;
}

const edges = computed(() =>
  atlas.edges.map((e) => ({ ...e, d: curve(atlas.nodes.get(e.from)!, atlas.nodes.get(e.to)!) })),
);
const nodes = computed(() => atlas.order.map((id) => atlas.nodes.get(id)!));

const WORDS: Record<string, string> = { ok: 'ok', attention: 'needs attention', down: 'down', maintenance: 'maintenance', repair: 'needs repair', unknown: 'no reading' };
const nodeName = (n: AtlasNode) =>
  n.folded ? `${n.label}, folded: show them` : `${n.label}, ${KIND_WORDS[n.kind] ?? n.kind}, ${WORDS[n.health] ?? n.health}${n.reason ? `: ${n.reason}` : ''}`;

let ro: ResizeObserver | undefined;
onMounted(() => {
  if (typeof ResizeObserver === 'undefined' || !view.value) return;
  let first = true;
  ro = new ResizeObserver(() => {
    if (first) return void (first = false);
    if (zoom === 'fit' && !selected) fit();
  });
  ro.observe(view.value);
});
onBeforeUnmount(() => ro?.disconnect());

defineExpose({ fit, centreOn, zoomBy });
</script>

<template>
  <div
    ref="view"
    class="atlas-canvas"
    role="group"
    :aria-label="label"
    @pointerdown="down"
    @pointermove="move"
    @pointerup="up"
    @pointercancel="up"
    @wheel="onWheel"
  >
    <div class="atlas-world" :style="{ width: `${atlas.width}px`, height: `${atlas.height}px`, transform: `translate(${tx}px, ${ty}px) scale(${scale})` }">
      <svg class="atlas-edges" :width="atlas.width" :height="atlas.height" aria-hidden="true">
        <path
          v-for="e in edges"
          :key="e.key"
          :d="e.d"
          :class="['atlas-edge', `l-${e.layer}`, `t-${e.type}`, { lit: lit?.edges.has(e.key), dim: lit && !lit.edges.has(e.key) }]"
        />
      </svg>
      <button
        v-for="n in nodes"
        :key="n.id"
        type="button"
        :data-id="n.id"
        :class="['atlas-node', `h-${n.health}`, `k-${n.kind}`, { sel: n.id === selected, lit: lit?.nodes.has(n.id), dim: lit && !lit.nodes.has(n.id), fold: n.folded }]"
        :style="{ left: `${n.x - NODE_W / 2}px`, top: `${n.y - NODE_H / 2}px`, width: `${NODE_W}px`, height: `${NODE_H}px` }"
        :tabindex="n.id === roving ? 0 : -1"
        :aria-label="nodeName(n)"
        :aria-current="n.id === selected ? 'true' : undefined"
        @click="activate(n)"
        @focus="focusId = n.id"
        @keydown="key($event, n)"
      >
        <Glyph :kind="n.folded ? 'prog' : glyphFor(n.health)" />
        <span class="atlas-name">{{ n.label }}</span>
        <span class="atlas-kind">{{ n.folded ? 'show' : (KIND_WORDS[n.kind] ?? n.kind) }}</span>
      </button>
    </div>
    <div class="atlas-tools">
      <UiButton size="sm" variant="ghost" aria-label="Zoom in" @click="zoomBy(1.25)">+</UiButton>
      <UiButton size="sm" variant="ghost" aria-label="Zoom out" @click="zoomBy(1 / 1.25)">−</UiButton>
      <UiButton size="sm" variant="ghost" @click="fit()">Fit</UiButton>
    </div>
  </div>
</template>
