<script setup lang="ts">
/**
 * Camera Hero (Frigate). No footage: every frame is a placeholder drawn from
 * neutral tokens (CameraScene), captioned with the camera's last detection.
 * Target: a Frigate instance (`cameras@frigate:<instance id>/app`, its
 * cameras are what it `exposes`), one camera, or with none, every camera.
 *
 * Small: the latest detection with its caption. Wide: the cameras side by
 * side. Large: one big frame and two small. Hero: three frames and the recent
 * detections.
 *
 * A camera offline while others record is attention; Frigate down keeps the
 * last frames, hatched, "last frame 14:02". A camera turned off on purpose
 * (privacy) is quiet and dimmed. While the integration needs repair, events
 * are "—".
 */
import { computed } from 'vue';
import type { SizeId } from '@chadash/layout';
import Glyph from '../components/marks/Glyph.vue';
import type { TileStatus } from '../components/Tile.vue';
import CameraScene from './CameraScene.vue';
import { fmtInt, hhmm, plural } from '../lib/format';
import { attr, byKind, incoming, label, outgoing, placeOf, resolve, value } from '../model/graph';
import { glyphFor } from '../model/status';
import { normalizeHealth, resourceLinks, type Resource } from '../model/types';
import { useLive } from '../stores/live';
import Frame from './parts/Frame.vue';
import StateNote from './parts/StateNote.vue';
import { statusOf } from './subject';
import { useSubject } from './use';

const { size, target } = defineProps<{ size: SizeId; target?: string | undefined }>();
const live = useLive();

// The Demo Home's cameras, each with its own scene (CameraScene: drive, deck, garden; the garage reuses the drive).
const ORDER = ['driveway', 'front_door', 'garden', 'garage'];
const picked = computed(() => (target ? resolve(live.graph.value, target) : undefined));
const frigate = computed(() => {
  const p = picked.value;
  if (p?.kind === 'app') return p;
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
  if (!cams.value.length) return '';
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
const layout = computed(() => (size === 'small' ? 'one' : size === 'wide' ? 'two' : size === 'hero' ? 'hero' : ''));
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
    <template v-if="!cams.length">
      <div class="v sm word">No cameras</div>
      <p class="lab">Connect Frigate to see your cameras here.</p>
    </template>
    <template v-else>
      <div :class="['cams', layout]">
        <div
          v-for="(c, i) in shown"
          :key="c.id"
          :class="['cam', { big: i === 0, off: camStatus(c) !== 'ok' && camStatus(c) !== 'attention', frozen }]"
          role="img"
          :aria-label="caption(c) + (frozen ? `, ${stamp}` : '')"
        >
          <CameraScene :variant="variant(c, i)" :person="recent(c)" />
          <span class="cam-ts">{{ label(c).toUpperCase() }} {{ i === 0 && !frozen ? hhmm(live.now.value) : '' }}</span>
          <span v-if="i === 0" class="cam-rec">{{ frozen ? 'LAST FRAME' : attr(c, 'record') ? 'REC' : '' }}</span>
          <span class="cam-cap">
            <Glyph v-if="camStatus(c) !== 'ok'" :kind="glyphFor(camStatus(c))" />{{ caption(c) }}
          </span>
        </div>
      </div>
      <ul v-if="size === 'hero' || size === 'large'" class="list cam-events" aria-label="Recent detections">
        <li v-for="e in events.slice(0, size === 'hero' ? 4 : 2)" :key="e.id" class="li">
          <span class="cam-thumb" aria-hidden="true"><CameraScene :variant="variant(e.cam, 0)" :person="attr(e.cam, 'last_event.label') === 'person'" /></span>
          <span class="t">{{ e.text }}</span>
          <span></span>
        </li>
        <li v-if="!events.length" class="li"><span></span><span class="t">No detections today</span><span></span></li>
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
