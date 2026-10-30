<script setup lang="ts">
/**
 * Calendar: events from calendar resources (kind `calendar`, `attrs.events`;
 * model/agenda.ts), from calendar feeds (integrations/ical) and Home
 * Assistant (integrations/homeassistant). Target: one calendar; else the
 * calendars ticked in Widget settings (`options.calendars`); else all of
 * them. With `options.media`, Sonarr's, Radarr's and Lidarr's releases join
 * in, as on Up next.
 *
 * Each calendar with something in the next two weeks has a monochrome mark
 * (parts/CalMarker.vue), named in a one-line legend when two to five do;
 * releases have their kind's. A home with many calendars (Home Assistant
 * publishes every one) shows the count in the header, not every name. What's on now
 * says how long is left, what's over today is dimmed, and today's list has a
 * line at now. All-day events lead their day; one running several days says
 * which day it's on.
 *
 * Strip: now or next. Small: the rest of today. Wide: now or next large,
 * beside the rest of today and tomorrow. Panel: the week as a ribbon over
 * that. Tall and column: the agenda by day for two weeks. Large: the month
 * over the next three days.
 *
 * A calendar whose source is down or needs repair keeps its last events, and
 * a note says so.
 */
import { computed } from 'vue';
import type { SizeId } from '@chadash/layout';
import Glyph from '../ui/marks/Glyph.vue';
import type { TileStatus } from '../components/Tile.vue';
import { duration, hhmm, plural, shortDate } from '../lib/format';
import { addDays, dayKey, dayStart, daysBetween, eventsOf, mediaEvents, onDay, weekday, type AgendaEvent } from '../model/agenda';
import { attr, byKind, calendar, distinct, label, resolve } from '../model/graph';
import type { GraphDoc, Resource } from '../model/types';
import { useLive } from '../stores/live';
import { useUi } from '../stores/ui';
import { useOptions } from './options';
import CalMarker from './parts/CalMarker.vue';
import Frame from './parts/Frame.vue';
import MediaMark, { type MediaKind } from './parts/MediaMark.vue';
import { typeOf } from './read';
import { subjectOf } from './subject';

const { size, target } = defineProps<{ size: SizeId; target?: string | undefined }>();
const live = useLive();
const ui = useUi();
const opts = useOptions('agenda');

const byName = (a: Resource, b: Resource) => label(a).localeCompare(label(b));
const picked = computed(() => {
  const t = target ? resolve(live.graph.value, target) : undefined;
  return t?.kind === 'calendar' ? t : undefined;
});
/** The calendars shown, in name order (their marks follow it). */
const calendars = computed<Resource[]>(() => {
  if (picked.value) return [picked.value];
  const g = live.graph.value;
  const ticked = (opts.value['calendars'] as string[]).map((id) => g?.resources[id]).filter((r): r is Resource => r?.kind === 'calendar');
  return [...(ticked.length ? ticked : byKind(g, 'calendar'))].sort(byName);
});
/** The apps whose releases join in, with the media option on: Up next's sources. */
const mediaApps = computed<Resource[]>(() =>
  opts.value['media'] === true ? distinct(live.graph.value).filter((r) => r.caps?.includes('media.calendar') || Array.isArray(attr(r, 'calendar'))).sort(byName) : [],
);
const sources = computed(() => [...calendars.value, ...mediaApps.value]);

const events = computed<AgendaEvent[]>(() => {
  const own = eventsOf(calendars.value);
  if (!mediaApps.value.length) return own;
  const ids = new Set(mediaApps.value.map((r) => r.id));
  const g = live.graph.value;
  const doc: GraphDoc | undefined = g ? { resources: Object.fromEntries(Object.entries(g.resources).filter(([id]) => ids.has(id))), relations: {} } : undefined;
  return [...own, ...mediaEvents(calendar(doc, live.now.value), mediaApps.value)].sort((a, b) => a.startMs - b.startMs || Number(b.allDay) - Number(a.allDay));
});

