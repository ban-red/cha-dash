<script setup lang="ts">
/**
 * Home Assistant's panel: the lists behind the tile's facets, over the board
 * (`ui.state.homePanel`). A native modal <dialog> with WAI-ARIA tabs, in the
 * order People, Needs a look, Lights, Switches, Climate, Playing, each only
 * when there's something in it; it opens on the tab the tile asked for.
 *
 * Every light and switch (on first), each thermostat, each media player,
 * each person, everything that needs a look. When the instance's `control`
 * allows a kind and the viewer's role may run it (model/roles.ts), its rows
 * get controls that run the hub's actions through the safety ladder
 * (`ui.runAction`; T1, with Undo): a switch per light or switch, Play or
 * Pause per player, and − and + on a thermostat's target (sent once the
 * clicks settle). Without it, admins get a line that says where to allow it.
 *
 * Home Assistant is read once a minute, so a change shows as asked for
 * (`pending`) until the next read agrees, the task fails, or 90 s pass.
 * Controls are off while the hub is unreachable. Its own chunk, loaded the
 * first time the panel opens.
 */
import { computed, nextTick, onBeforeUnmount, onMounted, reactive, ref, useId, useTemplateRef, watch } from 'vue';
import '../styles/act-dialog.css';
import Glyph from '../components/marks/Glyph.vue';
import { useModal } from '../board/useModal';
import { fmtInt } from '../lib/format';
import { attr, resolve, series } from '../model/graph';
import { useLive } from '../stores/live';
import { useUi } from '../stores/ui';
import MediaMark from './parts/MediaMark.vue';
import { ACT, GLYPH_LABEL, controlOf, deg, devicesOf, householdMay, lookOf, personsOf, roomLine, roomsOf, str, type ControlDomain, type Device, type PanelTab, type Room } from './homeassistant';

const ui = useUi();
const live = useLive();
const uid = useId();
const dlg = useTemplateRef<HTMLDialogElement>('dlg');
useModal(dlg);

const app = computed(() => (ui.state.homePanel ? resolve(live.graph.value, ui.state.homePanel.app) : undefined));
const name = computed(() => app.value?.name || 'Home Assistant');
const count = (k: string) => series(live.metrics.value, app.value?.id ?? '', k)?.value ?? 0;
const tempUnit = computed(() => str(attr<unknown>(app.value, 'temperature_unit')) || '°');

const persons = computed(() => personsOf(app.value, live.now.value));
const look = computed(() => lookOf(app.value, count('locks_unlocked')));
const lights = computed(() => devicesOf(app.value, 'light_items'));
const switches = computed(() => devicesOf(app.value, 'switch_items'));
const players = computed(() => devicesOf(app.value, 'player_items'));
const rooms = computed(() => roomsOf(app.value));

const TABS: { id: PanelTab; label: string }[] = [
  { id: 'people', label: 'People' },
  { id: 'look', label: 'Needs a look' },
  { id: 'lights', label: 'Lights' },
  { id: 'switches', label: 'Switches' },
  { id: 'climate', label: 'Climate' },
  { id: 'media', label: 'Playing' },
];
const tabs = computed(() =>
  TABS.filter((t) => {
    switch (t.id) {
      case 'people':
        return persons.value.length > 0;
      case 'look':
        return look.value.length > 0;
      case 'lights':
        return lights.value.length > 0;
      case 'switches':
        return switches.value.length > 0;
      case 'climate':
        return rooms.value.length > 0;
      case 'media':
        return players.value.length > 0;
    }
    return false;
  }),
);
const tab = ref<PanelTab>((ui.state.homePanel?.tab as PanelTab) ?? 'people');
/** The tab shown: the one asked for while it has something, else the first. */
const current = computed<PanelTab | undefined>(() => (tabs.value.some((t) => t.id === tab.value) ? tab.value : tabs.value[0]?.id));
watch(
  () => ui.state.homePanel?.tab,
  (t) => {
    if (t) tab.value = t as PanelTab;
  },
);

