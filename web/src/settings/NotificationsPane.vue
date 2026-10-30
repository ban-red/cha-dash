<script setup lang="ts">
/**
 * Settings → Notifications. Every role gets "This device" (NotifyDevice).
 * Owners and admins also get what the hub decides to send and where:
 *
 * - the defaults in plain words, and a toggle per rule (critical actions are
 *   always on), with resolutions, the attention grace and which channels;
 * - channels: Web Push (built in), ntfy and webhooks, each with a test
 *   button that sends one now and says how it went;
 * - the delivery log, failures first to hand.
 *
 * Secrets (an ntfy token, a webhook's signing secret) are typed once and
 * never shown again; the hub says only whether one is set. Its own chunk,
 * loaded with the pane.
 *
 * Checkboxes save as they're ticked, and go back to what the hub has when a
 * save fails. The grace select only marks the choice; its Save sends it, so
 * arrowing through it never saves (WCAG 3.2.2). Controls that may hold focus
 * are aria-disabled rather than disabled while they work, and focus moves on
 * purpose when a confirm group or the form opens and closes.
 */
import SettingsHelp from './SettingsHelp.vue';
import { computed, onMounted, reactive, ref, useId } from 'vue';
import Glyph from '../components/marks/Glyph.vue';
import NotifyDevice from './NotifyDevice.vue';
import { ago } from '../lib/format';
import { hubWords } from '../lib/actions';
import { focusFirst } from '../lib/focus';
import { useUi } from '../stores/ui';
import {
  KIND_WORDS,
  TOPIC_RE,
  channelTarget,
  createChannel,
  deleteChannel,
  generateSecret,
  listChannels,
  listDeliveries,
  listRules,
  testChannel,
  updateChannel,
  updateRule,
  urlProblem,
  type ChannelInput,
  type NotifyChannel,
  type NotifyDelivery,
  type NotifyRule,
  type RuleSettings,
} from './notify';

const ui = useUi();
const uid = useId();
const admin = computed(() => ui.can.value.role === 'owner' || ui.can.value.role === 'admin');
const now = ref(Date.now());

// ── rules and channels ──
const rules = ref<NotifyRule[]>([]);
const channels = ref<NotifyChannel[]>([]);
const publicURL = ref(true);
const status = ref<'loading' | 'ready' | 'error'>('loading');
const loadError = ref('');
const ruleBusy = ref('');
const ruleError = ref<Record<string, string>>({});

async function load(): Promise<void> {
  try {
    const [rs, cs] = await Promise.all([listRules(), listChannels()]);
    rules.value = rs;
    channels.value = cs?.channels ?? [];
    publicURL.value = cs?.public_url !== false;
    status.value = 'ready';
    now.value = Date.now();
  } catch (err) {
    status.value = 'error';
    loadError.value = hubWords(err, 'Notification settings couldn’t be loaded.');
  }
}

/** Save a change to a rule. Resolves whether it saved. */
async function setRule(r: NotifyRule, patch: Partial<RuleSettings>): Promise<boolean> {
  ruleBusy.value = r.id;
  ruleError.value = { ...ruleError.value, [r.id]: '' };
  try {
    const updated = await updateRule(r.id, patch);
    rules.value = rules.value.map((x) => (x.id === r.id ? updated : x));
    return true;
  } catch (err) {
    ruleError.value = { ...ruleError.value, [r.id]: hubWords(err, 'That didn’t save.') };
    return false;
  } finally {
    ruleBusy.value = '';
  }
}

const ruleNow = (id: string) => rules.value.find((x) => x.id === id);

/** While a rule saves, its checkboxes stay focusable but don't toggle. */
function holdWhileBusy(r: NotifyRule, e: Event): void {
  if (ruleBusy.value === r.id) e.preventDefault();
}

/**
 * Tick or untick a rule's checkbox. Vue won't touch `checked` when the
 * rule's value hasn't changed, so a failed save sets the box back itself.
 */
async function toggleRule(r: NotifyRule, key: 'enabled' | 'resolved', e: Event): Promise<void> {
  const box = e.target as HTMLInputElement;
  const ok = await setRule(r, key === 'enabled' ? { enabled: box.checked } : { resolved: box.checked });
  if (!ok) box.checked = !!ruleNow(r.id)?.[key];
}

