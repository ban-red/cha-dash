<script setup lang="ts">
/**
 * Download queue: qBittorrent (`dl.mbps`, `ul.mbps`, `leeching`, `seeding`,
 * `queued`, `attrs.connection_status`), SABnzbd (`dl.mbps`, `queue`,
 * `left.gib`; state `paused` is "paused (by you)", not a problem) and the
 * *arr queues (`queue`, the Demo Home's Radarr `queued`). Target: a client
 * instance, else the first qBittorrent, else SABnzbd; the other client gets
 * a row.
 *
 * Glyph: active downloads. Small: leeching and seeding, the speed. Wide: the
 * speed over 24 hours and each queue as a row. Tall: up to eight rows.
 *
 * Down names what waits on the client, from the graph ("Sonarr, Radarr wait
 * on it"). qBittorrent doesn't publish per-torrent items yet, so the rows are
 * the queues, each a determinate count, never a spinner.
 */
import { computed } from 'vue';
import type { SizeId } from '@chadash/layout';
import Glyph from '../components/marks/Glyph.vue';
import Sparkline from '../components/marks/Sparkline.vue';
import type { TileStatus } from '../components/Tile.vue';
import { fmtInt, fmtNum, since } from '../lib/format';
import { appsOfType, incoming, label, placeOf, resolve, series } from '../model/graph';
import { glyphFor } from '../model/status';
import { resourceLinks, type Resource } from '../model/types';
import { useLive } from '../stores/live';
import Frame from './parts/Frame.vue';
import StateNote from './parts/StateNote.vue';
import { typeOf } from './read';
import { TYPE_NAMES } from './serviceStats';
import { offWords, statusOf } from './subject';
import { useSubject } from './use';

const { size, target } = defineProps<{ size: SizeId; target?: string | undefined }>();
const live = useLive();

const app = computed(
  () => (target ? resolve(live.graph.value, target) : undefined) ?? resolve(live.graph.value, 'app:qbittorrent') ?? appsOfType(live.graph.value, 'sabnzbd')[0],
);
const s = useSubject(() => app.value?.id);
const ID = computed(() => app.value?.id ?? '');
const type = computed(() => typeOf(app.value) ?? 'qbittorrent');
const usenet = computed(() => type.value === 'sabnzbd');
const m = (k: string) => series(live.metrics.value, ID.value, k);
const dl = computed(() => m('dl.mbps'));
const ul = computed(() => m('ul.mbps'));
const leeching = computed(() => m('leeching'));
const seeding = computed(() => m('seeding'));
/** Torrents waiting (qBittorrent's `queued`), or SABnzbd's jobs (`queue`). */
const queuedT = computed(() => m(usenet.value ? 'queue' : 'queued'));
const place = computed(() => placeOf(live.graph.value, ID.value));

interface QueueRow {
  r: Resource;
  n: number | null;
  status: TileStatus;
}
const arrs = computed<QueueRow[]>(() =>
  ['sonarr', 'radarr']
    .flatMap((t) => appsOfType(live.graph.value, t))
    .map((r) => ({ r, n: (series(live.metrics.value, r.id, 'queue') ?? series(live.metrics.value, r.id, 'queued'))?.value ?? null, status: statusOf(r) })),
);
/** The other download clients beside the main one (SABnzbd next to qBittorrent). */
const others = computed(() =>
  ['qbittorrent', 'sabnzbd'].flatMap((t) => appsOfType(live.graph.value, t)).filter((r) => r.id !== ID.value),
);
const active = computed(() => (usenet.value ? queuedT.value?.value : leeching.value?.value) ?? null);
const idle = computed(() => (active.value ?? 0) === 0 && (dl.value?.value ?? 0) < 0.1 && arrs.value.every((a) => !a.n));
const dependants = computed(() =>
  app.value ? [...incoming(live.graph.value, app.value.id, 'depends_on')].filter((r) => r.kind === 'app').map((r) => label(r)) : [],
);
const downLine = computed(() =>
  s.value.status === 'down'
    ? {
        glyph: 'down' as const,
        text: [`Down ${since(s.value.health.since, live.now.value)}`.trim(), s.value.cause ? `likely: ${s.value.cause}` : '', dependants.value.length ? `${dependants.value.slice(0, 3).join(', ')} wait on it` : '']
          .filter(Boolean)
          .join(' · '),
      }
    : undefined,
);
const tag = computed(() => (s.value.status === 'ok' ? (idle.value ? 'idle' : '') : undefined));
const tone = computed(() => (s.value.status === 'attention' ? 'attn' : s.value.status === 'down' ? 'down' : 'neutral'));
const rate = (v: number) => `${fmtNum(v)} Mb/s`;
function clientWords(r: Resource, st: TileStatus): string {
  if (st === 'off') return offWords(r);
  const v = (k: string) => series(live.metrics.value, r.id, k)?.value;
  if (typeOf(r) === 'sabnzbd') return `${fmtInt(v('queue'))} in the queue · ${fmtNum(v('left.gib'))} GiB left`;
  return `${fmtInt(v('leeching'))} leeching · ${fmtInt(v('queued'))} queued`;
}
const rows = computed(() => [
  ...(app.value ? [{ id: app.value.id, name: TYPE_NAMES[type.value] ?? label(app.value), words: clientWords(app.value, s.value.status), status: s.value.status }] : []),
  ...others.value.map((r) => ({ id: r.id, name: label(r), words: clientWords(r, statusOf(r)), status: statusOf(r) })),
  ...arrs.value.map((a) => ({ id: a.r.id, name: label(a.r), words: a.n === null ? '—' : a.n === 0 ? 'queue clear' : `${fmtInt(a.n)} queued`, status: a.status })),
]);
const client = computed(() => TYPE_NAMES[type.value] ?? 'qBittorrent');
</script>

