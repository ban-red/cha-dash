<script setup lang="ts">
/**
 * The round × that closes a dialog, sheet or toast. Name it for what it
 * does there ("Close settings", "Cancel", "Dismiss"): the icon has no text.
 */
const { label = 'Close' } = defineProps<{ label?: string }>();
</script>

<template>
  <button type="button" class="x" :aria-label="label">
    <svg viewBox="0 0 10 10" aria-hidden="true"><path d="m2.5 2.5 5 5m0-5-5 5" fill="none" stroke="currentColor" stroke-width="1.5" stroke-linecap="round" /></svg>
  </button>
</template>
