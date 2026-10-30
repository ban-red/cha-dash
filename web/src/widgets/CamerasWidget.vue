<script setup lang="ts">
/**
 * Camera Hero (Frigate). Each frame is the camera's current picture when the
 * Frigate connection shares pictures (`attrs.picture`, fetched through the
 * hub by CameraPicture, refreshed while on screen), else a placeholder drawn
 * from neutral tokens (CameraScene); either way captioned with the camera's
 * last detection. Target: a Frigate instance (`cameras@frigate:<instance
 * id>/app`, its cameras are what it `exposes`), one camera, or with none,
 * every camera. A frame opens its camera's sheet.
 *
 * Small: the latest detection with its caption. Wide: the cameras side by
 * side. Panel and Large: one big frame and two small. Hero: three frames and
 * the recent detections. Panel, Large and Hero have a control row for the
 * big camera (detection, recording, the camera, snapshots, alerts) when the
 * viewer may run Frigate's actions.
 *
 * Widget settings (WidgetSpec.options): which cameras, pictures on or off,
 * how often they refresh, the detections list, the controls.
 *
 * A camera offline while others record is attention; Frigate down keeps the
 * last frames, hatched, "last frame 14:02". A camera turned off on purpose
 * (privacy) is quiet and dimmed. While the integration needs repair, events
 * are "—".
 */
import { computed, ref, watch } from 'vue';
import type { SizeId } from '@chadash/layout';
import Glyph from '../components/marks/Glyph.vue';
import type { TileStatus } from '../components/Tile.vue';
import CameraPicture from './CameraPicture.vue';
import CameraScene from './CameraScene.vue';
import { fmtInt, hhmm, plural } from '../lib/format';
import { attr, byKind, incoming, label, outgoing, placeOf, resolve, value } from '../model/graph';
import { glyphFor } from '../model/status';
import { normalizeHealth, resourceLinks, type Resource } from '../model/types';
import { listActions, type Offer } from '../lib/actions';
import { useLive } from '../stores/live';
import { useUi } from '../stores/ui';
import { useOptions } from './options';
import Frame from './parts/Frame.vue';
import StateNote from './parts/StateNote.vue';
import { statusOf } from './subject';
import { useSubject } from './use';

const { size, target } = defineProps<{ size: SizeId; target?: string | undefined }>();
const live = useLive();
const ui = useUi();
const opts = useOptions('cameras');

// The Demo Home's cameras, each with its own scene (CameraScene: drive, deck, garden; the garage reuses the drive).
const ORDER = ['driveway', 'front_door', 'garden', 'garage'];
const picked = computed(() => (target ? resolve(live.graph.value, target) : undefined));
const frigate = computed(() => {
  const p = picked.value;
  // An app, or an instance that hasn't published one yet (its repair shows).
  if (p?.kind === 'app' || p?.kind === 'integration') return p;
  if (p?.kind === 'camera') return incoming(live.graph.value, p.id, 'exposes')[0];
  return resolve(live.graph.value, 'app:frigate');
});
const s = useSubject(() => frigate.value?.id);
const cams = computed(() => {
  const g = live.graph.value;
  const p = picked.value;
  const list = p?.kind === 'camera' ? [p] : frigate.value && p ? outgoing(g, frigate.value.id, 'exposes').filter((r) => r.kind === 'camera') : byKind(g, 'camera');
  const rank = (r: Resource) => {
    const i = ORDER.findIndex((k) => r.id.endsWith(k));
    return i < 0 ? 99 : i;
  };
  const pick = opts.value['cameras'] as string[];
  const wanted = pick.length && p?.kind !== 'camera' ? list.filter((r) => pick.includes(r.id)) : list;
  return [...wanted].sort((a, b) => rank(a) - rank(b) || label(a).localeCompare(label(b)));
});
const variant = (c: Resource, i: number) => {
  const k = ORDER.findIndex((o) => c.id.endsWith(o));
  return k >= 0 ? k : i;
};
const camStatus = (c: Resource): TileStatus => statusOf(c);
const live3 = computed(() => cams.value.filter((c) => c.state === 'online' || c.state === 'running'));
const detections = computed(() => cams.value.reduce<number | null>((n, c) => {
  const v = value(live.metrics.value, c.id, 'detections.24h');
  return v === null ? n : (n ?? 0) + v;
}, null));
const frigateDown = computed(() => s.value.status === 'down');
const troubled = computed(() => cams.value.filter((c) => {
  const st = camStatus(c);
  return st === 'down' || st === 'attention';
}));
const status = computed<TileStatus>(() => {
  if (frigateDown.value) return 'down';
  if (s.value.status === 'repair' || s.value.status === 'maintenance') return s.value.status;
  if (cams.value.length && troubled.value.length === cams.value.length && troubled.value.every((c) => camStatus(c) === 'down')) return 'down';
  return troubled.value.length ? 'attention' : 'ok';
});
const frozen = computed(() => live.stale.value || frigateDown.value);
const tag = computed(() => {
  if (!cams.value.length) return status.value === 'ok' ? '' : undefined;
  if (frigateDown.value) return undefined;
  if (troubled.value.length === 1) return `${label(troubled.value[0])} ${camStatus(troubled.value[0]!) === 'down' ? 'offline' : 'degraded'}`;
  if (troubled.value.length) return `${troubled.value.length} cameras need you`;
  return `${live3.value.filter((c) => attr(c, 'record') !== false).length} recording`;
});