/** The calendars with something in the window, in name order: only they get marks. */
const active = computed(() => {
  const ids = new Set(events.value.map((e) => e.source.id));
  return calendars.value.filter((r) => ids.has(r.id));
});
/** Each calendar's mark, by its place among those; a release's kind. */
const markOf = computed(() => new Map(active.value.map((r, i) => [r.id, i])));
const MEDIA: Record<string, MediaKind> = { radarr: 'film', lidarr: 'music' };
const mediaKind = (e: AgendaEvent): MediaKind => MEDIA[typeOf(e.source) ?? ''] ?? 'tv';

const now = computed(() => live.now.value);
const today = computed(() => dayKey(now.value));
const WINDOW = 14;
const isNow = (e: AgendaEvent) => !e.allDay && e.startMs <= now.value && now.value < e.endMs;
const isOver = (e: AgendaEvent) => !e.allDay && (e.endMs > e.startMs ? e.endMs <= now.value : e.startMs < now.value);
const until = (ms: number) => (ms < 60_000 ? 'under a minute' : duration(ms - (ms % 60_000)));

function dayWord(day: string): string {
  if (day === today.value) return 'Today';
  if (day === addDays(today.value, 1)) return 'Tomorrow';
  return shortDate(dayStart(day) + 12 * 3_600_000);
}
/** What goes in the time column on a day: the start, "until 10:00" on a long event's last day, else "All day". */
function timeOn(e: AgendaEvent, day: string): string {
  if (e.allDay) return 'All day';
  if (e.startDay === day) return hhmm(e.startMs);
  if (e.endDay === day) return `until ${hhmm(e.endMs)}`;
  return 'All day';
}
/** "–10:30" under a single day's start. */
const endOn = (e: AgendaEvent, day: string) => (!e.allDay && e.startDay === day && e.endDay === day && e.endMs > e.startMs ? `–${hhmm(e.endMs)}` : '');
/** "day 2 of 3" for an event over several days. */
const spanOn = (e: AgendaEvent, day: string) => (e.endDay > e.startDay ? `day ${daysBetween(e.startDay, day) + 1} of ${daysBetween(e.startDay, e.endDay) + 1}` : '');
/** The line under a title: where, and which calendar (or the release's detail and app). */
const detailOf = (e: AgendaEvent, withSource: boolean) =>
  [e.media ? e.media.detail : e.location, withSource || e.media ? label(e.source) : ''].filter(Boolean).join(' · ');

/** The days ahead with something on, today always first when it has anything. */
const days = computed(() => {
  const out: { day: string; items: AgendaEvent[]; nowAt: number }[] = [];
  for (let i = 0; i < WINDOW; i++) {
    const day = addDays(today.value, i);
    const items = onDay(events.value, day);
    if (!items.length) continue;
    // Today's line at now: after what's over and before what's on or ahead (all-day events stay above it).
    const nowAt = i === 0 ? items.findIndex((e) => !e.allDay && e.startDay === day && !isOver(e)) : -1;
    out.push({ day, items, nowAt: i === 0 && nowAt === -1 ? items.length : nowAt });
  }
  return out;
});
/** The agenda capped at a size's rows, whole days first. */
const rows = computed(() => (size === 'tall' ? 10 : size === 'column' ? 12 : 4));
const agenda = computed(() => {
  let left = rows.value;
  const out: typeof days.value = [];
  for (const d of days.value) {
    if (left <= 0) break;
    const items = d.items.slice(0, left);
    out.push({ ...d, items });
    left -= items.length;
  }
  return out;
});

/** What's on now, else next; else today's all-day event. */
const hero = computed(() => {
  const ahead = events.value.filter((e) => e.endDay >= today.value);
  return ahead.find((e) => isNow(e)) ?? ahead.find((e) => !e.allDay && e.startMs > now.value) ?? ahead.find((e) => e.allDay && e.startDay <= today.value);
});
/** Beside the hero at wide and panel: the rest of today and tomorrow. */
const after = computed(() => {
  const h = hero.value;
  const out: { day: string; e: AgendaEvent }[] = [];
  for (const day of [today.value, addDays(today.value, 1), ...(size === 'panel' ? [addDays(today.value, 2)] : [])])
    for (const e of onDay(events.value, day)) if (e !== h && !isOver(e) && !(day !== e.startDay && !e.allDay)) out.push({ day, e });
  return out.slice(0, size === 'panel' ? 5 : 4);
});
/** Small: what's left of today. */
const rest = computed(() => onDay(events.value, today.value).filter((e) => !isOver(e)));
const later = computed(() => events.value.find((e) => e.startDay > today.value));

