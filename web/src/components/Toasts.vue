<script setup lang="ts">
import Glyph from './marks/Glyph.vue';
import { useUi } from '../stores/ui';

const ui = useUi();
</script>

<template>
  <div class="toasts" role="status" aria-live="polite">
  <TransitionGroup name="toast">
    <div v-for="t in ui.state.toasts" :key="t.id" class="toast glass">
      <Glyph :kind="t.tone === 'error' ? 'attn' : 'check'" />
      <span>{{ t.text }}</span>
      <button type="button" class="x" aria-label="Dismiss" @click="ui.dismiss(t.id)">
        <svg viewBox="0 0 10 10" aria-hidden="true"><path d="m2.5 2.5 5 5m0-5-5 5" fill="none" stroke="currentColor" stroke-width="1.5" stroke-linecap="round" /></svg>
      </button>
    </div>
  </TransitionGroup>
  </div>
</template>

<style>
.toast-enter-active,
.toast-leave-active,
.toast-move {
  transition:
    opacity var(--motion-fade-duration) var(--motion-fade-easing),
    transform var(--motion-lift-duration) var(--motion-lift-easing);
}
.toast-enter-from,
.toast-leave-to {
  opacity: 0;
  transform: translateY(12px);
}
</style>
