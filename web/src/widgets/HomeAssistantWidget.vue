<script setup lang="ts">
/**
 * Home Assistant: who's home, lights and switches, and house power against
 * its normal band. Target: an instance (`homeassistant@homeassistant:<instance
 * id>/app`), else the first.
 *
 * House power is a widget setting (`options.power`), off by default. Without
 * it the tile fills that space with cards for what the house reports:
 * Indoors (thermostats' temperature and what they're doing), Safety (smoke,
 * CO, gas and leak sensors: OK, or what tripped) and Playing (media players,
 * with the track). A card shows only when the house has the sensors for it;
 * with none, the open, unlocked, low-battery and update counts stand in. The
 * home stat names who is home. A tripped safety sensor marks the tile and
 * heads the Needs a look list.
 *
 * Glyph: people home (2/2). Strip: home · lights · power (the long strip
 * adds what's open and the alarm). Small: house power with its sparkline,
 * else people home with lights and switches. Wide: three stats, the power
 * sparkline and energy today (else the four checks), with what needs a
 * look underneath (open doors and windows, unlocked locks, low batteries,
 * updates) and the alarm. Small and Wide page to a Needs a look list when
 * there's something in it. Large: Wide with that list in place, and the
 * entity counts at the foot. A triggered alarm marks the tile.
 *
 * Reads `people_home`, `lights_on`, `switches_on`, `power.kw`, `energy.kwh`,
 * `open`, `locks_unlocked`, `batteries_low`, `updates` and `unavailable`,
 * with `attrs.people_total`, `lights_total`, `switches_total`, `entities`,
 * `alarm`, `open_items`, `battery_items`, `update_items`, `people_items`,
 * `climate_items`, `safety_items`, `safety_total`, `media_items`,
 * `media_total` and `temperature_unit`, with `safety_tripped` (integrations/homeassistant).
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
import { useOptions } from './options';
import { useViews } from './views';

const { size, target } = defineProps<{ size: SizeId; target?: string | undefined }>();
const live = useLive();
const opts = useOptions('homeassistant');
const showPower = computed(() => opts.value['power'] === true);

const app = computed(() => resolve(live.graph.value, target || 'app:homeassistant'));
const s = useSubject(() => app.value?.id);
const ID = computed(() => app.value?.id ?? '');
const m = (k: string) => series(live.metrics.value, ID.value, k);
const people = computed(() => m('people_home'));
const lights = computed(() => m('lights_on'));
const switches = computed(() => m('switches_on'));
const power = computed(() => (showPower.value ? m('power.kw') : undefined));
const energy = computed(() => (showPower.value ? m('energy.kwh') : undefined));
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
  for (const o of list('safety_items')) out.push({ key: `s:${str(o['name'])}`, glyph: 'attn', name: str(o['name']), detail: SAFETY_WORDS[str(o['kind'])] ?? 'tripped' });
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
    count('safety_tripped') ? plural(count('safety_tripped'), 'safety alert') : '',
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
/** The four checks, for the space house power leaves: each is flagged when above zero. */
const checks = computed(() =>
  [
    { k: 'open', label: 'open' },
    { k: 'locks_unlocked', label: 'unlocked' },
    { k: 'batteries_low', label: 'low batteries' },
    { k: 'updates', label: 'updates' },
  ].map((c) => ({ ...c, n: m(c.k)?.value ?? 0, known: m(c.k) !== undefined })),
);
/** Who's home, by name, under the number (the count alone when the integration doesn't send names). */
const homeNames = computed(() =>
  list('people_items')
    .filter((p) => str(p['where']) === 'home')
    .map((p) => str(p['name']))
    .filter(Boolean)
    .join(', '),
);
interface Card {
  key: string;
  value: string;
  unit?: string;
  label: string;
  caption: string;
  flag?: boolean;
}
const num = (v: unknown) => (typeof v === 'number' && Number.isFinite(v) ? v : undefined);
const tempUnit = computed(() => str(attr<unknown>(app.value, 'temperature_unit')) || '°');
const ACTIONS: Record<string, string> = { heating: 'heating', cooling: 'cooling', drying: 'drying', fan: 'fan on', heat: 'heat', cool: 'cool', heat_cool: 'auto', auto: 'auto' };
const SAFETY_WORDS: Record<string, string> = { smoke: 'smoke', carbon_monoxide: 'carbon monoxide', gas: 'gas', moisture: 'water leak' };
/** The cards that fill the space house power leaves, for what this house has. */
const cards = computed<Card[]>(() => {
  const out: Card[] = [];
  const rooms = list('climate_items');
  const temps = rooms.map((r) => num(r['current'])).filter((v): v is number => v !== undefined);
  if (temps.length) {
    const avg = temps.reduce((a, b) => a + b, 0) / temps.length;
    const doing = rooms.map((r) => ACTIONS[str(r['action'])]).filter((a): a is string => Boolean(a) && a !== 'heat' && a !== 'cool' && a !== 'auto');
    const one = rooms.length === 1 ? str(rooms[0]!['name']) : `${rooms.length} rooms`;
    out.push({ key: 'climate', value: avg.toFixed(1), unit: tempUnit.value, label: 'indoors', caption: [one, doing[0]].filter(Boolean).join(' · ') });
  }
  const trippedN = m('safety_tripped')?.value ?? 0;
  const sensors = total('safety_total') ?? 0;
  if (sensors > 0) {
    const first = list('safety_items')[0];
    out.push({
      key: 'safety',
      value: trippedN ? fmtInt(trippedN) : 'OK',
      label: trippedN ? 'tripped' : 'safety',
      caption: trippedN ? [str(first?.['name']), SAFETY_WORDS[str(first?.['kind'])]].filter(Boolean).join(' · ') : `${plural(sensors, 'sensor')}, all quiet`,
      flag: trippedN > 0,
    });
  }
  const players = total('media_total') ?? 0;
  if (players > 0) {
    const playing = list('media_items');
    const first = playing[0];
    out.push({
      key: 'media',
      value: fmtInt(m('media_playing')?.value ?? playing.length),
      label: 'playing',
      caption: first
        ? [str(first['title']), str(first['artist'])].filter(Boolean).join(' · ') || str(first['name'])
        : (m('media_playing')?.value ?? 0) > 0
          ? `of ${plural(players, 'player')}`
          : `${plural(players, 'player')}, all quiet`,
    });
  }
  return out;
});
const tripped = computed(() => (m('safety_tripped')?.value ?? 0) > 0);
/** The small tile's foot without power: indoors, then what needs a look and the alarm. */
const smallFoot = computed(() => {
  const c = cards.value.find((x) => x.key === 'climate');
  return [c ? `${c.value}${c.unit ?? ''} indoors` : '', lookLine.value, alarm.value ? `alarm ${alarm.value.toLowerCase()}` : ''].filter(Boolean).join(' · ');
});
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
    :status="app ? ((triggered || tripped) && s.status === 'ok' ? 'attention' : undefined) : 'unknown'"
    :tag="tripped ? 'Safety alert' : triggered ? 'Alarm triggered' : undefined"
    :tag-glyph="tripped || triggered ? 'attn' : undefined"
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
      <template v-if="showPower">
        <MiniGraph :values="power?.history" :band="power?.band" :stale="power?.stale" :height="22" label="House power, 24 hours" :format="kw" />
        <span :class="['sv', 'n', { unknown: power?.value == null }]">{{ fmtNum(power?.value) }}<small>kW</small></span>
      </template>
      <span v-else :class="['sv', 'n', { unknown: switches?.value == null }]">{{ fmtInt(switches?.value) }}<small>switches</small></span>
    </template>

    <div v-else-if="view === 'look'" class="list" role="list" aria-label="Needs a look in Home Assistant">
      <div v-for="l in look.slice(0, LOOK_ROWS)" :key="l.key" class="li" role="listitem">
        <Glyph :kind="l.glyph" :label="GLYPH_LABEL[l.glyph] ?? 'needs a look'" />
        <span class="t">{{ l.name }}</span>
        <span class="n">{{ l.detail }}</span>
      </div>
      <small v-if="look.length > LOOK_ROWS" class="ha-more">+{{ look.length - LOOK_ROWS }} more</small>
    </div>

    <template v-else-if="size === 'small' && !showPower">
      <div :class="['v', { unknown: people?.value == null }]">{{ fmtInt(people?.value) }}<span class="u">/{{ total('people_total') ?? '—' }} home</span></div>
      <p class="lab"><span class="n">{{ fmtInt(lights?.value) }}</span> lights on · <span class="n">{{ fmtInt(switches?.value) }}</span> switches</p>
      <StateNote :subject="s" :last-known="Boolean(people)" />
      <div v-if="smallFoot" class="tf">
        <span>{{ smallFoot }}</span>
      </div>
    </template>

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
            <div class="lab ha-names" :title="homeNames || undefined">{{ homeNames || 'home' }}</div>
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
        <div v-if="!showPower && cards.length" class="ha-cards" role="group" aria-label="House">
          <div v-for="c in cards" :key="c.key" :class="['ha-card', { flag: c.flag }]">
            <div class="v sm">{{ c.value }}<span v-if="c.unit" class="u">{{ c.unit }}</span><span class="lab">{{ c.label }}</span></div>
            <div class="lab ha-cap" :title="c.caption">{{ c.caption }}</div>
          </div>
        </div>
        <dl v-else-if="!showPower" class="ha-checks" aria-label="House checks">
          <div v-for="c in checks" :key="c.k" :class="['ha-check', { flag: c.n > 0 }]">
            <dd :class="['v', 'sm', { unknown: !c.known }]">{{ c.known ? fmtInt(c.n) : '—' }}</dd>
            <dt class="lab">{{ c.label }}</dt>
          </div>
        </dl>
        <div v-else class="col">
          <div class="row">
            <div :class="['v', 'sm', { unknown: power?.value == null }]">{{ fmtNum(power?.value) }}<span class="u">kW</span></div>
            <span class="lab"><span class="n">{{ fmtNum(energy?.value) }}</span> kWh today</span>
          </div>
          <MiniGraph :values="power?.history" :band="power?.band" :stale="power?.stale" :height="40" label="House power" :format="kw" />
        </div>
      </div>
      <StateNote :subject="s" :last-known="Boolean(power ?? people)" />
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
      <div v-else-if="showPower ? lookLine || alarm : alarm" class="tf">
        <span>{{ (showPower ? [lookLine, alarm ? `alarm ${alarm.toLowerCase()}` : ''] : [`alarm ${alarm.toLowerCase()}`]).filter(Boolean).join(' · ') }}</span>
      </div>
    </template>
  </Frame>
</template>

<style>
.ha-cards {
  display: grid;
  grid-template-columns: repeat(auto-fit, minmax(84px, 1fr));
  gap: var(--space-3);
  min-width: 0;
}
.ha-card {
  display: flex;
  flex-direction: column;
  gap: 2px;
  min-width: 0;
}
.ha-card .v {
  display: flex;
  align-items: baseline;
  gap: 4px;
  white-space: nowrap;
}
.ha-card .v .lab {
  margin-left: 2px;
}
.ha-card.flag .v {
  color: var(--attention-text);
}
.ha-cap,
.ha-names {
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}
.ha-checks {
  display: grid;
  grid-template-columns: repeat(2, minmax(0, 1fr));
  gap: var(--space-2) var(--space-4);
  margin: 0;
}
.ha-check {
  display: flex;
  align-items: baseline;
  gap: var(--space-2);
}
.ha-check dd,
.ha-check dt {
  margin: 0;
}
.ha-check.flag .v {
  color: var(--attention-text);
}
.ha-more {
  color: var(--text-3);
  font-size: var(--type-caption);
}
</style>
