<script setup lang="ts">
/**
 * Containers: on one engine (`containers@docker:<instance id>/engine`), on a
 * host (`containers@unraid:<instance id>/host`, whose containers run on it
 * directly), or with no target, across every engine. Healthy ones get no
 * badge; problems rise to the top.
 *
 * Small: running out of total. Wide: the count, how many aren't healthy, and
 * the three most recent changes. Tall: up to ten containers with a status
 * glyph. Large: a table with image tag, uptime, CPU, memory and whether an
 * update is available.
 *
 * Down says what it was ("exited (137) at 03:12 · OOM-killed") and who depends
 * on it, from the graph ("sonarr, radarr depend on it").
 *
 * A container that several sources report (Docker and Unraid) is one row:
 * the hub's correlator links them (`same_as`), and the row takes the worst
 * health and names every source.
 */
import { computed } from 'vue';
import type { SizeId } from '@chadash/layout';
import Glyph from '../components/marks/Glyph.vue';
import type { TileStatus } from '../components/Tile.vue';
import { fmtInt, fmtPct, hhmm, plural } from '../lib/format';
import { attr, engines, incoming, label, mergedHealth, resolve, series, sourcesOf } from '../model/graph';
import { glyphFor, worst } from '../model/status';
import { resourceLinks, type Resource } from '../model/types';
import { useLive } from '../stores/live';
import Frame from './parts/Frame.vue';
import StateNote from './parts/StateNote.vue';
import { containersOf, upFor } from './read';
import { statusOf } from './subject';
import { useSubject } from './use';

const { size, target } = defineProps<{ size: SizeId; target?: string | undefined }>();
const live = useLive();

const scope = computed(() => (target ? resolve(live.graph.value, target) : undefined));
const s = useSubject(() => scope.value?.id);
const list = computed(() => engines(live.graph.value));
const all = computed(() => {
  const g = live.graph.value;
  if (scope.value) return containersOf(g, scope.value);
  // Across engines (and hosts that run containers themselves, like Unraid's API).
  return containersOf(g, undefined);
});

