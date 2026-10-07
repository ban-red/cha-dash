<script setup lang="ts">
/**
 * Hold-to-confirm (T2): press and hold for ~650 ms. The ring fills while you
 * hold and the blast-radius list lights up with it (`progress`); letting go
 * early springs the ring back and nothing happens. Space or Enter hold too.
 *
 * A click that no pointer press or held key came before (a screen reader in
 * browse mode, voice control, switch access) can't hold anything, so it arms
 * a plain two-step instead: the button becomes "Confirm …" and says so in a
 * live hint, and the next activation confirms.
 */
import { computed, onBeforeUnmount, ref } from 'vue';

defineOptions({ inheritAttrs: false });
import { prefersReducedMotion, SPRING } from '../lib/motion';

const { label, duration = 650, hint = 'hold-hint' } = defineProps<{ label: string; duration?: number; /** The id of the hint beside it. */ hint?: string }>();
const emit = defineEmits<{ confirm: []; progress: [p: number] }>();

const C = 2 * Math.PI * 11;
const p = ref(0);
const done = ref(false);
let raf = 0;
let t0 = 0;
let back: { stop: () => void } | undefined;

/**
 * Spring the ring back to empty on the `press` token (0.2 s, no bounce): a
 * critically damped spring, x(t) = x0 · (1 + ωt) · e^(−ωt), interruptible by
 * pressing again.
 */
function springBack(from: number, onUpdate: (v: number) => void): { stop: () => void } {
  const omega = (2 * Math.PI) / SPRING.press.visualDuration;
  const start = performance.now();
  let id = 0;
  const step = (ts: number) => {
    const t = Math.max(0, ts - start) / 1000;
    const v = from * (1 + omega * t) * Math.exp(-omega * t);
    if (v < 0.001) {
      onUpdate(0);
      return;
    }
    onUpdate(v);
    id = requestAnimationFrame(step);
  };
  id = requestAnimationFrame(step);
  return { stop: () => cancelAnimationFrame(id) };
}
let holding = false;
/** A pointer press or a held Space/Enter came before the next click: that click is the hold's, not a request. */
let physical = false;
/** Armed by an assistive click: the next activation confirms. */
const armed = ref(false);
const name = computed(() => (armed.value ? `Confirm: ${label}` : `Hold to ${label}`));

function set(v: number) {
  p.value = v;
  emit('progress', v);
}

function tick(ts: number) {
  if (!holding) return;
  const v = Math.min(1, (ts - t0) / duration);
  set(v);
  if (v >= 1) finish();
  else raf = requestAnimationFrame(tick);
}

function start(e?: Event) {
  if (done.value || holding || armed.value) return;
  physical = true;
  e?.preventDefault();
  back?.stop();
  back = undefined;
  holding = true;
  t0 = performance.now() - p.value * duration;
  raf = requestAnimationFrame(tick);
}

/** Forget the press once its own click (dispatched in the same task) has passed. */
function release() {
  setTimeout(() => (physical = false));
}

function stop() {
  release();
  if (!holding || done.value) return;
  holding = false;
  cancelAnimationFrame(raf);
  if (p.value === 0) return;
  if (prefersReducedMotion()) {
    set(0);
    return;
  }
  back = springBack(p.value, set);
}

function finish() {
  holding = false;
  done.value = true;
  set(1);
  setTimeout(() => emit('confirm'), prefersReducedMotion() ? 150 : 420);
}

function onKeydown(e: KeyboardEvent) {
  if (e.key !== ' ' && e.key !== 'Enter') return;
  if (armed.value) {
    // Armed: Space or Enter is the second step's click, not a hold.
    physical = false;
    return;
  }
  if (!e.repeat) start(e);
}

function onClick() {
  if (done.value) return;
  if (physical) {
    // The hold's own click (pointer up, or the key let go): the ring decides.
    physical = false;
    return;
  }
  if (!armed.value) {
    armed.value = true;
    return;
  }
  armed.value = false;
  finish();
}
function onKeyup(e: KeyboardEvent) {
  if (e.key === ' ' || e.key === 'Enter') stop();
}

onBeforeUnmount(() => {
  cancelAnimationFrame(raf);
  back?.stop();
});

defineExpose({ progress: p, armed });
</script>

<template>
  <button
    type="button"
    v-bind="$attrs"
    :class="['hold', { done, armed }]"
    :aria-label="name"
    :aria-describedby="hint"
    @pointerdown="start"
    @pointerup="stop"
    @pointerleave="stop"
    @pointercancel="stop"
    @keydown="onKeydown"
    @keyup="onKeyup"
    @click="onClick"
    @contextmenu.prevent
  >
    <svg class="ring" viewBox="0 0 28 28" aria-hidden="true">
      <circle class="track" cx="14" cy="14" r="11" />
      <circle v-if="p > 0" class="arc" cx="14" cy="14" r="11" :stroke-dasharray="C" :stroke-dashoffset="C * (1 - p)" />
      <path class="tick" d="m9.5 14.2 3 3 6-6.4" />
    </svg>
    <span>{{ armed ? `Confirm: ${label}` : `Hold to ${label}` }}</span>
  </button>
  <span class="sr" aria-live="polite">{{ armed ? `Press again to ${label}. Cha Dash won’t do it until you do.` : '' }}</span>
</template>
