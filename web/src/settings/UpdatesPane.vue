<script setup lang="ts">
/**
 * Settings → Updates (the Update Center): which containers have a newer
 * image, by host, with PVE hosts' pending packages and Unraid's version in
 * the same list. Shapes and routes are in ./updates.ts.
 *
 * Docker hosts through chadash-node are checked: the node asks each
 * registry at most every 6 hours. Update runs the action the hub names for
 * the row (docker.container.update: pull, recreate, the health gate, and a
 * rollback), through the usual T2 hold-to-confirm. Update all, or a
 * selection, is one rollout (updates.rollout): dependencies first, a
 * snapshot first where one is possible, stopping at the first update that
 * fails. It can be scheduled into a maintenance window instead. Hold keeps a
 * container on its image until it's released; Snooze hides an update for 7
 * days or until a newer one appears. What can't be checked says why, folded
 * away. Each row has a risk from its version numbers; with release notes on
 * (an admin's setting), a row whose image names its GitHub repository shows
 * the releases since the version it runs.
 *
 * Its own chunk, loaded with the pane.
 */
import { computed, onBeforeUnmount, onMounted, ref } from 'vue';
import { ApiError, api } from '../lib/api';
import { hubWords } from '../lib/actions';
import { ago } from '../lib/format';
import { useUi } from '../stores/ui';
import type { Rollout } from '../model/types';
import Glyph from '../ui/marks/Glyph.vue';
import UiButton from '../ui/Button.vue';
import UiField from '../ui/Field.vue';
import ReleaseNotes from './ReleaseNotes.vue';
import SettingsHelp from './SettingsHelp.vue';
import UpdateRollouts from './UpdateRollouts.vue';
import UpdateWindows from './UpdateWindows.vue';
import {
  ROLLOUT_ACTION,
  ROLLOUT_TARGET,
  kindOf,
  riskWords,
  rolledBackWords,
  selectable,
  shortDigest,
  type RolloutPlan,
  type UpdateRow,
  type WindowView,
} from './updates';

const ui = useUi();
const rows = ref<UpdateRow[]>([]);
const status = ref<'loading' | 'ready' | 'error'>('loading');
const loadError = ref('');
const checking = ref(false);
const busy = ref<Record<string, boolean>>({});
const now = ref(Date.now());

const canUpdate = computed(() => ui.can.value.act('T2'));
const canHold = computed(() => ui.can.value.act('T1'));
const isAdmin = computed(() => ui.can.value.role === 'owner' || ui.can.value.role === 'admin');

async function load(): Promise<void> {
  try {
    const res = await api<{ updates?: UpdateRow[] }>('/api/updates');
    rows.value = res.updates ?? [];
    status.value = 'ready';
    const keep = new Set(rows.value.filter(selectable).map((r) => r.resource));
    selected.value = selected.value.filter((id) => keep.has(id));
  } catch (err) {
    loadError.value = err instanceof ApiError ? err.message : 'Couldn’t load the updates.';
    status.value = 'error';
  }
  now.value = Date.now();
}

// ── settings, rollouts, windows ──────────────────────────────────────────

const notesOn = ref(false);
const savingNotes = ref(false);
async function loadSettings(): Promise<void> {
  try {
    notesOn.value = (await api<{ release_notes: boolean }>('/api/updates/settings')).release_notes;
  } catch {
    notesOn.value = false; // a hub before 0.4, or a hiccup: no notes
  }
}
async function setNotes(on: boolean): Promise<void> {
  savingNotes.value = true;
  try {
    notesOn.value = (await api<{ release_notes: boolean }>('/api/updates/settings', { method: 'PUT', body: { release_notes: on } })).release_notes;
    ui.toast(on ? 'Release notes are on. The hub reads them from GitHub.' : 'Release notes are off.');
  } catch (err) {
    ui.toast(hubWords(err, 'The setting wasn’t saved.'), 'error');
  } finally {
    savingNotes.value = false;
  }
}

const rollouts = ref<Rollout[]>([]);
const rolloutStatus = ref<'loading' | 'ready' | 'error'>('loading');
const rolloutError = ref('');
async function loadRollouts(): Promise<void> {
  try {
    rollouts.value = (await api<{ rollouts?: Rollout[] }>('/api/updates/rollouts')).rollouts ?? [];
    rolloutStatus.value = 'ready';
  } catch (err) {
    rolloutError.value = hubWords(err, 'Couldn’t load the rollouts.');
    rolloutStatus.value = 'error';
  }
}

