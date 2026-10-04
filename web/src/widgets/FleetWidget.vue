<script setup lang="ts">
/** Fleet: every host in one instrument table. Memory is a bar with the warn tick. */
import { computed } from 'vue';
import type { SizeId } from '@chadash/layout';
import Tile from '../components/Tile.vue';
import Glyph from '../components/marks/Glyph.vue';
import Meter from '../components/marks/Meter.vue';
import { fmtPct, plural } from '../lib/format';
import { byKind, engines, hostRows } from '../model/graph';
import { isProblem } from '../model/graph';
import { useLive } from '../stores/live';
import { useUi } from '../stores/ui';

const { size } = defineProps<{ size: SizeId; target?: string | undefined }>();
const live = useLive();
const ui = useUi();

const rows = computed(() => hostRows(live.graph.value, live.metrics.value));
const needs = computed(() => rows.value.filter((r) => isProblem(r.health)));
const shown = computed(() => {
  if (size === 'wide') {
    // Four columns by two rows: the hosts that need you first, then the rest.
    return [...needs.value, ...rows.value.filter((r) => !isProblem(r.health))].slice(0, 3);
  }
  return rows.value;
});
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
const status = computed(() => (needs.value.some((r) => r.health.status === 'down') ? 'down' : needs.value.length ? 'attention' : 'ok'));
const tag = computed(() => (needs.value.length ? `${needs.value.length} need${needs.value.length === 1 ? 's' : ''} you` : rows.value.length ? 'all healthy' : ''));
const firstIncident = computed(() => live.problems.value.find((i) => needs.value.some((r) => r.id === i.target)));
</script>

<template>
  <Tile
    :size="size"
    name="Fleet"
    mono="PV"
    :context="context"
    :status="status"
    :tag="tag"
    :tag-glyph="needs.length ? (status === 'down' ? 'down' : 'attn') : rows.length ? 'ok' : undefined"
    :loading="live.loading.value"
    :clickable="Boolean(firstIncident)"
    @open="firstIncident && ui.openInbox(firstIncident.id)"
  >
    <p v-if="!rows.length" class="sub">No hosts yet. Connect Proxmox or Unraid to see them here.</p>
    <div v-else class="fleet" role="table" aria-label="Hosts">
      <div class="fr head" role="row">
        <span role="columnheader">Host</span>
        <span class="role" role="columnheader">Role</span>
        <span class="r" role="columnheader">CPU</span>
        <span role="columnheader">Memory</span>
        <span class="r" role="columnheader">Guests</span>
      </div>
      <div v-for="h in shown" :key="h.id" class="fr" role="row">
        <span class="h" role="cell">
          <Glyph v-if="isProblem(h.health)" :kind="h.health.status === 'down' ? 'down' : 'attn'" :label="h.health.reason || 'needs attention'" />{{ h.name }}
        </span>
        <span class="role" role="cell">{{ h.role }}</span>
        <span class="cpu n" role="cell">{{ fmtPct(h.cpu) }}{{ h.cpu === null ? '' : '%' }}</span>
        <span class="mem" role="cell">
          <Meter :value="h.mem" :warn="90" :crit="98" :tick="90" thin :label="`${h.name} memory`" />
          <span class="n">{{ fmtPct(h.mem) }}{{ h.mem === null ? '' : '%' }}</span>
        </span>
        <span class="gs n" role="cell">{{ h.guestsTotal ? `${h.guestsRunning}/${h.guestsTotal}` : '—' }}</span>
      </div>
    </div>
    <div v-if="rows.length" class="tf">
      <span><span class="n">{{ guests.running }}</span> of <span class="n">{{ guests.total }}</span> guests running · <span class="n">{{ engines(live.graph.value).length }}</span> Docker engines</span>
    </div>
  </Tile>
</template>
