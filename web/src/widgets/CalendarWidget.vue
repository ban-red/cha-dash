<script setup lang="ts">
/**
 * Media calendar ("Up next"): the `media.calendar` capability of every Sonarr,
 * Radarr and Lidarr instance, read from their `attrs.calendar`
 * (`[{at, title, detail}]`), so renames don't break it. Target: one instance;
 * else the apps ticked in Widget settings (`options.sources`); else the
 * instances the tile config lists (`bind.sources` with capability
 * `media.calendar`, as the importer writes them); else all of them.
 * Unmonitored releases (the *arrs publish them marked) are listed only when
 * the tile's `options.unmonitored` is true, or the source's own options say
 * so (the importer's per-source `unmonitored`); they're marked "unmonitored".
 *
 * Every row has its kind's mark (an episode, a film, an album, from the
 * source's type), and Sonarr's premieres and finales ride as a label. What
 * came out in the last hour is dimmed, and today's list has a line at now.
 *
 * Strip: "Next: Starling Point 21:00 · in 3h". Wide: the next release large,
 * with the rest of today and tomorrow beside it. Panel: the week as a ribbon
 * (a dot per release) over the same. Tall and column: the agenda by day,
 * the next release marked with how long until it. Large: the week, a row
 * per day under its date.
 *
 * An instance that's down or needs repair doesn't empty the list: the others'
 * rows stay, and a note says what's missing ("Radarr down: movies missing
 * from this list").
 */
import { computed } from 'vue';
import type { SizeId } from '@chadash/layout';
import Glyph from '../components/marks/Glyph.vue';
import type { TileStatus } from '../components/Tile.vue';
import { duration, hhmm, plural, shortDate } from '../lib/format';
import { attr, calendar, distinct, label, outgoing, resolve, type CalendarEntry } from '../model/graph';
import type { GraphDoc, Resource } from '../model/types';
import { useLive } from '../stores/live';
import { useTile } from '../stores/tiles';
import { useOptions } from './options';
import Frame from './parts/Frame.vue';
import MediaMark, { type MediaKind } from './parts/MediaMark.vue';
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
const opts = useOptions('calendar');
/** The apps ticked in Widget settings, those still in the graph. */
const ticked = computed(() => (opts.value['sources'] as string[]).map((id) => live.graph.value?.resources[id]).filter((r): r is Resource => Boolean(r)));
const sources = computed<Resource[]>(() => {
  if (picked.value) return [picked.value];
  if (ticked.value.length) return ticked.value;
  if (configured.value.length) return configured.value;
  return distinct(live.graph.value).filter((r) => r.caps?.includes('media.calendar') || Array.isArray(attr(r, 'calendar')));
});
const states = computed(() => sources.value.map((r) => subjectOf(live.graph.value, live.problems.value, live.repairs.value, r.id)));
const broken = computed(() => states.value.filter((x) => x.status === 'down' || x.needsRepair));
const DAY = 86_400_000;

/** A calendar entry as the tile draws it. */
interface Row extends CalendarEntry {
  key: string;
  kind: MediaKind;
  /** Out already: the calendar keeps the last hour's. */
  aired: boolean;
  /** Sonarr's premiere or finale, lifted out of the detail ("Season finale"). */
  tag: string;
  /** The detail without it. */
  text: string;
}
/** Each source's kind of release, by the name its rows carry. */
const kinds = computed(() => {
  const by: Record<string, MediaKind> = { radarr: 'film', lidarr: 'music' };
  return new Map(sources.value.map((r) => [label(r), by[typeOf(r) ?? ''] ?? 'tv']));
});
const SPECIAL = /(?:\s*·\s*)?\b(series premiere|season premiere|midseason finale|season finale|series finale)\b/i;
function toRow(e: CalendarEntry): Row {
  const m = e.detail ? SPECIAL.exec(e.detail) : null;
  const tag = m ? m[1]!.charAt(0).toUpperCase() + m[1]!.slice(1).toLowerCase() : '';
  return {
    ...e,
    key: e.at + e.title + (e.detail ?? ''),
    kind: kinds.value.get(e.source) ?? 'tv',
    aired: Date.parse(e.at) < live.now.value,
    tag,
    text: m ? e.detail!.replace(m[0], '').trim() : (e.detail ?? ''),
  };
}
const all = computed<Row[]>(() => {
  const ids = new Set(sources.value.map((r) => r.id));
  const doc: GraphDoc | undefined = live.graph.value
    ? { resources: Object.fromEntries(Object.entries(live.graph.value.resources).filter(([id]) => ids.has(id))), relations: {} }
    : undefined;
  return calendar(doc, live.now.value, withUnmonitored)
    .filter((e) => Date.parse(e.at) < live.now.value + 7 * DAY)
    .map(toRow);
});
const horizon = computed(() => (size === 'wide' ? 2 : size === 'panel' ? 3 : 7));
const limit = computed(() => (size === 'tall' || size === 'column' ? 8 : size === 'panel' ? 6 : 4));
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
/** "in 3h 10m", or what it is when it's out. */
function until(e: Row): string {
  const ms = Date.parse(e.at) - live.now.value;
  if (ms <= 0) return e.kind === 'tv' ? 'aired' : 'out now';
  return ms < 60_000 ? 'in under a minute' : `in ${duration(ms - (ms % 60_000))}`;
}
/** When, said briefly: the time today, else the day and time. */
const when = (e: Row) => (dayWord(e.at) === 'Today' ? hhmm(e.at) : `${dayWord(e.at)} ${hhmm(e.at)}`);

