<script setup lang="ts">
/**
 * The Unraid server as one tile: vitals (CPU, memory, uptime), the array and
 * its parity, the hottest disk, containers, VMs and shares. `host` shows the
 * box and `unraid` the array's disks; this reads across both. Target: the
 * host, its array, or with none, the first Unraid host.
 *
 * Glyph: memory %. Strip: memory bar, CPU, array. Small: CPU, memory and array
 * as three bars. Wide: the three bars and the facts beside them. Tall: Wide
 * with the 24 h graphs. Large: Tall plus the containers that need a look.
 *
 * A stopped array or a disabled disk is down, an invalid or overdue parity, a
 * full or hot disk is attention. A running parity check is in progress.
 */
import { computed } from 'vue';
import type { SizeId } from '@chadash/layout';
import Glyph from '../components/marks/Glyph.vue';
import Meter from '../components/marks/Meter.vue';
import MiniGraph from '../components/marks/MiniGraph.vue';
import type { TileStatus } from '../components/Tile.vue';
import { ago, fmtNum, fmtPct, plural, shortDate } from '../lib/format';
import { attr, incoming, label, series } from '../model/graph';
import { glyphFor } from '../model/status';
import { normalizeHealth, resourceLinks } from '../model/types';
import { useLive } from '../stores/live';
import Frame from './parts/Frame.vue';
import StateNote from './parts/StateNote.vue';
import { arrayFor, containersOf, guestsOf, unraidHostFor, upFor } from './read';
import { isWithheld } from './subject';
import { useSubject } from './use';

const { size, target } = defineProps<{ size: SizeId; target?: string | undefined }>();
const live = useLive();

const host = computed(() => unraidHostFor(live.graph.value, target));
const arr = computed(() => arrayFor(live.graph.value, host.value?.id));
const s = useSubject(() => host.value?.id);
const m = (id: string | undefined, key: string) => (id ? series(live.metrics.value, id, key) : undefined);
const mem = computed(() => (isWithheld(host.value, 'mem.pct') ? undefined : m(host.value?.id, 'mem.pct')));
const cpu = computed(() => (isWithheld(host.value, 'cpu.pct') ? undefined : m(host.value?.id, 'cpu.pct')));
const used = computed(() => m(arr.value?.id, 'used.pct'));
const memWarn = computed(() => mem.value?.warn ?? 90);
const memCrit = computed(() => mem.value?.crit ?? 98);

const parity = computed(() => attr<string>(arr.value, 'parity') ?? 'none');
const checking = computed(() => parity.value === 'checking');
const progress = computed(() => attr<number>(arr.value, 'parity_check.progress'));
const checkedAt = computed(() => attr<string>(arr.value, 'parity_checked_at'));
const overdue = computed(() => Boolean(checkedAt.value && live.now.value - Date.parse(checkedAt.value) > 35 * 86_400_000));
const parityWords = computed(() => {
  if (checking.value) return `parity check${typeof progress.value === 'number' ? ` · ${Math.round(progress.value)}%` : ''}`;
  if (parity.value === 'valid') return 'parity valid';
  if (parity.value === 'invalid') return 'parity invalid';
  return parity.value === 'none' ? 'no parity' : `parity ${parity.value}`;
});

const disks = computed(() =>
  incoming(live.graph.value, arr.value?.id ?? '', 'part_of')
    .filter((r) => r.kind === 'disk')
    .map((r) => ({ r, name: label(r), health: normalizeHealth(r).status, temp: m(r.id, 'temp.c') })),
);
const downDisk = computed(() => disks.value.find((d) => d.health === 'down'));
const hottest = computed(() =>
  disks.value
    .filter((d) => typeof d.temp?.value === 'number')
    .sort((a, b) => (b.temp?.value ?? 0) - (a.temp?.value ?? 0))[0],
);
const hot = computed(() => Boolean(hottest.value && (hottest.value.temp?.value ?? 0) >= (hottest.value.temp?.warn ?? 50)));
const full = computed(() => typeof used.value?.value === 'number' && used.value.value >= (used.value.warn ?? 85));

const cs = computed(() => containersOf(live.graph.value, host.value));
const running = computed(() => cs.value.filter((c) => c.state === 'running').length);
const problems = computed(() => cs.value.filter((c) => ['down', 'attention'].includes(normalizeHealth(c).status)));
const vms = computed(() => guestsOf(live.graph.value, host.value));
const vmsUp = computed(() => vms.value.filter((v) => v.state === 'running').length);
const shares = computed(() => {
  const g = live.graph.value;
  return [...incoming(g, host.value?.id ?? '', 'part_of'), ...incoming(g, arr.value?.id ?? '', 'part_of')].filter((r) => r.kind === 'share').length;
});

