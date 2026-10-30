<script setup lang="ts">
/**
 * The Peek banner: while a moment is pinned on the Pulse strip, the board
 * shows the past, and this says so: tiles' health and numbers as they
 * were (stores/live.ts → metrics), while lists and text stay live. A frame
 * in the accent colour runs round the window and a pill at the top names
 * the moment, with Back to now. Esc or a click anywhere but the strip or
 * the pill goes back too.
 */
import { computed, onBeforeUnmount, watch } from 'vue';
import { hhmm } from '../lib/format';
import { pulseSlotAt } from '../model/history';
import { clearCursor, cursor, pulse } from '../stores/cursor';
import UiButton from '../ui/Button.vue';

const peek = computed(() => {
  const c = cursor.value;
  const p = pulse.value;
  if (c?.source !== 'pulse' || !p) return undefined;
  const i = pulseSlotAt(p, c.at);
  if (i === undefined) return undefined;
  const from = p.start + i * p.step;
  const min = Math.max(0, Math.round((Date.now() - c.at) / 60_000));
  const ago = min < 90 ? `${min} min ago` : `${Math.round(min / 60)} h ago`;
  return { when: `${hhmm(from)}–${hhmm(from + p.step)}`, ago };
});

const back = () => clearCursor('pulse');
function key(e: KeyboardEvent): void {
  if (e.key !== 'Escape' || e.defaultPrevented || document.querySelector('dialog[open]')) return;
  back();
}
function down(e: PointerEvent): void {
  const t = e.target as Element | null;
  if (t?.closest?.('.pulse-bar, .peek')) return;
  back();
}
function off(): void {
  document.removeEventListener('keydown', key);
  document.removeEventListener('pointerdown', down, true);
}
watch(
  () => peek.value !== undefined,
  (on) => {
    off();
    if (!on) return;
    document.addEventListener('keydown', key);
    document.addEventListener('pointerdown', down, true);
  },
  { immediate: true },
);
onBeforeUnmount(off);
</script>

<template>
  <div class="peek-live sr" role="status" aria-live="polite">{{ peek ? `Showing the board at ${peek.when}` : '' }}</div>
  <template v-if="peek">
    <div class="peek-frame" aria-hidden="true"></div>
    <div class="peek glass">
      <svg class="peek-icon" viewBox="0 0 16 16" aria-hidden="true">
        <path d="M2.5 8a5.5 5.5 0 1 0 1.6-3.9" />
        <path d="M2 2.5v2.6h2.6" />
        <path d="M8 5v3.2l2 1.3" />
      </svg>
      <span class="peek-t"
        ><b>Peeking at {{ peek.when }}</b> <span class="peek-ago">{{ peek.ago }} · health and numbers as they were, lists and text live</span></span
      >
      <UiButton size="sm" variant="primary" @click="back">Back to now</UiButton>
    </div>
  </template>
</template>

<style>
.peek-frame {
  position: fixed;
  z-index: 55;
  inset: 0;
  border: 3px solid var(--accent);
  pointer-events: none;
}
.peek {
  position: fixed;
  z-index: 56;
  top: calc(12px + env(safe-area-inset-top));
  left: 50%;
  display: flex;
  align-items: center;
  gap: var(--space-2);
  max-width: calc(100vw - 32px);
  padding: 6px 6px 6px 12px;
  border: var(--hairline) solid var(--accent-line, var(--accent));
  border-radius: var(--radius-full);
  box-shadow: var(--shadow-overlay);
  color: var(--text);
  font: var(--weight-regular) var(--type-caption) / 1.3 var(--font-ui);
  transform: translateX(-50%);
}
.peek-icon {
  flex: none;
  width: 16px;
  height: 16px;
  fill: none;
  stroke: var(--accent-text, var(--accent));
  stroke-width: 1.6;
  stroke-linecap: round;
  stroke-linejoin: round;
}
.peek-t {
  min-width: 0;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}
.peek-t b {
  font-weight: var(--weight-semibold);
  font-variant-numeric: tabular-nums;
}
.peek-ago {
  color: var(--text-2);
}
@media (max-width: 560px) {
  .peek-ago {
    display: none;
  }
}
@media (prefers-reduced-motion: no-preference) {
  .peek,
  .peek-frame {
    animation: peek-in var(--motion-fade-duration, 0.18s) var(--motion-fade-easing, ease-out) both;
  }
}
@keyframes peek-in {
  from {
    opacity: 0;
  }
}
</style>
