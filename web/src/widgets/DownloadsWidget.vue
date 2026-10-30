<script setup lang="ts">
/**
 * Download queue: qBittorrent and Transmission (`dl.mbps`, `ul.mbps`,
 * `leeching`, `seeding`, `queued`, `attrs.connection_status`), SABnzbd and
 * NZBGet (`dl.mbps`, `queue`, `left.gib`; state `paused` is "paused (by
 * you)", not a problem) and the *arr queues (`queue`, the Demo Home's Radarr
 * `queued`). Target: a client instance, else the first qBittorrent, else the
 * first of the others in DOWNLOAD_CLIENTS' order; the other clients get a
 * row.
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
import MiniGraph from '../components/marks/MiniGraph.vue';
import type { TileStatus } from '../components/Tile.vue';
import { duration, fmtInt, fmtNum, since } from '../lib/format';
import { appsOfType, attr, incoming, label, placeOf, resolve, series } from '../model/graph';
import { DOWNLOAD_CLIENTS, USENET_CLIENTS } from '../model/widgets';
import { glyphFor } from '../model/status';
import { resourceLinks, type Resource } from '../model/types';
import { useLive } from '../stores/live';
import Frame from './parts/Frame.vue';
import StateNote from './parts/StateNote.vue';
import { typeOf } from './read';
import { TYPE_NAMES } from './serviceStats';
import { offWords, statusOf } from './subject';
import { useSubject } from './use';
import { useViews } from './views';

const { size, target } = defineProps<{ size: SizeId; target?: string | undefined }>();
const live = useLive();

const app = computed(
  () =>
    (target ? resolve(live.graph.value, target) : undefined) ??
    resolve(live.graph.value, 'app:qbittorrent') ??
    DOWNLOAD_CLIENTS.map((t) => appsOfType(live.graph.value, t)[0]).find(Boolean),
);
const s = useSubject(() => app.value?.id);
const ID = computed(() => app.value?.id ?? '');
const type = computed(() => typeOf(app.value) ?? 'qbittorrent');
const usenet = computed(() => USENET_CLIENTS.includes(type.value));
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
  ['sonarr', 'radarr', 'lidarr']
    .flatMap((t) => appsOfType(live.graph.value, t))
    .map((r) => ({ r, n: (series(live.metrics.value, r.id, 'queue') ?? series(live.metrics.value, r.id, 'queued'))?.value ?? null, status: statusOf(r) })),
);
/** The other download clients beside the main one (SABnzbd next to qBittorrent, Transmission, NZBGet). */
const others = computed(() =>
  DOWNLOAD_CLIENTS.flatMap((t) => appsOfType(live.graph.value, t)).filter((r) => r.id !== ID.value),
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
  if (USENET_CLIENTS.includes(typeOf(r) ?? '')) return `${fmtInt(v('queue'))} in the queue · ${fmtNum(v('left.gib'))} GiB left`;
  return `${fmtInt(v('leeching'))} leeching · ${fmtInt(v('queued'))} queued`;
}
const rows = computed(() => [
  ...(app.value ? [{ id: app.value.id, name: TYPE_NAMES[type.value] ?? label(app.value), words: clientWords(app.value, s.value.status), status: s.value.status }] : []),
  ...others.value.map((r) => ({ id: r.id, name: label(r), words: clientWords(r, statusOf(r)), status: statusOf(r) })),
  ...arrs.value.map((a) => ({ id: a.r.id, name: label(a.r), words: a.n === null ? '—' : a.n === 0 ? 'queue clear' : `${fmtInt(a.n)} queued`, status: a.status })),
]);
interface Torrent {
  name: string;
  state: string;
  progress: number;
  dl: number;
  ul: number;
  eta: number | null;
}
const nz = (v: unknown) => (typeof v === 'number' && Number.isFinite(v) ? v : 0);
/** What's moving on a qBittorrent (its `attrs.active`): downloads first, then what uploads. */
const torrents = computed<Torrent[]>(() => {
  const v = attr<unknown>(app.value, 'active');
  if (!Array.isArray(v)) return [];
  return v
    .filter((x): x is Record<string, unknown> => typeof x === 'object' && x !== null && typeof (x as Record<string, unknown>)['name'] === 'string')
    .map((x) => ({
      name: String(x['name']),
      state: String(x['state'] ?? ''),
      progress: nz(x['progress']),
      dl: nz(x['dl_mbps']),
      ul: nz(x['ul_mbps']),
      eta: typeof x['eta_s'] === 'number' ? x['eta_s'] : null,
    }));
});
const TORRENT_ROWS: Partial<Record<SizeId, number>> = { wide: 2, panel: 4, tall: 6, column: 7, large: 8, hero: 8 };
const torrentLimit = computed(() => TORRENT_ROWS[size] ?? 3);
const torrentWords = (t: Torrent) =>
  t.state === 'downloading'
    ? [`${fmtNum(t.dl)} Mb/s ↓`, t.eta ? `${duration(t.eta * 1000)} left` : ''].filter(Boolean).join(' · ')
    : t.state === 'seeding'
      ? `↑ ${fmtNum(t.ul)} Mb/s`
      : t.state;
