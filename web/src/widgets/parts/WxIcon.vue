<script setup lang="ts">
/** A monochrome weather mark per condition family, stroked in the current colour. */
export type WxFamily = 'sun' | 'moon' | 'cloud' | 'rain' | 'snow' | 'fog' | 'storm';
const { family, label = '' } = defineProps<{ family: WxFamily; label?: string | undefined }>();
const CLOUD = 'M5.5 12.5h9a3.1 3.1 0 0 0 .3-6.2 4.6 4.6 0 0 0-8.8 1.2 2.5 2.5 0 0 0-.5 5Z';
</script>

<template>
  <svg class="wx-ic" viewBox="0 0 20 20" :role="label ? 'img' : undefined" :aria-label="label || undefined" :aria-hidden="label ? undefined : 'true'">
    <template v-if="family === 'sun'">
      <circle cx="10" cy="10" r="3.6" />
      <path d="M10 1.5v2.4M10 16.1v2.4M1.5 10h2.4M16.1 10h2.4M4 4l1.7 1.7M14.3 14.3 16 16M4 16l1.7-1.7M14.3 5.7 16 4" />
    </template>
    <path v-else-if="family === 'moon'" d="M15.5 12.6A6.5 6.5 0 0 1 7.4 4.5a6.5 6.5 0 1 0 8.1 8.1Z" />
    <path v-else-if="family === 'cloud'" d="M5.5 15.5h9a3.3 3.3 0 0 0 .3-6.6 4.8 4.8 0 0 0-9.2 1.3A2.7 2.7 0 0 0 5.5 15.5Z" />
    <path v-else-if="family === 'rain'" :d="`${CLOUD}M7 15l-.8 2.2M10.5 15l-.8 2.2M14 15l-.8 2.2`" />
    <path v-else-if="family === 'snow'" :d="`${CLOUD}M7 15.6h.01M10.5 16.6h.01M14 15.6h.01`" />
    <path v-else-if="family === 'fog'" d="M3 7.5h14M4.5 11h11M3 14.5h14" />
    <path v-else :d="`${CLOUD}M10.8 12.5l-2 3h2.6l-1.6 3`" />
  </svg>
</template>
