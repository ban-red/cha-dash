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
 * The cameras are packed into a grid of cells that depends on the size (Wide
 * 2 × 1, Large 2 × 2, Panel 3 × 2, Hero 4 × 2), in order, each taking its
 * size from the tile's settings (1 × 1, 2 wide, 2 tall, 2 × 2) or, on Auto,
 * the first a big frame and the rest one cell; one that doesn't fit is left
 * out and counted ("+2 more"). Small: the latest detection with its caption.
 * Large and Hero add the recent detections. Panel, Large and Hero have a control row for the
 * big camera (detection, recording, the camera, snapshots, alerts) when the
 * viewer may run Frigate's actions.
 *
 * Widget settings (WidgetSpec.options): which cameras, in what order and how
 * big, pictures on or off, how often they refresh, the detections list, the
 * controls.
 *
 * A camera offline while others record is attention; Frigate down keeps the
 * last frames, hatched, "last frame 14:02". A camera turned off on purpose
 * (privacy) is quiet and dimmed. While the integration needs repair, events
 * are "—".
 */
import { computed, ref, watch } from 'vue';
import type { SizeId } from '@chadash/layout';
import Glyph from '../ui/marks/Glyph.vue';
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
  // Picked: in the order picked. Otherwise the Demo Home's order, then by name.
  if (pick.length && p?.kind !== 'camera') return list.filter((r) => pick.includes(r.id)).sort((a, b) => pick.indexOf(a.id) - pick.indexOf(b.id));
  return [...list].sort((a, b) => rank(a) - rank(b) || label(a).localeCompare(label(b)));
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
/** The grid of cells per size, and the Auto size of the first camera. */
const GRID: Partial<Record<SizeId, { cols: number; rows: number; lead: [number, number] }>> = {
  small: { cols: 1, rows: 1, lead: [1, 1] },
  wide: { cols: 2, rows: 1, lead: [1, 1] },
  panel: { cols: 3, rows: 2, lead: [2, 2] },
  large: { cols: 2, rows: 2, lead: [2, 1] },
  hero: { cols: 4, rows: 2, lead: [2, 2] },
};
const grid = computed(() => GRID[size] ?? GRID.large!);
interface Placed {
  cam: Resource;
  col: number;
  row: number;
  w: number;
  h: number;
}
/** Each camera's cells, from the tile's settings or Auto. */
function span(c: Resource, i: number): [number, number] {
  const v = (opts.value['camera_sizes'] as Record<string, string>)[c.id];
  const m = /^(\d)x(\d)$/.exec(v ?? '');
  const [w, h] = m ? [Number(m[1]), Number(m[2])] : i === 0 ? grid.value.lead : [1, 1];
  return [Math.min(w, grid.value.cols), Math.min(h, grid.value.rows)];
}
/** Pack the cameras in order, row by row, each in the first free spot it fits; the rest are left out. */
const placed = computed<Placed[]>(() => {
  // Small shows the camera with the newest detection.
  const list = size === 'small' ? (events.value[0] ? [events.value[0].cam] : cams.value.slice(0, 1)) : cams.value;
  const { cols, rows } = grid.value;
  const used = Array.from({ length: rows }, () => Array<boolean>(cols).fill(false));
  const free = (c: number, r: number, w: number, h: number) => {
    for (let y = r; y < r + h; y++) for (let x = c; x < c + w; x++) if (y >= rows || x >= cols || used[y]![x]) return false;
    return true;
  };
  const out: Placed[] = [];
  list.forEach((cam, i) => {
    const [w, h] = span(cam, i);
    for (let r = 0; r < rows; r++)
      for (let c = 0; c < cols; c++) {
        if (!free(c, r, w, h)) continue;
        for (let y = r; y < r + h; y++) for (let x = c; x < c + w; x++) used[y]![x] = true;
        out.push({ cam, col: c + 1, row: r + 1, w, h });
        return;
      }
  });
  return out;
});
const shown = computed(() => placed.value.map((p) => p.cam));
/** Cameras that didn't fit (not counted on Small, which shows one on purpose). */
const hidden = computed(() => (size === 'small' ? 0 : cams.value.length - placed.value.length));
const showEvents = computed(() => (size === 'hero' || size === 'large') && opts.value['detections'] === true && events.value.length > 0);
const gridStyle = computed(() => ({ gridTemplateColumns: `repeat(${grid.value.cols}, minmax(0, 1fr))`, gridTemplateRows: `repeat(${grid.value.rows}, minmax(0, 1fr))` }));
/** The instance a camera comes from (`integration:<id>`), when it shares pictures and the tile shows them. */
function pictureFrom(c: Resource): string | undefined {
  if (!opts.value['pictures'] || attr(c, 'picture') !== true) return undefined;
  const src = c.source ?? '';
  return src.startsWith('integration:') ? src.slice('integration:'.length) : undefined;
}
const every = computed(() => Number(opts.value['refresh']) || 10);
/** Pixels tall to ask for: a frame bigger than a cell, or on a one-row tile, is bigger. */
const heightFor = (p: Placed) => (p.w * p.h > 1 ? 540 : size === 'small' || size === 'wide' ? 360 : 270);
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
      <div class="cams packed" :style="gridStyle">
        <component
          :is="opens(p.cam) ? 'button' : 'div'"
          v-for="(p, i) in placed"
          :key="p.cam.id"
          :type="opens(p.cam) ? 'button' : undefined"
          :class="['cam', { big: p.w * p.h > 1, off: camStatus(p.cam) !== 'ok' && camStatus(p.cam) !== 'attention', frozen, pic: Boolean(pictureFrom(p.cam)) }]"
          :style="{ gridColumn: `${p.col} / span ${p.w}`, gridRow: `${p.row} / span ${p.h}` }"
          :role="opens(p.cam) ? undefined : 'img'"
          :aria-label="opens(p.cam) ? undefined : caption(p.cam) + (frozen ? `, ${stamp}` : '')"
          @click="open(p.cam)"
        >
          <!-- A button is named by what it shows (WCAG 2.5.3), so the stamp it can't show rides along hidden. -->
          <span v-if="opens(p.cam) && frozen" class="sr">{{ stamp }}</span>
          <template v-for="c in [p.cam]" :key="c.id">
          <CameraPicture v-if="pictureFrom(c)" :instance="pictureFrom(c)!" :resource="c.id" :height="heightFor(p)" :every="every" :frozen="frozen">
            <CameraScene :variant="variant(c, i)" :person="recent(c)" />
          </CameraPicture>
          <CameraScene v-else :variant="variant(c, i)" :person="recent(c)" />
          <span class="cam-ts">{{ label(c).toUpperCase() }} {{ i === 0 && !frozen ? hhmm(live.now.value) : '' }}</span>
          <span v-if="i === 0" class="cam-rec">{{ frozen ? 'LAST FRAME' : attr(c, 'record') ? 'REC' : '' }}<template v-if="!frozen && attr(c, 'detect') === false"> · DETECT OFF</template></span>
          <span class="cam-cap">
            <Glyph v-if="camStatus(c) !== 'ok'" :kind="glyphFor(camStatus(c))" />{{ caption(c) }}
          </span>
          </template>
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
        <span v-else>{{ plural(cams.length, 'camera') }}<template v-if="hidden"> (+{{ hidden }} more)</template> · <span class="n">{{ s.needsRepair ? '—' : fmtInt(detections) }}</span> detections today</span>
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
</style>