/** The week from today, for the ribbon: how many things each day. */
const week = computed(() =>
  Array.from({ length: 7 }, (_, i) => {
    const day = addDays(today.value, i);
    const [wd = ''] = shortDate(dayStart(day) + 12 * 3_600_000).split(' ');
    return { day, wd: i === 0 ? 'Today' : wd, num: Number(day.slice(8)), n: onDay(events.value, day).length, full: `${i === 0 ? 'Today, ' : ''}${shortDate(dayStart(day) + 12 * 3_600_000)}` };
  }),
);
/** This month's grid, Monday first, with how many things each day (as far as the calendars reach). */
const month = computed(() => {
  const first = `${today.value.slice(0, 8)}01`;
  const nextFirst = `${addDays(first, 32).slice(0, 8)}01`;
  const lead = weekday(first);
  const cells = Math.ceil((lead + daysBetween(first, nextFirst)) / 7) * 7;
  const name = new Date(`${first}T12:00:00Z`).toLocaleDateString('en-GB', { month: 'long', year: 'numeric', timeZone: 'UTC' });
  const list = Array.from({ length: cells }, (_, i) => {
    const day = addDays(first, i - lead);
    return { day, num: Number(day.slice(8)), inMonth: day >= first && day < nextFirst, today: day === today.value, past: day < today.value, n: onDay(events.value, day).length };
  });
  return { name, weeks: Array.from({ length: cells / 7 }, (_, w) => list.slice(w * 7, w * 7 + 7)) };
});
const WEEKDAYS = [
  ['M', 'Monday'],
  ['T', 'Tuesday'],
  ['W', 'Wednesday'],
  ['T', 'Thursday'],
  ['F', 'Friday'],
  ['S', 'Saturday'],
  ['S', 'Sunday'],
] as const;
/** Large: the next three days, under the month. */
const soon = computed(() => days.value.filter((d) => daysBetween(today.value, d.day) < 3));

const states = computed(() => sources.value.map((r) => subjectOf(live.graph.value, live.problems.value, live.repairs.value, r.id)));
const broken = computed(() => states.value.filter((x) => x.status === 'down' || x.needsRepair));
const status = computed<TileStatus>(() => (broken.value.length && broken.value.length === sources.value.length && broken.value.some((x) => x.status === 'down') ? 'down' : 'ok'));
const brokenNote = computed(() =>
  broken.value.map((x) => `${label(x.resource)} ${x.status === 'down' ? 'is down: its events are missing' : 'needs repair: its events are last-known'}`),
);
/** The legend, where two to five calendars share what's shown; past five the marks repeat, so it would mislead. */
const legend = computed(() => (active.value.length > 1 && active.value.length <= 5 && size !== 'strip' && size !== 'small' ? active.value : []));
/** The header's context: the sources by name while there are two at most, else how many. */
const sourceNames = computed(() =>
  sources.value.length <= 2
    ? sources.value.map((r) => label(r)).join(' · ')
    : [calendars.value.length ? plural(calendars.value.length, 'calendar') : '', mediaApps.value.length ? 'releases' : ''].filter(Boolean).join(' · '),
);
const openSettings = () => ui.openSettings('integrations', 'new');
</script>

