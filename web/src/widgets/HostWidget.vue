<script setup lang="ts">
/**
 * One host in detail: a Proxmox node (`host@proxmox:<instance id>/node/<node>`),
 * the Unraid box (`host@unraid:jupiter`), or a Docker engine. Memory is the
 * primary value, with its warn tick.
 *
 * Glyph: name and memory %. Strip: name · memory bar · CPU. Small: memory with
 * its bar, CPU, guests up/total. Wide: CPU and memory sparklines with their
 * normal bands, guests and uptime. Large: Wide plus its storage as bars and
 * the top six guests.
 *
 * Down says what goes with it ("3 guests, the router"). PVE has no
 * temperatures (only the node agent will), so temperature shows only when known.
 */
import { computed } from 'vue';
import type { SizeId } from '@chadash/layout';
import Glyph from '../components/marks/Glyph.vue';
import Meter from '../components/marks/Meter.vue';
import Sparkline from '../components/marks/Sparkline.vue';
import { fmtInt, fmtNum, fmtPct, plural, since } from '../lib/format';
import { attr, incoming, label, resolve, series } from '../model/graph';
import { glyphFor } from '../model/status';
import { resourceLinks } from '../model/types';
import { useLive } from '../stores/live';
import Frame from './parts/Frame.vue';
import StateNote from './parts/StateNote.vue';
import { guestRow, sortGuests } from './guests';
import { containersOf, guestsOf, storageOf, upFor } from './read';
import { isWithheld } from './subject';
import { useSubject } from './use';

const { size, target = '' } = defineProps<{ size: SizeId; target?: string | undefined }>();
const live = useLive();

const host = computed(() => resolve(live.graph.value, target));
const s = useSubject(() => host.value?.id);
const id = computed(() => host.value?.id ?? target);
const m = (key: string) => (isWithheld(host.value, key) ? undefined : series(live.metrics.value, id.value, key));
const mem = computed(() => m('mem.pct'));
const cpu = computed(() => m('cpu.pct'));
const temp = computed(() => m('temp.c'));
const platform = computed(() => String(attr(host.value, 'platform') ?? ''));
const context = computed(() => {
  const parts = [
    platform.value === 'unraid' ? `Unraid ${attr(host.value, 'version') ?? ''}`.trim() : platform.value === 'pve' ? 'Proxmox VE' : host.value?.kind === 'engine' ? 'Docker' : platform.value,
  ];
  const gb = attr<number>(host.value, 'mem_gb');
  if (gb) parts.push(`${gb} GB`);
  return parts.filter(Boolean).join(' · ');
});

/** VMs and LXCs; an Unraid box or a Docker engine counts its containers instead. */
const guests = computed(() => {
  const g = live.graph.value;
  const own = guestsOf(g, host.value);
  if (own.length || !host.value) return { kind: 'guests', list: own };
  return { kind: 'containers', list: containersOf(g, host.value) };
});
const running = computed(() => guests.value.list.filter((r) => r.state === 'running').length);
const guestWords = computed(() => (guests.value.list.length ? `${running.value}/${guests.value.list.length}` : '—'));
const rows = computed(() => (guests.value.kind === 'guests' ? sortGuests(guests.value.list.map((r) => guestRow(live.graph.value, live.metrics.value, r, live.now.value))) : []));
const disks = computed(() =>
  storageOf(live.graph.value, host.value)
    .map((r) => ({ r, used: series(live.metrics.value, r.id, 'used.pct') }))
    .filter((x) => x.used)
    .slice(0, 4),
);

/** What goes down with it: its guests, and the router when one of them runs it. */
const blast = computed(() => {
  const g = live.graph.value;
  const n = guests.value.list.filter((r) => r.state === 'running').length;
  const router = guests.value.list.some((gr) => incoming(g, gr.id, 'runs_on').some((x) => x.kind === 'router'));
  const parts = [n ? plural(n, guests.value.kind === 'guests' ? 'guest' : 'container') : '', router ? 'the router' : ''].filter(Boolean);
  return parts.join(', ');
});
const downLine = computed(() =>
  s.value.status === 'down'
    ? {
        glyph: 'down' as const,
        text: [`Unreachable ${since(s.value.health.since, live.now.value)}`.trim(), blast.value ? `${blast.value} go with it` : ''].filter(Boolean).join(' · '),
      }
    : undefined,
);
const tone = computed(() => (s.value.status === 'down' ? 'down' : s.value.status === 'attention' ? 'attn' : 'neutral'));
const memTone = computed(() => {
  const w = mem.value?.warn ?? 90;
  return typeof mem.value?.value === 'number' && mem.value.value >= w ? 'attn' : tone.value === 'down' ? 'down' : 'neutral';
});
const pct = (v: number) => `${Math.round(v)}%`;
</script>

