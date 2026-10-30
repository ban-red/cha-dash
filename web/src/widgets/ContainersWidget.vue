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
import Glyph from '../ui/marks/Glyph.vue';
import type { TileStatus } from '../components/Tile.vue';
import { fmtInt, fmtPct, hhmm, plural } from '../lib/format';
import { attr, engines, label, resolve, series } from '../model/graph';
import { glyphFor, worst } from '../model/status';
import { resourceLinks, type Resource } from '../model/types';
import { useLive } from '../stores/live';
import { useUi } from '../stores/ui';
import Frame from './parts/Frame.vue';
import StateNote from './parts/StateNote.vue';
import { containerRow, imageTag, rowName, updateWord } from './containers';
import { containersOf, upFor } from './read';
import { useSubject } from './use';

const { size, target } = defineProps<{ size: SizeId; target?: string | undefined }>();
const live = useLive();
const ui = useUi();
/** An engine in the footer opens its sheet, as a row opens its container's; never while editing. */
function openEngine(id: string): void {
  if (!ui.state.editing && ui.hasSheet(id)) ui.openSheet(id);
}
/** A row opens the container's sheet, except while the board is being edited. */
function open(r: Resource): void {
  if (!ui.state.editing) ui.openSheet(r.id);
}

const scope = computed(() => (target ? resolve(live.graph.value, target) : undefined));
const s = useSubject(() => scope.value?.id);
const list = computed(() => engines(live.graph.value));
const all = computed(() => {
  const g = live.graph.value;
  if (scope.value) return containersOf(g, scope.value);
  // Across engines (and hosts that run containers themselves, like Unraid's API).
  return containersOf(g, undefined);
});

const RANK: Partial<Record<TileStatus, number>> = { down: 0, attention: 1, maintenance: 2, unknown: 3, ok: 4, off: 5 };
const rows = computed(() => all.value.map((r) => containerRow(live.graph.value, r)).sort((a, b) => (RANK[a.status] ?? 9) - (RANK[b.status] ?? 9) || label(a.r).localeCompare(label(b.r))));
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
const cpu = (r: Resource) => series(live.metrics.value, r.id, 'cpu.pct')?.value ?? null;
const memMib = (r: Resource) => series(live.metrics.value, r.id, 'mem.mib')?.value ?? null;
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

    <template v-else-if="size === 'tall' || size === 'column'">
      <div class="v sm">{{ running }}<span class="u">/{{ all.length }} running</span></div>
      <ul class="list" :aria-label="name">
        <li v-for="x in rows.slice(0, 10)" :key="x.r.id" :class="['li', { dim: x.status === 'off' }]">
          <Glyph v-if="x.status !== 'ok'" :kind="glyphFor(x.status)" :label="x.word" />
          <span v-else></span>
          <span><button type="button" class="t lk" :aria-label="rowName(x)" :tabindex="ui.state.editing ? -1 : undefined" @click="open(x.r)">{{ label(x.r) }}</button><small v-if="x.word">{{ x.word }}</small><small v-if="x.seenBy" class="src">{{ x.seenBy }}</small></span>
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
            <Glyph v-if="x.status !== 'ok'" :kind="glyphFor(x.status)" :label="x.word" /><button type="button" class="lk" :aria-label="rowName(x)" :tabindex="ui.state.editing ? -1 : undefined" @click="open(x.r)">{{ label(x.r) }}</button><small v-if="updateWord(x.r) === 'update'">update</small><small v-if="x.seenBy" class="src">{{ x.seenBy }}</small>
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
        <span v-if="size === 'wide' || size === 'panel'" class="lab">{{ unhealthy.length ? `${unhealthy.length} not healthy` : 'none unhealthy' }}</span>
      </div>
      <ul v-if="(size === 'wide' || size === 'panel') && (unhealthy.length || recent.length)" class="list" :aria-label="unhealthy.length ? 'Containers that need attention' : 'Recent changes'">
        <li v-for="x in (unhealthy.length ? unhealthy : recent).slice(0, size === 'panel' ? 6 : 3)" :key="x.r.id" class="li">
          <Glyph v-if="x.status !== 'ok'" :kind="glyphFor(x.status)" :label="x.word" />
          <span v-else></span>
          <span>
            <button type="button" class="t lk" :aria-label="x.word ? rowName(x) : `${label(x.r)}, started ${hhmm(x.changed)}`" :tabindex="ui.state.editing ? -1 : undefined" @click="open(x.r)">{{ label(x.r) }}</button><small>{{ x.word || `started ${hhmm(x.changed)}` }}</small><small v-if="x.seenBy" class="src">{{ x.seenBy }}</small>
          </span>
          <span></span>
        </li>
      </ul>
      <StateNote v-else-if="size !== 'small'" :subject="s" />
      <div class="tf">
        <span v-if="!scope">
          <template v-for="(e, i) in list" :key="e.id"
            >{{ i ? ' · ' : '' }}<button type="button" class="lk" :aria-label="`${e.name}, ${e.total} containers`" :tabindex="ui.state.editing ? -1 : undefined" @click="openEngine(e.id)">{{ e.name }}</button>
            <span class="n">{{ e.total }}</span></template
          >
        </span>
        <span v-else>{{ unhealthy.length ? `${unhealthy.length} not healthy` : 'all healthy' }}</span>
      </div>
    </template>
  </Frame>
</template>
