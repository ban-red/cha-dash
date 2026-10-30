<script setup lang="ts">
/**
 * The Unraid array, the Workbench's "re-lay out, never scale" reference: one
 * layout per size, from a glyph (array %) to a hero disk map. Target: the
 * array (`unraid@unraid:<instance id>/array`), its host, or with none, the
 * first array.
 *
 * Reads what the Unraid integration publishes: the array's `used.pct`,
 * `size_tb`, `parity` (`valid`, `invalid`, `checking`, `none`) and
 * `parity_checked_at`; each disk's `role`, `used.pct` and `temp.c` (unknown
 * while spun down); the host's pools; containers on the host. A running parity
 * check is in progress (◔, with `attrs['parity_check.progress']` when it's
 * there), not maintenance.
 */
import { computed } from 'vue';
import type { SizeId } from '@chadash/layout';
import Glyph from '../components/marks/Glyph.vue';
import Meter from '../components/marks/Meter.vue';
import MiniGraph from '../components/marks/MiniGraph.vue';
import type { TileStatus } from '../components/Tile.vue';
import { ago, fmtNum, fmtPct, plural } from '../lib/format';
import { attr, incoming, label, outgoing, series } from '../model/graph';
import { glyphFor } from '../model/status';
import { normalizeHealth, resourceLinks, type Resource } from '../model/types';
import { useLive } from '../stores/live';
import Frame from './parts/Frame.vue';
import StateNote from './parts/StateNote.vue';
import { arrayFor, containersOf, guestsOf } from './read';
import { useSubject } from './use';

const { size, target } = defineProps<{ size: SizeId; target?: string | undefined }>();
const live = useLive();

const arr = computed(() => arrayFor(live.graph.value, target));
const s = useSubject(() => arr.value?.id);
const host = computed(() => (arr.value ? outgoing(live.graph.value, arr.value.id, 'part_of').find((r) => r.kind === 'host') : undefined));
const used = computed(() => series(live.metrics.value, arr.value?.id ?? '', 'used.pct'));
const sizeTb = computed(() => attr<number>(arr.value, 'size_tb'));
const parity = computed(() => attr<string>(arr.value, 'parity') ?? 'none');
const checkedAt = computed(() => attr<string>(arr.value, 'parity_checked_at'));
const progress = computed(() => attr<number>(arr.value, 'parity_check.progress'));
const checking = computed(() => parity.value === 'checking');

interface Disk {
  r: Resource;
  name: string;
  parity: boolean;
  used: number | null;
  temp: number | null;
  warnTemp: number;
  status: TileStatus;
}
const disks = computed<Disk[]>(() => {
  const g = live.graph.value;
  const m = live.metrics.value;
  return incoming(g, arr.value?.id ?? '', 'part_of')
    .filter((r) => r.kind === 'disk')
    .map((r) => {
      const t = series(m, r.id, 'temp.c');
      const u = series(m, r.id, 'used.pct');
      const h = normalizeHealth(r).status;
      const usedV = u?.value ?? null;
      const temp = t?.value ?? null;
      const warnTemp = t?.warn ?? 50;
      const status: TileStatus =
        h === 'down' ? 'down' : h === 'attention' || (usedV !== null && usedV >= (u?.warn ?? 90)) || (temp !== null && temp >= warnTemp) ? 'attention' : 'ok';
      return { r, name: label(r), parity: attr(r, 'role') === 'parity', used: usedV, temp, warnTemp, status };
    })
    .sort((a, b) => Number(b.parity) - Number(a.parity) || a.name.localeCompare(b.name, undefined, { numeric: true }));
});
const pools = computed(() =>
  host.value
    ? incoming(live.graph.value, host.value.id, 'part_of')
        .filter((r) => r.kind === 'pool')
        .map((r) => ({ r, used: series(live.metrics.value, r.id, 'used.pct')?.value ?? null }))
    : [],
);
const hottest = computed(() => disks.value.filter((d) => d.temp !== null).sort((a, b) => (b.temp ?? 0) - (a.temp ?? 0))[0]);
const hottestSeries = computed(() => (hottest.value ? series(live.metrics.value, hottest.value.r.id, 'temp.c') : undefined));
const containers = computed(() => containersOf(live.graph.value, host.value).filter((c) => c.state === 'running').length);
const vms = computed(() => guestsOf(live.graph.value, host.value));
const overdue = computed(() => Boolean(checkedAt.value && live.now.value - Date.parse(checkedAt.value!) > 35 * 86_400_000));

