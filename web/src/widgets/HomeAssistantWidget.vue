<script setup lang="ts">
/**
 * Home Assistant: who's home, and the house at a glance. Target: an instance
 * (`homeassistant@homeassistant:<instance id>/app`), else the first.
 *
 * Wide and large lead with presence: a mark per person (filled when home,
 * outlined when away), who's home by name, and where the others are. Under
 * it, a row of facets for what this house reports, the pressing ones first:
 * Indoors (the thermostats' average and what they're doing), Lights (with
 * switches under), Safety (smoke, CO, gas and leak sensors), Doors (open and
 * unlocked), Playing (media players, with the track) and, with the widget
 * setting `options.power` (off by default), Power with energy today. A
 * facet shows only when the house has the sensors for it; a flagged one (a
 * tripped sensor, an open door) moves to the front. Wide shows four, large
 * six. The alarm's state is the header's word.
 *
 * Views: Now, then Needs a look (what's tripped, open, unlocked, low on
 * battery or waiting for an update), People (where each person is, and
 * since when), Climate (each thermostat: temperature, target, what it's
 * doing, humidity) and Playing (what each player plays), each only when
 * there's something in it. Small and wide page between them; large shows
 * Needs a look, Climate and Playing as sections under the facets, and the
 * entity counts at the foot.
 *
 * Click-through: on wide and large, each facet with a list behind it, the
 * presence line and the needs-a-look line are buttons that open the panel
 * (HomeAssistantPanel) on that list: every light and switch, each
 * thermostat, each player, each person, everything that needs a look. When
 * the instance's `control` allows it, the panel switches lights and
 * switches, plays and pauses players and sets thermostats. Never while
 * editing the board.
 *
 * Glyph: people home (2/2). Strip: home · lights · indoors, with power or
 * the indoor temperature at the end (the long strip adds what needs a
 * look). Small: people home with their marks, else house power with its
 * sparkline. A triggered alarm or a tripped safety sensor marks the tile.
 *
 * Reads `people_home`, `lights_on`, `switches_on`, `power.kw`, `energy.kwh`,
 * `open`, `locks_unlocked`, `batteries_low`, `updates`, `unavailable`,
 * `safety_tripped` and `media_playing`, with `attrs.people_total`,
 * `lights_total`, `switches_total`, `entities`, `alarm`, `open_items`,
 * `battery_items`, `update_items`, `people_items`, `climate_items`,
 * `safety_items`, `safety_total`, `media_items`, `media_total` and
 * `temperature_unit` (integrations/homeassistant).
 */
import { computed } from 'vue';
import type { SizeId } from '@chadash/layout';
import Glyph from '../components/marks/Glyph.vue';
import MiniGraph from '../components/marks/MiniGraph.vue';
import { fmtInt, fmtNum, plural } from '../lib/format';
import { attr, placeOf, resolve, series } from '../model/graph';
import { resourceLinks } from '../model/types';
import { useLive } from '../stores/live';
import { useUi } from '../stores/ui';
import { ACTIVE, GLYPH_LABEL, SAFETY_WORDS, devicesOf, list as listOf, lookOf, personsOf, playingOf, roomLine, roomsOf, str, words, type PanelTab } from './homeassistant';
import Frame from './parts/Frame.vue';
import MediaMark from './parts/MediaMark.vue';
import StateNote from './parts/StateNote.vue';
import { useSubject } from './use';
import { useOptions } from './options';
import { useViews } from './views';

const { size, target } = defineProps<{ size: SizeId; target?: string | undefined }>();
const live = useLive();
const ui = useUi();
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

const list = (key: string) => listOf(app.value, key);
const count = (k: string) => m(k)?.value ?? 0;

/** The alarm panel's state as words: "armed_home" → "armed home", the header's word while healthy. */
const alarm = computed(() => words(str(attr<unknown>(app.value, 'alarm'))).toLowerCase());
const triggered = computed(() => attr<string>(app.value, 'alarm') === 'triggered');
const tripped = computed(() => count('safety_tripped') > 0);

