<script setup lang="ts">
/**
 * Weather, from the `weather` resource the weather source publishes per
 * instance (`weather:<instance id>/here`; integrations/weather/README.md):
 *
 * - metrics, always metric: `temp.c` and `feels.c` (band = today's low–high),
 *   `humidity.pct`, `wind.kmh`, `precip.mm`;
 * - attrs (left out when unknown): `location`, `units` (`metric`/`imperial`,
 *   display only, so °F and mph are converted here), `condition` (a slug),
 *   `summary` (a phrase), `is_day`, `high_c`, `low_c`, `precip_pct`,
 *   `sunrise`, `sunset`, `wind_dir_deg`, `hourly` (up to six `{at, temp_c,
 *   condition, precip_pct, is_day}`, nulls for missing), `daily` (five
 *   `{date, condition, summary, high_c, low_c, precip_pct}`, today first),
 *   `attribution` and `attribution_url`;
 * - health `attention` for a thunderstorm or freezing rain now or within six
 *   hours (the reason says which); an unreachable provider leaves the series
 *   stale with their last values (the last forecast, hatched).
 *
 * Target: one weather resource, else the first. Without one: "Not connected",
 * and where to set a location.
 *
 * Glyph: temperature. Strip: temperature · condition · high/low. Small: temperature, condition, high/low, and a
 * second view with the next hours. Wide: now beside the next hours as a
 * temperature curve. Panel: now and the sun's arc, the details on one line,
 * the curve. Tall: now, the details and five days, each a bar on the week's
 * range. Large: the panel over the five days. The provider's credit shows
 * from small up.
 */
import { computed } from 'vue';
import type { SizeId } from '@chadash/layout';
import type { TileStatus } from '../components/Tile.vue';
import { attr, byKind, label, resolve, series } from '../model/graph';
import { useLive } from '../stores/live';
import { useUi } from '../stores/ui';
import Frame from './parts/Frame.vue';
import StateNote from './parts/StateNote.vue';
import WxIcon, { type WxFamily } from './parts/WxIcon.vue';
import { useSubject } from './use';
import { useViews } from './views';

const { size, target } = defineProps<{ size: SizeId; target?: string | undefined }>();
const live = useLive();
const ui = useUi();

const place = computed(() => {
  const t = target ? resolve(live.graph.value, target) : undefined;
  return t?.kind === 'weather' ? t : byKind(live.graph.value, 'weather')[0];
});
const s = useSubject(() => place.value?.id);
const id = computed(() => place.value?.id ?? '');
const temp = computed(() => series(live.metrics.value, id.value, 'temp.c'));
const imperial = computed(() => attr(place.value, 'units') === 'imperial');
const num = (v: unknown): number | null => (typeof v === 'number' && Number.isFinite(v) ? v : null);
const deg = (c: number | null | undefined): string => {
  if (typeof c !== 'number' || !Number.isFinite(c)) return '—';
  return String(Math.round(imperial.value ? (c * 9) / 5 + 32 : c));
};

/** The source's condition slugs, in words, for when there's no summary. */
const WORDS: Record<string, string> = {
  clear: 'clear',
  mostly_clear: 'mostly clear',
  partly_cloudy: 'partly cloudy',
  overcast: 'overcast',
  fog: 'fog',
  drizzle: 'drizzle',
  freezing_drizzle: 'freezing drizzle',
  rain: 'rain',
  freezing_rain: 'freezing rain',
  snow: 'snow',
  snow_grains: 'snow grains',
  rain_showers: 'rain showers',
  snow_showers: 'snow showers',
  thunderstorm: 'thunderstorm',
  hail: 'thunderstorm with hail',
  unknown: '',
};
const words = (c: unknown) => (typeof c === 'string' ? (WORDS[c] ?? c.replace(/[-_]/g, ' ')) : '');
const condition = computed(() => attr<string>(place.value, 'summary') ?? words(attr(place.value, 'condition')));
const high = computed(() => attr<number>(place.value, 'high_c'));
const low = computed(() => attr<number>(place.value, 'low_c'));
const hl = computed(() => (high.value === undefined && low.value === undefined ? '' : `H ${deg(high.value)}° L ${deg(low.value)}°`));

