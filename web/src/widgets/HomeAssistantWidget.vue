<script setup lang="ts">
/** Home Assistant: who's home, lights and switches, and house power against its normal band. */
import { computed } from 'vue';
import type { SizeId } from '@chadash/layout';
import Tile from '../components/Tile.vue';
import Sparkline from '../components/marks/Sparkline.vue';
import { fmtInt, fmtNum } from '../lib/format';
import { attr, placeOf, resolve, series } from '../model/graph';
import { glyphFor, tileStatus } from '../model/status';
import { normalizeHealth, resourceLinks } from '../model/types';
import { useLive } from '../stores/live';

const { size } = defineProps<{ size: SizeId; target?: string | undefined }>();
const live = useLive();
const WANT = 'app:homeassistant';

const app = computed(() => resolve(live.graph.value, WANT));
const ID = computed(() => app.value?.id ?? WANT);
const s = (m: string) => series(live.metrics.value, ID.value, m);
const people = computed(() => s('people_home'));
const lights = computed(() => s('lights_on'));
const switches = computed(() => s('switches_on'));
const power = computed(() => s('power.kw'));
const energy = computed(() => s('energy.kwh'));
const health = computed(() => normalizeHealth(app.value));
const status = computed(() => tileStatus(health.value, app.value));
const stale = computed(() => Boolean(power.value?.stale || people.value?.stale));
const place = computed(() => placeOf(live.graph.value, ID.value));
</script>

<template>
  <Tile
    :size="size"
    name="Home Assistant"
    mono="HA"
    :context="place ? `on ${place}` : ''"
    :href="resourceLinks(app)[0]?.url"
    :status="app ? status : 'unknown'"
    :stale="stale"
    :as-of="power?.as_of"
    :tag="status === 'ok' ? '' : health.reason"
    :tag-glyph="status === 'ok' ? undefined : glyphFor(status)"
    :loading="live.loading.value"
  >
    <template v-if="!app">
      <div class="v sm word">Not connected</div>
      <p class="lab">Connect Home Assistant to see the house here.</p>
    </template>
    <template v-else-if="size === 'small'">
      <div class="v">{{ fmtNum(power?.value) }}<span class="u">kW</span></div>
      <div class="lab">house power</div>
      <div class="tf">
        <span><span class="n">{{ fmtInt(people?.value) }}</span> home · <span class="n">{{ fmtInt(lights?.value) }}</span> lights on</span>
      </div>
    </template>
    <div v-else class="split">
      <div class="stats3">
        <div class="stat">
          <div class="v">{{ fmtInt(people?.value) }}<span class="u">/{{ attr(app, 'people_total') ?? '—' }}</span></div>
          <div class="lab">home</div>
        </div>
        <div class="stat">
          <div class="v">{{ fmtInt(lights?.value) }}<span class="u">/{{ attr(app, 'lights_total') ?? '—' }}</span></div>
          <div class="lab">lights on</div>
        </div>
        <div class="stat">
          <div class="v">{{ fmtInt(switches?.value) }}</div>
          <div class="lab">switches</div>
        </div>
      </div>
      <div class="col">
        <div class="row">
          <div class="v sm">{{ fmtNum(power?.value) }}<span class="u">kW</span></div>
          <span class="lab"><span class="n">{{ fmtNum(energy?.value) }}</span> kWh today</span>
        </div>
        <Sparkline :values="power?.history" :band="power?.band" :height="40" label="House power" :format="(v: number) => `${v.toFixed(2)} kW`" />
      </div>
    </div>
  </Tile>
</template>
