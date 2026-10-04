<script setup lang="ts">
/**
 * Settings → Security: the security level (SecurityLevel.vue: how much
 * security findings interrupt you), then the owner's passkeys. Add one (on this device), rename
 * them so you can tell them apart, delete the ones you no longer have.
 *
 * Passkeys need a secure context (HTTPS, or localhost). On a plain LAN
 * address the page explains that instead of offering a button that can't
 * work. The WebAuthn code loads only when you add one.
 */
import { nextTick, onMounted, ref } from 'vue';
import Glyph from '../components/marks/Glyph.vue';
import SecurityLevel from './SecurityLevel.vue';
import { ago, absolute } from '../lib/format';
import { INSECURE_MESSAGE, passkeySupport, UNSUPPORTED_MESSAGE } from '../lib/webauthn';
import type { Passkey } from '../lib/passkeys';
import { errorWords } from '../stores/integrations';
import { useUi } from '../stores/ui';

const ui = useUi();
const support = passkeySupport();
const keys = ref<Passkey[]>([]);
const status = ref<'loading' | 'ready' | 'error'>('loading');
const loadError = ref('');
const adding = ref(false);
const addError = ref('');
const renaming = ref('');
const draft = ref('');
const confirming = ref('');
const busy = ref('');
const rowError = ref<Record<string, string>>({});
const now = Date.now();
const origin = typeof location !== 'undefined' ? location.origin : '';
const host = typeof location !== 'undefined' ? location.hostname : '';

const lib = () => import('../lib/passkeys');

async function load(): Promise<void> {
  try {
    keys.value = await (await lib()).listPasskeys();
    status.value = 'ready';
  } catch (err) {
    status.value = 'error';
    loadError.value = errorWords(err, 'Passkeys couldn’t be loaded.');
  }
}

async function add(): Promise<void> {
  adding.value = true;
  addError.value = '';
  try {
    const created = await (await lib()).registerPasskey();
    ui.toast(`Passkey added${created?.name ? `: ${created.name}` : ''}. Next time, choose “Sign in with a passkey”.`);
    await load();
  } catch (err) {
    addError.value = (err as Error).message || 'The passkey wasn’t added.';
  } finally {
    adding.value = false;
  }
}

async function startRename(k: Passkey): Promise<void> {
  renaming.value = k.id;
  draft.value = k.name;
  confirming.value = '';
  await nextTick();
  document.getElementById(`pk-name-${k.id}`)?.focus();
}

async function saveRename(k: Passkey): Promise<void> {
  const name = draft.value.trim();
  if (name === k.name) {
    renaming.value = '';
    return;
  }
  const m = await lib();
  const problem = m.passkeyNameProblem(name);
  if (problem) {
    rowError.value = { ...rowError.value, [k.id]: problem };
    return;
  }
  busy.value = k.id;
  rowError.value = { ...rowError.value, [k.id]: '' };
  try {
    const updated = await m.renamePasskey(k.id, name);
    keys.value = keys.value.map((x) => (x.id === k.id ? { ...x, ...(updated ?? {}), name: updated?.name ?? name } : x));
    renaming.value = '';
  } catch (err) {
    rowError.value = { ...rowError.value, [k.id]: (err as Error).message || 'Couldn’t rename it.' };
  } finally {
    busy.value = '';
  }
}

async function remove(k: Passkey): Promise<void> {
  busy.value = k.id;
  try {
    await (await lib()).deletePasskey(k.id);
    keys.value = keys.value.filter((x) => x.id !== k.id);
    confirming.value = '';
    ui.toast(`Deleted the passkey “${k.name}”.`);
  } catch (err) {
    rowError.value = { ...rowError.value, [k.id]: (err as Error).message || 'Couldn’t delete it.' };
    confirming.value = '';
  } finally {
    busy.value = '';
  }
}

onMounted(load);
</script>

