<script setup lang="ts">
/**
 * One number with its context: unit, normal band, trend and freshness.
 * Never a bare "73%". Bound to one series, `metric@<resource id>#<metric key>`
 * (`metric@pve:jupiter-nas#mem.pct`, `metric@net:opnsense/wan#rx.mbps`).
 *
 * Glyph: value and unit. Strip: label · sparkline · value. Small: value and a
 * word-sized sparkline with its band and last-point dot. Wide: value, a
 * larger sparkline with the threshold line, min and max over the window.
 *
 * Attention is the series past its warn threshold (Ember line and dot, ▲ and
 * the word); down is its source being down (last value, "source down since").
 * Maintenance and off are the source's, inherited.
 */
import { computed } from 'vue';
import type { SizeId } from '@chadash/layout';
import Glyph from '../components/marks/Glyph.vue';
import Sparkline from '../components/marks/Sparkline.vue';
import type { TileStatus } from '../components/Tile.vue';
import { since } from '../lib/format';
import { label, series } from '../model/graph';
import { resourceLinks } from '../model/types';
import { useLive } from '../stores/live';
import Frame from './parts/Frame.vue';
import StateNote from './parts/StateNote.vue';
import { extent, fmtValue, metricWords, trendOf, unitOf } from './read';
import { isWithheld } from './subject';
import { useSubject } from './use';

const { size, target = '' } = defineProps<{ size: SizeId; target?: string | undefined }>();
const live = useLive();

const hash = computed(() => target.lastIndexOf('#'));
const id = computed(() => (hash.value > 0 ? target.slice(0, hash.value) : target));
const key = computed(() => (hash.value > 0 ? target.slice(hash.value + 1) : ''));
const s = useSubject(() => id.value);
const r = computed(() => s.value.resource);
const sr = computed(() => (key.value ? series(live.metrics.value, id.value, key.value) : undefined));
const unit = computed(() => unitOf(sr.value, key.value));
const words = computed(() => metricWords(key.value));
const limited = computed(() => isWithheld(r.value, key.value));
const v = computed(() => (limited.value ? null : (sr.value?.value ?? null)));
const shown = computed(() => fmtValue(v.value, unit.value));
const noData = computed(() => Boolean(sr.value) && v.value === null && !(sr.value?.history ?? []).some((x) => typeof x === 'number'));
const over = computed(() => {
  const w = sr.value?.warn;
  return typeof v.value === 'number' && typeof w === 'number' && v.value >= w;
});
const status = computed<TileStatus>(() => {
  const own = s.value.status;
  if (own === 'down' || own === 'maintenance' || own === 'off' || own === 'repair') return own;
  return over.value ? 'attention' : own === 'unknown' && sr.value ? 'ok' : own;
});
const trend = computed(() => trendOf(sr.value, live.metrics.value?.history, unit.value === '%' ? ' pts' : ''));
const climbing = computed(() => Boolean(trend.value?.startsWith('up')));
const tag = computed(() => {
  if (status.value === 'attention' && over.value) return `${words.value} ${shown.value}${unit.value}${climbing.value ? ' and climbing' : ''}`;
  return undefined;
});
const tone = computed(() => (status.value === 'down' ? 'down' : over.value ? 'attn' : 'neutral'));
const range = computed(() => extent(sr.value));
const format = (x: number) => `${fmtValue(x, unit.value)}${unit.value === '%' ? '%' : unit.value ? ` ${unit.value}` : ''}`;
const sourceDown = computed(() => (status.value === 'down' ? `Source down ${since(s.value.health.since, live.now.value)}`.trim() : ''));
</script>

<template>
  <Frame
    :size="size"
    :name="r ? label(r) : id || 'Metric'"
    :context="words"
    :href="resourceLinks(r)[0]?.url"
    :subject="s"
    :status="r || sr ? status : 'unknown'"
    :tag="tag"
    :tag-glyph="tag ? 'attn' : undefined"
    :stale="Boolean(sr?.stale)"
    :as-of="sr?.as_of"
  >
    <template v-if="!sr">
      <div :class="size === 'glyph' ? 'gv unknown' : 'v sm unknown'">—</div>
      <p :class="size === 'glyph' ? 'gl' : 'lab'">{{ size === 'glyph' ? 'no series' : `Nothing reports ${words.toLowerCase()} here.` }}</p>
    </template>

    <template v-else-if="size === 'glyph'">
      <div :class="['gv', { unknown: v === null }]">{{ shown }}<span class="u">{{ unit }}</span></div>
      <div class="gl">{{ words }}</div>
    </template>

    <template v-else-if="size === 'strip' || size === 'strip-long'">
      <Sparkline :values="sr.history" :band="sr.band" :tone="tone" :height="22" :stale="sr.stale" :label="`${words}, 24 hours`" :format="format" />
      <span :class="['sv', 'n', { unknown: v === null }]">{{ shown }}<small>{{ unit }}</small></span>
    </template>

    <template v-else>
      <div :class="size === 'small' ? 'col' : 'split'">
        <div class="col">
          <div :class="['v', { unknown: v === null, lg: size === 'large' }]">{{ shown }}<span class="u">{{ unit }}</span></div>
          <div v-if="noData" class="lab">No data yet · history starts now</div>
          <div v-else-if="size !== 'small' && range" class="sub">
            min <b class="n">{{ format(range.min) }}</b> · max <b class="n">{{ format(range.max) }}</b>
            <template v-if="sr.warn !== undefined"> · threshold <b class="n">{{ format(sr.warn) }}</b></template>
          </div>
          <div v-if="sourceDown" class="note w-note n-down"><Glyph kind="down" /><span>{{ sourceDown }}</span></div>
          <StateNote v-else :subject="s.status === 'ok' ? undefined : s" :limited="limited" :last-known="v !== null" />
        </div>
        <Sparkline
          :values="limited ? [] : sr.history"
          :band="sr.band"
          :tone="tone"
          :height="size === 'small' ? 30 : 56"
          :threshold="size === 'small' ? undefined : sr.warn"
          :stale="sr.stale"
          :label="`${words}, 24 hours`"
          :format="format"
        />
      </div>
      <div v-if="size !== 'small' && trend" class="tf"><span>{{ trend }}</span></div>
    </template>
  </Frame>
</template>