const status = computed<TileStatus>(() => {
  const own = s.value.status;
  if (own === 'down' || own === 'repair' || own === 'off') return own;
  if (downDisk.value || arr.value?.state === 'stopped') return 'down';
  if (own === 'attention' || parity.value === 'invalid' || overdue.value || hot.value || full.value || problems.value.length) return 'attention';
  return own === 'unknown' ? 'ok' : own;
});
const tag = computed(() => {
  if (status.value === s.value.status && status.value !== 'ok') return undefined;
  if (arr.value?.state === 'stopped') return 'array stopped';
  if (downDisk.value) return `${downDisk.value.name} disabled`;
  if (parity.value === 'invalid') return 'parity invalid';
  if (overdue.value) return 'parity check overdue';
  if (hot.value && hottest.value) return `${hottest.value.name} ${Math.round(hottest.value.temp!.value!)}°`;
  if (full.value) return `array ${fmtPct(used.value?.value)}%`;
  if (problems.value.length) return plural(problems.value.length, 'container') + ' need a look';
  if (checking.value) return parityWords.value;
  return '';
});
const tagGlyph = computed(() => (status.value === 'ok' && checking.value ? 'prog' : tag.value ? glyphFor(status.value) : undefined));

const name = computed(() => label(host.value) || target || 'Unraid');
const context = computed(() => {
  const parts = [`Unraid ${attr(host.value, 'version') ?? ''}`.trim()];
  const gb = attr<number>(host.value, 'mem_gb');
  if (gb) parts.push(`${gb} GB`);
  return parts.join(' · ');
});
const pct = (v: number) => `${Math.round(v)}%`;
const memTone = computed(() => (typeof mem.value?.value === 'number' && mem.value.value >= memWarn.value ? 'attn' : 'neutral'));
const tempWords = computed(() => (hottest.value ? `${hottest.value.name} ${Math.round(hottest.value.temp!.value!)}°` : '—'));
/** The parity cell where there's little room: the date of the last check, or the check's progress. */
const parityCell = computed(() => {
  if (checking.value) return typeof progress.value === 'number' ? `${Math.round(progress.value)}%` : 'checking';
  if (parity.value === 'valid' && checkedAt.value) return shortDate(checkedAt.value).replace(/^\w+\s/, '');
  return parity.value === 'valid' ? 'valid' : parity.value === 'none' ? 'none' : parity.value;
});
const runningCell = computed(() => `${running.value}${vms.value.length ? ` · ${vmsUp.value} VM${vmsUp.value === 1 ? '' : 's'}` : ''}`);
const parityShort = computed(() => (checking.value ? parityWords.value : checkedAt.value ? `checked ${ago(checkedAt.value, live.now.value)}` : parityWords.value));
</script>

