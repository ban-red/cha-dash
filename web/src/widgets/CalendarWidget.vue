<script setup lang="ts">
/**
 * Media calendar ("Up next"): the `media.calendar` capability of every Sonarr
 * and Radarr instance, read from their `attrs.calendar`
 * (`[{at, title, detail}]`), so renames don't break it. Target: one instance;
 * else the instances the tile config lists (`bind.sources` with capability
 * `media.calendar`, as the importer writes them); else all of them.
 * Unmonitored releases (the *arrs publish them marked) are listed only when
 * the tile's `options.unmonitored` is true, or the source's own options say
 * so (the importer's per-source `unmonitored`); they're marked "unmonitored".
 *
 * Strip: "Next: Starling Point 04:00". Wide: today and tomorrow. Tall: tonight
 * and the week. Large: a week grid.
 *
 * An instance that's down or needs repair doesn't empty the list: the others'
 * rows stay, and a note says what's missing ("Radarr down: movies missing
 * from this list").
 */
import { computed } from 'vue';
import type { SizeId } from '@chadash/layout';
import Glyph from '../components/marks/Glyph.vue';
import type { TileStatus } from '../components/Tile.vue';
import { hhmm, shortDate } from '../lib/format';
import { attr, calendar, distinct, label, outgoing, resolve, type CalendarEntry } from '../model/graph';
import type { GraphDoc, Resource } from '../model/types';
import { useLive } from '../stores/live';
import { useTile } from '../stores/tiles';
import Frame from './parts/Frame.vue';
import { typeOf } from './read';
import { subjectOf } from './subject';

const { size, target } = defineProps<{ size: SizeId; target?: string | undefined }>();
const live = useLive();

const tile = useTile();
const picked = computed(() => (target ? resolve(live.graph.value, target) : undefined));
/** The instances the tile config names (`bind.sources`, by capability), as the apps they publish. */
const configured = computed(() =>
  (tile.config.value?.bind?.sources ?? [])
    .filter((src) => src.capability === 'media.calendar')
    .flatMap((src) => outgoing(live.graph.value, `integration:${src.instance}`, 'monitors').filter((r) => r.kind === 'app')),
);
/** Whether a source's unmonitored releases are listed: the tile's option, else that source's (the importer's per-source one). */
function withUnmonitored(r: Resource): boolean {
  const cfg = tile.config.value;
  if (cfg?.options?.['unmonitored'] === true) return true;
  return (cfg?.bind?.sources ?? []).some(
    (src) =>
      src.capability === 'media.calendar' &&
      src.options?.['unmonitored'] === true &&
      outgoing(live.graph.value, `integration:${src.instance}`, 'monitors').some((x) => x.id === r.id),
  );
}
const sources = computed<Resource[]>(() => {
  if (picked.value) return [picked.value];
  if (configured.value.length) return configured.value;
  return distinct(live.graph.value).filter((r) => r.caps?.includes('media.calendar') || Array.isArray(attr(r, 'calendar')));
});
const states = computed(() => sources.value.map((r) => subjectOf(live.graph.value, live.problems.value, live.repairs.value, r.id)));
const broken = computed(() => states.value.filter((x) => x.status === 'down' || x.needsRepair));
const DAY = 86_400_000;
const all = computed(() => {
  const ids = new Set(sources.value.map((r) => r.id));
  const doc: GraphDoc | undefined = live.graph.value
    ? { resources: Object.fromEntries(Object.entries(live.graph.value.resources).filter(([id]) => ids.has(id))), relations: {} }
    : undefined;
  return calendar(doc, live.now.value, withUnmonitored).filter((e) => Date.parse(e.at) < live.now.value + 7 * DAY);
});
const horizon = computed(() => (size === 'wide' ? 2 : size === 'panel' ? 3 : 7));
const limit = computed(() => (size === 'tall' || size === 'column' ? 8 : size === 'large' ? 40 : size === 'panel' ? 6 : 4));
const entries = computed(() => {
  const end = startOfDay(live.now.value) + horizon.value * DAY;
  return all.value.filter((e) => Date.parse(e.at) < end).slice(0, limit.value);
});
function startOfDay(ms: number): number {
  // Midnight in the display time zone, found by stepping back to where the date changes.
  const d = shortDate(ms);
  let t = ms - (ms % 3_600_000);
  for (let i = 0; i < 25 && shortDate(t - 1) === d; i++) t -= 3_600_000;
  return t;
}
function dayWord(at: string | number): string {
  const d = shortDate(at);
  if (d === shortDate(live.now.value)) return 'Today';
  if (d === shortDate(live.now.value + DAY)) return 'Tomorrow';
  return d;
}
const groups = computed(() => {
  const out: { day: string; items: CalendarEntry[] }[] = [];
  for (const e of entries.value) {
    const day = dayWord(e.at);
    const g = out[out.length - 1];
    if (g && g.day === day) g.items.push(e);
    else out.push({ day, items: [e] });
  }
  return out;
});
const week = computed(() => {
  const start = startOfDay(live.now.value);
  return Array.from({ length: 7 }, (_, i) => {
    const from = start + i * DAY;
    return { key: from, day: i === 0 ? 'Today' : shortDate(from + 12 * 3_600_000).split(' ')[0]!, items: all.value.filter((e) => Date.parse(e.at) >= from && Date.parse(e.at) < from + DAY) };
  });
});
const next = computed(() => all.value.find((e) => Date.parse(e.at) >= live.now.value) ?? all.value[0]);
const status = computed<TileStatus>(() => (broken.value.some((x) => x.status === 'down') && broken.value.length === sources.value.length ? 'down' : 'ok'));
const brokenNote = computed(() =>
  broken.value.map((x) => {
    const n = label(x.resource);
    const what = ({ radarr: 'movies', lidarr: 'albums' } as Record<string, string>)[typeOf(x.resource) ?? ''] ?? 'episodes';
    return x.status === 'down' ? `${n} down: ${what} missing from this list` : `${n} needs repair: its rows are last-known`;
  }),
);
const sourceNames = computed(() => sources.value.map((r) => label(r)).join(' · '));
</script>

