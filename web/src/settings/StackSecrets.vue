<script setup lang="ts">
/**
 * Stack secrets: the values compose files reference as `${secret:<name>}`,
 * resolved in memory at deploy time. Write-only: the hub only ever names them
 * and says whether each is set. A typed value lives in one password field,
 * goes out once on Save, and is cleared straight after, whatever the answer.
 */
import { nextTick, onMounted, ref, useId } from 'vue';
import Glyph from '../components/marks/Glyph.vue';
import { ago } from '../lib/format';
import { hubWords } from '../lib/actions';
import { StepUpCancelled } from '../stores/actions';
import { useUi } from '../stores/ui';
import { SECRET_NAME_RE, deleteSecret, listSecrets, putSecret, type StackSecret } from './stacks';

const ui = useUi();
const uid = useId();
const secrets = ref<StackSecret[]>([]);
const status = ref<'loading' | 'ready' | 'error'>('loading');
const loadError = ref('');
const now = ref(Date.now());

const name = ref('');
const value = ref('');
const nameError = ref('');
const formError = ref('');
const saving = ref(false);
const deleting = ref('');
const confirmDelete = ref('');
const rowError = ref<Record<string, string>>({});

async function load(): Promise<void> {
  try {
    secrets.value = (await listSecrets()).sort((a, b) => a.name.localeCompare(b.name));
    status.value = 'ready';
    now.value = Date.now();
  } catch (err) {
    status.value = 'error';
    loadError.value = hubWords(err, 'Stack secrets couldn’t be loaded.');
  }
}

async function replace(s: StackSecret): Promise<void> {
  name.value = s.name;
  value.value = '';
  nameError.value = '';
  formError.value = '';
  await nextTick();
  document.getElementById(`${uid}-val`)?.focus();
}

async function save(): Promise<void> {
  const n = name.value.trim();
  nameError.value = SECRET_NAME_RE.test(n) ? '' : 'Letters, digits and . _ -, starting with a letter or digit; at most 64.';
  formError.value = value.value ? '' : 'Type the value to store.';
  if (nameError.value || formError.value) return;
  saving.value = true;
  const typed = value.value;
  // The field empties now: the value exists only in this request.
  value.value = '';
  try {
    await ui.withStepUp(`Set the stack secret ${n}`, () => putSecret(n, typed));
    ui.toast(`Saved ${n}. Stacks that reference it can use it on their next deploy.`);
    name.value = '';
    await load();
  } catch (err) {
    if (!(err instanceof StepUpCancelled)) formError.value = hubWords(err, `${n} wasn’t saved.`);
  } finally {
    saving.value = false;
  }
}

async function remove(s: StackSecret): Promise<void> {
  deleting.value = s.name;
  rowError.value = { ...rowError.value, [s.name]: '' };
  try {
    await ui.withStepUp(`Delete the stack secret ${s.name}`, () => deleteSecret(s.name));
    confirmDelete.value = '';
    ui.toast(`Deleted ${s.name}.`);
    await load();
  } catch (err) {
    if (!(err instanceof StepUpCancelled)) rowError.value = { ...rowError.value, [s.name]: hubWords(err, `${s.name} wasn’t deleted.`) };
  } finally {
    deleting.value = '';
  }
}

onMounted(load);
</script>

<template>
  <section id="stack-secrets" class="st-section" aria-labelledby="sks-h">
    <header><h3 id="sks-h">Stack secrets</h3></header>
    <p class="st-lede">
      Reference one in a compose file as <code>${secret:name}</code>. Cha Dash fills it in at deploy time and never writes it to disk or git. Values can be
      replaced, never read back.
    </p>

    <div v-if="status === 'loading'" class="st-skel" aria-busy="true"><span class="skel"></span><span class="skel"></span></div>
    <div v-else-if="status === 'error'" class="st-note bad" role="alert">
      <Glyph kind="down" /><span>{{ loadError }}</span>
      <button type="button" class="btn sm" @click="load">Try again</button>
    </div>
    <p v-else-if="!secrets.length" class="st-meta">No stack secrets yet.</p>
    <ul v-else class="st-list sks-list" aria-label="Stack secrets">
      <li v-for="s in secrets" :key="s.name">
        <div class="st-row sks-row">
          <Glyph :kind="s.set ? 'check' : 'attn'" />
          <span>
            <span class="t st-mono">{{ s.name }}</span>
            <span class="m">{{ s.set ? 'set' : 'not set' }}<template v-if="s.updated_at"> · changed {{ ago(s.updated_at, now) }}</template></span>
            <span v-if="rowError[s.name]" class="err" role="alert">{{ rowError[s.name] }}</span>
          </span>
          <span class="end">
            <button type="button" class="btn sm ghost" :aria-label="`Replace ${s.name}`" @click="replace(s)">{{ s.set ? 'Replace' : 'Set' }}</button>
            <button v-if="confirmDelete !== s.name" type="button" class="btn sm ghost" :aria-label="`Delete ${s.name}`" @click="confirmDelete = s.name">Delete</button>
          </span>
        </div>
        <div v-if="confirmDelete === s.name" class="st-confirm sks-confirm" role="group" :aria-label="`Delete ${s.name}?`">
          <span>Delete <b class="st-mono">{{ s.name }}</b>? Stacks that reference it won’t deploy until it’s set again.</span>
          <button type="button" class="btn danger solid sm" :disabled="deleting === s.name" @click="remove(s)">Delete</button>
          <button type="button" class="btn sm" @click="confirmDelete = ''">Cancel</button>
        </div>
      </li>
    </ul>

    <form class="st-form sks-form" novalidate aria-labelledby="sks-add-h" @submit.prevent="save">
      <h4 id="sks-add-h" class="sk-sub">Set a secret</h4>
      <div class="sks-fields">
        <div class="field">
          <label :for="`${uid}-name`">Name</label>
          <input
            :id="`${uid}-name`"
            v-model="name"
            type="text"
            autocomplete="off"
            autocapitalize="off"
            spellcheck="false"
            maxlength="64"
            placeholder="db_password"
            :aria-invalid="nameError ? true : undefined"
            :aria-describedby="nameError ? `${uid}-name-e` : undefined"
          />
          <span v-if="nameError" :id="`${uid}-name-e`" class="help bad">{{ nameError }}</span>
        </div>
        <div class="field">
          <label :for="`${uid}-val`">Value</label>
          <input :id="`${uid}-val`" v-model="value" type="password" autocomplete="new-password" spellcheck="false" :aria-describedby="`${uid}-val-h`" />
          <span :id="`${uid}-val-h`" class="help">Write-only. Saving replaces any value already stored.</span>
        </div>
      </div>
      <p v-if="formError" class="err" role="alert">{{ formError }}</p>
      <div class="st-acts">
        <button type="submit" class="btn" :disabled="saving" :aria-busy="saving || undefined">{{ saving ? 'Saving…' : 'Save secret' }}</button>
      </div>
    </form>
  </section>
</template>

<style>
.sks-fields {
  display: grid;
  grid-template-columns: repeat(auto-fit, minmax(200px, 1fr));
  gap: var(--space-3);
}
.sks-confirm {
  margin: 0 var(--space-2) var(--space-3);
}
.sks-form {
  margin-top: var(--space-3);
}
</style>
