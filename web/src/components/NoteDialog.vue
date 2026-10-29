<script setup lang="ts">
/**
 * A tile note in full, when the tile had to cut it short (ClampText): an
 * upstream's health message, say. A native modal <dialog>: focus moves to
 * the text, Esc closes it, and the text scrolls when it's longer than the
 * screen. With the upstream's page for it (a health note's status page),
 * the footer offers to open that in a new tab. Its own chunk, loaded the
 * first time a note is opened.
 */
import { computed, onMounted, useId, useTemplateRef } from 'vue';
import '../styles/act-dialog.css';
import Glyph from './marks/Glyph.vue';
import { useModal } from '../board/useModal';
import { useUi } from '../stores/ui';

const ui = useUi();
const uid = useId();
const dlg = useTemplateRef<HTMLDialogElement>('dlg');
const body = useTemplateRef<HTMLDivElement>('body');
useModal(dlg);

const note = computed(() => ui.state.note);

onMounted(() => body.value?.focus());

function close(): void {
  ui.closeNote();
}
</script>

<template>
  <dialog ref="dlg" class="act-dlg note-dlg" :aria-labelledby="`${uid}-h`" @cancel.prevent="close" @keydown.esc.stop>
    <div class="act-dlg-body">
      <header class="act-dlg-head">
        <h2 :id="`${uid}-h`">{{ note?.title }}</h2>
        <button type="button" class="x" aria-label="Close" @click="close">
          <svg viewBox="0 0 10 10" aria-hidden="true"><path d="m2.5 2.5 5 5m0-5-5 5" fill="none" stroke="currentColor" stroke-width="1.5" stroke-linecap="round" /></svg>
        </button>
      </header>
      <div ref="body" class="note-dlg-text" tabindex="0" :aria-label="`${note?.title ?? 'Note'}, in full`">
        <Glyph v-if="note?.glyph" :kind="note.glyph" />
        <p>{{ note?.text }}</p>
      </div>
      <div class="act-dlg-foot">
        <a v-if="note?.href" class="btn" :href="note.href" target="_blank" rel="noopener noreferrer" @click="close">Open in {{ note.title }}<span aria-hidden="true"> ↗</span></a>
        <button type="button" class="btn primary" @click="close">Close</button>
      </div>
    </div>
  </dialog>
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
