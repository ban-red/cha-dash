<script setup lang="ts">
/**
 * VMs and LXCs: of one host (`guests@proxmox:<instance id>/node/egret`), of a
 * cluster (`guests@proxmox:<instance id>/cluster`), or with no target, all of
 * them. Problems first, then work in progress, running guests, and the ones
 * stopped on purpose, dimmed: those are not a problem.
 *
 * Wide: three or four guests with status, CPU and memory. Tall: up to ten,
 * name and status glyph. Large: a table with CPU, a memory bar, uptime and
 * the last backup.
 *
 * Guests the viewer's role can't see are counted, not named, from the
 * scope's `attrs['hidden.guests']`.
 */
import { computed } from 'vue';
import type { SizeId } from '@chadash/layout';
import Glyph from '../components/marks/Glyph.vue';
import Meter from '../components/marks/Meter.vue';
import type { TileStatus } from '../components/Tile.vue';
import { fmtPct, plural } from '../lib/format';
import { attr, label, resolve } from '../model/graph';
import { glyphFor } from '../model/status';
import { resourceLinks } from '../model/types';
import { useLive } from '../stores/live';
import Frame from './parts/Frame.vue';
import StateNote from './parts/StateNote.vue';
import { guestRow, sortGuests, type GuestRow } from './guests';
import { guestsOf } from './read';
import { useSubject } from './use';

const { size, target } = defineProps<{ size: SizeId; target?: string | undefined }>();
const live = useLive();

const scope = computed(() => (target ? resolve(live.graph.value, target) : undefined));
const s = useSubject(() => scope.value?.id);
const rows = computed(() => sortGuests(guestsOf(live.graph.value, scope.value).map((r) => guestRow(live.graph.value, live.metrics.value, r, live.now.value))));
const hidden = computed(() => Number(attr(scope.value, 'hidden.guests') ?? 0));
const limit = computed(() => (size === 'tall' || size === 'column' ? 10 : size === 'large' ? 8 : size === 'panel' ? 6 : 4));
const shown = computed(() => rows.value.slice(0, limit.value));
const more = computed(() => rows.value.length - shown.value.length);
const running = computed(() => rows.value.filter((r) => r.resource.state === 'running').length);
const problems = computed(() => rows.value.filter((r) => r.status === 'down' || r.status === 'attention'));
const status = computed<TileStatus>(() => {
  const own = s.value.status;
  if (own === 'down' || own === 'repair' || own === 'maintenance') return own;
  if (problems.value.some((r) => r.status === 'down')) return 'down';
  return problems.value.length ? 'attention' : 'ok';
});
const tag = computed(() => {
  if (status.value === 'repair' || (status.value === s.value.status && status.value !== 'ok')) return undefined;
  if (problems.value.length === 1) return `${problems.value[0]!.name}: ${problems.value[0]!.word}`;
  if (problems.value.length) return `${problems.value.length} need you`;
  return '';
});
const name = computed(() => (scope.value ? `Guests · ${label(scope.value)}` : 'Guests'));
const context = computed(() => (rows.value.length ? `${running.value} of ${plural(rows.value.length, 'guest')} running` : ''));
const word = (g: GuestRow) => g.word || (g.resource.state === 'running' ? 'running' : (g.resource.state ?? ''));
const incident = computed(() => live.problems.value.find((i) => problems.value.some((r) => r.id === i.target)));
</script>

<template>
  <Frame
    :size="size"
    :name="name"
    mono="VM"
    :context="context"
    :href="resourceLinks(scope)[0]?.url"
    :subject="s"
    :status="status"
    :tag="tag"
    :tag-glyph="tag ? glyphFor(status) : undefined"
    :incident="s.problem ?? incident"
  >
    <template v-if="target && !scope">
      <div class="v sm unknown">—</div>
      <p class="lab">This host isn't in the graph any more.</p>
    </template>
    <template v-else-if="!rows.length">
      <div class="v sm word">No guests</div>
      <p class="lab">{{ scope ? `Nothing runs on ${label(scope)}.` : 'Connect Proxmox VE to see your VMs and containers.' }}</p>
    </template>

    <div v-else-if="size === 'large'" class="stbl gtbl" role="table" :aria-label="name">
      <div class="sr head" role="row">
        <span role="columnheader">Guest</span>
        <span class="r" role="columnheader">CPU</span>
        <span role="columnheader">Memory</span>
        <span class="r" role="columnheader">Up</span>
        <span class="r" role="columnheader">Backup</span>
      </div>
      <div v-for="g in shown" :key="g.id" :class="['sr', { dim: g.status === 'off' }]" role="row">
        <span class="h" role="cell"><Glyph :kind="g.progress ? 'prog' : glyphFor(g.status)" :label="word(g)" />{{ g.name }}<small v-if="g.word">{{ g.word }}</small></span>
        <span class="r n" role="cell">{{ g.cpu === null ? '—' : `${fmtPct(g.cpu)}%` }}</span>
        <span class="mem" role="cell"><Meter :value="g.mem" :warn="90" :crit="98" :tick="90" thin :label="`${g.name} memory`" /></span>
        <span class="r n" role="cell">{{ g.up }}</span>
        <span class="r n" role="cell">{{ g.backup }}</span>
      </div>
    </div>

    <ul v-else-if="size === 'tall' || size === 'column'" class="list" :aria-label="name">
      <li v-for="g in shown" :key="g.id" :class="['li', { dim: g.status === 'off' }]">
        <Glyph :kind="g.progress ? 'prog' : glyphFor(g.status)" :label="word(g)" />
        <span><span class="t">{{ g.name }}</span><small v-if="g.word">{{ g.word }}</small></span>
        <span></span>
      </li>
    </ul>

    <ul v-else class="list" :aria-label="name">
      <li v-for="g in shown" :key="g.id" :class="['li', 'g4', { dim: g.status === 'off' }]">
        <Glyph :kind="g.progress ? 'prog' : glyphFor(g.status)" :label="word(g)" />
        <span><span class="t">{{ g.name }}</span><small>{{ word(g) }}</small></span>
        <span class="n">{{ g.cpu === null ? '—' : `${fmtPct(g.cpu)}%` }}</span>
        <span class="n">{{ g.mem === null ? '—' : `${fmtPct(g.mem)}%` }}</span>
      </li>
    </ul>

    <StateNote v-if="rows.length" :subject="s" />
    <div v-if="rows.length && (more > 0 || hidden > 0)" class="tf">
      <span>{{ more > 0 ? `+${more} more` : '' }}{{ more > 0 && hidden > 0 ? ' · ' : '' }}{{ hidden > 0 ? `+${hidden} hidden` : '' }}</span>
    </div>
  </Frame>
</template>