const problemDisk = computed(() => disks.value.find((d) => d.status === 'down') ?? disks.value.find((d) => d.status === 'attention'));
const status = computed<TileStatus>(() => {
  const own = s.value.status;
  if (own === 'down' || own === 'repair' || own === 'off') return own;
  if (checking.value) return 'ok'; // in progress, not maintenance
  if (problemDisk.value?.status === 'down') return 'down';
  if (own === 'attention' || problemDisk.value || overdue.value || parity.value === 'invalid') return 'attention';
  return own === 'unknown' ? 'ok' : own;
});
const parityWords = computed(() => {
  if (checking.value) return `parity check${typeof progress.value === 'number' ? ` · ${Math.round(progress.value)}%` : ''}`;
  if (parity.value === 'valid') return 'parity valid';
  if (parity.value === 'invalid') return 'parity invalid';
  return parity.value === 'none' ? 'no parity' : `parity ${parity.value}`;
});
const tag = computed(() => {
  if (status.value === s.value.status && status.value !== 'ok') return undefined;
  if (checking.value) return parityWords.value;
  if (status.value === 'down' && problemDisk.value) return `${problemDisk.value.name} disabled`;
  if (status.value === 'attention') {
    if (problemDisk.value) return problemDisk.value.used !== null && problemDisk.value.used >= 90 ? `${problemDisk.value.name} ${Math.round(problemDisk.value.used)}%` : `${problemDisk.value.name} ${problemDisk.value.temp}°`;
    if (overdue.value) return 'parity check overdue';
    return parityWords.value;
  }
  return size === 'wide' || size === 'large' || size === 'hero' ? parityWords.value : '';
});
const tagGlyph = computed(() => (checking.value ? 'prog' : tag.value ? glyphFor(status.value) : undefined));
const downLine = computed(() =>
  status.value === 'down' && problemDisk.value && s.value.status !== 'down' ? { glyph: 'down' as const, text: `${problemDisk.value.name} disabled · parity protects data` } : undefined,
);
const checkedWords = computed(() => (checkedAt.value ? `checked ${ago(checkedAt.value, live.now.value)}` : ''));
const name = computed(() => label(host.value) || label(arr.value) || 'Array');
const ofTb = computed(() => (sizeTb.value ? `% of ${fmtNum(sizeTb.value)} TB` : '%'));
const diskTip = (d: Disk) => `${d.name}: ${d.parity ? 'parity' : d.used === null ? 'unknown' : `${Math.round(d.used)}% used`}${d.temp === null ? ', spun down' : `, ${d.temp}°`}`;
</script>

