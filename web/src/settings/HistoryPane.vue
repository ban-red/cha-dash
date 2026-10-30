<script setup lang="ts">
/**
 * Settings → History: how much metrics and health history the hub keeps in
 * history.db (ADR-0031), what it holds now, and clearing it.
 *
 *   GET  /api/settings/history         {level, locked, demo, series, resources, file_bytes, tiers, estimates}
 *   PUT  /api/settings/history         {level?, max_bytes?, exclude?, exclude_keys?, keep?} → the same
 *   POST /api/settings/history/clear   → the same; ?instance=<id> for one integration
 *
 * The levels are a native radio group with Save, like the security level,
 * so arrowing through them never saves. Each says about how big history.db
 * grows at it, for the series recorded so far. The size limit is a select
 * with its own Save; past it, the hub drops the oldest points within the
 * hour, and the pane says when it last did. By integration lists every
 * connection (from the instances list, so one with nothing recorded yet can
 * be left out too) with a Record box and one Save; leaving one out stops its
 * metrics, never its health. On disk says how long each level of detail is
 * kept, as a select per row (the level's own, or a choice the hub offers)
 * with one Save. By metric lists patterns ("docker:*#net_rx") left out,
 * each saved as it's added or removed. Clearing, all or one, asks first,
 * inline.
 * With CHADASH_HISTORY=off, or in demo mode, the pane says why there's
 * nothing to choose.
 *
 * Its own chunk: SettingsSheet loads it when the pane opens.
 */
import { computed, onMounted, ref, useId } from 'vue';
import Glyph from '../components/marks/Glyph.vue';
import { api } from '../lib/api';
import { fmtInt, shortDate } from '../lib/format';
import type { HistoryLevel } from '../model/history';
import { errorWords, integrations, loadInstances } from '../stores/integrations';
import { useUi } from '../stores/ui';

interface TierView {
  tier: string;
  step_ms: number;
  keep_ms: number;
  default_keep_ms: number;
  custom: boolean;
  choices_ms: number[];
  chunks: number;
  bytes: number;
  oldest: number | null;
}
interface HistoryView {
  level: HistoryLevel;
  locked: boolean;
  demo: boolean;
  series: number;
  resources: number;
  file_bytes: number;
  max_bytes: number;
  trimmed_at: number | null;
  tiers: TierView[];
  instances: { id: string; series: number; bytes: number }[];
  excluded: string[];
  exclude_keys: { pattern: string; series: number }[];
  estimates: Partial<Record<HistoryLevel, number>>;
}

const ui = useUi();
const name = useId();
const view = ref<HistoryView | null>(null);
const status = ref<'loading' | 'ready' | 'error'>('loading');
const loadError = ref('');
const choice = ref<HistoryLevel>('standard');
const saving = ref(false);
const saveError = ref('');
const limit = ref(0);
const limiting = ref(false);
const limitError = ref('');
const recorded = ref<Record<string, boolean>>({});
const excluding = ref(false);
const excludeError = ref('');
/** The row whose Clear is asking, or '' (and 'all' for Clear history). */
const confirming = ref<string>('');
const clearing = ref(false);
const clearError = ref('');
/** Each tier's retention in the form: ms, or DEFAULT for the level's. */
const DEFAULT = -1;
const keepChoice = ref<Record<string, number>>({});
const keeping = ref(false);
const keepError = ref('');
const pattern = ref('');
const patterning = ref(false);
const patternError = ref('');

const LEVELS: { id: HistoryLevel; label: string; description: string }[] = [
  { id: 'standard', label: 'Standard', description: 'By the minute for 48 hours and by the quarter hour for 14 days, with health for 90 days.' },
  { id: 'long-term', label: 'Long-term', description: 'Also by the hour for a year and by the day forever, with health for two years.' },
  { id: 'off', label: 'Off', description: 'Nothing new is kept. What’s on disk stays until it ages out, and sparklines start empty after a restart.' },
];

const LIMITS = [256e6, 512e6, 1e9, 2e9, 5e9, 10e9, 0];
const limitWords = (b: number) => (b === 0 ? 'No limit' : size(b));
/** The saved limit among the choices, even when it's one the hub was given another way. */
const limitChoices = computed(() => {
  const saved = view.value?.max_bytes;
  return saved === undefined || LIMITS.includes(saved) ? LIMITS : [...LIMITS, saved].sort((a, b) => (a || Infinity) - (b || Infinity));
});