async function toggleChannel(r: NotifyRule, id: string, e: Event): Promise<void> {
  const box = e.target as HTMLInputElement;
  const all = channels.value.map((c) => c.id);
  const current = r.channels ?? all;
  const next = box.checked ? [...new Set([...current, id])] : current.filter((c) => c !== id);
  const ok = await setRule(r, { channels: all.every((c) => next.includes(c)) ? null : next });
  if (!ok) {
    const saved = ruleNow(r.id)?.channels;
    box.checked = saved === null || saved === undefined || saved.includes(id);
  }
}

/** Grace picked in a rule's select and not saved yet. */
const graceDraft = reactive<Record<string, number>>({});
const graceDirty = (r: NotifyRule) => graceDraft[r.id] !== undefined && graceDraft[r.id] !== r.grace_minutes;

async function saveGrace(r: NotifyRule): Promise<void> {
  const g = graceDraft[r.id];
  if (g === undefined || g === r.grace_minutes || ruleBusy.value === r.id) return;
  await setRule(r, { grace_minutes: g });
  // Saved or not, the select shows what the hub has.
  delete graceDraft[r.id];
}

const GRACE = [1, 2, 5, 10, 15, 30, 60];

// ── channel tests and changes ──
const testing = ref('');
const tested = ref<Record<string, { ok: boolean; text: string }>>({});
const confirmDelete = ref('');
const rowBusy = ref('');

async function test(c: NotifyChannel): Promise<void> {
  if (testing.value === c.id) return;
  testing.value = c.id;
  try {
    const res = await testChannel(c.id);
    const where = c.kind === 'webpush' ? ` to ${res.devices === 1 ? 'your device' : `your ${res.devices ?? 0} devices`}` : '';
    tested.value = {
      ...tested.value,
      [c.id]: res.ok ? { ok: true, text: `Sent${where}. Check that it arrived.` } : { ok: false, text: res.error ?? 'It didn’t go through.' },
    };
  } catch (err) {
    tested.value = { ...tested.value, [c.id]: { ok: false, text: hubWords(err, 'The test couldn’t be sent.') } };
  } finally {
    testing.value = '';
    void loadLog();
    void load();
  }
}

async function setEnabled(c: NotifyChannel, enabled: boolean): Promise<void> {
  if (rowBusy.value === c.id) return;
  rowBusy.value = c.id;
  try {
    const updated = await updateChannel(c.id, { enabled });
    channels.value = channels.value.map((x) => (x.id === c.id ? updated : x));
  } catch (err) {
    tested.value = { ...tested.value, [c.id]: { ok: false, text: hubWords(err, 'That didn’t save.') } };
  } finally {
    rowBusy.value = '';
  }
}

/** Open the confirm group and put focus on its Cancel, the safe choice. */
function askDelete(c: NotifyChannel): void {
  confirmDelete.value = c.id;
  void focusFirst(`${uid}-dc-${c.id}`);
}

function cancelDelete(c: NotifyChannel): void {
  confirmDelete.value = '';
  void focusFirst(`${uid}-del-${c.id}`, 'nc-h');
}

async function remove(c: NotifyChannel): Promise<void> {
  if (rowBusy.value === c.id) return;
  rowBusy.value = c.id;
  try {
    await deleteChannel(c.id);
    confirmDelete.value = '';
    ui.toast(`Deleted ${c.name}.`);
    await load();
    // The row is gone: back to the list's heading.
    void focusFirst('nc-h');
  } catch (err) {
    tested.value = { ...tested.value, [c.id]: { ok: false, text: hubWords(err, `${c.name} wasn’t deleted.`) } };
  } finally {
    rowBusy.value = '';
  }
}

function channelGlyph(c: NotifyChannel): 'off' | 'ok' | 'down' | 'attn' {
  if (!c.enabled) return 'off';
  if (!c.last) return 'ok';
  return c.last.state === 'failed' ? 'down' : c.last.state === 'dropped' ? 'attn' : 'ok';
}

function lastWords(c: NotifyChannel): string {
  if (!c.enabled) return 'off';
  if (!c.last) return 'nothing sent yet';
  const when = ago(c.last.at, now.value);
  return { sent: `last sent ${when}`, failed: `last try failed ${when}`, dropped: `last one dropped ${when}`, pending: `retrying since ${when}` }[c.last.state] ?? when;
}

