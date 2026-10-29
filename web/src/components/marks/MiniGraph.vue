<script setup lang="ts">
/**
 * The mini graph: a word-sized trend for a tile. A smooth monotone curve (it
 * never overshoots the data) over a soft gradient, a quiet band for the
 * normal range, and a haloed dot on the last point. Hover or touch scrubs it:
 * a guide and a readout say what the value was and when. No frame, no grid.
 * Gaps (null) break the line rather than pretending to be 0, and a flat
 * series is a flat line. Before there is history, a dotted leader runs from
 * the left edge to the first sample, and the band waits until there is a trend
 * for it to frame. A text summary goes to assistive tech; the scrub is
 * a pointer extra and stays out of the accessibility tree.
 *
 * Optional: a `threshold`, drawn as a dashed line (and kept in range), and
 * `stale`, which hatches the gap after the last known sample instead of
 * leaving it blank. `spanMin` is the minutes the whole series covers (the
 * 24 h history by default), for the readout's "6 h ago"; `at` replaces it.
 */
import { computed, ref, useId } from 'vue';

const {
  values,
  band,
  tone = 'neutral',
  height = 34,
  label,
  format = (v: number) => String(Math.round(v * 10) / 10),
  threshold,
  stale = false,
  spanMin = 1440,
  at,
} = defineProps<{
  values: readonly (number | null)[] | undefined;
  band?: readonly [number, number] | undefined;
  tone?: 'neutral' | 'attn' | 'down';
  height?: number;
  label: string;
  format?: (v: number) => string;
  threshold?: number | undefined;
  stale?: boolean | undefined;
  spanMin?: number;
  at?: ((index: number, count: number) => string) | undefined;
}>();

const gid = useId();
const known = (v: number | null | undefined): v is number => typeof v === 'number' && Number.isFinite(v);
const nums = computed(() => (values ?? []).filter(known));

type Pt = [number, number];

/** A monotone cubic through the points (Fritsch–Carlson): smooth, and never above or below its neighbours. */
function curve(p: Pt[]): string {
  const f = (n: number) => n.toFixed(2);
  if (p.length === 1) return `M${f(p[0]![0])} ${f(p[0]![1])}`;
  const n = p.length;
  const d: number[] = [];
  for (let i = 0; i < n - 1; i++) d.push((p[i + 1]![1] - p[i]![1]) / (p[i + 1]![0] - p[i]![0]));
  const m: number[] = [d[0]!];
  for (let i = 1; i < n - 1; i++) m.push(d[i - 1]! * d[i]! <= 0 ? 0 : (2 * d[i - 1]! * d[i]!) / (d[i - 1]! + d[i]!));
  m.push(d[n - 2]!);
  let out = `M${f(p[0]![0])} ${f(p[0]![1])}`;
  for (let i = 0; i < n - 1; i++) {
    const h = (p[i + 1]![0] - p[i]![0]) / 3;
    out += `C${f(p[i]![0] + h)} ${f(p[i]![1] + m[i]! * h)} ${f(p[i + 1]![0] - h)} ${f(p[i + 1]![1] - m[i + 1]! * h)} ${f(p[i + 1]![0])} ${f(p[i + 1]![1])}`;
  }
  return out;
}

const geom = computed(() => {
  const vs = values ?? [];
  const n = vs.length;
  if (n < 2 || nums.value.length === 0) return undefined;
  const thr = known(threshold) ? threshold : undefined;
  let lo = Math.min(...nums.value, band ? band[0] : Infinity, thr ?? Infinity);
  let hi = Math.max(...nums.value, band ? band[1] : -Infinity, thr ?? -Infinity);
  // Room for the stroke and the dot; a flat series sits mid-height.
  const pad = (hi - lo) * 0.14 || 1;
  lo -= pad;
  hi += pad;
  const y = (v: number) => 100 - ((v - lo) / (hi - lo)) * 100;
  const x = (i: number) => (i / (n - 1)) * 100;
  const pts: (Pt | null)[] = vs.map((v, i) => (known(v) ? [x(i), y(v)] : null));
  let line = '';
  let area = '';
  let run: Pt[] = [];
  const flush = () => {
    if (run.length === 0) return;
    const d = curve(run);
    line += d + ' ';
    if (run.length > 1) area += `${d} L${run[run.length - 1]![0].toFixed(2)} 100 L${run[0]![0].toFixed(2)} 100 Z `;
    run = [];
  };
  for (const p of pts) {
    if (p) run.push(p);
    else flush();
  }
  flush();
  let last: Pt | undefined;
  for (let i = n - 1; i >= 0; i--) {
    if (pts[i]) {
      last = pts[i]!;
      break;
    }
  }
  // No history yet: a dotted leader at the first sample's level stands in for it.
  const first = pts.findIndex((p) => p);
  const lead = first > 0 ? { x: pts[first]![0], y: pts[first]![1] } : undefined;
  // A band frames a trend; with a couple of samples it's just a slab.
  const bandRect = band && nums.value.length >= 4 ? { y: y(band[1]), h: Math.max(0, y(band[0]) - y(band[1])) } : undefined;
  const thrY = thr !== undefined ? y(thr) : undefined;
  // Stale: the line stops at the last sample, and the gap after it is hatched.
  const gap = stale && last && last[0] < 99.5 ? last[0] : undefined;
  return { line: line.trim(), area: area.trim(), pts, last, bandRect, thrY, gap, lead, n };
});