const next = computed(() => all.value.find((e) => !e.aired) ?? all.value[0]);
/** The agenda by day; today's says where now falls among its rows. */
const groups = computed(() => {
  const out: { day: string; items: Row[]; nowAt: number }[] = [];
  for (const e of entries.value) {
    const day = dayWord(e.at);
    const g = out[out.length - 1];
    if (g && g.day === day) g.items.push(e);
    else out.push({ day, items: [e], nowAt: -1 });
  }
  const today = out[0]?.day === 'Today' ? out[0] : undefined;
  if (today) today.nowAt = today.items.findIndex((e) => !e.aired) === -1 ? today.items.length : today.items.findIndex((e) => !e.aired);
  return out;
});
/** Beside the next release at wide and panel: what follows it in the horizon. */
const after = computed(() => {
  const n = next.value;
  return entries.value.filter((e) => e !== n && !e.aired).slice(0, size === 'panel' ? 5 : 4);
});
/** Where the list beside the hero crosses into another day. */
const newDay = (i: number) => i === 0 ? dayWord(after.value[0]!.at) !== dayWord(next.value!.at) : dayWord(after.value[i]!.at) !== dayWord(after.value[i - 1]!.at);

const week = computed(() => {
  const start = startOfDay(live.now.value);
  return Array.from({ length: 7 }, (_, i) => {
    const from = start + i * DAY;
    const [wd = '', num = '', month = ''] = shortDate(from + 12 * 3_600_000).split(' ');
    const items = all.value.filter((e) => Date.parse(e.at) >= from && Date.parse(e.at) < from + DAY);
    return { key: from, wd, num, day: i === 0 ? 'Today' : i === 1 ? 'Tomorrow' : wd, full: `${i === 0 ? 'Today, ' : ''}${wd} ${num} ${month}`, items };
  });
});
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
      <MediaMark v-if="next" :kind="next.kind" />
      <span v-if="next" class="sx strip-next"
        >Next: <b>{{ next.title }}</b> <span class="n">{{ when(next) }}</span><template v-if="dayWord(next.at) === 'Today'"> · {{ until(next) }}</template></span
      >
      <span v-else class="sx">Nothing airing this week</span>
    </template>

    <template v-else-if="!all.length">
      <div class="v sm word">Nothing airing this week</div>
      <div class="lab">{{ broken.length ? brokenNote[0] : 'No releases in the next 7 days.' }}</div>
    </template>

    <!-- Large: the week, a row per day under its date. -->
    <div v-else-if="size === 'large'" class="list cal-week">
      <div v-for="(d, i) in week" :key="d.key" :class="['wk-day', { today: i === 0, empty: !d.items.length }]">
        <div class="cal-date" aria-hidden="true">
          <span>{{ i === 0 ? 'Today' : d.wd }}</span>
          <b class="n">{{ d.num }}</b>
        </div>
        <ul :aria-label="d.full">
          <li v-for="e in d.items.slice(0, 3)" :key="e.key" :class="['ag-row', 'cal-line', { aired: e.aired, next: e === next }]">
            <span class="n">{{ hhmm(e.at) }}</span>
            <MediaMark :kind="e.kind" />
            <span class="cal-t">{{ e.title }}<small v-if="e.text || e.unmonitored"> {{ [e.text, e.unmonitored ? 'unmonitored' : ''].filter(Boolean).join(' · ') }}</small></span>
            <span v-if="e.tag" class="cal-tag">{{ e.tag }}</span>
          </li>
          <li v-if="d.items.length > 3" class="cal-more">+{{ d.items.length - 3 }} more</li>
          <li v-if="!d.items.length" class="cal-more">Nothing</li>
        </ul>
      </div>
    </div>

    <!-- Wide and panel: the next release large, what follows beside it; panel adds the week's ribbon. -->
    <div v-else-if="size === 'wide' || size === 'panel'" :class="['cal', `cal-${size}`]">
      <ol v-if="size === 'panel'" class="cal-ribbon" aria-label="This week">
        <li v-for="(d, i) in week" :key="d.key" :class="{ today: i === 0, has: d.items.length }">
          <span class="cal-wd" aria-hidden="true">{{ i === 0 ? 'Today' : d.wd }}</span>
          <b class="n" aria-hidden="true">{{ d.num }}</b>
          <span class="cal-dots" aria-hidden="true"><i v-for="n in Math.min(3, d.items.length)" :key="n"></i></span>
          <span class="cal-sr">{{ d.full }}: {{ d.items.length ? plural(d.items.length, 'release') : 'nothing' }}</span>
        </li>
      </ol>
      <div v-if="next" class="cal-hero">
        <div class="ag-day">{{ next.aired ? 'Just out' : 'Next' }} · {{ dayWord(next.at) }}</div>
        <div class="cal-when">
          <span class="cal-time n">{{ hhmm(next.at) }}</span>
          <span class="cal-until">{{ until(next) }}</span>
        </div>
        <div class="cal-title"><MediaMark :kind="next.kind" />{{ next.title }}</div>
        <div class="cal-detail">
          <span v-if="next.tag" class="cal-tag">{{ next.tag }}</span>
          {{ [next.text, next.source, next.unmonitored ? 'unmonitored' : ''].filter(Boolean).join(' · ') }}
        </div>
      </div>
      <ul v-if="after.length" class="cal-after" aria-label="After that">
        <template v-for="(e, i) in after" :key="e.key">
          <li v-if="newDay(i)" class="ag-day" aria-hidden="true">{{ dayWord(e.at) }}</li>
          <li class="ag-row cal-line" :aria-label="`${when(e)}: ${e.title}${e.text ? `, ${e.text}` : ''}`">
            <span class="n">{{ hhmm(e.at) }}</span>
            <MediaMark :kind="e.kind" />
            <span class="cal-t">{{ e.title }}</span>
            <span v-if="e.tag" class="cal-tag">{{ e.tag }}</span>
          </li>
        </template>
      </ul>
      <p v-else-if="next && !next.aired" class="cal-after lab">Nothing else {{ size === 'wide' ? 'today or tomorrow' : 'in the next three days' }}.</p>
    </div>

    <!-- Tall and column: the agenda by day. -->
    <div v-else class="list cal-agenda">
      <template v-for="g in groups" :key="g.day">
        <div class="ag-day" aria-hidden="true">{{ g.day }}</div>
        <ul :aria-label="g.day">
          <template v-for="(e, i) in g.items" :key="e.key">
            <li v-if="i === g.nowAt" class="cal-now" aria-hidden="true"><span class="n">{{ hhmm(live.now.value) }}</span></li>
            <li :class="['ag-row', 'cal-row', { aired: e.aired, next: e === next }]">
              <span class="cal-at">
                <span class="n">{{ hhmm(e.at) }}</span>
                <small v-if="e === next || e.aired">{{ until(e) }}</small>
              </span>
              <span class="cal-body">
                <span class="cal-t"><MediaMark :kind="e.kind" />{{ e.title }}</span>
                <span v-if="e.tag" class="cal-tag">{{ e.tag }}</span>
                <small>{{ e.source }}{{ e.text ? ` · ${e.text}` : '' }}{{ e.unmonitored ? ' · unmonitored' : '' }}</small>
              </span>
            </li>
          </template>
          <li v-if="g.nowAt === g.items.length" class="cal-now" aria-hidden="true"><span class="n">{{ hhmm(live.now.value) }}</span></li>
        </ul>
      </template>
      <p v-if="!entries.length" class="lab">Nothing this week. Next: {{ next?.title }} {{ next ? dayWord(next.at) : '' }}.</p>
    </div>

    <div v-if="all.length && brokenNote.length && size !== 'strip' && size !== 'glyph'" class="note w-note">
      <Glyph :kind="status === 'down' || broken.some((x) => x.status === 'down') ? 'down' : 'repair'" /><span>{{ brokenNote.join(' · ') }}</span>
    </div>
  </Frame>
