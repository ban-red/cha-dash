<script setup lang="ts">
/**
 * Settings → API tokens. Every role manages its own tokens; owners and
 * admins see everyone's and can revoke any.
 *
 *   GET    /api/tokens          → {tokens: [{id, user_id, username, name, kind, scopes, created_at, expires_at, last_used_at, last_used_ip?, revoked_at}]}
 *   GET    /api/tokens/scopes   → the scope picker: {reads, families, actions, all, default_days, max_days, approval, agent}
 *   POST   /api/tokens          {name, kind, scopes, expires_in_days} → {token, secret}: the secret, once
 *   DELETE /api/tokens/{id}     → {token}, revoked at once
 *
 * A token acts as its user, never above the user's role, narrowed by its
 * scopes. T0 and T1 actions run when the token approves its own plan; T2
 * waits in the Inbox for an admin or the owner; T3 and T4 never. The secret
 * is shown once, right after it's made, with Copy; the hub keeps only a hash.
 *
 * While the owner has Agents on (`agent.on`), a token can be an agent token
 * instead (ADR-0017): it works only on POST /mcp, reads what its read
 * scopes allow (narrowable to a host, tag or lens; logs off unless picked)
 * and proposes the actions picked here (`propose:<action>`); every proposal
 * waits for a person, at every tier.
 *
 * Its own chunk, loaded with the pane.
 */
import SettingsHelp from './SettingsHelp.vue';
import { computed, nextTick, onMounted, reactive, ref, useId } from 'vue';
import Glyph from '../ui/marks/Glyph.vue';
import { ago, absolute } from '../lib/format';
import { api } from '../lib/api';
import { hubWords } from '../lib/actions';
import { useSession } from '../stores/session';
import { useUi } from '../stores/ui';
import { copyText } from './recipe';
import { focusFirst } from '../lib/focus';
import UiButton from '../ui/Button.vue';
import UiField from '../ui/Field.vue';
import type { TokenKind } from '../model/types';

export interface HubToken {
  id: string;
  user_id: string;
  username: string;
  name: string;
  /** api, or agent: reads and proposes through MCP, never acts. */
  kind: TokenKind;
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

/** The picker's agent section (authz `agentOffers`). */
interface AgentCatalog {
  /** Agents is on: agent tokens can be made. */
  on: boolean;
  reads: Offer[];
  propose: Offer[];
  families: Offer[];
  /** The qualifiers a read may carry: host, tag, lens. */
  narrow: string[];
  limits: string;
}

interface ScopeCatalog {
  reads: Offer[];
  families: Offer[];
  actions: Offer[];
  all: Offer;
  default_days: number;
  max_days: number;
  approval?: string;
  agent?: AgentCatalog;
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
  kind: 'api' as TokenKind,
  reads: [] as string[],
  run: 'none' as 'none' | 'all' | 'pick',
  actions: [] as string[],
  extra: '',
  /** An agent token's reads, proposals and narrowing. */
  agentReads: ['graph.read'] as string[],
  propose: [] as string[],
  narrow: '' as '' | 'host' | 'tag' | 'lens',
  narrowTo: '',
});
const agentCatalog = computed(() => (catalog.value?.agent?.on ? catalog.value.agent : undefined));
const NARROW_WORDS: Record<string, string> = { host: 'A host', tag: 'A tag', lens: 'A board (lens)' };
const nameError = ref('');
const DAY_CHOICES = [30, 90, 180, 365];
/** The secret just made: shown once, until Done. */
const fresh = ref<{ token: HubToken; secret: string } | undefined>();
const copied = ref(false);

async function startCreate(): Promise<void> {
  creating.value = true;
  Object.assign(draft, { name: '', days: 90, kind: 'api', reads: [], run: 'none', actions: [], extra: '', agentReads: ['graph.read'], propose: [], narrow: '', narrowTo: '' });
  nameError.value = '';
  createError.value = '';
  await loadCatalog();
  if (catalog.value) draft.days = catalog.value.default_days;
  await nextTick();
  document.getElementById(`${uid}-name`)?.focus();
}