/** "840 kB", "31 MB", "1.2 GB". */
function size(bytes: number): string {
  if (bytes < 1e6) return `${Math.max(1, Math.round(bytes / 1e3))} kB`;
  if (bytes < 1e9) return `${Math.round(bytes / 1e6)} MB`;
  return `${Number((bytes / 1e9).toFixed(1))} GB`;
}

function estimate(l: HistoryLevel): string {
  const v = view.value;
  const bytes = v?.estimates[l];
  if (!v || bytes === undefined) return '';
  const at = v.series >= 300 ? `${fmtInt(v.series)} series` : 'a typical 300 series';
  return `About ${size(bytes)} at ${at}${l === 'long-term' ? ', after two years' : ''}.`;
}

/** "48 hours", "14 days", "1 year", "forever". */
function keepWords(ms: number): string {
  const h = ms / 3_600_000;
  if (ms === 0) return 'forever';
  if (h < 72) return `${h} hours`;
  const d = h / 24;
  if (d < 365) return `${d} days`;
  const y = Math.round(d / 365);
  return y === 1 ? '1 year' : `${y} years`;
}
const stepWords: Record<string, string> = { '1m': 'Each minute', '15m': 'Each quarter hour', '1h': 'Each hour', '1d': 'Each day' };

function syncKeep(): void {
  const out: Record<string, number> = {};
  for (const t of view.value?.tiers ?? []) out[t.tier] = t.custom ? t.keep_ms : DEFAULT;
  keepChoice.value = out;
}
/** A tier's choices, with a saved value the hub was given another way. */
function keepOptions(t: TierView): number[] {
  const cs = t.choices_ms;
  return !t.custom || cs.includes(t.keep_ms) ? cs : [...cs, t.keep_ms].sort((a, b) => (a || Infinity) - (b || Infinity));
}
const keepDirty = computed(() => (view.value?.tiers ?? []).some((t) => keepChoice.value[t.tier] !== (t.custom ? t.keep_ms : DEFAULT)));
/** What a tier would keep with the form's choice, forever as Infinity. */
function effective(t: TierView): number {
  const c = keepChoice.value[t.tier] ?? DEFAULT;
  const ms = c === DEFAULT ? t.default_keep_ms : c;
  return ms === 0 ? Infinity : ms;
}
/** Coarser points kept for less time than finer ones would leave long ranges blank. */
const keepProblem = computed(() => {
  const ts = view.value?.tiers ?? [];
  for (let i = 1; i < ts.length; i++) {
    if (effective(ts[i]!) < effective(ts[i - 1]!)) {
      return `${stepWords[ts[i]!.tier] ?? ts[i]!.tier} can’t be kept for less time than ${(stepWords[ts[i - 1]!.tier] ?? ts[i - 1]!.tier).toLowerCase()}.`;
    }
  }
  return '';
});

async function saveKeep(): Promise<void> {
  const v = view.value;
  if (!v || !keepDirty.value || keeping.value || keepProblem.value) return;
  const keep: Record<string, number | null> = {};
  for (const t of v.tiers) {
    const c = keepChoice.value[t.tier] ?? DEFAULT;
    keep[t.tier] = c === DEFAULT ? null : c;
  }
  keeping.value = true;
  keepError.value = '';
  try {
    view.value = await api<HistoryView>('/api/settings/history', { method: 'PUT', body: { keep } });
    syncKeep();
    ui.toast('History keeps each level of detail as chosen. Older points go within a minute.');
  } catch (err) {
    syncKeep();
    keepError.value = errorWords(err, 'How long history is kept wasn’t changed.');
  } finally {
    keeping.value = false;
  }
}

