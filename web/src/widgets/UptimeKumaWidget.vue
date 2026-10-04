<script setup lang="ts">
/**
 * Uptime Kuma: monitors up out of total, and the last 24 h as cells (one per
 * history point; a cell with any monitor down is attention).
 */
import { computed } from 'vue';
import type { SizeId } from '@chadash/layout';
import Tile from '../components/Tile.vue';
import { fmtInt } from '../lib/format';
import { attr, placeOf, series } from '../model/graph';
import { glyphFor, tileStatus } from '../model/status';
import { normalizeHealth, resourceLinks } from '../model/types';
import { useLive } from '../stores/live';

const { size } = defineProps<{ size: SizeId; target?: string | undefined }>();
const live = useLive();
const ID = 'app:uptimekuma';

const app = computed(() => live.graph.value?.resources[ID]);
const up = computed(() => series(live.metrics.value, ID, 'monitors.up'));
const down = computed(() => series(live.metrics.value, ID, 'monitors.down'));
const total = computed(() => attr<number>(app.value, 'monitors') ?? ((up.value?.value ?? 0) + (down.value?.value ?? 0) || null));
const health = computed(() => normalizeHealth(app.value));
const downNow = computed(() => down.value?.value ?? 0);
const status = computed(() => tileStatus(health.value, app.value));
const cells = computed(() => {
  const h = down.value?.history ?? [];
  const n = size === 'wide' ? 48 : 24;
  const step = Math.max(1, Math.floor(h.length / n));
  const out: ('ok' | 'attn' | 'unknown')[] = [];
  for (let i = h.length - n * step; i < h.length; i += step) {
    if (i < 0) {
      out.push('unknown');
      continue;
    }
    const slice = h.slice(i, i + step);
    out.push(slice.some((v) => v === null) ? 'unknown' : slice.some((v) => (v ?? 0) > 0) ? 'attn' : 'ok');
  }
  return out;
});
const upPct = computed(() => {
  const h = down.value?.history ?? [];
  const known = h.filter((v): v is number => typeof v === 'number');
  return known.length ? Math.round((known.filter((v) => v === 0).length / known.length) * 1000) / 10 : null;
});
const place = computed(() => placeOf(live.graph.value, ID));
const tag = computed(() => (downNow.value > 0 ? `${downNow.value} monitor${downNow.value === 1 ? '' : 's'} down` : status.value === 'ok' ? '' : health.value.reason));
</script>

<template>
  <Tile
    :size="size"
    name="Uptime Kuma"
    mono="UK"
    :context="place ?? ''"
    :href="resourceLinks(app)[0]?.url"
    :status="app ? status : 'unknown'"
    :stale="Boolean(up?.stale)"
    :as-of="up?.as_of"
    :tag="tag"
    :tag-glyph="downNow > 0 ? 'attn' : status === 'ok' ? undefined : glyphFor(status)"
    :loading="live.loading.value"
  >
    <template v-if="!app">
      <div class="v sm word">Not connected</div>
      <p class="lab">Add Uptime Kuma to see its monitors.</p>
    </template>
    <template v-else>
      <div class="v">{{ fmtInt(up?.value) }}<span class="u">/{{ fmtInt(total) }} up</span></div>
      <div class="ub" role="img" :aria-label="downNow > 0 ? `${downNow} down now; last 24 hours shown as cells` : 'Last 24 hours: all up'">
        <i v-for="(c, i) in cells" :key="i" :class="c === 'ok' ? '' : c"></i>
      </div>
      <div class="tf">
        <span><span class="n">{{ upPct ?? '—' }}%</span> all up · 24 h</span>
      </div>
    </template>
  </Tile>
</template>
