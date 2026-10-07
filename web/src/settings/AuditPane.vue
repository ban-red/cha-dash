<script setup lang="ts">
/**
 * Settings → Audit (owner and admin): every attempt to change something,
 * refusals and failures included, newest first.
 *
 *   GET /api/audit?actor=&action=&target=&result=&since=&before=&limit=&cursor=  → {events, next?}
 *   GET /api/audit/verify                                                 → {ok, checked, first_break?}
 *   GET /api/audit/export                                                 NDJSON
 *
 * The log is hash-chained; the chain's state is said plainly at the top.
 * "Show older" pages with the `next` cursor (sent back as `cursor`); when
 * that fails it says so beside the button, which then tries again. After a
 * filter, a polite live region says how many events match. Each row's
 * Details names its event for screen readers.
 * Its own chunk, loaded with the pane.
 */
import { computed, onMounted, reactive, ref, useId } from 'vue';
import Glyph from '../components/marks/Glyph.vue';
import { absolute, duration, fmtInt, hhmm, shortDate } from '../lib/format';
import { api } from '../lib/api';
import { hubWords } from '../lib/actions';
import { focusFirst } from '../lib/focus';
import { label } from '../model/graph';
import { useLive } from '../stores/live';

export interface AuditEvent {
  id: string;
  seq?: number;
  at: string;
  actor?: { kind?: string; id?: string; name?: string; via?: string; session?: string; ip?: string };
  action: string;
  target?: string;
  params?: Record<string, unknown>;
  tier?: string;
  result: 'ok' | 'failed' | 'refused' | (string & {});
  error?: { code?: string; message?: string } | null;
  duration_ms?: number;
  task?: string | null;
  prev_hash?: string;
  hash?: string;
}

interface Verify {
  ok: boolean;
  checked: number;
  first_break?: number | { seq?: number; id?: string; at?: string } | null;
}

const live = useLive();
const uid = useId();
const PAGE = 50;

const filters = reactive({ actor: '', action: '', target: '', result: '', since: '' });
const SINCE: { id: string; label: string; ms: number }[] = [
  { id: '', label: 'Any time', ms: 0 },
  { id: '1h', label: 'Last hour', ms: 3600_000 },
  { id: '24h', label: 'Last 24 hours', ms: 86_400_000 },
  { id: '7d', label: 'Last 7 days', ms: 7 * 86_400_000 },
  { id: '30d', label: 'Last 30 days', ms: 30 * 86_400_000 },
];

const events = ref<AuditEvent[]>([]);
const next = ref<string | undefined>(undefined);
const status = ref<'loading' | 'ready' | 'error'>('loading');
const loadError = ref('');
const more = ref(false);
const olderError = ref('');
/** What the live region says after a filter: how many events match. */
const announce = ref('');
const open = reactive(new Set<string>());
const verify = ref<Verify | null>(null);
const verifyError = ref('');
const verifying = ref(false);

function query(cursor?: string): string {
  const q = new URLSearchParams();
  for (const k of ['actor', 'action', 'target', 'result'] as const) if (filters[k].trim()) q.set(k, filters[k].trim());
  const span = SINCE.find((s) => s.id === filters.since)?.ms ?? 0;
  if (span) q.set('since', new Date(live.now.value - span).toISOString());
  if (cursor) q.set('cursor', cursor);
  q.set('limit', String(PAGE));
  return q.toString();
}

async function load(opts: { say?: boolean } = {}): Promise<void> {
  status.value = 'loading';
  loadError.value = '';
  olderError.value = '';
  announce.value = '';
  try {
    const res = await api<{ events?: AuditEvent[]; next?: string | null }>(`/api/audit?${query()}`);
    events.value = res?.events ?? [];
    next.value = res?.next || undefined;
    status.value = 'ready';
    if (opts.say) announce.value = countWords();
  } catch (err) {
    status.value = 'error';
    loadError.value = hubWords(err, 'The audit log couldn’t be loaded.');
  }
}

/** "12 events", "No events match", with a word when there are older ones. */
function countWords(): string {
  const n = events.value.length;
  if (!n) return filtered.value ? 'No events match' : 'No events yet';
  return `${fmtInt(n)} ${n === 1 ? 'event' : 'events'}${next.value ? ', and older ones with Show older' : ''}`;
}

function runFilter(): void {
  void load({ say: true });
}

async function older(): Promise<void> {
  // aria-disabled, not disabled, while loading: the button keeps focus.
  if (!next.value || more.value) return;
  more.value = true;
  olderError.value = '';
  try {
    const res = await api<{ events?: AuditEvent[]; next?: string | null }>(`/api/audit?${query(next.value)}`);
    const seen = new Set(events.value.map((e) => e.id));
    const fresh = (res?.events ?? []).filter((e) => !seen.has(e.id));
    events.value = [...events.value, ...fresh];
    next.value = res?.next || undefined;
    // Carry on where the new events start; the button may be gone.
    const first = fresh[0];
    if (first) void focusFirst(`${uid}-dt-${first.id}`);
    else if (!next.value) void focusFirst('au-ev-h');
  } catch (err) {
    olderError.value = hubWords(err, 'Older events couldn’t be loaded.');
  } finally {
    more.value = false;
  }
}

