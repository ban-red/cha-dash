<script setup lang="ts">
/** Incidents: everything open (problems first, then repairs), and the latest resolution when quiet. */
import { computed } from 'vue';
import type { SizeId } from '@chadash/layout';
import Tile from '../components/Tile.vue';
import Glyph from '../components/marks/Glyph.vue';
import { hhmm } from '../lib/format';
import { resolved, settling } from '../model/inbox';
import { glyphFor } from '../model/status';
import { useLive } from '../stores/live';
import { useUi } from '../stores/ui';

const { size } = defineProps<{ size: SizeId; target?: string | undefined }>();
const live = useLive();
const ui = useUi();

const open = computed(() => [...live.problems.value, ...live.repairs.value, ...settling(live.inbox.value)]);
const shown = computed(() => open.value.slice(0, size === 'wide' ? 3 : size === 'panel' ? 5 : 7));
const last = computed(() => resolved(live.inbox.value)[0]);
const status = computed(() => (live.problems.value.some((i) => i.severity === 'down') ? 'down' : live.problems.value.length ? 'attention' : 'ok'));
const tag = computed(() => (open.value.length ? `${open.value.length} open` : 'none open'));
</script>

<template>
  <Tile
    :size="size"
    name="Incidents"
    mono="IN"
    context="problems and repairs"
    :status="status"
    :tag="tag"
    :tag-glyph="glyphFor(status)"
    :loading="live.loading.value && !live.inbox.value"
  >
    <template v-if="!open.length">
      <div class="v sm word">Nothing needs you</div>
      <div class="lab">{{ last ? `Last: ${last.title}, resolved ${hhmm(last.resolved_at ?? last.updated_at)}.` : 'No incidents today.' }}</div>
    </template>
    <div v-else class="list" role="list">
      <div v-for="i in shown" :key="i.id" role="listitem">
        <button type="button" class="li" @click="ui.openInbox(i.id)">
          <Glyph :kind="i.class === 'repair' ? 'repair' : glyphFor(i.severity)" :label="i.class === 'repair' ? 'repair' : i.severity" />
          <span><span class="t">{{ i.title }}</span><small>{{ i.class === 'repair' ? 'Repair' : i.phase }}</small></span>
          <span class="n">{{ hhmm(i.opened_at) }}</span>
        </button>
      </div>
    </div>
    <div v-if="open.length > shown.length" class="tf">
      <span>+{{ open.length - shown.length }} more in the Inbox</span>
    </div>
  </Tile>
</template>
