<script setup lang="ts">
/**
 * The router (OPNsense), the network core: its states outrank everything
 * else. Target: the router (`opnsense@opnsense:<instance id>/router`), else
 * the first one. Its WAN is the `wan` resource `part_of` it.
 *
 * Glyph: WAN ↓ now. Strip: WAN · throughput sparkline · ↓ now. Small: ↓ now
 * and the 30-day totals. Wide: ↓ now, the sparkline, 30-day ↓/↑ totals, CPU.
 * Large: Wide plus interfaces and gateways.
 *
 * Reads the WAN's `rx.mbps`, `tx.mbps`, `rx.gb_30d`, `tx.gb_30d` and the
 * router's `cpu.pct`. CPU needs OPNsense's *Lobby: Dashboard* privilege and
 * gateways *System: Gateways*; without them that part is permission-limited
 * (a lock and the privilege it needs), never a fake 0.
 */
import { computed } from 'vue';
import type { SizeId } from '@chadash/layout';
import Glyph from '../components/marks/Glyph.vue';
import MiniGraph from '../components/marks/MiniGraph.vue';
import type { TileStatus } from '../components/Tile.vue';
import { fmtInt, fmtNum, fmtPct, since } from '../lib/format';
import { attr, byKind, incoming, label, placeOf, resolve, series } from '../model/graph';
import { normalizeHealth, resourceLinks } from '../model/types';
import { useLive } from '../stores/live';
import Frame from './parts/Frame.vue';
import StateNote from './parts/StateNote.vue';
import { statusOf } from './subject';
import { useSubject } from './use';

const { size, target } = defineProps<{ size: SizeId; target?: string | undefined }>();
const live = useLive();

const router = computed(() => {
  const t = target ? resolve(live.graph.value, target) : undefined;
  // A router, or an instance that hasn't published one yet (its repair shows).
  return t?.kind === 'router' || t?.kind === 'integration' ? t : byKind(live.graph.value, 'router')[0];
});
const s = useSubject(() => router.value?.id);
const wan = computed(() => {
  const g = live.graph.value;
  const own = router.value ? incoming(g, router.value.id, 'part_of').find((r) => r.kind === 'wan') : undefined;
  return own ?? (router.value?.kind === 'integration' ? undefined : byKind(g, 'wan')[0]);
});
const w = (key: string) => series(live.metrics.value, wan.value?.id ?? '', key);
const rx = computed(() => w('rx.mbps'));
const tx = computed(() => w('tx.mbps'));
const rx30 = computed(() => w('rx.gb_30d'));
const tx30 = computed(() => w('tx.gb_30d'));
const cpu = computed(() => series(live.metrics.value, router.value?.id ?? '', 'cpu.pct'));
const wanStatus = computed(() => statusOf(wan.value));
const status = computed<TileStatus>(() => (wanStatus.value === 'down' ? 'down' : s.value.status));
const wanSince = computed(() => normalizeHealth(wan.value).since);
const tag = computed(() => {
  if (status.value === 'down' && wanStatus.value === 'down') return `WAN down ${since(wanSince.value, live.now.value)}`.trim();
  if (status.value === 'ok') return wan.value ? 'WAN up' : '';
  return undefined;
});
const place = computed(() => (router.value ? placeOf(live.graph.value, router.value.id) : undefined));
const fmtRate = (v: number) => `${fmtNum(v)} Mb/s`;
const gb = (v: number | null | undefined) => (typeof v === 'number' && v >= 1000 ? `${fmtNum(v / 1000)} TB` : `${fmtInt(v)} GB`);
</script>

