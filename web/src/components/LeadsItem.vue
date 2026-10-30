<script setup lang="ts">
/**
 * A discovery in the Inbox: services the hub sees through what's connected
 * but doesn't read yet (model/leads.ts), one item however many there are.
 * Review opens Settings → Bring in → Discover, where they go through the
 * Review sheet; Dismiss remembers the answer hub-wide, with Undo. Nothing
 * is connected without asking.
 */
import Glyph from '../ui/marks/Glyph.vue';
import { integrationName, type Lead } from '../model/leads';
import { catalogEntry, errorWords } from '../stores/integrations';
import { dismissLeads, restoreLeads } from '../stores/leads';
import { useUi } from '../stores/ui';
import UiButton from '../ui/Button.vue';

const { leads } = defineProps<{ leads: Lead[] }>();
const ui = useUi();
const kindName = (id: string) => catalogEntry(id)?.name ?? integrationName(id);

async function dismiss(): Promise<void> {
  const keys = leads.map((l) => l.key);
  try {
    await dismissLeads(keys);
    ui.toast(`Dismissed ${keys.length === 1 ? leads[0]!.name : `${keys.length} services`}.`, 'ok', 10_000, { label: 'Undo', run: () => void restoreLeads(keys) });
  } catch (err) {
    ui.toast(errorWords(err, 'Couldn’t dismiss them.'), 'error');
  }
}
</script>

<template>
  <div class="ib open disc">
    <Glyph kind="prog" label="discovered" />
    <div>
      <div class="ib-title">{{ leads.length === 1 ? 'One service' : `${leads.length} services` }} found through what’s connected. Review?</div>
      <div class="m">Cha Dash can see {{ leads.length === 1 ? 'it' : 'them' }}, but doesn’t read {{ leads.length === 1 ? 'it' : 'them' }} yet.</div>
      <ul class="disc-list">
        <li v-for="l in leads.slice(0, 6)" :key="l.key">
          <span class="disc-name">{{ l.name }}</span>
          <span class="disc-where">{{ kindName(l.integration) }}<template v-if="l.url"> · {{ l.url.replace(/^https?:\/\//, '') }}</template></span>
        </li>
        <li v-if="leads.length > 6"><span class="disc-where">and {{ leads.length - 6 }} more</span></li>
      </ul>
      <div class="acts disc-acts">
        <UiButton variant="primary" size="sm" @click="ui.openSettings('import', 'discover')">Review</UiButton>
        <UiButton variant="ghost" size="sm" @click="dismiss">Dismiss</UiButton>
      </div>
    </div>
    <span class="when"><span class="ib-kind">Discovery</span></span>
  </div>
</template>
