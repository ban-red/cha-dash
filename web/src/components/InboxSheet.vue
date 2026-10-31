<script setup lang="ts">
/**
 * The Inbox, grouped by status: open (problems that need you, down first,
 * then integration repairs), discovered (services found by their labels,
 * offered as tiles: stores/discovery.ts), approvals (plans an API token made
 * that wait for an admin or the owner), suggestions (the hub's correlator
 * asking whether two things are one), being fixed (a fix is running, or
 * it's recovering by itself), and resolved. Real and demo hubs fill it the
 * same way, from the `inbox` topic. A UiSheet: a glass panel on desktop, a
 * bottom sheet on a phone that a flick dismisses. An item that resolves while you watch shows
 * its done row in place for a moment, then moves to Resolved.
 */
import { computed, defineAsyncComponent, onMounted, reactive, watch } from 'vue';
import Glyph from '../ui/marks/Glyph.vue';
import DiscoveryItem from './DiscoveryItem.vue';
import LeadsItem from './LeadsItem.vue';
import IncidentItem from './IncidentItem.vue';
import SuggestionItem from './SuggestionItem.vue';
// Approvals are rare and only for owners and admins: their own chunk.
const ApprovalItem = defineAsyncComponent(() => import('./ApprovalItem.vue'));
import { hhmm } from '../lib/format';
import { groupInbox, hintsHeading, incidents, isApproval, isSuggestion, type InboxGroups } from '../model/inbox';
import { useDiscovery } from '../stores/discovery';
import { loadLeads, useLeads } from '../stores/leads';
import { useLive } from '../stores/live';
import { useUi } from '../stores/ui';
import type { Incident } from '../model/types';
import UiCloseButton from '../ui/CloseButton.vue';
import UiSheet from '../ui/Sheet.vue';

const live = useLive();
const ui = useUi();
const discovery = useDiscovery();
const labels = computed(() => discovery.suggestions.value.length > 0);
// Leads are an admin's: they open Settings → Bring in. One Inbox item for all of them.
const leads = useLeads(live);
const leadsShown = computed(() => ui.can.value.panes.includes('import') && leads.value.length > 0);
const discovered = computed(() => labels.value || leadsShown.value);
const expanded = reactive(new Set<string>());
const lingering = reactive(new Map<string, number>());

const LINGER_MS = 2600;

const all = computed(() => incidents(live.inbox.value));
const isLingering = (id: string) => lingering.has(id);
const groups = computed(() => groupInbox(live.inbox.value, live.fixes));

/**
 * Lists hold still while you read them: an item keeps the group it had when
 * the Inbox opened (or when it arrived), even if a fix moves it to "being
 * fixed" meanwhile. The groups re-sort the next time the Inbox opens.
 */
type Place = 'open' | 'fixing';
const placed = reactive(new Map<string, Place>());
function place(g: InboxGroups, reset = false) {
  if (reset) {
    for (const id of [...placed.keys()]) if (!isLingering(id)) placed.delete(id);
  }
  for (const i of g.open) if (!placed.has(i.id)) placed.set(i.id, 'open');
  for (const i of g.fixing) if (!placed.has(i.id)) placed.set(i.id, 'fixing');
}
watch(groups, (g) => place(g), { immediate: true, flush: 'sync' });

const inGroup = (where: Place): Incident[] =>
  [...groups.value.open, ...groups.value.fixing, ...groups.value.resolved.filter((i) => isLingering(i.id))].filter((i) => placed.get(i.id) === where);
const open = computed(() => inGroup('open'));
const fixing = computed(() => inGroup('fixing'));
const openNow = (list: Incident[]) => list.filter((i) => i.status === 'open').length;
const done = computed(() => groups.value.resolved.filter((i) => !isLingering(i.id)));
const suggestions = computed(() => groups.value.suggestions);
const approvals = computed(() => groups.value.approvals);
const hints = computed(() => groups.value.hints);
/** Open items plus the discovery question, the approvals and the suggestions, which need an answer too. */
const openCount = computed(() => openNow(open.value) + (discovered.value ? 1 : 0) + approvals.value.length + suggestions.value.length);

