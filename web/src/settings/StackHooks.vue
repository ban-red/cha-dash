<script setup lang="ts">
/**
 * Deploy webhooks (ADR-0021): a hook belongs to a remote and branch, and one
 * push to it makes every stack that follows them check at once. A webhook is
 * a poke, never a payload: Cha Dash only reads the push's ref, then fetches
 * for itself.
 *
 * The secret is shown once, when the hook is made or rotated, until Done.
 * Offering deploy on push for the syncs that follow it needs a step-up.
 */
import { computed, onMounted, ref, useId } from 'vue';
import Glyph from '../ui/marks/Glyph.vue';
import { ago, absolute, plural } from '../lib/format';
import { hubWords } from '../lib/actions';
import { focusFirst } from '../lib/focus';
import { StepUpCancelled } from '../stores/actions';
import { useUi } from '../stores/ui';
import { copyText } from './recipe';
import { DEPLOY_ON_PUSH_WORDS, MODE_WORDS, SENDERS, STATE_WORDS, createHook, deleteHook, hookURL, listHooks, rotateHook, senderLabel, type HookSender, type HookView } from './sync';
import UiButton from '../ui/Button.vue';
import UiField from '../ui/Field.vue';

const ui = useUi();
const uid = useId();
const now = ref(Date.now());

const hooks = ref<HookView[]>([]);
const status = ref<'loading' | 'ready' | 'error'>('loading');
const loadError = ref('');

async function load(): Promise<void> {
  try {
    hooks.value = await listHooks();
    status.value = 'ready';
    now.value = Date.now();
  } catch (err) {
    status.value = 'error';
    loadError.value = hubWords(err, 'Webhooks couldn’t be loaded.');
  }
}

// ── the secret, once ──

const fresh = ref<{ hook: HookView; secret: string; rotated: boolean }>();
const copied = ref('');

async function copy(what: 'secret' | 'url', text: string): Promise<void> {
  const ok = await copyText(text);
  copied.value = ok ? what : '';
  if (!ok) ui.toast('Couldn’t copy. Select it and copy it yourself.', 'error');
}

function done(): void {
  const id = fresh.value?.hook.id;
  fresh.value = undefined;
  copied.value = '';
  void focusFirst(id ? `${uid}-rot-${id}` : `${uid}-add`, 'skh-h');
}

const freshSender = computed(() => SENDERS.find((s) => s.id === fresh.value?.hook.sender));

// ── create ──

const adding = ref(false);
const url = ref('');
const branch = ref('main');
const sender = ref<HookSender>('github');
const deployOnPush = ref(false);
const errors = ref<Record<string, string>>({});
const formError = ref('');
const saving = ref(false);

function startAdd(): void {
  adding.value = true;
  url.value = '';
  branch.value = 'main';
  sender.value = 'github';
  deployOnPush.value = false;
  errors.value = {};
  formError.value = '';
  void focusFirst(`${uid}-url`);
}

function cancelAdd(): void {
  adding.value = false;
  void focusFirst(`${uid}-add`);
}

