<script setup lang="ts">
/**
 * Text cut to a few lines, as tiles need. When it is cut, it becomes a
 * button that opens it whole (NoteDialog) and ends with a "more" cue; when
 * it fits, it's plain text. Whether it's cut is measured, so a wide tile
 * that fits the whole message shows no button. With an `href` (the
 * upstream's page that explains it), text that fits is a link there, in a
 * new tab, and the dialog of cut text offers the same link.
 */
import { onBeforeUnmount, onMounted, ref, useTemplateRef, watch } from 'vue';
import type { GlyphKind } from './marks/Glyph.vue';
import { useUi } from '../stores/ui';

const {
  text,
  title,
  glyph,
  href,
  lines = 2,
} = defineProps<{
  text: string;
  /** The dialog's heading: whose note it is. */
  title: string;
  glyph?: GlyphKind | undefined;
  href?: string | undefined;
  lines?: number;
}>();

const ui = useUi();
const el = useTemplateRef<HTMLElement>('el');
const cut = ref(false);
let ro: ResizeObserver | undefined;

function measure(): void {
  const e = el.value;
  cut.value = !!e && e.scrollHeight > e.clientHeight + 1;
}

onMounted(() => {
  measure();
  if (typeof ResizeObserver !== 'undefined' && el.value) {
    ro = new ResizeObserver(measure);
    ro.observe(el.value);
  }
});
onBeforeUnmount(() => ro?.disconnect());
watch(
  () => text,
  () => requestAnimationFrame(measure),
);

function open(): void {
  ui.showNote({ title, text, ...(glyph && { glyph }), ...(href && { href }) });
}
</script>

<template>
  <button v-if="cut" type="button" class="clamp is-cut" :style="{ '--clamp-lines': lines }" @click.stop="open">
    <span ref="el" class="clamp-text">{{ text }}</span>
    <!-- Named by what it shows (WCAG 2.5.3): "more" is read, and says what it does. -->
    <span class="clamp-more">more<span class="sr">, show all</span></span>
  </button>
  <a v-else-if="href" class="clamp clamp-link" :href="href" target="_blank" rel="noopener noreferrer" :style="{ '--clamp-lines': lines }" @click.stop>
    <span ref="el" class="clamp-text">{{ text }}<span aria-hidden="true"> ↗</span></span>
  </a>
  <span v-else class="clamp" :style="{ '--clamp-lines': lines }"><span ref="el" class="clamp-text">{{ text }}</span></span>
</template>

<style>
.clamp {
  display: block;
  min-width: 0;
}
.clamp-text {
  display: -webkit-box;
  -webkit-box-orient: vertical;
  -webkit-line-clamp: var(--clamp-lines, 2);
  line-clamp: var(--clamp-lines, 2);
  overflow: hidden;
  overflow-wrap: anywhere;
}
.note button.clamp,
button.clamp {
  width: 100%;
  text-decoration: none;
  padding: 0;
  border: 0;
  background: none;
  font: inherit;
  color: inherit;
  text-align: left;
  cursor: pointer;
}
.clamp-more {
  display: inline-block;
  margin-top: 1px;
  font: var(--weight-medium) var(--type-caption) / 1.2 var(--font-ui);
  color: var(--text-3);
  text-decoration: underline;
  text-decoration-color: color-mix(in oklch, currentColor 45%, transparent);
  text-underline-offset: 2px;
}
a.clamp-link {
  color: inherit;
  text-decoration: underline;
  text-decoration-color: color-mix(in oklch, currentColor 35%, transparent);
  text-underline-offset: 2px;
}
a.clamp-link:hover {
  text-decoration-color: currentColor;
}
a.clamp-link:focus-visible {
  outline: 2px solid var(--focus);
  outline-offset: 2px;
  border-radius: var(--radius-sm);
}
button.clamp:hover .clamp-more {
  color: var(--text-2);
}
button.clamp:focus-visible {
  outline: 2px solid var(--focus);
  outline-offset: 2px;
  border-radius: var(--radius-sm);
}
</style>