const torrentLead = (t: Torrent) => (t.state === 'seeding' ? '100%' : `${t.progress}%`);
interface Listed {
  name: string;
  state: string;
  progress: number;
  dl: number;
  ul: number;
  ratio: number | null;
}
/** Every torrent worth a line, worst first (`attrs.list`), for the Torrents view. */
const listed = computed<Listed[]>(() => {
  const v = attr<unknown>(app.value, 'list');
  if (!Array.isArray(v)) return [];
  return v
    .filter((x): x is Record<string, unknown> => typeof x === 'object' && x !== null && typeof (x as Record<string, unknown>)['name'] === 'string')
    .map((x) => ({ name: String(x['name']), state: String(x['state'] ?? ''), progress: nz(x['progress']), dl: nz(x['dl_mbps']), ul: nz(x['ul_mbps']), ratio: typeof x['ratio'] === 'number' ? x['ratio'] : null }));
});
const TABLE_ROWS: Partial<Record<SizeId, number>> = { small: 6, wide: 6, panel: 11 };
const tableLimit = computed(() => TABLE_ROWS[size] ?? 6);
const shownRows = computed(() => Math.min(listed.value.length, tableLimit.value));
const torrentCount = computed(() => Math.max(listed.value.length, nz(attr(app.value, 'torrents'))));
/** The row's speed: what moves now, else the share ratio. */
const rowSpeed = (t: Listed) => (t.dl > 0 ? `${fmtNum(t.dl)} ↓` : t.ul > 0 ? `${fmtNum(t.ul)} ↑` : t.ratio !== null && t.progress >= 100 ? `${fmtNum(t.ratio, 3)}×` : '');
const rowGlyph = (t: Listed) => (t.state === 'error' ? 'attn' : t.state === 'downloading' || t.state === 'fetching metadata' ? 'prog' : undefined);
const { views, view } = useViews(() =>
  app.value && !usenet.value && listed.value.length && (size === 'small' || size === 'wide' || size === 'panel')
    ? [{ id: 'now', label: 'Now' }, { id: 'torrents', label: 'Torrents' }]
    : [],
);
const GIB = 1024;
/** The line under the torrents: ratio, free space, peers, what waits on the client. */
const facts = computed(() => {
  const a = app.value;
  const ratio = attr<number>(a, 'ratio');
  const free = attr<number>(a, 'free_gib');
  const peers = attr<number>(a, 'peers');
  const waits = arrs.value.filter((q) => q.n).map((q) => `${label(q.r)} ${fmtInt(q.n)}`);
  return [
    typeof ratio === 'number' ? `ratio ${fmtNum(ratio, 3)}` : '',
    typeof free === 'number' ? `${free >= GIB ? `${fmtNum(free / GIB, 3)} TiB` : `${fmtNum(free, 3)} GiB`} free` : '',
    typeof peers === 'number' ? `${fmtInt(peers)} peers` : '',
    attr(a, 'alt_speed') === true ? 'speed limit on' : '',
    waits.length ? `queued: ${waits.join(', ')}` : '',
  ].filter(Boolean);
});
const client = computed(() => TYPE_NAMES[type.value] ?? 'qBittorrent');
const MONO: Record<string, string> = { qbittorrent: 'QB', transmission: 'TR', sabnzbd: 'SB', nzbget: 'NZ' };
</script>