<template>
  <Frame
    :size="size"
    :name="name"
    mono="UN"
    logo="unraid"
    :context="size === 'glyph' || size === 'small' ? '' : 'Unraid · array'"
    :href="resourceLinks(host)[0]?.url"
    :subject="s"
    :status="status"
    :tag="tag"
    :tag-glyph="tagGlyph"
    :stale="Boolean(used?.stale)"
    :as-of="used?.as_of"
  >
    <template v-if="!arr">
      <div :class="size === 'glyph' ? 'gv unknown' : 'v sm word'">{{ size === 'glyph' ? '—' : 'No array' }}</div>
      <p v-if="size !== 'glyph'" class="lab">Connect Unraid to see its array here.</p>
    </template>

    <template v-else-if="size === 'glyph'">
      <div :class="['gv', { unknown: used?.value == null }]">{{ fmtPct(used?.value) }}<span class="u">%</span></div>
      <div class="gl">array</div>
    </template>

    <template v-else-if="size === 'strip' || size === 'strip-long'">
      <Meter class="sm-meter" :value="used?.value" :warn="used?.warn ?? 85" :crit="used?.crit ?? 95" :tick="used?.warn ?? 85" label="Array used" />
      <span class="sv n">{{ fmtPct(used?.value) }}<small>%</small></span>
      <span class="sx">
        <Glyph v-if="parity === 'valid'" kind="check" label="parity valid" /><template v-else>{{ parityWords }}</template><template v-if="hottest"> · {{ hottest.temp }}°</template>
      </span>
    </template>

    <template v-else-if="size === 'small'">
      <div :class="['v', { unknown: used?.value == null }]">{{ fmtPct(used?.value) }}<span class="u">%</span></div>
      <Meter :value="used?.value" :warn="used?.warn ?? 85" :crit="used?.crit ?? 95" :tick="used?.warn ?? 85" label="Array used" />
      <StateNote :subject="s" :line="downLine" />
      <div class="tf"><span>{{ parityWords }}</span></div>
    </template>

    <template v-else-if="size === 'wide'">
      <div class="split">
        <div class="col">
          <div :class="['v', { unknown: used?.value == null }]">{{ fmtPct(used?.value) }}<span class="u">{{ ofTb }}</span></div>
          <Meter :value="used?.value" :warn="used?.warn ?? 85" :crit="used?.crit ?? 95" :tick="used?.warn ?? 85" label="Array used" />
          <StateNote :subject="s" :line="downLine" />
        </div>
        <dl class="kv">
          <dt>Parity</dt>
          <dd>{{ checking ? parityWords : checkedAt ? ago(checkedAt, live.now.value) : parityWords }}</dd>
          <dt>Hottest</dt>
          <dd>{{ hottest ? `${hottest.name} ${hottest.temp}°` : '—' }}</dd>
          <dt>Containers</dt>
          <dd>{{ containers }}</dd>
        </dl>
      </div>
    </template>

    <template v-else-if="size === 'tall' || size === 'large'">
      <div class="row">
        <div :class="['v', { lg: size === 'large', unknown: used?.value == null }]">{{ fmtPct(used?.value) }}<span class="u">{{ size === 'large' ? ofTb : '%' }}</span></div>
        <span v-if="size === 'large'" class="lab">{{ parityWords }}{{ checkedWords && !checking ? ` · ${checkedWords}` : '' }}</span>
      </div>
      <Meter :value="used?.value" :warn="used?.warn ?? 85" :crit="used?.crit ?? 95" :tick="used?.warn ?? 85" label="Array used" />
      <div v-if="size === 'tall'" class="sub">{{ parityWords }}{{ checkedWords && !checking ? ` · ${checkedWords}` : '' }}</div>
      <StateNote :subject="s" :line="downLine" />
      <div class="disks" role="list" aria-label="Disks">
        <div v-for="d in disks" :key="d.r.id" class="disk" role="listitem" :aria-label="diskTip(d)">
          <span class="dn"><Glyph v-if="d.status !== 'ok'" :kind="glyphFor(d.status)" />{{ d.name }}</span>
          <span v-if="d.parity" class="sub">{{ parity === 'valid' ? 'valid' : parity }}</span>
          <Meter v-else :value="d.used" :warn="90" :crit="97" thin :label="`${d.name} used`" />
          <span class="n">{{ d.parity || d.used === null ? '' : `${Math.round(d.used)}%` }}</span>
          <span class="n">{{ d.temp === null ? '—' : `${d.temp}°` }}</span>
        </div>
      </div>
      <div class="tf">
        <span><span class="n">{{ containers }}</span> containers<template v-if="size === 'large' && vms.length"> · <span class="n">{{ vms.length }}</span> VMs</template></span>
        <span v-if="size === 'large' && pools[0]">{{ label(pools[0].r) }} <span class="n">{{ fmtPct(pools[0].used) }}%</span></span>
      </div>
    </template>

    <template v-else>
      <div class="ua-hero">
        <div class="diskmap" role="list" aria-label="Disk map">
          <div v-for="d in disks" :key="d.r.id" class="dm" role="listitem" :aria-label="diskTip(d)">
            <div :class="['bar', { par: d.parity, attn: d.status === 'attention', down: d.status === 'down' }]">
              <i :style="{ height: (d.parity ? 100 : (d.used ?? 0)) + '%' }"></i>
            </div>
            <small>{{ d.name }}</small>
            <small>{{ d.temp === null ? '—' : `${d.temp}°` }}</small>
          </div>
        </div>
        <div class="col ua-side">
          <div>
            <div :class="['v', 'lg', { unknown: used?.value == null }]">{{ fmtPct(used?.value) }}<span class="u">{{ ofTb }}</span></div>
            <Meter :value="used?.value" :warn="used?.warn ?? 85" :crit="used?.crit ?? 95" :tick="used?.warn ?? 85" label="Array used" />
            <StateNote :subject="s" :line="downLine" />
          </div>
          <div v-if="hottest">
            <div class="lab">Hottest disk · {{ hottest.name }} · 24 h</div>
            <MiniGraph
              :values="hottestSeries?.history"
              :band="hottestSeries?.band"
              :tone="(hottest.temp ?? 0) >= hottest.warnTemp ? 'attn' : 'neutral'"
              :height="48"
              :label="`Hottest disk, ${hottest.name}, temperature`"
              :format="(v: number) => `${v.toFixed(0)}°`"
            />
          </div>
          <dl class="kv">
            <dt>Containers</dt>
            <dd>{{ containers }} running</dd>
            <dt>VMs</dt>
            <dd>{{ vms.length ? plural(vms.length, 'VM') : '—' }}</dd>
            <dt>Parity</dt>
            <dd>{{ checking ? parityWords : checkedWords || parityWords }}</dd>
          </dl>
        </div>
      </div>
    </template>
  </Frame>