/** A monochrome mark per condition: the slug's family, the moon after dark. */
function family(c: string, isDay = true): WxFamily {
  if (/thunder|hail/.test(c)) return 'storm';
  if (/snow/.test(c)) return 'snow';
  if (/rain|drizzle|shower/.test(c)) return 'rain';
  if (/fog/.test(c)) return 'fog';
  if (c === 'partly_cloudy') return isDay ? 'part-sun' : 'part-moon';
  if (/overcast|cloud/.test(c)) return 'cloud';
  if (c === 'clear' || c === 'mostly_clear') return isDay ? 'sun' : 'moon';
  return 'cloud';
}
const nowFamily = computed(() => family(String(attr(place.value, 'condition') ?? ''), attr(place.value, 'is_day') !== false));

/** "07:07" from an RFC 3339 time: the place's own clock, which it carries. */
const clock = (at: string) => /T(\d{2}:\d{2})/.exec(at)?.[1] ?? '—';

interface Hour {
  at: string;
  hour: string;
  temp: number | null;
  condition: string;
  isDay: boolean;
  rain: number | null;
}
const hours = computed<Hour[]>(() => {
  const list = attr<unknown[]>(place.value, 'hourly');
  if (!Array.isArray(list)) return [];
  return list
    .map((e) => e as { at?: string; temp_c?: number | null; condition?: string | null; is_day?: boolean | null; precip_pct?: number | null })
    .filter((e) => typeof e.at === 'string')
    .slice(0, 6)
    .map((e) => ({
      at: e.at!,
      hour: /T(\d{2}):/.exec(e.at!)?.[1] ?? '—',
      temp: num(e.temp_c),
      condition: e.condition ?? '',
      isDay: e.is_day !== false,
      rain: num(e.precip_pct),
    }));
});

/** Now, then the next hours: the columns under the temperature curve. */
interface Point {
  key: string;
  hour: string;
  temp: number | null;
  family: WxFamily;
  word: string;
  rain: number | null;
}
const points = computed<Point[]>(() => [
  { key: 'now', hour: 'Now', temp: num(temp.value?.value), family: nowFamily.value, word: condition.value || 'unknown', rain: null },
  ...hours.value.map((h) => ({ key: h.at, hour: h.hour, temp: h.temp, family: family(h.condition, h.isDay), word: words(h.condition) || 'unknown', rain: h.rain })),
]);
/** Rain chances worth a line under the hours. */
const showRain = computed(() => points.value.some((p) => (p.rain ?? 0) >= 20));

/**
 * A monotone cubic through the points (Fritsch–Butland slopes), so the line
 * never bulges above the warmest hour or below the coldest.
 */
function smooth(pts: [number, number][]): string {
  const [first] = pts;
  if (!first) return '';
  if (pts.length === 1) return `M${first[0]} ${first[1]}`;
  const slope = pts.slice(1).map((p, i) => (p[1] - pts[i]![1]) / (p[0] - pts[i]![0]));
  const m = pts.map((_, i) => {
    if (i === 0) return slope[0]!;
    if (i === pts.length - 1) return slope[i - 1]!;
    const a = slope[i - 1]!;
    const b = slope[i]!;
    return a * b <= 0 ? 0 : (2 * a * b) / (a + b);
  });
  let d = `M${first[0]} ${first[1]}`;
  for (let i = 1; i < pts.length; i++) {
    const [x0, y0] = pts[i - 1]!;
    const [x1, y1] = pts[i]!;
    const h = (x1 - x0) / 3;
    d += `C${x0 + h} ${(y0 + m[i - 1]! * h).toFixed(2)} ${x1 - h} ${(y1 - m[i]! * h).toFixed(2)} ${x1} ${y1.toFixed(2)}`;
  }
  return d;
}

/**
 * The curve in a box 20 units per column by 100, stretched to the plot (the
 * stroke keeps its width); `ys` places each column's dot, in % of the plot's
 * height. A missing hour breaks the line rather than guessing it.
 */
const curve = computed(() => {
  const pts = points.value;
  const known = pts.map((p) => p.temp).filter((t): t is number => t !== null);
  if (known.length < 2) return undefined;
  const hi = Math.max(...known);
  const lo = Math.min(...known);
  const ys = pts.map((p) => (p.temp === null ? null : hi === lo ? 50 : 18 + (64 * (hi - p.temp)) / (hi - lo)));
  const runs: [number, number][][] = [];
  let run: [number, number][] = [];
  ys.forEach((y, i) => {
    if (y === null) {
      if (run.length) runs.push(run);
      run = [];
    } else run.push([i * 20 + 10, y]);
  });
  if (run.length) runs.push(run);
  const lines = runs.filter((r) => r.length > 1);
  return {
    width: pts.length * 20,
    ys,
    line: lines.map(smooth).join(''),
    area: lines.map((r) => `${smooth(r)}L${r.at(-1)![0]} 100L${r[0]![0]} 100Z`).join(''),
  };
});

