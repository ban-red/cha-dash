<script setup lang="ts">
/**
 * Home Assistant: who's home, lights and switches, and house power against
 * its normal band. Target: an instance (`homeassistant@homeassistant:<instance
 * id>/app`), else the first.
 *
 * Glyph: people home (2/2). Strip: home · lights · power. Small: house power
 * with its sparkline. Wide: three stats, the power sparkline and energy
 * today. Large: Wide plus the counts as rows; picked entities wait for
 * per-tile config, so that part asks for them.
 *
 * Reads `people_home`, `lights_on`, `switches_on`, `power.kw` and
 * `energy.kwh`, with `attrs.people_total`, `lights_total`, `switches_total`
 * and `entities` (integrations/homeassistant).
 */
import { computed } from 'vue';
import type { SizeId } from '@chadash/layout';
import Sparkline from '../components/marks/Sparkline.vue';
import { fmtInt, fmtNum } from '../lib/format';
import { attr, placeOf, resolve, series } from '../model/graph';
import { resourceLinks } from '../model/types';
import { useLive } from '../stores/live';
import Frame from './parts/Frame.vue';
import StateNote from './parts/StateNote.vue';
import { useSubject } from './use';

const { size, target } = defineProps<{ size: SizeId; target?: string | undefined }>();
const live = useLive();

const app = computed(() => resolve(live.graph.value, target || 'app:homeassistant'));
const s = useSubject(() => app.value?.id);
const ID = computed(() => app.value?.id ?? '');
const m = (k: string) => series(live.metrics.value, ID.value, k);
const people = computed(() => m('people_home'));
const lights = computed(() => m('lights_on'));
const switches = computed(() => m('switches_on'));
const power = computed(() => m('power.kw'));
const energy = computed(() => m('energy.kwh'));
const stale = computed(() => Boolean(power.value?.stale || people.value?.stale));
const place = computed(() => placeOf(live.graph.value, ID.value));
const total = (k: string) => attr<number>(app.value, k);
const kw = (v: number) => `${v.toFixed(2)} kW`;
const nothing = computed(() => Boolean(app.value) && !people.value && !lights.value && !power.value && !switches.value);
const rows = computed(() => [
  { k: 'People home', v: people.value?.value ?? null, of: total('people_total') },
  { k: 'Lights on', v: lights.value?.value ?? null, of: total('lights_total') },
  { k: 'Switches on', v: switches.value?.value ?? null, of: total('switches_total') },
  { k: 'Entities', v: total('entities') ?? null, of: undefined },
]);
</script>

<template>
  <Frame
    :size="size"
    name="Home Assistant"
    mono="HA"
    :context="size === 'glyph' ? '' : place ? `on ${place}` : ''"
    :href="resourceLinks(app)[0]?.url"
    :subject="s"
    :status="app ? undefined : 'unknown'"
    :stale="stale"
    :as-of="power?.as_of ?? people?.as_of"
  >
    <template v-if="!app">
      <div :class="size === 'glyph' ? 'gv unknown' : 'v sm word'">{{ size === 'glyph' ? '—' : 'Not connected' }}</div>
      <p v-if="size !== 'glyph'" class="lab">Connect Home Assistant to see the house here.</p>
    </template>

    <template v-else-if="nothing && size !== 'glyph'">
      <div class="v sm word">Nothing picked yet</div>
      <p class="lab">Pick a few entities to show here.</p>
      <StateNote :subject="s" />
    </template>

    <template v-else-if="size === 'glyph'">
      <div :class="['gv', { unknown: people?.value == null }]">{{ fmtInt(people?.value) }}<span class="u">/{{ fmtInt(total('people_total')) }}</span></div>
      <div class="gl">home</div>
    </template>

    <template v-else-if="size === 'strip' || size === 'strip-long'">
      <span class="sx"><span class="n">{{ fmtInt(people?.value) }}</span> home · <span class="n">{{ fmtInt(lights?.value) }}</span> lights</span>
      <Sparkline :values="power?.history" :band="power?.band" :stale="power?.stale" :height="22" label="House power, 24 hours" :format="kw" />
      <span :class="['sv', 'n', { unknown: power?.value == null }]">{{ fmtNum(power?.value) }}<small>kW</small></span>
    </template>

    <template v-else-if="size === 'small'">
      <div :class="['v', { unknown: power?.value == null }]">{{ fmtNum(power?.value) }}<span class="u">kW</span></div>
      <Sparkline :values="power?.history" :band="power?.band" :stale="power?.stale" :height="28" label="House power, 24 hours" :format="kw" />
      <StateNote :subject="s" :last-known="Boolean(power)" />
      <div class="tf">
        <span><span class="n">{{ fmtInt(people?.value) }}</span> home · <span class="n">{{ fmtInt(lights?.value) }}</span> lights on</span>
      </div>
    </template>

    <template v-else>
      <div class="split">
        <div class="stats3">
          <div class="stat">
            <div :class="['v', { unknown: people?.value == null }]">{{ fmtInt(people?.value) }}<span class="u">/{{ total('people_total') ?? '—' }}</span></div>
            <div class="lab">home</div>
          </div>
          <div class="stat">
            <div :class="['v', { unknown: lights?.value == null }]">{{ fmtInt(lights?.value) }}<span class="u">/{{ total('lights_total') ?? '—' }}</span></div>
            <div class="lab">lights on</div>
          </div>
          <div class="stat">
            <div :class="['v', { unknown: switches?.value == null }]">{{ fmtInt(switches?.value) }}</div>
            <div class="lab">switches</div>
          </div>
        </div>
        <div class="col">
          <div class="row">
            <div :class="['v', 'sm', { unknown: power?.value == null }]">{{ fmtNum(power?.value) }}<span class="u">kW</span></div>
            <span class="lab"><span class="n">{{ fmtNum(energy?.value) }}</span> kWh today</span>
          </div>
          <Sparkline :values="power?.history" :band="power?.band" :stale="power?.stale" :height="40" label="House power" :format="kw" />
        </div>
      </div>
      <StateNote :subject="s" :last-known="Boolean(power)" />
      <template v-if="size === 'large'">
        <dl class="kv">
          <template v-for="r in rows" :key="r.k">
            <dt>{{ r.k }}</dt>
            <dd>{{ fmtInt(r.v) }}{{ r.of !== undefined ? ` of ${fmtInt(r.of)}` : '' }}</dd>
          </template>
        </dl>
        <div class="tf"><span>Pick a few entities to show here (per-tile config arrives with the widget picker).</span></div>
      </template>
    </template>
  </Frame>
</template>
