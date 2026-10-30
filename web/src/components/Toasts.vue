<script setup lang="ts">
/**
 * Toasts, politely announced. A toast can carry one button: Undo after a T1
 * action (for its 10 s), or "Open its settings" when an action is
 * unavailable until a credential is added.
 */
import Glyph from '../ui/marks/Glyph.vue';
import { useUi, type Toast } from '../stores/ui';
import UiButton from '../ui/Button.vue';
import UiCloseButton from '../ui/CloseButton.vue';

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
      <UiButton v-if="t.action" size="sm" class="toast-act" @click="run(t)">{{ t.action.label }}</UiButton>
      <UiCloseButton label="Dismiss" @click="ui.dismiss(t.id)" />
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