interface Row {
  r: Resource;
  status: TileStatus;
  word: string;
  changed: number;
  /** "Docker · Unraid" when more than one source reports it. */
  seenBy: string;
}
const DOWN_STATES = new Set(['exited', 'dead']);
function rowOf(r: Resource): Row {
  const g = live.graph.value;
  const h = mergedHealth(g, r.id);
  const status = statusOf({ ...r, health: h });
  const sources = sourcesOf(g, r.id);
  let word = '';
  if (status === 'down') {
    const exit = attr<string | number>(r, 'last_exit') ?? attr<number>(r, 'exit_code');
    const at = attr<string>(r, 'finished_at');
    const parts = [DOWN_STATES.has(r.state ?? '') ? `exited${exit !== undefined ? ` (${String(exit).replace(/ .*/, '')})` : ''}${at ? ` at ${hhmm(at)}` : ''}` : (r.state ?? 'down')];
    if (attr(r, 'oom_killed') === true || /oom/i.test(String(exit ?? '') + (h.reason ?? ''))) parts.push('OOM-killed');
    else if (h.reason) parts.push(h.reason);
    const deps = [...incoming(live.graph.value, r.id, 'runs_on'), ...incoming(live.graph.value, r.id, 'depends_on')].map((d) => label(d));
    if (deps.length) parts.push(`${deps.slice(0, 2).join(', ')} depend${deps.length === 1 ? 's' : ''} on it`);
    word = parts.join(' · ');
  } else if (status === 'attention') {
    word = h.reason ?? (Number(attr(r, 'restart_count') ?? 0) > 3 ? 'restarting repeatedly' : 'unhealthy');
  } else if (status === 'off') {
    word = 'stopped (expected)';
  } else if (status === 'maintenance') {
    word = h.reason ?? (r.state === 'paused' ? 'paused' : 'maintenance');
  }
  const changed = Math.max(Date.parse(attr<string>(r, 'started_at') ?? '') || 0, Date.parse(attr<string>(r, 'finished_at') ?? '') || 0);
  return { r, status, word, changed, seenBy: sources.length > 1 ? sources.join(' · ') : '' };
}
const RANK: Partial<Record<TileStatus, number>> = { down: 0, attention: 1, maintenance: 2, unknown: 3, ok: 4, off: 5 };
const rows = computed(() => all.value.map(rowOf).sort((a, b) => (RANK[a.status] ?? 9) - (RANK[b.status] ?? 9) || label(a.r).localeCompare(label(b.r))));
const recent = computed(() => [...rows.value].filter((x) => x.changed).sort((a, b) => b.changed - a.changed).slice(0, 3));
const running = computed(() => all.value.filter((c) => c.state === 'running').length);
const unhealthy = computed(() => rows.value.filter((x) => x.status === 'attention' || x.status === 'down'));
const status = computed<TileStatus>(() => {
  const own = s.value.status;
  if (own === 'down' || own === 'repair' || own === 'maintenance') return own;
  return worst(unhealthy.value.map((x) => x.status));
});
const tag = computed(() => {
  if (status.value === s.value.status && status.value !== 'ok') return undefined;
  if (unhealthy.value.length === 1) return `${label(unhealthy.value[0]!.r)} ${unhealthy.value[0]!.status === 'down' ? 'down' : 'not healthy'}`;
  return unhealthy.value.length ? `${unhealthy.value.length} not healthy` : all.value.length ? 'all healthy' : '';
});
const name = computed(() => (scope.value ? `Containers · ${label(scope.value)}` : 'Containers'));
const context = computed(() => (scope.value ? '' : list.value.length ? plural(list.value.length, 'engine') : ''));
const noEngines = computed(() => !scope.value && !list.value.length && !all.value.length);
const imageTag = (r: Resource) => {
  const img = attr<string>(r, 'image') ?? '';
  const at = img.lastIndexOf(':');
  return at > img.lastIndexOf('/') ? img.slice(at + 1) : 'latest';
};
const cpu = (r: Resource) => series(live.metrics.value, r.id, 'cpu.pct')?.value ?? null;
const memMib = (r: Resource) => series(live.metrics.value, r.id, 'mem.mib')?.value ?? null;
const update = (r: Resource) => (attr(r, 'update_available') === true ? 'update' : attr(r, 'update_available') === false ? '—' : '');
const incident = computed(() => live.problems.value.find((i) => unhealthy.value.some((x) => x.r.id === i.target)));
const hasStats = computed(() => all.value.some((r) => series(live.metrics.value, r.id, 'cpu.pct')));
</script>

