<script setup lang="ts">
/** Section header: label · meta · rule. The meta line is live, from the graph. */
import { computed } from 'vue';
import type { PlacedSection } from '@chadash/layout';
import { fmtInt, plural } from '../lib/format';
import { byKind, resolve, value } from '../model/graph';
import { useLive } from '../stores/live';

const { section } = defineProps<{ section: Pick<PlacedSection, 'id' | 'title'> }>();
const live = useLive();

const meta = computed(() => {
  const g = live.graph.value;
  if (!g) return '';
  switch (section.id) {
    case 'home': {
      const ha = resolve(live.graph.value, 'app:homeassistant');
      const people = ha ? value(live.metrics.value, ha.id, 'people_home') : null;
      const cams = byKind(g, 'camera').length;
      return [people !== null ? `${fmtInt(people)} ${people === 1 ? 'person' : 'people'} home` : '', cams ? plural(cams, 'camera') : ''].filter(Boolean).join(' · ');
    }
    case 'infra': {
      const hosts = byKind(g, 'host').length;
      const engines = byKind(g, 'engine').length;
      return [hosts ? plural(hosts, 'host') : '', engines ? plural(engines, 'Docker engine') : ''].filter(Boolean).join(' · ');
    }
    case 'media':
      return 'Plex · the *arrs · downloads';
    case 'activity': {
      const open = live.problems.value.length + live.repairs.value.length;
      return open ? `${open} open` : 'all clear';
    }
    default:
      return '';
  }
});
</script>

<template>
  <header class="sh">
    <span>{{ section.title ?? section.id }}</span>
    <span class="sh-rule" aria-hidden="true"></span>
    <span v-if="meta" class="sh-m">{{ meta }}</span>
  </header>
</template>
