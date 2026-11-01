<script setup lang="ts">
/**
 * A Proxmox instance's "PVE notifications" (internal/pvehook): the secret
 * PVE's webhook target sends in a header, so a backup failure or a fenced
 * node reaches Cha Dash at once instead of at its next poll.
 *
 *   GET    /api/integrations/instances/{id}/pve-hook   {set, path, header, created_at?, last_used_at?}
 *   POST   /api/integrations/instances/{id}/pve-hook   a new secret, replacing any; it's in this answer only
 *   DELETE /api/integrations/instances/{id}/pve-hook   the webhook refuses everything until a new one
 *
 * The secret is shown once, until "I've saved it". Rotate and Remove ask
 * first, inline. The steps in PVE are in integrations/proxmox/README.md.
 */
import { computed, onMounted, ref, useId } from 'vue';
import { api } from '../lib/api';
import { hubWords } from '../lib/actions';
import { ago } from '../lib/format';
import { focusFirst } from '../lib/focus';
import { useUi } from '../stores/ui';
import Glyph from '../ui/marks/Glyph.vue';
import UiButton from '../ui/Button.vue';
import { copyText } from './recipe';

interface HookView {
  instance: string;
  set: boolean;
  path: string;
  header: string;
  created_at?: number;
  last_used_at?: number;
  secret?: string;
}

const { instance } = defineProps<{ instance: string }>();
const ui = useUi();
const uid = useId();
const view = ref<HookView>();
const status = ref<'loading' | 'ready' | 'error'>('loading');
const error = ref('');
const busy = ref(false);
const confirm = ref<'' | 'rotate' | 'remove'>('');
const fresh = ref('');
const copied = ref('');
const now = ref(Date.now());

const base = `/api/integrations/instances/${encodeURIComponent(instance)}/pve-hook`;
const url = computed(() => (view.value && typeof location !== 'undefined' ? `${location.origin}${view.value.path}` : (view.value?.path ?? '')));

async function load(): Promise<void> {
  try {
    view.value = await api<HookView>(base);
    status.value = 'ready';
    now.value = Date.now();
  } catch (err) {
    error.value = hubWords(err, 'The webhook’s state couldn’t be read.');
    status.value = 'error';
  }
}
onMounted(() => void load());

async function make(): Promise<void> {
  if (busy.value) return;
  busy.value = true;
  error.value = '';
  try {
    const res = await api<HookView>(base, { method: 'POST' });
    const { secret, ...rest } = res;
    fresh.value = secret ?? '';
    view.value = rest;
    confirm.value = '';
    void focusFirst(`${uid}-fresh-h`);
  } catch (err) {
    error.value = hubWords(err, 'No secret was made.');
  } finally {
    busy.value = false;
  }
}

async function remove(): Promise<void> {
  if (busy.value) return;
  busy.value = true;
  error.value = '';
  try {
    await api(base, { method: 'DELETE' });
    confirm.value = '';
    await load();
    ui.toast('Removed. Cha Dash refuses PVE’s notifications for this instance until you make a new secret.');
    void focusFirst(`${uid}-make`);
  } catch (err) {
    error.value = hubWords(err, 'The secret wasn’t removed.');
  } finally {
    busy.value = false;
  }
}

async function copy(what: 'url' | 'secret', text: string): Promise<void> {
  const ok = await copyText(text);
  copied.value = ok ? what : '';
  if (!ok) ui.toast('Couldn’t copy. Select it and copy it yourself.', 'error');
}

function done(): void {
  fresh.value = '';
  copied.value = '';
  void focusFirst(`${uid}-rotate`);
}
</script>

