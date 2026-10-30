<script setup lang="ts">
/**
 * A tile note in full, when the tile had to cut it short (ClampText): an
 * upstream's health message, say. A UiDialog: focus moves to the text, Esc closes it, and the text scrolls when it's longer than the
 * screen. With the upstream's page for it (a health note's status page),
 * the footer offers to open that in a new tab. Its own chunk, loaded the
 * first time a note is opened.
 */
import { computed, onMounted, useTemplateRef } from 'vue';
import '../styles/act-dialog.css';
import Glyph from '../ui/marks/Glyph.vue';
import { useUi } from '../stores/ui';
import UiButton from '../ui/Button.vue';
import UiDialog from '../ui/Dialog.vue';

const ui = useUi();
const body = useTemplateRef<HTMLDivElement>('body');

const note = computed(() => ui.state.note);

onMounted(() => body.value?.focus());

function close(): void {
  ui.closeNote();
}
</script>

<template>
  <UiDialog class="note-dlg" :title="note?.title" @close="close" @keydown.esc.stop>
    <div ref="body" class="note-dlg-text" tabindex="0" :aria-label="`${note?.title ?? 'Note'}, in full`">
      <Glyph v-if="note?.glyph" :kind="note.glyph" />
      <p>{{ note?.text }}</p>
    </div>
    <template #foot>
      <UiButton v-if="note?.href" :href="note.href" target="_blank" rel="noopener noreferrer" @click="close">Open in {{ note.title }}<span aria-hidden="true"> ↗</span></UiButton>
      <UiButton variant="primary" @click="close">Close</UiButton>
    </template>
  </UiDialog>
</template>

<style>
.note-dlg-text {
  display: flex;
  gap: var(--space-2);
  align-items: flex-start;
  max-height: min(60dvh, 28rem);
  overflow: auto;
  overscroll-behavior: contain;
  padding: 2px;
  font-size: var(--type-body);
  line-height: var(--leading-relaxed);
  color: var(--text);
  overflow-wrap: anywhere;
  white-space: pre-line;
}
.note-dlg-text:focus-visible {
  outline: 2px solid var(--focus);
  outline-offset: 2px;
  border-radius: var(--radius-sm);
}
.note-dlg-text .g {
  margin-top: 0.4em;
}
.note-dlg-text p {
  margin: 0;
}
</style>