function caption(c: Resource): string {
  const st = camStatus(c);
  if (st === 'off') return `${label(c)} · off (as intended)`;
  if (st === 'down') return `${label(c)} · offline`;
  if (st === 'maintenance') return `${label(c)} · maintenance`;
  if (s.value.needsRepair) return `${label(c)} · —`;
  const lbl = attr<string>(c, 'last_event.label');
  const at = attr<string>(c, 'last_event.at');
  const score = attr<number>(c, 'last_event.score');
  if (!lbl || !at) return `${label(c)} · quiet`;
  return `${label(c)} · ${lbl}${score ? ` ${Math.round(score * 100)}%` : ''} · ${hhmm(at)}`;
}
const recent = (c: Resource) => {
  const at = attr<string>(c, 'last_event.at');
  return Boolean(at && live.now.value - Date.parse(at) < 6 * 3_600_000 && attr(c, 'last_event.label') === 'person');
};
const events = computed(() =>
  cams.value
    .filter((c) => attr(c, 'last_event.at'))
    .map((c) => ({ id: c.id, cam: c, text: caption(c), at: attr<string>(c, 'last_event.at')! }))
    .sort((a, b) => Date.parse(b.at) - Date.parse(a.at)),
);
/** Small shows the camera with the newest detection. */
const shown = computed(() => (size === 'small' ? (events.value[0] ? [events.value[0].cam] : cams.value.slice(0, 1)) : size === 'wide' ? cams.value.slice(0, 2) : cams.value.slice(0, 3)));
const showEvents = computed(() => (size === 'hero' || size === 'large') && opts.value['detections'] === true && events.value.length > 0);
const layout = computed(() => (size === 'small' ? 'one' : size === 'wide' ? 'two' : size === 'hero' ? 'hero' : ''));
/** The instance a camera comes from (`integration:<id>`), when it shares pictures and the tile shows them. */
function pictureFrom(c: Resource): string | undefined {
  if (!opts.value['pictures'] || attr(c, 'picture') !== true) return undefined;
  const src = c.source ?? '';
  return src.startsWith('integration:') ? src.slice('integration:'.length) : undefined;
}
const every = computed(() => Number(opts.value['refresh']) || 10);
/** Pixels tall to ask for: the big frame of a large tile is bigger. */
const heightFor = (i: number) => (i === 0 && (size === 'large' || size === 'hero' || size === 'panel') ? 540 : size === 'small' || size === 'wide' ? 360 : 240);
const opens = (c: Resource) => !ui.state.editing && ui.hasSheet(c.id);
function open(c: Resource): void {
  if (opens(c)) ui.openSheet(c.id);
}

