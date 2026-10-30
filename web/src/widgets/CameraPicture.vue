<script setup lang="ts">
/**
 * A camera's current picture, fetched by the hub from the integration that
 * owns the camera (GET /api/integrations/instances/{id}/picture), so the
 * browser never talks to the camera server. It refreshes every `every`
 * seconds, only while it's on screen and the tab is visible. Each new frame
 * is loaded off screen and swapped in, so the picture never blanks; until
 * the first one arrives, and after one fails, the slot (a drawn placeholder)
 * shows instead. After a failure it tries again less often.
 */
import { computed, onBeforeUnmount, onMounted, ref, useTemplateRef, watch } from 'vue';

const { instance, resource, height = 360, every = 10, frozen = false } = defineProps<{
  instance: string;
  resource: string;
  /** Pixels tall, for the hub to ask the source to scale. */
  height?: number;
  /** Seconds between frames. */
  every?: number;
  /** Stop refreshing (the hub is offline): keep the last frame. */
  frozen?: boolean;
}>();
const emit = defineEmits<{ frame: [at: number]; failed: [] }>();

const root = useTemplateRef<HTMLElement>('root');
const src = ref('');
const failed = ref(false);
const onScreen = ref(typeof IntersectionObserver === 'undefined');
const tabVisible = ref(typeof document === 'undefined' || document.visibilityState !== 'hidden');
const base = computed(() => `/api/integrations/instances/${encodeURIComponent(instance)}/picture?resource=${encodeURIComponent(resource)}&h=${Math.round(height)}`);

let timer: ReturnType<typeof setTimeout> | undefined;
let loading: HTMLImageElement | undefined;
let misses = 0;

function schedule(ms: number): void {
  clearTimeout(timer);
  timer = undefined;
  if (frozen || !onScreen.value || !tabVisible.value) return;
  timer = setTimeout(load, ms);
}

function load(): void {
  if (loading || frozen) return;
  const img = new Image();
  loading = img;
  const url = `${base.value}&t=${Date.now()}`;
  img.onload = () => {
    loading = undefined;
    misses = 0;
    failed.value = false;
    src.value = url;
    emit('frame', Date.now());
    schedule(every * 1000);
  };
  img.onerror = () => {
    loading = undefined;
    misses++;
    if (!src.value || misses > 1) {
      failed.value = true;
      emit('failed');
    }
    schedule(Math.min(every * 4, 120) * 1000);
  };
  img.src = url;
}

function onVisibility(): void {
  tabVisible.value = document.visibilityState !== 'hidden';
}

let io: IntersectionObserver | undefined;
onMounted(() => {
  document.addEventListener('visibilitychange', onVisibility);
  if (typeof IntersectionObserver !== 'undefined' && root.value) {
    io = new IntersectionObserver((entries) => {
      onScreen.value = entries.some((e) => e.isIntersecting);
    });
    io.observe(root.value);
  }
  load();
});
onBeforeUnmount(() => {
  document.removeEventListener('visibilitychange', onVisibility);
  io?.disconnect();
  clearTimeout(timer);
  dropPending();
});
// Back on screen, or the tab visible again: a fresh frame now.
watch([onScreen, tabVisible, () => frozen], ([seen, vis, fr]) => {
  if (seen && vis && !fr) load();
  else schedule(0);
});
/** Forget a frame still loading: it's of the camera shown before. */
function dropPending(): void {
  if (loading) loading.onload = loading.onerror = null;
  loading = undefined;
}
// Another camera or size: start over.
watch(base, () => {
  dropPending();
  src.value = '';
  failed.value = false;
  misses = 0;
  clearTimeout(timer);
  load();
});
</script>

<template>
  <span ref="root" class="cam-pic">
    <img v-if="src && !failed" :src="src" alt="" decoding="async" draggable="false" />
    <slot v-else />
  </span>
</template>

<style>
.cam-pic {
  position: absolute;
  inset: 0;
  display: block;
}
.cam-pic img {
  width: 100%;
  height: 100%;
  object-fit: cover;
  display: block;
}
</style>