/** What needs a look, most pressing first: tripped, open, unlocked, low batteries, updates. */
const look = computed(() => lookOf(app.value, count('locks_unlocked')));
/** The counts behind the list, as one line: "3 open · 1 unlocked · 2 low batteries · 2 updates". */
const lookLine = computed(() =>
  [
    tripped.value ? plural(count('safety_tripped'), 'safety alert') : '',
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
    switches.value ? `${fmtInt(switches.value.value)} switches on` : '',
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

// Presence.
const persons = computed(() => personsOf(app.value, live.now.value));
const FACES = 5;
const homeNames = computed(() =>
  persons.value
    .filter((p) => p.home)
    .map((p) => p.name)
    .join(', '),
);
/** The presence line: who's home, by name when the integration sends them. */
const presence = computed(() => {
  const ps = persons.value;
  if (!ps.length) return people.value ? 'home' : '';
  const home = ps.filter((p) => p.home);
  if (home.length === ps.length) return ps.length === 1 ? `${home[0]!.name} is home` : 'Everyone’s home';
  if (!home.length) return 'Nobody’s home';
  return `${homeNames.value} home`;
});
/** Where the others are: "Sam at Work · Jo away". */
const elsewhere = computed(() =>
  persons.value
    .filter((p) => !p.home)
    .map((p) => (p.where === 'Away' ? `${p.name} away` : p.where === 'Unknown' ? `${p.name} unknown` : `${p.name} at ${p.where}`))
    .join(' · '),
);

// Climate and media.
const tempUnit = computed(() => str(attr<unknown>(app.value, 'temperature_unit')) || '°');
const rooms = computed(() => roomsOf(app.value));
const playing = computed(() => playingOf(app.value));
const playingN = computed(() => m('media_playing')?.value ?? playing.value.length);

// The facets.
interface Facet {
  key: string;
  label: string;
  value: string;
  unit?: string;
  caption: string;
  /** Something to see to: drawn in attention and moved to the front. */
  flag?: boolean;
  /** The value is a word ("OK"), set in the UI face. */
  word?: boolean;
  unknown?: boolean;
  rank: number;
}
const facets = computed<Facet[]>(() => {
  const out: Facet[] = [];
  if (showPower.value && power.value) {
    out.push({ key: 'power', label: 'Power', value: fmtNum(power.value.value), unit: 'kW', caption: energy.value ? `${fmtNum(energy.value.value)} kWh today` : 'now', unknown: power.value.value == null, rank: 1 });
  }
  if (rooms.value.length) {
    const avg = rooms.value.reduce((a, r) => a + r.current, 0) / rooms.value.length;
    const doing = rooms.value.find((r) => ACTIVE.has(r.action));
    const where = rooms.value.length === 1 ? rooms.value[0]!.name : `${rooms.value.length} rooms`;
    out.push({ key: 'climate', label: 'Indoors', value: avg.toFixed(1), unit: tempUnit.value, caption: doing ? `${doing.name} ${doing.action}` : where, rank: 2 });
  }
  if (lights.value) {
    const of = total('lights_total');
    out.push({
      key: 'lights',
      label: 'Lights',
      value: fmtInt(lights.value.value),
      ...(of !== undefined ? { unit: `/${fmtInt(of)}` } : {}),
      caption: switches.value ? plural(switches.value.value ?? 0, 'switch', 'switches') : 'on',
      unknown: lights.value.value == null,
      rank: 3,
    });
  }
  const sensors = total('safety_total') ?? 0;
  if (sensors > 0) {
    const first = list('safety_items')[0];
    out.push({
      key: 'safety',
      label: 'Safety',
      value: tripped.value ? fmtInt(count('safety_tripped')) : 'OK',
      ...(tripped.value ? { unit: 'tripped' } : {}),
      caption: tripped.value ? [str(first?.['name']), SAFETY_WORDS[str(first?.['kind'])]].filter(Boolean).join(' · ') || 'tripped' : plural(sensors, 'sensor'),
      flag: tripped.value,
      word: !tripped.value,
      rank: 5,
    });
  }
  if (hasLook.value || m('open') || m('locks_unlocked')) {
    const open = count('open');
    const unlocked = count('locks_unlocked');
    const n = open + unlocked;
    const firstOpen = list('open_items')[0];
    out.push({
      key: 'doors',
      label: 'Doors',
      value: n ? fmtInt(n) : 'Shut',
      caption: n
        ? n === 1 && open && firstOpen
          ? `${str(firstOpen['name'])} open`
          : [open ? `${fmtInt(open)} open` : '', unlocked ? `${fmtInt(unlocked)} unlocked` : ''].filter(Boolean).join(' · ')
        : total('locks_total')
          ? 'closed and locked'
          : 'all closed',
      flag: n > 0,
      word: !n,
      rank: 6,
    });
  }
  const players = total('media_total') ?? 0;
  if (players > 0) {
    const first = playing.value[0];
    out.push({
      key: 'media',
      label: 'Playing',
      value: fmtInt(playingN.value),
      caption: first ? [first.title, first.artist].filter(Boolean).join(' · ') || first.player : playingN.value ? `on ${plural(playingN.value, 'player')}` : 'nothing on',
      rank: playingN.value ? 4 : 7,
    });
  }
  if (!lights.value && switches.value) out.push({ key: 'switches', label: 'Switches', value: fmtInt(switches.value.value), caption: 'on', rank: 8 });
  return out.sort((a, b) => Number(Boolean(b.flag)) - Number(Boolean(a.flag)) || a.rank - b.rank);
});
const shown = computed(() => facets.value.slice(0, size === 'large' ? 6 : 4));

/** The small tile's foot without power: indoors and lights, then what needs a look. */
const smallFoot = computed(() => {
  const c = facets.value.find((x) => x.key === 'climate');
  return [c ? `${c.value}${c.unit} indoors` : '', lights.value ? `${fmtInt(lights.value.value)} lights on` : '', lookLine.value].filter(Boolean).join(' · ');
});
/** The strip's end: power when it's on, else indoors, else switches. */
const stripEnd = computed(() => {
  const c = facets.value.find((x) => x.key === 'climate');
  if (c) return { value: c.value, unit: c.unit ?? '', unknown: false };
  return { value: fmtInt(switches.value?.value), unit: 'switches', unknown: switches.value?.value == null };
});

const LOOK_ROWS = 4;
const ROWS = 4;

// The panel: which list each facet opens, when it has one.
const hasLights = computed(() => devicesOf(app.value, 'light_items').length > 0);
const hasSwitches = computed(() => devicesOf(app.value, 'switch_items').length > 0);
const hasPlayers = computed(() => devicesOf(app.value, 'player_items').length > 0);
const FACET_TAB: Record<string, PanelTab> = { climate: 'climate', lights: 'lights', switches: 'switches', safety: 'look', doors: 'look', media: 'media' };
/** Safety and Doors open Needs a look only while they're flagged: "Shut" has nothing behind it. */
function tabFor(key: string, flag?: boolean): PanelTab | undefined {
  const t = FACET_TAB[key];
  if (!t || !app.value) return undefined;
  if (t === 'lights') return hasLights.value ? t : hasSwitches.value ? 'switches' : undefined;
  if (t === 'switches') return hasSwitches.value ? t : undefined;
  if (t === 'media') return hasPlayers.value ? t : undefined;
  if (t === 'climate') return rooms.value.length ? t : undefined;
  return flag && look.value.length ? t : undefined;
}
function openPanel(tab: PanelTab): void {
  if (app.value && !ui.state.editing) ui.openHomePanel(app.value.id, tab);
}
const facetName = (f: { label: string; value: string; unit?: string | undefined; caption: string }) => `${f.label}: ${f.value}${f.unit ?? ''}, ${f.caption}. Show all`;
const { views, view } = useViews(() => {
  if (size !== 'small' && size !== 'wide') return [];
  const v = [{ id: 'now', label: 'Now' }];
  if (look.value.length) v.push({ id: 'look', label: 'Needs a look' });
  if (persons.value.length) v.push({ id: 'people', label: 'People' });
  if (rooms.value.length) v.push({ id: 'climate', label: 'Climate' });
  if (playing.value.length) v.push({ id: 'media', label: 'Playing' });
  return v;
});
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
    :ok-tag="alarm ? `alarm ${alarm}` : ''"
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
      <span class="sx"><span class="n">{{ fmtInt(people?.value) }}</span> home · <span class="n">{{ fmtInt(lights?.value) }}</span> lights<template v-if="size === 'strip-long' && lookLine"> · {{ lookLine }}</template></span>
      <template v-if="showPower">
        <MiniGraph :values="power?.history" :band="power?.band" :stale="power?.stale" :height="22" label="House power, 24 hours" :format="kw" />
        <span :class="['sv', 'n', { unknown: power?.value == null }]">{{ fmtNum(power?.value) }}<small>kW</small></span>
      </template>
      <span v-else :class="['sv', 'n', { unknown: stripEnd.unknown }]">{{ stripEnd.value }}<small>{{ stripEnd.unit }}</small></span>
    </template>

    <div v-else-if="view === 'look'" class="list" role="list" aria-label="Needs a look in Home Assistant">
      <div v-for="l in look.slice(0, LOOK_ROWS)" :key="l.key" class="li" role="listitem">
        <Glyph :kind="l.glyph" :label="GLYPH_LABEL[l.glyph] ?? 'needs a look'" />
        <span class="t">{{ l.name }}</span>
        <span class="n">{{ l.detail }}</span>
      </div>
      <small v-if="look.length > LOOK_ROWS" class="ha-more">+{{ look.length - LOOK_ROWS }} more</small>
    </div>

    <div v-else-if="view === 'people'" class="list" role="list" aria-label="People in Home Assistant">
      <div v-for="p in persons.slice(0, ROWS)" :key="p.name" class="li ha-li" role="listitem">
        <span :class="['ha-face', { home: p.home }]" aria-hidden="true">{{ p.initial }}</span>
        <span class="t">{{ p.name }}<small>{{ p.since }}</small></span>
        <span :class="['ha-where', { home: p.home }]">{{ p.where }}</span>
      </div>
      <small v-if="persons.length > ROWS" class="ha-more">+{{ persons.length - ROWS }} more</small>
    </div>

    <div v-else-if="view === 'climate'" class="list" role="list" aria-label="Climate in Home Assistant">
      <div v-for="r in rooms.slice(0, ROWS)" :key="r.name" class="li ha-li ha-room" role="listitem">
        <span class="t">{{ r.name }}<small>{{ roomLine(r) }}</small></span>
        <span class="ha-temp">{{ r.current.toFixed(1) }}<span class="u">{{ tempUnit }}</span></span>
      </div>
      <small v-if="rooms.length > ROWS" class="ha-more">+{{ rooms.length - ROWS }} more</small>
    </div>

    <div v-else-if="view === 'media'" class="list" role="list" aria-label="Playing in Home Assistant">
      <div v-for="t in playing.slice(0, ROWS)" :key="t.key" class="li ha-li" role="listitem">
        <MediaMark :kind="t.artist ? 'music' : 'tv'" />
        <span class="t">{{ t.title || t.player }}<small>{{ [t.artist, t.title ? t.player : ''].filter(Boolean).join(' · ') }}</small></span>
      </div>
    </div>

    <template v-else-if="size === 'small' && !showPower">
      <div :class="['v', { unknown: people?.value == null }]">{{ fmtInt(people?.value) }}<span class="u">/{{ total('people_total') ?? '—' }} home</span></div>
      <div v-if="persons.length" class="ha-faces" role="img" :aria-label="[presence, elsewhere].filter(Boolean).join('. ')">
        <span v-for="p in persons.slice(0, FACES)" :key="p.name" :class="['ha-face', { home: p.home }]" :title="`${p.name} · ${p.where}`">{{ p.initial }}</span>
        <span v-if="persons.length > FACES" class="ha-face more">+{{ persons.length - FACES }}</span>
      </div>
      <p v-else class="lab"><span class="n">{{ fmtInt(lights?.value) }}</span> lights on · <span class="n">{{ fmtInt(switches?.value) }}</span> switches</p>
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
      <div class="ha-pres">
        <div v-if="persons.length" class="ha-faces" aria-hidden="true">
          <span v-for="p in persons.slice(0, FACES)" :key="p.name" :class="['ha-face', { home: p.home }]" :title="`${p.name} · ${p.where}`">{{ p.initial }}</span>
          <span v-if="persons.length > FACES" class="ha-face more">+{{ persons.length - FACES }}</span>
        </div>
        <component
          :is="persons.length ? 'button' : 'div'"
          class="ha-who"
          v-bind="persons.length ? { type: 'button', 'aria-label': `${[presence, elsewhere].filter(Boolean).join('. ')}. Show people`, tabindex: ui.state.editing ? -1 : undefined } : {}"
          @click="persons.length && openPanel('people')"
        >
          <div :class="['v', 'sm', { unknown: people?.value == null }]">
            {{ fmtInt(people?.value) }}<span class="u">/{{ total('people_total') ?? '—' }}</span><span class="ha-names lab" :title="presence">{{ presence }}</span>
          </div>
          <div v-if="elsewhere" class="lab ha-else" :title="elsewhere">{{ elsewhere }}</div>
        </component>
        <MiniGraph v-if="showPower && power && size === 'wide'" class="ha-graph" :values="power.history" :band="power.band" :stale="power.stale" :height="30" label="House power, 24 hours" :format="kw" />
      </div>

      <dl v-if="shown.length" :class="['ha-facets', `n${shown.length}`]" aria-label="House">
        <div v-for="f in shown" :key="f.key" :class="['ha-facet', `ha-${f.key}`, { flag: f.flag, opens: tabFor(f.key, f.flag) }]">
          <dt class="ha-k">
            {{ f.label
            }}<button
              v-if="tabFor(f.key, f.flag)"
              type="button"
              class="ha-open"
              :aria-label="facetName(f)"
              :tabindex="ui.state.editing ? -1 : undefined"
              @click="openPanel(tabFor(f.key, f.flag)!)"
            />
          </dt>
          <dd :class="['v', 'sm', { word: f.word, unknown: f.unknown }]">{{ f.value }}<span v-if="f.unit" class="u">{{ f.unit }}</span></dd>
          <dd class="lab ha-cap" :title="f.caption">{{ f.caption }}</dd>
        </div>
      </dl>
      <StateNote :subject="s" :last-known="Boolean(power ?? people)" />

      <template v-if="size === 'large'">
        <MiniGraph v-if="showPower && power" :values="power.history" :band="power.band" :stale="power.stale" :height="36" label="House power, 24 hours" :format="kw" />
        <div class="list ha-sections">
          <template v-if="look.length">
            <p class="ha-k">Needs a look</p>
            <div role="list" aria-label="Needs a look in Home Assistant">
              <div v-for="l in look.slice(0, 5)" :key="l.key" class="li" role="listitem">
                <Glyph :kind="l.glyph" :label="GLYPH_LABEL[l.glyph] ?? 'needs a look'" />
                <span class="t">{{ l.name }}</span>
                <span class="n">{{ l.detail }}</span>
              </div>
            </div>
            <small v-if="look.length > 5" class="ha-more">+{{ look.length - 5 }} more</small>
          </template>
          <p v-else-if="hasLook" class="ha-ok lab"><Glyph kind="check" />All closed, locked and up to date.</p>
          <dl v-else class="kv">
            <template v-for="r in rows" :key="r.k">
              <dt>{{ r.k }}</dt>
              <dd>{{ fmtInt(r.v) }}{{ r.of !== undefined ? ` of ${fmtInt(r.of)}` : '' }}</dd>
            </template>
          </dl>
          <template v-if="rooms.length">
            <p class="ha-k">Climate</p>
            <div role="list" aria-label="Climate in Home Assistant">
              <div v-for="r in rooms.slice(0, 3)" :key="r.name" class="li ha-li ha-room" role="listitem">
                <span class="t">{{ r.name }}<small>{{ roomLine(r) }}</small></span>
                <span class="ha-temp">{{ r.current.toFixed(1) }}<span class="u">{{ tempUnit }}</span></span>
              </div>
            </div>
          </template>
          <template v-if="playing.length">
            <p class="ha-k">Playing</p>
            <div role="list" aria-label="Playing in Home Assistant">
              <div v-for="t in playing.slice(0, 3)" :key="t.key" class="li ha-li" role="listitem">
                <MediaMark :kind="t.artist ? 'music' : 'tv'" />
                <span class="t">{{ t.title || t.player }}<small>{{ [t.artist, t.title ? t.player : ''].filter(Boolean).join(' · ') }}</small></span>
              </div>
            </div>
          </template>
        </div>
        <div v-if="footLine" class="tf"><span>{{ footLine }}</span></div>
      </template>
      <div v-else-if="lookLine" class="tf">
        <button v-if="look.length" type="button" class="ha-look lk" :tabindex="ui.state.editing ? -1 : undefined" @click="openPanel('look')">Needs a look: {{ lookLine }}</button>
        <span v-else>Needs a look: {{ lookLine }}</span>
      </div>
    </template>
  </Frame>
</template>

<style>
/* Presence: a mark per person, who's home, where the others are. */
.ha-pres {
  display: flex;
  align-items: center;
  gap: var(--space-3);
  min-width: 0;
}
.ha-who {
  flex: 1;
  min-width: 0;
  display: flex;
  flex-direction: column;
  gap: 3px;
}
.ha-who .v {
  flex-wrap: nowrap;
}
.ha-who .v .ha-names {
  margin-left: var(--space-2);
  overflow: hidden;
  text-overflow: ellipsis;
  font-family: var(--font-ui);
  letter-spacing: 0;
  color: var(--text-2);
}
.ha-graph {
  flex: 0 1 40%;
  min-width: 0;
}
.ha-faces {
  display: flex;
  flex: none;
  padding-left: 6px;
}
.ha-face {
  flex: none;
  display: grid;
  place-items: center;
  width: 26px;
  height: 26px;
  margin-left: -6px;
  border-radius: var(--radius-full);
  border: 1.5px dashed var(--line-2);
  background: var(--surface);
  box-shadow: 0 0 0 2px var(--surface);
  font: var(--weight-medium) 11px/1 var(--font-ui);
  color: var(--text-3);
}
.ha-face.home {
  border: 1.5px solid var(--text);
  background: var(--text);
  color: var(--surface);
}
.ha-face.more {
  border-style: solid;
  font-size: 10px;
}
.li .ha-face {
  width: 22px;
  height: 22px;
  margin: 0;
  box-shadow: none;
  font-size: 10px;
}
.s-small .ha-faces {
  margin-top: var(--space-2);
}

/* The facets: one row of evenly spaced cells, hairlines between. */
.ha-facets {
  display: grid;
  grid-template-columns: repeat(4, minmax(0, 1fr));
  margin: auto 0 0;
  padding-top: var(--space-3);
  border-top: var(--hairline) solid var(--line);
  row-gap: var(--space-3);
}
.ha-facets.n1 {
  grid-template-columns: minmax(0, 1fr);
}
.ha-facets.n2 {
  grid-template-columns: repeat(2, minmax(0, 1fr));
}
.ha-facets.n3,
.s-large .ha-facets {
  grid-template-columns: repeat(3, minmax(0, 1fr));
}
.s-large .ha-facets {
  margin-top: var(--space-1);
}
.ha-facet {
  display: flex;
  flex-direction: column;
  gap: 4px;
  min-width: 0;
  padding: 0 var(--space-3);
  border-left: var(--hairline) solid var(--line);
}
/* A facet that opens the panel: the whole cell is the button's target. */
.ha-facet.opens {
  position: relative;
}
.ha-open {
  position: absolute;
  inset: calc(var(--space-2) * -1) 0;
  padding: 0;
  border: 0;
  border-radius: var(--radius-md);
  background: none;
  cursor: pointer;
}
.ha-facet.opens:hover .ha-k,
.ha-facet.opens:has(.ha-open:focus-visible) .ha-k {
  color: var(--text);
}
.ha-facet.opens.flag:hover .ha-k {
  color: var(--attention-text);
}
.ha-open:focus-visible {
  outline: 2px solid var(--focus);
  outline-offset: 2px;
}
button.ha-who {
  padding: 0;
  border: 0;
  background: none;
  color: inherit;
  font: inherit;
  text-align: left;
  cursor: pointer;
}
button.ha-who:hover .ha-names {
  color: var(--text);
}
.ha-look {
  padding: 0;
  border: 0;
  background: none;
  color: inherit;
  font: inherit;
  text-align: left;
  cursor: pointer;
}
.ha-look:hover {
  color: var(--text);
}
.ha-facet:first-child,
.s-large .ha-facet:nth-child(3n + 1) {
  padding-left: 0;
  border-left: 0;
}
.ha-facet dd {
  margin: 0;
}
.ha-facet.flag .v,
.ha-facet.flag .ha-k {
  color: var(--attention-text);
}
.ha-k {
  margin: 0;
  font: var(--weight-medium) 9.5px/1 var(--font-num);
  letter-spacing: 0.14em;
  text-transform: uppercase;
  color: var(--text-3);
}
:root[data-theme='paper'] .ha-k {
  font: italic 400 13px/1.1 var(--font-head);
  letter-spacing: 0;
  text-transform: none;
  color: var(--text-2);
}
.ha-cap,
.ha-else {
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}
@container (max-width: 300px) {
  .ha-facets,
  .ha-facets.n3,
  .s-large .ha-facets {
    grid-template-columns: repeat(2, minmax(0, 1fr));
  }
  .ha-facet:nth-child(odd) {
    padding-left: 0;
    border-left: 0;
  }
  .ha-facet:nth-child(even) {
    padding-left: var(--space-3);
    border-left: var(--hairline) solid var(--line);
  }
  .s-wide .ha-facet:nth-child(n + 3) {
    display: none;
  }
}

/* The lists: people, rooms and players. */
.ha-li {
  grid-template-columns: auto minmax(0, 1fr) auto;
  align-items: center;
}
.ha-li .md-ic {
  width: 14px;
  height: 14px;
  fill: none;
  stroke: var(--text-3);
  stroke-width: 1.3;
}
.ha-room {
  grid-template-columns: minmax(0, 1fr) auto;
}
.ha-where {
  font-size: var(--type-caption);
  color: var(--text-3);
}
.ha-where.home {
  color: var(--text);
}
.ha-temp {
  font: var(--weight-medium) var(--type-body) / 1 var(--font-num);
  font-variant-numeric: tabular-nums;
  color: var(--text);
}
.ha-temp .u {
  margin-left: 1px;
  font-size: var(--type-caption);
  color: var(--text-3);
}
.ha-sections {
  gap: 0;
}
.ha-sections > .ha-k {
  margin: var(--space-3) 0 var(--space-1);
}
.ha-sections > .ha-k:first-child {
  margin-top: var(--space-2);
}
.ha-ok {
  display: flex;
  align-items: center;
  gap: var(--space-2);
  margin: var(--space-2) 0 0;
}
.ha-ok .g {
  width: 10px;
  height: 10px;
}
.ha-more {
  color: var(--text-3);
  font-size: var(--type-caption);
}
</style>
