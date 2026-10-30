<script setup lang="ts">
/**
 * Now playing: Plex (`streams`, `streams.transcode`, `bandwidth.mbps`;
 * `attrs.movies`, `shows`, `episodes`, `artists`, `now_playing`,
 * `recently_added`), Jellyfin (`streams`, `streams.transcode`, the library
 * counts) and Tautulli (`streams`, `streams.transcode`, `bandwidth.mbps`,
 * `now_playing`). Target: any of those apps
 * (`plex@jellyfin:<instance id>/app`), else the first Plex, else Jellyfin,
 * else Tautulli. Beside Plex, Tautulli fills in what Plex doesn't publish.
 *
 * Glyph: active streams. Strip: "2 playing · 1 transcoding · 16 Mb/s".
 * Small: streams or "Nothing playing", and what plays or the library. Wide:
 * streams with transcoding and bandwidth beside what's playing (title,
 * progress, who, direct play or transcode), or the library when idle.
 * Small and Wide page to Recently added when the app has it. Large: what's
 * playing, recently added and the last 24 hours of streams together.
 *
 * A broken integration is not an outage: when Plex rejects its token the tile
 * keeps last-known counts with "as of", says why, and offers Reconnect (the
 * Repair in the Inbox).
 */
import { computed } from 'vue';
import type { SizeId } from '@chadash/layout';
import MiniGraph from '../ui/marks/MiniGraph.vue';
import type { TileStatus } from '../components/Tile.vue';
import { ago, fmtInt, fmtNum, hhmm, plural, since } from '../lib/format';
import { appsOfType, attr, placeOf, resolve, series } from '../model/graph';
import { resourceLinks } from '../model/types';
import { useLive } from '../stores/live';
import Frame from './parts/Frame.vue';
import StateNote from './parts/StateNote.vue';
import { typeOf } from './read';
import { TYPE_NAMES } from './serviceStats';
import { useSubject } from './use';
import { useViews } from './views';

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
const bandwidth = computed(() => series(live.metrics.value, ID.value, 'bandwidth.mbps') ?? series(live.metrics.value, tautulli.value?.id ?? '', 'bandwidth.mbps'));
const n = computed(() => streams.value?.value ?? null);
const place = computed(() => placeOf(live.graph.value, ID.value));
const movies = computed(() => attr<number>(app.value, 'movies'));
const shows = computed(() => attr<number>(app.value, 'shows'));
const episodes = computed(() => attr<number>(app.value, 'episodes'));
const artists = computed(() => attr<number>(app.value, 'artists'));
const remote = computed(() => attr<number>(app.value, 'streams_remote'));