const summary = computed(() => {
  const v = nums.value;
  if (v.length === 0) return `${label}: no data`;
  const now = v[v.length - 1]!;
  const first = v[0]!;
  const trend = now > first * 1.08 ? 'rising' : now < first * 0.92 ? 'falling' : 'steady';
  const normal = band ? `, normal ${format(band[0])} to ${format(band[1])}` : '';
  const limit = typeof threshold === 'number' ? `, threshold ${format(threshold)}` : '';
  return `${label}: ${stale ? 'last known' : 'now'} ${format(now)}${normal}${limit}, ${trend}`;
});

// Scrub: the nearest known sample under the pointer.
const scrub = ref<number | undefined>();
/** Tiles clip what overflows them: with no room above the graph, the readout goes below. */
const below = ref(false);
function ago(i: number, n: number): string {
  if (at) return at(i, n);
  const m = Math.round((n - 1 - i) * (spanMin / n));
  return m <= 0 ? (stale ? 'last known' : 'now') : m < 90 ? `${m} min ago` : `${Math.round(m / 60)} h ago`;
}
function move(e: PointerEvent): void {
  const g = geom.value;
  const el = e.currentTarget as HTMLElement;
  const box = el.getBoundingClientRect();
  if (!g || box.width <= 0) return;
  const tile = el.closest('.tile')?.getBoundingClientRect();
  below.value = !!tile && box.top - tile.top < 30;
  const want = Math.round(Math.min(1, Math.max(0, (e.clientX - box.left) / box.width)) * (g.n - 1));
  let best: number | undefined;
  for (let d = 0; d < g.n && best === undefined; d++) {
    if (g.pts[want - d]) best = want - d;
    else if (g.pts[want + d]) best = want + d;
  }
  scrub.value = best;
}
const at_ = computed(() => {
  const g = geom.value;
  const i = scrub.value;
  const p = g && i !== undefined ? g.pts[i] : undefined;
  if (!g || i === undefined || !p) return undefined;
  return { x: p[0], y: p[1], text: format(values![i] as number), when: ago(i, g.n) };
});
</script>

<template>
  <div
    :class="['spark', 'mg', `tone-${tone}`, { 'is-scrubbing': at_, 'is-stale': stale }]"
    :style="{ height: height + 'px' }"
    role="img"
    :aria-label="summary"
    @pointermove="move"
    @pointerdown="move"
    @pointerleave="scrub = undefined"
    @pointercancel="scrub = undefined"
  >
    <div v-if="geom" class="mg-plot">
      <svg viewBox="0 0 100 100" preserveAspectRatio="none" aria-hidden="true">
        <defs>
          <linearGradient :id="`${gid}-fill`" x1="0" y1="0" x2="0" y2="1">
            <stop class="mg-s0" offset="0" />
            <stop class="mg-s1" offset="1" />
          </linearGradient>
          <linearGradient :id="`${gid}-band`" x1="0" y1="0" x2="1" y2="0">
            <stop class="mg-b0" offset="0" />
            <stop class="mg-b1" offset="0.08" />
            <stop class="mg-b1" offset="0.92" />
            <stop class="mg-b0" offset="1" />
          </linearGradient>
        </defs>
        <template v-if="geom.bandRect">
          <rect class="mg-band" x="0" :y="geom.bandRect.y" width="100" :height="geom.bandRect.h" :fill="`url(#${gid}-band)`" />
          <line class="mg-edge" x1="0" x2="100" :y1="geom.bandRect.y" :y2="geom.bandRect.y" vector-effect="non-scaling-stroke" />
          <line class="mg-edge" x1="0" x2="100" :y1="geom.bandRect.y + geom.bandRect.h" :y2="geom.bandRect.y + geom.bandRect.h" vector-effect="non-scaling-stroke" />
        </template>
        <line v-if="geom.lead" class="mg-lead" x1="0" :x2="geom.lead.x" :y1="geom.lead.y" :y2="geom.lead.y" vector-effect="non-scaling-stroke" />
        <path v-if="geom.area" class="mg-area" :d="geom.area" :fill="`url(#${gid}-fill)`" />
        <path class="mg-line" :d="geom.line" vector-effect="non-scaling-stroke" />
        <line v-if="geom.thrY !== undefined" class="mg-thr" x1="0" x2="100" :y1="geom.thrY" :y2="geom.thrY" vector-effect="non-scaling-stroke" />
      </svg>
    </div>
    <span v-if="geom?.gap !== undefined" class="mg-gap" :style="{ left: geom.gap + '%' }"></span>
    <span v-if="geom?.last" class="mg-dot" :style="{ left: geom.last[0] + '%', top: geom.last[1] + '%' }"></span>
    <template v-if="at_">
      <span class="mg-guide" :style="{ left: at_.x + '%' }" aria-hidden="true"></span>
      <span class="mg-pin" :style="{ left: at_.x + '%', top: at_.y + '%' }" aria-hidden="true"></span>
      <span :class="['mg-read', at_.x > 55 ? 'to-left' : 'to-right', { below }]" :style="{ left: at_.x + '%' }" aria-hidden="true">
        <b>{{ at_.text }}</b> {{ at_.when }}
      </span>
    </template>
  </div>
