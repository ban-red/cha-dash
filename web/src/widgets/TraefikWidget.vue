<script setup lang="ts">
/** Traefik: routers, services and middlewares, plus how many routes the graph knows and the check latency. */
import { computed } from 'vue';
import type { SizeId } from '@chadash/layout';
import Tile from '../components/Tile.vue';
import { fmtInt } from '../lib/format';
import { attr, byKind, placeOf, series } from '../model/graph';
import { glyphFor, tileStatus } from '../model/status';
import { normalizeHealth, resourceLinks } from '../model/types';
import { useLive } from '../stores/live';

const { size } = defineProps<{ size: SizeId; target?: string | undefined }>();
const live = useLive();
const ID = 'app:traefik';

const app = computed(() => live.graph.value?.resources[ID]);
const http = computed(() => series(live.metrics.value, ID, 'http.ms'));
const routes = computed(() => byKind(live.graph.value, 'route'));
const health = computed(() => normalizeHealth(app.value));
const status = computed(() => tileStatus(health.value, app.value));
const place = computed(() => placeOf(live.graph.value, ID));
const routed = computed(() =>
  routes.value
    .map((r) => r.name || r.id.split('/').pop() || r.id)
    .sort()
    .slice(0, 8),
);
</script>

<template>
  <Tile
    :size="size"
    name="Traefik"
    mono="TR"
    :context="place ?? ''"
    :href="resourceLinks(app)[0]?.url"
    :status="app ? status : 'unknown'"
    :tag="status === 'ok' ? '' : health.reason"
    :tag-glyph="status === 'ok' ? undefined : glyphFor(status)"
    :loading="live.loading.value"
  >
    <template v-if="!app">
      <div class="v sm word">Not connected</div>
      <p class="lab">Point Cha Dash at Traefik's API to turn routes into links.</p>
    </template>
    <template v-else-if="size === 'small'">
      <div class="v">{{ fmtInt(attr<number>(app, 'routers')) }}</div>
      <div class="lab">routers</div>
      <div class="tf">
        <span><span class="n">{{ fmtInt(attr<number>(app, 'services')) }}</span> services · <span class="n">{{ fmtInt(attr<number>(app, 'middlewares')) }}</span> mw</span>
      </div>
    </template>
    <div v-else class="split">
      <div class="col">
        <div class="v">{{ fmtInt(attr<number>(app, 'routers')) }}<span class="u">routers</span></div>
        <div class="sub">
          <b class="n">{{ fmtInt(attr<number>(app, 'services')) }}</b> services · <b class="n">{{ fmtInt(attr<number>(app, 'middlewares')) }}</b> middlewares
        </div>
        <div class="sub"><b class="n">{{ routes.length }}</b> routes in the graph · <span class="n">{{ fmtInt(http?.value) }} ms</span></div>
      </div>
      <div class="names" aria-label="Routes">
        <span v-for="r in routed" :key="r">{{ r }}</span>
      </div>
    </div>
  </Tile>
</template>
