<script setup lang="ts">
/**
 * Settings → Security → Agents (ADR-0017): whether AI agents may connect,
 * through the MCP endpoint, and the limits on each agent token's proposals.
 * Off by default. While it's off no agent token can be made, POST /mcp
 * answers 404, and nothing an agent left waiting can be approved; turning
 * it off withdraws those proposals. It revokes no token.
 *
 *   GET /api/settings/agents  → {enabled, max_pending, max_per_hour}
 *   PUT /api/settings/agents  {enabled, max_pending?, max_per_hour?} → the same
 *
 * As with the step-up rule, picking a radio only marks the choice; Save
 * sends it (WCAG 3.2.2). Owner only.
 */
import { computed, onMounted, reactive, ref, useId } from 'vue';
import Glyph from '../ui/marks/Glyph.vue';
import { api } from '../lib/api';
import { hubWords } from '../lib/actions';
import type { AgentSettings } from '../model/types';
import { useUi } from '../stores/ui';
import UiButton from '../ui/Button.vue';
import UiField from '../ui/Field.vue';

const ui = useUi();
const name = useId();
/** What the hub has. */
const saved = ref<AgentSettings>({ enabled: false, max_pending: 3, max_per_hour: 20 });
/** What's picked, saved or not. */
const draft = reactive<AgentSettings>({ enabled: false, max_pending: 3, max_per_hour: 20 });
const status = ref<'loading' | 'ready' | 'error'>('loading');
const loadError = ref('');
const saving = ref(false);
const saveError = ref('');

const changed = computed(
  () => draft.enabled !== saved.value.enabled || draft.max_pending !== saved.value.max_pending || draft.max_per_hour !== saved.value.max_per_hour,
);
const limitsOK = computed(
  () => Number.isInteger(draft.max_pending) && draft.max_pending >= 1 && draft.max_pending <= 20 && Number.isInteger(draft.max_per_hour) && draft.max_per_hour >= 1 && draft.max_per_hour <= 200,
);

function take(v: AgentSettings | undefined): void {
  saved.value = { enabled: !!v?.enabled, max_pending: v?.max_pending || 3, max_per_hour: v?.max_per_hour || 20 };
  Object.assign(draft, saved.value);
}

async function load(): Promise<void> {
  try {
    take(await api<AgentSettings>('/api/settings/agents'));
    status.value = 'ready';
  } catch (err) {
    status.value = 'error';
    loadError.value = hubWords(err, 'This setting couldn’t be loaded.');
  }
}

async function save(): Promise<void> {
  if (!changed.value || saving.value) return;
  if (!limitsOK.value) {
    saveError.value = 'Limits are 1 to 20 proposals waiting and 1 to 200 an hour.';
    return;
  }
  saving.value = true;
  saveError.value = '';
  const wasOn = saved.value.enabled;
  try {
    take(await api<AgentSettings>('/api/settings/agents', { method: 'PUT', body: { ...draft } }));
    ui.toast(saved.value.enabled ? (wasOn ? 'Agent limits saved.' : 'Agents are on. Make an agent token in API tokens.') : 'Agents are off. What they left waiting was withdrawn.');
  } catch (err) {
    Object.assign(draft, saved.value);
    saveError.value = hubWords(err, 'The setting wasn’t changed.');
  } finally {
    saving.value = false;
  }
}

onMounted(load);
</script>

<template>
  <section class="st-section" aria-labelledby="ag-h">
    <header><h3 id="ag-h">Agents</h3></header>
    <p class="st-lede">
      Let AI agents read your home and propose actions, through Cha Dash’s MCP endpoint. An agent never acts: every proposal waits for you or another admin,
      at every tier, and T2 and up take a passkey or password check. Cha Dash calls no model itself.
    </p>
    <div v-if="status === 'loading'" class="st-skel" aria-busy="true"><span class="skel"></span><span class="skel"></span></div>
    <div v-else-if="status === 'error'" class="st-note bad" role="alert"><Glyph kind="down" /><span>{{ loadError }}</span></div>
    <template v-else>
      <fieldset class="sl-group" :aria-busy="saving">
        <legend class="sr">Agents</legend>
        <label :class="['sl-card', { on: !draft.enabled }]">
          <input type="radio" :name="name" :checked="!draft.enabled" :disabled="saving" :aria-describedby="`${name}-off`" @change="draft.enabled = false" />
          <span class="sl-text">
            <span class="sl-label">Off<span class="chip">default</span></span>
            <span :id="`${name}-off`" class="sl-desc">No agent token can be made, and the MCP endpoint doesn’t answer. Turning it off withdraws what agents left waiting.</span>
          </span>
        </label>
        <label :class="['sl-card', { on: draft.enabled }]">
          <input type="radio" :name="name" :checked="draft.enabled" :disabled="saving" :aria-describedby="`${name}-on`" @change="draft.enabled = true" />
          <span class="sl-text">
            <span class="sl-label">On</span>
            <span :id="`${name}-on`" class="sl-desc">Agent tokens can be made in API tokens, each with what it may read and propose. Whatever an agent reads may go to its model’s provider.</span>
          </span>
        </label>
      </fieldset>
      <div v-if="draft.enabled" class="ag-limits">
        <UiField label="Waiting at once, per agent" help="Proposals beyond it are refused until you decide some." v-slot="{ control }">
          <input v-bind="control" v-model.number="draft.max_pending" type="number" min="1" max="20" inputmode="numeric" />
        </UiField>
        <UiField label="Proposals an hour, per agent" help="A burst beyond it shows in the Inbox." v-slot="{ control }">
          <input v-bind="control" v-model.number="draft.max_per_hour" type="number" min="1" max="200" inputmode="numeric" />
        </UiField>
      </div>
      <div class="st-acts">
        <UiButton size="sm" aria-label="Save the Agents setting" :aria-disabled="!changed || saving ? 'true' : undefined" :aria-busy="saving || undefined" @click="save">Save</UiButton>
      </div>
      <p v-if="saveError" class="err" role="alert">{{ saveError }}</p>
    </template>
  </section>
</template>

<style>
.ag-limits {
  display: flex;
  flex-wrap: wrap;
  gap: var(--space-3);
  margin-top: var(--space-3);
}
.ag-limits input {
  max-width: 8rem;
}
</style>