interface Playing {
  title: string;
  /** "S3 E4", or who watches and where. */
  who: string;
  state: string;
  progress: number | null;
  decision: string;
}
interface Added {
  title: string;
  detail: string;
  at: number;
}
const str = (v: unknown) => (typeof v === 'string' ? v : '');
const num = (v: unknown) => (typeof v === 'number' && Number.isFinite(v) ? v : null);
const rows = (r: typeof app.value, key: string): Record<string, unknown>[] => {
  const v = attr<unknown>(r, key);
  return Array.isArray(v) ? v.filter((x): x is Record<string, unknown> => typeof x === 'object' && x !== null) : [];
};
/** What's playing: the app's own list, else Tautulli's beside Plex. Last-known while the token is broken. */
const playing = computed<Playing[]>(() => {
  const own = rows(app.value, 'now_playing');
  const list = own.length || !tautulli.value ? own : rows(tautulli.value, 'now_playing');
  return list
    .map((x) => ({
      title: str(x['title']),
      who: [str(x['user']), str(x['player'])].filter(Boolean).join(' · '),
      state: str(x['state']),
      progress: num(x['progress']),
      decision: [str(x['decision']), x['remote'] === true ? 'remote' : ''].filter(Boolean).join(', '),
    }))
    .filter((x) => x.title);
});
/** How a row plays: "direct play", "transcode, remote", or "paused". */
const how = (p: Playing) => (p.state && p.state !== 'playing' ? p.state : p.decision);
/** Newest in the libraries (`at` in Unix seconds). */
const recent = computed<Added[]>(() =>
  rows(app.value, 'recently_added')
    .map((x) => ({ title: str(x['title']), detail: [str(x['detail']), str(x['library'])].filter(Boolean).join(' · '), at: (num(x['at']) ?? NaN) * 1000 }))
    .filter((x) => x.title && Number.isFinite(x.at)),
);
const PLAYING_ROWS = 2;
const RECENT_ROWS = 4;
const { views, view } = useViews(() => (recent.value.length && (size === 'small' || size === 'wide') ? [{ id: 'now', label: 'Now' }, { id: 'recent', label: 'Recently added' }] : []));
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
/** Under the stream count: "1 transcoding · 16 Mb/s", or "all direct · 12 Mb/s". */
const streamFacts = computed(() => {
  const out: { value: string; label: string }[] = [];
  if (transcode.value) out.push(transcode.value.value ? { value: fmtInt(transcode.value.value), label: 'transcoding' } : { value: '', label: 'all direct' });
  if (bandwidth.value) out.push({ value: fmtNum(bandwidth.value.value), label: 'Mb/s' });
  return out;
});
const playingLabel = computed(() => `Playing on ${TYPE_NAMES[type.value] ?? 'Plex'}`);
const down = computed(() => (s.value.status === 'down' ? `Movies aren't working ${since(s.value.health.since, live.now.value)}`.trim() : ''));
const library = computed(() => [movies.value !== undefined ? `${fmtInt(movies.value)} movies` : '', shows.value !== undefined ? `${fmtInt(shows.value)} shows` : ''].filter(Boolean).join(' · '));
/** The rest of the library, for the larger sizes: "14,212 episodes · 412 artists". */
const more = computed(() => [episodes.value ? `${fmtInt(episodes.value)} episodes` : '', artists.value ? `${fmtInt(artists.value)} artists` : ''].filter(Boolean).join(' · '));
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
    :views="views"
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
        <template v-else><b class="n">{{ fmtInt(n) }}</b> playing<template v-if="transcode?.value"> · <span class="n">{{ transcode.value }}</span> transcoding</template><template v-if="size === 'strip-long' && bandwidth?.value"> · <span class="n">{{ fmtNum(bandwidth.value) }}</span> Mb/s</template><template v-if="size === 'strip-long' && playing[0]"> · {{ playing[0].title }}</template></template>
      </span>
    </template>

    <div v-else-if="view === 'recent'" class="list" role="list" :aria-label="`Recently added to ${TYPE_NAMES[type] ?? 'Plex'}`">
      <div v-for="a in recent.slice(0, RECENT_ROWS)" :key="a.at + a.title" class="ag-row" role="listitem">
        <span class="n">{{ ago(a.at, live.now.value) }}</span>
        <span class="px-name">{{ a.title }}<small>{{ a.detail }}</small></span>
      </div>
    </div>

    <template v-else-if="size === 'small'">
      <div v-if="n" class="v">{{ fmtInt(n) }}<span class="u">playing</span></div>
      <div v-else :class="['v', 'sm', 'word', { unknown: n === null }]">{{ n === null ? '—' : 'Nothing playing' }}</div>
      <StateNote v-if="s.status !== 'ok' || down" :subject="s" :line="down ? { glyph: 'down', text: down } : undefined" :last-known="n !== null" />
      <div v-else-if="n && playing[0]" class="lab px-name">{{ playing[0].title }}<template v-if="playing.length > 1"> +{{ playing.length - 1 }}</template></div>
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
              <template v-for="(f, i) in streamFacts" :key="f.label">{{ i ? ' · ' : '' }}<b v-if="f.value" class="n">{{ f.value }}</b>{{ f.value ? ' ' : '' }}{{ f.label }}</template>
              <template v-if="!streamFacts.length">active streams</template>
            </template>
            <template v-else>{{ lastWatched || 'no active streams' }}</template>
          </div>
        </div>
        <div v-if="n && playing.length && size === 'wide'" class="list px-beside" role="list" :aria-label="playingLabel">
          <div v-for="p in playing.slice(0, PLAYING_ROWS)" :key="p.title + p.who" class="ag-row" role="listitem">
            <span class="n">{{ p.progress === null ? '—' : `${p.progress}%` }}</span>
            <span class="px-name">{{ p.title }}<small>{{ [p.who, how(p)].filter(Boolean).join(' · ') }}</small></span>
          </div>
          <small v-if="playing.length > PLAYING_ROWS" class="px-more">+{{ playing.length - PLAYING_ROWS }} more</small>
        </div>
        <div v-else class="col">
          <div class="sub"><b class="n">{{ fmtInt(movies) }}</b> movies · <b class="n">{{ fmtInt(shows) }}</b> shows</div>
          <div v-if="more" class="lab">{{ more }}</div>
          <StateNote :subject="s" :line="down ? { glyph: 'down', text: down } : undefined" :last-known="n !== null" />
        </div>
      </div>
      <StateNote v-if="size === 'wide' && n && playing.length" :subject="s" :line="down ? { glyph: 'down', text: down } : undefined" :last-known="n !== null" />
      <template v-if="size === 'large'">
        <div v-if="n && playing.length" class="list" role="list" :aria-label="playingLabel">
          <div v-for="p in playing.slice(0, 3)" :key="p.title + p.who" class="ag-row" role="listitem">
            <span class="n">{{ p.progress === null ? '—' : `${p.progress}%` }}</span>
            <span class="px-name">{{ p.title }}<small>{{ [p.who, how(p)].filter(Boolean).join(' · ') }}</small></span>
          </div>
        </div>
        <div v-if="recent.length" class="list" role="list" :aria-label="`Recently added to ${TYPE_NAMES[type] ?? 'Plex'}`">
          <div v-for="a in recent.slice(0, n && playing.length ? 2 : 4)" :key="a.at + a.title" class="ag-row" role="listitem">
            <span class="n">{{ ago(a.at, live.now.value) }}</span>
            <span class="px-name">{{ a.title }}<small>{{ a.detail || 'added' }}</small></span>
          </div>
        </div>
        <MiniGraph :values="streams?.history" :band="streams?.band" :stale="streams?.stale || s.needsRepair" :height="recent.length || playing.length ? 36 : 56" label="Streams, 24 hours" :format="(v: number) => plural(Math.round(v), 'stream')" />
        <div v-if="n && remote" class="tf"><span><b class="n">{{ fmtInt(remote) }}</b> of {{ fmtInt(n) }} remote</span></div>
      </template>
    </template>
  </Frame>
</template>

<style>
.px-name {
  min-width: 0;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}
.px-name small {
  overflow: hidden;
  text-overflow: ellipsis;
}
.px-beside {
  min-width: 0;
}
.px-more {
  color: var(--text-3);
  font-size: var(--type-caption);
}
</style>