const windows = ref<WindowView[]>([]);
const windowStatus = ref<'loading' | 'ready' | 'error'>('loading');
const windowError = ref('');
async function loadWindows(): Promise<void> {
  try {
    windows.value = (await api<{ windows?: WindowView[] }>('/api/windows')).windows ?? [];
    windowStatus.value = 'ready';
  } catch (err) {
    windowError.value = hubWords(err, 'Couldn’t load the maintenance windows.');
    windowStatus.value = 'error';
  }
}
const upcoming = computed(() => windows.value.filter((w) => w.open || w.next_open));

const running = computed(() => rollouts.value.some((r) => r.state === 'running'));
let timer: ReturnType<typeof setTimeout> | undefined;
function schedule(): void {
  clearTimeout(timer);
  timer = setTimeout(() => void Promise.all([load(), loadRollouts()]).then(schedule), running.value ? 5_000 : 30_000);
}
onMounted(() => void Promise.all([load(), loadSettings(), loadRollouts(), loadWindows()]).then(schedule));
onBeforeUnmount(() => clearTimeout(timer));

// ── the list ─────────────────────────────────────────────────────────────

/** Rows to act on (available, held or rolled back), by host. */
const groups = computed(() => {
  const by = new Map<string, UpdateRow[]>();
  for (const r of rows.value) {
    if (r.state !== 'available' && r.state !== 'held' && r.state !== 'rolled_back') continue;
    const k = r.host || r.engine;
    by.set(k, [...(by.get(k) ?? []), r]);
  }
  return [...by.entries()].sort(([a], [b]) => a.localeCompare(b)).map(([host, list]) => ({ host, rows: list }));
});
const available = computed(() => rows.value.filter((r) => r.state === 'available').length);
const current = computed(() => rows.value.filter((r) => r.state === 'current').length);
const cantCheck = computed(() => rows.value.filter((r) => r.state === 'cant_check'));
const lastChecked = computed(() => {
  const t = rows.value.map((r) => (r.checked_at ? Date.parse(r.checked_at) : 0)).reduce((a, b) => Math.max(a, b), 0);
  return t ? ago(new Date(t).toISOString(), now.value) : '';
});

async function checkNow(): Promise<void> {
  checking.value = true;
  try {
    const res = await api<{ asked: number }>('/api/updates/check', { method: 'POST' });
    ui.toast(res.asked ? 'Checking now. Answers arrive within a minute.' : 'There’s no Docker host through a node to check.');
    setTimeout(() => void load(), 5000);
  } catch (err) {
    ui.toast(err instanceof ApiError ? err.message : 'Couldn’t start a check.', 'error');
  } finally {
    checking.value = false;
  }
}

function update(r: UpdateRow): void {
  if (!r.update) return;
  void ui.runAction({ action: r.update.id, target: r.update.target, label: `Update ${r.name}`, tier: 'T2' });
}

async function hold(r: UpdateRow, kind: 'hold' | 'snooze' | 'release'): Promise<void> {
  busy.value[r.resource] = true;
  try {
    const path = `/api/updates/${encodeURIComponent(r.resource)}/hold`;
    const next = await api<UpdateRow>(path, kind === 'release' ? { method: 'DELETE' } : { method: 'POST', body: { kind } });
    rows.value = rows.value.map((x) => (x.resource === r.resource ? next : x));
    selected.value = selected.value.filter((id) => id !== r.resource || selectable(next));
  } catch (err) {
    ui.toast(err instanceof ApiError ? err.message : 'That didn’t work.', 'error');
  } finally {
    busy.value[r.resource] = false;
  }
}

const short = shortDigest;

/** Try again: offer the digest an update rolled back from. */
async function tryAgain(r: UpdateRow): Promise<void> {
  busy.value[r.resource] = true;
  try {
    const next = await api<UpdateRow>(`/api/updates/${encodeURIComponent(r.resource)}/rejected`, { method: 'DELETE' });
    rows.value = rows.value.map((x) => (x.resource === r.resource ? next : x));
    ui.toast(`${r.name}’s update is offered again.`);
  } catch (err) {
    ui.toast(hubWords(err, 'That didn’t work.'), 'error');
  } finally {
    busy.value[r.resource] = false;
  }
}