async function check(): Promise<void> {
  verifying.value = true;
  verifyError.value = '';
  try {
    verify.value = await api<Verify>('/api/audit/verify');
  } catch (err) {
    verify.value = null;
    verifyError.value = hubWords(err, 'The chain couldn’t be checked.');
  } finally {
    verifying.value = false;
  }
}

function clear(): void {
  Object.assign(filters, { actor: '', action: '', target: '', result: '', since: '' });
  void load({ say: true });
}

const filtered = computed(() => Object.values(filters).some((v) => v !== ''));

/** Where the chain breaks, in words. */
const breakWords = computed(() => {
  const b = verify.value?.first_break;
  if (b === undefined || b === null) return 'an unknown event';
  if (typeof b === 'number') return `event ${fmtInt(b)}`;
  const at = b.at ? ` (${shortDate(b.at)} ${hhmm(b.at)})` : '';
  return b.seq !== undefined ? `event ${fmtInt(b.seq)}${at}` : `event ${b.id ?? '?'}${at}`;
});

const RESULT: Record<string, { glyph: 'check' | 'down' | 'lock'; word: string }> = {
  ok: { glyph: 'check', word: 'done' },
  failed: { glyph: 'down', word: 'failed' },
  refused: { glyph: 'lock', word: 'refused' },
};
const resultOf = (e: AuditEvent) => RESULT[e.result] ?? { glyph: 'down' as const, word: e.result };

/** A target as a name when the Home Graph knows it. */
function targetName(t: string | undefined): string {
  if (!t) return '';
  const r = live.graph.value?.resources[t];
  return r ? label(r) || t : t;
}

function who(e: AuditEvent): string {
  const a = e.actor;
  const name = a?.name || (a?.kind === 'system' ? 'Cha Dash' : a?.kind) || 'someone';
  return a?.via ? `${name} · via ${a.via}` : name;
}

const today = computed(() => shortDate(live.now.value));
const when = (at: string) => (shortDate(at) === today.value ? hhmm(at) : `${shortDate(at)} ${hhmm(at)}`);
const toggle = (id: string) => (open.has(id) ? open.delete(id) : open.add(id));
const short = (h: string | undefined) => (h ? h.replace(/^sha256:/, '').slice(0, 12) : '—');
/** What a row's Details button opens, for its accessible name. */
const summary = (e: AuditEvent) => `${e.action}${e.target ? ` · ${targetName(e.target)}` : ''}, ${resultOf(e).word}, ${when(e.at)}`;

onMounted(() => {
  void load();
  void check();
});
</script>

