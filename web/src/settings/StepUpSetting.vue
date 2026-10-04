<script setup lang="ts">
/**
 * Settings → Security → Confirming destructive actions (`security.step_up`):
 * how Cha Dash checks it's you before T3+ actions and before revealing
 * secrets. Passkey, or the password when there's no passkey (the default);
 * or Passkey only, which refuses those actions until a passkey exists.
 *
 * Changing it is itself a T3 change, so the hub may answer
 * `step_up_required`: the step-up dialog runs, then the save is tried again.
 * Closing the dialog puts the choice back. Owner only.
 */
import { onMounted, ref, useId } from 'vue';
import Glyph from '../components/marks/Glyph.vue';
import { api } from '../lib/api';
import { StepUpCancelled } from '../stores/actions';
import { hubWords } from '../lib/actions';
import { useUi } from '../stores/ui';

export type StepUpMode = 'passkey_or_password' | 'passkey_only';

const { hasPasskey } = defineProps<{ hasPasskey?: boolean | undefined }>();

const OPTIONS: { id: StepUpMode; label: string; description: string; default?: boolean }[] = [
  {
    id: 'passkey_or_password',
    label: 'Passkey, or password if no passkey',
    description: 'A passkey when the account has one. Without one, the password does, with a nudge to add a passkey.',
    default: true,
  },
  {
    id: 'passkey_only',
    label: 'Passkey only',
    description: 'Destructive actions and revealing secrets need a passkey. Accounts without one can’t do them until they add one.',
  },
];

const ui = useUi();
const name = useId();
const mode = ref<StepUpMode>('passkey_or_password');
const status = ref<'loading' | 'ready' | 'error'>('loading');
const loadError = ref('');
const saving = ref(false);
const saveError = ref('');

async function load(): Promise<void> {
  try {
    const v = await api<{ step_up?: string }>('/api/settings/security');
    mode.value = v?.step_up === 'passkey_only' ? 'passkey_only' : 'passkey_or_password';
    status.value = 'ready';
  } catch (err) {
    status.value = 'error';
    loadError.value = hubWords(err, 'This setting couldn’t be loaded.');
  }
}

async function choose(next: StepUpMode): Promise<void> {
  if (mode.value === next || saving.value) return;
  const before = mode.value;
  mode.value = next;
  saving.value = true;
  saveError.value = '';
  try {
    const res = await ui.withStepUp('Change how destructive actions are confirmed', () =>
      api<{ step_up?: string }>('/api/settings/security', { method: 'PUT', body: { step_up: next } }),
    );
    if (res?.step_up === 'passkey_only' || res?.step_up === 'passkey_or_password') mode.value = res.step_up;
    ui.toast(next === 'passkey_only' ? 'Destructive actions now need a passkey.' : 'Destructive actions take a passkey, or the password without one.');
  } catch (err) {
    mode.value = before;
    if (!(err instanceof StepUpCancelled)) saveError.value = hubWords(err, 'The setting wasn’t changed.');
  } finally {
    saving.value = false;
  }
}

onMounted(load);
</script>

<template>
  <section class="st-section" aria-labelledby="su-h">
    <header><h3 id="su-h">Confirming destructive actions</h3></header>
    <p class="st-lede">
      Before a destructive action (T3), a critical one (T4) or revealing a secret, Cha Dash checks it’s really you. A check lasts 5 minutes; critical actions
      always ask again. Changing this asks too.
    </p>
    <div v-if="status === 'loading'" class="st-skel" aria-busy="true"><span class="skel"></span><span class="skel"></span></div>
    <div v-else-if="status === 'error'" class="st-note bad" role="alert"><Glyph kind="down" /><span>{{ loadError }}</span></div>
    <fieldset v-else class="sl-group" :aria-busy="saving">
      <legend class="sr">Confirming destructive actions</legend>
      <label v-for="o in OPTIONS" :key="o.id" :class="['sl-card', { on: mode === o.id }]">
        <input type="radio" :name="name" :value="o.id" :checked="mode === o.id" :disabled="saving" :aria-describedby="`${name}-${o.id}`" @change="choose(o.id)" />
        <span class="sl-text">
          <span class="sl-label">{{ o.label }}<span v-if="o.default" class="chip">default</span></span>
          <span :id="`${name}-${o.id}`" class="sl-desc">{{ o.description }}</span>
        </span>
      </label>
    </fieldset>
    <div v-if="status === 'ready' && mode === 'passkey_only' && hasPasskey === false" class="st-note attn" role="note">
      <Glyph kind="attn" />
      <span>You don’t have a passkey yet, so you can’t run destructive actions until you add one below.</span>
    </div>
    <p v-if="saveError" class="err" role="alert">{{ saveError }}</p>
  </section>
</template>
