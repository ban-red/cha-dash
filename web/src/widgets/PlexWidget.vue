<script setup lang="ts">
/**
 * Now playing: Plex or Jellyfin (`streams`, `attrs.movies`, `attrs.shows`;
 * Jellyfin also `streams.transcode`) and Tautulli (`streams`,
 * `streams.transcode`, `bandwidth.mbps`). Target: any of those apps
 * (`plex@jellyfin:<instance id>/app`), else the first Plex, else Jellyfin,
 * else Tautulli.
 *
 * Glyph: active streams. Strip: "2 playing · 1 transcoding". Small: streams or
 * "Nothing playing", and the library. Wide: streams with transcoding and
 * bandwidth, the library, and when something last played. Large: Wide plus
 * the last 24 hours of streams.
 *
 * A broken integration is not an outage: when Plex rejects its token the tile
 * keeps last-known counts with "as of", says why, and offers Reconnect (the
 * Repair in the Inbox). Per-stream detail (who, which device, progress) and
 * recently added aren't published yet.
 */
import { computed } from 'vue';
import type { SizeId } from '@chadash/layout';
import MiniGraph from '../components/marks/MiniGraph.vue';
import type { TileStatus } from '../components/Tile.vue';
import { fmtInt, fmtNum, hhmm, plural, since } from '../lib/format';
import { appsOfType, attr, placeOf, resolve, series } from '../model/graph';
import { resourceLinks } from '../model/types';
import { useLive } from '../stores/live';
import Frame from './parts/Frame.vue';
import StateNote from './parts/StateNote.vue';
import { typeOf } from './read';
import { TYPE_NAMES } from './serviceStats';
import { useSubject } from './use';

const { size, target } = defineProps<{ size: SizeId; target?: string | undefined }>();
const live = useLive();

const app = computed(
  () =>
    (target ? resolve(live.graph.value, target) : undefined) ??
    resolve(live.graph.value, 'app:plex') ??
    appsOfType(live.graph.value, 'jellyfin')[0] ??
    appsOfType(live.graph.value, 'tautulli')[0],
);
const s = useSubject(() => app.value?.id);
const type = computed(() => typeOf(app.value) ?? 'plex');
/** Tautulli beside Plex adds transcoding and bandwidth; Jellyfin counts its own transcodes. */
const tautulli = computed(() => (type.value === 'tautulli' ? app.value : type.value === 'plex' ? appsOfType(live.graph.value, 'tautulli')[0] : undefined));
const ID = computed(() => app.value?.id ?? '');
const streams = computed(() => series(live.metrics.value, ID.value, 'streams'));
const transcode = computed(() => series(live.metrics.value, ID.value, 'streams.transcode') ?? series(live.metrics.value, tautulli.value?.id ?? '', 'streams.transcode'));
const bandwidth = computed(() => series(live.metrics.value, tautulli.value?.id ?? '', 'bandwidth.mbps'));
const n = computed(() => streams.value?.value ?? null);
const place = computed(() => placeOf(live.graph.value, ID.value));
const movies = computed(() => attr<number>(app.value, 'movies'));
const shows = computed(() => attr<number>(app.value, 'shows'));
const heavy = computed(() => (transcode.value?.value ?? 0) >= 3);
const status = computed<TileStatus>(() => (s.value.status === 'ok' && heavy.value ? 'attention' : s.value.status));
const tag = computed(() => {
  if (status.value === 'attention' && heavy.value && s.value.status === 'ok') return `${transcode.value?.value} transcoding`;
  if (status.value === 'ok') return n.value ? plural(n.value, 'stream') : n.value === 0 ? 'idle' : '';
  return undefined;
});
/** When something last played, from the streams history ("last watched 22:30"). */
const lastWatched = computed(() => {
  const h = streams.value?.history ?? [];
  const axis = live.metrics.value?.history;
  if (!axis) return '';
  for (let i = h.length - 1; i >= 0; i--) {
    if ((h[i] ?? 0) > 0) {
      const at = Date.parse(axis.start) + i * axis.step_s * 1000;
      return `last watched ${hhmm(at)}`;
    }
  }
  return '';
});
const down = computed(() => (s.value.status === 'down' ? `Movies aren't working ${since(s.value.health.since, live.now.value)}`.trim() : ''));
const library = computed(() => [movies.value !== undefined ? `${fmtInt(movies.value)} movies` : '', shows.value !== undefined ? `${fmtInt(shows.value)} shows` : ''].filter(Boolean).join(' · '));
</script>

