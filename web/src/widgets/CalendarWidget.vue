<script setup lang="ts">
/** Up next: releases from the *arrs' calendars, grouped by day. */
import { computed } from 'vue';
import type { SizeId } from '@chadash/layout';
import Tile from '../components/Tile.vue';
import { hhmm, shortDate } from '../lib/format';
import { calendar } from '../model/graph';
import { useLive } from '../stores/live';

const { size } = defineProps<{ size: SizeId; target?: string | undefined }>();
const live = useLive();

const entries = computed(() => calendar(live.graph.value, live.now.value).slice(0, size === 'tall' ? 6 : 4));
const groups = computed(() => {
  const today = shortDate(live.now.value);
  const tomorrow = shortDate(live.now.value + 86_400_000);
  const out: { day: string; items: typeof entries.value }[] = [];
  for (const e of entries.value) {
    const d = shortDate(e.at);
    const day = d === today ? 'Today' : d === tomorrow ? 'Tomorrow' : d;
    const g = out[out.length - 1];
    if (g && g.day === day) g.items.push(e);
    else out.push({ day, items: [e] });
  }
  return out;
});
const sources = computed(() => [...new Set(calendar(live.graph.value, live.now.value).map((e) => e.source))].join(' · '));
</script>

<template>
  <Tile :size="size" name="Up next" mono="CA" :context="sources" :loading="live.loading.value">
    <template v-if="!entries.length">
      <div class="v sm word">Nothing scheduled</div>
      <div class="lab">No releases this week.</div>
    </template>
    <div v-else class="list" role="list">
      <template v-for="g in groups" :key="g.day">
        <div class="ag-day">{{ g.day }}</div>
        <div v-for="e in g.items" :key="e.at + e.title" class="ag-row" role="listitem">
          <span class="n">{{ hhmm(e.at) }}</span>
          <span>{{ e.title }}<small>{{ e.source }}{{ e.detail ? ` · ${e.detail}` : '' }}</small></span>
        </div>
      </template>
    </div>
  </Tile>
</template>