const tabEls = ref<HTMLButtonElement[]>([]);
function onTabKey(e: KeyboardEvent, i: number): void {
  const n = tabs.value.length;
  const to = e.key === 'ArrowRight' ? (i + 1) % n : e.key === 'ArrowLeft' ? (i - 1 + n) % n : e.key === 'Home' ? 0 : e.key === 'End' ? n - 1 : -1;
  if (to < 0) return;
  e.preventDefault();
  tab.value = tabs.value[to]!.id;
  void nextTick(() => tabEls.value[to]?.focus());
}
onMounted(() => {
  const i = tabs.value.findIndex((t) => t.id === current.value);
  tabEls.value[Math.max(0, i)]?.focus();
});

// Filtering the long lists.
const FILTER_FROM = 12;
const query = ref('');
watch(current, () => (query.value = ''));
const shown = (list: Device[]) => {
  const q = query.value.trim().toLowerCase();
  return q ? list.filter((d) => d.name.toLowerCase().includes(q) || d.entity.includes(q)) : list;
};
const onCount = (list: Device[]) => list.filter((d) => isOn(d)).length;
/** The Lights or Switches tab's list. */
const kind = computed(() => (current.value === 'switches' ? { list: switches.value, domain: 'switch' as const, noun: 'switches' } : { list: lights.value, domain: 'light' as const, noun: 'lights' }));

// Control.
const control = computed(() => controlOf(app.value));
const canAct = (domain: ControlDomain) => control.value.has(domain) && ui.can.value.act('T1', householdMay(control.value, domain));
const offline = computed(() => live.stale.value || ui.state.editing);
/** Where to allow it: admins only (it's the integration's settings). */
const instance = computed(() => (app.value?.source ?? '').replace(/^integration:/, ''));
const showAllow = (domain: ControlDomain) => !control.value.has(domain) && ui.can.value.integrations && instance.value !== '' && !instance.value.includes(':');

/** What was asked for, until Home Assistant's next read agrees (or the task fails, or 90 s pass). */
interface Pending {
  state: string;
  temp?: number;
  at: number;
}
const pending = reactive<Record<string, Pending>>({});
const PENDING_MS = 90_000;
/** The latest task for an entity: its plan's label names it ("Turn on light.kitchen"). */
function taskOf(entity: string) {
  let latest;
  for (const t of Object.values(ui.tasks.value)) {
    if (t.target !== app.value?.id || !t.label.split(' ').includes(entity)) continue;
    if (!latest || (t.started_at ?? '') > (latest.started_at ?? '')) latest = t;
  }
  return latest;
}
function pendingOf(entity: string): Pending | undefined {
  const p = pending[entity];
  if (!p || live.now.value - p.at > PENDING_MS) return undefined;
  const t = taskOf(entity);
  if (t && (t.state === 'failed' || t.state === 'cancelled')) return undefined;
  return p;
}
const stateOf = (d: Device) => pendingOf(d.entity)?.state ?? d.state;
const isOn = (d: Device) => stateOf(d) === 'on';
const working = (entity: string) => {
  const t = taskOf(entity);
  return Boolean(t && !['succeeded', 'failed', 'cancelled'].includes(t.state));
};
// Drop what was asked for once the read agrees.
watch([lights, switches, players, rooms], () => {
  const all = new Map<string, { state: string; temp?: number | undefined }>();
  for (const d of [...lights.value, ...switches.value, ...players.value]) all.set(d.entity, { state: d.state });
  for (const r of rooms.value) all.set(r.entity, { state: r.mode, temp: r.target });
  for (const [k, p] of Object.entries(pending)) {
    const now = all.get(k);
    if (now && (p.temp !== undefined ? now.temp === p.temp : now.state === p.state)) delete pending[k];
  }
});