/** The scopes the form adds up to, in the order the hub will keep them. */
const chosen = computed<string[]>(() => {
  if (draft.kind === 'agent') {
    const to = draft.narrowTo.trim();
    const narrowed = (s: string) => (draft.narrow && to && s !== 'logs.read' ? `${s}:${draft.narrow}=${to}` : s);
    return [...new Set([...draft.agentReads.map(narrowed), ...draft.propose])];
  }
  const out = [...draft.reads];
  if (draft.run === 'all') out.push('*');
  if (draft.run === 'pick') out.push(...draft.actions);
  for (const s of draft.extra.split(/[\s,]+/)) if (s) out.push(s);
  return [...new Set(out)];
});

async function create(): Promise<void> {
  if (saving.value || !catalog.value) return;
  nameError.value = draft.name.trim() ? '' : 'Name it after what will use it, such as “backup script”.';
  createError.value = '';
  if (nameError.value) return;
  if (draft.kind === 'agent' && draft.narrow && !draft.narrowTo.trim()) {
    createError.value = `Name the ${draft.narrow} to narrow its reads to, or read everything your role sees.`;
    return;
  }
  if (!chosen.value.length) {
    createError.value = 'Pick at least one thing the token may read or run.';
    return;
  }
  saving.value = true;
  try {
    const res = await api<{ token: HubToken; secret: string }>('/api/tokens', {
      method: 'POST',
      body: { name: draft.name.trim(), ...(draft.kind === 'agent' ? { kind: 'agent' } : {}), scopes: chosen.value, expires_in_days: draft.days },
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
  void focusFirst(`${uid}-new`, 'tk-h');
}

function cancelCreate(): void {
  creating.value = false;
  void focusFirst(`${uid}-new`, 'tk-h');
}

// ── revoke ──
const revoking = ref('');
const busy = ref('');
const rowError = reactive<Record<string, string>>({});

/** Open the confirm group and put focus on its Cancel, the safe choice. */
function askRevoke(t: HubToken): void {
  revoking.value = t.id;
  void focusFirst(`${uid}-rvc-${t.id}`);
}

function cancelRevoke(t: HubToken): void {
  revoking.value = '';
  void focusFirst(`${uid}-rvb-${t.id}`, 'tk-h');
}

async function revoke(t: HubToken): Promise<void> {
  // aria-disabled, not disabled, while busy: the button keeps focus.
  if (busy.value) return;
  busy.value = t.id;
  rowError[t.id] = '';
  try {
    const res = await api<{ token?: HubToken }>(`/api/tokens/${encodeURIComponent(t.id)}`, { method: 'DELETE' });
    tokens.value = tokens.value.map((x) => (x.id === t.id ? { ...x, ...(res?.token ?? { revoked_at: new Date().toISOString() }) } : x));
    revoking.value = '';
    // The row stays, revoked, without its Revoke button: back to the list's heading.
    void focusFirst('tk-h');
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
      <h3 id="tk-h" tabindex="-1">{{ seesAll ? 'Everyone’s tokens' : 'Your tokens' }}</h3>
      <span v-if="status === 'ready' && tokens.length" class="st-meta">{{ tokens.filter((t) => !t.revoked_at).length }} active</span>
      <UiButton v-if="!creating && !fresh" size="sm" :id="`${uid}-new`" @click="startCreate">New token</UiButton>
    </header>
    <SettingsHelp summary="For scripts and automations. A token never has more access than you.">
      For scripts and automations. A token acts as you, never with more than your role allows, and only for what you pick here. Send it as
      <code>Authorization: Bearer cdt_…</code> on <code>/api/</code> requests. Actions up to T1 run when the token asks; T2 actions wait in the Inbox until an
      admin or the owner approves them; T3 and T4 never run from a token.
    </SettingsHelp>

    <div v-if="fresh" class="tk-fresh st-note attn" role="region" :aria-labelledby="`${uid}-fresh-h`">
      <div class="tk-fresh-body">
        <h4 :id="`${uid}-fresh-h`">Copy “{{ fresh.token.name }}” now</h4>
        <p>This is the only time Cha Dash shows it. It keeps just a fingerprint, so a lost token can’t be shown again: revoke it and make a new one.</p>
        <div class="secret-row">
          <label :for="`${uid}-secret`" class="sr">The new token</label>
          <input :id="`${uid}-secret`" class="st-mono" type="text" readonly :value="fresh.secret" spellcheck="false" autocomplete="off" @focus="($event.target as HTMLInputElement).select()" />
          <UiButton @click="copy">{{ copied ? 'Copied' : 'Copy' }}</UiButton>
        </div>
        <p class="sr" aria-live="polite">{{ copied ? 'Copied to the clipboard.' : '' }}</p>
        <div class="st-acts"><UiButton variant="primary" size="sm" @click="done">I’ve saved it</UiButton></div>
      </div>
    </div>

    <form v-if="creating" class="st-form tk-add" novalidate aria-labelledby="tk-add-h" @submit.prevent="create">
      <h4 id="tk-add-h" class="sr">New token</h4>
      <UiField :id="`${uid}-name`" label="Name" help="What will use it, so you can tell tokens apart later." :error="nameError" v-slot="{ control }">
        <input v-bind="control" v-model="draft.name" type="text" maxlength="64" autocomplete="off" />
      </UiField>
      <UiField label="Expires" v-slot="{ control }">
        <select v-bind="control" v-model.number="draft.days">
          <option v-for="d in DAY_CHOICES.filter((x) => x <= (catalog?.max_days ?? 365))" :key="d" :value="d">in {{ d }} days{{ d === (catalog?.default_days ?? 90) ? ' (default)' : '' }}</option>
        </select>
      </UiField>

      <div v-if="!catalog" class="st-skel" aria-busy="true"><span class="skel"></span><span class="skel"></span></div>
      <fieldset v-if="agentCatalog" class="st-fieldset">
        <legend>What it’s for</legend>
        <div class="choice-row" role="radiogroup" aria-label="Kind of token">
          <label><input v-model="draft.kind" type="radio" :name="`${uid}-kind`" value="api" />A script or automation</label>
          <label><input v-model="draft.kind" type="radio" :name="`${uid}-kind`" value="agent" />An AI agent, through MCP</label>
        </div>
        <p v-if="draft.kind === 'agent'" class="help">An agent token reads and proposes, and never acts: every proposal waits for you or another admin, at every tier. It works only on <code>POST /mcp</code>.</p>
      </fieldset>
      <template v-if="catalog && draft.kind === 'agent' && agentCatalog">
        <fieldset class="st-fieldset">
          <legend>What the agent may read</legend>
          <label v-for="r in agentCatalog.reads" :key="r.scope" class="check-row tk-opt">
            <input v-model="draft.agentReads" type="checkbox" :value="r.scope" :disabled="!r.available" :aria-describedby="`${uid}-ar-${r.scope}`" />
            <span>
              <span class="t">{{ r.label }}</span> <span class="st-mono tk-scope">{{ r.scope }}</span>
              <small :id="`${uid}-ar-${r.scope}`">{{ r.available ? r.description : r.unavailable }}</small>
            </span>
          </label>
          <div class="tk-narrow">
            <UiField label="Narrow its reads to" v-slot="{ control }">
              <select v-bind="control" v-model="draft.narrow">
                <option value="">Everything your role sees</option>
                <option v-for="n in agentCatalog.narrow" :key="n" :value="n">{{ NARROW_WORDS[n] ?? n }}</option>
              </select>
            </UiField>
            <UiField v-if="draft.narrow" :label="draft.narrow === 'lens' ? 'Board ID' : draft.narrow === 'host' ? 'Host name or ID' : 'Tag'" v-slot="{ control }">
              <input v-bind="control" v-model="draft.narrowTo" type="text" maxlength="200" autocomplete="off" spellcheck="false" />
            </UiField>
          </div>
        </fieldset>
        <fieldset class="st-fieldset">
          <legend>What it may propose</legend>
          <p v-if="!agentCatalog.propose.length" class="help">No integration offers actions yet.</p>
          <label v-for="f in agentCatalog.families" :key="f.scope" class="check-row tk-opt">
            <input v-model="draft.propose" type="checkbox" :value="f.scope" />
            <span><span class="t">{{ f.label }}</span> <span class="st-mono tk-scope">{{ f.scope }}</span></span>
          </label>
          <label v-for="a in agentCatalog.propose" :key="a.scope" class="check-row tk-opt">
            <input v-model="draft.propose" type="checkbox" :value="a.scope" :disabled="!a.available" :aria-describedby="a.available && !a.description ? undefined : `${uid}-ap-${a.scope}`" />
            <span>
              <span class="t">{{ a.label }}</span> <span class="st-mono tk-scope">{{ a.scope }}</span> <span class="chip">{{ a.tier }}</span>
              <small v-if="!a.available || a.description" :id="`${uid}-ap-${a.scope}`">{{ a.available ? a.description : a.unavailable }}</small>
            </span>
          </label>
        </fieldset>
        <p class="st-note"><Glyph kind="attn" /><span>{{ agentCatalog.limits }}</span></p>
      </template>
      <template v-else-if="catalog">
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

        <details class="tk-more">
          <summary>Scopes by hand</summary>
        <UiField label="More scopes" optional>
          <template #default="{ control }"><input v-bind="control" v-model="draft.extra" class="st-mono" type="text" autocomplete="off" spellcheck="false" /></template>
          <template #help>Scopes by hand, separated by spaces: an action limited to one target, such as <code>proxmox.guest.start:target=proxmox:i_x/qemu/201</code>.</template>
        </UiField>
        </details>
        <p v-if="catalog.approval" class="st-note"><Glyph kind="attn" /><span>{{ catalog.approval }}</span></p>
      </template>

      <p v-if="createError" class="err" role="alert">{{ createError }}</p>
      <div class="st-acts">
        <UiButton variant="primary" type="submit" :aria-disabled="saving || !catalog ? 'true' : undefined" :aria-busy="saving || undefined">{{ saving ? 'Making…' : 'Make token' }}</UiButton>
        <UiButton variant="ghost" @click="cancelCreate">Cancel</UiButton>
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
              <span v-if="t.kind === 'agent'" class="chip">agent</span>
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
            <UiButton variant="ghost" size="sm" :id="`${uid}-rvb-${t.id}`" :aria-label="`Revoke ${t.name}`" @click="askRevoke(t)">Revoke</UiButton>
          </span>
        </div>
        <div v-if="revoking === t.id" class="st-confirm tk-revoke" role="group" :aria-labelledby="`${uid}-rv-${t.id}`">
          <span :id="`${uid}-rv-${t.id}`">Revoke <b>{{ t.name }}</b>? Anything using it stops working at once, and plans it left waiting for approval are withdrawn.</span>
          <UiButton variant="danger-solid" :aria-disabled="busy === t.id ? 'true' : undefined" :aria-busy="busy === t.id || undefined" @click="revoke(t)">Revoke {{ t.name }}</UiButton>
          <UiButton :id="`${uid}-rvc-${t.id}`" @click="cancelRevoke(t)">Cancel</UiButton>
        </div>
      </li>
    </ul>
  </section>
</template>

<style>
.tk-more > summary {
  cursor: pointer;
  font-size: var(--type-small);
  color: var(--text-3);
}
.tk-more[open] > summary {
  margin-bottom: var(--space-2);
}
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
.tk-narrow {
  display: flex;
  flex-wrap: wrap;
  gap: var(--space-3);
  margin-top: var(--space-2);
}
</style>