<template>
  <Frame
    :size="size"
    name="Calendar"
    mono="CL"
    :context="size === 'strip' || size === 'small' ? '' : sourceNames"
    :status="status"
    :tag="broken.length ? `${plural(broken.length, 'source')} ${broken.length === 1 ? 'is' : 'are'} out` : ''"
    :tag-glyph="broken.length ? (status === 'down' ? 'down' : 'repair') : undefined"
    :stale="broken.length > 0 && broken.length === sources.length && broken.every((x) => x.needsRepair)"
  >
    <template v-if="!sources.length">
      <div class="v sm word">Not connected</div>
      <p v-if="size !== 'strip'" class="lab">Add a calendar feed or connect Home Assistant to see your calendars here.</p>
      <div v-if="size !== 'strip'" class="tf">
        <span><button type="button" class="linkish" @click.stop="openSettings">Add a calendar</button></span>
      </div>
    </template>

    <template v-else-if="size === 'strip'">
      <template v-if="hero">
        <MediaMark v-if="hero.media" :kind="mediaKind(hero)" />
        <CalMarker v-else :index="markOf.get(hero.source.id) ?? 0" />
        <span class="sx ev-strip"
          >{{ isNow(hero) ? 'Now' : hero.allDay ? dayWord(hero.startDay < today ? today : hero.startDay) : 'Next' }}: <b>{{ hero.title }}</b>
          <template v-if="isNow(hero)"> · ends in {{ until(hero.endMs - now) }}</template>
          <template v-else-if="!hero.allDay">
            {{ ' ' }}<span class="n">{{ hero.startDay === today ? hhmm(hero.startMs) : `${dayWord(hero.startDay)} ${hhmm(hero.startMs)}` }}</span
            ><template v-if="hero.startDay === today"> · in {{ until(hero.startMs - now) }}</template></template
          ></span
        >
      </template>
      <span v-else class="sx">Nothing in the next two weeks</span>
    </template>

    <template v-else-if="!events.length">
      <div class="v sm word">Nothing ahead</div>
      <div class="lab">{{ broken.length ? brokenNote[0] : 'Nothing in the next two weeks.' }}</div>
    </template>

    <!-- Small: what's left of today. -->
    <div v-else-if="size === 'small'" class="ev-small">
      <div class="ag-day">Today</div>
      <ul v-if="rest.length" aria-label="Today">
        <li v-for="e in rest.slice(0, 3)" :key="e.key" :class="['ev-line', { now: isNow(e) }]">
          <span class="n">{{ isNow(e) ? 'now' : timeOn(e, today) }}</span>
          <span class="ev-t">{{ e.title }}</span>
        </li>
        <li v-if="rest.length > 3" class="ev-more">+{{ rest.length - 3 }} more</li>
      </ul>
      <template v-else>
        <p class="lab">Nothing else today.</p>
        <p v-if="later" class="lab">
          Next: <b>{{ later.title }}</b> {{ dayWord(later.startDay) }}{{ later.allDay ? '' : ` ${hhmm(later.startMs)}` }}
        </p>
      </template>
    </div>

    <!-- Large: the month over the next three days. -->
    <div v-else-if="size === 'large'" class="ev-large">
      <table class="ev-month">
        <caption>
          {{ month.name }}
        </caption>
        <thead>
          <tr>
            <th v-for="(w, i) in WEEKDAYS" :key="i" scope="col"><abbr :title="w[1]">{{ w[0] }}</abbr></th>
          </tr>
        </thead>
        <tbody>
          <tr v-for="(wk, i) in month.weeks" :key="i">
            <td v-for="c in wk" :key="c.day" :class="{ out: !c.inMonth, today: c.today, past: c.past, has: c.n }">
              <span class="n">{{ c.num }}</span>
              <span class="ev-dots" aria-hidden="true"><i v-for="k in Math.min(3, c.n)" :key="k"></i></span>
              <span v-if="c.n || c.today" class="ev-sr">{{ c.today ? 'today, ' : '' }}{{ c.n ? plural(c.n, 'event') : 'nothing on' }}</span>
            </td>
          </tr>
        </tbody>
      </table>
      <div class="list ev-soon">
        <template v-for="d in soon" :key="d.day">
          <div class="ag-day" aria-hidden="true">{{ dayWord(d.day) }}</div>
          <ul :aria-label="dayWord(d.day)">
            <li v-for="e in d.items" :key="e.key" :class="['ev-line', { over: d.day === today && isOver(e), now: isNow(e) }]">
              <span class="n">{{ isNow(e) ? 'now' : timeOn(e, d.day) }}</span>
              <MediaMark v-if="e.media" :kind="mediaKind(e)" />
              <CalMarker v-else :index="markOf.get(e.source.id) ?? 0" />
              <span class="ev-t">{{ e.title }}<small v-if="detailOf(e, false) || spanOn(e, d.day)">{{ [spanOn(e, d.day), detailOf(e, false)].filter(Boolean).join(' · ') }}</small></span>
              <span class="ev-sr">{{ label(e.source) }}</span>
            </li>
          </ul>
        </template>
        <p v-if="!soon.length" class="lab">Nothing in the next three days.</p>
      </div>
    </div>

    <!-- Wide and panel: now or next large, the rest beside it; panel adds the week. -->
    <div v-else-if="size === 'wide' || size === 'panel'" :class="['ev', `ev-${size}`]">
      <ol v-if="size === 'panel'" class="ev-ribbon" aria-label="This week">
        <li v-for="(d, i) in week" :key="d.day" :class="{ today: i === 0, has: d.n }">
          <span class="ev-wd" aria-hidden="true">{{ d.wd }}</span>
          <b class="n" aria-hidden="true">{{ d.num }}</b>
          <span class="ev-dots" aria-hidden="true"><i v-for="k in Math.min(3, d.n)" :key="k"></i></span>
          <span class="ev-sr">{{ d.full }}: {{ d.n ? plural(d.n, 'event') : 'nothing on' }}</span>
        </li>
      </ol>
      <div v-if="hero" class="ev-hero">
        <div class="ag-day">{{ isNow(hero) ? 'Now' : hero.allDay ? 'All day' : 'Next' }} · {{ dayWord(hero.startDay < today ? today : hero.startDay) }}</div>
        <div class="ev-when">
          <span class="ev-time n">{{ hero.allDay ? 'All day' : `${hhmm(hero.startMs)}${hero.endMs > hero.startMs && hero.endDay === hero.startDay ? `–${hhmm(hero.endMs)}` : ''}` }}</span>
          <span v-if="!hero.allDay" class="ev-until">{{ isNow(hero) ? `ends in ${until(hero.endMs - now)}` : `in ${until(hero.startMs - now)}` }}</span>
        </div>
        <div class="ev-title">
          <MediaMark v-if="hero.media" :kind="mediaKind(hero)" />
          <CalMarker v-else :index="markOf.get(hero.source.id) ?? 0" />{{ hero.title }}
        </div>
        <div class="ev-detail">{{ [spanOn(hero, hero.startDay < today ? today : hero.startDay), detailOf(hero, true)].filter(Boolean).join(' · ') }}</div>
      </div>
      <ul v-if="after.length" class="ev-after" aria-label="After that">
        <template v-for="(x, i) in after" :key="x.e.key + x.day">
          <li v-if="x.day !== (i ? after[i - 1]!.day : today)" class="ag-day" aria-hidden="true">{{ dayWord(x.day) }}</li>
          <li class="ev-line" :aria-label="`${dayWord(x.day)} ${timeOn(x.e, x.day)}: ${x.e.title}, ${label(x.e.source)}`">
            <span class="n">{{ timeOn(x.e, x.day) }}</span>
            <MediaMark v-if="x.e.media" :kind="mediaKind(x.e)" />
            <CalMarker v-else :index="markOf.get(x.e.source.id) ?? 0" />
            <span class="ev-t">{{ x.e.title }}</span>
          </li>
        </template>
      </ul>
      <p v-else class="ev-after lab">Nothing else {{ size === 'wide' ? 'today or tomorrow' : 'in the next three days' }}.</p>
    </div>

    <!-- Tall and column: the agenda by day. -->
    <div v-else class="list ev-agenda">
      <template v-for="d in agenda" :key="d.day">
        <div class="ag-day" aria-hidden="true">{{ dayWord(d.day) }}</div>
        <ul :aria-label="dayWord(d.day)">
          <template v-for="(e, i) in d.items" :key="e.key">
            <li v-if="i === d.nowAt" class="ev-now" aria-hidden="true"><span class="n">{{ hhmm(now) }}</span></li>
            <li :class="['ev-row', { over: d.day === today && isOver(e), now: isNow(e), allday: timeOn(e, d.day) === 'All day' }]">
              <span class="ev-at">
                <span class="n">{{ timeOn(e, d.day) }}</span>
                <small v-if="isNow(e)">ends in {{ until(e.endMs - now) }}</small>
                <small v-else-if="endOn(e, d.day)" class="n">{{ endOn(e, d.day) }}</small>
              </span>
              <span class="ev-body">
                <span class="ev-t">
                  <MediaMark v-if="e.media" :kind="mediaKind(e)" />
                  <CalMarker v-else :index="markOf.get(e.source.id) ?? 0" />{{ e.title }}
                </span>
                <small v-if="spanOn(e, d.day) || detailOf(e, size === 'column')">{{ [spanOn(e, d.day), detailOf(e, size === 'column')].filter(Boolean).join(' · ') }}</small>
                <span v-if="size !== 'column' && !e.media" class="ev-sr">{{ label(e.source) }}</span>
              </span>
            </li>
          </template>
          <li v-if="d.nowAt === d.items.length" class="ev-now" aria-hidden="true"><span class="n">{{ hhmm(now) }}</span></li>
        </ul>
      </template>
    </div>

    <div v-if="events.length && brokenNote.length && size !== 'strip'" class="note w-note">
      <Glyph :kind="broken.some((x) => x.status === 'down') ? 'down' : 'repair'" /><span>{{ brokenNote.join(' · ') }}</span>
    </div>
    <div v-if="legend.length && events.length" class="tf ev-legend">
      <span v-for="(r, i) in legend" :key="r.id"><CalMarker :index="i" />{{ label(r) }}</span>
    </div>
  </Frame>