function toggle(d: Device, domain: ControlDomain): void {
  if (offline.value || working(d.entity) || !canAct(domain)) return;
  const on = isOn(d);
  pending[d.entity] = { state: on ? 'off' : 'on', at: live.now.value };
  void ui.runAction({
    action: on ? ACT.off : ACT.on,
    target: app.value!.id,
    params: { entity: d.entity },
    label: `Turn ${on ? 'off' : 'on'} ${d.name}`,
    tier: 'T1',
  });
}
function playPause(d: Device): void {
  if (offline.value || working(d.entity) || !canAct('media_player')) return;
  const playing = stateOf(d) === 'playing';
  pending[d.entity] = {
    state: playing ? 'paused' : 'playing',
    at: live.now.value,
  };
  void ui.runAction({
    action: playing ? ACT.pause : ACT.play,
    target: app.value!.id,
    params: { entity: d.entity },
    label: `${playing ? 'Pause' : 'Play'} ${d.name}`,
    tier: 'T1',
  });
}

/** A thermostat's target as shown: what was asked for, else what it reads. */
const targetOf = (r: Room) => pendingOf(r.entity)?.temp ?? r.target;
const stepOf = (r: Room) => r.step ?? (tempUnit.value === '°F' ? 1 : 0.5);
const canSet = (r: Room) => Boolean(r.entity) && r.target !== undefined && r.mode !== 'off' && canAct('climate');
/** Clicks on − and + settle for a moment before the one action goes out. */
const SETTLE_MS = 800;
const timers = new Map<string, ReturnType<typeof setTimeout>>();
function nudge(r: Room, dir: 1 | -1): void {
  if (offline.value || working(r.entity) || !canSet(r)) return;
  const step = stepOf(r);
  let t = Math.round(((targetOf(r) ?? r.current) + dir * step) / step) * step;
  if (r.min !== undefined) t = Math.max(r.min, t);
  if (r.max !== undefined) t = Math.min(r.max, t);
  t = Math.round(t * 10) / 10;
  pending[r.entity] = { state: r.mode, temp: t, at: live.now.value };
  clearTimeout(timers.get(r.entity));
  timers.set(
    r.entity,
    setTimeout(() => {
      timers.delete(r.entity);
      if (!app.value) return;
      void ui.runAction({
        action: ACT.climate,
        target: app.value.id,
        params: { entity: r.entity, temperature: t },
        label: `Set ${r.name} to ${deg(t)}`,
        tier: 'T1',
      });
    }, SETTLE_MS),
  );
}
onBeforeUnmount(() => {
  // Send what was set before the panel closed.
  for (const [entity, h] of timers) {
    clearTimeout(h);
    const r = rooms.value.find((x) => x.entity === entity);
    const t = pending[entity]?.temp;
    if (r && t !== undefined && app.value)
      void ui.runAction({
        action: ACT.climate,
        target: app.value.id,
        params: { entity, temperature: t },
        label: `Set ${r.name} to ${deg(t)}`,
        tier: 'T1',
      });
  }
  timers.clear();
});
const atMin = (r: Room) => r.min !== undefined && (targetOf(r) ?? r.current) <= r.min;
const atMax = (r: Room) => r.max !== undefined && (targetOf(r) ?? r.current) >= r.max;

const STATE_WORDS: Record<string, string> = {
  on: 'On',
  off: 'Off',
  unavailable: 'Unavailable',
  unknown: 'Unknown',
  playing: 'Playing',
  paused: 'Paused',
  idle: 'Idle',
  standby: 'Standby',
  buffering: 'Buffering',
};
const stateWord = (s: string) => STATE_WORDS[s] ?? (s ? s[0]!.toUpperCase() + s.slice(1).replace(/_/g, ' ') : '');
const trackLine = (d: Device) => [d.title, d.artist].filter(Boolean).join(' · ');
const canPlayPause = (d: Device) => ['playing', 'paused'].includes(stateOf(d)) && canAct('media_player');

function close(): void {
  ui.closeHomePanel();
}
/** The connection's settings, at its `control` field. */
function openSettings(): void {
  const id = instance.value;
  close();
  ui.openSettings('integrations', id, { field: 'control' });
}
</script>