interface Day {
  date: string;
  label: string;
  family: WxFamily;
  word: string;
  high: number | null;
  low: number | null;
  rain: number | null;
}
const days = computed<Day[]>(() => {
  const list = attr<unknown[]>(place.value, 'daily');
  if (!Array.isArray(list)) return [];
  return list
    .map((e) => e as { date?: string; condition?: string | null; summary?: string | null; high_c?: number | null; low_c?: number | null; precip_pct?: number | null })
    .filter((e) => typeof e.date === 'string' && /^\d{4}-\d{2}-\d{2}$/.test(e.date))
    .slice(0, 5)
    .map((e, i) => ({
      date: e.date!,
      label: i === 0 ? 'Today' : new Date(`${e.date!}T12:00:00Z`).toLocaleDateString('en-GB', { weekday: 'short', timeZone: 'UTC' }),
      family: family(e.condition ?? ''),
      word: e.summary || words(e.condition) || 'unknown',
      high: num(e.high_c),
      low: num(e.low_c),
      rain: num(e.precip_pct),
    }));
});
/** The week's coldest to warmest (and now), the scale every day's bar sits on. */
const week = computed(() => {
  const all = days.value.flatMap((d) => [d.low, d.high]);
  all.push(num(temp.value?.value));
  const known = all.filter((t): t is number => t !== null);
  if (!known.length) return undefined;
  const lo = Math.min(...known);
  const hi = Math.max(...known);
  return { lo, span: hi - lo || 1 };
});
const onWeek = (t: number) => (week.value ? ((t - week.value.lo) / week.value.span) * 100 : 0);
function bar(d: Day): string | undefined {
  if (d.low === null || d.high === null || !week.value) return undefined;
  const left = onWeek(d.low);
  return `left: ${left.toFixed(1)}%; width: max(6px, ${(onWeek(d.high) - left).toFixed(1)}%)`;
}
/** Where now sits on today's bar. */
const nowOnWeek = computed(() => {
  const t = num(temp.value?.value);
  return t === null || !week.value ? undefined : `left: ${onWeek(t).toFixed(1)}%`;
});

/**
 * The sun's arc for today: sunrise left, sunset right, the sun where it is
 * now (none before sunrise or after sunset). The arc is an ellipse in a
 * 100 × 50 box; the part already travelled is drawn solid.
 */
const sun = computed(() => {
  const rise = attr<string>(place.value, 'sunrise');
  const set = attr<string>(place.value, 'sunset');
  if (typeof rise !== 'string' || typeof set !== 'string') return undefined;
  const r = Date.parse(rise);
  const e = Date.parse(set);
  if (!Number.isFinite(r) || !Number.isFinite(e) || e <= r) return undefined;
  const f = Math.min(1, Math.max(0, (live.now.value - r) / (e - r)));
  const a = Math.PI * (1 - f);
  const x = (50 + 42 * Math.cos(a)).toFixed(2);
  const y = (44 - 36 * Math.sin(a)).toFixed(2);
  return { rise: clock(rise), set: clock(set), up: f > 0 && f < 1, x, y, travelled: f > 0 ? `M8 44A42 36 0 0 1 ${x} ${y}` : '' };
});

