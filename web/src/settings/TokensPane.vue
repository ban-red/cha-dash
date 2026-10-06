<script setup lang="ts">
/**
 * Settings → API tokens. Every role manages its own tokens; owners and
 * admins see everyone's and can revoke any.
 *
 *   GET    /api/tokens          → {tokens: [{id, user_id, username, name, scopes, created_at, expires_at, last_used_at, last_used_ip?, revoked_at}]}
 *   GET    /api/tokens/scopes   → the scope picker: {reads, families, actions, all, default_days, max_days, approval}
 *   POST   /api/tokens          {name, scopes, expires_in_days} → {token, secret}: the secret, once
 *   DELETE /api/tokens/{id}     → {token}, revoked at once
 *
 * A token acts as its user, never above the user's role, narrowed by its
 * scopes. T0 and T1 actions run when the token approves its own plan; T2
 * waits in the Inbox for an admin or the owner; T3 and T4 never. The secret
 * is shown once, right after it's made, with Copy; the hub keeps only a hash.
 *
 * Its own chunk, loaded with the pane.
 */
import { computed, nextTick, onMounted, reactive, ref, useId } from 'vue';
import Glyph from '../components/marks/Glyph.vue';
import { ago, absolute } from '../lib/format';
import { api } from '../lib/api';
import { hubWords } from '../lib/actions';
import { useSession } from '../stores/session';
import { useUi } from '../stores/ui';
import { copyText } from './recipe';

export interface HubToken {
  id: string;
  user_id: string;
  username: string;
  name: string;
  scopes: string[];
  created_at: string;
  expires_at: string | null;
  last_used_at: string | null;
  last_used_ip?: string;
  revoked_at: string | null;
}

interface Offer {
  scope: string;
  label: string;
  description?: string;
  tier?: string;
  available: boolean;
  unavailable?: string;
}

interface ScopeCatalog {
  reads: Offer[];
  families: Offer[];
  actions: Offer[];
  all: Offer;
  default_days: number;
  max_days: number;
  approval?: string;
}

const ui = useUi();
const session = useSession();
const uid = useId();
const now = Date.now();

const tokens = ref<HubToken[]>([]);
const status = ref<'loading' | 'ready' | 'error'>('loading');
const loadError = ref('');
const catalog = ref<ScopeCatalog | undefined>();
const seesAll = computed(() => ui.can.value.approve);
const meId = computed(() => session.state.user?.id);

const sorted = computed(() => [...tokens.value].sort((a, b) => Number(!!stateOf(a).off) - Number(!!stateOf(b).off) || b.created_at.localeCompare(a.created_at)));

function stateOf(t: HubToken): { word: string; off?: boolean } {
  if (t.revoked_at) return { word: 'revoked', off: true };
  if (t.expires_at && Date.parse(t.expires_at) <= now) return { word: 'expired', off: true };
  return { word: '' };
}

async function load(): Promise<void> {
  try {
    const res = await api<{ tokens?: HubToken[] }>('/api/tokens');
    tokens.value = res?.tokens ?? [];
    status.value = 'ready';
  } catch (err) {
    status.value = 'error';
    loadError.value = hubWords(err, 'API tokens couldn’t be loaded.');
  }
}

async function loadCatalog(): Promise<void> {
  if (catalog.value) return;
  try {
    catalog.value = await api<ScopeCatalog>('/api/tokens/scopes');
  } catch (err) {
    createError.value = hubWords(err, 'The scopes couldn’t be loaded.');
  }
}

// ── create ──
const creating = ref(false);
const saving = ref(false);
const createError = ref('');
const draft = reactive({
  name: '',
  days: 90,
  reads: [] as string[],
  run: 'none' as 'none' | 'all' | 'pick',
  actions: [] as string[],
  extra: '',
});
const nameError = ref('');
const DAY_CHOICES = [30, 90, 180, 365];
/** The secret just made: shown once, until Done. */
const fresh = ref<{ token: HubToken; secret: string } | undefined>();
const copied = ref(false);

async function startCreate(): Promise<void> {
  creating.value = true;
  Object.assign(draft, { name: '', days: 90, reads: [], run: 'none', actions: [], extra: '' });
  nameError.value = '';
  createError.value = '';
  await loadCatalog();
  if (catalog.value) draft.days = catalog.value.default_days;
  await nextTick();
  document.getElementById(`${uid}-name`)?.focus();
}