</template>

<style>
/* ───────────── Up next ───────────── */
.md-ic {
  flex: none;
  width: 13px;
  height: 13px;
  fill: none;
  stroke: currentColor;
  stroke-width: 1.3;
  stroke-linecap: round;
  stroke-linejoin: round;
  color: var(--text-3);
}
.s-strip > .md-ic {
  width: 15px;
  height: 15px;
}
.cal-sr {
  position: absolute;
  width: 1px;
  height: 1px;
  overflow: hidden;
  clip-path: inset(50%);
  white-space: nowrap;
}
.cal ul,
.cal ol,
.cal-agenda ul,
.cal-week ul {
  margin: 0;
  padding: 0;
  list-style: none;
}
/* Sonarr's premieres and finales: a quiet outlined label. */
.cal-tag {
  flex: none;
  display: inline-block;
  padding: 1px 5px;
  border: var(--hairline) solid var(--border);
  border-radius: var(--radius-full);
  font-size: var(--type-micro);
  line-height: 1.4;
  white-space: nowrap;
  color: var(--text-2);
}

/* Wide and panel: the hero beside what follows. */
.cal {
  flex: 1 1 auto;
  display: grid;
  grid-template-columns: minmax(0, 1fr) minmax(0, 1.1fr);
  grid-template-areas: 'hero after';
  gap: var(--space-2) var(--space-4);
  min-height: 0;
  overflow: hidden;
}
.cal-panel {
  grid-template-areas: 'ribbon ribbon' 'hero after';
  grid-template-rows: auto minmax(0, 1fr);
}
@container (max-width: 300px) {
  .cal {
    grid-template-columns: minmax(0, 1fr);
    grid-template-areas: 'ribbon' 'hero' 'after';
  }
}
.cal-hero {
  grid-area: hero;
  display: flex;
  flex-direction: column;
  gap: 3px;
  min-width: 0;
}
.cal-hero .ag-day {
  margin: 2px 0 1px;
}
.cal-when {
  display: flex;
  align-items: baseline;
  gap: var(--space-2);
}
.cal-time {
  font: var(--weight-medium) var(--type-title-2) / 1 var(--font-num);
  letter-spacing: -0.02em;
  font-variant-numeric: var(--figures-display);
  color: var(--text);
}
.cal-until {
  font-size: var(--type-caption);
  color: var(--text-2);
}
.cal-title {
  display: -webkit-box;
  -webkit-line-clamp: 2;
  -webkit-box-orient: vertical;
  overflow: hidden;
  font-size: var(--type-body);
  font-weight: var(--weight-medium);
  line-height: 1.3;
  color: var(--text);
}
.cal-title .md-ic {
  margin-right: 5px;
  vertical-align: -1px;
}
.cal-detail {
  display: -webkit-box;
  -webkit-line-clamp: 2;
  -webkit-box-orient: vertical;
  overflow: hidden;
  font-size: var(--type-caption);
  line-height: 1.4;
  color: var(--text-3);
}
.cal-detail .cal-tag {
  margin-right: 4px;
}
.cal-after {
  grid-area: after;
  min-width: 0;
  align-self: start;
}
.cal-after .ag-day {
  margin: var(--space-1) 0 2px;
}
.cal-after .ag-day:first-child {
  margin-top: 2px;
}

