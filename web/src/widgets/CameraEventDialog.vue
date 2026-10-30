<script setup lang="ts">
/**
 * One camera detection on its own, opened from the Cameras tile's detections
 * list (`ui.state.cameraEvent`, a camera's resource id): what Frigate saw,
 * how sure it was, where and when. The footer links to the event in Frigate
 * itself when the hub has its page (`last_event.url`, built on the
 * connection's URL). It is the camera's newest event, or the one picked from
 * the camera's sheet (`ui.state.cameraEvent.event`). With the connection sharing pictures it opens on the
 * event's snapshot, fetched through the hub like a camera's frame (the
 * picture route's `event`), and without one when Frigate has none (snapshots
 * off for the camera, or already aged out). It shows the camera's newest detection as the graph has
 * it now, so a newer one replaces it while the dialog is open. A UiDialog;
 * its own chunk, loaded the first time one is opened.
 */
import { computed, ref, watch } from 'vue';
import { ago, hhmm, shortDate } from '../lib/format';
import { cameraEvents } from '../model/cameraEvents';
import { attr, label, resolve } from '../model/graph';
import { safeUrl } from '../model/types';
import { useLive } from '../stores/live';
import { useUi } from '../stores/ui';
import UiButton from '../ui/Button.vue';
import UiDialog from '../ui/Dialog.vue';

const ui = useUi();
const live = useLive();

const cam = computed(() => (ui.state.cameraEvent ? resolve(live.graph.value, ui.state.cameraEvent.camera) : undefined));
/** The event asked for, else the camera's newest; gone once it is no longer among the camera's newest. */
const ev = computed(() => {
  const list = cameraEvents(cam.value);
  const want = ui.state.cameraEvent?.event;
  return want ? list.find((e) => e.id === want) : list[0];
});
const what = computed(() => ev.value?.label ?? '');
const at = computed(() => ev.value?.at);
const score = computed(() => ev.value?.score);
const href = computed(() => safeUrl(ev.value?.url));
/** The snapshot: only where the connection shares pictures, for this very event. */
const snapshot = computed(() => {
  const c = cam.value;
  const id = ev.value?.id;
  const src = c?.source ?? '';
  if (!c || !id || attr(c, 'picture') !== true || !src.startsWith('integration:')) return undefined;
  const q = new URLSearchParams({ resource: c.id, event: id, h: '540' });
  return `/api/integrations/instances/${encodeURIComponent(src.slice('integration:'.length))}/picture?${q.toString()}`;
});
const failed = ref('');
watch(snapshot, () => (failed.value = ''));
const shown = computed(() => (snapshot.value && snapshot.value !== failed.value ? snapshot.value : undefined));

const title = computed(() => (what.value ? `${what.value[0]!.toUpperCase()}${what.value.slice(1)} detected` : 'Detection'));

const facts = computed(() => [
  { term: 'Camera', value: cam.value ? label(cam.value) : '—' },
  { term: 'Seen', value: what.value || '—' },
  { term: 'Confidence', value: score.value ? `${Math.round(score.value * 100)}%` : '—' },
  { term: 'When', value: at.value ? `${shortDate(at.value)}, ${hhmm(at.value)} · ${ago(at.value, live.now.value)}` : '—' },
]);

function close(): void {
  ui.closeCameraEvent();
}
</script>

<template>
  <UiDialog class="cam-ev-dlg" size="md" :title="title" @close="close">
    <p v-if="!at" class="cam-ev-none">This detection isn’t among the camera’s newest any more.</p>
    <template v-else>
      <figure v-if="shown" class="cam-ev-pic">
        <img :src="shown" :alt="`${what || 'Detection'} on ${cam ? label(cam) : 'the camera'}`" @error="failed = snapshot ?? ''" />
      </figure>
      <dl class="cam-ev-facts">
        <template v-for="f in facts" :key="f.term">
          <dt>{{ f.term }}</dt>
          <dd>{{ f.value }}</dd>
        </template>
      </dl>
    </template>
    <template #foot>
      <UiButton v-if="href" :href="href" target="_blank" rel="noopener noreferrer">Open in Frigate<span aria-hidden="true"> ↗</span></UiButton>
      <UiButton variant="primary" @click="close">Close</UiButton>
    </template>
  </UiDialog>
</template>

<style>
.cam-ev-pic {
  margin: 0 0 var(--space-4);
  border-radius: var(--radius-md);
  overflow: hidden;
  background: var(--neutral-2);
}
.cam-ev-pic img {
  display: block;
  width: 100%;
  height: auto;
  max-height: min(50dvh, 22rem);
  object-fit: contain;
}
.cam-ev-facts {
  display: grid;
  grid-template-columns: max-content 1fr;
  gap: var(--space-2) var(--space-4);
  margin: 0;
  font-size: var(--type-body);
}
.cam-ev-facts dt {
  color: var(--text-2);
}
.cam-ev-facts dd {
  margin: 0;
  color: var(--text);
  overflow-wrap: anywhere;
}
.cam-ev-none {
  margin: 0;
  color: var(--text-2);
}
</style>