<template>
  <Frame
    :size="size"
    :name="label(host) || target || 'Host'"
    :context="size === 'small' || size === 'glyph' ? '' : context"
    :href="resourceLinks(host)[0]?.url"
    :subject="s"
    :stale="Boolean(mem?.stale)"
    :as-of="mem?.as_of"
  >
    <template v-if="!host">
      <div :class="size === 'glyph' ? 'gv unknown' : 'v sm unknown'">—</div>
      <p :class="size === 'glyph' ? 'gl' : 'sub'">{{ size === 'glyph' ? 'not found' : "This host isn't in the graph any more." }}</p>
    </template>

    <template v-else-if="size === 'glyph'">
      <div :class="['gv', { unknown: mem?.value == null }]">{{ fmtPct(mem?.value) }}<span class="u">%</span></div>
      <div class="gl">memory</div>
    </template>

    <template v-else-if="size === 'strip' || size === 'strip-long'">
      <Meter class="sm-meter" :value="mem?.value" :warn="mem?.warn ?? 90" :crit="mem?.crit ?? 98" :tick="mem?.warn ?? 90" :label="`${label(host)} memory`" />
      <span class="sv n">{{ fmtPct(mem?.value) }}<small>%</small></span>
      <span class="sx">CPU <span class="n">{{ fmtPct(cpu?.value) }}%</span></span>
    </template>

    <template v-else-if="size === 'small'">
      <div :class="['v', { unknown: mem?.value == null }]">{{ fmtPct(mem?.value) }}<span class="u">% mem</span></div>
      <Meter :value="mem?.value" :warn="mem?.warn ?? 90" :crit="mem?.crit ?? 98" :tick="mem?.warn ?? 90" :label="`${label(host)} memory`" />
      <StateNote :subject="s" :line="downLine" />
      <div class="tf">
        <span>
          CPU <span class="n">{{ fmtPct(cpu?.value) }}%</span><template v-if="temp?.value != null"> · <span class="n">{{ fmtNum(temp.value) }}°</span></template>
          · <span class="n">{{ guestWords }}</span> {{ guests.kind }}
        </span>
      </div>
    </template>

    <template v-else>
      <div class="split host-split">
        <div class="col">
          <div :class="['v', { unknown: mem?.value == null }]">{{ fmtPct(mem?.value) }}<span class="u">% memory</span></div>
          <Meter :value="mem?.value" :warn="mem?.warn ?? 90" :crit="mem?.crit ?? 98" :tick="mem?.warn ?? 90" :label="`${label(host)} memory`" />
          <div class="sub">
            CPU <b class="n">{{ fmtPct(cpu?.value) }}%</b><template v-if="temp?.value != null"> · <b class="n">{{ fmtNum(temp.value) }}°C</b></template>
            · <b class="n">{{ guestWords }}</b> {{ guests.kind }} · up <b class="n">{{ upFor(attr(host, 'boot_at'), live.now.value) }}</b>
          </div>
          <StateNote :subject="s" :line="downLine" />
        </div>
        <div class="col host-sparks">
          <Sparkline :values="mem?.history" :band="mem?.band" :tone="memTone" :threshold="mem?.warn" :stale="mem?.stale" :height="size === 'large' ? 40 : 30" :label="`${label(host)} memory, 24 hours`" :format="pct" />
          <Sparkline :values="cpu?.history" :band="cpu?.band" :tone="tone === 'down' ? 'down' : 'neutral'" :stale="cpu?.stale" :height="size === 'large' ? 28 : 18" :label="`${label(host)} CPU, 24 hours`" :format="pct" />
        </div>
      </div>
      <template v-if="size === 'large'">
        <div v-if="disks.length" class="host-disks" role="list" aria-label="Storage">
          <div v-for="d in disks" :key="d.r.id" class="disk" role="listitem">
            <span class="dn">{{ label(d.r) }}</span>
            <Meter :value="d.used?.value" :warn="d.used?.warn ?? 85" :crit="d.used?.crit ?? 95" :tick="d.used?.warn ?? 85" thin :label="`${label(d.r)} used`" />
            <span class="n">{{ fmtPct(d.used?.value) }}%</span>
          </div>
        </div>
        <ul v-if="rows.length" class="list" :aria-label="`Guests on ${label(host)}`">
          <li v-for="g in rows.slice(0, 6)" :key="g.id" :class="['li', { dim: g.status === 'off' }]">
            <Glyph :kind="g.progress ? 'prog' : glyphFor(g.status)" :label="g.word || 'running'" />
            <span><span class="t">{{ g.name }}</span><small v-if="g.word">{{ g.word }}</small></span>
            <span class="n">{{ g.mem === null ? '—' : `${fmtInt(g.mem)}%` }}</span>
          </li>
        </ul>
        <p v-else-if="guests.kind === 'guests'" class="lab">No guests</p>
      </template>
    </template>
  </Frame>
</template>