</template>

<style>
/* ───────────── Calendar ───────────── */
.cal-mk {
  flex: none;
  width: 9px;
  height: 9px;
  fill: var(--text-2);
  stroke: none;
}
.cal-mk .o {
  fill: none;
  stroke: var(--text-2);
  stroke-width: 1.3;
}
.ev-sr {
  position: absolute;
  width: 1px;
  height: 1px;
  overflow: hidden;
  clip-path: inset(50%);
  white-space: nowrap;
}
.ev ul,
.ev ol,
.ev-small ul,
.ev-large ul,
.ev-agenda ul {
  margin: 0;
  padding: 0;
  list-style: none;
}
.ev-strip b {
  color: var(--text);
}

/* One line: time, mark, title. */
.ev-line {
  display: grid;
  grid-template-columns: 4.2em 10px minmax(0, 1fr);
  align-items: center;
  gap: 6px;
  padding: 3px 0;
  font-size: var(--type-small);
  color: var(--text-2);
}
.ev-small .ev-line {
  grid-template-columns: 4.2em minmax(0, 1fr);
}
.ev-line > .n {
  white-space: nowrap;
  overflow: hidden;
  text-overflow: ellipsis;
  font-size: var(--type-caption);
}
.ev-line .ev-t {
  min-width: 0;
  white-space: nowrap;
  overflow: hidden;
  text-overflow: ellipsis;
  color: var(--text);
}
.ev-line .ev-t small {
  margin-left: 0.4em;
  font-size: var(--type-caption);
  color: var(--text-3);
}
.ev-line.now .n,
.ev-line.now .ev-t {
  font-weight: var(--weight-semibold);
  color: var(--text);
}
.ev-line.over .n,
.ev-line.over .ev-t {
  color: var(--text-3);
}
.ev-more {
  padding-top: 2px;
  font-size: var(--type-caption);
  color: var(--text-3);
}
.ev-small {
  display: flex;
  flex-direction: column;
  min-height: 0;
  overflow: hidden;
}
.ev-small .lab b {
  color: var(--text-2);
  font-weight: var(--weight-medium);
}