/** The scopes the form adds up to, in the order the hub will keep them. */
const chosen = computed<string[]>(() => {
  const out = [...draft.reads];
  if (draft.run === 'all') out.push('*');
  if (draft.run === 'pick') out.push(...draft.actions);
  for (const s of draft.extra.split(/[\s,]+/)) if (s) out.push(s);
  return [...new Set(out)];
});

async function create(): Promise<void> {
  nameError.value = draft.name.trim() ? '' : 'Name it after what will use it, such as “backup script”.';
  createError.value = '';
  if (nameError.value) return;
  if (!chosen.value.length) {
    createError.value = 'Pick at least one thing the token may read or run.';
    return;
  }
  saving.value = true;
  try {
    const res = await api<{ token: HubToken; secret: string }>('/api/tokens', {
      method: 'POST',
      body: { name: draft.name.trim(), scopes: chosen.value, expires_in_days: draft.days },
    });
    fresh.value = res;
    copied.value = false;
    tokens.value = [res.token, ...tokens.value];
    creating.value = false;
    await nextTick();
    document.getElementById(`${uid}-secret`)?.focus();
  } catch (err) {
    createError.value = hubWords(err, 'The token wasn’t made.');
  } finally {
    saving.value = false;
  }
}

async function copy(): Promise<void> {
  if (!fresh.value) return;
  copied.value = await copyText(fresh.value.secret);
  if (!copied.value) ui.toast('Couldn’t copy. Select the token and copy it yourself.', 'error');
}

function done(): void {
  fresh.value = undefined;
  copied.value = false;
}

// ── revoke ──
const revoking = ref('');
const busy = ref('');
const rowError = reactive<Record<string, string>>({});

async function revoke(t: HubToken): Promise<void> {
  busy.value = t.id;
  rowError[t.id] = '';
  try {
    const res = await api<{ token?: HubToken }>(`/api/tokens/${encodeURIComponent(t.id)}`, { method: 'DELETE' });
    tokens.value = tokens.value.map((x) => (x.id === t.id ? { ...x, ...(res?.token ?? { revoked_at: new Date().toISOString() }) } : x));
    revoking.value = '';
    ui.toast(`Revoked “${t.name}”. Anything using it stops working now.`);
  } catch (err) {
    rowError[t.id] = hubWords(err, 'The token wasn’t revoked.');
  } finally {
    busy.value = '';
  }
}

const scopeWords = (s: string) => (s === '*' ? 'everything your role allows' : s);
const expiresWords = (t: HubToken) => (t.expires_at ? (Date.parse(t.expires_at) <= now ? `expired ${absolute(t.expires_at)}` : `expires ${absolute(t.expires_at)}`) : 'never expires');

onMounted(load);
</script>