<template>
  <section class="st-section pvh" :aria-labelledby="`${uid}-h`">
    <header><h3 :id="`${uid}-h`">PVE notifications</h3></header>
    <p class="st-lede">
      Proxmox VE 8.3 and later can tell Cha Dash at once when a backup fails or a node is fenced. Add a webhook target in PVE (Datacenter → Notifications) that
      sends this secret in the <code>{{ view?.header ?? 'X-Chadash-Webhook-Secret' }}</code> header. Cha Dash never reads what PVE sends: a notification only
      makes it check this Proxmox now.
    </p>

    <div v-if="fresh" class="st-note attn pvh-fresh" role="region" :aria-labelledby="`${uid}-fresh-h`">
      <div class="pvh-body">
        <h4 :id="`${uid}-fresh-h`" tabindex="-1">Copy the secret now</h4>
        <p>This is the only time Cha Dash shows it. A secret made before it has stopped working.</p>
        <div class="pvh-copy">
          <label :for="`${uid}-u`">URL</label>
          <input :id="`${uid}-u`" class="st-mono" type="text" readonly :value="url" spellcheck="false" autocomplete="off" @focus="($event.target as HTMLInputElement).select()" />
          <UiButton size="sm" @click="copy('url', url)">{{ copied === 'url' ? 'Copied' : 'Copy URL' }}</UiButton>
          <label :for="`${uid}-s`">Secret</label>
          <input :id="`${uid}-s`" class="st-mono" type="text" readonly :value="fresh" spellcheck="false" autocomplete="off" @focus="($event.target as HTMLInputElement).select()" />
          <UiButton size="sm" @click="copy('secret', fresh)">{{ copied === 'secret' ? 'Copied' : 'Copy secret' }}</UiButton>
        </div>
        <p class="sr" aria-live="polite">{{ copied ? 'Copied to the clipboard.' : '' }}</p>
        <div class="st-acts"><UiButton variant="primary" size="sm" @click="done">I’ve saved it</UiButton></div>
      </div>
    </div>

    <div v-if="status === 'loading'" class="st-skel" aria-busy="true"><span class="skel"></span></div>
    <div v-else-if="status === 'error'" class="st-note bad" role="alert">
      <Glyph kind="down" /><span>{{ error }}</span>
      <UiButton size="sm" @click="load">Try again</UiButton>
    </div>
    <template v-else-if="view">
      <p class="pvh-state" role="status">
        <Glyph :kind="view.set ? 'check' : 'off'" />
        <span v-if="!view.set">No secret yet, so Cha Dash refuses PVE’s notifications for this instance.</span>
        <span v-else>
          A secret is set<template v-if="view.created_at">, made {{ ago(view.created_at, now) }}</template>.
          {{ view.last_used_at ? `PVE last notified ${ago(view.last_used_at, now)}.` : 'PVE hasn’t notified yet.' }}
        </span>
      </p>
      <div v-if="!confirm" class="st-acts">
        <UiButton v-if="!view.set" :id="`${uid}-make`" size="sm" variant="primary" :aria-busy="busy || undefined" @click="make">Make a secret</UiButton>
        <template v-else>
          <UiButton :id="`${uid}-rotate`" size="sm" @click="confirm = 'rotate'">Rotate</UiButton>
          <UiButton size="sm" variant="ghost" @click="confirm = 'remove'">Remove</UiButton>
        </template>
      </div>
      <div v-else class="st-confirm" role="group" :aria-label="confirm === 'rotate' ? 'Rotate the secret?' : 'Remove the secret?'">
        <span v-if="confirm === 'rotate'">Rotate the secret? The old one stops working at once, so PVE isn’t heard until you paste the new one into its target.</span>
        <span v-else>Remove the secret? Cha Dash refuses PVE’s notifications for this instance until you make a new one.</span>
        <UiButton :variant="confirm === 'rotate' ? 'primary' : 'danger-solid'" size="sm" :aria-busy="busy || undefined" @click="confirm === 'rotate' ? make() : remove()">
          {{ confirm === 'rotate' ? 'Rotate' : 'Remove' }}
        </UiButton>
        <UiButton size="sm" :disabled="busy" @click="confirm = ''">Cancel</UiButton>
      </div>
      <p v-if="error" class="err" role="alert">{{ error }}</p>
    </template>
  </section>
</template>

<style scoped>
.pvh-state {
  display: flex;
  gap: var(--space-2);
  align-items: baseline;
  margin: 0;
  font-size: var(--type-small);
  color: var(--text-2);
}
.pvh-body {
  display: flex;
  flex-direction: column;
  gap: var(--space-2);
  min-width: 0;
}
.pvh-body h4,
.pvh-body p {
  margin: 0;
}
.pvh-copy {
  display: grid;
  grid-template-columns: auto minmax(0, 1fr) auto;
  gap: var(--space-2);
  align-items: center;
}
.pvh-copy label {
  font-size: var(--type-small);
  color: var(--text-2);
}
.pvh-copy input {
  min-width: 0;
}
</style>