/** The control row: Frigate's camera switches, as the hub offers them to this viewer. */
const FEATURES = [
  { key: 'detect', label: 'Detection' },
  { key: 'record', label: 'Recording' },
  { key: 'snapshots', label: 'Snapshots' },
  { key: 'alerts', label: 'Alerts' },
  { key: 'enabled', label: 'Camera' },
] as const;
const main = computed(() => (size === 'panel' || size === 'large' || size === 'hero') && opts.value['controls'] ? shown.value[0] : undefined);
const offers = ref<{ target: string; list: Offer[] }>({ target: '', list: [] });
let asking: AbortController | undefined;
watch(
  () => main.value?.id,
  async (id) => {
    asking?.abort();
    if (!id) return;
    const ctrl = new AbortController();
    asking = ctrl;
    const list = await listActions(id, ctrl.signal);
    if (!ctrl.signal.aborted) offers.value = { target: id, list: list.filter((o) => ui.can.value.act(o.tier, o.household)) };
  },
  { immediate: true },
);
const busy = computed(() => Boolean(main.value && Object.values(ui.tasks.value).some((t) => t.target === main.value!.id && !['succeeded', 'failed', 'cancelled'].includes(t.state))));
const controls = computed(() => {
  const c = main.value;
  if (!c || offers.value.target !== c.id) return [];
  return FEATURES.flatMap((f) => {
    const on = attr<boolean>(c, f.key);
    if (typeof on !== 'boolean') return [];
    const o = offers.value.list.find((x) => x.id === `frigate.camera.${f.key}.${on ? 'off' : 'on'}`);
    return o ? [{ ...f, on, offer: o }] : [];
  });
});
/** Why the switches can't be used (no control account), said once. */
const controlsWhy = computed(() => (controls.value.length && controls.value.every((x) => !x.offer.available) ? controls.value[0]!.offer.unavailable ?? '' : ''));
function toggle(x: { offer: Offer; label: string; on: boolean }): void {
  const c = main.value;
  if (!c || !x.offer.available || busy.value || live.stale.value || ui.state.editing) return;
  void ui.runAction({ action: x.offer.id, target: c.id, label: x.offer.label, tier: x.offer.tier, ...(x.offer.params ? { params: x.offer.params } : {}) });
}

const stamp = computed(() => (frozen.value ? `last frame ${hhmm(live.offlineAt.value ?? normalizeHealth(frigate.value).since)}` : ''));
</script>