<template>
  <section class="st-section" aria-labelledby="tk-h">
    <header>
      <h3 id="tk-h">{{ seesAll ? 'Everyone’s tokens' : 'Your tokens' }}</h3>
      <button v-if="!creating && !fresh" type="button" class="btn sm" @click="startCreate">New token</button>
    </header>
    <p class="st-lede">
      For scripts and automations. A token acts as you, never with more than your role allows, and only for what you pick here. Send it as
      <code>Authorization: Bearer cdt_…</code> on <code>/api/</code> requests. Actions up to T1 run when the token asks; T2 actions wait in the Inbox until an
      admin or the owner approves them; T3 and T4 never run from a token.
    </p>

    <div v-if="fresh" class="tk-fresh st-note attn" role="region" :aria-labelledby="`${uid}-fresh-h`">
      <div class="tk-fresh-body">
        <h4 :id="`${uid}-fresh-h`">Copy “{{ fresh.token.name }}” now</h4>
        <p>This is the only time Cha Dash shows it. It keeps just a fingerprint, so a lost token can’t be shown again: revoke it and make a new one.</p>
        <div class="secret-row">
          <label :for="`${uid}-secret`" class="sr">The new token</label>
          <input :id="`${uid}-secret`" class="st-mono" type="text" readonly :value="fresh.secret" spellcheck="false" autocomplete="off" @focus="($event.target as HTMLInputElement).select()" />
          <button type="button" class="btn" @click="copy">{{ copied ? 'Copied' : 'Copy' }}</button>
        </div>
        <p class="sr" aria-live="polite">{{ copied ? 'Copied to the clipboard.' : '' }}</p>
        <div class="st-acts"><button type="button" class="btn primary sm" @click="done">I’ve saved it</button></div>
      </div>
    </div>

    <form v-if="creating" class="st-form tk-add" novalidate aria-labelledby="tk-add-h" @submit.prevent="create">
      <h4 id="tk-add-h" class="sr">New token</h4>
      <div class="field">
        <label :for="`${uid}-name`">Name</label>
        <input :id="`${uid}-name`" v-model="draft.name" type="text" maxlength="64" autocomplete="off" :aria-invalid="nameError ? true : undefined" :aria-describedby="`${uid}-name-h`" />
        <span :id="`${uid}-name-h`" :class="['help', { bad: nameError }]">{{ nameError || 'What will use it, so you can tell tokens apart later.' }}</span>
      </div>
      <div class="field">
        <label :for="`${uid}-days`">Expires</label>
        <select :id="`${uid}-days`" v-model.number="draft.days">
          <option v-for="d in DAY_CHOICES.filter((x) => x <= (catalog?.max_days ?? 365))" :key="d" :value="d">in {{ d }} days{{ d === (catalog?.default_days ?? 90) ? ' (default)' : '' }}</option>
        </select>
      </div>

      <div v-if="!catalog" class="st-skel" aria-busy="true"><span class="skel"></span><span class="skel"></span></div>
      <template v-else>
        <fieldset class="st-fieldset">
          <legend>What it may read</legend>
          <label v-for="r in catalog.reads" :key="r.scope" class="check-row tk-opt">
            <input v-model="draft.reads" type="checkbox" :value="r.scope" :disabled="!r.available" :aria-describedby="`${uid}-r-${r.scope}`" />
            <span>
              <span class="t">{{ r.label }}</span> <span class="st-mono tk-scope">{{ r.scope }}</span>
              <small :id="`${uid}-r-${r.scope}`">{{ r.available ? r.description : r.unavailable }}</small>
            </span>
          </label>
        </fieldset>

        <fieldset class="st-fieldset">
          <legend>What it may run</legend>
          <div class="choice-row" role="radiogroup" aria-label="Actions">
            <label><input v-model="draft.run" type="radio" :name="`${uid}-run`" value="none" />Nothing</label>
            <label><input v-model="draft.run" type="radio" :name="`${uid}-run`" value="all" />Everything your role allows</label>
            <label><input v-model="draft.run" type="radio" :name="`${uid}-run`" value="pick" />Pick actions</label>
          </div>
          <p v-if="draft.run === 'all'" class="help">{{ catalog.all.description }}</p>
          <template v-if="draft.run === 'pick'">
            <p v-if="!catalog.actions.length" class="help">No integration offers actions yet.</p>
            <label v-for="f in catalog.families" :key="f.scope" class="check-row tk-opt">
              <input v-model="draft.actions" type="checkbox" :value="f.scope" />
              <span><span class="t">{{ f.label }}</span> <span class="st-mono tk-scope">{{ f.scope }}</span></span>
            </label>
            <label v-for="a in catalog.actions" :key="a.scope" class="check-row tk-opt">
              <input v-model="draft.actions" type="checkbox" :value="a.scope" :disabled="!a.available" :aria-describedby="a.available ? undefined : `${uid}-a-${a.scope}`" />
              <span>
                <span class="t">{{ a.label }}</span> <span class="st-mono tk-scope">{{ a.scope }}</span> <span class="chip">{{ a.tier }}</span>
                <small v-if="!a.available" :id="`${uid}-a-${a.scope}`">{{ a.unavailable }}</small>
              </span>
            </label>
          </template>
        </fieldset>

        <div class="field">
          <label :for="`${uid}-extra`">More scopes <span class="opt-word">optional</span></label>
          <input :id="`${uid}-extra`" v-model="draft.extra" class="st-mono" type="text" autocomplete="off" spellcheck="false" :aria-describedby="`${uid}-extra-h`" />
          <span :id="`${uid}-extra-h`" class="help">Scopes by hand, separated by spaces: an action limited to one target, such as <code>proxmox.guest.start:target=proxmox:i_x/qemu/201</code>.</span>
        </div>
        <p v-if="catalog.approval" class="st-note"><Glyph kind="attn" /><span>{{ catalog.approval }}</span></p>
      </template>

      <p v-if="createError" class="err" role="alert">{{ createError }}</p>
      <div class="st-acts">
        <button type="submit" class="btn primary" :disabled="saving || !catalog" :aria-busy="saving || undefined">{{ saving ? 'Making…' : 'Make token' }}</button>
        <button type="button" class="btn ghost" @click="creating = false">Cancel</button>
      </div>
    </form>

    <div v-if="status === 'loading'" class="st-skel" aria-busy="true"><span class="skel"></span><span class="skel"></span></div>
    <div v-else-if="status === 'error'" class="st-note bad" role="alert"><Glyph kind="down" /><span>{{ loadError }}</span></div>
    <div v-else-if="!tokens.length && !creating" class="st-empty">
      <span class="t">No API tokens yet</span>
      <span class="m">Make one for a script, a Home Assistant automation or a cron job. You pick what it may read and run, and when it expires.</span>
    </div>
    <ul v-else class="st-list tk-list">
      <li v-for="t in sorted" :key="t.id">
        <div :class="['st-row', 'tk-row', { off: stateOf(t).off }]">
          <span class="mono-badge" aria-hidden="true">{{ t.name.slice(0, 2).toUpperCase() }}</span>
          <span>
            <span class="t">
              {{ t.name }}
              <span v-if="seesAll && t.user_id !== meId" class="chip">{{ t.username }}</span>
              <span v-if="stateOf(t).word" class="chip">{{ stateOf(t).word }}</span>
            </span>
            <span class="m">{{ t.scopes.map(scopeWords).join(', ') }}</span>
            <span class="m">
              made <span :title="absolute(t.created_at)">{{ ago(t.created_at, now) }}</span> ·
              <template v-if="t.revoked_at">revoked <span :title="absolute(t.revoked_at)">{{ ago(t.revoked_at, now) }}</span></template>
              <template v-else>{{ expiresWords(t) }}</template> ·
              <template v-if="t.last_used_at">last used <span :title="absolute(t.last_used_at)">{{ ago(t.last_used_at, now) }}</span><template v-if="t.last_used_ip"> from {{ t.last_used_ip }}</template></template>
              <template v-else>never used</template>
            </span>
            <span v-if="rowError[t.id]" class="err" role="alert">{{ rowError[t.id] }}</span>
          </span>
          <span v-if="!t.revoked_at && revoking !== t.id" class="end">
            <button type="button" class="btn sm ghost" :aria-label="`Revoke ${t.name}`" @click="revoking = t.id">Revoke</button>
          </span>
        </div>
        <div v-if="revoking === t.id" class="st-confirm tk-revoke" role="group" :aria-labelledby="`${uid}-rv-${t.id}`">
          <span :id="`${uid}-rv-${t.id}`">Revoke <b>{{ t.name }}</b>? Anything using it stops working at once, and plans it left waiting for approval are withdrawn.</span>
          <button type="button" class="btn danger solid" :disabled="busy === t.id" @click="revoke(t)">Revoke {{ t.name }}</button>
          <button type="button" class="btn" @click="revoking = ''">Cancel</button>
        </div>
      </li>
    </ul>
  </section>
</template>

<style>
.tk-row.off .t,
.tk-row.off .m {
  color: var(--text-3);
}
.tk-add {
  padding: var(--space-4);
  border: var(--hairline) solid var(--line);
  border-radius: var(--radius-lg);
}
.tk-opt {
  align-items: flex-start;
}
.tk-opt input {
  margin-top: 2px;
}
.tk-opt small {
  display: block;
  margin-top: 2px;
  font-size: var(--type-small);
  line-height: 1.4;
  color: var(--text-3);
}
.tk-scope {
  font-size: var(--type-caption);
  color: var(--text-3);
}
.tk-fresh {
  flex-direction: column;
}
.tk-fresh-body {
  display: flex;
  flex-direction: column;
  gap: var(--space-2);
  width: 100%;
}
.tk-fresh h4 {
  font: var(--weight-medium) var(--type-body) / 1.3 var(--font-ui);
}
.tk-revoke {
  margin: 0 var(--space-2) var(--space-3);
}
</style>