// ── the add / edit form ──
const editing = ref<'' | 'new' | string>('');
const form = reactive({ kind: 'ntfy' as 'ntfy' | 'webhook', name: '', server: 'https://ntfy.sh', topic: '', token: '', url: '', secret: '', clearToken: false });
const shown = ref(false);
const errors = ref<Record<string, string>>({});
const formError = ref('');
const saving = ref(false);
const editingChannel = computed(() => channels.value.find((c) => c.id === editing.value));

/** Where focus goes when the form closes: the Edit that opened it, or Add a channel. */
function formOpener(): string[] {
  return editing.value && editing.value !== 'new' ? [`${uid}-edit-${editing.value}`, `${uid}-addc`, 'nc-h'] : [`${uid}-addc`, 'nc-h'];
}

function closeForm(): void {
  const back = formOpener();
  editing.value = '';
  void focusFirst(...back);
}

function openForm(c?: NotifyChannel): void {
  errors.value = {};
  formError.value = '';
  shown.value = false;
  if (c) {
    editing.value = c.id;
    Object.assign(form, { kind: c.kind, name: c.name, server: c.config.server ?? '', topic: c.config.topic ?? '', url: c.config.url ?? '', token: '', secret: '', clearToken: false });
  } else {
    editing.value = 'new';
    Object.assign(form, { kind: 'ntfy', name: '', server: 'https://ntfy.sh', topic: '', url: '', token: '', secret: '', clearToken: false });
  }
  // The form's first field: the kind for a new channel, else the name.
  void focusFirst(c ? `${uid}-name` : `${uid}-kind-ntfy`, `${uid}-name`);
}

function newSecret(): void {
  form.secret = generateSecret();
  shown.value = true;
}

function validate(): boolean {
  const e: Record<string, string> = {};
  if (!form.name.trim() || form.name.trim().length > 64) e.name = 'A name of 1 to 64 characters.';
  if (form.kind === 'ntfy') {
    const p = urlProblem(form.server);
    if (p) e.server = p;
    if (!TOPIC_RE.test(form.topic.trim())) e.topic = 'Letters, digits, - and _; at most 64.';
  } else {
    const p = urlProblem(form.url);
    if (p) e.url = p;
    const has = editingChannel.value?.secrets['secret']?.set;
    if (form.secret && form.secret.length < 16) e.secret = 'At least 16 characters. Generate one if you like.';
    else if (!form.secret && !has) e.secret = 'A webhook needs a signing secret, so the receiver can tell the message is from this hub.';
  }
  errors.value = e;
  return Object.keys(e).length === 0;
}

async function save(): Promise<void> {
  if (saving.value || !validate()) return;
  saving.value = true;
  formError.value = '';
  const body: ChannelInput = { name: form.name.trim() };
  const secrets: Record<string, string | null> = {};
  if (form.kind === 'ntfy') {
    body.config = { server: form.server.trim(), topic: form.topic.trim() };
    if (form.token) secrets['token'] = form.token;
    else if (form.clearToken) secrets['token'] = null;
  } else {
    body.config = { url: form.url.trim() };
    if (form.secret) secrets['secret'] = form.secret;
  }
  if (Object.keys(secrets).length) body.secrets = secrets;
  // Typed secrets leave the page with this request and nowhere else.
  form.token = '';
  form.secret = '';
  try {
    if (editing.value === 'new') {
      body.kind = form.kind;
      const c = await createChannel(body);
      ui.toast(`Added ${c.name}. Send a test to check it.`);
    } else {
      await updateChannel(editing.value, body);
      ui.toast(`Saved ${body.name}.`);
    }
    const back = formOpener();
    editing.value = '';
    await load();
    void focusFirst(...back);
  } catch (err) {
    const fields = (err as { body?: { fields?: { path: string; message: string }[] } }).body?.fields;
    if (fields?.length) {
      const e: Record<string, string> = {};
      for (const f of fields) e[f.path.split('/').pop() ?? ''] = f.message;
      errors.value = e;
    }
    formError.value = hubWords(err, 'The channel wasn’t saved.');
  } finally {
    saving.value = false;
  }
}

