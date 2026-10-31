<script setup lang="ts">
/**
 * Settings → Updates (09 · v0.3, the Update Center, minimal): which
 * containers have a newer image, by host.
 *
 *   GET    /api/updates                    → {updates: UpdateRow[]}
 *   POST   /api/updates/check              → {asked}: Check now
 *   POST   /api/updates/{resource}/hold    {kind: hold|snooze} → the row (audited)
 *   DELETE /api/updates/{resource}/hold    → the row (audited)
 *
 * Docker hosts through chadash-node are checked: the node asks each
 * registry at most every 6 hours. Update runs the action the hub names for
 * the row (docker.container.update: pull, recreate, the health gate, and a
 * rollback), through the usual T2 hold-to-confirm. Hold keeps a container
 * on its image until it's released; Snooze hides an update for 7 days or
 * until a newer one appears. What can't be checked says why, folded away.
 *
 * Its own chunk, loaded with the pane.
 */
import { computed, onBeforeUnmount, onMounted, ref } from 'vue';
import { ApiError, api } from '../lib/api';
import { ago } from '../lib/format';
import { useUi } from '../stores/ui';
import Glyph from '../ui/marks/Glyph.vue';
import UiButton from '../ui/Button.vue';
import SettingsHelp from './SettingsHelp.vue';

interface UpdateRow {
  resource: string;
  name: string;
  engine: string;
  host: string;
  stack?: string;
  image?: string;
  state: 'current' | 'available' | 'cant_check' | 'held';
  current?: string;
  latest?: string;
  checked_at?: string;
  reason?: string;
  hold?: { kind: 'hold' | 'snooze'; until?: string; created_by?: string; created_at: string };
  update?: { id: string; target: string };
  why?: string;
}

const ui = useUi();
const rows = ref<UpdateRow[]>([]);
const status = ref<'loading' | 'ready' | 'error'>('loading');
const loadError = ref('');
const checking = ref(false);
const busy = ref<Record<string, boolean>>({});
const now = ref(Date.now());

const canUpdate = computed(() => ui.can.value.act('T2'));
const canHold = computed(() => ui.can.value.act('T1'));

async function load(): Promise<void> {
  try {
    const res = await api<{ updates?: UpdateRow[] }>('/api/updates');
    rows.value = res.updates ?? [];
    status.value = 'ready';
  } catch (err) {
    loadError.value = err instanceof ApiError ? err.message : 'Couldn’t load the updates.';
    status.value = 'error';
  }
  now.value = Date.now();
}

let timer: ReturnType<typeof setTimeout> | undefined;
function schedule(): void {
  clearTimeout(timer);
  timer = setTimeout(() => void load().then(schedule), 30_000);
}
onMounted(() => void load().then(schedule));
onBeforeUnmount(() => clearTimeout(timer));

/** Rows to act on (available or held), by host. */
const groups = computed(() => {
  const by = new Map<string, UpdateRow[]>();
  for (const r of rows.value) {
    if (r.state !== 'available' && r.state !== 'held') continue;
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
  } catch (err) {
    ui.toast(err instanceof ApiError ? err.message : 'That didn’t work.', 'error');
  } finally {
    busy.value[r.resource] = false;
  }
}

const short = (d?: string) => (d ? d.replace(/^sha256:/, '').slice(0, 12) : '');

function holdWords(r: UpdateRow): string {
  if (!r.hold) return '';
  const by = r.hold.created_by ? ` by ${r.hold.created_by}` : '';
  if (r.hold.kind === 'hold') return `Held${by} ${ago(r.hold.created_at, now.value)}, until released`;
  return `Snoozed${by} until ${r.hold.until ? new Date(r.hold.until).toLocaleDateString() : 'later'}, or a newer image`;
}
</script>

<template>
  <section class="st-section" aria-labelledby="up-h">
    <header>
      <h3 id="up-h">Updates</h3>
      <UiButton v-if="canHold" size="sm" :disabled="checking" @click="checkNow">Check now</UiButton>
    </header>
    <SettingsHelp summary="Containers whose image tag points at a newer image in its registry.">
      Cha Dash asks each Docker host’s node whether a container’s image tag has moved in its registry. The node asks each registry at most every 6 hours and
      changes nothing. Updating pulls the tag, recreates the container on it, waits for it to report healthy, and puts the previous image back on its own if
      it doesn’t.
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
    <p v-else class="st-meta up-sum" role="status">
      {{ available ? `${available} update${available === 1 ? '' : 's'} available` : 'Everything checked is up to date' }}
      · {{ current }} current<template v-if="lastChecked"> · last checked {{ lastChecked }}</template>
    </p>
  </section>

  <template v-if="status === 'ready'">
    <section v-for="g in groups" :key="g.host" class="st-section" :aria-label="`Updates on ${g.host}`">
      <header>
        <h3>{{ g.host }}</h3>
      </header>
      <ul class="st-list" :aria-label="`Containers on ${g.host} with a newer image`">
        <li v-for="r in g.rows" :key="r.resource">
          <div class="st-row up-row">
            <Glyph :kind="r.state === 'held' ? 'off' : 'attn'" />
            <span>
              <span class="t">
                {{ r.name }}
                <span v-if="r.stack" class="chip">{{ r.stack }}</span>
                <span v-if="r.state === 'held'" class="chip">{{ r.hold?.kind === 'snooze' ? 'snoozed' : 'held' }}</span>
              </span>
              <span class="m">
                <span class="st-mono">{{ r.image }}</span>
                · <span class="st-mono">{{ short(r.current) }}</span> → <span class="st-mono">{{ short(r.latest) }}</span>
                <template v-if="r.checked_at"> · checked {{ ago(r.checked_at, now) }}</template>
              </span>
              <span v-if="r.hold" class="m">{{ holdWords(r) }}</span>
              <span v-if="r.why" class="m up-why">{{ r.why }}</span>
            </span>
            <span class="end up-acts">
              <UiButton v-if="r.update && canUpdate && r.state === 'available'" size="sm" variant="primary" :aria-label="`Update ${r.name}`" @click="update(r)">
                Update<span class="tier">T2</span>
              </UiButton>
              <template v-if="canHold">
                <UiButton v-if="r.hold" size="sm" :disabled="busy[r.resource]" :aria-label="`Release ${r.name}`" @click="hold(r, 'release')">Release</UiButton>
                <template v-else>
                  <UiButton size="sm" :disabled="busy[r.resource]" :aria-label="`Snooze ${r.name}'s update for 7 days`" @click="hold(r, 'snooze')">Snooze</UiButton>
                  <UiButton size="sm" :disabled="busy[r.resource]" :aria-label="`Hold ${r.name} on its image`" @click="hold(r, 'hold')">Hold</UiButton>
                </template>
              </template>
            </span>
          </div>
        </li>
      </ul>
    </section>

    <section v-if="cantCheck.length" class="st-section" aria-labelledby="up-cc">
      <details>
        <summary>
          <h3 id="up-cc">Can’t check · {{ cantCheck.length }}</h3>
        </summary>
        <ul class="st-list" aria-label="Containers Cha Dash can’t check">
          <li v-for="r in cantCheck" :key="r.resource">
            <div class="st-row up-row">
              <Glyph kind="off" />
              <span>
                <span class="t">{{ r.name }} <span class="m">· {{ r.host || r.engine }}</span></span>
                <span class="m">{{ r.reason }}</span>
              </span>
            </div>
          </li>
        </ul>
      </details>
    </section>
  </template>
</template>

<style>
.up-row {
  grid-template-columns: auto minmax(0, 1fr) auto;
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
</style>
