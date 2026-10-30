<script setup lang="ts">
/**
 * An *arr queue (Sonarr, Radarr, Lidarr): what's queued now with 24 h of history, and
 * what's wanted. An empty queue says so ("Queue clear"), never a bare 0. Small
 * and wide page to the Queue (`attrs.queue_items`: what each download is, how
 * far along, how long is left, why one is stuck) and Wanted (`attrs.wanted_items`:
 * what's missing, newest first) lists, which scroll.
 * While its integration needs repair the counts are last-known and the note
 * offers Reconnect.
 */
import { computed } from 'vue';
import type { SizeId } from '@chadash/layout';
import MiniGraph from '../ui/marks/MiniGraph.vue';
import { duration, fmtInt, shortDate } from '../lib/format';
import { attr, label, placeOf, resolve, series } from '../model/graph';
import { resourceLinks } from '../model/types';
import { useLive } from '../stores/live';
import Frame from './parts/Frame.vue';
import StateNote from './parts/StateNote.vue';
import { useSubject } from './use';
import { useViews } from './views';

const { size, target = '' } = defineProps<{ size: SizeId; target?: string | undefined }>();
const live = useLive();

const app = computed(() => resolve(live.graph.value, target));
const s = useSubject(() => app.value?.id);
const id = computed(() => app.value?.id ?? target);
const queue = computed(() => series(live.metrics.value, id.value, 'queue') ?? series(live.metrics.value, id.value, 'queued'));
const wanted = computed(() => series(live.metrics.value, id.value, 'wanted'));
const q = computed(() => queue.value?.value ?? null);
const place = computed(() => placeOf(live.graph.value, id.value));
const tone = computed(() => (s.value.status === 'attention' ? 'attn' : s.value.status === 'down' ? 'down' : 'neutral'));
/** Small has room for the note only when it's more than attention (the tag says that). */
const noted = computed(() => size !== 'small' || (s.value.status !== 'ok' && s.value.status !== 'attention'));

interface QueueRow {
  title: string;
  detail: string;
  status: string;
  progress: number;
  left: number | null;
  client: string;
  error: string;
}
interface WantedRow {
  title: string;
  detail: string;
  at: number | null;
}
const str = (v: unknown) => (typeof v === 'string' ? v : '');
const num = (v: unknown) => (typeof v === 'number' && Number.isFinite(v) ? v : null);
const rowsOf = (key: string): Record<string, unknown>[] => {
  const v = attr<unknown>(app.value, key);
  return Array.isArray(v) ? v.filter((x): x is Record<string, unknown> => typeof x === 'object' && x !== null) : [];
};
/** What's downloading, queued or stuck (`attrs.queue_items`). */
const queueRows = computed<QueueRow[]>(() =>
  rowsOf('queue_items')
    .map((x) => ({ title: str(x['title']), detail: str(x['detail']), status: str(x['status']), progress: num(x['progress']) ?? 0, left: num(x['left_s']), client: str(x['client']), error: str(x['error']) }))
    .filter((x) => x.title),
);
/** What's missing, newest first (`attrs.wanted_items`, `at` in Unix seconds). */
const wantedRows = computed<WantedRow[]>(() =>
  rowsOf('wanted_items')
    .map((x) => ({ title: str(x['title']), detail: str(x['detail']), at: num(x['at']) }))
    .filter((x) => x.title),
);
const wantedTotal = computed(() => wanted.value?.value ?? null);
const stuck = (r: QueueRow) => r.status === 'warning' || r.status === 'error' || r.status === 'failed';
const queueWords = (r: QueueRow) =>
  stuck(r)
    ? r.error || r.status
    : [r.status === 'downloading' && r.left ? `${duration(r.left * 1000)} left` : r.status, r.client].filter(Boolean).join(' · ');
const { views, view } = useViews(() => {
  if (!app.value || (size !== 'small' && size !== 'wide')) return [];
  const out = [{ id: 'now', label: 'Now' }];
  if (queueRows.value.length) out.push({ id: 'queue', label: 'Queue' });
  if (wantedRows.value.length) out.push({ id: 'wanted', label: 'Wanted' });
  return out;
});
</script>

<template>
  <Frame
    :size="size"
    :name="label(app) || 'Queue'"
    :context="place ?? ''"
    :href="resourceLinks(app)[0]?.url"
    :subject="s"
    :status="app ? undefined : 'unknown'"
    :stale="Boolean(queue?.stale)"
    :as-of="queue?.as_of"
    :views="views"
  >
    <template v-if="!app">
      <div class="v sm unknown">—</div>
      <p class="lab">This service isn't in the graph.</p>
    </template>
    <div v-else-if="view === 'queue'" class="arr-list" role="list" tabindex="0" :aria-label="`Queue on ${label(app)}`">
      <div v-for="(r, i) in queueRows" :key="i" role="listitem" :class="['ag-row', { bad: stuck(r) }]">
        <span class="n">{{ r.progress }}%</span>
        <span>{{ r.title }}<small>{{ [r.detail, queueWords(r)].filter(Boolean).join(' · ') }}</small></span>
      </div>
      <small v-if="q !== null && q > queueRows.length" class="arr-more">+{{ q - queueRows.length }} more</small>
    </div>

    <div v-else-if="view === 'wanted'" class="arr-list" role="list" tabindex="0" :aria-label="`Wanted on ${label(app)}`">
      <div v-for="(r, i) in wantedRows" :key="i" role="listitem" class="ag-row">
        <span class="n">{{ r.at === null ? '—' : shortDate(r.at * 1000) }}</span>
        <span>{{ r.title }}<small v-if="r.detail">{{ r.detail }}</small></span>
      </div>
      <small v-if="wantedTotal !== null && wantedTotal > wantedRows.length" class="arr-more">+{{ fmtInt(wantedTotal - wantedRows.length) }} more</small>
    </div>

    <template v-else>
      <template v-if="q">
        <div class="row">
          <div class="v">{{ fmtInt(q) }}</div>
          <span class="lab">queued</span>
        </div>
        <MiniGraph :values="queue?.history" :band="queue?.band" :tone="tone" :stale="queue?.stale" :height="size === 'wide' ? 40 : 28" :label="`${label(app)} queue`" />
      </template>
      <template v-else>
        <div :class="['v', 'sm', 'word', { unknown: q === null }]">{{ q === null ? '—' : 'Queue clear' }}</div>
        <div class="lab">{{ q === null ? 'queue unknown' : 'nothing downloading' }}</div>
      </template>
      <StateNote v-if="noted" :subject="s" :last-known="q !== null" />
      <div class="tf">
        <span><span class="n">{{ fmtInt(wanted?.value) }}</span> wanted</span>
      </div>
    </template>
  </Frame>
</template>

<style>
.arr-list {
  flex: 1 1 0;
  min-height: 0;
  overflow-y: auto;
  overscroll-behavior: contain;
  scrollbar-width: thin;
}
.arr-list .ag-row.bad small {
  color: var(--attention);
}
.arr-more {
  display: block;
  padding-top: var(--space-1);
  color: var(--text-3);
  font-size: var(--type-caption);
}
</style>