<template>
  <Frame
    :size="size"
    name="Cameras"
    mono="FR"
    :context="size === 'small' ? '' : frigate ? `Frigate on ${placeOf(live.graph.value, frigate.id) ?? 'the LAN'}` : ''"
    :href="resourceLinks(frigate)[0]?.url"
    :subject="s"
    :status="status"
    :tag="tag"
    :tag-glyph="tag ? glyphFor(status) : undefined"
    :stale="frigateDown"
    :as-of="frigateDown ? normalizeHealth(frigate).since : undefined"
    :incident="s.problem ?? s.repair ?? live.problems.value.find((i) => troubled.some((c) => c.id === i.target))"
  >
    <template v-if="!cams.length && s.needsRepair">
      <div class="v sm word">—</div>
      <StateNote :subject="s" />
    </template>
    <template v-else-if="!cams.length">
      <div class="v sm word">No cameras</div>
      <p class="lab">Connect Frigate to see your cameras here.</p>
    </template>
    <template v-else>
      <div :class="['cams', layout]">
        <component
          :is="opens(c) ? 'button' : 'div'"
          v-for="(c, i) in shown"
          :key="c.id"
          :type="opens(c) ? 'button' : undefined"
          :class="['cam', { big: i === 0, off: camStatus(c) !== 'ok' && camStatus(c) !== 'attention', frozen, pic: Boolean(pictureFrom(c)) }]"
          :role="opens(c) ? undefined : 'img'"
          :aria-label="caption(c) + (frozen ? `, ${stamp}` : '')"
          @click="open(c)"
        >
          <CameraPicture v-if="pictureFrom(c)" :instance="pictureFrom(c)!" :resource="c.id" :height="heightFor(i)" :every="every" :frozen="frozen">
            <CameraScene :variant="variant(c, i)" :person="recent(c)" />
          </CameraPicture>
          <CameraScene v-else :variant="variant(c, i)" :person="recent(c)" />
          <span class="cam-ts">{{ label(c).toUpperCase() }} {{ i === 0 && !frozen ? hhmm(live.now.value) : '' }}</span>
          <span v-if="i === 0" class="cam-rec">{{ frozen ? 'LAST FRAME' : attr(c, 'record') ? 'REC' : '' }}<template v-if="!frozen && attr(c, 'detect') === false"> · DETECT OFF</template></span>
          <span class="cam-cap">
            <Glyph v-if="camStatus(c) !== 'ok'" :kind="glyphFor(camStatus(c))" />{{ caption(c) }}
          </span>
        </component>
      </div>
      <div v-if="controls.length" class="cam-ctl" role="group" :aria-label="`${label(main)} controls`">
        <button
          v-for="x in controls"
          :key="x.key"
          type="button"
          class="cam-sw"
          :aria-pressed="x.on"
          :disabled="!x.offer.available || busy || live.stale.value || ui.state.editing"
          :title="x.offer.available ? x.offer.label : x.offer.unavailable"
          @click="toggle(x)"
        >
          <span class="cam-dot" aria-hidden="true"></span>{{ x.label }}
        </button>
        <span v-if="controlsWhy" class="cam-why">{{ controlsWhy }}</span>
      </div>
      <ul v-if="showEvents" class="list cam-events" aria-label="Recent detections">
        <li v-for="e in events.slice(0, size === 'hero' ? 4 : 2)" :key="e.id" class="li">
          <span class="cam-thumb" aria-hidden="true"><CameraScene :variant="variant(e.cam, 0)" :person="attr(e.cam, 'last_event.label') === 'person'" /></span>
          <span class="t">{{ e.text }}</span>
          <span></span>
        </li>
      </ul>
      <StateNote v-if="size !== 'small' && s.status !== 'ok'" :subject="s" />
      <div class="tf">
        <span v-if="detections === 0">No detections today</span>
        <span v-else>{{ plural(cams.length, 'camera') }} · <span class="n">{{ s.needsRepair ? '—' : fmtInt(detections) }}</span> detections today</span>
        <span v-if="size !== 'small' && attr(frigate, 'version')" class="n">v{{ attr(frigate, 'version') }}</span>
      </div>
    </template>
  </Frame>
</template>

<style>
/* A real picture: the overlays sit on a dark scrim with light text, whatever the theme, since a photo isn't themed. */
.cam.pic .cam-ts,
.cam.pic .cam-rec,
.cam.pic .cam-cap {
  color: oklch(0.98 0 0);
  text-shadow: 0 1px 2px oklch(0 0 0 / 0.6);
}
.cam.pic::before {
  content: '';
  position: absolute;
  inset: 0;
  z-index: 1;
  pointer-events: none;
  background: linear-gradient(to bottom, oklch(0 0 0 / 0.45), transparent 22%, transparent 70%, oklch(0 0 0 / 0.6));
}
button.cam {
  padding: 0;
  border: 0;
  font: inherit;
  text-align: left;
  cursor: pointer;
}
button.cam:focus-visible {
  outline: 2px solid var(--focus);
  outline-offset: 2px;
}
.cam-ctl {
  flex: none;
  display: flex;
  flex-wrap: wrap;
  align-items: center;
  gap: var(--space-1);
}
.cam-sw {
  display: inline-flex;
  align-items: center;
  gap: 6px;
  min-height: 28px;
  padding: 2px 10px;
  border: var(--hairline) solid var(--line-2);
  border-radius: 999px;
  background: var(--surface);
  color: var(--text-2);
  font-size: var(--type-caption);
}
.cam-sw[aria-pressed='true'] {
  color: var(--text);
}
.cam-sw .cam-dot {
  width: 7px;
  height: 7px;
  border-radius: 50%;
  box-shadow: inset 0 0 0 1.4px currentColor;
}
.cam-sw[aria-pressed='true'] .cam-dot {
  background: currentColor;
  box-shadow: none;
}
.cam-sw:disabled {
  opacity: 0.6;
  cursor: default;
}
.cam-why {
  color: var(--text-3);
  font-size: var(--type-caption);
}
</style>