<template>
  <Frame
    :size="size"
    :name="name"
    mono="UN"
    logo="unraid"
    :context="size === 'glyph' || size === 'small' || size === 'strip' ? '' : context"
    :href="resourceLinks(host)[0]?.url"
    :subject="s"
    :status="status"
    :tag="tag"
    :tag-glyph="tagGlyph"
    :stale="Boolean(mem?.stale)"
    :as-of="mem?.as_of"
  >
    <template v-if="!host">
      <div :class="size === 'glyph' ? 'gv unknown' : 'v sm word'">{{ size === 'glyph' ? '—' : 'No Unraid server' }}</div>
      <p v-if="size !== 'glyph'" class="lab">Connect Unraid to see the server here.</p>
    </template>

    <template v-else-if="size === 'glyph'">
      <div :class="['gv', { unknown: mem?.value == null }]">{{ fmtPct(mem?.value) }}<span class="u">%</span></div>
      <div class="gl">memory</div>
    </template>

    <template v-else-if="size === 'strip' || size === 'strip-long'">
      <Meter class="sm-meter" :value="mem?.value" :warn="memWarn" :crit="memCrit" :tick="memWarn" :label="`${name} memory`" />
      <span class="sv n">{{ fmtPct(mem?.value) }}<small>%</small></span>
      <span class="sx">CPU <span class="n">{{ fmtPct(cpu?.value) }}%</span> · array <span class="n">{{ fmtPct(used?.value) }}%</span></span>
    </template>

    <template v-else>
      <div :class="['uh-body', { split: size === 'large' }]">
        <div class="uh-vitals">
        <div class="uh-bars" role="list" aria-label="Vitals">
          <div class="disk" role="listitem">
            <span class="dn">CPU</span>
            <Meter :value="cpu?.value" :warn="cpu?.warn ?? 90" :crit="cpu?.crit ?? 98" thin :label="`${name} CPU`" />
            <span class="n">{{ fmtPct(cpu?.value) }}%</span>
          </div>
          <div class="disk" role="listitem">
            <span class="dn">Memory</span>
            <Meter :value="mem?.value" :warn="memWarn" :crit="memCrit" :tick="memWarn" thin :label="`${name} memory`" />
            <span class="n">{{ fmtPct(mem?.value) }}%</span>
          </div>
          <div v-if="arr" class="disk" role="listitem">
            <span class="dn">Array</span>
            <Meter :value="used?.value" :warn="used?.warn ?? 85" :crit="used?.crit ?? 95" :tick="used?.warn ?? 85" thin :label="`${name} array used`" />
            <span class="n">{{ fmtPct(used?.value) }}%</span>
          </div>
        </div>
          <StateNote :subject="s" />
        </div>

        <div v-if="size === 'small'" class="tf">
          <span><Glyph v-if="parity === 'valid'" kind="check" label="parity valid" /><template v-else>{{ parityWords }}</template> · <span class="n">{{ running }}</span> containers</span>
        </div>

        <dl v-else-if="size === 'wide'" class="uh-facts">
          <div><dt>Up</dt><dd>{{ upFor(attr(host, 'boot_at'), live.now.value) }}</dd></div>
          <div><dt>Parity</dt><dd>{{ parityCell }}</dd></div>
          <div><dt>Hottest</dt><dd>{{ tempWords }}</dd></div>
          <div><dt>Running</dt><dd>{{ runningCell }}</dd></div>
        </dl>

        <dl v-else class="kv uh-kv">
          <dt>Up</dt>
          <dd>{{ upFor(attr(host, 'boot_at'), live.now.value) }}</dd>
          <dt>Parity</dt>
          <dd>{{ parityShort }}</dd>
          <dt>Hottest</dt>
          <dd>{{ tempWords }}</dd>
          <dt>Containers</dt>
          <dd>{{ cs.length ? `${running}/${cs.length}` : '—' }}</dd>
          <dt>VMs</dt>
          <dd>{{ vms.length ? `${vmsUp}/${vms.length}` : '—' }}</dd>
          <dt>Shares</dt>
          <dd>{{ shares || '—' }}</dd>
        </dl>
      </div>

      <template v-if="size === 'tall' || size === 'large'">
        <MiniGraph :values="mem?.history" :band="mem?.band" :tone="memTone" :threshold="mem?.warn" :stale="mem?.stale" :height="36" :label="`${name} memory, 24 hours`" :format="pct" />
        <MiniGraph :values="cpu?.history" :band="cpu?.band" :stale="cpu?.stale" :height="24" :label="`${name} CPU, 24 hours`" :format="pct" />
      </template>

      <template v-if="size === 'large'">
        <ul v-if="problems.length" class="list" :aria-label="`Containers on ${name} that need a look`">
          <li v-for="c in problems.slice(0, 4)" :key="c.id" class="li">
            <Glyph :kind="glyphFor(normalizeHealth(c).status)" :label="normalizeHealth(c).status" />
            <span><span class="t">{{ label(c) }}</span><small>{{ c.state }}</small></span>
          </li>
        </ul>
        <p v-else class="lab">{{ cs.length ? `All ${fmtNum(cs.length)} containers are fine` : 'No containers' }}</p>
      </template>
    </template>
  </Frame>
</template>

<style>
.uh-body {
  display: flex;
  flex-direction: column;
  gap: var(--space-2);
  min-height: 0;
}
.uh-body.split {
  display: grid;
  flex: 0 0 auto;
  align-items: start;
  grid-template-columns: minmax(0, 1fr) minmax(0, 1fr);
  gap: var(--space-5);
}
.uh-vitals {
  display: grid;
  gap: var(--space-2);
  min-width: 0;
}
.uh-bars {
  display: grid;
  gap: 6px;
}
.uh-bars .disk {
  grid-template-columns: 4.4em minmax(0, 1fr) 2.8em;
}
@container (max-width: 260px) {
  .uh-bars .disk {
    grid-template-columns: 4.2em minmax(0, 1fr) 2.6em;
  }
}
.uh-kv {
  margin: 0;
}
.uh-facts {
  display: grid;
  grid-template-columns: repeat(4, minmax(0, 1fr));
  gap: var(--space-2);
  margin: 0;
  padding-top: var(--space-2);
  border-top: var(--hairline) solid var(--line);
}
.uh-facts dt {
  font-size: var(--type-caption);
  color: var(--text-3);
}
.uh-facts dd {
  margin: 2px 0 0;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
  font: var(--weight-medium) var(--type-small) / 1.2 var(--font-num);
  color: var(--text-2);
}
</style>