</template>

<style>
/* ───────────── Unraid array ───────────── */
.ua-hero {
  flex: 1;
  min-height: 0;
  display: grid;
  grid-template-columns: minmax(0, 1.2fr) minmax(0, 1fr);
  gap: var(--space-6);
}
.ua-side {
  justify-content: space-between;
  gap: var(--space-3);
}
.diskmap {
  display: grid;
  grid-auto-flow: column;
  grid-auto-columns: minmax(0, 1fr);
  gap: 6px;
  align-items: end;
  min-height: 0;
  height: 100%;
}
.dm {
  display: flex;
  flex-direction: column;
  justify-content: flex-end;
  gap: 5px;
  height: 100%;
  min-width: 0;
}
.dm .bar {
  position: relative;
  flex: 1;
  overflow: hidden;
  border-radius: var(--radius-xs);
  background: color-mix(in oklch, var(--text-3) 18%, transparent);
}
.dm .bar i {
  position: absolute;
  left: 0;
  right: 0;
  bottom: 0;
  background: color-mix(in oklch, var(--text-3) 52%, transparent);
  transition: height var(--motion-value-duration) var(--motion-value-easing);
}
.dm .bar.attn i {
  background: var(--attention);
}
.dm .bar.down {
  outline: 2px solid var(--down);
  outline-offset: -2px;
}
.dm .bar.par i {
  background: repeating-linear-gradient(135deg, var(--text-3) 0 3px, transparent 3px 6px);
}
.dm small {
  text-align: center;
  white-space: nowrap;
  overflow: hidden;
  font: var(--weight-medium) 10px/1 var(--font-num);
  color: var(--text-3);
}

@media (prefers-reduced-motion: reduce) {
  html:not([data-motion='full']) .dm .bar i {
    transition: none;
  }
}
</style>
