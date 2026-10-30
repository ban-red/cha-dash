<script setup lang="ts">
/**
 * Fleet: every host in one instrument table (Proxmox nodes from every
 * instance, Unraid, Docker hosts). Memory is a bar with the warn tick.
 *
 * Wide: the host count and the worst three. Tall: names and memory bars.
 * Large: host · role · CPU · memory · guests. Hero: the same with room.
 *
 * Down hosts rank first, network-core ones before the rest, and say what
 * they take with them. A row whose instance needs repair shows "—" and ◌; a
 * host that's meant to be off is dimmed, not a problem.
 */
import { computed } from 'vue';
import type { SizeId } from '@chadash/layout';
import Glyph from '../components/marks/Glyph.vue';
import Meter from '../components/marks/Meter.vue';
import type { TileStatus } from '../components/Tile.vue';
import { fmtPct, plural, since } from '../lib/format';
import { byKind, engines, hostRows, isProblem, type HostRow } from '../model/graph';
import { glyphFor } from '../model/status';
import { useLive } from '../stores/live';
import Frame from './parts/Frame.vue';
import { subjectOf } from './subject';

const { size } = defineProps<{ size: SizeId; target?: string | undefined }>();
const live = useLive();

interface Row extends HostRow {
  status: TileStatus;
  repair: boolean;
  core: boolean;
  word: string;
}
const rows = computed<Row[]>(() =>
  hostRows(live.graph.value, live.metrics.value).map((h) => {
    const s = subjectOf(live.graph.value, live.problems.value, live.repairs.value, h.id);
    const r = live.graph.value?.resources[h.id];
    const core = r?.labels?.['chadash.role'] === 'network-core';
    const word =
      s.status === 'down'
        ? `down ${since(s.health.since, live.now.value)}${core ? ' · network core' : ''}`.trim()
        : s.status === 'repair'
          ? 'needs repair'
          : s.status === 'off'
            ? 'off (expected)'
            : s.status === 'maintenance'
              ? (s.health.reason ?? 'maintenance')
              : (h.health.reason ?? '');
    return { ...h, status: s.status, repair: s.needsRepair, core, word };
  }),
);
const RANK = (r: Row) => (r.status === 'down' ? (r.core ? 0 : 1) : r.status === 'attention' ? 2 : r.status === 'repair' ? 3 : r.status === 'maintenance' ? 4 : r.status === 'off' ? 6 : 5);
const needs = computed(() => rows.value.filter((r) => isProblem(r.health) || r.status === 'down'));
const cap = size === 'wide' ? 3 : size === 'panel' ? 5 : Infinity;
const shown = computed(() => [...rows.value].sort((a, b) => RANK(a) - RANK(b)).slice(0, cap));
const healthyHidden = computed(() => (cap === Infinity ? 0 : rows.value.length - shown.value.length));
const guests = computed(() => {
  const g = live.graph.value;
  const all = [...byKind(g, 'vm'), ...byKind(g, 'lxc')];
  return { running: all.filter((r) => r.state === 'running').length, total: all.length };
});
const clusters = computed(() => byKind(live.graph.value, 'cluster').length);
const context = computed(() => {
  const n = rows.value.length;
  if (!n) return '';
  return [plural(n, 'host'), clusters.value ? plural(clusters.value, 'cluster') : ''].filter(Boolean).join(' · ');
});
const status = computed<TileStatus>(() => (needs.value.some((r) => r.status === 'down') ? 'down' : needs.value.length ? 'attention' : 'ok'));
const tag = computed(() => {
  const core = needs.value.find((r) => r.status === 'down' && r.core);
  if (core) return `${core.name} down · router unreachable`;
  return needs.value.length ? `${needs.value.length} need${needs.value.length === 1 ? 's' : ''} you` : rows.value.length ? 'all healthy' : '';
});
const firstIncident = computed(() => live.problems.value.find((i) => needs.value.some((r) => r.id === i.target)));
const cell = (r: Row, v: number | null) => (r.repair || v === null ? '—' : `${fmtPct(v)}%`);
const rowGlyph = (r: Row) => (r.status === 'ok' ? undefined : glyphFor(r.status));
</script>

<template>
  <Frame
    :size="size"
    name="Fleet"
    mono="PV"
    :context="context"
    :status="status"
    :tag="tag"
    :tag-glyph="needs.length ? (status === 'down' ? 'down' : 'attn') : rows.length ? 'ok' : undefined"
    :stale="rows.length > 0 && rows.every((r) => r.stale)"
    :incident="firstIncident"
  >
    <template v-if="!rows.length">
      <div class="v sm word">No hosts yet</div>
      <p class="sub">Connect Proxmox or Unraid to see them here.</p>
    </template>

    <ul v-else-if="size === 'tall' || size === 'column'" class="fleet-tall" aria-label="Hosts">
      <li v-for="h in shown" :key="h.id" :class="{ dim: h.status === 'off', stale: h.stale }">
        <span class="h"><Glyph v-if="rowGlyph(h)" :kind="rowGlyph(h)!" :label="h.word || h.status" />{{ h.name }}</span>
        <span class="n">{{ cell(h, h.mem) }}</span>
        <Meter :value="h.repair ? null : h.mem" :warn="90" :crit="98" :tick="90" thin :label="`${h.name} memory`" />
      </li>
    </ul>

    <div v-else class="fleet" role="table" aria-label="Hosts">
      <div class="fr head" role="row">
        <span role="columnheader">Host</span>
        <span class="role" role="columnheader">Role</span>
        <span class="r" role="columnheader">CPU</span>
        <span role="columnheader">Memory</span>
        <span class="r" role="columnheader">Guests</span>
      </div>
      <div v-for="h in shown" :key="h.id" :class="['fr', { dim: h.status === 'off', stale: h.stale }]" role="row">
        <span class="h" role="cell">
          <Glyph v-if="rowGlyph(h)" :kind="rowGlyph(h)!" :label="h.word || h.health.reason || 'needs attention'" />{{ h.name }}
        </span>
        <span class="role" role="cell">{{ h.status === 'down' || h.status === 'repair' ? h.word : h.role }}</span>
        <span class="cpu n" role="cell">{{ cell(h, h.cpu) }}</span>
        <span class="mem" role="cell">
          <Meter :value="h.repair ? null : h.mem" :warn="90" :crit="98" :tick="90" thin :label="`${h.name} memory`" />
          <span class="n">{{ cell(h, h.mem) }}</span>
        </span>
        <span class="gs n" role="cell">{{ h.guestsTotal ? `${h.guestsRunning}/${h.guestsTotal}` : '—' }}</span>
      </div>
    </div>
    <div v-if="rows.length" class="tf">
      <span v-if="healthyHidden > 0">+{{ healthyHidden }} {{ needs.length ? 'more' : 'healthy' }}</span>
      <span v-else><span class="n">{{ guests.running }}</span> of <span class="n">{{ guests.total }}</span> guests running · <span class="n">{{ engines(live.graph.value).length }}</span> Docker engines</span>
    </div>
  </Frame>
</template>
