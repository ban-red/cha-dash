<script setup lang="ts">
/** One host: memory as the primary value (with its warn tick), CPU and temperature, 24 h of memory. */
import { computed } from 'vue';
import type { SizeId } from '@chadash/layout';
import Tile from '../components/Tile.vue';
import Meter from '../components/marks/Meter.vue';
import Sparkline from '../components/marks/Sparkline.vue';
import { fmtNum, fmtPct, since } from '../lib/format';
import { attr, label, series } from '../model/graph';
import { glyphFor, tileStatus } from '../model/status';
import { normalizeHealth, resourceLinks } from '../model/types';
import { useLive } from '../stores/live';
import { useUi } from '../stores/ui';

const { size, target = '' } = defineProps<{ size: SizeId; target?: string | undefined }>();
const live = useLive();
const ui = useUi();

const host = computed(() => live.graph.value?.resources[target]);
const health = computed(() => normalizeHealth(host.value));
const status = computed(() => tileStatus(health.value, host.value));
const mem = computed(() => series(live.metrics.value, target, 'mem.pct'));
const cpu = computed(() => series(live.metrics.value, target, 'cpu.pct'));
const temp = computed(() => series(live.metrics.value, target, 'temp.c'));
const platform = computed(() => String(attr(host.value, 'platform') ?? ''));
const context = computed(() => {
  const parts = [platform.value === 'unraid' ? `Unraid ${attr(host.value, 'version') ?? ''}`.trim() : platform.value === 'pve' ? 'Proxmox VE' : platform.value];
  const gb = attr<number>(host.value, 'mem_gb');
  if (gb) parts.push(`${gb} GB`);
  return parts.filter(Boolean).join(' · ');
});
const incident = computed(() => live.problems.value.find((i) => i.target === target));
const tag = computed(() => {
  if (status.value === 'attention' || status.value === 'down') return health.value.reason ?? 'needs you';
  return host.value?.state === 'online' || host.value?.state === 'running' ? 'online' : (host.value?.state ?? '');
});
const tone = computed(() => (status.value === 'down' ? 'down' : status.value === 'attention' ? 'attn' : 'neutral'));
</script>

<template>
  <Tile
    :size="size"
    :name="label(host) || target"
    :context="size === 'small' ? '' : context"
    :href="resourceLinks(host)[0]?.url"
    :status="host ? status : 'unknown'"
    :stale="Boolean(mem?.stale)"
    :as-of="mem?.as_of"
    :tag="tag"
    :tag-glyph="host ? glyphFor(status) : undefined"
    :loading="live.loading.value"
    :clickable="Boolean(incident)"
    @open="incident && ui.openInbox(incident.id)"
  >
    <template v-if="!host">
      <div class="v sm unknown">—</div>
      <p class="sub">This host isn't in the graph any more.</p>
    </template>
    <template v-else-if="size === 'small'">
      <div class="v">{{ fmtPct(mem?.value) }}<span class="u">% mem</span></div>
      <Meter :value="mem?.value" :warn="mem?.warn ?? 90" :crit="mem?.crit ?? 98" :tick="mem?.warn ?? 90" :label="`${label(host)} memory`" />
      <div class="tf">
        <span>CPU <span class="n">{{ fmtPct(cpu?.value) }}%</span> · <span class="n">{{ fmtNum(temp?.value) }}°</span></span>
      </div>
    </template>
    <div v-else class="split">
      <div class="col">
        <div class="v">{{ fmtPct(mem?.value) }}<span class="u">% memory</span></div>
        <Meter :value="mem?.value" :warn="mem?.warn ?? 90" :crit="mem?.crit ?? 98" :tick="mem?.warn ?? 90" :label="`${label(host)} memory`" />
        <div class="sub">
          CPU <b class="n">{{ fmtPct(cpu?.value) }}%</b> · <b class="n">{{ fmtNum(temp?.value) }}°C</b>
          <template v-if="health.since && status !== 'ok'"> · {{ since(health.since, live.now.value) }}</template>
        </div>
      </div>
      <Sparkline
        :values="mem?.history"
        :band="mem?.band"
        :tone="tone"
        :height="52"
        :label="`${label(host)} memory, 24 hours`"
        :format="(v: number) => `${Math.round(v)}%`"
      />
    </div>
  </Tile>
</template>
