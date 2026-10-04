<script setup lang="ts">
/**
 * Cameras: one large frame and two small, captioned with the last detection.
 * Placeholder visuals only in M0 (no images); the hero size adds the events list.
 */
import { computed } from 'vue';
import type { SizeId } from '@chadash/layout';
import Tile from '../components/Tile.vue';
import CameraScene from './CameraScene.vue';
import { fmtInt, hhmm } from '../lib/format';
import { attr, byKind, label, placeOf, resolve, value } from '../model/graph';
import { tileStatus, worst } from '../model/status';
import { normalizeHealth, resourceLinks } from '../model/types';
import { useLive } from '../stores/live';

const { size } = defineProps<{ size: SizeId; target?: string | undefined }>();
const live = useLive();

const ORDER = ['driveway', 'side_deck', 'backyard'];
const cams = computed(() =>
  byKind(live.graph.value, 'camera').sort((a, b) => {
    const ia = ORDER.findIndex((k) => a.id.endsWith(k));
    const ib = ORDER.findIndex((k) => b.id.endsWith(k));
    return (ia < 0 ? 99 : ia) - (ib < 0 ? 99 : ib) || label(a).localeCompare(label(b));
  }),
);
const frigate = computed(() => resolve(live.graph.value, 'app:frigate'));
const shown = computed(() => cams.value.slice(0, 3));
const detections = computed(() => cams.value.reduce((n, c) => n + (value(live.metrics.value, c.id, 'detections.24h') ?? 0), 0));
const status = computed(() => worst(cams.value.map((c) => tileStatus(normalizeHealth(c), c))));
const online = computed(() => cams.value.filter((c) => c.state === 'online' || c.state === 'running').length);

function caption(c: (typeof cams.value)[number]): string {
  const lbl = attr<string>(c, 'last_event.label');
  const at = attr<string>(c, 'last_event.at');
  const score = attr<number>(c, 'last_event.score');
  if (!lbl || !at) return `${label(c)} · quiet`;
  return `${label(c)} · ${lbl}${score ? ` ${Math.round(score * 100)}%` : ''} · ${hhmm(at)}`;
}
const recent = (c: (typeof cams.value)[number]) => {
  const at = attr<string>(c, 'last_event.at');
  return Boolean(at && live.now.value - Date.parse(at) < 6 * 3_600_000 && attr(c, 'last_event.label') === 'person');
};
const events = computed(() =>
  cams.value
    .filter((c) => attr(c, 'last_event.at'))
    .map((c) => ({ id: c.id, text: caption(c), at: attr<string>(c, 'last_event.at')! }))
    .sort((a, b) => Date.parse(b.at) - Date.parse(a.at)),
);
const layout = computed(() => (size === 'wide' ? 'row3' : size === 'hero' ? 'hero' : ''));
</script>

<template>
  <Tile
    :size="size"
    name="Cameras"
    mono="FR"
    :context="frigate ? `Frigate on ${placeOf(live.graph.value, frigate.id) ?? 'the LAN'}` : ''"
    :href="resourceLinks(frigate)[0]?.url"
    :status="status"
    :tag="cams.length ? `${online} recording` : ''"
    :tag-glyph="cams.length ? (status === 'ok' ? 'ok' : status === 'down' ? 'down' : 'attn') : undefined"
    :loading="live.loading.value"
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
          :class="['cam', { big: i === 0, off: c.state !== 'online' && c.state !== 'running' }]"
          role="img"
          :aria-label="caption(c)"
        >
          <CameraScene :variant="ORDER.findIndex((k) => c.id.endsWith(k)) >= 0 ? ORDER.findIndex((k) => c.id.endsWith(k)) : i" :person="recent(c)" />
          <span class="cam-ts">{{ label(c).toUpperCase() }} {{ i === 0 ? hhmm(live.now.value) : '' }}</span>
          <span v-if="i === 0" class="cam-rec">{{ live.stale.value ? 'LAST FRAME' : attr(c, 'record') ? 'REC' : '' }}</span>
          <span class="cam-cap">{{ caption(c) }}</span>
        </div>
      </div>
      <ul v-if="size === 'hero'" class="list" aria-label="Recent detections">
        <li v-for="e in events.slice(0, 3)" :key="e.id" class="li"><span></span><span class="t">{{ e.text }}</span><span></span></li>
      </ul>
      <div class="tf">
        <span>{{ cams.length }} cameras · <span class="n">{{ fmtInt(detections) }}</span> detections today</span>
        <span v-if="attr(frigate, 'version')" class="n">v{{ attr(frigate, 'version') }}</span>
      </div>
    </template>
  </Tile>
</template>
