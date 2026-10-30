<script setup lang="ts">
/** A monochrome mark for a release's kind (an episode, a film, an album), stroked in the current colour. */
export type MediaKind = 'tv' | 'film' | 'music';
const { kind } = defineProps<{ kind: MediaKind }>();
</script>

<template>
  <svg :class="['md-ic', `md-${kind}`]" viewBox="0 0 16 16" aria-hidden="true">
    <template v-if="kind === 'tv'">
      <rect x="1.5" y="2.5" width="13" height="9" rx="1.5" />
      <path d="M5.5 14h5M8 11.5V14" />
    </template>
    <template v-else-if="kind === 'film'">
      <rect x="2" y="2" width="12" height="12" rx="1.5" />
      <path d="M5 2v12M11 2v12M2 6h3M2 10h3M11 6h3M11 10h3" />
    </template>
    <template v-else>
      <path d="M6 12V3.5l7-1.5v8.5" />
      <circle cx="4.3" cy="12" r="1.8" />
      <circle cx="11.3" cy="10.5" r="1.8" />
    </template>
  </svg>
</template>
