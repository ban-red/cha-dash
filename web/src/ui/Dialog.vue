<script setup lang="ts">
/**
 * A native modal <dialog>: opened on mount (focus moves in, the page behind
 * is inert), closed on unmount. Esc and the × emit `close`; the parent
 * decides what closing means and unmounts it. No motion library, so the
 * lazy chunks that use it stay small.
 *
 * Header: `#lead` (a glyph), the title (`title` or `#title`), `#head`
 * (a chip, a picker), then the ×. The default slot is the body; `#foot`
 * holds the buttons, right-aligned. `as="form"` makes the body a form that
 * emits `submit`; `requestSubmit()` submits it from a shortcut.
 *
 * Class, `aria-describedby` and listeners fall through to the <dialog>.
 * Shell styles are in dialog.css, which loads with the first dialog chunk.
 */
import { useId, useTemplateRef } from 'vue';
import CloseButton from './CloseButton.vue';
import { useModal } from './useModal';
import './dialog.css';

export type DialogSize = 'sm' | 'md' | 'lg';

const {
  title,
  size = 'md',
  as = 'div',
  closeLabel = 'Close',
} = defineProps<{
  title?: string | undefined;
  /** 460, 520 or 720 px wide, less 32 px on a phone. */
  size?: DialogSize;
  as?: 'div' | 'form';
  closeLabel?: string;
}>();

const emit = defineEmits<{ close: []; submit: [] }>();

const uid = useId();
const dlg = useTemplateRef<HTMLDialogElement>('dlg');
const form = useTemplateRef<HTMLFormElement>('form');
useModal(dlg);

defineExpose({
  requestSubmit: () => form.value?.requestSubmit?.(),
  get el() {
    return dlg.value;
  },
});
</script>

<template>
  <dialog ref="dlg" :class="['dlg', `dlg-${size}`]" :aria-labelledby="`${uid}-h`" @cancel.prevent="emit('close')">
    <component :is="as" :ref="as === 'form' ? 'form' : undefined" class="dlg-body" :novalidate="as === 'form' || undefined" @submit.prevent="emit('submit')">
      <header class="dlg-head">
        <slot name="lead" />
        <h2 :id="`${uid}-h`"><slot name="title">{{ title }}</slot></h2>
        <slot name="head" />
        <CloseButton :label="closeLabel" @click="emit('close')" />
      </header>
      <slot />
      <div v-if="$slots.foot" class="dlg-foot"><slot name="foot" /></div>
    </component>
  </dialog>
</template>