<template>
  <Frame
    :size="size"
    name="Now playing"
    mono="PX"
    :context="size === 'glyph' ? '' : `${TYPE_NAMES[type] ?? 'Plex'}${tautulli && type !== 'tautulli' ? ' · Tautulli' : ''}${place ? ` · ${place}` : ''}`"
    :href="resourceLinks(app)[0]?.url"
    :subject="s"
    :status="app ? status : 'unknown'"
    :tag="tag"
    :tag-glyph="tag ? (status === 'attention' ? 'attn' : 'ok') : undefined"
    :stale="Boolean(streams?.stale)"
    :as-of="streams?.as_of"
  >
    <template v-if="!app">
      <div :class="size === 'glyph' ? 'gv unknown' : 'v sm word'">{{ size === 'glyph' ? '—' : 'Not connected' }}</div>
      <p v-if="size !== 'glyph'" class="lab">Connect Plex or Jellyfin to see what's playing.</p>
    </template>

    <template v-else-if="size === 'glyph'">
      <div :class="['gv', { unknown: n === null }]">{{ fmtInt(n) }}</div>
      <div class="gl">{{ n === 0 ? 'idle' : 'playing' }}</div>
    </template>

    <template v-else-if="size === 'strip' || size === 'strip-long'">
      <span class="sx strip-next">
        <template v-if="n === null">—</template>
        <template v-else-if="n === 0">Nothing playing<template v-if="lastWatched"> · {{ lastWatched }}</template></template>
        <template v-else><b class="n">{{ fmtInt(n) }}</b> playing<template v-if="transcode?.value"> · <span class="n">{{ transcode.value }}</span> transcoding</template></template>
      </span>
    </template>

    <template v-else-if="size === 'small'">
      <div v-if="n" class="v">{{ fmtInt(n) }}<span class="u">playing</span></div>
      <div v-else :class="['v', 'sm', 'word', { unknown: n === null }]">{{ n === null ? '—' : 'Nothing playing' }}</div>
      <StateNote v-if="s.status !== 'ok' || down" :subject="s" :line="down ? { glyph: 'down', text: down } : undefined" :last-known="n !== null" />
      <div v-else class="lab">{{ library || lastWatched }}</div>
    </template>

    <template v-else>
      <div class="split">
        <div class="col">
          <div v-if="n" class="v">{{ fmtInt(n) }}<span class="u">playing</span></div>
          <div v-else :class="['v', 'sm', 'word', { unknown: n === null }]">{{ n === null ? '—' : 'Nothing playing' }}</div>
          <div class="sub">
            <template v-if="s.needsRepair">last known · </template>
            <template v-if="n">
              <template v-if="transcode"><b class="n">{{ fmtInt(transcode.value) }}</b> transcoding</template>
              <template v-if="bandwidth"> · <b class="n">{{ fmtNum(bandwidth.value) }}</b> Mb/s</template>
              <template v-if="!transcode && !bandwidth">active streams</template>
            </template>
            <template v-else>{{ lastWatched || 'no active streams' }}</template>
          </div>
        </div>
        <div class="col">
          <div class="sub"><b class="n">{{ fmtInt(movies) }}</b> movies · <b class="n">{{ fmtInt(shows) }}</b> shows</div>
          <StateNote :subject="s" :line="down ? { glyph: 'down', text: down } : undefined" :last-known="n !== null" />
        </div>
      </div>
      <template v-if="size === 'large'">
        <MiniGraph :values="streams?.history" :band="streams?.band" :stale="streams?.stale || s.needsRepair" :height="56" label="Streams, 24 hours" :format="(v: number) => plural(Math.round(v), 'stream')" />
        <div class="tf"><span>Who's watching and recently added arrive when Plex publishes them</span></div>
      </template>
    </template>
  </Frame>
</template>