function holdWords(r: UpdateRow): string {
  if (!r.hold) return '';
  const by = r.hold.created_by ? ` by ${r.hold.created_by}` : '';
  if (r.hold.kind === 'hold') return `Held${by} ${ago(r.hold.created_at, now.value)}, until released`;
  return `Snoozed${by} until ${r.hold.until ? new Date(r.hold.until).toLocaleDateString() : 'later'}, or a newer image`;
}

// ── rollouts ─────────────────────────────────────────────────────────────

const selected = ref<string[]>([]);
const updatable = computed(() => rows.value.filter(selectable));
const windowFor = ref('');
const planning = ref(false);

/** Update all (targets empty) or the selection, now or in a window. */
async function rollout(targets: string[]): Promise<void> {
  if (planning.value) return;
  planning.value = true;
  let plan: RolloutPlan;
  try {
    plan = await api<RolloutPlan>('/api/updates/rollouts/preview', { method: 'POST', body: { targets } });
  } catch (err) {
    ui.toast(hubWords(err, 'Couldn’t plan the rollout.'), 'error');
    return;
  } finally {
    planning.value = false;
  }
  const n = plan.steps.filter((s) => s.state !== 'skipped').length;
  if (!n) {
    ui.toast('There’s nothing to update: what you picked is held, snoozed or can’t be updated from here.', 'error');
    return;
  }
  const win = upcoming.value.find((w) => w.id === windowFor.value);
  const what = `${n} container${n === 1 ? '' : 's'}`;
  const params: Record<string, unknown> = { targets, hash: plan.hash };
  if (win) params.window = win.id;
  await ui.runAction({
    action: ROLLOUT_ACTION,
    target: ROLLOUT_TARGET,
    params,
    label: win ? `Schedule ${what} for ${win.name}` : `Update ${what}`,
    tier: plan.tier,
  });
  selected.value = [];
  void loadRollouts();
}

function toggleAll(e: Event): void {
  selected.value = (e.target as HTMLInputElement).checked ? updatable.value.map((r) => r.resource) : [];
}

// ── release notes ────────────────────────────────────────────────────────

const notesOpen = ref<Record<string, boolean>>({});
</script>

