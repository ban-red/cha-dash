<script setup lang="ts">
/**
 * One line of recipe text: `code` and **bold** (the only markup recipes
 * allow, rendered as elements, never as HTML), with the generator's values
 * filled into {{placeholders}} and a visible gap where one is still missing.
 */
import { computed } from 'vue';
import { inline, segments, type Recipe } from './recipe';

const { text, recipe, values } = defineProps<{ text: string; recipe: Recipe; values: Record<string, string | undefined> }>();

const parts = computed(() => inline(text).map((p) => ({ t: p.t, segs: segments(p.v, recipe, values) })));
</script>

<template>
  <template v-for="(p, i) in parts" :key="i">
    <component :is="p.t === 'code' ? 'code' : p.t === 'strong' ? 'b' : 'span'">
      <template v-for="(s, j) in p.segs" :key="j">
        <span v-if="s.t === 'val'" class="rc-val">{{ s.v }}</span>
        <span v-else-if="s.t === 'gap'" class="rc-gap">‹{{ s.label }}›</span>
        <template v-else>{{ s.v }}</template>
      </template>
    </component>
  </template>
</template>