// ── the delivery log ──
const log = ref<NotifyDelivery[]>([]);
const logFilter = ref<'' | 'failed'>('');
const logStatus = ref<'loading' | 'ready' | 'error'>('loading');
const logError = ref('');

async function loadLog(): Promise<void> {
  try {
    log.value = await listDeliveries(logFilter.value);
    logStatus.value = 'ready';
    now.value = Date.now();
  } catch (err) {
    logStatus.value = 'error';
    logError.value = hubWords(err, 'The delivery log couldn’t be loaded.');
  }
}

function filterLog(f: '' | 'failed'): void {
  logFilter.value = f;
  void loadLog();
}

const STATE: Record<string, { glyph: 'check' | 'down' | 'attn' | 'prog'; word: string }> = {
  sent: { glyph: 'check', word: 'sent' },
  failed: { glyph: 'down', word: 'failed' },
  dropped: { glyph: 'attn', word: 'dropped' },
  pending: { glyph: 'prog', word: 'retrying' },
};
const stateOf = (d: NotifyDelivery) => STATE[d.state] ?? { glyph: 'attn' as const, word: d.state };

onMounted(() => {
  if (!admin.value) return;
  void load();
  void loadLog();
});
</script>

<template>
  <NotifyDevice />

  <template v-if="admin">
    <section class="st-section" aria-labelledby="nr-h">
      <header><h3 id="nr-h">What notifies</h3></header>
      <SettingsHelp summary="Only real problems notify, once each. A flap that clears itself never pages anyone.">
        Cha Dash tells people about symptoms, not causes, and only once per problem. Something down is sent at once; something that needs attention waits a few
        minutes, so a flap that clears by itself never pages anyone. Hints, security advice at the Relaxed level, and problems already being fixed stay quiet.
        When several arrive together, as on a backup night, they come as one message.
      </SettingsHelp>
      <div v-if="status === 'loading'" class="st-skel" aria-busy="true"><span class="skel"></span><span class="skel"></span><span class="skel"></span></div>
      <div v-else-if="status === 'error'" class="st-note bad" role="alert">
        <Glyph kind="down" /><span>{{ loadError }}</span>
        <button type="button" class="btn sm" @click="load">Try again</button>
      </div>
      <ul v-else class="st-list nr-list" aria-label="Rules">
        <li v-for="r in rules" :key="r.id" class="nr-rule">
          <label class="check-row">
            <input
              type="checkbox"
              :checked="r.enabled"
              :disabled="r.locked"
              :aria-disabled="ruleBusy === r.id ? 'true' : undefined"
              :aria-describedby="`${uid}-${r.id}-d`"
              @click="holdWhileBusy(r, $event)"
              @change="toggleRule(r, 'enabled', $event)"
            />
            <span class="t">{{ r.label }}</span>
            <span v-if="r.locked" class="set-chip">always on</span>
          </label>
          <p :id="`${uid}-${r.id}-d`" class="m">{{ r.description }}</p>
          <div v-if="r.enabled" class="nr-opts">
            <label v-if="r.has_resolved" class="check-row">
              <input
                type="checkbox"
                :checked="r.resolved"
                :aria-disabled="ruleBusy === r.id ? 'true' : undefined"
                @click="holdWhileBusy(r, $event)"
                @change="toggleRule(r, 'resolved', $event)"
              />
              <span>Also when it’s resolved</span>
            </label>
            <div v-if="r.grace_minutes !== undefined && r.grace_minutes > 0" class="field nr-grace">
              <label :for="`${uid}-${r.id}-g`">Wait before notifying</label>
              <div class="nr-grace-row">
                <select :id="`${uid}-${r.id}-g`" :value="graceDraft[r.id] ?? r.grace_minutes" @change="graceDraft[r.id] = Number(($event.target as HTMLSelectElement).value)">
                  <option v-for="g in GRACE" :key="g" :value="g">{{ g === 1 ? '1 minute' : `${g} minutes` }}</option>
                </select>
                <button
                  type="button"
                  class="btn sm"
                  :aria-label="`Save the wait for ${r.label}`"
                  :aria-disabled="!graceDirty(r) || ruleBusy === r.id ? 'true' : undefined"
                  :aria-busy="(ruleBusy === r.id && graceDirty(r)) || undefined"
                  @click="saveGrace(r)"
                >
                  Save
                </button>
              </div>
            </div>
            <fieldset v-if="channels.length > 1" class="nr-chans">
              <legend>Send to</legend>
              <label v-for="c in channels" :key="c.id" class="check-row">
                <input
                  type="checkbox"
                  :checked="r.channels === null || r.channels.includes(c.id)"
                  :aria-disabled="ruleBusy === r.id ? 'true' : undefined"
                  @click="holdWhileBusy(r, $event)"
                  @change="toggleChannel(r, c.id, $event)"
                />
                <span>{{ c.name }}</span>
              </label>
            </fieldset>
          </div>
          <p v-if="ruleError[r.id]" class="err" role="alert">{{ ruleError[r.id] }}</p>
        </li>
      </ul>
    </section>

    <section class="st-section" aria-labelledby="nc-h">
      <header>
        <h3 id="nc-h" tabindex="-1">Channels</h3>
        <button v-if="status === 'ready' && editing !== 'new'" :id="`${uid}-addc`" type="button" class="btn sm" @click="openForm()">Add a channel</button>
      </header>
      <div v-if="status === 'ready' && !publicURL" class="st-note hatched">
        <Glyph kind="link" /><span>Messages carry no link back to Cha Dash until <code>CHADASH_PUBLIC_URL</code> is set. Web Push opens the right page either way.</span>
      </div>
      <ul v-if="status === 'ready'" class="st-list" aria-label="Channels">
        <li v-for="c in channels" :key="c.id">
          <div class="st-row">
            <Glyph :kind="channelGlyph(c)" />
            <span>
              <span class="t">{{ c.name }}</span>
              <span class="m">{{ KIND_WORDS[c.kind] }} · {{ channelTarget(c) }} · {{ lastWords(c) }}</span>
              <span v-if="c.last?.error && c.enabled" class="m">{{ c.last.error }}</span>
              <span v-if="tested[c.id]" :class="tested[c.id]!.ok ? 'm' : 'err'" role="status">{{ tested[c.id]!.text }}</span>
            </span>
            <span class="end">
              <button
                type="button"
                class="btn sm"
                :disabled="!c.enabled"
                :aria-disabled="testing === c.id ? 'true' : undefined"
                :aria-busy="testing === c.id || undefined" :aria-label="`Send a test to ${c.name}`" @click="test(c)">
                {{ testing === c.id ? 'Sending…' : 'Test' }}
              </button>
              <button
                type="button"
                class="btn sm ghost"
                :aria-disabled="rowBusy === c.id ? 'true' : undefined"
                :aria-label="`${c.enabled ? 'Turn off' : 'Turn on'} ${c.name}`" @click="setEnabled(c, !c.enabled)">
                {{ c.enabled ? 'Turn off' : 'Turn on' }}
              </button>
              <template v-if="!c.builtin">
                <button :id="`${uid}-edit-${c.id}`" type="button" class="btn sm ghost" :aria-label="`Edit ${c.name}`" @click="openForm(c)">Edit</button>
                <button v-if="confirmDelete !== c.id" :id="`${uid}-del-${c.id}`" type="button" class="btn sm ghost" :aria-label="`Delete ${c.name}`" @click="askDelete(c)">Delete</button>
              </template>
            </span>
          </div>
          <div v-if="confirmDelete === c.id" class="st-confirm nc-confirm" role="group" :aria-label="`Delete ${c.name}?`">
            <span>Delete <b>{{ c.name }}</b> and its secret? Its deliveries stay in the log.</span>
            <button type="button" class="btn danger solid sm" :aria-disabled="rowBusy === c.id ? 'true' : undefined" :aria-busy="rowBusy === c.id || undefined" @click="remove(c)">Delete</button>
            <button :id="`${uid}-dc-${c.id}`" type="button" class="btn sm" @click="cancelDelete(c)">Cancel</button>
          </div>
        </li>
      </ul>

      <form v-if="editing" class="st-form nc-form" novalidate :aria-labelledby="`${uid}-fh`" @submit.prevent="save">
        <h4 :id="`${uid}-fh`" class="nc-sub">{{ editing === 'new' ? 'Add a channel' : `Edit ${editingChannel?.name ?? ''}` }}</h4>
        <div v-if="editing === 'new'" class="choice-row" role="radiogroup" aria-label="Kind">
          <label><input :id="`${uid}-kind-ntfy`" v-model="form.kind" type="radio" value="ntfy" name="nc-kind" />ntfy</label>
          <label><input v-model="form.kind" type="radio" value="webhook" name="nc-kind" />Webhook</label>
        </div>
        <div class="field">
          <label :for="`${uid}-name`">Name</label>
          <input :id="`${uid}-name`" v-model="form.name" type="text" maxlength="64" autocomplete="off" :aria-invalid="errors.name ? true : undefined" :aria-describedby="errors.name ? `${uid}-name-e` : undefined" />
          <span v-if="errors.name" :id="`${uid}-name-e`" class="help bad">{{ errors.name }}</span>
        </div>
        <template v-if="form.kind === 'ntfy'">
          <div class="nc-fields">
            <div class="field">
              <label :for="`${uid}-server`">Server</label>
              <input :id="`${uid}-server`" v-model="form.server" type="url" inputmode="url" autocomplete="off" spellcheck="false" :aria-invalid="errors.server ? true : undefined" :aria-describedby="errors.server ? `${uid}-server-e` : undefined" />
              <span v-if="errors.server" :id="`${uid}-server-e`" class="help bad">{{ errors.server }}</span>
            </div>
            <div class="field">
              <label :for="`${uid}-topic`">Topic</label>
              <input :id="`${uid}-topic`" v-model="form.topic" type="text" autocomplete="off" spellcheck="false" maxlength="64" :aria-invalid="errors.topic ? true : undefined" :aria-describedby="`${uid}-topic-h`" />
              <span :id="`${uid}-topic-h`" :class="['help', { bad: errors.topic }]">{{ errors.topic || 'On a public server, anyone who knows the topic can read it: pick something hard to guess, or use a token.' }}</span>
            </div>
          </div>
          <div class="field">
            <div class="label-row">
              <label :for="`${uid}-token`">Access token</label>
              <span v-if="editingChannel?.secrets['token']?.set" class="set-chip">set</span>
              <span class="opt-word">optional</span>
            </div>
            <input :id="`${uid}-token`" v-model="form.token" type="password" autocomplete="new-password" spellcheck="false" :aria-describedby="`${uid}-token-h`" />
            <span :id="`${uid}-token-h`" class="help">An ntfy access token (tk_…), sent as a bearer token. Write-only{{ editingChannel?.secrets['token']?.set ? ': leave it empty to keep the one stored' : '' }}.</span>
            <label v-if="editingChannel?.secrets['token']?.set" class="check-row"><input v-model="form.clearToken" type="checkbox" :disabled="!!form.token" />Remove the stored token</label>
          </div>
        </template>
        <template v-else>
          <div class="field">
            <label :for="`${uid}-url`">URL</label>
            <input :id="`${uid}-url`" v-model="form.url" type="url" inputmode="url" autocomplete="off" spellcheck="false" :aria-invalid="errors.url ? true : undefined" :aria-describedby="`${uid}-url-h`" />
            <span :id="`${uid}-url-h`" :class="['help', { bad: errors.url }]">{{ errors.url || 'Cha Dash POSTs JSON here. Redirects aren’t followed.' }}</span>
          </div>
          <div class="field">
            <div class="label-row">
              <label :for="`${uid}-secret`">Signing secret</label>
              <span v-if="editingChannel?.secrets['secret']?.set" class="set-chip">set</span>
            </div>
            <div class="secret-row">
              <input
                :id="`${uid}-secret`"
                v-model="form.secret"
                :type="shown ? 'text' : 'password'"
                autocomplete="new-password"
                spellcheck="false"
                :aria-invalid="errors.secret ? true : undefined"
                :aria-describedby="`${uid}-secret-h`"
              />
              <button type="button" class="btn sm" @click="newSecret">Generate</button>
            </div>
            <span :id="`${uid}-secret-h`" :class="['help', { bad: errors.secret }]">
              {{ errors.secret || 'Each request carries X-Chadash-Signature: an HMAC-SHA256 of the timestamp and the body with this secret. Copy it to the receiver now: it can’t be read back.' }}
            </span>
          </div>
        </template>
        <p v-if="formError" class="err" role="alert">{{ formError }}</p>
        <div class="st-acts">
          <button type="submit" class="btn" :aria-disabled="saving ? 'true' : undefined" :aria-busy="saving || undefined">{{ saving ? 'Saving…' : editing === 'new' ? 'Add channel' : 'Save' }}</button>
          <button type="button" class="btn ghost" @click="closeForm">Cancel</button>
        </div>
      </form>
    </section>

    <section class="st-section" aria-labelledby="nl-h">
      <header>
        <h3 id="nl-h">Delivery log</h3>
        <button type="button" class="btn sm ghost" @click="loadLog">Refresh</button>
      </header>
      <div class="seg nl-seg" role="group" aria-label="Show">
        <button type="button" :aria-pressed="logFilter === ''" @click="filterLog('')">All</button>
        <button type="button" :aria-pressed="logFilter === 'failed'" @click="filterLog('failed')">Failed</button>
      </div>
      <div v-if="logStatus === 'loading'" class="st-skel" aria-busy="true"><span class="skel"></span><span class="skel"></span></div>
      <div v-else-if="logStatus === 'error'" class="st-note bad" role="alert">
        <Glyph kind="down" /><span>{{ logError }}</span>
      </div>
      <p v-else-if="!log.length" class="st-meta">{{ logFilter ? 'No failed deliveries.' : 'Nothing sent yet.' }}</p>
      <ul v-else class="st-list nl-list" aria-label="Deliveries">
        <li v-for="d in log" :key="d.id">
          <div class="st-row">
            <Glyph :kind="stateOf(d).glyph" :label="stateOf(d).word" />
            <span>
              <span class="t">{{ d.title }}</span>
              <span class="m">
                {{ d.channel_name }}<template v-if="d.device"> · {{ d.device }}</template> · {{ stateOf(d).word }}<template v-if="d.attempts > 1"> after {{ d.attempts }} tries</template>
              </span>
              <span v-if="d.error" class="m nl-err">{{ d.error }}</span>
            </span>
            <span class="end">{{ ago(d.created_at, now) }}</span>
          </div>
        </li>
      </ul>
    </section>
  </template>
