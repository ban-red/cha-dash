<script setup lang="ts">
/**
 * A discovery in the Inbox: containers whose `homepage.*` labels name a
 * group and a service, offered as tiles for this board. "Add them" puts
 * every one on the board in one save; "Dismiss" remembers the answer on the
 * hub, so they aren't offered again. Nothing is ever added without asking.
 */
import { computed, ref } from 'vue';
import Glyph from './marks/Glyph.vue';
import { placementSentence } from '../model/discovery';
import { useDiscovery } from '../stores/discovery';
import { useLive } from '../stores/live';
import { useUi } from '../stores/ui';

const discovery = useDiscovery();
const live = useLive();
const ui = useUi();
const busy = ref(false);

const list = computed(() => discovery.suggestions.value);
const title = computed(() => {
  const n = list.value.length;
  return `${n === 1 ? 'One service' : `${n} services`} found by ${n === 1 ? 'its' : 'their'} labels. Add ${n === 1 ? 'it' : 'them'}?`;
});
const where = computed(() => placementSentence(list.value));

async function add(): Promise<void> {
  busy.value = true;
  try {
    const added = await discovery.add();
    if (added === null) ui.toast('This board changed somewhere else. Showing the latest; try again.', 'error');
    else if (added.length) ui.toast(`Added ${added.length === 1 ? added[0]!.name : `${added.length} services`} to the board. Edit the layout to move or remove ${added.length === 1 ? 'it' : 'them'}.`);
    else ui.toast('None of them fit their section. Widen it in edit mode and try again.', 'error');
  } catch {
    ui.toast('Couldn’t save the board. Nothing was added.', 'error');
  } finally {
    busy.value = false;
  }
}

async function dismiss(): Promise<void> {
  busy.value = true;
  try {
    await discovery.dismiss();
  } catch {
    ui.toast('Couldn’t remember that on the hub; it may be offered again.', 'error');
  } finally {
    busy.value = false;
  }
}
</script>

<template>
  <div class="ib open disc">
    <Glyph kind="prog" label="discovered" />
    <div>
      <div class="ib-title">{{ title }}</div>
      <div class="m">{{ where }}</div>
      <ul class="disc-list">
        <li v-for="s in list" :key="s.resource">
          <span class="disc-name">{{ s.name }}</span>
          <span class="disc-where">{{ s.sectionTitle }}{{ s.newSection ? ' (new section)' : '' }}</span>
        </li>
      </ul>
      <div class="acts disc-acts">
        <button type="button" class="btn primary sm" :disabled="busy || live.stale.value" @click="add">{{ list.length === 1 ? 'Add it' : 'Add them' }}</button>
        <button type="button" class="btn ghost sm" :disabled="busy || live.stale.value" @click="dismiss">Dismiss</button>
      </div>
    </div>
    <span class="when"><span class="ib-kind">Discovery</span></span>
  </div>
</template>

<style>
.disc-list {
  list-style: none;
  margin: var(--space-2) 0 0;
  padding: 0;
  font-size: var(--type-small);
}
.disc-list li {
  display: flex;
  justify-content: space-between;
  gap: var(--space-3);
  padding: 4px 0;
  border-top: var(--hairline) solid var(--line);
}
.disc-name {
  min-width: 0;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
  color: var(--text);
}
.disc-where {
  flex: none;
  color: var(--text-3);
}
.disc-acts {
  display: flex;
  flex-wrap: wrap;
  gap: var(--space-2);
  margin-top: var(--space-3);
}
</style>