<template>
  <Frame
    :size="size"
    name="Downloads"
    :mono="usenet ? 'SB' : 'QB'"
    :context="size === 'glyph' ? '' : place ? `${client} · ${place}` : client"
    :href="resourceLinks(app)[0]?.url"
    :subject="s"
    :status="app ? undefined : 'unknown'"
    :tag="tag"
    :tag-glyph="tag ? 'ok' : undefined"
    :stale="Boolean(dl?.stale)"
    :as-of="dl?.as_of"
  >
    <template v-if="!app">
      <div :class="size === 'glyph' ? 'gv unknown' : 'v sm word'">{{ size === 'glyph' ? '—' : 'Not connected' }}</div>
      <p v-if="size !== 'glyph'" class="lab">Connect qBittorrent or SABnzbd to see downloads.</p>
    </template>

    <template v-else-if="size === 'glyph'">
      <div :class="['gv', { unknown: active === null }]">{{ fmtInt(active) }}</div>
      <div class="gl">{{ idle ? 'idle' : 'downloading' }}</div>
    </template>

    <template v-else-if="size === 'small' || size === 'strip' || size === 'strip-long'">
      <div v-if="idle && s.status === 'ok'" class="v sm word">Queue clear</div>
      <div v-else :class="['v', { unknown: dl?.value == null }]">{{ fmtNum(dl?.value) }}<span class="u">Mb/s ↓</span></div>
      <div v-if="size === 'small' && usenet" class="lab"><span class="n">{{ fmtInt(queuedT?.value) }}</span> in the queue · <span class="n">{{ fmtNum(m('left.gib')?.value) }}</span> GiB left</div>
      <div v-else-if="size === 'small'" class="lab"><span class="n">{{ fmtInt(leeching?.value) }}</span> leeching · <span class="n">{{ fmtInt(seeding?.value) }}</span> seeding</div>
      <StateNote v-if="size === 'small'" :subject="s" :line="downLine" :last-known="dl?.value != null" />
      <div v-if="size === 'small'" class="tf">
        <span v-if="usenet">{{ idle ? 'nothing downloading' : `${client}` }}</span>
        <span v-else>{{ idle ? 'nothing downloading · ' : '' }}↑ <span class="n">{{ fmtNum(ul?.value) }}</span> Mb/s</span>
      </div>
    </template>

    <template v-else>
      <div class="split">
        <div class="col">
          <div v-if="idle && s.status === 'ok'" class="v sm word">Queue clear</div>
          <div v-else :class="['v', { unknown: dl?.value == null }]">{{ fmtNum(dl?.value) }}<span class="u">Mb/s ↓</span></div>
          <div v-if="usenet" class="sub"><b class="n">{{ fmtInt(queuedT?.value) }}</b> in the queue · <b class="n">{{ fmtNum(m('left.gib')?.value) }}</b> GiB left</div>
          <div v-else class="sub"><b class="n">{{ fmtInt(leeching?.value) }}</b> leeching · <b class="n">{{ fmtInt(seeding?.value) }}</b> seeding · ↑ <b class="n">{{ fmtNum(ul?.value) }}</b></div>
          <StateNote :subject="s" :line="downLine" :last-known="dl?.value != null" />
        </div>
        <Sparkline :values="dl?.history" :band="dl?.band" :tone="tone" :stale="dl?.stale" :height="size === 'tall' ? 36 : 48" label="Download speed, 24 hours" :format="rate" />
      </div>
      <ul class="list" aria-label="Queues">
        <li v-for="q in rows.slice(0, size === 'tall' ? 8 : 3)" :key="q.id" class="li">
          <Glyph :kind="glyphFor(q.status)" :label="q.status === 'ok' ? 'ok' : q.status" />
          <span class="t">{{ q.name }}</span>
          <span class="n">{{ q.words }}</span>
        </li>
      </ul>
    </template>
  </Frame>
</template>