async function savePatterns(next: string[], done: string): Promise<boolean> {
  patterning.value = true;
  patternError.value = '';
  try {
    view.value = await api<HistoryView>('/api/settings/history', { method: 'PUT', body: { exclude_keys: next } });
    ui.toast(done);
    return true;
  } catch (err) {
    patternError.value = errorWords(err, 'What’s recorded wasn’t changed.');
    return false;
  } finally {
    patterning.value = false;
  }
}
async function addPattern(): Promise<void> {
  const v = view.value;
  const p = pattern.value.trim();
  if (!v || !p || patterning.value) return;
  if (v.exclude_keys.some((k) => k.pattern === p)) {
    patternError.value = `${p} is already left out.`;
    return;
  }
  if (await savePatterns([...v.exclude_keys.map((k) => k.pattern), p], `Metrics matching ${p} are left out of history.`)) pattern.value = '';
}
function removePattern(p: string): void {
  const v = view.value;
  if (!v || patterning.value) return;
  void savePatterns(
    v.exclude_keys.map((k) => k.pattern).filter((k) => k !== p),
    `Metrics matching ${p} are recorded again.`,
  );
}
const tiersShown = computed(() => (view.value?.tiers ?? []).filter((t) => t.chunks > 0 || view.value?.level === 'long-term' || t.tier === '1m' || t.tier === '15m'));

async function load(): Promise<void> {
  try {
    view.value = await api<HistoryView>('/api/settings/history');
    choice.value = view.value.level;
    limit.value = view.value.max_bytes;
    syncRecorded();
    syncKeep();
    status.value = 'ready';
  } catch (err) {
    status.value = 'error';
    loadError.value = errorWords(err, 'History settings couldn’t be loaded.');
  }
}

async function save(): Promise<void> {
  const v = view.value;
  if (!v || v.level === choice.value || saving.value) return;
  const level = LEVELS.find((l) => l.id === choice.value)!;
  saving.value = true;
  saveError.value = '';
  try {
    view.value = await api<HistoryView>('/api/settings/history', { method: 'PUT', body: { level: level.id } });
    choice.value = view.value.level;
    ui.toast(level.id === 'off' ? 'History is off. Nothing new is kept.' : `History: ${level.label}.`);
  } catch (err) {
    choice.value = v.level;
    saveError.value = errorWords(err, 'The history level wasn’t changed.');
  } finally {
    saving.value = false;
  }
}

/** Every connection, with what it has recorded: the instances list, plus any the hub still holds history for. */
const byIntegration = computed(() => {
  const v = view.value;
  if (!v) return [];
  const held = new Map(v.instances.map((i) => [i.id, i]));
  const rows = integrations.instances.map((i) => ({ id: i.id, name: i.name, series: held.get(i.id)?.series ?? 0, bytes: held.get(i.id)?.bytes ?? 0 }));
  for (const i of v.instances) if (!rows.some((r) => r.id === i.id)) rows.push({ id: i.id, name: i.id ? `Removed (${i.id})` : 'The hub', series: i.series, bytes: i.bytes });
  return rows.sort((a, b) => b.series - a.series || a.name.localeCompare(b.name));
});
function syncRecorded(): void {
  const out: Record<string, boolean> = {};
  for (const r of byIntegration.value) out[r.id] = !view.value?.excluded.includes(r.id);
  recorded.value = out;
}
const excludeDirty = computed(() => byIntegration.value.some((r) => recorded.value[r.id] !== !view.value?.excluded.includes(r.id)));

async function saveExcluded(): Promise<void> {
  const v = view.value;
  if (!v || !excludeDirty.value || excluding.value) return;
  const exclude = byIntegration.value.filter((r) => recorded.value[r.id] === false).map((r) => r.id);
  // Keep exclusions of connections this page doesn't list.
  for (const id of v.excluded) if (!byIntegration.value.some((r) => r.id === id)) exclude.push(id);
  excluding.value = true;
  excludeError.value = '';
  try {
    view.value = await api<HistoryView>('/api/settings/history', { method: 'PUT', body: { exclude } });
    syncRecorded();
    ui.toast(exclude.length ? `${exclude.length === 1 ? 'One integration is' : `${exclude.length} integrations are`} left out of history.` : 'Every integration is recorded.');
  } catch (err) {
    syncRecorded();
    excludeError.value = errorWords(err, 'What’s recorded wasn’t changed.');
  } finally {
    excluding.value = false;
  }
}

async function clearOne(id: string, name: string): Promise<void> {
  clearing.value = true;
  clearError.value = '';
  try {
    view.value = await api<HistoryView>(`/api/settings/history/clear?instance=${encodeURIComponent(id)}`, { method: 'POST' });
    confirming.value = '';
    syncRecorded();
    ui.toast(`Cleared the history of ${name}.`);
  } catch (err) {
    clearError.value = errorWords(err, 'That history wasn’t cleared.');
  } finally {
    clearing.value = false;
  }
}