/* Wide and panel. */
.ev {
  flex: 1 1 auto;
  display: grid;
  grid-template-columns: minmax(0, 1fr) minmax(0, 1.1fr);
  grid-template-areas: 'hero after';
  gap: var(--space-2) var(--space-4);
  min-height: 0;
  overflow: hidden;
}
.ev-panel {
  grid-template-areas: 'ribbon ribbon' 'hero after';
  grid-template-rows: auto minmax(0, 1fr);
}
@container (max-width: 300px) {
  .ev {
    grid-template-columns: minmax(0, 1fr);
    grid-template-areas: 'ribbon' 'hero' 'after';
  }
}
.ev-hero {
  grid-area: hero;
  display: flex;
  flex-direction: column;
  gap: 3px;
  min-width: 0;
}
.ev-hero .ag-day {
  margin: 2px 0 1px;
}
.ev-when {
  display: flex;
  flex-wrap: wrap;
  align-items: baseline;
  gap: 2px var(--space-2);
}
.ev-time {
  font: var(--weight-medium) var(--type-title-2) / 1 var(--font-num);
  letter-spacing: -0.02em;
  font-variant-numeric: var(--figures-display);
  white-space: nowrap;
  color: var(--text);
}
.ev-until {
  font-size: var(--type-caption);
  color: var(--text-2);
}
.ev-title {
  display: -webkit-box;
  -webkit-line-clamp: 2;
  -webkit-box-orient: vertical;
  overflow: hidden;
  font-size: var(--type-body);
  font-weight: var(--weight-medium);
  line-height: 1.3;
  color: var(--text);
}
.ev-title .cal-mk,
.ev-title .md-ic,
.ev-row .ev-t .cal-mk,
.ev-row .ev-t .md-ic {
  margin-right: 6px;
  vertical-align: 0;
}
.ev-detail {
  display: -webkit-box;
  -webkit-line-clamp: 2;
  -webkit-box-orient: vertical;
  overflow: hidden;
  font-size: var(--type-caption);
  line-height: 1.4;
  color: var(--text-3);
}
.ev-wide .ev-detail {
  -webkit-line-clamp: 1;
}
.ev-after {
  grid-area: after;
  align-self: start;
  min-width: 0;
}
.ev-after .ag-day {
  margin: var(--space-1) 0 2px;
}

