<script setup lang="ts">
/**
 * Label discovery's dismissed services on this board, in edit mode (its own
 * chunk, loaded from the edit bar's "Dismissed"). Each row names a service
 * by its container labels; "Offer again" forgets the dismissal on the hub
 * (`POST /api/boards/{id}/discovery/restore`), so the Inbox suggests it
 * again. Nothing is added to the board from here.
 */
import { ref, useId, useTemplateRef } from 'vue';
import { useDiscovery, type DismissedService } from '../stores/discovery';
import { useUi } from '../stores/ui';
import { useModal } from './useModal';

const emit = defineEmits<{ close: [] }>();
const discovery = useDiscovery();
const ui = useUi();
const uid = useId();
const dlg = useTemplateRef<HTMLDialogElement>('dlg');
useModal(dlg);
const busy = ref('');

async function restore(row: DismissedService): Promise<void> {
  busy.value = row.resources[0] ?? '';
  try {
    await discovery.restore(row.resources);
    ui.toast(`${row.name} will be offered again in the Inbox.`);
  } catch {
    ui.toast(`Couldn’t restore ${row.name}. Try again.`, 'error');
  } finally {
    busy.value = '';
  }
}
</script>

<template>
  <dialog ref="dlg" class="edit-dlg dismissed" :aria-labelledby="`${uid}-h`" :aria-describedby="`${uid}-d`" @cancel.prevent="emit('close')">
    <div class="edit-dlg-body">
      <header class="edit-dlg-head">
        <h2 :id="`${uid}-h`">Dismissed services</h2>
        <button type="button" class="x" aria-label="Close" @click="emit('close')">
          <svg viewBox="0 0 10 10" aria-hidden="true"><path d="m2.5 2.5 5 5m0-5-5 5" fill="none" stroke="currentColor" stroke-width="1.5" stroke-linecap="round" /></svg>
        </button>
      </header>
      <p :id="`${uid}-d`" class="ds-lead">Services found by their labels that you said no to on this board. Offer one again and the Inbox asks about it.</p>
      <ul v-if="discovery.dismissedServices.value.length" class="ds-list">
        <li v-for="row in discovery.dismissedServices.value" :key="row.resources.join(' ')">
          <span class="ds-name">{{ row.name }}</span>
          <span v-if="row.group" class="ds-group">{{ row.group }}</span>
          <button type="button" class="btn sm" :disabled="busy !== ''" @click="restore(row)">Offer again<span class="sr">: {{ row.name }}</span></button>
        </li>
      </ul>
      <p v-else class="ds-lead" role="status">Nothing is dismissed on this board.</p>
      <div class="edit-dlg-foot">
        <button type="button" class="btn primary" @click="emit('close')">Done</button>
      </div>
    </div>
  </dialog>
</template>

<style>
.ds-lead {
  margin: 0;
  color: var(--text-2);
  font-size: var(--type-small);
}
.ds-list {
  list-style: none;
  margin: 0;
  padding: 0;
  display: grid;
  gap: var(--space-1);
}
.ds-list li {
  display: flex;
  align-items: center;
  gap: var(--space-2);
  min-height: var(--target-min);
}
.ds-name {
  font-weight: var(--weight-medium);
  color: var(--text);
}
.ds-group {
  color: var(--text-3);
  font-size: var(--type-small);
}
.ds-list .btn {
  margin-left: auto;
}
</style>