<template>
  <section class="st-section" aria-labelledby="au-chain-h">
    <header>
      <h3 id="au-chain-h">The chain</h3>
      <a class="btn sm" href="/api/audit/export" download>Export · NDJSON</a>
    </header>
    <p class="st-lede">Every change Cha Dash makes, or refuses to make, is recorded here. Each event carries the hash of the one before it, so a gap or an edit shows.</p>
    <div v-if="verifying && !verify" class="st-skel" aria-busy="true"><span class="skel"></span></div>
    <div v-else-if="verifyError" class="st-note bad" role="alert"><Glyph kind="down" /><span>{{ verifyError }}</span></div>
    <div v-else-if="verify?.ok" class="st-note" role="status">
      <Glyph kind="check" /><span>The audit log is intact · {{ fmtInt(verify.checked) }} events checked</span>
    </div>
    <div v-else-if="verify" class="st-note bad" role="alert">
      <Glyph kind="down" />
      <span>
        The audit log’s chain breaks at {{ breakWords }} · {{ fmtInt(verify.checked) }} events checked. Events from there on may have been changed outside Cha Dash;
        the export keeps them as they are.
      </span>
    </div>
    <div class="st-acts">
      <button type="button" class="btn sm ghost" :disabled="verifying" :aria-busy="verifying || undefined" @click="check">{{ verifying ? 'Checking…' : 'Check again' }}</button>
    </div>
  </section>

  <section class="st-section" aria-labelledby="au-ev-h">
    <header>
      <h3 id="au-ev-h" tabindex="-1">Events</h3>
      <span class="st-meta">newest first</span>
    </header>

    <form class="au-filters" aria-label="Filter events" @submit.prevent="runFilter">
      <div class="field">
        <label :for="`${uid}-actor`">Who</label>
        <input :id="`${uid}-actor`" v-model="filters.actor" type="text" autocomplete="off" placeholder="username" />
      </div>
      <div class="field">
        <label :for="`${uid}-action`">Action</label>
        <input :id="`${uid}-action`" v-model="filters.action" type="text" autocomplete="off" placeholder="proxmox.guest.reboot" />
      </div>
      <div class="field">
        <label :for="`${uid}-target`">Target</label>
        <input :id="`${uid}-target`" v-model="filters.target" type="text" autocomplete="off" placeholder="resource ID" />
      </div>
      <div class="field">
        <label :for="`${uid}-result`">Result</label>
        <select :id="`${uid}-result`" v-model="filters.result">
          <option value="">Any</option>
          <option value="ok">Done</option>
          <option value="failed">Failed</option>
          <option value="refused">Refused</option>
        </select>
      </div>
      <div class="field">
        <label :for="`${uid}-since`">When</label>
        <select :id="`${uid}-since`" v-model="filters.since">
          <option v-for="s in SINCE" :key="s.id" :value="s.id">{{ s.label }}</option>
        </select>
      </div>
      <div class="au-filter-acts">
        <button type="submit" class="btn sm primary">Filter</button>
        <button v-if="filtered" type="button" class="btn sm ghost" @click="clear">Clear</button>
      </div>
    </form>
    <p class="sr" role="status">{{ announce }}</p>

    <div v-if="status === 'loading'" class="st-skel" aria-busy="true"><span class="skel"></span><span class="skel"></span><span class="skel"></span></div>
    <div v-else-if="status === 'error'" class="st-note bad" role="alert"><Glyph kind="down" /><span>{{ loadError }}</span></div>
    <div v-else-if="!events.length" class="st-empty">
      <div class="t">{{ filtered ? 'Nothing matches' : 'Nothing recorded yet' }}</div>
      <div class="m">{{ filtered ? 'Try fewer filters, or a longer time.' : 'Actions, settings changes and sign-in changes appear here as they happen.' }}</div>
    </div>
    <ol v-else class="st-list au-list">
      <li v-for="e in events" :key="e.id">
        <div class="st-row au-row">
          <Glyph :kind="resultOf(e).glyph" :label="resultOf(e).word" />
          <span>
            <span class="t"><span class="st-mono">{{ e.action }}</span><template v-if="e.target"> · {{ targetName(e.target) }}</template></span>
            <span class="m">
              {{ who(e) }}<template v-if="e.tier"> · {{ e.tier }}</template> · {{ resultOf(e).word
              }}<template v-if="e.duration_ms !== undefined && e.duration_ms !== null"> in {{ duration(e.duration_ms) }}</template>
            </span>
            <span v-if="e.error?.message" class="m au-err">{{ e.error.message }}</span>
          </span>
          <span class="end">
            <time :datetime="e.at" :title="absolute(e.at)">{{ when(e.at) }}</time>
            <button
              :id="`${uid}-dt-${e.id}`"
              type="button"
              class="btn sm ghost"
              :aria-expanded="open.has(e.id)"
              :aria-controls="open.has(e.id) ? `${uid}-d-${e.id}` : undefined"
              @click="toggle(e.id)"
            >
              Details<span class="sr">: {{ summary(e) }}</span>
            </button>
          </span>
        </div>
        <dl v-if="open.has(e.id)" :id="`${uid}-d-${e.id}`" class="kv au-detail">
          <template v-if="e.seq !== undefined"><dt>Event</dt><dd>{{ fmtInt(e.seq) }}</dd></template>
          <dt>Target</dt><dd class="st-mono">{{ e.target || '—' }}</dd>
          <template v-if="e.actor?.ip"><dt>From</dt><dd class="st-mono">{{ e.actor.ip }}</dd></template>
          <template v-if="e.task"><dt>Task</dt><dd class="st-mono">{{ e.task }}</dd></template>
          <template v-if="e.params && Object.keys(e.params).length"><dt>Params</dt><dd class="st-mono">{{ JSON.stringify(e.params) }}</dd></template>
          <template v-if="e.error?.code"><dt>Code</dt><dd class="st-mono">{{ e.error.code }}</dd></template>
          <dt>Hash</dt><dd class="st-mono">{{ short(e.hash) }} ← {{ short(e.prev_hash) }}</dd>
        </dl>
      </li>
    </ol>
    <div v-if="status === 'ready' && next" class="st-acts au-older">
      <button type="button" class="btn sm" :aria-disabled="more ? 'true' : undefined" :aria-busy="more || undefined" :aria-describedby="olderError ? `${uid}-older-e` : undefined" @click="older">
        {{ more ? 'Loading…' : olderError ? 'Try again' : 'Show older' }}
      </button>
      <p v-if="olderError" :id="`${uid}-older-e`" class="err" role="alert">{{ olderError }}</p>
    </div>
  </section>
</template>

<style>
.au-filters {
  display: grid;
  grid-template-columns: repeat(auto-fit, minmax(9.5rem, 1fr));
  gap: var(--space-3);
  align-items: end;
}
.au-filter-acts {
  display: flex;
  gap: var(--space-2);
  align-items: center;
  min-height: var(--target-min);
}
.au-row {
  align-items: start;
}
.au-row > .g {
  margin-top: 4px;
}
.au-row .t,
.au-row .m {
  display: block;
}
.au-err {
  color: var(--text-2);
}
.au-row .end time {
  font-family: var(--font-num);
}
.au-older {
  flex-wrap: wrap;
  align-items: center;
}
.au-older .err {
  margin: 0;
}
.au-detail {
  margin: 0 var(--space-2) var(--space-3) calc(var(--space-2) + 22px);
  word-break: break-all;
}
</style>