</template>

<style>
.mg {
  --mg-ink: var(--text-2);
  position: relative;
  width: 100%;
  color: var(--mg-ink);
  touch-action: pan-y;
}
.mg-plot {
  position: absolute;
  inset: 0;
}
.mg-plot svg {
  position: absolute;
  inset: 0;
  width: 100%;
  height: 100%;
  overflow: visible;
}
.mg.tone-attn {
  --mg-ink: var(--attention);
}
.mg.tone-down {
  --mg-ink: var(--down);
}
.mg-s0 {
  stop-color: var(--mg-ink);
  stop-opacity: 0.26;
}
.mg-s1 {
  stop-color: var(--mg-ink);
  stop-opacity: 0;
}
.mg-b0 {
  stop-color: var(--band);
  stop-opacity: 0;
}
.mg-b1 {
  stop-color: var(--band);
  stop-opacity: 1;
}
.mg-line {
  fill: none;
  stroke: currentColor;
  stroke-width: 1.75;
  stroke-linejoin: round;
  stroke-linecap: round;
}
.mg-edge {
  stroke: var(--line-2);
  stroke-width: 1;
  stroke-dasharray: 1 4;
  stroke-linecap: round;
}
.mg-lead {
  stroke: currentColor;
  stroke-width: 1.75;
  stroke-linecap: round;
  stroke-dasharray: 0.1 5;
  opacity: 0.55;
}
.mg-thr {
  stroke: var(--text-3);
  stroke-width: 1;
  stroke-dasharray: 3 3;
}
.mg-gap {
  position: absolute;
  top: 0;
  right: 0;
  bottom: 0;
  border-left: var(--hairline) dashed var(--line-2);
  background: repeating-linear-gradient(135deg, var(--hatch) 0 1.5px, transparent 1.5px 6px);
}
.mg-dot {
  position: absolute;
  width: 7px;
  height: 7px;
  margin: -3.5px 0 0 -3.5px;
  border-radius: 50%;
  background: currentColor;
  box-shadow:
    0 0 0 2px var(--tile-bg, var(--surface)),
    0 0 0 4px color-mix(in oklch, currentColor 28%, transparent);
}
:root[data-surface='ruled'] .mg-dot {
  box-shadow:
    0 0 0 2px var(--bg),
    0 0 0 4px color-mix(in oklch, currentColor 28%, transparent);
}
.mg.is-stale .mg-dot {
  box-shadow: 0 0 0 2px var(--tile-bg, var(--surface));
  opacity: 0.8;
}
.mg-guide {
  position: absolute;
  top: 0;
  bottom: 0;
  width: 0;
  border-left: 1px solid var(--line-2);
  pointer-events: none;
}
.mg-pin {
  position: absolute;
  width: 7px;
  height: 7px;
  margin: -3.5px 0 0 -3.5px;
  border-radius: 50%;
  background: var(--tile-bg, var(--surface));
  border: 1.5px solid currentColor;
  pointer-events: none;
}
.mg-read {
  position: absolute;
  bottom: calc(100% + 6px);
  z-index: 2;
  padding: 2px 7px;
  border-radius: 6px;
  border: var(--hairline) solid var(--line-2);
  background: var(--surface-2);
  color: var(--text-2);
  font-size: 11.5px;
  line-height: 1.5;
  white-space: nowrap;
  pointer-events: none;
}
.mg-read.below {
  bottom: auto;
  top: calc(100% + 6px);
}
.mg-read b {
  color: var(--text);
  font-weight: 600;
}
.mg-read.to-right {
  transform: translateX(-8px);
}
.mg-read.to-left {
  transform: translateX(calc(-100% + 8px));
}
.mg.is-scrubbing .mg-dot {
  opacity: 0.35;
}

@media (prefers-reduced-motion: no-preference) {
  /* Drawn in once, left to right, when it first appears. */
  .mg-plot {
    animation: mg-reveal 0.7s cubic-bezier(0.2, 0.7, 0.2, 1) both;
  }
  .mg-dot {
    animation: mg-dot 0.5s 0.55s ease-out both;
  }
}
@keyframes mg-reveal {
  from {
    clip-path: inset(-6px 100% -6px 0);
  }
  to {
    clip-path: inset(-6px -6px -6px 0);
  }
}
@keyframes mg-dot {
  from {
    opacity: 0;
    transform: scale(0.4);
  }
  to {
    opacity: 1;
    transform: scale(1);
  }
}
</style>
