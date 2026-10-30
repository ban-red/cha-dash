<script setup lang="ts">
/**
 * A labelled form control: label, the control, then help or an error. The
 * default slot gets `control`, the attributes to bind on the input, select
 * or textarea (`id`, `aria-describedby`, `aria-invalid`), so the label, the
 * help and the error are always tied to it:
 *
 *   <UiField label="Name" help="What will use it." :error="nameError" v-slot="{ control }">
 *     <input v-bind="control" v-model="name" />
 *   </UiField>
 *
 * An error replaces the help while it shows; `invalid` keeps the help and
 * turns it the error colour (a rule the value doesn't meet yet). `#label` and `#help` take rich
 * content (a code sample, a link). `optional` puts the word at the end of the
 * label's row, and `#aside` puts something beside the label (a "set" chip).
 * Pass `id` when something else needs the control's id (a scroll target).
 * `hideLabel` keeps the label for screen readers only (a filter box whose
 * placeholder says what it is). `alert` announces the error when it appears (role="alert"), for errors
 * that come back from a submit rather than as you type.
 * `check` is for a checkbox or radio: the control sits inside the label,
 * before its words.
 */
import { computed, useId, useSlots } from 'vue';

const { label, help, error, invalid = false, optional = false, check = false, hideLabel = false, alert = false, id } = defineProps<{
  label?: string;
  help?: string | undefined;
  /** Shown instead of the help, in the down colour, and marks the control invalid. */
  error?: string | false | null | undefined;
  invalid?: boolean;
  optional?: boolean;
  check?: boolean;
  hideLabel?: boolean;
  alert?: boolean;
  id?: string | undefined;
}>();

const slots = useSlots();
const uid = useId();
const controlId = computed(() => id ?? `${uid}-c`);
const helpId = computed(() => `${controlId.value}-help`);
const hasMessage = computed(() => !!error || !!help || !!slots.help);

const control = computed(() => ({
  id: controlId.value,
  'aria-describedby': hasMessage.value ? helpId.value : undefined,
  'aria-invalid': error || invalid ? (true as const) : undefined,
}));
</script>

<template>
  <div :class="['field', { check }]">
    <label v-if="check" :for="controlId">
      <slot :control="control" />
      <slot name="label">{{ label }}</slot>
    </label>
    <template v-else>
      <div v-if="optional || $slots.aside" class="label-row">
        <label :for="controlId"><slot name="label">{{ label }}</slot></label>
        <slot name="aside" />
        <span v-if="optional" class="opt-word">optional</span>
      </div>
      <label v-else :for="controlId" :class="{ sr: hideLabel }"><slot name="label">{{ label }}</slot></label>
      <slot :control="control" />
    </template>
    <span v-if="hasMessage" :id="helpId" :class="['help', { bad: !!error || invalid }]" :role="alert && error ? 'alert' : undefined">
      <template v-if="error">{{ error }}</template>
      <slot v-else name="help">{{ help }}</slot>
    </span>
  </div>
</template>
