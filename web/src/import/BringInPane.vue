<script setup lang="ts">
/**
 * Settings → Bring in: everything that brings things into Cha Dash, with
 * one Review sheet (docs/plan/11-import-and-discovery.md).
 *
 *   /settings/import            Import: drop, choose or paste a config
 *   /settings/import/discover   Discover: this host, leads from what's connected, places you name
 *   /settings/import/history    History: applied imports, Import again, Revert
 *
 * Restoring a Cha Dash export stays in Backup; a dropped export goes there.
 */
import { computed, defineAsyncComponent, ref } from 'vue';
import ImportFlow from './ImportFlow.vue';
import HistoryList from './HistoryList.vue';
import { navigate, settingsPath, view } from '../lib/router';
import type { SourceId } from './model';

const DiscoverPane = defineAsyncComponent(() => import('../discover/DiscoverPane.vue'));

const TABS = [
  { id: '', label: 'Import' },
  { id: 'discover', label: 'Discover' },
  { id: 'history', label: 'History' },
] as const;
const tab = computed(() => {
  const id = view.value.name === 'settings' ? (view.value.id ?? '') : '';
  return TABS.some((t) => t.id === id) ? id : '';
});
const again = ref<SourceId | undefined>();
const flowKey = ref(0);

function go(id: string): void {
  navigate(settingsPath('import') + (id ? `/${id}` : ''), { replace: true });
}

function importAgain(source: SourceId): void {
  again.value = source;
  flowKey.value++;
  go('');
}
</script>

<template>
  <section class="st-section bring-in-head" aria-label="Bring in">
    <p class="st-lede">
      Bring in what you run: import another dashboard’s config, or let Cha Dash find services. Every import shows a preview first, and nothing is deleted.
    </p>
    <p class="st-meta">Restoring a Cha Dash export? That’s in <a :href="settingsPath('backup')" @click.prevent="navigate(settingsPath('backup'), { replace: true })">Backup</a>.</p>
    <nav class="seg bring-in-tabs" aria-label="Bring in">
      <a v-for="t in TABS" :key="t.id" :href="settingsPath('import') + (t.id ? `/${t.id}` : '')" :aria-current="tab === t.id ? 'page' : undefined" @click.prevent="go(t.id)">{{ t.label }}</a>
    </nav>
  </section>
  <ImportFlow v-if="tab === ''" :key="flowKey" :initial-source="again" />
  <DiscoverPane v-else-if="tab === 'discover'" />
  <HistoryList v-else @again="importAgain" />
</template>

<style>
.bring-in-tabs {
  max-width: 420px;
}
.bring-in-tabs a {
  flex: 1;
  padding: 7px 10px;
  border-radius: var(--radius-md);
  font: var(--weight-medium) var(--type-small) / 1 var(--font-ui);
  color: var(--text-2);
  text-align: center;
  text-decoration: none;
}
.bring-in-tabs a[aria-current='page'] {
  background: var(--surface);
  color: var(--text);
  box-shadow:
    0 0 0 var(--hairline) var(--line-2),
    0 1px 2px color-mix(in oklch, var(--bg) 60%, transparent);
}
</style>