/* The week: a day each, a dot per event (three at most). */
.ev-ribbon {
  grid-area: ribbon;
  display: grid;
  grid-template-columns: repeat(7, minmax(0, 1fr));
  gap: 2px;
  padding-bottom: var(--space-2);
  border-bottom: var(--hairline) solid var(--line);
}
.ev-ribbon li {
  position: relative;
  display: flex;
  flex-direction: column;
  align-items: center;
  gap: 3px;
  min-width: 0;
}
.ev-wd {
  max-width: 100%;
  overflow: hidden;
  white-space: nowrap;
  text-overflow: ellipsis;
  font: var(--weight-medium) 9.5px/1 var(--font-num);
  letter-spacing: 0.1em;
  text-transform: uppercase;
  color: var(--text-3);
}
.ev-ribbon b {
  font-size: var(--type-small);
  font-weight: var(--weight-medium);
  line-height: 1;
  color: var(--text-3);
}
.ev-ribbon li.has b {
  color: var(--text-2);
}
.ev-ribbon li.today .ev-wd,
.ev-ribbon li.today b {
  color: var(--text);
}
.ev-ribbon li.today b {
  text-decoration: underline;
  text-decoration-thickness: 1.5px;
  text-underline-offset: 3px;
}
.ev-dots {
  display: flex;
  justify-content: center;
  gap: 2px;
  height: 4px;
}
.ev-dots i {
  width: 4px;
  height: 4px;
  border-radius: var(--radius-full);
  background: var(--text-2);
}