/** Where the wind comes from, as a compass point. */
const compass = (d: number) => ['N', 'NE', 'E', 'SE', 'S', 'SW', 'W', 'NW'][Math.round((((d % 360) + 360) % 360) / 45) % 8]!;
/** The details: a list at tall, one line elsewhere. */
const facts = computed(() => {
  const out: { k: string; v: string; phrase: string }[] = [];
  const feels = num(series(live.metrics.value, id.value, 'feels.c')?.value);
  if (feels !== null) out.push({ k: 'Feels', v: `${deg(feels)}°`, phrase: `feels ${deg(feels)}°` });
  const wind = num(series(live.metrics.value, id.value, 'wind.kmh')?.value);
  if (wind !== null) {
    const dir = num(attr(place.value, 'wind_dir_deg'));
    const v = `${imperial.value ? `${Math.round(wind / 1.609)} mph` : `${Math.round(wind)} km/h`}${dir === null ? '' : ` ${compass(dir)}`}`;
    out.push({ k: 'Wind', v, phrase: `wind ${v}` });
  }
  const humidity = num(series(live.metrics.value, id.value, 'humidity.pct')?.value);
  if (humidity !== null) out.push({ k: 'Humidity', v: `${Math.round(humidity)}%`, phrase: `humidity ${Math.round(humidity)}%` });
  const rain = num(attr(place.value, 'precip_pct'));
  if (rain !== null) out.push({ k: 'Rain', v: `${Math.round(rain)}%`, phrase: `${Math.round(rain)}% rain today` });
  return out;
});
const extras = computed(() => facts.value.map((f) => f.phrase).join(' · '));
/** What fits where the details share a column: feels and wind at wide, all but humidity at tall. */
const brief = computed(() => facts.value.filter((f) => f.k === 'Feels' || f.k === 'Wind').map((f) => f.phrase).join(' · '));
const tallFacts = computed(() => facts.value.filter((f) => f.k !== 'Humidity'));

const status = computed<TileStatus>(() => (s.value.status === 'unknown' && place.value ? 'ok' : s.value.status));
const name = computed(() => (place.value ? (attr<string>(place.value, 'location') ?? label(place.value)) : 'Weather'));
/** Sizes wide enough to name the condition in the header, beside the place. */
const headed = computed(() => size === 'wide' || size === 'panel' || size === 'large');
const attribution = computed(() => attr<string>(place.value, 'attribution'));
const attributionUrl = computed(() => {
  const u = attr<string>(place.value, 'attribution_url');
  return u && /^https?:\/\//i.test(u) ? u : undefined;
});

/** Small has no room for the hours beside now, so they're its second view. */
const { views, view } = useViews(() =>
  size === 'small' && place.value && hours.value.length
    ? [
        { id: 'now', label: 'Now' },
        { id: 'hours', label: 'Next hours' },
      ]
    : [],
);
/** The integrations catalog, where Weather is added (a location is its config). */
const openSettings = () => ui.openSettings('integrations', 'new');
</script>

