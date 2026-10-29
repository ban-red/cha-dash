<script setup lang="ts">
/**
 * Drop a config anywhere on the page: while a file is dragged over the
 * window, a drop target covers it; dropping opens Settings → Bring in →
 * Import with the files, which names their source. Admins and the owner
 * only (the page never offers what the hub would refuse). Text dragged
 * from the page itself isn't a file and doesn't show it.
 */
import { onBeforeUnmount, onMounted, ref } from 'vue';
import { useUi } from '../stores/ui';
import { hand } from './fresh';

const ui = useUi();
const shown = ref(false);
let depth = 0;

const hasFiles = (e: DragEvent) => [...(e.dataTransfer?.types ?? [])].includes('Files');
const allowed = () => ui.can.value.panes.includes('import');

function enter(e: DragEvent): void {
  if (!hasFiles(e) || !allowed()) return;
  depth++;
  shown.value = true;
}
function leave(e: DragEvent): void {
  if (!hasFiles(e)) return;
  depth = Math.max(0, depth - 1);
  if (!depth) shown.value = false;
}
function over(e: DragEvent): void {
  if (shown.value) e.preventDefault();
}
function drop(e: DragEvent): void {
  if (!shown.value) return;
  e.preventDefault();
  depth = 0;
  shown.value = false;
  const files = [...(e.dataTransfer?.files ?? [])];
  if (!files.length) return;
  ui.openSettings('import');
  hand(files);
}

onMounted(() => {
  window.addEventListener('dragenter', enter);
  window.addEventListener('dragleave', leave);
  window.addEventListener('dragover', over);
  window.addEventListener('drop', drop);
});
onBeforeUnmount(() => {
  window.removeEventListener('dragenter', enter);
  window.removeEventListener('dragleave', leave);
  window.removeEventListener('dragover', over);
  window.removeEventListener('drop', drop);
});
</script>

<template>
  <div v-if="shown" class="page-drop" role="status">
    <p><b>Drop to import</b><br />Homepage files, a Homarr backup, a board in YAML, or a Cha Dash export.</p>
  </div>
</template>

<style>
.page-drop {
  position: fixed;
  inset: var(--space-4);
  z-index: 60;
  display: grid;
  place-items: center;
  border: 2px dashed var(--text-2);
  border-radius: var(--radius-lg, var(--radius-md));
  background: color-mix(in oklch, var(--bg) 88%, transparent);
  text-align: center;
  pointer-events: none;
}
.page-drop p {
  margin: 0;
  color: var(--text-2);
}
</style>