<template>
  <section class="st-section" aria-labelledby="up-h">
    <header>
      <h3 id="up-h">Updates</h3>
      <UiButton v-if="canHold" size="sm" :disabled="checking" @click="checkNow">Check now</UiButton>
    </header>
    <SettingsHelp summary="Containers whose image tag points at a newer image, and hosts with pending updates.">
      Cha Dash asks each Docker host’s node whether a container’s image tag has moved in its registry. The node asks each registry at most every 6 hours and
      changes nothing. Updating pulls the tag, recreates the container on it, waits for it to report healthy, and puts the previous image back on its own if
      it doesn’t. Update all runs them one at a time, dependencies first, with a snapshot first where one is possible, and stops at the first that fails.
      Proxmox hosts’ packages (when Cha Dash’s token may read them) and Unraid’s version are listed too; you update those in Proxmox or Unraid.
    </SettingsHelp>

    <div v-if="status === 'loading'" class="st-skel" aria-busy="true"><span class="skel"></span><span class="skel"></span></div>
    <div v-else-if="status === 'error'" class="st-note bad" role="alert">
      <Glyph kind="down" /><span>{{ loadError }}</span>
      <UiButton size="sm" @click="load">Try again</UiButton>
    </div>
    <div v-else-if="!rows.length" class="st-empty">
      <div class="t">Nothing checked yet</div>
      <div class="m">Updates are checked on Docker hosts Cha Dash reads through chadash-node. Add a node in Settings → Nodes, and its containers show here.</div>
    </div>
    <template v-else>
      <p class="st-meta up-sum" role="status">
        {{ available ? `${available} update${available === 1 ? '' : 's'} available` : 'Everything checked is up to date' }}
        · {{ current }} current<template v-if="lastChecked"> · last checked {{ lastChecked }}</template>
      </p>
      <div v-if="canUpdate && updatable.length" class="up-bar" role="group" aria-label="Update several">
        <UiField v-if="updatable.length > 1" label="Select all" check v-slot="{ control }">
          <input v-bind="control" type="checkbox" :checked="selected.length === updatable.length" :indeterminate="selected.length > 0 && selected.length < updatable.length" @change="toggleAll" />
        </UiField>
        <UiField v-if="upcoming.length" label="When" v-slot="{ control }">
          <select v-bind="control" v-model="windowFor">
            <option value="">Now</option>
            <option v-for="w in upcoming" :key="w.id" :value="w.id">In {{ w.name }}</option>
          </select>
        </UiField>
        <UiButton variant="primary" size="sm" :disabled="planning" :aria-busy="planning || undefined" @click="rollout([])">
          {{ windowFor ? 'Schedule all' : 'Update all' }} ({{ updatable.length }})<span class="tier">T2</span>
        </UiButton>
        <UiButton v-if="selected.length" size="sm" :disabled="planning" @click="rollout(selected)">
          {{ windowFor ? 'Schedule selected' : 'Update selected' }} ({{ selected.length }})<span class="tier">T2</span>
        </UiButton>
      </div>
    </template>
  </section>

  <template v-if="status === 'ready'">
    <section v-for="g in groups" :key="g.host" class="st-section" :aria-label="`Updates on ${g.host}`">
      <header>
        <h3>{{ g.host }}</h3>
      </header>
      <ul class="st-list" :aria-label="`Updates on ${g.host}`">
        <li v-for="r in g.rows" :key="r.resource">
          <div :class="['st-row', 'up-row', { pick: canUpdate && selectable(r) }]">
            <input
              v-if="canUpdate && selectable(r)"
              v-model="selected"
              class="up-pick"
              type="checkbox"
              :value="r.resource"
              :aria-label="`Select ${r.name}`"
            />
            <Glyph :kind="r.state === 'held' ? 'off' : r.state === 'rolled_back' ? 'down' : 'attn'" />
            <span>
              <span class="t">
                {{ r.name }}
                <span v-if="r.stack" class="chip">{{ r.stack }}</span>
                <span v-if="r.state === 'held'" class="chip">{{ r.hold?.kind === 'snooze' ? 'snoozed' : 'held' }}</span>
                <span v-if="r.state === 'rolled_back'" class="chip">rolled back</span>
                <span v-if="r.risk && r.state === 'available'" :class="['chip', 'up-risk', r.risk]">{{ riskWords(r.risk) }}</span>
              </span>
              <span v-if="kindOf(r) === 'image'" class="m">
                <span class="st-mono">{{ r.image }}</span>
                · <span class="st-mono">{{ short(r.current) }}</span> → <span class="st-mono">{{ short(r.latest) }}</span>
                <template v-if="r.checked_at"> · checked {{ ago(r.checked_at, now) }}</template>
              </span>
              <span v-else class="m">{{ kindOf(r) === 'apt' ? `Host packages · ${r.latest}` : `${r.image} ${r.current}` }}</span>
              <span v-if="r.hold" class="m">{{ holdWords(r) }}</span>
              <span v-if="r.state === 'rolled_back'" class="m up-why">{{ rolledBackWords(r) }}</span>
              <span v-if="r.why" class="m up-why">{{ r.why }}</span>
            </span>
            <span class="end up-acts">
              <UiButton v-if="r.link" size="sm" :href="r.link.url" target="_blank" rel="noopener noreferrer">{{ r.link.title }}</UiButton>
              <UiButton
                v-if="notesOn && r.source && kindOf(r) === 'image'"
                size="sm"
                variant="ghost"
                :aria-expanded="!!notesOpen[r.resource]"
                :aria-label="`Release notes for ${r.name}`"
                @click="notesOpen[r.resource] = !notesOpen[r.resource]"
              >
                Notes
              </UiButton>
              <UiButton v-if="r.update && canUpdate && r.state === 'available'" size="sm" variant="primary" :aria-label="`Update ${r.name}`" @click="update(r)">
                Update<span class="tier">T2</span>
              </UiButton>
              <UiButton
                v-if="canUpdate && r.state === 'rolled_back'"
                size="sm"
                :disabled="busy[r.resource]"
                :aria-label="`Try ${r.name}'s update again`"
                @click="tryAgain(r)"
              >
                Try again
              </UiButton>
              <template v-if="canHold && kindOf(r) === 'image'">
                <UiButton v-if="r.hold" size="sm" :disabled="busy[r.resource]" :aria-label="`Release ${r.name}`" @click="hold(r, 'release')">Release</UiButton>
                <template v-else>
                  <UiButton size="sm" :disabled="busy[r.resource]" :aria-label="`Snooze ${r.name}'s update for 7 days`" @click="hold(r, 'snooze')">Snooze</UiButton>
                  <UiButton size="sm" :disabled="busy[r.resource]" :aria-label="`Hold ${r.name} on its image`" @click="hold(r, 'hold')">Hold</UiButton>
                </template>
              </template>
            </span>
          </div>
          <details v-if="r.packages?.length" class="up-pkgs">
            <summary>{{ r.packages.length }} package{{ r.packages.length === 1 ? '' : 's' }}</summary>
            <ul :aria-label="`Pending packages on ${r.name}`">
              <li v-for="p in r.packages" :key="p.name">
                <span class="st-mono">{{ p.name }}</span> {{ p.from }} → {{ p.to }} <span :class="['chip', 'up-risk', p.risk]">{{ riskWords(p.risk) }}</span>
              </li>
            </ul>
          </details>
          <ReleaseNotes v-if="notesOn && notesOpen[r.resource]" :resource="r.resource" :name="r.name" />
        </li>
      </ul>
    </section>

    <section v-if="cantCheck.length" class="st-section" aria-labelledby="up-cc">
      <details>
        <summary>
          <h3 id="up-cc">Can’t check · {{ cantCheck.length }}</h3>
        </summary>
        <ul class="st-list" aria-label="What Cha Dash can’t check">
          <li v-for="r in cantCheck" :key="r.resource">
            <div class="st-row up-row">
              <Glyph kind="off" />
              <span>
                <span class="t">{{ r.name }} <span class="m">· {{ kindOf(r) === 'os' ? `${r.image} ${r.current}` : r.host || r.engine }}</span></span>
                <span class="m">{{ r.reason }}</span>
              </span>
              <span v-if="r.link" class="end">
                <UiButton size="sm" :href="r.link.url" target="_blank" rel="noopener noreferrer">{{ r.link.title }}</UiButton>
              </span>
            </div>
          </li>
        </ul>
      </details>
    </section>
  </template>

  <UpdateRollouts :rollouts="rollouts" :windows="windows" :status="rolloutStatus" :error="rolloutError" @changed="loadRollouts" @retry="loadRollouts" />
  <UpdateWindows :windows="windows" :status="windowStatus" :error="windowError" @changed="loadWindows" @retry="loadWindows" />

  <section v-if="isAdmin" class="st-section" aria-labelledby="up-rn">
    <header>
      <h3 id="up-rn">Release notes</h3>
    </header>
    <UiField
      label="Read release notes from GitHub"
      help="For an image that names its GitHub repository, the hub reads the releases since the version it runs, without an account. Off by default: it’s the hub calling GitHub."
      check
      v-slot="{ control }"
    >
      <input v-bind="control" type="checkbox" :checked="notesOn" :disabled="savingNotes" @change="setNotes(($event.target as HTMLInputElement).checked)" />
    </UiField>
  </section>
</template>

<style>
.up-row {
  grid-template-columns: auto minmax(0, 1fr) auto;
}
.up-row.pick {
  grid-template-columns: auto auto minmax(0, 1fr) auto;
}
.up-pick {
  width: 18px;
  height: 18px;
  margin: 0;
  align-self: center;
}
.up-row .t {
  display: flex;
  flex-wrap: wrap;
  align-items: center;
  gap: 6px;
}
.up-row .m {
  display: block;
}
.up-acts {
  display: flex;
  flex-wrap: wrap;
  gap: 6px;
  justify-content: end;
}
.up-why {
  color: var(--text-2);
}
.up-sum {
  margin: 0;
}
.up-bar {
  display: flex;
  flex-wrap: wrap;
  align-items: end;
  gap: 8px 12px;
  margin-top: 8px;
}
.up-bar .field {
  margin: 0;
}
.up-risk.major {
  color: var(--down-text);
}
.up-risk.minor,
.up-risk.unknown {
  color: var(--attention-text);
}
.up-pkgs {
  padding: 0 0 8px 28px;
}
.up-pkgs ul {
  margin: 4px 0 0;
  padding: 0;
  list-style: none;
  display: grid;
  gap: 2px;
}
</style>