<template>
  <Frame
    :size="size"
    :name="name"
    mono="WX"
    :context="headed ? condition : ''"
    :subject="s"
    :status="place ? status : 'ok'"
    :stale="Boolean(temp?.stale)"
    :as-of="temp?.as_of"
    :views="views"
  >
    <template v-if="!place">
      <div :class="size === 'glyph' ? 'gv unknown' : 'v sm unknown'">—</div>
      <p :class="size === 'glyph' ? 'gl' : 'lab'">{{ size === 'glyph' ? 'not connected' : 'Not connected. Set a location to see the weather here.' }}</p>
      <div v-if="size !== 'glyph' && size !== 'strip'" class="tf">
        <span><button type="button" class="linkish" @click.stop="openSettings">Set location</button></span>
      </div>
    </template>

    <template v-else-if="size === 'glyph'">
      <div :class="['gv', { unknown: temp?.value == null }]">{{ deg(temp?.value) }}<span class="u">°</span></div>
      <div class="gl">{{ condition || '—' }}</div>
    </template>

    <template v-else-if="size === 'strip'">
      <WxIcon :family="nowFamily" />
      <span :class="['sv', 'n', { unknown: temp?.value == null }]">{{ deg(temp?.value) }}<small>°</small></span>
      <span class="sx">{{ condition }}{{ hl ? ` · ${hl}` : '' }}</span>
    </template>

    <template v-else-if="view === 'hours'">
      <ol class="wx-rows" aria-label="Next hours">
        <li v-for="h in hours.slice(0, 4)" :key="h.at">
          <span class="n">{{ h.hour }}:00</span>
          <WxIcon :family="family(h.condition, h.isDay)" :label="words(h.condition) || 'unknown'" />
          <span class="n">{{ deg(h.temp) }}°</span>
          <span class="n wx-rain">{{ h.rain === null ? '' : `${Math.round(h.rain)}%` }}</span>
        </li>
      </ol>
    </template>

    <template v-else>
      <div :class="['wx', `wx-${size}`]">
        <div class="col wx-main">
          <div class="row wx-now">
            <div :class="['v', { lg: size !== 'small' && size !== 'wide', unknown: temp?.value == null }]">{{ deg(temp?.value) }}<span class="u">°{{ imperial ? 'F' : 'C' }}</span></div>
            <WxIcon class="big" :family="nowFamily" :label="condition || undefined" />
          </div>
          <div class="lab">{{ headed ? hl || '—' : [condition, hl].filter(Boolean).join(' · ') || '—' }}</div>
          <div v-if="size === 'wide' && brief" class="sub">{{ brief }}</div>
          <dl v-if="size === 'tall' && tallFacts.length" class="wx-facts">
            <div v-for="f in tallFacts" :key="f.k">
              <dt>{{ f.k }}</dt>
              <dd class="n">{{ f.v }}</dd>
            </div>
          </dl>
          <StateNote :subject="s" />
        </div>

        <div v-if="(size === 'panel' || size === 'large') && extras" class="sub wx-sub">{{ extras }}</div>

        <figure v-if="sun && (size === 'panel' || size === 'large')" class="wx-sun">
          <svg viewBox="0 0 100 50" aria-hidden="true">
            <path class="wx-sun-h" d="M0 44H100" />
            <path class="wx-sun-arc" d="M8 44A42 36 0 0 1 92 44" />
            <path v-if="sun.travelled" class="wx-sun-done" :d="sun.travelled" />
            <circle v-if="sun.up" class="wx-sun-dot" :cx="sun.x" :cy="sun.y" r="3.4" />
          </svg>
          <figcaption>
            <span><span class="wx-sr">Sunrise </span><b class="n">{{ sun.rise }}</b></span>
            <span><span class="wx-sr">Sunset </span><b class="n">{{ sun.set }}</b></span>
          </figcaption>
        </figure>

        <ol v-if="size === 'tall' || size === 'large'" class="wx-days" aria-label="Next five days">
          <li v-for="(d, i) in days" :key="d.date">
            <span class="wx-day">{{ d.label }}</span>
            <WxIcon :family="d.family" :label="d.word" />
            <span class="n wx-rain">{{ d.rain !== null && d.rain >= 20 ? `${Math.round(d.rain)}%` : '' }}</span>
            <span class="n wx-lo">{{ deg(d.low) }}°</span>
            <span class="wx-bar" aria-hidden="true">
              <i v-if="bar(d)" :style="bar(d)"></i>
              <b v-if="i === 0 && nowOnWeek" :style="nowOnWeek"></b>
            </span>
            <span class="n wx-hi">{{ deg(d.high) }}°</span>
          </li>
          <li v-if="!days.length" class="wx-none">No daily forecast</li>
        </ol>

        <div v-if="size === 'wide' || size === 'panel' || size === 'large'" :class="['wx-curve', { 'has-rain': showRain }]">
          <svg v-if="curve" class="wx-plot" :viewBox="`0 0 ${curve.width} 100`" preserveAspectRatio="none" aria-hidden="true">
            <path class="wx-area" :d="curve.area" />
            <path class="wx-trace" :d="curve.line" />
          </svg>
          <ol class="wx-hours" :aria-label="hours.length ? 'Now and the next hours' : 'Now'" :style="{ '--n': points.length }">
            <li v-for="(p, i) in points" :key="p.key" :class="{ now: i === 0 }">
              <span class="n wx-t">{{ deg(p.temp) }}°</span>
              <span class="wx-pt"><i v-if="curve && curve.ys[i] != null" :style="{ top: `${curve.ys[i]}%` }"></i></span>
              <WxIcon :family="p.family" :label="p.word" />
              <span class="n wx-h">{{ p.hour }}</span>
              <span v-if="showRain" class="n wx-rain">{{ p.rain !== null && p.rain >= 20 ? `${Math.round(p.rain)}%` : '' }}</span>
            </li>
          </ol>
          <p v-if="!hours.length" class="wx-none">No hourly forecast</p>
        </div>
      </div>
      <div v-if="attribution" class="tf wx-credit">
        <a v-if="attributionUrl" :href="attributionUrl" target="_blank" rel="noopener noreferrer" @click.stop>{{ attribution }}</a>
        <span v-else>{{ attribution }}</span>
      </div>
    </template>
  </Frame>
</template>

<style>
/* ───────────── Weather ───────────── */
.wx-ic {
  flex: none;
  width: 18px;
  height: 18px;
  fill: none;
  stroke: currentColor;
  stroke-width: 1.4;
  stroke-linecap: round;
  stroke-linejoin: round;
  color: var(--text-2);
}
.wx-ic.big {
  width: 30px;
  height: 30px;
  stroke-width: 1.2;
}
.wx-now {
  align-items: center;
  justify-content: flex-start;
}

