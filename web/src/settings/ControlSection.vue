<script setup lang="ts">
/**
 * "Let Cha Dash act": the part of an integration's form that holds its
 * control credential (model/control.ts), folded away from the credential it
 * reads with. It says, in words, whether the instance can act now, and what
 * acting means here (a plan, a confirmation by risk, an audit record).
 *
 * - A control group (Proxmox: a second token) shows its fields, all or
 *   nothing.
 * - A mode (Unraid: read-only or control) shows the choice with what each
 *   one means, updated as you pick.
 *
 * The recipe's control variant sits beside the form (IntegrationEditor). An
 * action refused as unavailable links here: `?section=control` opens the
 * section and scrolls to it.
 */
import { computed, useId } from 'vue';
import Glyph from '../components/marks/Glyph.vue';
import SchemaField from './SchemaField.vue';
import type { JsonSchema } from '../lib/schema';
import { actWords, modeConsequence, modeOn, type ActStatus, type ControlSpec } from '../model/control';

const {
  control,
  schema,
  keys,
  values,
  saved,
  current,
  kind,
  note = '',
} = defineProps<{
  control: ControlSpec;
  schema: JsonSchema;
  keys: string[];
  /** The form's config values (SchemaField writes into them). */
  values: Record<string, unknown>;
  /** What the saved instance can do, or undefined while adding. */
  saved: ActStatus | undefined;
  /** What it could do once these settings are saved. */
  current: ActStatus;
  kind: string;
  /** The hub's control_available hint, when it gave one. */
  note?: string;
}>();
const open = defineModel<boolean>('open', { default: false });

const uid = useId();
const shown = computed(() => saved ?? current);
const changes = computed(() => saved !== undefined && saved.state !== current.state);
const on = computed(() => (control.mode ? modeOn(control, values) : current.state === 'acts'));

function onToggle(e: Event): void {
  open.value = (e.target as HTMLDetailsElement).open;
}
</script>

<template>
  <section id="control" class="ctl" :aria-labelledby="`${uid}-h`">
    <details :open="open" @toggle="onToggle">
      <summary>
        <span :id="`${uid}-h`" class="ctl-title">Let Cha Dash act</span>
        <span :class="['ctl-state', `is-${shown.state}`]">
          <Glyph :kind="shown.state === 'acts' ? 'ok' : shown.state === 'partial' ? 'attn' : 'lock'" />{{ shown.state === 'acts' ? 'Can act' : shown.state === 'partial' ? 'Incomplete' : 'Reads only' }}
        </span>
      </summary>
      <div class="ctl-body">
        <p class="help">
          <template v-if="control.mode">One credential reads and, if you allow it, acts.</template>
          <template v-else>A second credential, used only when Cha Dash acts; the one above keeps reading. Leave these empty and Cha Dash only reads.</template>
          Every action shows a plan first, asks you to confirm by its risk (undo, hold, or type its name), and is written to the audit log.
        </p>
        <p class="ctl-now" role="status">
          <b>{{ actWords(shown, kind) }}</b>
          <template v-if="changes"> After you save: {{ actWords(current, kind).toLowerCase() }}</template>
        </p>
        <p v-if="note && shown.state !== 'acts'" class="ctl-note"><Glyph kind="link" />{{ note }}</p>

        <SchemaField
          v-for="key in keys"
          :key="key"
          :schema="schema.properties![key]!"
          :container="values"
          :k="key"
          :path="key"
          :required="false"
        />
        <p v-if="control.mode" class="ctl-means" aria-live="polite">{{ modeConsequence(on, kind) }}</p>
        <p v-if="current.state === 'partial'" class="help bad">Still empty: {{ current.missing.join(', ') }}.</p>
      </div>
    </details>
  </section>
</template>

<style>
.ctl details {
  border: var(--hairline) solid var(--line);
  border-radius: var(--radius-md);
  background: var(--surface);
}
.ctl summary {
  display: flex;
  align-items: center;
  gap: var(--space-3);
  padding: var(--space-3) var(--space-4);
  cursor: pointer;
  list-style: none;
}
.ctl summary::-webkit-details-marker {
  display: none;
}
.ctl summary::before {
  content: '›';
  color: var(--text-2);
  transition: transform var(--motion-press-duration) var(--motion-press-easing);
}
.ctl details[open] > summary::before {
  transform: rotate(90deg);
}
.ctl-title {
  font-weight: var(--weight-medium);
  color: var(--text);
}
.ctl-state {
  margin-left: auto;
  display: inline-flex;
  align-items: center;
  gap: var(--space-1);
  font-size: var(--type-small);
  color: var(--text-2);
}
.ctl-state.is-acts {
  color: var(--text);
}
.ctl-body {
  display: flex;
  flex-direction: column;
  gap: var(--space-3);
  padding: 0 var(--space-4) var(--space-4);
}
.ctl-now,
.ctl-means {
  color: var(--text);
}
.ctl-note {
  display: flex;
  gap: var(--space-2);
  color: var(--text-2);
}
</style>
