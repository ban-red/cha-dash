<script setup lang="ts">
/**
 * Home Assistant: who's home, lights and switches, and house power against
 * its normal band. Target: an instance (`homeassistant@homeassistant:<instance
 * id>/app`), else the first.
 *
 * Glyph: people home (2/2). Strip: home · lights · power (the long strip
 * adds what's open and the alarm). Small: house power with its sparkline.
 * Wide: three stats, the power sparkline and energy today, with what needs a
 * look underneath (open doors and windows, unlocked locks, low batteries,
 * updates) and the alarm. Small and Wide page to a Needs a look list when
 * there's something in it. Large: Wide with that list in place, and the
 * entity counts at the foot. A triggered alarm marks the tile.
 *
 * Reads `people_home`, `lights_on`, `switches_on`, `power.kw`, `energy.kwh`,
 * `open`, `locks_unlocked`, `batteries_low`, `updates` and `unavailable`,
 * with `attrs.people_total`, `lights_total`, `switches_total`, `entities`,
 * `alarm`, `open_items`, `battery_items` and `update_items`
 * (integrations/homeassistant).
 */
import { computed } from 'vue';
import type { SizeId } from '@chadash/layout';
import Glyph, { type GlyphKind } from '../components/marks/Glyph.vue';
import MiniGraph from '../components/marks/MiniGraph.vue';
import { fmtInt, fmtNum, plural } from '../lib/format';
import { attr, placeOf, resolve, series } from '../model/graph';
import { resourceLinks } from '../model/types';
import { useLive } from '../stores/live';
import Frame from './parts/Frame.vue';
import StateNote from './parts/StateNote.vue';
import { useSubject } from './use';
import { useViews } from './views';

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

const str = (v: unknown) => (typeof v === 'string' ? v : '');
const list = (key: string): Record<string, unknown>[] => {
  const v = attr<unknown>(app.value, key);
  return Array.isArray(v) ? v.filter((x): x is Record<string, unknown> => typeof x === 'object' && x !== null) : [];
};
const count = (k: string) => m(k)?.value ?? 0;
/** The alarm panel's state as words: "armed_home" → "Armed home". */
const alarm = computed(() => {
  const a = str(attr<unknown>(app.value, 'alarm'));
  return a ? a[0]!.toUpperCase() + a.slice(1).replace(/_/g, ' ') : '';
});
const triggered = computed(() => attr<string>(app.value, 'alarm') === 'triggered');
const OPEN_WORDS: Record<string, string> = { garage: 'garage open', garage_door: 'garage open', window: 'window open', gate: 'gate open' };
interface Look {
  key: string;
  glyph: GlyphKind;
  name: string;
  detail: string;
}
/** What needs a look, most pressing first: open, unlocked, low batteries, updates. */
const look = computed<Look[]>(() => {
  const out: Look[] = [];
  for (const o of list('open_items')) out.push({ key: `o:${str(o['name'])}`, glyph: 'attn', name: str(o['name']), detail: OPEN_WORDS[str(o['kind'])] ?? 'open' });
  const unlocked = count('locks_unlocked');
  if (unlocked) out.push({ key: 'locks', glyph: 'lock', name: plural(unlocked, 'lock'), detail: 'unlocked' });
  for (const b of list('battery_items')) {
    const lvl = b['level'];
    out.push({ key: `b:${str(b['name'])}`, glyph: 'attn', name: str(b['name']), detail: typeof lvl === 'number' ? `battery ${Math.round(lvl)}%` : 'battery low' });
  }
  for (const u of list('update_items')) {
    const from = str(u['installed']);
    const to = str(u['latest']);
    out.push({ key: `u:${str(u['name'])}`, glyph: 'maint', name: str(u['name']), detail: from && to ? `${from} → ${to}` : 'update available' });
  }
  return out.filter((x) => x.name);
});
/** The counts behind the list, as one line: "3 open · 1 unlocked · 2 low batteries · 2 updates". */
const lookLine = computed(() =>
  [
    count('open') ? `${fmtInt(count('open'))} open` : '',
    count('locks_unlocked') ? `${fmtInt(count('locks_unlocked'))} unlocked` : '',
    count('batteries_low') ? plural(count('batteries_low'), 'low battery', 'low batteries') : '',
    count('updates') ? plural(count('updates'), 'update') : '',
  ]
    .filter(Boolean)
    .join(' · '),
);
const footLine = computed(() =>
  [
    total('entities') !== undefined ? `${fmtInt(total('entities'))} entities` : '',
    count('unavailable') ? `${fmtInt(count('unavailable'))} unavailable` : '',
    alarm.value ? `alarm ${alarm.value.toLowerCase()}` : '',
  ]
    .filter(Boolean)
    .join(' · '),
);
/** The integration sends the lists (an older hub or the Demo Home doesn't): only then can the tile say all is well. */
const hasLook = computed(() => Array.isArray(attr<unknown>(app.value, 'open_items')));
/** Without the lists, a 4×4 shows the counts as rows. */
const rows = computed(() => [
  { k: 'People home', v: people.value?.value ?? null, of: total('people_total') },
  { k: 'Lights on', v: lights.value?.value ?? null, of: total('lights_total') },
  { k: 'Switches on', v: switches.value?.value ?? null, of: total('switches_total') },
]);
const LOOK_ROWS = 4;
const GLYPH_LABEL: Partial<Record<GlyphKind, string>> = { maint: 'update', lock: 'unlocked' };
const { views, view } = useViews(() => (look.value.length && (size === 'small' || size === 'wide') ? [{ id: 'now', label: 'Now' }, { id: 'look', label: 'Needs a look' }] : []));
</script>

