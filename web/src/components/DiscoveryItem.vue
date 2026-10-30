<script setup lang="ts">
/**
 * A discovery in the Inbox: containers whose `homepage.*` labels name a
 * group and a service, offered as tiles for this board. "Add them" puts
 * every one on the board in one save; "Dismiss" remembers the answer on the
 * hub, so they aren't offered again, and its toast's Undo takes it back
 * (edit mode lists every dismissed one too). Nothing is ever added without
 * asking. A container that is a known integration says which catalog widget
 * it becomes, or which integration to connect for one.
 */
import { computed, ref } from 'vue';
import Glyph from '../ui/marks/Glyph.vue';
import { placementSentence, type LabelService } from '../model/discovery';
import { WIDGETS } from '../model/widgets';
import { useDiscovery } from '../stores/discovery';
import { useLive } from '../stores/live';
import { useUi } from '../stores/ui';
import UiButton from '../ui/Button.vue';

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

/** How a service lands: as a catalog widget, or a service tile (and what to connect for a widget). */
function how(s: LabelService): string {
  if (s.widget) return `as ${s.widget.type === 'service' ? `${s.known?.name ?? 'its'} service tile` : (WIDGETS[s.widget.type]?.name ?? s.widget.type)}`;
  return s.known ? `connect ${s.known.name} for its widget` : '';
}

async function dismiss(): Promise<void> {
  busy.value = true;
  const n = list.value.length;
  const first = list.value[0]?.name ?? '';
  try {
    const refs = await discovery.dismiss();
    ui.toast(`Dismissed ${n === 1 ? first : `${n} services`}. ${n === 1 ? 'It isn’t' : 'They aren’t'} offered again.`, 'ok', 10_000, {
      label: 'Undo',
      run: () => void discovery.restore(refs).catch(() => ui.toast('Couldn’t undo that on the hub. Restore it from edit mode.', 'error')),
    });
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
          <span class="disc-where">{{ s.sectionTitle }}{{ s.newSection ? ' (new section)' : '' }}<template v-if="how(s)"> · {{ how(s) }}</template></span>
        </li>
      </ul>
      <div class="acts disc-acts">
        <UiButton variant="primary" size="sm" :disabled="busy || live.stale.value" @click="add">{{ list.length === 1 ? 'Add it' : 'Add them' }}</UiButton>
        <UiButton variant="ghost" size="sm" :disabled="busy || live.stale.value" @click="dismiss">Dismiss</UiButton>
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