async function saveLimit(): Promise<void> {
  const v = view.value;
  if (!v || v.max_bytes === limit.value || limiting.value) return;
  limiting.value = true;
  limitError.value = '';
  try {
    view.value = await api<HistoryView>('/api/settings/history', { method: 'PUT', body: { max_bytes: limit.value } });
    limit.value = view.value.max_bytes;
    ui.toast(limit.value === 0 ? 'History has no size limit.' : `History is limited to ${limitWords(limit.value)}.`);
  } catch (err) {
    limit.value = v.max_bytes;
    limitError.value = errorWords(err, 'The size limit wasn’t changed.');
  } finally {
    limiting.value = false;
  }
}

async function clear(): Promise<void> {
  clearing.value = true;
  clearError.value = '';
  try {
    view.value = await api<HistoryView>('/api/settings/history/clear', { method: 'POST' });
    confirming.value = '';
    syncRecorded();
    ui.toast('History cleared. Recording carries on from now.');
  } catch (err) {
    clearError.value = errorWords(err, 'The history wasn’t cleared.');
  } finally {
    clearing.value = false;
  }
}

onMounted(() => {
  void loadInstances().then(syncRecorded);
  void load();
});
</script>

<template>
  <div class="hp">
    <div v-if="status === 'loading'" class="st-skel" aria-busy="true"><span class="skel"></span><span class="skel"></span></div>
    <div v-else-if="status === 'error'" class="st-note bad" role="alert"><Glyph kind="down" /><span>{{ loadError }}</span></div>
    <template v-else-if="view">
      <section class="st-section" aria-labelledby="hp-level-h">
        <header><h3 id="hp-level-h">Recording</h3></header>
        <p class="st-lede">
          Cha Dash keeps what integrations report, with the average, low and high of each slot, and every change in health. Sheets chart it, and sparklines keep
          their past across restarts.
        </p>
        <div v-if="view.locked" class="st-note" role="note">
          <Glyph kind="off" />
          <span v-if="view.demo">The Demo Home makes its own history, so this hub keeps none.</span>
          <span v-else>History is off for this hub: it was started with <code>CHADASH_HISTORY=off</code>. Remove that and restart to choose here.</span>
        </div>
        <template v-else>
          <fieldset class="sl-group" :aria-busy="saving">
            <legend class="sr">How much history to keep</legend>
            <label v-for="l in LEVELS" :key="l.id" :class="['sl-card', { on: choice === l.id }]">
              <input type="radio" :name="name" :value="l.id" :checked="choice === l.id" :disabled="saving" :aria-describedby="`${name}-${l.id}`" @change="choice = l.id" />
              <span class="sl-text">
                <span class="sl-label">{{ l.label }}<span v-if="l.id === 'standard'" class="chip">default</span></span>
                <span :id="`${name}-${l.id}`" class="sl-desc">{{ l.description }} {{ l.id === 'off' ? '' : estimate(l.id) }}</span>
              </span>
            </label>
          </fieldset>
          <div class="st-acts">
            <button
              type="button"
              class="btn sm"
              aria-label="Save history level"
              :aria-disabled="choice === view.level || saving ? 'true' : undefined"
              :aria-busy="saving || undefined"
              @click="save"
            >
              Save
            </button>
          </div>
          <p v-if="saveError" class="err" role="alert">{{ saveError }}</p>
        </template>
      </section>

      <section v-if="!view.locked" class="st-section" aria-labelledby="hp-disk-h">
        <header><h3 id="hp-disk-h">On disk</h3></header>
        <p class="st-lede">
          {{ size(view.file_bytes) }} in <code>history.db</code>: {{ fmtInt(view.series) }} series and the health of {{ fmtInt(view.resources) }} resources.
          Containers are read only while their sheet is open, so their history has gaps.
        </p>
        <table class="hp-tiers">
          <caption class="sr">
            What each level of detail holds
          </caption>
          <thead>
            <tr>
              <th scope="col">Points</th>
              <th scope="col">Kept for</th>
              <th scope="col">Since</th>
              <th scope="col" class="n">Size</th>
            </tr>
          </thead>
          <tbody>
            <tr v-for="t in tiersShown" :key="t.tier">
              <th scope="row">{{ stepWords[t.tier] ?? t.tier }}</th>
              <td>
                <select v-model.number="keepChoice[t.tier]" :disabled="keeping" :aria-label="`Keep ${(stepWords[t.tier] ?? t.tier).toLowerCase()} points for`">
                  <option :value="DEFAULT">{{ keepWords(t.default_keep_ms) }} (level’s)</option>
                  <option v-for="ms in keepOptions(t)" :key="ms" :value="ms">{{ keepWords(ms) }}</option>
                </select>
              </td>
              <td>{{ t.oldest === null ? '—' : shortDate(t.oldest) }}</td>
              <td class="n">{{ t.chunks ? size(t.bytes) : '—' }}</td>
            </tr>
          </tbody>
        </table>
        <div class="st-acts">
          <button
            type="button"
            class="btn sm"
            aria-label="Save how long each level of detail is kept"
            :aria-disabled="!keepDirty || keeping || keepProblem ? 'true' : undefined"
            :aria-busy="keeping || undefined"
            @click="saveKeep"
          >
            Save
          </button>
        </div>
        <p v-if="keepProblem" class="err" role="alert">{{ keepProblem }}</p>
        <p v-if="keepError" class="err" role="alert">{{ keepError }}</p>
        <div class="hp-limit">
          <label for="hp-limit">Size limit</label>
          <select id="hp-limit" v-model.number="limit" :disabled="limiting" aria-describedby="hp-limit-d">
            <option v-for="b in limitChoices" :key="b" :value="b">{{ limitWords(b) }}</option>
          </select>
          <button
            type="button"
            class="btn sm"
            aria-label="Save size limit"
            :aria-disabled="limit === view.max_bytes || limiting ? 'true' : undefined"
            :aria-busy="limiting || undefined"
            @click="saveLimit"
          >
            Save
          </button>
          <p id="hp-limit-d" class="hp-limit-d">
            Past it, Cha Dash drops the oldest points of the level of detail that takes the most room, within the hour. Health changes are kept.{{
              view.trimmed_at ? ` It last did on ${shortDate(view.trimmed_at)}.` : ''
            }}
          </p>
        </div>
        <p v-if="limitError" class="err" role="alert">{{ limitError }}</p>
        <div v-if="confirming !== 'all'" class="st-acts">
          <button type="button" class="btn sm ghost" :disabled="view.series === 0 && view.resources === 0" @click="confirming = 'all'">Clear history…</button>
        </div>
        <div v-else class="st-confirm" role="alertdialog" aria-labelledby="hp-clear-q">
          <span id="hp-clear-q">Delete every point and health change in <code>history.db</code>? Charts start over from now. This can’t be undone.</span>
          <button type="button" class="btn danger solid" :disabled="clearing" :aria-busy="clearing || undefined" @click="clear">Clear history<span class="tier">T3</span></button>
          <button type="button" class="btn" @click="confirming = ''">Cancel</button>
        </div>
        <p v-if="clearError && confirming === 'all'" class="err" role="alert">{{ clearError }}</p>
      </section>

      <section v-if="!view.locked && byIntegration.length" class="st-section" aria-labelledby="hp-int-h">
        <header><h3 id="hp-int-h">By integration</h3></header>
        <p class="st-lede">Leave out an integration whose metrics you don’t need, such as a noisy one. Its health is still recorded.</p>
        <table class="hp-ints">
          <caption class="sr">
            What each integration has recorded
          </caption>
          <thead>
            <tr>
              <th scope="col">Integration</th>
              <th scope="col" class="n">Series</th>
              <th scope="col" class="n">Size</th>
              <th scope="col">Record</th>
              <th scope="col"><span class="sr">Clear</span></th>
            </tr>
          </thead>
          <tbody>
            <template v-for="row in byIntegration" :key="row.id">
              <tr>
                <th scope="row">{{ row.name }}</th>
                <td class="n">{{ fmtInt(row.series) }}</td>
                <td class="n">{{ row.bytes ? size(row.bytes) : '—' }}</td>
                <td><input v-model="recorded[row.id]" type="checkbox" :aria-label="`Record ${row.name}’s metrics`" :disabled="excluding" /></td>
                <td class="n">
                  <button v-if="row.series" type="button" class="btn sm ghost" :aria-label="`Clear ${row.name}’s history…`" @click="confirming = row.id">Clear…</button>
                </td>
              </tr>
              <tr v-if="confirming === row.id">
                <td colspan="5">
                  <div class="st-confirm" role="alertdialog" :aria-labelledby="`hp-clear-${row.id}`">
                    <span :id="`hp-clear-${row.id}`">Delete the {{ fmtInt(row.series) }} series {{ row.name }} recorded? Its health history stays. This can’t be undone.</span>
                    <button type="button" class="btn danger solid" :disabled="clearing" :aria-busy="clearing || undefined" @click="clearOne(row.id, row.name)">
                      Clear<span class="tier">T3</span>
                    </button>
                    <button type="button" class="btn" @click="confirming = ''">Cancel</button>
                  </div>
                  <p v-if="clearError" class="err" role="alert">{{ clearError }}</p>
                </td>
              </tr>
            </template>
          </tbody>
        </table>
        <div class="st-acts">
          <button
            type="button"
            class="btn sm"
            aria-label="Save what’s recorded"
            :aria-disabled="!excludeDirty || excluding ? 'true' : undefined"
            :aria-busy="excluding || undefined"
            @click="saveExcluded"
          >
            Save
          </button>
        </div>
        <p v-if="excludeError" class="err" role="alert">{{ excludeError }}</p>
      </section>

      <section v-if="!view.locked" class="st-section" aria-labelledby="hp-key-h">
        <header><h3 id="hp-key-h">By metric</h3></header>
        <p id="hp-key-d" class="st-lede">
          Leave out single metrics by their key, such as <code>docker:plex#net_rx</code>. <code>*</code> stands for anything, so <code>docker:*#net_rx</code> is every
          container’s download rate. What they recorded stays until it ages out.
        </p>
        <ul v-if="view.exclude_keys.length" class="hp-keys">
          <li v-for="k in view.exclude_keys" :key="k.pattern">
            <code>{{ k.pattern }}</code>
            <span class="hp-keys-n">{{ k.series === 1 ? '1 series' : `${fmtInt(k.series)} series` }}</span>
            <button type="button" class="btn sm ghost" :aria-label="`Record ${k.pattern} again`" :disabled="patterning" @click="removePattern(k.pattern)">Remove</button>
          </li>
        </ul>
        <form class="hp-limit" @submit.prevent="addPattern">
          <label for="hp-key">Leave out</label>
          <input id="hp-key" v-model="pattern" type="text" spellcheck="false" autocomplete="off" maxlength="200" placeholder="docker:*#net_rx" aria-describedby="hp-key-d" />
          <button type="submit" class="btn sm" :aria-disabled="!pattern.trim() || patterning ? 'true' : undefined" :aria-busy="patterning || undefined">Add</button>
        </form>
        <p v-if="patternError" class="err" role="alert">{{ patternError }}</p>
      </section>
    </template>
  </div>