<template>
  <Frame
    :size="size"
    name="Home Assistant"
    mono="HA"
    :context="size === 'glyph' ? '' : place ? `on ${place}` : ''"
    :href="resourceLinks(app)[0]?.url"
    :subject="s"
    :status="app ? (triggered && s.status === 'ok' ? 'attention' : undefined) : 'unknown'"
    :tag="triggered ? 'Alarm triggered' : undefined"
    :tag-glyph="triggered ? 'attn' : undefined"
    :views="views"
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
      <span class="sx"><span class="n">{{ fmtInt(people?.value) }}</span> home · <span class="n">{{ fmtInt(lights?.value) }}</span> lights<template v-if="size === 'strip-long' && lookLine"> · {{ lookLine }}</template><template v-if="size === 'strip-long' && alarm"> · {{ alarm.toLowerCase() }}</template></span>
      <MiniGraph :values="power?.history" :band="power?.band" :stale="power?.stale" :height="22" label="House power, 24 hours" :format="kw" />
      <span :class="['sv', 'n', { unknown: power?.value == null }]">{{ fmtNum(power?.value) }}<small>kW</small></span>
    </template>

    <div v-else-if="view === 'look'" class="list" role="list" aria-label="Needs a look in Home Assistant">
      <div v-for="l in look.slice(0, LOOK_ROWS)" :key="l.key" class="li" role="listitem">
        <Glyph :kind="l.glyph" :label="GLYPH_LABEL[l.glyph] ?? 'needs a look'" />
        <span class="t">{{ l.name }}</span>
        <span class="n">{{ l.detail }}</span>
      </div>
      <small v-if="look.length > LOOK_ROWS" class="ha-more">+{{ look.length - LOOK_ROWS }} more</small>
    </div>

    <template v-else-if="size === 'small'">
      <div :class="['v', { unknown: power?.value == null }]">{{ fmtNum(power?.value) }}<span class="u">kW</span></div>
      <MiniGraph :values="power?.history" :band="power?.band" :stale="power?.stale" :height="28" label="House power, 24 hours" :format="kw" />
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
          <MiniGraph :values="power?.history" :band="power?.band" :stale="power?.stale" :height="40" label="House power" :format="kw" />
        </div>
      </div>
      <StateNote :subject="s" :last-known="Boolean(power)" />
      <template v-if="size === 'large'">
        <div v-if="look.length" class="list" role="list" aria-label="Needs a look in Home Assistant">
          <div v-for="l in look.slice(0, 5)" :key="l.key" class="li" role="listitem">
            <Glyph :kind="l.glyph" :label="GLYPH_LABEL[l.glyph] ?? 'needs a look'" />
            <span class="t">{{ l.name }}</span>
            <span class="n">{{ l.detail }}</span>
          </div>
          <small v-if="look.length > 5" class="ha-more">+{{ look.length - 5 }} more</small>
        </div>
        <p v-else-if="hasLook" class="lab">All closed, locked and up to date.</p>
        <dl v-else class="kv">
          <template v-for="r in rows" :key="r.k">
            <dt>{{ r.k }}</dt>
            <dd>{{ fmtInt(r.v) }}{{ r.of !== undefined ? ` of ${fmtInt(r.of)}` : '' }}</dd>
          </template>
        </dl>
        <div v-if="footLine" class="tf"><span>{{ footLine }}</span></div>
      </template>
      <div v-else-if="lookLine || alarm" class="tf">
        <span>{{ [lookLine, alarm ? `alarm ${alarm.toLowerCase()}` : ''].filter(Boolean).join(' · ') }}</span>
      </div>
    </template>
  </Frame>
</template>

<style>
.ha-more {
  color: var(--text-3);
  font-size: var(--type-caption);
}
</style>
