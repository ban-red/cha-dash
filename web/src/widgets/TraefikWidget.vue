<script setup lang="ts">
/**
 * Traefik: routers, services and middlewares (`attrs.routers`, `services`,
 * `middlewares`), plus the routes that are `part_of` the instance and the
 * check latency. Target: an instance (`traefik@traefik:<instance id>/app`),
 * else the first.
 */
import { computed } from 'vue';
import type { SizeId } from '@chadash/layout';
import { fmtInt } from '../lib/format';
import { attr, incoming, placeOf, resolve, series } from '../model/graph';
import { resourceLinks } from '../model/types';
import { useLive } from '../stores/live';
import Frame from './parts/Frame.vue';
import StateNote from './parts/StateNote.vue';
import { useSubject } from './use';

const { size, target } = defineProps<{ size: SizeId; target?: string | undefined }>();
const live = useLive();
const WANT = 'app:traefik';

const app = computed(() => resolve(live.graph.value, target || WANT));
const s = useSubject(() => app.value?.id);
const ID = computed(() => app.value?.id ?? WANT);
const http = computed(() => series(live.metrics.value, ID.value, 'http.ms'));
const routes = computed(() => incoming(live.graph.value, ID.value, 'part_of').filter((r) => r.kind === 'route'));
const place = computed(() => placeOf(live.graph.value, ID.value));
const routed = computed(() =>
  routes.value
    .map((r) => r.name || r.id.split('/').pop() || r.id)
    .sort()
    .slice(0, 8),
);
</script>

<template>
  <Frame
    :size="size"
    name="Traefik"
    mono="TR"
    :context="place ?? ''"
    :href="resourceLinks(app)[0]?.url"
    :subject="s"
    :status="app ? undefined : 'unknown'"
  >
    <template v-if="!app">
      <div class="v sm word">Not connected</div>
      <p class="lab">Point Cha Dash at Traefik's API to turn routes into links.</p>
    </template>
    <template v-else-if="size === 'small'">
      <div class="v">{{ fmtInt(attr<number>(app, 'routers')) }}</div>
      <div class="lab">routers</div>
      <StateNote v-if="s.status !== 'ok' && s.status !== 'attention'" :subject="s" :last-known="true" />
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
        <StateNote :subject="s" :last-known="true" />
      </div>
      <div class="names" aria-label="Routes">
        <span v-for="r in routed" :key="r">{{ r }}</span>
      </div>
    </div>
  </Frame>
</template>
