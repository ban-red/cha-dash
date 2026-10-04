<script setup lang="ts">
/**
 * Uptime Kuma: monitors up out of total, and the last 24 h as cells (one per
 * history point; a cell with any monitor down is attention).
 */
import { computed } from 'vue';
import type { SizeId } from '@chadash/layout';
import { fmtInt } from '../lib/format';
import { attr, placeOf, resolve, series } from '../model/graph';
import { resourceLinks } from '../model/types';
import { useLive } from '../stores/live';
import Frame from './parts/Frame.vue';
import StateNote from './parts/StateNote.vue';
import { useSubject } from './use';

const { size, target } = defineProps<{ size: SizeId; target?: string | undefined }>();
const live = useLive();
const WANT = 'app:uptimekuma';

/** An instance by its layout item (`uptimekuma@uptimekuma:<instance id>/app`), else the first. */
const app = computed(() => resolve(live.graph.value, target || WANT));
const ID = computed(() => app.value?.id ?? WANT);
const up = computed(() => series(live.metrics.value, ID.value, 'monitors.up'));
const down = computed(() => series(live.metrics.value, ID.value, 'monitors.down'));
const total = computed(() => attr<number>(app.value, 'monitors') ?? ((up.value?.value ?? 0) + (down.value?.value ?? 0) || null));
const s = useSubject(() => app.value?.id);
const downNow = computed(() => down.value?.value ?? 0);
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
const place = computed(() => placeOf(live.graph.value, ID.value));
const tag = computed(() => `${downNow.value} monitor${downNow.value === 1 ? '' : 's'} down`);
</script>

<template>
  <Frame
    :size="size"
    name="Uptime Kuma"
    mono="UK"
    :context="place ?? ''"
    :href="resourceLinks(app)[0]?.url"
    :subject="s"
    :status="app ? (s.status === 'ok' && downNow > 0 ? 'attention' : undefined) : 'unknown'"
    :stale="Boolean(up?.stale)"
    :as-of="up?.as_of"
    :tag="downNow > 0 ? tag : undefined"
    :tag-glyph="downNow > 0 ? 'attn' : undefined"
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
      <StateNote v-if="s.status !== 'ok' && s.status !== 'attention'" :subject="s" :last-known="up?.value != null" />
      <div class="tf">
        <span><span class="n">{{ upPct ?? '—' }}%</span> all up · 24 h</span>
      </div>
    </template>
  </Frame>
</template>