<template>
  <Frame
    :size="size"
    name="Up next"
    mono="CA"
    :context="size === 'strip' ? '' : sourceNames"
    :status="status"
    :tag="broken.length ? (broken.length === sources.length && status === 'down' ? 'sources down' : `${broken.length} source${broken.length === 1 ? '' : 's'} missing`) : ''"
    :tag-glyph="broken.length ? (status === 'down' ? 'down' : 'repair') : undefined"
    :stale="broken.length > 0 && broken.length === sources.length && broken.every((x) => x.needsRepair)"
  >
    <template v-if="!sources.length">
      <div class="v sm word">Not connected</div>
      <p v-if="size !== 'strip'" class="lab">Connect Sonarr or Radarr to see what's airing.</p>
    </template>

    <template v-else-if="size === 'strip' || size === 'strip-long' || size === 'glyph'">
      <span v-if="next" class="sx strip-next">Next: <b>{{ next.title }}</b> <span class="n">{{ dayWord(next.at) === 'Today' ? hhmm(next.at) : `${dayWord(next.at)} ${hhmm(next.at)}` }}</span></span>
      <span v-else class="sx">Nothing airing this week</span>
    </template>

    <template v-else-if="!all.length">
      <div class="v sm word">Nothing airing this week</div>
      <div class="lab">{{ broken.length ? brokenNote[0] : 'No releases in the next 7 days.' }}</div>
    </template>

    <div v-else-if="size === 'large'" class="week" role="table" aria-label="This week">
      <div v-for="d in week" :key="d.key" class="wk-day" role="row">
        <div class="ag-day" role="rowheader">{{ d.day }}</div>
        <div v-for="e in d.items.slice(0, 4)" :key="e.at + e.title" class="wk-item" role="cell">
          <span class="n">{{ hhmm(e.at) }}</span> {{ e.title }}<small v-if="e.unmonitored"> · unmonitored</small>
        </div>
        <div v-if="d.items.length > 4" class="wk-item more" role="cell">+{{ d.items.length - 4 }}</div>
      </div>
    </div>

    <div v-else class="list" :role="entries.length ? 'list' : undefined">
      <p v-if="!entries.length" class="lab">Nothing today or tomorrow. Next: {{ next?.title }} {{ next ? dayWord(next.at) : '' }}.</p>
      <template v-for="g in groups" :key="g.day">
        <div class="ag-day">{{ g.day }}</div>
        <div v-for="e in g.items" :key="e.at + e.title" class="ag-row" role="listitem">
          <span class="n">{{ hhmm(e.at) }}</span>
          <span>{{ e.title }}<small>{{ e.source }}{{ e.detail ? ` · ${e.detail}` : '' }}{{ e.unmonitored ? ' · unmonitored' : '' }}</small></span>
        </div>
      </template>
    </div>

    <div v-if="all.length && brokenNote.length && size !== 'strip' && size !== 'glyph'" class="note w-note">
      <Glyph :kind="status === 'down' || broken.some((x) => x.status === 'down') ? 'down' : 'repair'" /><span>{{ brokenNote.join(' · ') }}</span>
    </div>
  </Frame>
</template>
