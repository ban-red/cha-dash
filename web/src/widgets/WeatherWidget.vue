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
 *   `hourly` (up to six `{at, temp_c, condition, precip_pct, is_day}`, nulls
 *   for missing), `attribution` and `attribution_url`;
 * - health `attention` for a thunderstorm or freezing rain now or within six
 *   hours (the reason says which); an unreachable provider leaves the series
 *   stale with their last values (the last forecast, hatched).
 *
 * Target: one weather resource, else the first. Without one: "Not connected",
 * and where to set a location.
 *
 * Glyph: temperature. Strip: temperature · condition · high/low. Small:
 * temperature, condition, high/low, and a second view with the next hours.
 * Wide: now plus the next six hours. The provider's credit shows at small and
 * wide.
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
      // The place's own clock: `at` carries its offset.
      hour: /T(\d{2}):/.exec(e.at!)?.[1] ?? '—',
      temp: typeof e.temp_c === 'number' ? e.temp_c : null,
      condition: e.condition ?? '',
      isDay: e.is_day !== false,
      rain: typeof e.precip_pct === 'number' ? e.precip_pct : null,
    }));
});
const status = computed<TileStatus>(() => (s.value.status === 'unknown' && place.value ? 'ok' : s.value.status));
const name = computed(() => (place.value ? (attr<string>(place.value, 'location') ?? label(place.value)) : 'Weather'));
const extras = computed(() => {
  const out: string[] = [];
  const feels = series(live.metrics.value, id.value, 'feels.c')?.value;
  if (typeof feels === 'number') out.push(`feels ${deg(feels)}°`);
  const wind = series(live.metrics.value, id.value, 'wind.kmh')?.value;
  if (typeof wind === 'number') out.push(imperial.value ? `wind ${Math.round(wind / 1.609)} mph` : `wind ${Math.round(wind)} km/h`);
  const rain = attr<number>(place.value, 'precip_pct');
  if (typeof rain === 'number') out.push(`${Math.round(rain)}% rain today`);
  return out.join(' · ');
});
const attribution = computed(() => attr<string>(place.value, 'attribution'));
const attributionUrl = computed(() => {
  const u = attr<string>(place.value, 'attribution_url');
  return u && /^https?:\/\//i.test(u) ? u : undefined;
});

/** A monochrome mark per condition: the slug's family, the moon after dark. */
function family(c: string, isDay = true): WxFamily {
  if (/thunder|hail/.test(c)) return 'storm';
  if (/snow/.test(c)) return 'snow';
  if (/rain|drizzle|shower/.test(c)) return 'rain';
  if (/fog/.test(c)) return 'fog';
  if (/overcast|cloud/.test(c)) return 'cloud';
  if (c === 'clear' || c === 'mostly_clear') return isDay ? 'sun' : 'moon';
  return 'cloud';
}
const nowFamily = computed(() => family(String(attr(place.value, 'condition') ?? ''), attr(place.value, 'is_day') !== false));
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
    :context="size === 'wide' ? condition : ''"
    :subject="s"
    :status="place ? status : 'ok'"
    :stale="Boolean(temp?.stale)"
    :as-of="temp?.as_of"
    :views="views"
  >
    <template v-if="!place">
      <div :class="size === 'glyph' ? 'gv unknown' : 'v sm unknown'">—</div>
      <p :class="size === 'glyph' ? 'gl' : 'lab'">{{ size === 'glyph' ? 'not connected' : 'Not connected. Set a location to see the weather here.' }}</p>
      <div v-if="size === 'small' || size === 'wide'" class="tf">
        <span><button type="button" class="linkish" @click.stop="openSettings">Set location</button></span>
      </div>
    </template>

    <template v-else-if="size === 'glyph'">
      <div :class="['gv', { unknown: temp?.value == null }]">{{ deg(temp?.value) }}<span class="u">°</span></div>
      <div class="gl">{{ condition || '—' }}</div>
    </template>

    <template v-else-if="size === 'strip' || size === 'strip-long'">
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
      <div :class="size === 'wide' ? 'split wx-split' : 'col'">
        <div class="col">
          <div class="row wx-now">
            <div :class="['v', { unknown: temp?.value == null }]">{{ deg(temp?.value) }}<span class="u">°{{ imperial ? 'F' : 'C' }}</span></div>
            <WxIcon class="big" :family="nowFamily" :label="condition || undefined" />
          </div>
          <div class="lab">{{ size === 'wide' ? hl : [condition, hl].filter(Boolean).join(' · ') || '—' }}</div>
          <div v-if="size === 'wide' && extras" class="sub">{{ extras }}</div>
          <StateNote :subject="s" />
        </div>
        <ol v-if="size === 'wide'" class="wx-hours" aria-label="Next six hours">
          <li v-for="h in hours" :key="h.at">
            <span class="n">{{ h.hour }}</span>
            <WxIcon :family="family(h.condition, h.isDay)" :label="words(h.condition) || 'unknown'" />
            <span class="n">{{ deg(h.temp) }}°</span>
          </li>
          <li v-if="!hours.length" class="wx-none">No hourly forecast</li>
        </ol>
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
  width: 28px;
  height: 28px;
}
.wx-now {
  align-items: center;
  justify-content: flex-start;
}
.wx-split {
  align-items: start;
}
.wx-hours {
  display: grid;
  grid-template-columns: repeat(6, minmax(0, 1fr));
  gap: var(--space-1);
  margin: 0;
  padding: 0;
  list-style: none;
}
.wx-hours li {
  display: flex;
  flex-direction: column;
  align-items: center;
  gap: 4px;
  font-size: var(--type-caption);
  color: var(--text-3);
}
.wx-hours li .n:last-child {
  color: var(--text);
}
.wx-hours .wx-ic {
  width: 16px;
  height: 16px;
}
.wx-hours .wx-none {
  grid-column: 1 / -1;
}
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
