<script setup lang="ts">
/**
 * What a widget that loads on demand shows meanwhile (a skeleton, no
 * spinner), and if its code can't be fetched, a quiet sentence. It gets no
 * props from defineAsyncComponent, so it fills its cell whatever the size.
 */
const { failed = false } = defineProps<{ failed?: boolean }>();
</script>

<template>
  <article v-if="!failed" class="tile is-loading" aria-busy="true" aria-label="Loading widget">
    <span class="skel"></span>
    <span class="skel"></span>
  </article>
  <article v-else class="tile" aria-label="Widget unavailable">
    <div class="v sm unknown">—</div>
    <p class="lab">This widget couldn't load. Reload the page to try again.</p>
  </article>
</template>
