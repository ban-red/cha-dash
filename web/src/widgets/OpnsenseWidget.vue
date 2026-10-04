<script setup lang="ts">
/** OPNsense: WAN throughput now, 24 h of it as a sparkline, and the 30-day totals. */
import { computed } from 'vue';
import type { SizeId } from '@chadash/layout';
import Tile from '../components/Tile.vue';
import Sparkline from '../components/marks/Sparkline.vue';
import { fmtInt, fmtNum, fmtPct } from '../lib/format';
import { byKind, outgoing, placeOf, series } from '../model/graph';
import { glyphFor, tileStatus } from '../model/status';
import { normalizeHealth, resourceLinks } from '../model/types';
import { useLive } from '../stores/live';

const { size } = defineProps<{ size: SizeId; target?: string | undefined }>();
const live = useLive();

const router = computed(() => byKind(live.graph.value, 'router')[0]);
const wan = computed(() => byKind(live.graph.value, 'wan').find((w) => outgoing(live.graph.value, w.id, 'part_of').some((r) => r.id === router.value?.id)) ?? byKind(live.graph.value, 'wan')[0]);
const rx = computed(() => series(live.metrics.value, wan.value?.id ?? '', 'rx.mbps'));
const tx = computed(() => series(live.metrics.value, wan.value?.id ?? '', 'tx.mbps'));
const rx30 = computed(() => series(live.metrics.value, wan.value?.id ?? '', 'rx.gb_30d'));
const tx30 = computed(() => series(live.metrics.value, wan.value?.id ?? '', 'tx.gb_30d'));
const cpu = computed(() => series(live.metrics.value, router.value?.id ?? '', 'cpu.pct'));
const health = computed(() => normalizeHealth(router.value));
const wanHealth = computed(() => normalizeHealth(wan.value));
const status = computed(() => (wanHealth.value.status === 'down' ? 'down' : tileStatus(health.value, router.value)));
const place = computed(() => (router.value ? placeOf(live.graph.value, router.value.id) : undefined));
const tag = computed(() => (status.value === 'down' ? 'WAN down' : status.value === 'attention' ? (health.value.reason ?? 'needs you') : wan.value ? 'WAN up' : ''));
</script>

<template>
  <Tile
    :size="size"
    :name="router?.name ?? 'Router'"
    mono="OP"
    :context="place ? `router · VM on ${place}` : 'router'"
    :href="resourceLinks(router)[0]?.url"
    :status="router ? status : 'unknown'"
    :stale="Boolean(rx?.stale)"
    :as-of="rx?.as_of"
    :tag="tag"
    :tag-glyph="router ? glyphFor(status) : undefined"
    :loading="live.loading.value"
  >
    <template v-if="!router">
      <div class="v sm word">No router</div>
      <p class="lab">Connect OPNsense to see the WAN here.</p>
    </template>
    <template v-else-if="size === 'small'">
      <div class="v">{{ fmtNum(rx?.value) }}<span class="u">Mb/s ↓</span></div>
      <Sparkline :values="rx?.history" :band="rx?.band" :height="30" label="WAN download" :format="(v: number) => `${fmtNum(v)} Mb/s`" />
      <div class="tf"><span>↑ <span class="n">{{ fmtNum(tx?.value) }}</span> Mb/s</span></div>
    </template>
    <div v-else class="split">
      <div class="col">
        <div class="v">{{ fmtNum(rx?.value) }}<span class="u">Mb/s ↓</span></div>
        <div class="sub"><b class="n">{{ fmtInt(rx30?.value) }} GB</b> down · <b class="n">{{ fmtInt(tx30?.value) }} GB</b> up</div>
        <div class="sub">last 30 days · CPU <span class="n">{{ fmtPct(cpu?.value) }}%</span></div>
      </div>
      <Sparkline :values="rx?.history" :band="rx?.band" :height="58" label="WAN download, 24 hours" :format="(v: number) => `${fmtNum(v)} Mb/s`" />
    </div>
  </Tile>
</template>