/* A row on one line: time, mark, title, its label. */
.ag-row.cal-line {
  grid-template-columns: 3.3em 13px minmax(0, 1fr) auto;
  align-items: center;
  gap: 6px;
  padding: 4px 0;
}
.cal-line .cal-t {
  min-width: 0;
  white-space: nowrap;
  overflow: hidden;
  text-overflow: ellipsis;
  color: var(--text);
}
.cal-line > .n,
.cal-at .n {
  white-space: nowrap;
}
.cal-line .cal-t small {
  display: inline;
  margin-left: 0.4em;
  font-size: var(--type-caption);
  color: var(--text-3);
}
.cal-line.aired .n,
.cal-line.aired .cal-t,
.cal-row.aired .cal-t,
.cal-row.aired .cal-at .n {
  color: var(--text-3);
}

/* Panel's week: a day each, a dot per release (three at most). */
.cal-ribbon {
  grid-area: ribbon;
  display: grid;
  grid-template-columns: repeat(7, minmax(0, 1fr));
  gap: 2px;
  padding-bottom: var(--space-2);
  border-bottom: var(--hairline) solid var(--line);
}
.cal-ribbon li {
  position: relative;
  display: flex;
  flex-direction: column;
  align-items: center;
  gap: 3px;
  min-width: 0;
}
.cal-wd {
  max-width: 100%;
  overflow: hidden;
  white-space: nowrap;
  text-overflow: ellipsis;
  font: var(--weight-medium) 9.5px/1 var(--font-num);
  letter-spacing: 0.1em;
  text-transform: uppercase;
  color: var(--text-3);
}
.cal-ribbon b {
  font-size: var(--type-small);
  font-weight: var(--weight-medium);
  line-height: 1;
  color: var(--text-3);
}
.cal-ribbon li.has b {
  color: var(--text-2);
}
.cal-ribbon li.today .cal-wd,
.cal-ribbon li.today b {
  color: var(--text);
}
.cal-ribbon li.today b {
  text-decoration: underline;
  text-decoration-thickness: 1.5px;
  text-underline-offset: 3px;
}
.cal-dots {
  display: flex;
  gap: 2px;
  height: 4px;
}
.cal-dots i {
  width: 4px;
  height: 4px;
  border-radius: var(--radius-full);
  background: var(--text-2);
}

