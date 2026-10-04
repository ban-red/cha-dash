<script setup lang="ts">
/**
 * Hourly status bars. The shape carries the state, not only the colour: an
 * outage is a full-height bar, a slow hour a taller one, maintenance a pair of
 * pause strokes, no data a hatched slot. A sentence goes to assistive tech.
 */
import { computed } from 'vue';
import { cellsSummary, type Cell } from '../checks';

const { cells, label } = defineProps<{ cells: readonly Cell[]; label: string }>();
const summary = computed(() => `${label}. ${cellsSummary(cells)}`);
</script>

<template>
  <div class="bars" role="img" :aria-label="summary">
    <i v-for="(c, i) in cells" :key="i" :class="`b-${c}`"></i>
  </div>
</template>