async function create(): Promise<void> {
  if (saving.value) return;
  const e: Record<string, string> = {};
  const u = url.value.trim();
  if (!u) e.url = 'Give the remote’s address, as the syncs that follow it have it.';
  else if (!/^(https?|ssh):\/\//i.test(u)) e.url = 'Start with https://, http:// or ssh://.';
  else if (/^[a-z]+:\/\/[^/]*@/i.test(u)) e.url = 'Leave the user and password out of the address.';
  if (!branch.value.trim()) e.branch = 'Name the branch.';
  errors.value = e;
  formError.value = '';
  if (Object.keys(e).length) {
    void focusFirst(`${uid}-${Object.keys(e)[0]}`);
    return;
  }
  saving.value = true;
  try {
    const req = { url: u, branch: branch.value.trim(), sender: sender.value, ...(deployOnPush.value ? { deploy_on_push: true } : {}) };
    const res = await ui.withStepUp(deployOnPush.value ? 'Make a webhook that deploys on push' : 'Make a webhook', () => createHook(req));
    adding.value = false;
    fresh.value = { hook: res.hook, secret: res.secret, rotated: false };
    copied.value = '';
    if (res.switched) ui.toast(`Switched ${plural(res.switched, 'sync')} to deploy on push.`);
    await load();
    void focusFirst(`${uid}-fresh-h`);
  } catch (err) {
    if (!(err instanceof StepUpCancelled)) formError.value = hubWords(err, 'The webhook wasn’t made.');
  } finally {
    saving.value = false;
  }
}

// ── rotate and delete ──

const confirm = ref<{ id: string; what: 'rotate' | 'delete' }>();
const busy = ref('');
const rowError = ref<Record<string, string>>({});

function ask(h: HookView, what: 'rotate' | 'delete'): void {
  confirm.value = { id: h.id, what };
  void focusFirst(`${uid}-cc-${h.id}`);
}

function cancel(h: HookView): void {
  const what = confirm.value?.what ?? 'rotate';
  confirm.value = undefined;
  void focusFirst(`${uid}-${what === 'rotate' ? 'rot' : 'del'}-${h.id}`);
}

async function rotate(h: HookView): Promise<void> {
  if (busy.value) return;
  busy.value = h.id;
  rowError.value = { ...rowError.value, [h.id]: '' };
  try {
    const res = await ui.withStepUp('Rotate a webhook’s secret', () => rotateHook(h.id));
    confirm.value = undefined;
    fresh.value = { hook: res.hook, secret: res.secret, rotated: true };
    copied.value = '';
    await load();
    void focusFirst(`${uid}-fresh-h`);
  } catch (err) {
    if (!(err instanceof StepUpCancelled)) rowError.value = { ...rowError.value, [h.id]: hubWords(err, 'The secret wasn’t rotated.') };
  } finally {
    busy.value = '';
  }
}

async function remove(h: HookView): Promise<void> {
  if (busy.value) return;
  busy.value = h.id;
  rowError.value = { ...rowError.value, [h.id]: '' };
  try {
    await ui.withStepUp('Delete a webhook', () => deleteHook(h.id));
    confirm.value = undefined;
    if (fresh.value?.hook.id === h.id) fresh.value = undefined;
    ui.toast('Webhook deleted. The syncs that follow that branch keep polling.');
    await load();
    void focusFirst('skh-h');
  } catch (err) {
    if (!(err instanceof StepUpCancelled)) rowError.value = { ...rowError.value, [h.id]: hubWords(err, 'The webhook wasn’t deleted.') };
  } finally {
    busy.value = '';
  }
}

const syncWords = (h: HookView) =>
  h.syncs.length ? h.syncs.map((s) => `${s.stack} on ${s.node} (${MODE_WORDS[s.mode]}${s.state === 'active' ? '' : `, ${STATE_WORDS[s.state].label}`})`).join(', ') : 'no stack follows it yet';

onMounted(load);
</script>

<template>
  <section id="stack-hooks" class="st-section" aria-labelledby="skh-h">
    <header>
      <h3 id="skh-h" tabindex="-1">Deploy webhooks</h3>
      <UiButton v-if="status === 'ready' && !adding" :id="`${uid}-add`" size="sm" @click="startAdd">New webhook</UiButton>
    </header>
    <p class="st-lede">
      A webhook tells Cha Dash a branch changed, so every stack that follows it checks at once instead of at its next poll. Cha Dash reads only which branch was
      pushed, then fetches for itself.
    </p>

    <!-- the secret, once -->
    <div v-if="fresh" class="st-note attn skh-fresh" role="region" :aria-labelledby="`${uid}-fresh-h`">
      <div class="skh-fresh-body">
        <h4 :id="`${uid}-fresh-h`" tabindex="-1">{{ fresh.rotated ? 'Copy the new secret now' : 'Copy the webhook’s secret now' }}</h4>
        <p>
          This is the only time Cha Dash shows it.<template v-if="fresh.rotated"> The old secret has stopped working: update it where the webhook is set up.</template>
        </p>
        <div class="skh-copy">
          <label :for="`${uid}-fu`" class="skh-lab">URL</label>
          <input :id="`${uid}-fu`" class="st-mono" type="text" readonly :value="hookURL(fresh.hook.path)" spellcheck="false" autocomplete="off" @focus="($event.target as HTMLInputElement).select()" />
          <UiButton size="sm" @click="copy('url', hookURL(fresh.hook.path))">{{ copied === 'url' ? 'Copied' : 'Copy URL' }}</UiButton>
          <label :for="`${uid}-fs`" class="skh-lab">Secret</label>
          <input :id="`${uid}-fs`" class="st-mono" type="text" readonly :value="fresh.secret" spellcheck="false" autocomplete="off" @focus="($event.target as HTMLInputElement).select()" />
          <UiButton size="sm" @click="copy('secret', fresh.secret)">{{ copied === 'secret' ? 'Copied' : 'Copy secret' }}</UiButton>
        </div>
        <p v-if="freshSender" class="skh-where">{{ freshSender.where }}</p>
        <p class="sr" aria-live="polite">{{ copied ? 'Copied to the clipboard.' : '' }}</p>
        <div class="st-acts"><UiButton variant="primary" size="sm" @click="done">I’ve saved it</UiButton></div>
      </div>
    </div>

    <div v-if="status === 'loading'" class="st-skel" aria-busy="true"><span class="skel"></span><span class="skel"></span></div>
    <div v-else-if="status === 'error'" class="st-note bad" role="alert">
      <Glyph kind="down" /><span>{{ loadError }}</span>
      <UiButton size="sm" @click="load">Try again</UiButton>
    </div>
    <p v-else-if="!hooks.length && !adding" class="st-meta skh-empty">No webhooks. Syncs poll every few minutes; a webhook makes them follow a push at once.</p>
    <ul v-else-if="hooks.length" class="st-list" aria-label="Deploy webhooks">
      <li v-for="h in hooks" :key="h.id">
        <div class="st-row skh-row">
          <Glyph :kind="h.failures ? 'attn' : h.last_used_at ? 'ok' : 'off'" />
          <span>
            <span class="t"><span class="st-mono skh-wrap">{{ h.url }}</span> · <span class="st-mono">{{ h.branch }}</span> <span class="chip">{{ senderLabel(h.sender) }}</span></span>
            <span class="m st-mono skh-wrap">{{ hookURL(h.path) }}</span>
            <span class="m">
              Triggers {{ syncWords(h) }} ·
              <template v-if="h.last_used_at">last used <span :title="absolute(h.last_used_at)">{{ ago(h.last_used_at, now) }}</span></template>
              <template v-else>never used</template>
              <template v-if="h.failures"> · {{ plural(h.failures, 'refused request') }}</template>
            </span>
            <span v-if="rowError[h.id]" class="err" role="alert">{{ rowError[h.id] }}</span>
          </span>
          <span class="end">
            <UiButton variant="ghost" size="sm" :aria-label="`Copy the webhook URL for ${h.branch} in ${h.url}`" @click="copy('url', hookURL(h.path))">Copy URL</UiButton>
            <template v-if="confirm?.id !== h.id">
              <UiButton :id="`${uid}-rot-${h.id}`" variant="ghost" size="sm" :aria-label="`Rotate the secret of the webhook for ${h.branch} in ${h.url}`" @click="ask(h, 'rotate')">Rotate</UiButton>
              <UiButton :id="`${uid}-del-${h.id}`" variant="ghost" size="sm" :aria-label="`Delete the webhook for ${h.branch} in ${h.url}`" @click="ask(h, 'delete')">Delete</UiButton>
            </template>
          </span>
        </div>
        <div v-if="confirm?.id === h.id" class="st-confirm skh-confirm" role="group" :aria-label="confirm.what === 'rotate' ? 'Rotate the secret?' : 'Delete the webhook?'">
          <span v-if="confirm.what === 'rotate'">Rotate the secret? The old one stops working at once, so pushes aren’t heard until you paste the new one into the webhook.</span>
          <span v-else>Delete this webhook? The syncs that follow {{ h.branch }} keep polling.</span>
          <UiButton
            variant="danger-solid"
            size="sm"
            :aria-disabled="busy === h.id ? 'true' : undefined"
            :aria-busy="busy === h.id || undefined"
            @click="confirm.what === 'rotate' ? rotate(h) : remove(h)"
          >
            {{ confirm.what === 'rotate' ? 'Rotate' : 'Delete' }}
          </UiButton>
          <UiButton :id="`${uid}-cc-${h.id}`" size="sm" @click="cancel(h)">Cancel</UiButton>
        </div>
      </li>
    </ul>

    <form v-if="adding" class="st-form skh-form" novalidate :aria-labelledby="`${uid}-fh`" @submit.prevent="create">
      <h4 :id="`${uid}-fh`" class="sk-sub">New webhook</h4>
      <UiField :id="`${uid}-url`" label="Remote" help="As the syncs that follow it have it; .git, a trailing / and the default port don’t matter." :error="errors.url" v-slot="{ control }">
        <input v-bind="control" v-model="url" type="url" autocomplete="off" autocapitalize="off" spellcheck="false" placeholder="https://git.example/ops/stacks.git" />
      </UiField>
      <div class="skh-fields">
        <UiField :id="`${uid}-branch`" label="Branch" :error="errors.branch" v-slot="{ control }">
          <input v-bind="control" v-model="branch" type="text" autocomplete="off" autocapitalize="off" spellcheck="false" />
        </UiField>
        <UiField label="Sent by" help="It checks only that sender’s header." v-slot="{ control }">
          <select v-bind="control" v-model="sender">
            <option v-for="s in SENDERS" :key="s.id" :value="s.id">{{ s.label }}</option>
          </select>
        </UiField>
      </div>
      <UiField check label="Switch the syncs that follow it to deploy on push" :help="`${DEPLOY_ON_PUSH_WORDS} Asks you to confirm it’s you.`" v-slot="{ control }">
        <input v-bind="control" v-model="deployOnPush" type="checkbox" />
      </UiField>
      <p v-if="formError" class="err" role="alert">{{ formError }}</p>
      <div class="st-acts">
        <UiButton type="submit" variant="primary" :aria-disabled="saving ? 'true' : undefined" :aria-busy="saving || undefined">{{ saving ? 'Making…' : 'Make webhook' }}</UiButton>
        <UiButton variant="ghost" @click="cancelAdd">Cancel</UiButton>
      </div>
    </form>
  </section>
</template>

<style>
.skh-row {
  grid-template-columns: auto minmax(0, 1fr) auto;
}
.skh-row .t {
  display: flex;
  flex-wrap: wrap;
  align-items: center;
  gap: 6px;
}
.skh-wrap {
  overflow-wrap: anywhere;
}
.skh-confirm {
  margin: 0 var(--space-2) var(--space-3);
}
.skh-fresh-body {
  display: grid;
  gap: var(--space-2);
  min-width: 0;
}
.skh-copy {
  display: grid;
  grid-template-columns: auto minmax(0, 1fr) auto;
  align-items: center;
  gap: var(--space-2);
}
.skh-copy input {
  min-width: 0;
}
.skh-lab {
  font-size: var(--type-small);
  color: var(--text-2);
}
.skh-where {
  font-size: var(--type-small);
}
.skh-form {
  display: grid;
  gap: var(--space-3);
  margin-top: var(--space-3);
}
.skh-fields {
  display: grid;
  grid-template-columns: repeat(auto-fit, minmax(200px, 1fr));
  gap: var(--space-3);
}
@media (max-width: 480px) {
  .skh-copy {
    grid-template-columns: minmax(0, 1fr);
  }
}
</style>
