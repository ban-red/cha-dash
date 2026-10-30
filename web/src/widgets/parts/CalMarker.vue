<script setup lang="ts">
/**
 * A calendar's mark: one of five monochrome shapes, by its place in the
 * tile's list of calendars, so several can share a tile without colour
 * (colour means state here). No triangle: ▲ is attention.
 */
import { computed } from 'vue';

const SHAPES = ['dot', 'ring', 'square', 'diamond', 'half'] as const;
const { index } = defineProps<{ index: number }>();
const shape = computed(() => SHAPES[((index % SHAPES.length) + SHAPES.length) % SHAPES.length]!);
</script>

<template>
  <svg :class="['cal-mk', `cal-mk-${shape}`]" viewBox="0 0 10 10" aria-hidden="true">
    <circle v-if="shape === 'dot'" cx="5" cy="5" r="3.2" />
    <circle v-else-if="shape === 'ring'" class="o" cx="5" cy="5" r="2.9" />
    <rect v-else-if="shape === 'square'" x="2" y="2" width="6" height="6" rx="0.8" />
    <path v-else-if="shape === 'diamond'" d="M5 1.2 8.8 5 5 8.8 1.2 5Z" />
    <template v-else>
      <circle class="o" cx="5" cy="5" r="2.9" />
      <path d="M5 2.1a2.9 2.9 0 0 0 0 5.8Z" />
    </template>
  </svg>
</template>