<template>
  <Frame
    :size="size"
    :name="name"
    mono="DK"
    :context="context"
    :href="resourceLinks(scope)[0]?.url"
    :subject="s"
    :status="status"
    :tag="tag"
    :tag-glyph="tag ? (all.length ? glyphFor(status) : undefined) : undefined"
    :incident="s.problem ?? incident"
  >
    <template v-if="target && !scope">
      <div class="v sm unknown">—</div>
      <p class="lab">This engine isn't in the graph any more.</p>
    </template>
    <template v-else-if="noEngines">
      <div class="v sm word">No Docker engines</div>
      <p class="lab">Point Cha Dash at a socket proxy to see containers.</p>
    </template>
    <template v-else-if="!all.length">
      <div class="v sm word">No containers</div>
      <p class="lab">No containers on this engine.</p>
    </template>

    <template v-else-if="size === 'tall'">
      <div class="v sm">{{ running }}<span class="u">/{{ all.length }} running</span></div>
      <ul class="list" :aria-label="name">
        <li v-for="x in rows.slice(0, 10)" :key="x.r.id" :class="['li', { dim: x.status === 'off' }]">
          <Glyph v-if="x.status !== 'ok'" :kind="glyphFor(x.status)" :label="x.word" />
          <span v-else></span>
          <span><span class="t">{{ label(x.r) }}</span><small v-if="x.word">{{ x.word }}</small><small v-if="x.seenBy" class="src">{{ x.seenBy }}</small></span>
          <span></span>
        </li>
      </ul>
      <div v-if="rows.length > 10" class="tf"><span>+{{ rows.length - 10 }} more</span></div>
    </template>

    <template v-else-if="size === 'large'">
      <div class="row">
        <div class="v sm">{{ running }}<span class="u">/{{ all.length }} running</span></div>
        <span class="lab">{{ unhealthy.length ? `${unhealthy.length} not healthy` : 'all healthy' }}</span>
      </div>
      <div class="stbl ctbl" role="table" :aria-label="name">
        <div class="sr head" role="row">
          <span role="columnheader">Container</span>
          <span role="columnheader">Tag</span>
          <span class="r" role="columnheader">Up</span>
          <span class="r" role="columnheader">CPU</span>
          <span class="r" role="columnheader">Mem</span>
        </div>
        <div v-for="x in rows.slice(0, 9)" :key="x.r.id" :class="['sr', { dim: x.status === 'off' }]" role="row">
          <span class="h" role="cell">
            <Glyph v-if="x.status !== 'ok'" :kind="glyphFor(x.status)" :label="x.word" />{{ label(x.r) }}<small v-if="update(x.r) === 'update'">update</small><small v-if="x.seenBy" class="src">{{ x.seenBy }}</small>
          </span>
          <span class="tagc" role="cell">{{ imageTag(x.r) }}</span>
          <span class="r n" role="cell">{{ x.r.state === 'running' ? upFor(attr(x.r, 'started_at'), live.now.value) : '—' }}</span>
          <span class="r n" role="cell">{{ cpu(x.r) === null ? '—' : `${fmtPct(cpu(x.r))}%` }}</span>
          <span class="r n" role="cell">{{ memMib(x.r) === null ? '—' : `${fmtInt(memMib(x.r))} MiB` }}</span>
        </div>
      </div>
      <div class="tf">
        <span>{{ rows.length > 9 ? `+${rows.length - 9} more · ` : '' }}{{ hasStats ? '' : 'CPU and memory arrive with per-tile interest' }}</span>
      </div>
    </template>

    <template v-else>
      <div class="row">
        <div class="v">{{ running }}<span class="u">/{{ all.length }} running</span></div>
        <span v-if="size === 'wide'" class="lab">{{ unhealthy.length ? `${unhealthy.length} not healthy` : 'none unhealthy' }}</span>
      </div>
      <ul v-if="size === 'wide' && (unhealthy.length || recent.length)" class="list" :aria-label="unhealthy.length ? 'Containers that need attention' : 'Recent changes'">
        <li v-for="x in (unhealthy.length ? unhealthy : recent).slice(0, 3)" :key="x.r.id" class="li">
          <Glyph v-if="x.status !== 'ok'" :kind="glyphFor(x.status)" :label="x.word" />
          <span v-else></span>
          <span>
            <span class="t">{{ label(x.r) }}</span><small>{{ x.word || `started ${hhmm(x.changed)}` }}</small><small v-if="x.seenBy" class="src">{{ x.seenBy }}</small>
          </span>
          <span></span>
        </li>
      </ul>
      <StateNote v-else-if="size !== 'small'" :subject="s" />
      <div class="tf">
        <span v-if="!scope">
          <template v-for="(e, i) in list" :key="e.id">{{ i ? ' · ' : '' }}{{ e.name }} <span class="n">{{ e.total }}</span></template>
        </span>
        <span v-else>{{ unhealthy.length ? `${unhealthy.length} not healthy` : 'all healthy' }}</span>
      </div>
    </template>
  </Frame>
</template>