/* The body's layout per size: now, the sun, the days and the hours. */
.wx {
  flex: 1 1 auto;
  display: grid;
  gap: var(--space-2) var(--space-4);
  min-height: 0;
  /* Never over the credit: at the smallest grid unit, the bottom is cut. */
  overflow: hidden;
  grid-template-areas: 'main';
  grid-template-columns: minmax(0, 1fr);
  align-content: start;
}
.wx-main {
  grid-area: main;
}
.wx-sun {
  grid-area: sun;
}
.wx-days {
  grid-area: days;
}
.wx-curve {
  grid-area: curve;
}
.wx-sub {
  grid-area: facts;
  white-space: nowrap;
  overflow: hidden;
  text-overflow: ellipsis;
  font-size: var(--type-caption);
}
.wx-wide {
  grid-template-areas: 'main curve';
  grid-template-columns: minmax(0, 1fr) minmax(0, 1.35fr);
  align-items: center;
}
.wx-panel {
  grid-template-areas: 'main sun' 'facts facts' 'curve curve';
  grid-template-columns: minmax(0, 1.2fr) minmax(0, 1fr);
}
.wx-tall {
  grid-template-areas: 'main' 'days';
}
.wx-large {
  grid-template-areas: 'main sun' 'facts facts' 'curve curve' 'days days';
  grid-template-columns: minmax(0, 1.2fr) minmax(0, 1fr);
}
@container (max-width: 300px) {
  .wx-wide,
  .wx-panel,
  .wx-large {
    grid-template-areas: 'main' 'sun' 'facts' 'days' 'curve';
    grid-template-columns: minmax(0, 1fr);
  }
}

/* The details at tall: a label and its value per line, between hairlines. */
.wx-facts {
  display: grid;
  margin: var(--space-1) 0 0;
  font-size: var(--type-caption);
}
.wx-facts div {
  display: flex;
  justify-content: space-between;
  gap: var(--space-2);
  min-width: 0;
  padding: 3px 0;
  border-top: var(--hairline) solid var(--line);
}
.wx-facts dt {
  color: var(--text-3);
}
.wx-facts dd {
  margin: 0;
  min-width: 0;
  white-space: nowrap;
  overflow: hidden;
  text-overflow: ellipsis;
  color: var(--text);
}

/* The sun's arc: a horizon, the day's path dashed, the part travelled solid. */
.wx-sun {
  display: flex;
  flex-direction: column;
  justify-content: flex-end;
  gap: 4px;
  min-width: 0;
  margin: 0;
}
.wx-sun svg {
  display: block;
  width: 100%;
  max-height: 46px;
  overflow: visible;
  fill: none;
}
.wx-sun svg path {
  vector-effect: non-scaling-stroke;
}
.wx-sun-h {
  stroke: var(--border);
  stroke-width: 1;
}
.wx-sun-arc {
  stroke: var(--text-3);
  stroke-width: 1;
  stroke-dasharray: 2 3;
}
.wx-sun-done {
  stroke: var(--text-2);
  stroke-width: 1.5;
  stroke-linecap: round;
}
.wx-sun-dot {
  fill: var(--text);
}
.wx-sun figcaption {
  display: flex;
  justify-content: space-between;
  gap: var(--space-2);
  font-size: var(--type-caption);
  color: var(--text-3);
}
.wx-sun figcaption b {
  font-weight: var(--weight-medium);
  color: var(--text-2);
}
.wx-sr {
  position: absolute;
  width: 1px;
  height: 1px;
  overflow: hidden;
  clip-path: inset(50%);
  white-space: nowrap;
}