</template>

<style>
.hp-limit {
  display: flex;
  flex-wrap: wrap;
  gap: var(--space-2);
  align-items: center;
  margin: 0 0 var(--space-3);
  font-size: var(--type-small);
}
.hp-limit-d {
  flex-basis: 100%;
  margin: 0;
  color: var(--text-3);
}
:is(.hp-tiers, .hp-ints) {
  width: 100%;
  margin: 0 0 var(--space-3);
  border-collapse: collapse;
  font-size: var(--type-small);
}
:is(.hp-tiers, .hp-ints) th,
:is(.hp-tiers, .hp-ints) td {
  padding: 6px var(--space-2) 6px 0;
  border-bottom: var(--hairline) solid var(--line);
  text-align: left;
  font-weight: normal;
}
:is(.hp-tiers, .hp-ints) thead th {
  color: var(--text-3);
}
.hp-keys {
  margin: 0 0 var(--space-3);
  padding: 0;
  list-style: none;
  font-size: var(--type-small);
}
.hp-keys li {
  display: flex;
  gap: var(--space-2);
  align-items: center;
  padding: 4px 0;
  border-bottom: var(--hairline) solid var(--line);
}
.hp-keys code {
  overflow-wrap: anywhere;
}
.hp-keys-n {
  margin-left: auto;
  color: var(--text-3);
  white-space: nowrap;
}
:is(.hp-tiers, .hp-ints) .n {
  text-align: right;
  font-variant-numeric: tabular-nums;
}
</style>
