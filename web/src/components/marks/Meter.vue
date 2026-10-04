<script setup lang="ts">
/**
 * A bar with threshold ticks (bars beat gauges). Single hue until a value
 * crosses `warn` (attention) or `crit` (down). Unknown is a dashed track,
 * never an empty bar.
 */
import { computed } from 'vue';

const {
  value,
  min = 0,
  max = 100,
  warn,
  crit,
  tick,
  label,
  thin = false,
  thick = false,
  unit = '%',
} = defineProps<{
  value: number | null | undefined;
  min?: number;
  max?: number;
  warn?: number | undefined;
  crit?: number | undefined;
  tick?: number | undefined;
  label: string;
  thin?: boolean;
  thick?: boolean;
  unit?: string;
}>();

const known = computed(() => typeof value === 'number' && Number.isFinite(value));
const pct = (v: number) => Math.max(0, Math.min(100, ((v - min) / (max - min || 1)) * 100));
const fill = computed(() => (known.value ? pct(value as number) : 0));
const tickAt = computed(() => (tick !== undefined ? pct(tick) : undefined));
const sev = computed(() => {
  if (!known.value) return 'unknown';
  const v = value as number;
  if (crit !== undefined && v >= crit) return 'down';
  if (warn !== undefined && v >= warn) return 'attn';
  return 'ok';
});
const valueText = computed(() =>
  known.value ? `${Math.round(value as number)}${unit}${tick !== undefined ? `, threshold ${tick}${unit}` : ''}` : 'unknown',
);
</script>

<template>
  <div
    :class="['meter', `sev-${sev}`, { thin, thick, unknown: !known }]"
    role="meter"
    :aria-label="label"
    :aria-valuemin="min"
    :aria-valuemax="max"
    :aria-valuenow="known ? (value as number) : undefined"
    :aria-valuetext="valueText"
  >
    <span v-if="known" class="m-fill" :style="{ width: fill + '%' }"></span>
    <span v-if="tickAt !== undefined" class="m-tick" :style="{ left: tickAt + '%' }"></span>
  </div>
</template>