/* Five days: each day's low to high on the week's scale, now marked on today. */
.wx-days {
  display: grid;
  align-content: start;
  gap: 2px;
  margin: 0;
  padding: 0;
  list-style: none;
  font-size: var(--type-small);
  color: var(--text-2);
}
.wx-days li {
  display: grid;
  grid-template-columns: 3em 16px 2.4em 2.2em minmax(24px, 1fr) 2.2em;
  align-items: center;
  gap: var(--space-2);
  min-height: 18px;
  white-space: nowrap;
}
.wx-days .wx-ic {
  width: 16px;
  height: 16px;
}
.wx-days .wx-rain {
  font-size: var(--type-caption);
  color: var(--text-3);
}
.wx-days .wx-lo {
  text-align: right;
  color: var(--text-3);
}
.wx-days .wx-hi {
  color: var(--text);
}
.wx-days .wx-none {
  display: block;
  color: var(--text-3);
}
@container (max-width: 215px) {
  .wx-days {
    font-size: var(--type-caption);
  }
  .wx-days li {
    grid-template-columns: 3.1em 14px 2em minmax(16px, 1fr) 2em;
    gap: 5px;
  }
  .wx-days .wx-ic {
    width: 14px;
    height: 14px;
  }
  .wx-days .wx-rain {
    display: none;
  }
}
.wx-bar {
  position: relative;
  height: 4px;
  border-radius: var(--radius-full);
  background: var(--line);
}
.wx-bar i {
  position: absolute;
  top: 0;
  bottom: 0;
  border-radius: var(--radius-full);
  background: var(--text-3);
}
.wx-days li:first-child .wx-bar i {
  background: var(--text-2);
}
.wx-bar b {
  position: absolute;
  top: 50%;
  width: 7px;
  height: 7px;
  border-radius: var(--radius-full);
  background: var(--text);
  box-shadow: 0 0 0 2px var(--tile-bg);
  transform: translate(-50%, -50%);
}

/* The next hours: a column each, the temperature curve drawn through them. */
.wx-curve {
  position: relative;
  min-width: 0;
}
.wx-plot {
  position: absolute;
  /* The plot row: under the temperatures (13px and the 3px gap), 30px tall. */
  top: 16px;
  left: 0;
  width: 100%;
  height: 30px;
  overflow: visible;
  pointer-events: none;
}
.wx-plot path {
  vector-effect: non-scaling-stroke;
}
.wx-trace {
  fill: none;
  stroke: var(--text-2);
  stroke-width: 1.5;
  stroke-linecap: round;
  stroke-linejoin: round;
}
.wx-area {
  fill: var(--text);
  opacity: 0.05;
}
.wx-hours {
  display: grid;
  grid-template-columns: repeat(var(--n, 7), minmax(0, 1fr));
  margin: 0;
  padding: 0;
  list-style: none;
}
.wx-hours li {
  display: flex;
  flex-direction: column;
  align-items: center;
  gap: 3px;
  min-width: 0;
  font-size: var(--type-caption);
  line-height: 1;
  color: var(--text-3);
}
.wx-hours .wx-t {
  height: 13px;
  color: var(--text-2);
}
.wx-hours .wx-pt {
  position: relative;
  align-self: stretch;
  height: 30px;
}
.wx-hours .wx-pt i {
  position: absolute;
  left: 50%;
  width: 5px;
  height: 5px;
  border-radius: var(--radius-full);
  background: var(--text-2);
  box-shadow: 0 0 0 2px var(--tile-bg);
  transform: translate(-50%, -50%);
}
.wx-hours .wx-ic {
  width: 16px;
  height: 16px;
}
.wx-hours .wx-h {
  font-size: var(--type-micro);
}
.wx-hours .wx-rain {
  height: 10px;
  font-size: var(--type-micro);
}
.wx-hours li.now .wx-t,
.wx-hours li.now .wx-h {
  font-weight: var(--weight-semibold);
  color: var(--text);
}
.wx-hours li.now .wx-pt i {
  width: 7px;
  height: 7px;
  background: var(--text);
}
.wx-curve .wx-none {
  margin: var(--space-1) 0 0;
  font-size: var(--type-caption);
  color: var(--text-3);
}

/* Small's second view: the next hours as rows. */
.wx-rows {
  display: grid;
  gap: 2px;
  margin: 0;
  padding: 0;
  list-style: none;
  font-size: var(--type-caption);
}
.wx-rows li {
  display: grid;
  grid-template-columns: 3.2em 16px 1fr auto;
  align-items: center;
  gap: var(--space-1);
  color: var(--text-2);
}
.wx-rows .wx-ic {
  width: 16px;
  height: 16px;
}
.wx-rows .wx-rain {
  color: var(--text-3);
}
.wx-credit a,
.wx-credit span {
  color: var(--text-3);
  text-decoration: none;
  font-size: 10px;
}
.wx-credit a:hover {
  text-decoration: underline;
}
</style>
