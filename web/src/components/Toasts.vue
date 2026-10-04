<script setup lang="ts">
/**
 * Toasts, politely announced. A toast can carry one button: Undo after a T1
 * action (for its 10 s), or "Open its settings" when an action is
 * unavailable until a credential is added.
 */
import Glyph from './marks/Glyph.vue';
import { useUi, type Toast } from '../stores/ui';

const ui = useUi();

function run(t: Toast): void {
  ui.dismiss(t.id);
  t.action?.run();
}
</script>

<template>
  <div class="toasts" role="status" aria-live="polite">
  <TransitionGroup name="toast">
    <div v-for="t in ui.state.toasts" :key="t.id" class="toast glass">
      <Glyph :kind="t.tone === 'error' ? 'attn' : 'check'" />
      <span>{{ t.text }}</span>
      <button v-if="t.action" type="button" class="btn sm toast-act" @click="run(t)">{{ t.action.label }}</button>
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
.toast .toast-act {
  flex: none;
  margin-left: var(--space-2);
}
</style>
