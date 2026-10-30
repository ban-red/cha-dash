<script setup lang="ts">
/**
 * A finished task's log: the lines a container's logs read back (the task
 * opens it on its own, `reads_data`), or any other task's log from its
 * toast's "Show log". The lines are a copy taken when the task ended: the
 * hub keeps them only while it runs and never stores them, and they leave
 * the page when the dialog closes.
 *
 * A UiDialog: focus moves in, Esc closes it and nothing behind it. Its own chunk, loaded the first time a log is shown.
 */
import { computed, onMounted, ref, useTemplateRef } from 'vue';
import '../styles/act-dialog.css';
import { copyText } from '../settings/recipe';
import { useUi } from '../stores/ui';
import UiButton from '../ui/Button.vue';
import UiDialog from '../ui/Dialog.vue';

const ui = useUi();
const pre = useTemplateRef<HTMLPreElement>('pre');
const closeBtn = useTemplateRef<InstanceType<typeof UiButton>>('closeBtn');

const log = computed(() => ui.state.taskLog);
const lines = computed(() => log.value?.lines ?? []);
const copied = ref(false);

onMounted(() => {
  // The lines when there are some, so arrows scroll them; else the way out.
  (pre.value ?? closeBtn.value)?.focus();
  // Start at the newest line, as a terminal would.
  if (pre.value) pre.value.scrollTop = pre.value.scrollHeight;
});

async function copy(): Promise<void> {
  copied.value = await copyText(lines.value.join('\n'));
}

function close(): void {
  ui.closeLog();
}
</script>

<template>
  <UiDialog class="tasklog" :title="log?.label" @close="close" @keydown.esc.stop>
    <template v-if="lines.length">
      <p class="act-hint">The last {{ lines.length === 1 ? 'line' : `${lines.length} lines` }}, newest at the bottom. Cha Dash doesn’t keep them: they’re gone when you close this.</p>
      <pre ref="pre" class="tl-lines" tabindex="0" :aria-label="`Log of ${log?.label ?? 'the task'}`">{{ lines.join('\n') }}</pre>
    </template>
    <p v-else class="act-hint">No lines came back.</p>
    <p class="sr" aria-live="polite">{{ copied ? 'Copied to the clipboard.' : '' }}</p>
    <template #foot>
      <UiButton v-if="lines.length" variant="ghost" @click="copy">{{ copied ? 'Copied' : 'Copy' }}</UiButton>
      <UiButton ref="closeBtn" variant="primary" @click="close">Close</UiButton>
    </template>
  </UiDialog>
</template>

<style>
.dlg.tasklog {
  width: min(880px, calc(100% - 32px));
}
.tl-lines {
  margin: 0;
  max-height: min(60dvh, 32rem);
  overflow: auto;
  padding: 8px 10px;
  border: var(--hairline) solid var(--line);
  border-radius: var(--radius-md);
  font: var(--weight-regular) var(--type-caption) / 1.55 var(--font-num);
  color: var(--text);
  white-space: pre-wrap;
  overflow-wrap: anywhere;
}
</style>
