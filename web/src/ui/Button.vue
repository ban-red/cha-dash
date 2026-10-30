<script setup lang="ts">
/**
 * The button: one look per variant, in two sizes. With `href` it renders a
 * link styled as a button. `type` defaults to "button", so a button inside a
 * form never submits it by accident. Attributes and listeners fall through to
 * the element (`disabled`, `aria-busy`, `@click`). `focus()` and `el` are
 * exposed for callers that move focus to it.
 *
 * The DOM keeps the `.btn` classes (`.btn.primary`, `.btn.sm`, …): tests and
 * layout CSS target them. The rules are in ui.css.
 */
import { computed, useTemplateRef } from 'vue';

export type ButtonVariant = 'default' | 'primary' | 'ghost' | 'danger' | 'danger-solid';
export type ButtonSize = 'md' | 'sm';

const {
  variant = 'default',
  size = 'md',
  type = 'button',
  href,
} = defineProps<{
  variant?: ButtonVariant;
  size?: ButtonSize;
  type?: 'button' | 'submit' | 'reset';
  href?: string | undefined;
}>();

const VARIANT: Record<ButtonVariant, string[]> = {
  default: [],
  primary: ['primary'],
  ghost: ['ghost'],
  danger: ['danger'],
  'danger-solid': ['danger', 'solid'],
};

const cls = computed(() => ['btn', ...VARIANT[variant], ...(size === 'sm' ? ['sm'] : [])]);
const el = useTemplateRef<HTMLButtonElement | HTMLAnchorElement>('el');

defineExpose({
  focus: (opts?: FocusOptions) => el.value?.focus(opts),
  get el() {
    return el.value;
  },
});
</script>

<template>
  <a v-if="href !== undefined" ref="el" :class="cls" :href="href"><slot /></a>
  <button v-else ref="el" :class="cls" :type="type"><slot /></button>
</template>
