<script setup lang="ts">
/**
 * An *arr queue (Sonarr, Radarr): what's queued now with 24 h of history, and
 * what's wanted. An empty queue says so ("Queue clear"), never a bare 0.
 * While its integration needs repair the counts are last-known and the note
 * offers Reconnect.
 */
import { computed } from 'vue';
import type { SizeId } from '@chadash/layout';
import MiniGraph from '../components/marks/MiniGraph.vue';
import { fmtInt } from '../lib/format';
import { label, placeOf, resolve, series } from '../model/graph';
import { resourceLinks } from '../model/types';
import { useLive } from '../stores/live';
import Frame from './parts/Frame.vue';
import StateNote from './parts/StateNote.vue';
import { useSubject } from './use';

const { size, target = '' } = defineProps<{ size: SizeId; target?: string | undefined }>();
const live = useLive();

const app = computed(() => resolve(live.graph.value, target));
const s = useSubject(() => app.value?.id);
const id = computed(() => app.value?.id ?? target);
const queue = computed(() => series(live.metrics.value, id.value, 'queue') ?? series(live.metrics.value, id.value, 'queued'));
const wanted = computed(() => series(live.metrics.value, id.value, 'wanted'));
const q = computed(() => queue.value?.value ?? null);
const place = computed(() => placeOf(live.graph.value, id.value));
const tone = computed(() => (s.value.status === 'attention' ? 'attn' : s.value.status === 'down' ? 'down' : 'neutral'));
/** Small has room for the note only when it's more than attention (the tag says that). */
const noted = computed(() => size !== 'small' || (s.value.status !== 'ok' && s.value.status !== 'attention'));
</script>

<template>
  <Frame
    :size="size"
    :name="label(app) || 'Queue'"
    :context="place ?? ''"
    :href="resourceLinks(app)[0]?.url"
    :subject="s"
    :status="app ? undefined : 'unknown'"
    :stale="Boolean(queue?.stale)"
    :as-of="queue?.as_of"
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
        <MiniGraph :values="queue?.history" :band="queue?.band" :tone="tone" :stale="queue?.stale" :height="size === 'wide' ? 40 : 28" :label="`${label(app)} queue`" />
      </template>
      <template v-else>
        <div :class="['v', 'sm', 'word', { unknown: q === null }]">{{ q === null ? '—' : 'Queue clear' }}</div>
        <div class="lab">{{ q === null ? 'queue unknown' : 'nothing downloading' }}</div>
      </template>
      <StateNote v-if="noted" :subject="s" :last-known="q !== null" />
      <div class="tf">
        <span><span class="n">{{ fmtInt(wanted?.value) }}</span> wanted</span>
      </div>
    </template>
  </Frame>
</template>