<template>
  <Frame
    :size="size"
    name="Downloads"
    :mono="MONO[type] ?? (usenet ? 'SB' : 'QB')"
    :context="size === 'glyph' ? '' : place ? `${client} · ${place}` : client"
    :href="resourceLinks(app)[0]?.url"
    :subject="s"
    :status="app ? undefined : 'unknown'"
    :tag="tag"
    :tag-glyph="tag ? 'ok' : undefined"
    :stale="Boolean(dl?.stale)"
    :as-of="dl?.as_of"
    :views="views"
  >
    <template v-if="!app">
      <div :class="size === 'glyph' ? 'gv unknown' : 'v sm word'">{{ size === 'glyph' ? '—' : 'Not connected' }}</div>
      <p v-if="size !== 'glyph'" class="lab">Connect qBittorrent, Transmission, SABnzbd or NZBGet to see downloads.</p>
    </template>

    <div v-else-if="view === 'torrents'" class="dl-table" role="table" :aria-label="`Torrents on ${client}`">
      <div v-for="t in listed.slice(0, tableLimit)" :key="t.name" role="row" :class="['dl-tr', { bad: t.state === 'error' }]">
        <span role="cell" class="dl-g"><Glyph v-if="rowGlyph(t)" :kind="rowGlyph(t)!" /></span>
        <span role="cell" class="dl-name">{{ t.name }}</span>
        <span role="cell" class="dl-st">{{ t.state }}</span>
        <span role="cell" class="n dl-pct">{{ t.progress }}%</span>
        <span role="cell" class="n dl-sp">{{ rowSpeed(t) }}</span>
      </div>
      <div v-if="torrentCount > shownRows" class="dl-more">+{{ torrentCount - shownRows }} more</div>
    </div>

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
        <ul v-if="torrents.length && size === 'wide'" class="list dl-torrents dl-beside" aria-label="Torrents">
          <li v-for="t in torrents.slice(0, torrentLimit)" :key="t.name" class="ag-row">
            <span class="n">{{ torrentLead(t) }}</span>
            <span class="dl-name">{{ t.name }}<small>{{ torrentWords(t) }}</small></span>
          </li>
          <li v-if="torrents.length > torrentLimit" class="more">+{{ torrents.length - torrentLimit }} more</li>
        </ul>
        <MiniGraph v-else :values="dl?.history" :band="dl?.band" :tone="tone" :stale="dl?.stale" :height="size === 'tall' || size === 'column' ? 36 : 48" label="Download speed, 24 hours" :format="rate" />
      </div>
      <template v-if="torrents.length">
        <ul v-if="size !== 'wide'" class="list dl-torrents" aria-label="Torrents">
          <li v-for="t in torrents.slice(0, torrentLimit)" :key="t.name" class="ag-row">
            <span class="n">{{ torrentLead(t) }}</span>
            <span class="dl-name">{{ t.name }}<small>{{ torrentWords(t) }}</small></span>
          </li>
          <li v-if="torrents.length > torrentLimit" class="more">+{{ torrents.length - torrentLimit }} more</li>
        </ul>
        <div v-if="facts.length" class="tf"><span class="dl-facts">{{ facts.join(' · ') }}</span></div>
      </template>
      <ul v-else class="list" aria-label="Queues">
        <li v-for="q in rows.slice(0, size === 'tall' || size === 'column' ? 8 : size === 'panel' ? 5 : 3)" :key="q.id" class="li">
          <Glyph :kind="glyphFor(q.status)" :label="q.status === 'ok' ? 'ok' : q.status" />
          <span class="t">{{ q.name }}</span>
          <span class="n">{{ q.words }}</span>
        </li>
      </ul>
    </template>
  </Frame>
</template>

<style>
.dl-name {
  min-width: 0;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}
.dl-name small {
  display: block;
  overflow: hidden;
  text-overflow: ellipsis;
}
.dl-torrents .more {
  color: var(--text-3);
  font-size: var(--type-caption);
}
.dl-beside {
  min-width: 0;
  align-self: stretch;
  justify-content: center;
}
.dl-facts {
  min-width: 0;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.dl-table {
  display: flex;
  flex-direction: column;
  font-size: var(--type-small);
}
.dl-tr {
  display: grid;
  grid-template-columns: 12px minmax(0, 1fr) auto 3.2em 4.6em;
  gap: var(--space-2);
  align-items: center;
  padding: 2px 0;
  border-top: 1px solid var(--line);
}
.dl-tr:first-child {
  border-top: 0;
}
.dl-tr.bad .dl-name {
  color: var(--attention);
}
.dl-st {
  color: var(--text-3);
}
.dl-pct,
.dl-sp {
  text-align: right;
}
.dl-more {
  color: var(--text-3);
  font-size: var(--type-caption);
}
.dl-table .dl-name {
  display: block;
}
@container (max-width: 300px) {
  .dl-tr {
    grid-template-columns: 12px minmax(0, 1fr) 3.2em;
  }
  .dl-st,
  .dl-sp {
    display: none;
  }
}
</style>