// Keep a just-resolved item in place briefly, expanded on its done row.
watch(
  () => all.value.map((i) => `${i.id}:${i.status}`).join('|'),
  (_now, before) => {
    if (!before) return;
    const was = new Map(before.split('|').map((s) => s.split(':') as [string, string]));
    for (const i of all.value) {
      // An answered suggestion says so in a toast and moves to Resolved.
      if (i.status === 'resolved' && was.get(i.id) === 'open' && !isSuggestion(i) && !isApproval(i)) {
        lingering.set(i.id, Date.now());
        if (!placed.has(i.id)) placed.set(i.id, 'open');
        expanded.add(i.id);
        setTimeout(() => {
          lingering.delete(i.id);
          placed.delete(i.id);
        }, LINGER_MS);
      }
    }
  },
);

watch(
  () => ui.state.inboxFocus,
  (id) => {
    if (id) expanded.add(id);
  },
  { immediate: true },
);

function toggle(id: string) {
  if (expanded.has(id)) expanded.delete(id);
  else expanded.add(id);
}

// Lists re-sort each time it opens.
watch(
  () => ui.state.inboxOpen,
  (open) => open && place(groups.value, true),
  { immediate: true },
);
/** On open, focus the item it was opened for, if any; UiSheet focuses the panel otherwise. */
function focusItem(panel: HTMLElement): void {
  const id = ui.state.inboxFocus;
  if (id) panel.querySelector<HTMLElement>(`[aria-controls="ib-${CSS.escape(id)}"]`)?.focus();
}
onMounted(() => {
  if (ui.can.value.panes.includes('import')) void loadLeads();
});
</script>

<template>
  <UiSheet :open="ui.state.inboxOpen" class="inbox" aria-labelledby="inbox-h" @close="ui.closeInbox()" @open="focusItem">
    <div class="ib-head">
      <h2 id="inbox-h">Inbox</h2>
      <span class="n">{{ openCount ? `${openCount} need${openCount === 1 ? 's' : ''} you` : 'nothing open' }}</span>
      <UiCloseButton label="Close Inbox" @click="ui.closeInbox()" />
    </div>

    <div v-if="live.stale.value" class="ib">
      <Glyph kind="down" label="unreachable" />
      <div>
        <div class="ib-title">The hub is unreachable</div>
        <div class="m">This is the Inbox as of {{ hhmm(live.offlineAt.value) }}. Actions come back when the hub does.</div>
      </div>
    </div>

    <template v-if="open.length">
      <h3 class="ib-grp">Open · {{ openNow(open) }}</h3>
      <IncidentItem v-for="i in open" :key="i.id" :incident="i" :expanded="expanded.has(i.id)" @toggle="toggle(i.id)" />
    </template>

    <template v-if="approvals.length">
      <h3 class="ib-grp">Waiting for approval · {{ approvals.length }}</h3>
      <ApprovalItem v-for="i in approvals" :key="i.id" :incident="i" />
    </template>

    <template v-if="discovered">
      <h3 class="ib-grp">Discovered</h3>
      <DiscoveryItem v-if="labels" />
      <LeadsItem v-if="leadsShown" :leads="leads" />
    </template>

    <template v-if="suggestions.length">
      <h3 class="ib-grp">Suggestions · {{ suggestions.length }}</h3>
      <SuggestionItem v-for="i in suggestions" :key="i.id" :incident="i" />
    </template>

    <template v-if="fixing.length">
      <h3 class="ib-grp">Being fixed · {{ openNow(fixing) }}</h3>
      <IncidentItem v-for="i in fixing" :key="i.id" :incident="i" :expanded="expanded.has(i.id)" @toggle="toggle(i.id)" />
    </template>

    <template v-if="hints.length">
      <h3 class="ib-grp">{{ hintsHeading(hints) }}</h3>
      <IncidentItem v-for="i in hints" :key="i.id" :incident="i" :expanded="expanded.has(i.id)" @toggle="toggle(i.id)" />
    </template>

    <div v-if="!open.length && !fixing.length && !discovered && !suggestions.length && !approvals.length" class="ib-empty">
      <Glyph kind="check" />
      <div class="t">Nothing needs you</div>
      <div class="m">Every integration is reading normally.{{ done[0] ? ` The last fix was “${done[0].title}”, at ${hhmm(done[0].resolved_at)}.` : '' }}</div>
    </div>

    <template v-if="done.length">
      <h3 class="ib-grp">Resolved · {{ done.length }}</h3>
      <IncidentItem v-for="i in done" :key="i.id" :incident="i" :expanded="expanded.has(i.id)" @toggle="toggle(i.id)" />
    </template>
  </UiSheet>
</template>