</template>

<style>
.nr-rule {
  display: flex;
  flex-direction: column;
  gap: var(--space-2);
  padding: var(--space-3) var(--space-2);
}
.nr-rule .t {
  font: var(--weight-medium) var(--type-body) / 1.3 var(--font-ui);
}
.nr-rule > .m {
  max-width: 62ch;
  margin: 0 0 0 26px;
  font-size: var(--type-small);
  line-height: var(--leading-relaxed);
  color: var(--text-3);
}
.nr-opts {
  display: flex;
  flex-wrap: wrap;
  align-items: flex-end;
  gap: var(--space-3) var(--space-5);
  margin-left: 26px;
}
.nr-opts .check-row {
  font-size: var(--type-small);
}
.nr-grace {
  width: 16rem;
  max-width: 100%;
}
.nr-grace-row {
  display: flex;
  gap: var(--space-2);
  align-items: center;
}
.nr-grace-row select {
  flex: 1 1 auto;
  min-width: 0;
}
.nr-chans {
  display: flex;
  flex-wrap: wrap;
  gap: var(--space-2) var(--space-4);
  margin: 0;
  padding: 0;
  border: 0;
}
.nr-chans legend {
  width: 100%;
  margin-bottom: var(--space-1);
  font: var(--weight-medium) var(--type-caption) / 1.2 var(--font-num);
  color: var(--text-3);
}
.nc-confirm {
  margin: 0 var(--space-2) var(--space-3);
}
.nc-form {
  margin-top: var(--space-4);
}
.nc-sub {
  font: var(--weight-medium) var(--type-callout) / 1.3 var(--font-ui);
}
.nc-fields {
  display: grid;
  grid-template-columns: repeat(auto-fit, minmax(200px, 1fr));
  gap: var(--space-3);
}
.nl-seg {
  align-self: flex-start;
}
.nl-err {
  white-space: normal;
}
</style>