<template>
  <dialog ref="dlg" class="act-dlg ha-dlg" :aria-labelledby="`${uid}-h`" @cancel.prevent="close" @keydown.esc.stop>
    <div class="act-dlg-body">
      <header class="act-dlg-head">
        <h2 :id="`${uid}-h`">{{ name }}</h2>
        <button type="button" class="x" aria-label="Close" @click="close">
          <svg viewBox="0 0 10 10" aria-hidden="true"><path d="m2.5 2.5 5 5m0-5-5 5" fill="none" stroke="currentColor" stroke-width="1.5" stroke-linecap="round" /></svg>
        </button>
      </header>
      <p v-if="!app" class="ha-none">Home Assistant isn't connected any more.</p>
      <p v-else-if="!tabs.length" class="ha-none">Nothing to list yet. Home Assistant is read once a minute.</p>

      <template v-else>
        <div class="ha-tabs" role="tablist" :aria-label="name">
          <button
            v-for="(t, i) in tabs"
            :id="`${uid}-t-${t.id}`"
            :key="t.id"
            ref="tabEls"
            type="button"
            role="tab"
            class="ha-tab"
            :aria-selected="current === t.id"
            :aria-controls="`${uid}-p`"
            :tabindex="current === t.id ? 0 : -1"
            @click="tab = t.id"
            @keydown="onTabKey($event, i)"
          >
            {{ t.label }}
          </button>
        </div>

        <div :id="`${uid}-p`" class="ha-pane" role="tabpanel" :aria-labelledby="`${uid}-t-${current}`" tabindex="0">
          <template v-if="current === 'people'">
            <ul class="ha-rows">
              <li v-for="p in persons" :key="p.name" class="ha-row">
                <span :class="['ha-face', { home: p.home }]" aria-hidden="true">{{ p.initial }}</span>
                <span class="ha-main">{{ p.name }}<small>{{ p.since }}</small></span>
                <span :class="['ha-where', { home: p.home }]">{{ p.where }}</span>
              </li>
            </ul>
          </template>

          <template v-else-if="current === 'look'">
            <ul class="ha-rows">
              <li v-for="l in look" :key="l.key" class="ha-row">
                <Glyph :kind="l.glyph" :label="GLYPH_LABEL[l.glyph] ?? 'needs a look'" />
                <span class="ha-main">{{ l.name }}</span>
                <span class="ha-side">{{ l.detail }}</span>
              </li>
            </ul>
            <p class="ha-hint">Lists show up to eight of each. Locks, doors and the alarm are changed in Home Assistant.</p>
          </template>

          <template v-else-if="current === 'lights' || current === 'switches'">
            <div class="ha-sum">
              <span>{{ fmtInt(onCount(kind.list)) }} of {{ fmtInt(kind.list.length) }} on</span>
              <input v-if="kind.list.length >= FILTER_FROM" v-model="query" type="search" class="ha-filter" :placeholder="`Find ${kind.noun}`" :aria-label="`Find ${kind.noun}`" />
            </div>
            <ul class="ha-rows">
              <li v-for="d in shown(kind.list)" :key="d.entity" :class="['ha-row', { off: !isOn(d), gone: stateOf(d) === 'unavailable' }]">
                <span :class="['ha-dot', { on: isOn(d) }]" aria-hidden="true" />
                <span class="ha-main">{{ d.name }}<small v-if="isOn(d) && d.brightness !== undefined && !pendingOf(d.entity)">{{ Math.round(d.brightness) }}%</small></span>
                <button
                  v-if="canAct(kind.domain) && (stateOf(d) === 'on' || stateOf(d) === 'off')"
                  type="button"
                  role="switch"
                  class="ha-switch"
                  :aria-checked="isOn(d)"
                  :aria-label="d.name"
                  :aria-busy="working(d.entity) || undefined"
                  :disabled="offline || working(d.entity)"
                  @click="toggle(d, kind.domain)"
                >
                  <span class="ha-knob" />
                </button>
                <span v-else class="ha-side">{{ stateWord(stateOf(d)) }}</span>
              </li>
            </ul>
            <p v-if="query && !shown(kind.list).length" class="ha-none">No {{ kind.noun }} match “{{ query }}”.</p>
            <p v-if="showAllow(kind.domain)" class="ha-hint">
              To switch {{ kind.noun }} from here, allow them in this connection's settings.
              <button type="button" class="btn ghost sm" @click="openSettings">Open its settings</button>
            </p>
          </template>

          <template v-else-if="current === 'climate'">
            <ul class="ha-rows">
              <li v-for="r in rooms" :key="r.entity || r.name" class="ha-row ha-room">
                <span class="ha-main">{{ r.name }}<small>{{ roomLine(r) }}</small></span>
                <span class="ha-temp">{{ r.current.toFixed(1) }}<span class="u">{{ tempUnit }}</span></span>
                <span v-if="canSet(r)" class="ha-step" role="group" :aria-label="`${r.name} target`">
                  <button type="button" :aria-label="`Lower ${r.name}'s target`" :disabled="offline || working(r.entity) || atMin(r)" @click="nudge(r, -1)">−</button>
                  <output :aria-live="'polite'" :class="{ asked: pendingOf(r.entity) }">{{ targetOf(r)?.toFixed(1) }}<span class="u">{{ tempUnit }}</span></output>
                  <button type="button" :aria-label="`Raise ${r.name}'s target`" :disabled="offline || working(r.entity) || atMax(r)" @click="nudge(r, 1)">+</button>
                </span>
              </li>
            </ul>
            <p v-if="showAllow('climate')" class="ha-hint">
              To set thermostats from here, allow them in this connection's settings.
              <button type="button" class="btn ghost sm" @click="openSettings">Open its settings</button>
            </p>
          </template>

          <template v-else-if="current === 'media'">
            <ul class="ha-rows">
              <li v-for="d in players" :key="d.entity" :class="['ha-row', { off: !['playing', 'paused'].includes(stateOf(d)) }]">
                <MediaMark :kind="d.artist ? 'music' : 'tv'" />
                <span class="ha-main">{{ d.name }}<small>{{ trackLine(d) || stateWord(stateOf(d)) }}</small></span>
                <button
                  v-if="canPlayPause(d)"
                  type="button"
                  class="btn sm ha-play"
                  :aria-label="`${stateOf(d) === 'playing' ? 'Pause' : 'Play'} ${d.name}`"
                  :disabled="offline || working(d.entity)"
                  @click="playPause(d)"
                >
                  {{ stateOf(d) === 'playing' ? 'Pause' : 'Play' }}
                </button>
                <span v-else class="ha-side">{{ trackLine(d) ? stateWord(stateOf(d)) : '' }}</span>
              </li>
            </ul>
            <p v-if="showAllow('media_player')" class="ha-hint">
              To play and pause from here, allow media players in this connection's settings.
              <button type="button" class="btn ghost sm" @click="openSettings">Open its settings</button>
            </p>
          </template>
        </div>
        <p v-if="offline && control.size" class="ha-hint">
          {{ ui.state.editing ? 'Controls are off while editing the board.' : 'Controls are off while the hub is unreachable.' }}
        </p>
      </template>
    </div>
  </dialog>
