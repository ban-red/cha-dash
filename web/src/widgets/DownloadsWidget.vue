<script setup lang="ts">
/** Downloads (qBittorrent): speed now with 24 h of history, leeching and seeding. */
import { computed } from 'vue';
import type { SizeId } from '@chadash/layout';
import Tile from '../components/Tile.vue';
import Glyph from '../components/marks/Glyph.vue';
import Sparkline from '../components/marks/Sparkline.vue';
import { fmtInt, fmtNum, since } from '../lib/format';
import { placeOf, series } from '../model/graph';
import { glyphFor, tileStatus } from '../model/status';
import { normalizeHealth, resourceLinks } from '../model/types';
import { useLive } from '../stores/live';
import { useUi } from '../stores/ui';

const { size } = defineProps<{ size: SizeId; target?: string | undefined }>();
const live = useLive();
const ui = useUi();
const ID = 'app:qbittorrent';

const app = computed(() => live.graph.value?.resources[ID]);
const dl = computed(() => series(live.metrics.value, ID, 'dl.mbps'));
const ul = computed(() => series(live.metrics.value, ID, 'ul.mbps'));
const leeching = computed(() => series(live.metrics.value, ID, 'leeching'));
const seeding = computed(() => series(live.metrics.value, ID, 'seeding'));
const health = computed(() => normalizeHealth(app.value));
const status = computed(() => tileStatus(health.value, app.value));
const place = computed(() => placeOf(live.graph.value, ID));
const idle = computed(() => (leeching.value?.value ?? 0) === 0 && (dl.value?.value ?? 0) < 0.1);
const incident = computed(() => live.problems.value.find((i) => i.target.includes('qbittorrent')));
const tone = computed(() => (status.value === 'attention' ? 'attn' : status.value === 'down' ? 'down' : 'neutral'));
</script>

<template>
  <Tile
    :size="size"
    name="Downloads"
    mono="QB"
    :context="place ? `qBittorrent · ${place}` : 'qBittorrent'"
    :href="resourceLinks(app)[0]?.url"
    :status="app ? status : 'unknown'"
    :stale="Boolean(dl?.stale)"
    :as-of="dl?.as_of"
    :tag="status === 'ok' ? (idle ? 'idle' : '') : health.reason"
    :tag-glyph="app ? glyphFor(status) : undefined"
    :loading="live.loading.value"
    :clickable="Boolean(incident)"
    @open="incident && ui.openInbox(incident.id)"
  >
    <template v-if="!app">
      <div class="v sm word">Not connected</div>
      <p class="lab">Connect qBittorrent to see downloads.</p>
    </template>
    <template v-else-if="size === 'small'">
      <div v-if="idle" class="v sm word">Idle</div>
      <div v-else class="v">{{ fmtNum(dl?.value) }}<span class="u">Mb/s ↓</span></div>
      <div class="lab"><span class="n">{{ fmtInt(leeching?.value) }}</span> leeching · <span class="n">{{ fmtInt(seeding?.value) }}</span> seeding</div>
    </template>
    <div v-else class="split">
      <div class="col">
        <div v-if="idle" class="v sm word">Idle</div>
        <div v-else class="v">{{ fmtNum(dl?.value) }}<span class="u">Mb/s ↓</span></div>
        <div class="sub"><b class="n">{{ fmtInt(leeching?.value) }}</b> leeching · <b class="n">{{ fmtInt(seeding?.value) }}</b> seeding</div>
        <div v-if="status !== 'ok' && health.reason" class="note">
          <Glyph :kind="glyphFor(status)" /><span>{{ health.reason }}{{ health.since ? ` · ${since(health.since, live.now.value)}` : '' }}</span>
        </div>
        <div v-else class="sub">↑ <span class="n">{{ fmtNum(ul?.value) }}</span> Mb/s</div>
      </div>
      <Sparkline :values="dl?.history" :band="dl?.band" :tone="tone" :height="52" label="Download speed, 24 hours" :format="(v: number) => `${fmtNum(v)} Mb/s`" />
    </div>
  </Tile>
</template>