/* Tall and column: the agenda, with the next release marked and a line at now. */
.ag-row.cal-row {
  grid-template-columns: 3.4em minmax(0, 1fr);
  padding: 5px 0;
}
.cal-at {
  display: flex;
  flex-direction: column;
  gap: 2px;
}
.cal-at small {
  font-size: var(--type-micro);
  white-space: nowrap;
  color: var(--text-3);
}
.cal-row.next .cal-at small {
  color: var(--text-2);
}
.cal-body {
  min-width: 0;
}
.cal-body .cal-t {
  display: -webkit-box;
  -webkit-line-clamp: 2;
  -webkit-box-orient: vertical;
  overflow: hidden;
  color: var(--text);
}
.cal-row.next .cal-t {
  font-weight: var(--weight-semibold);
}
.cal-body .md-ic {
  margin-right: 5px;
  vertical-align: -1px;
}
.cal-body .cal-tag {
  margin-top: 3px;
}
@container (max-width: 215px) {
  .cal-body .md-ic {
    display: none;
  }
}
.cal-body small {
  white-space: nowrap;
  overflow: hidden;
  text-overflow: ellipsis;
}
.cal-now {
  position: relative;
  display: flex;
  align-items: center;
  height: 0;
  border-top: var(--hairline) solid var(--text-2);
}
.cal-now::before {
  content: '';
  position: absolute;
  left: 0;
  width: 5px;
  height: 5px;
  border-radius: var(--radius-full);
  background: var(--text);
  transform: translate(-1px, -50%);
}
.cal-now .n {
  position: absolute;
  right: 0;
  padding-left: 4px;
  font-size: var(--type-micro);
  line-height: 1;
  color: var(--text-2);
  background: var(--tile-bg);
  transform: translateY(-50%);
}
.cal-agenda .ag-row.cal-row:has(+ .cal-now),
.cal-now + .ag-row.cal-row {
  border-top-color: transparent;
}

/* Large: the week, the date beside each day's releases. */
.cal-week {
  gap: 0;
}
.cal-week .wk-day {
  display: grid;
  grid-template-columns: 2.6em minmax(0, 1fr);
  gap: var(--space-2);
  padding: 4px 0;
  border-top: var(--hairline) solid var(--line);
}
.cal-week .wk-day:first-child {
  border-top: 0;
  padding-top: 0;
}
.cal-date {
  display: flex;
  flex-direction: column;
  gap: 2px;
  padding-top: 3px;
}
.cal-date span {
  font: var(--weight-medium) 9px/1 var(--font-num);
  letter-spacing: 0.1em;
  text-transform: uppercase;
  color: var(--text-3);
}
.cal-date b {
  font-size: var(--type-callout);
  font-weight: var(--weight-medium);
  line-height: 1;
  color: var(--text-2);
}
.wk-day.today .cal-date span,
.wk-day.today .cal-date b {
  color: var(--text);
}
.wk-day.empty .cal-date b {
  color: var(--text-3);
}
.cal-week .ag-row.cal-line {
  padding: 2px 0;
  border-top: 0;
}
.cal-week .ag-row.cal-line.next .cal-t {
  font-weight: var(--weight-semibold);
}
.cal-more {
  padding: 2px 0;
  font-size: var(--type-caption);
  line-height: 1.5;
  color: var(--text-3);
}
.wk-day.empty .cal-more {
  padding-top: 5px;
}
</style>
