<script setup lang="ts">
/**
 * A single pool, datastore or share with a `used.pct` series: a PVE storage
 * (`storage@proxmox:<instance id>/storage/<id>`), an Unraid pool or disk, a
 * PBS datastore, the array. Bound to it.
 *
 * Glyph: % used. Strip: name · bar with its threshold tick · % · free. Small:
 * % used, the bar, "full in ~3 weeks" when it's trending up. Wide: the bar
 * plus the usage sparkline and its growth.
 *
 * Attention is over the threshold *and* climbing, the symptom ("90% and
 * climbing"), not a cause. "Full in" is PBS's own estimate (`full_in_days`)
 * when there is one, else the trend over the history window.
 */
import { computed } from 'vue';
import type { SizeId } from '@chadash/layout';
import Meter from '../components/marks/Meter.vue';
import Sparkline from '../components/marks/Sparkline.vue';
import type { TileStatus } from '../components/Tile.vue';
import { fmtPct } from '../lib/format';
import { label, outgoing, resolve, series } from '../model/graph';
import { resourceLinks } from '../model/types';
import { useLive } from '../stores/live';
import Frame from './parts/Frame.vue';
import StateNote from './parts/StateNote.vue';
import { freeWords, fullIn, trendOf } from './read';
import { isWithheld } from './subject';
import { useSubject } from './use';

const { size, target = '' } = defineProps<{ size: SizeId; target?: string | undefined }>();
const live = useLive();

const r = computed(() => resolve(live.graph.value, target));
const s = useSubject(() => r.value?.id);
const used = computed(() => series(live.metrics.value, r.value?.id ?? target, 'used.pct'));
const v = computed(() => used.value?.value ?? null);
const warn = computed(() => used.value?.warn ?? 85);
const crit = computed(() => used.value?.crit ?? 95);
const full = computed(() => fullIn(r.value, used.value, live.metrics.value?.history));
const trend = computed(() => trendOf(used.value, live.metrics.value?.history, ' pts'));
const climbing = computed(() => Boolean(full.value) || Boolean(trend.value?.startsWith('up')));
const over = computed(() => v.value !== null && v.value >= warn.value);
const status = computed<TileStatus>(() => {
  const own = s.value.status;
  if (own !== 'ok' && own !== 'unknown') return own;
  return over.value && climbing.value ? 'attention' : r.value ? 'ok' : 'unknown';
});
const tag = computed(() => (status.value === 'attention' && s.value.status !== 'attention' ? `${fmtPct(v.value)}% and climbing` : undefined));
const owner = computed(() => label(outgoing(live.graph.value, r.value?.id ?? '', 'part_of')[0]));
const kindWords: Record<string, string> = { storage: 'storage', pool: 'pool', share: 'share', backup_target: 'datastore', array: 'array', disk: 'disk' };
const context = computed(() => [owner.value, kindWords[r.value?.kind ?? ''] ?? r.value?.kind].filter(Boolean).join(' · '));
const free = computed(() => freeWords(r.value, v.value));
const hidden = computed(() => isWithheld(r.value, 'contents'));
const tone = computed(() => (status.value === 'down' ? 'down' : over.value ? 'attn' : 'neutral'));
</script>

<template>
  <Frame
    :size="size"
    :name="r ? label(r) : target || 'Storage'"
    mono="ST"
    :context="context"
    :href="resourceLinks(r)[0]?.url"
    :subject="s"
    :status="status"
    :tag="tag"
    :tag-glyph="tag ? 'attn' : undefined"
    :stale="Boolean(used?.stale)"
    :as-of="used?.as_of"
  >
    <template v-if="!r || !used">
      <div :class="size === 'glyph' ? 'gv unknown' : 'v sm unknown'">—</div>
      <p :class="size === 'glyph' ? 'gl' : 'lab'">{{ size === 'glyph' ? 'not found' : r ? 'This storage reports no usage.' : "This storage isn't in the graph any more." }}</p>
    </template>

    <template v-else-if="size === 'glyph'">
      <div :class="['gv', { unknown: v === null }]">{{ fmtPct(v) }}<span class="u">%</span></div>
      <div class="gl">used</div>
    </template>

    <template v-else-if="size === 'strip' || size === 'strip-long'">
      <Meter class="sm-meter" :value="v" :warn="warn" :crit="crit" :tick="warn" :label="`${label(r)} used`" />
      <span class="sv n">{{ fmtPct(v) }}<small>%</small></span>
      <span v-if="free" class="sx">{{ free }}</span>
    </template>

    <template v-else>
      <div :class="size === 'small' ? 'col' : 'split'">
        <div class="col">
          <div :class="['v', { unknown: v === null }]">{{ fmtPct(v) }}<span class="u">% used</span></div>
          <Meter :value="v" :warn="warn" :crit="crit" :tick="warn" :label="`${label(r)} used`" />
          <div class="lab">{{ [full, size === 'small' ? '' : free].filter(Boolean).join(' · ') || free || 'steady' }}</div>
          <StateNote :subject="s" :limited="hidden && size !== 'small'" />
        </div>
        <Sparkline
          v-if="size !== 'small'"
          :values="used.history"
          :band="used.band"
          :tone="tone"
          :threshold="warn"
          :stale="used.stale"
          :height="52"
          :label="`${label(r)} used, 24 hours`"
          :format="(x: number) => `${Math.round(x)}%`"
        />
      </div>
      <div v-if="size !== 'small' && trend" class="tf"><span>{{ trend }}</span></div>
    </template>
  </Frame>
</template>
