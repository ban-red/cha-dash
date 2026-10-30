<script setup lang="ts">
/**
 * Settings → Bring in → Discover: discovery outlives the wizard.
 *
 * - Leads: services the hub already sees through what's connected
 *   (model/leads.ts). Finding them touches no network; Check probes only
 *   the addresses on screen. The ones picked go to the Review sheet as the
 *   discovery import, so they land like any import: one transaction, in
 *   History, revertible. Dismiss is hub-wide, with Undo.
 * - This host and places you name: the wizard's Discover step.
 */
import { computed, onMounted, reactive, ref, shallowRef } from 'vue';
import Glyph from '../ui/marks/Glyph.vue';
import DiscoverStep from '../onboarding/DiscoverStep.vue';
import ImportFlow from '../import/ImportFlow.vue';
import { setDraft } from '../onboarding/draft';
import { discover, type DiscoverResult } from '../onboarding/discover';
import { navigate, settingsPath } from '../lib/router';
import { plural } from '../lib/format';
import { integrationName, leadsJSON, type Lead } from '../model/leads';
import { catalogEntry, errorWords } from '../stores/integrations';
import { dismissLeads, leadsState, loadLeads, restoreLeads, useDismissedLeads, useLeads } from '../stores/leads';
import { useLive } from '../stores/live';
import { useUi } from '../stores/ui';
import UiButton from '../ui/Button.vue';

const live = useLive();
const ui = useUi();
const leads = useLeads(live);
const dismissed = useDismissedLeads(live);
const picked = reactive(new Set<string>());
const checking = ref(false);
const checked = shallowRef<DiscoverResult | undefined>();
const reviewing = shallowRef<{ files: Record<string, string> } | undefined>();
const showDismissed = ref(false);

const withURL = computed(() => leads.value.filter((l) => l.url));
const chosen = computed(() => withURL.value.filter((l) => picked.has(l.key)));
const kindName = (id: string) => catalogEntry(id)?.name ?? integrationName(id);

/** What the check said about a lead's URL: answered as itself, or why not. */
function verdict(l: Lead): { ok: boolean; text: string } | undefined {
  const r = checked.value;
  if (!r || !l.url) return undefined;
  const at = l.url.replace(/\/+$/, '');
  const hit = r.found.find((c) => c.integration === l.integration && String(c.config['url'] ?? '') === at);
  if (hit) return { ok: true, text: `answers as ${kindName(l.integration)}` };
  const miss = r.checked.find((c) => c.target === at);
  return miss ? { ok: false, text: miss.error ?? 'didn’t answer' } : undefined;
}

async function check(): Promise<void> {
  const list = (chosen.value.length ? chosen.value : withURL.value).slice(0, 16);
  if (!list.length) return;
  checking.value = true;
  try {
    checked.value = await discover({ urls: list.map((l) => ({ integration: l.integration, url: l.url! })) });
  } catch (err) {
    ui.toast(errorWords(err, 'The hub couldn’t check them.'), 'error');
  } finally {
    checking.value = false;
  }
}

function review(): void {
  if (!chosen.value.length) return;
  reviewing.value = { files: { leads_json: leadsJSON(chosen.value) } };
}

function byHand(l: Lead): void {
  setDraft(l.integration, { name: l.name, config: l.url ? { url: l.url } : {} });
  navigate(settingsPath('integrations', 'new') + `/${encodeURIComponent(l.integration)}`);
}

async function dismiss(l: Lead): Promise<void> {
  try {
    await dismissLeads([l.key]);
    picked.delete(l.key);
    ui.toast(`Dismissed ${l.name}. Cha Dash won’t offer it again.`, 'ok', 10_000, { label: 'Undo', run: () => void restoreLeads([l.key]) });
  } catch (err) {
    ui.toast(errorWords(err, 'Couldn’t dismiss it.'), 'error');
  }
}

function applied(): void {
  reviewing.value = undefined;
  picked.clear();
}

