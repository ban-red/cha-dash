<script setup lang="ts">
/**
 * The clock. It has no source, so of the eight states only healthy applies
 * (loading is never visible), and it never goes stale with the hub: it isn't
 * a <Tile> for that reason, just the same frame. Time is the hub's clock in
 * the display time zone (demo mode shows the Demo Home's), on the dot-matrix
 * face.
 *
 * Glyph: hh:mm. Strip: time · date. Small: time and date. Wide: time, date and
 * a second time zone: the target (`clock@America/New_York`), else UTC, or the
 * browser's own zone when the display zone is already UTC. When the display
 * zone is an offset (the Demo Home shown as local time), the simulated clock
 * says nothing about real time elsewhere, so the second zone reads the
 * browser's real clock.
 */
import { computed } from 'vue';
import type { SizeId } from '@chadash/layout';
import { getDisplayTimeZone, hhmm, longDate, monogram, shortDate } from '../lib/format';
import { useLive } from '../stores/live';
import { useTile } from '../stores/tiles';
import TileCog from '../components/TileCog.vue';

const { size, target } = defineProps<{ size: SizeId; target?: string | undefined }>();
const live = useLive();

function validZone(z: string | undefined): string | undefined {
  if (!z) return undefined;
  try {
    new Intl.DateTimeFormat('en-GB', { timeZone: z });
    return z;
  } catch {
    return undefined;
  }
}
const browserZone = () => {
  try {
    return Intl.DateTimeFormat().resolvedOptions().timeZone;
  } catch {
    return 'UTC';
  }
};
const primary = computed(() => getDisplayTimeZone() ?? browserZone());
/** "-08:05": the Demo Home's clock shifted to look local. */
const shifted = computed(() => /^[+-]\d\d:\d\d$/.test(primary.value));
const second = computed(() => {
  const asked = validZone(target);
  if (asked) return asked;
  if (primary.value !== 'UTC') return 'UTC';
  const b = browserZone();
  return b !== 'UTC' ? b : undefined;
});
const time = computed(() => hhmm(live.now.value));
const date = computed(() => (size === 'small' || size === 'wide' ? longDate(live.now.value) : shortDate(live.now.value)));
const iso = computed(() => new Date(live.now.value).toISOString());

/** Minutes ahead of UTC for a zone at this moment. */
function offsetMin(zone: string, ms: number): number {
  const parts = new Intl.DateTimeFormat('en-US', { timeZone: zone, hourCycle: 'h23', year: 'numeric', month: 'numeric', day: 'numeric', hour: 'numeric', minute: 'numeric' }).formatToParts(ms);
  const get = (t: string) => Number(parts.find((p) => p.type === t)?.value ?? 0);
  const asUtc = Date.UTC(get('year'), get('month') - 1, get('day'), get('hour') % 24, get('minute'));
  return Math.round((asUtc - Math.floor(ms / 60_000) * 60_000) / 60_000);
}
const other = computed(() => {
  const z = second.value;
  if (!z) return undefined;
  const ms = shifted.value ? live.wall.value : live.now.value;
  const here = shifted.value ? browserZone() : primary.value;
  const t = new Date(ms).toLocaleTimeString('en-GB', { hour: '2-digit', minute: '2-digit', hour12: false, timeZone: z });
  const diff = (offsetMin(z, ms) - offsetMin(here, ms)) / 60;
  const city = z === 'UTC' ? 'UTC' : (z.split('/').pop() ?? z).replace(/_/g, ' ');
  const rel = diff === 0 ? 'same time' : `${diff > 0 ? '+' : '−'}${Math.abs(diff) % 1 ? Math.abs(diff).toFixed(1) : Math.abs(diff)} h`;
  return { t, city, rel };
});
/** The tile config's name and chromeless apply here as on every Tile. */
const tile = useTile();
const name = computed(() => tile.config.value?.name ?? 'Clock');
const label = computed(() => `${name.value}, ${time.value}, ${longDate(live.now.value)}${other.value ? `; ${other.value.city} ${other.value.t}` : ''}`);
</script>

<template>
  <article :class="['tile', `s-${size}`, 'w-clockt']" :aria-label="label">
    <div v-if="!tile.config.value?.chromeless" class="th">
      <span class="mono" aria-hidden="true">{{ name === 'Clock' ? 'CL' : monogram(name) }}</span>
      <span class="tn">{{ name }}</span>
    </div>
    <TileCog :name="name" />
    <template v-if="size === 'glyph'">
      <time class="gv clock-face" :datetime="iso">{{ time }}</time>
      <div class="gl">{{ shortDate(live.now.value).split(' ')[0] }}</div>
    </template>
    <template v-else-if="size === 'strip' || size === 'strip-long'">
      <time class="clock-face strip-clock" :datetime="iso">{{ time }}</time>
      <span class="sx">{{ date }}</span>
    </template>
    <template v-else>
      <time :class="['clock-face', 'big-clock', { wide: size !== 'small' }]" :datetime="iso">{{ time }}</time>
      <div class="clock-date">{{ date }}</div>
      <div v-if="size !== 'small' && other" class="tf">
        <span>{{ other.city }} <span class="n">{{ other.t }}</span> · {{ other.rel }}</span>
      </div>
    </template>
  </article>
</template>

<style>
/* ───────────── Clock: the dot-matrix face ───────────── */
.w-clockt {
  cursor: default;
}
.clock-face {
  font-family: var(--font-clock);
  font-weight: 700;
  letter-spacing: 0.01em;
  font-variant-numeric: tabular-nums;
  color: var(--text);
}
.gv.clock-face {
  font-size: 20px;
}
.strip-clock {
  flex: none;
  font-size: var(--type-title);
  line-height: 1;
}
.big-clock {
  margin-top: auto;
  font-size: clamp(36px, 30cqi, var(--type-clock));
  line-height: 0.84;
}
.big-clock.wide {
  font-size: clamp(44px, 22cqi, calc(var(--type-clock) * 1.3));
}
.clock-date {
  font: var(--weight-medium) var(--type-caption) / 1.2 var(--font-num);
  letter-spacing: var(--tracking-caps);
  text-transform: uppercase;
  color: var(--text-3);
}
.w-clockt .tf {
  margin-top: var(--space-1);
}
</style>