<template>
  <Frame
    :size="size"
    :name="router ? label(router) : 'Router'"
    mono="OP"
    :context="size === 'glyph' ? '' : place ? `router · VM on ${place}` : 'router'"
    :href="resourceLinks(router)[0]?.url"
    :subject="s"
    :status="router ? status : 'unknown'"
    :tag="tag"
    :tag-glyph="tag ? (status === 'down' ? 'down' : 'ok') : undefined"
    :stale="Boolean(rx?.stale)"
    :as-of="rx?.as_of"
  >
    <template v-if="!router">
      <div :class="size === 'glyph' ? 'gv unknown' : 'v sm word'">{{ size === 'glyph' ? '—' : 'No router' }}</div>
      <p v-if="size !== 'glyph'" class="lab">Connect OPNsense to see the WAN here.</p>
    </template>

    <template v-else-if="size === 'glyph'">
      <div :class="['gv', { unknown: rx?.value == null }]">{{ fmtNum(rx?.value) }}<span class="u">↓</span></div>
      <div class="gl">WAN Mb/s</div>
    </template>

    <template v-else-if="size === 'strip' || size === 'strip-long'">
      <MiniGraph :values="rx?.history" :band="rx?.band" :tone="status === 'down' ? 'down' : 'neutral'" :stale="rx?.stale" :height="22" label="WAN download, 24 hours" :format="fmtRate" />
      <span :class="['sv', 'n', { unknown: rx?.value == null }]">{{ fmtNum(rx?.value) }}<small>Mb/s ↓</small></span>
    </template>

    <template v-else-if="size === 'small'">
      <div :class="['v', { unknown: rx?.value == null }]">{{ fmtNum(rx?.value) }}<span class="u">Mb/s ↓</span></div>
      <div class="lab"><span class="n">{{ gb(rx30?.value) }}</span> down · <span class="n">{{ gb(tx30?.value) }}</span> up</div>
      <StateNote v-if="status === 'down'" :line="{ glyph: 'down', text: `Internet is down ${since(wanSince, live.now.value)}`.trim() }" />
      <StateNote v-else :subject="s" />
      <div class="tf"><span>last 30 days · ↑ <span class="n">{{ fmtNum(tx?.value) }}</span> Mb/s</span></div>
    </template>

    <template v-else>
      <div class="split">
        <div class="col">
          <div :class="['v', { unknown: rx?.value == null }]">{{ fmtNum(rx?.value) }}<span class="u">Mb/s ↓</span></div>
          <div class="sub"><b class="n">{{ gb(rx30?.value) }}</b> down · <b class="n">{{ gb(tx30?.value) }}</b> up</div>
          <div class="sub">
            last 30 days · CPU
            <span v-if="cpu" class="n">{{ fmtPct(cpu.value) }}%</span>
            <span v-else class="locked"><Glyph kind="lock" label="needs Lobby: Dashboard" />—</span>
          </div>
          <StateNote v-if="status === 'down'" :line="{ glyph: 'down', text: `Internet is down ${since(wanSince, live.now.value)}`.trim() }" />
          <StateNote v-else :subject="s" />
        </div>
        <MiniGraph
          :values="rx?.history"
          :band="rx?.band"
          :tone="status === 'down' ? 'down' : 'neutral'"
          :stale="rx?.stale"
          :height="size === 'large' ? 72 : 58"
          label="WAN download, 24 hours"
          :format="fmtRate"
        />
      </div>
      <template v-if="size === 'large'">
        <dl class="kv">
          <dt>Upload now</dt>
          <dd>{{ fmtNum(tx?.value) }} Mb/s</dd>
          <dt>Interfaces</dt>
          <dd>{{ attr(router, 'interfaces') ?? '—' }}</dd>
          <dt>WAN</dt>
          <dd>{{ wan ? `${label(wan)} · ${wanStatus === 'down' ? 'down' : 'up'}` : '—' }}</dd>
          <dt>Gateways</dt>
          <dd class="locked"><Glyph kind="lock" label="needs System: Gateways" />needs System: Gateways</dd>
        </dl>
        <div class="tf"><span>Top clients arrive in v0.3</span></div>
      </template>
    </template>
  </Frame>
</template>