</template>

<style>
.ha-dlg .act-dlg-body {
  gap: var(--space-3);
}
.ha-hint,
.ha-none {
  margin: 0;
  font-size: var(--type-small);
  line-height: var(--leading-relaxed);
  color: var(--text-2);
}
.ha-hint .btn {
  margin-left: var(--space-1);
}
.ha-tabs {
  display: flex;
  gap: var(--space-1);
  overflow-x: auto;
  border-bottom: var(--hairline) solid var(--line);
}
.ha-tab {
  flex: none;
  min-height: var(--target-min);
  padding: 0 var(--space-3);
  border: 0;
  border-bottom: 2px solid transparent;
  background: none;
  color: var(--text-2);
  font: var(--weight-medium) var(--type-small) / 1 var(--font-ui);
  cursor: pointer;
}
.ha-tab[aria-selected='true'] {
  border-bottom-color: var(--text);
  color: var(--text);
}
.ha-tab:focus-visible,
.ha-pane:focus-visible,
.ha-switch:focus-visible,
.ha-step button:focus-visible {
  outline: 2px solid var(--focus);
  outline-offset: 2px;
}
.ha-pane {
  display: flex;
  flex-direction: column;
  gap: var(--space-3);
  min-height: 120px;
  border-radius: var(--radius-sm);
}
.ha-sum {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: var(--space-3);
  font-size: var(--type-small);
  color: var(--text-2);
}
.ha-filter {
  flex: 0 1 220px;
  min-width: 0;
  height: 32px;
  padding: 0 var(--space-2);
  border: var(--hairline) solid var(--line-2);
  border-radius: var(--radius-md);
  background: var(--surface);
  color: var(--text);
  font: inherit;
}
.ha-rows {
  display: flex;
  flex-direction: column;
  margin: 0;
  padding: 0;
  list-style: none;
}
.ha-row {
  display: grid;
  grid-template-columns: auto minmax(0, 1fr) auto;
  align-items: center;
  gap: var(--space-3);
  min-height: var(--target-min);
  padding: var(--space-1) 0;
  border-top: var(--hairline) solid var(--line);
}
.ha-row:first-child {
  border-top: 0;
}
.ha-row.ha-room {
  grid-template-columns: minmax(0, 1fr) auto auto;
}
.ha-main {
  display: flex;
  flex-direction: column;
  gap: 2px;
  min-width: 0;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
  font-size: var(--type-body);
}
.ha-main small {
  overflow: hidden;
  text-overflow: ellipsis;
  font-size: var(--type-caption);
  color: var(--text-3);
}
.ha-row.off .ha-main {
  color: var(--text-2);
}
.ha-row.gone .ha-main {
  color: var(--text-3);
}
.ha-side {
  font-size: var(--type-caption);
  color: var(--text-3);
  white-space: nowrap;
}
.ha-dot {
  width: 8px;
  height: 8px;
  border-radius: var(--radius-full);
  border: 1.5px solid var(--line-2);
}
.ha-dot.on {
  border-color: var(--text);
  background: var(--text);
}
.ha-dlg .ha-face {
  width: 24px;
  height: 24px;
  margin: 0;
  box-shadow: none;
  font-size: 10px;
}
/* A switch: 44 px of target around a 36 × 20 track. */
.ha-switch {
  display: grid;
  place-items: center start;
  width: 44px;
  height: var(--target-min);
  padding: 0 4px;
  border: 0;
  background: none;
  cursor: pointer;
}
.ha-switch::before {
  content: '';
  grid-area: 1 / 1;
  width: 36px;
  height: 20px;
  border-radius: var(--radius-full);
  border: var(--hairline) solid var(--line-2);
  background: var(--surface-2, var(--surface));
}
.ha-knob {
  grid-area: 1 / 1;
  width: 14px;
  height: 14px;
  margin-left: 3px;
  border-radius: var(--radius-full);
  background: var(--text-3);
  transition: transform var(--dur-fast, 120ms) var(--ease-out, ease-out);
}
.ha-switch[aria-checked='true']::before {
  border-color: var(--text);
  background: var(--text);
}
.ha-switch[aria-checked='true'] .ha-knob {
  transform: translateX(16px);
  background: var(--surface);
}
.ha-switch:disabled {
  cursor: default;
  opacity: 0.55;
}
@media (prefers-reduced-motion: reduce) {
  .ha-knob {
    transition: none;
  }
}
.ha-step {
  display: flex;
  align-items: center;
  gap: 2px;
}
.ha-step button {
  width: 36px;
  height: 36px;
  padding: 0;
  border: var(--hairline) solid var(--line-2);
  border-radius: var(--radius-md);
  background: none;
  color: var(--text);
  font: var(--weight-medium) var(--type-body) / 1 var(--font-ui);
  cursor: pointer;
}
.ha-step button:disabled {
  cursor: default;
  opacity: 0.45;
}
.ha-step output {
  min-width: 52px;
  text-align: center;
  font: var(--weight-medium) var(--type-body) / 1 var(--font-num);
  font-variant-numeric: tabular-nums;
}
.ha-step output.asked {
  color: var(--text-2);
}
.ha-step .u,
.ha-dlg .ha-temp .u {
  margin-left: 1px;
  font-size: var(--type-caption);
  color: var(--text-3);
}
@media (pointer: coarse) {
  .ha-step button {
    width: var(--target-min);
    height: var(--target-min);
  }
  .ha-filter {
    height: var(--target-min);
    font-size: 16px;
  }
}
</style>