onMounted(() => void loadLeads());
</script>

<template>
  <ImportFlow v-if="reviewing" :preset="reviewing" initial-source="discovery" @applied="applied" @cancel="reviewing = undefined" />
  <template v-else>
    <section class="st-section" aria-labelledby="leads-h">
      <header>
        <h3 id="leads-h">Leads <span v-if="leads.length" class="n">{{ leads.length }}</span></h3>
        <span class="st-meta">From what’s connected; nothing was probed to find them</span>
      </header>
      <p v-if="!leadsState.loaded" class="imp-quiet" aria-busy="true">Looking at what’s connected…</p>
      <p v-else-if="!leads.length" class="imp-quiet">Nothing new. The services Cha Dash can see are all connected.</p>
      <ul v-else class="leads">
        <li v-for="l in leads" :key="l.key" class="imp-row">
          <label v-if="l.url" :for="`lead-${l.key}`">
            <input :id="`lead-${l.key}`" type="checkbox" :checked="picked.has(l.key)" @change="($event.target as HTMLInputElement).checked ? picked.add(l.key) : picked.delete(l.key)" />
            <span class="imp-name"><b>{{ l.name }}</b><template v-if="l.name !== kindName(l.integration)"> · {{ kindName(l.integration) }}</template> · <span class="n">{{ l.url }}</span></span>
          </label>
          <span v-else class="imp-name"><b>{{ l.name }}</b><template v-if="l.name !== kindName(l.integration)"> · {{ kindName(l.integration) }}</template> · no address seen yet</span>
          <span class="imp-sub">
            <span>Found as {{ l.ways.map((w) => w.text).join('; ') }}.</span>
            <span v-if="verdict(l)" :class="verdict(l)!.ok ? 'lead-ok' : 'lead-miss'"><Glyph :kind="verdict(l)!.ok ? 'check' : 'attn'" /> {{ verdict(l)!.text }}</span>
            <UiButton v-if="!l.url" size="sm" @click="byHand(l)">Add by hand…</UiButton>
            <UiButton variant="ghost" size="sm" @click="dismiss(l)">Dismiss</UiButton>
          </span>
        </li>
      </ul>
      <div v-if="withURL.length" class="st-acts">
        <UiButton variant="primary" :disabled="!chosen.length" @click="review">Review {{ chosen.length || '' }} selected</UiButton>
        <UiButton :disabled="checking" :aria-busy="checking" @click="check">
          {{ checking ? 'Checking…' : chosen.length ? `Check ${chosen.length}` : `Check all ${Math.min(withURL.length, 16)}` }}
        </UiButton>
        <span class="st-meta">Check sends one GET to each address on screen, and nothing else.</span>
      </div>
      <p v-if="dismissed.length" class="imp-quiet">
        <UiButton variant="ghost" size="sm" :aria-expanded="showDismissed" @click="showDismissed = !showDismissed">Dismissed · {{ dismissed.length }}</UiButton>
      </p>
      <ul v-if="showDismissed" class="rel">
        <li v-for="l in dismissed" :key="l.key">
          <span>{{ l.name }} · {{ kindName(l.integration) }}<template v-if="l.url"> · <span class="n">{{ l.url }}</span></template></span>
          <UiButton size="sm" @click="restoreLeads([l.key])">Offer again</UiButton>
        </li>
      </ul>
      <p v-if="checked" class="imp-quiet" role="status">Checked {{ plural(checked.checked.length, 'address', 'addresses') }}: {{ checked.found.length }} answered as expected.</p>
    </section>
    <section class="st-section" aria-labelledby="host-h">
      <header><h3 id="host-h">This host, and places you name</h3></header>
      <DiscoverStep />
    </section>
  </template>
</template>

<style>
.leads {
  list-style: none;
  margin: 0;
  padding: 0;
}
.lead-ok {
  color: var(--text);
}
.lead-miss {
  color: var(--text-2);
}
</style>