<template>
  <SecurityLevel />
  <section class="st-section" aria-labelledby="pk-h">
    <header>
      <h3 id="pk-h">Passkeys</h3>
      <button v-if="support === 'ok'" type="button" class="btn sm" :disabled="adding" :aria-busy="adding" @click="add">{{ adding ? 'Waiting for you…' : 'Add a passkey' }}</button>
    </header>
    <p class="st-lede">Sign in with Touch ID, Face ID, Windows Hello or a security key instead of your password. Each device or key is its own passkey; your password keeps working.</p>

    <div v-if="support === 'insecure'" class="st-note hatched" role="note">
      <Glyph kind="lock" />
      <span>{{ INSECURE_MESSAGE }} This page is on <span class="st-mono">{{ origin }}</span>.</span>
    </div>
    <div v-else-if="support === 'unsupported'" class="st-note" role="note"><Glyph kind="lock" /><span>{{ UNSUPPORTED_MESSAGE }}</span></div>
    <p v-if="addError" class="err" role="alert">{{ addError }}</p>

    <div v-if="status === 'loading'" class="st-skel" aria-busy="true"><span class="skel"></span><span class="skel"></span></div>
    <div v-else-if="status === 'error'" class="st-note bad" role="alert"><Glyph kind="down" /><span>{{ loadError }}</span></div>
    <div v-else-if="!keys.length" class="st-empty">
      <div class="t">No passkeys yet</div>
      <div class="m">{{ support === 'ok' ? 'Add one for this device. It takes a few seconds.' : 'Open Cha Dash over HTTPS to add one.' }}</div>
    </div>
    <ul v-else class="st-list">
      <li v-for="k in keys" :key="k.id">
        <div class="st-row pk-row">
          <span class="mono-badge" aria-hidden="true"><Glyph kind="lock" /></span>
          <span>
            <form v-if="renaming === k.id" class="pk-rename" @submit.prevent="saveRename(k)">
              <label :for="`pk-name-${k.id}`" class="sr">Passkey name</label>
              <input :id="`pk-name-${k.id}`" v-model="draft" maxlength="64" autocomplete="off" @keydown.esc.stop.prevent="renaming = ''" />
              <button type="submit" class="btn sm primary" :disabled="busy === k.id">Save</button>
              <button type="button" class="btn sm ghost" @click="renaming = ''">Cancel</button>
            </form>
            <template v-else>
              <span class="t">
                {{ k.name }}
                <span v-if="k.backed_up" class="chip">synced</span>
                <span v-if="k.rp_id && k.rp_id !== host" class="chip" :title="`Works only at ${k.rp_id}`">for {{ k.rp_id }}</span>
              </span>
              <span v-if="k.clone_warning" class="m pk-warn"><Glyph kind="attn" label="warning" /> Its counter went backwards once: two copies of it may be in use. If you don’t recognise that, delete it.</span>
              <span class="m">
                Added <span :title="absolute(k.created_at)">{{ ago(k.created_at, now) }}</span> ·
                <template v-if="k.last_used_at">last used <span :title="absolute(k.last_used_at)">{{ ago(k.last_used_at, now) }}</span></template>
                <template v-else>never used</template>
              </span>
            </template>
            <span v-if="rowError[k.id]" class="err" role="alert">{{ rowError[k.id] }}</span>
          </span>
          <span v-if="renaming !== k.id && confirming !== k.id" class="end">
            <button type="button" class="btn sm ghost" @click="startRename(k)">Rename</button>
            <button type="button" class="btn sm ghost" @click="confirming = k.id">Delete…</button>
          </span>
        </div>
        <div v-if="confirming === k.id" class="st-confirm pk-confirm" role="alertdialog" :aria-labelledby="`pk-del-${k.id}`">
          <span :id="`pk-del-${k.id}`">Delete <b>{{ k.name }}</b>? That device or key can’t sign in to this hub any more. Remove it from the device’s password manager too.</span>
          <button type="button" class="btn danger solid" :disabled="busy === k.id" @click="remove(k)">Delete<span class="tier">T1</span></button>
          <button type="button" class="btn" @click="confirming = ''">Cancel</button>
        </div>
      </li>
    </ul>
  </section>
</template>

<style>
.pk-row {
  grid-template-columns: auto minmax(0, 1fr) auto;
}
.pk-row .mono-badge .g {
  width: 12px;
  height: 12px;
}
.pk-rename {
  display: flex;
  flex-wrap: wrap;
  gap: var(--space-2);
}
.pk-rename input {
  flex: 1 1 12rem;
  min-height: 32px;
  padding: 6px 10px;
  border: var(--hairline) solid var(--line-2);
  border-radius: var(--radius-md);
  background: var(--surface);
  color: var(--text);
}
.pk-row .t .chip {
  margin-left: 6px;
  vertical-align: 1px;
}
.pk-warn {
  color: var(--text-2);
}
.pk-confirm {
  margin: 0 var(--space-2) var(--space-3);
}
</style>
