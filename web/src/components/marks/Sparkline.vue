<script setup lang="ts">
/**
 * A word-sized sparkline (Tufte): a gray band for the normal range, a dot on
 * the last point, no frame and no grid. Gaps (null) break the line rather
 * than pretending to be 0. A text summary goes to assistive tech.
 */
import { computed } from 'vue';

const {
  values,
  band,
  tone = 'neutral',
  height = 34,
  label,
  format = (v: number) => String(Math.round(v * 10) / 10),
} = defineProps<{
  values: readonly (number | null)[] | undefined;
  band?: readonly [number, number] | undefined;
  tone?: 'neutral' | 'attn' | 'down';
  height?: number;
  label: string;
  format?: (v: number) => string;
}>();

const nums = computed(() => (values ?? []).filter((v): v is number => typeof v === 'number' && Number.isFinite(v)));

const geom = computed(() => {
  const vs = values ?? [];
  const n = vs.length;
  if (n < 2 || nums.value.length === 0) return undefined;
  let lo = Math.min(...nums.value, band ? band[0] : Infinity);
  let hi = Math.max(...nums.value, band ? band[1] : -Infinity);
  const pad = (hi - lo) * 0.14 || 1;
  lo -= pad;
  hi += pad;
  const y = (v: number) => 100 - ((v - lo) / (hi - lo)) * 100;
  const x = (i: number) => (i / (n - 1)) * 100;
  let line = '';
  let area = '';
  let run: [number, number][] = [];
  const flush = () => {
    if (run.length === 0) return;
    const d = run.map((p, i) => (i ? 'L' : 'M') + p[0].toFixed(2) + ' ' + p[1].toFixed(2)).join(' ');
    line += d + ' ';
    if (run.length > 1) area += `${d} L${run[run.length - 1]![0].toFixed(2)} 100 L${run[0]![0].toFixed(2)} 100 Z `;
    run = [];
  };
  vs.forEach((v, i) => {
    if (typeof v === 'number' && Number.isFinite(v)) run.push([x(i), y(v)]);
    else flush();
  });
  flush();
  let last: [number, number] | undefined;
  for (let i = n - 1; i >= 0; i--) {
    const v = vs[i];
    if (typeof v === 'number' && Number.isFinite(v)) {
      last = [x(i), y(v)];
      break;
    }
  }
  const bandRect = band ? { y: y(band[1]), h: y(band[0]) - y(band[1]) } : undefined;
  return { line: line.trim(), area: area.trim(), last, bandRect };
});

const summary = computed(() => {
  const v = nums.value;
  if (v.length === 0) return `${label}: no data`;
  const now = v[v.length - 1]!;
  const first = v[0]!;
  const trend = now > first * 1.08 ? 'rising' : now < first * 0.92 ? 'falling' : 'steady';
  const normal = band ? `, normal ${format(band[0])} to ${format(band[1])}` : '';
  return `${label}: now ${format(now)}${normal}, ${trend}`;
});
</script>

<template>
  <div :class="['spark', `tone-${tone}`]" :style="{ height: height + 'px' }" role="img" :aria-label="summary">
    <svg v-if="geom" viewBox="0 0 100 100" preserveAspectRatio="none" aria-hidden="true">
      <rect v-if="geom.bandRect" class="sp-band" x="0" :y="geom.bandRect.y" width="100" :height="Math.max(0, geom.bandRect.h)" />
      <path v-if="geom.area" class="sp-area" :d="geom.area" />
      <path class="sp-line" :d="geom.line" vector-effect="non-scaling-stroke" />
    </svg>
    <span v-if="geom?.last" class="sp-dot" :style="{ left: geom.last[0] + '%', top: geom.last[1] + '%' }"></span>
  </div>
</template>
