<script setup lang="ts">
/**
 * An *arr queue (Sonarr, Radarr): what's queued now with 24 h of history, and
 * what's wanted. An empty queue says so ("Queue clear"), never a bare 0.
 */
import { computed } from 'vue';
import type { SizeId } from '@chadash/layout';
import Tile from '../components/Tile.vue';
import Glyph from '../components/marks/Glyph.vue';
import Sparkline from '../components/marks/Sparkline.vue';
import { fmtInt, since } from '../lib/format';
import { label, placeOf, resolve, series } from '../model/graph';
import { glyphFor, tileStatus } from '../model/status';
import { normalizeHealth, resourceLinks } from '../model/types';
import { useLive } from '../stores/live';

const { size, target = '' } = defineProps<{ size: SizeId; target?: string | undefined }>();
const live = useLive();

const app = computed(() => resolve(live.graph.value, target));
const id = computed(() => app.value?.id ?? target);
const queue = computed(() => series(live.metrics.value, id.value, 'queue') ?? series(live.metrics.value, id.value, 'queued'));
const wanted = computed(() => series(live.metrics.value, id.value, 'wanted'));
const health = computed(() => normalizeHealth(app.value));
const status = computed(() => tileStatus(health.value, app.value));
const q = computed(() => queue.value?.value ?? null);
const place = computed(() => placeOf(live.graph.value, id.value));
const tone = computed(() => (status.value === 'attention' ? 'attn' : status.value === 'down' ? 'down' : 'neutral'));
</script>

<template>
  <Tile
    :size="size"
    :name="label(app) || 'Queue'"
    :context="place ?? ''"
    :href="resourceLinks(app)[0]?.url"
    :status="app ? status : 'unknown'"
    :stale="Boolean(queue?.stale)"
    :as-of="queue?.as_of"
    :tag="status === 'ok' ? '' : health.reason"
    :tag-glyph="status === 'ok' ? undefined : glyphFor(status)"
    :loading="live.loading.value"
  >
    <template v-if="!app">
      <div class="v sm unknown">—</div>
      <p class="lab">This service isn't in the graph.</p>
    </template>
    <template v-else>
      <template v-if="q">
        <div class="row">
          <div class="v">{{ fmtInt(q) }}</div>
          <span class="lab">queued</span>
        </div>
        <Sparkline :values="queue?.history" :band="queue?.band" :tone="tone" :height="size === 'wide' ? 40 : 28" :label="`${label(app)} queue`" />
      </template>
      <template v-else>
        <div :class="['v', 'sm', 'word', { unknown: q === null }]">{{ q === null ? '—' : 'Queue clear' }}</div>
        <div class="lab">{{ q === null ? 'queue unknown' : 'nothing downloading' }}</div>
      </template>
      <div v-if="status !== 'ok' && health.reason && size === 'wide'" class="note">
        <Glyph :kind="glyphFor(status)" /><span>{{ health.reason }} {{ health.since ? `· ${since(health.since, live.now.value)}` : '' }}</span>
      </div>
      <div class="tf">
        <span><span class="n">{{ fmtInt(wanted?.value) }}</span> wanted</span>
      </div>
    </template>
  </Tile>
</template>
