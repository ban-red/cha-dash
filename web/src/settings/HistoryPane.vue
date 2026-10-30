<script setup lang="ts">
/**
 * Settings → History: how much metrics and health history the hub keeps in
 * history.db (ADR-0031), what it holds now, and clearing it.
 *
 *   GET  /api/settings/history         {level, locked, demo, series, resources, file_bytes, tiers, estimates}
 *   PUT  /api/settings/history         {level} → the same
 *   POST /api/settings/history/clear   → the same
 *
 * The levels are a native radio group with Save, like the security level,
 * so arrowing through them never saves. Each says about how big history.db
 * grows at it, for the series recorded so far. Clearing asks first, inline.
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
import { errorWords } from '../stores/integrations';
import { useUi } from '../stores/ui';

interface TierView {
  tier: string;
  step_ms: number;
  keep_ms: number;
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
  tiers: TierView[];
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
const confirming = ref(false);
const clearing = ref(false);
const clearError = ref('');

const LEVELS: { id: HistoryLevel; label: string; description: string }[] = [
  { id: 'standard', label: 'Standard', description: 'By the minute for 48 hours and by the quarter hour for 14 days, with health for 90 days.' },
  { id: 'long-term', label: 'Long-term', description: 'Also by the hour for a year and by the day forever, with health for two years.' },
  { id: 'off', label: 'Off', description: 'Nothing new is kept. What’s on disk stays until it ages out, and sparklines start empty after a restart.' },
];

/** "840 kB", "31 MB", "1.2 GB". */
function size(bytes: number): string {
  if (bytes < 1e6) return `${Math.max(1, Math.round(bytes / 1e3))} kB`;
  if (bytes < 1e9) return `${Math.round(bytes / 1e6)} MB`;
  return `${(bytes / 1e9).toFixed(1)} GB`;
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
const tiersShown = computed(() => (view.value?.tiers ?? []).filter((t) => t.chunks > 0 || view.value?.level === 'long-term' || t.tier === '1m' || t.tier === '15m'));

async function load(): Promise<void> {
  try {
    view.value = await api<HistoryView>('/api/settings/history');
    choice.value = view.value.level;
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

async function clear(): Promise<void> {
  clearing.value = true;
  clearError.value = '';
  try {
    view.value = await api<HistoryView>('/api/settings/history/clear', { method: 'POST' });
    confirming.value = false;
    ui.toast('History cleared. Recording carries on from now.');
  } catch (err) {
    clearError.value = errorWords(err, 'The history wasn’t cleared.');
  } finally {
    clearing.value = false;
  }
}

onMounted(load);
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
              <td>{{ keepWords(t.keep_ms) }}</td>
              <td>{{ t.oldest === null ? '—' : shortDate(t.oldest) }}</td>
              <td class="n">{{ t.chunks ? size(t.bytes) : '—' }}</td>
            </tr>
          </tbody>
        </table>
        <div v-if="!confirming" class="st-acts">
          <button type="button" class="btn sm ghost" :disabled="view.series === 0 && view.resources === 0" @click="confirming = true">Clear history…</button>
        </div>
        <div v-else class="st-confirm" role="alertdialog" aria-labelledby="hp-clear-q">
          <span id="hp-clear-q">Delete every point and health change in <code>history.db</code>? Charts start over from now. This can’t be undone.</span>
          <button type="button" class="btn danger solid" :disabled="clearing" :aria-busy="clearing || undefined" @click="clear">Clear history<span class="tier">T3</span></button>
          <button type="button" class="btn" @click="confirming = false">Cancel</button>
        </div>
        <p v-if="clearError" class="err" role="alert">{{ clearError }}</p>
      </section>
    </template>
  </div>
</template>

<style>
.hp-tiers {
  width: 100%;
  margin: 0 0 var(--space-3);
  border-collapse: collapse;
  font-size: var(--type-small);
}
.hp-tiers th,
.hp-tiers td {
  padding: 6px var(--space-2) 6px 0;
  border-bottom: var(--hairline) solid var(--line);
  text-align: left;
  font-weight: normal;
}
.hp-tiers thead th {
  color: var(--text-3);
}
.hp-tiers .n {
  text-align: right;
  font-variant-numeric: tabular-nums;
}
</style>