/* Tall and column: the agenda. */
.ev-row {
  display: grid;
  grid-template-columns: 3.6em minmax(0, 1fr);
  gap: var(--space-2);
  padding: 5px 0;
  border-top: var(--hairline) solid var(--line);
  font-size: var(--type-small);
}
.ev-at {
  display: flex;
  flex-direction: column;
  gap: 2px;
  min-width: 0;
}
.ev-at > .n {
  white-space: nowrap;
  color: var(--text-2);
}
.ev-row.allday .ev-at > .n {
  font-size: var(--type-caption);
  color: var(--text-3);
}
.ev-at small {
  font-size: var(--type-micro);
  white-space: nowrap;
  color: var(--text-3);
}
.ev-row.now .ev-at small {
  color: var(--text-2);
}
.ev-body {
  min-width: 0;
}
.ev-body .ev-t {
  display: -webkit-box;
  -webkit-line-clamp: 2;
  -webkit-box-orient: vertical;
  overflow: hidden;
  color: var(--text);
}
.ev-row.now .ev-t {
  font-weight: var(--weight-semibold);
}
.ev-row.over .ev-t,
.ev-row.over .ev-at > .n {
  color: var(--text-3);
}
.ev-body small {
  display: block;
  white-space: nowrap;
  overflow: hidden;
  text-overflow: ellipsis;
  font-size: var(--type-caption);
  color: var(--text-3);
}
@container (max-width: 215px) {
  .ev-row .ev-t .cal-mk,
  .ev-row .ev-t .md-ic {
    display: none;
  }
}
.ev-now {
  position: relative;
  height: 0;
  border-top: var(--hairline) solid var(--text-2);
}
.ev-now::before {
  content: '';
  position: absolute;
  left: 0;
  width: 5px;
  height: 5px;
  border-radius: var(--radius-full);
  background: var(--text);
  transform: translate(-1px, -50%);
}
.ev-now .n {
  position: absolute;
  right: 0;
  padding-left: 4px;
  font-size: var(--type-micro);
  line-height: 1;
  color: var(--text-2);
  background: var(--tile-bg);
  transform: translateY(-50%);
}
.ev-agenda .ev-row:has(+ .ev-now),
.ev-now + .ev-row,
.ev-agenda ul > .ev-row:first-child {
  border-top-color: transparent;
}

/* Large: the month, then the next three days. */
.ev-large {
  flex: 1 1 auto;
  display: flex;
  flex-direction: column;
  gap: var(--space-2);
  min-height: 0;
}
.ev-month {
  width: 100%;
  border-collapse: collapse;
  table-layout: fixed;
  font-size: var(--type-caption);
}
.ev-month caption {
  padding-bottom: 4px;
  text-align: left;
  font: var(--weight-medium) 9.5px/1 var(--font-num);
  letter-spacing: 0.14em;
  text-transform: uppercase;
  color: var(--text-3);
}
.ev-month th {
  padding: 0 0 2px;
  font-weight: var(--weight-medium);
  color: var(--text-3);
}
.ev-month abbr {
  text-decoration: none;
}
.ev-month td {
  position: relative;
  height: 24px;
  padding: 0;
  text-align: center;
  vertical-align: top;
  color: var(--text-2);
}
.ev-month td .n {
  display: inline-block;
  min-width: 1.7em;
  padding: 2px 0 1px;
  line-height: 1.2;
}
.ev-month td.past,
.ev-month td.out {
  color: var(--text-3);
}
.ev-month td.out .n {
  opacity: 0.6;
}
.ev-month td.today .n {
  font-weight: var(--weight-semibold);
  color: var(--text);
  border-radius: var(--radius-full);
  box-shadow: inset 0 0 0 1.5px var(--text);
}
.ev-month td .ev-dots {
  margin-top: 1px;
}
.ev-soon {
  min-height: 0;
  padding-top: var(--space-1);
  border-top: var(--hairline) solid var(--line);
}
.ev-soon .ag-day {
  margin: var(--space-1) 0 1px;
}

/* The legend: each calendar's mark and name. */
.ev-legend {
  justify-content: flex-start;
  gap: var(--space-3);
  overflow: hidden;
  white-space: nowrap;
}
.ev-legend > span {
  display: inline-flex;
  flex: 0 1 auto;
  align-items: center;
  gap: 5px;
  min-width: 0;
}
</style>
