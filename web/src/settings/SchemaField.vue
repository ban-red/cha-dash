<script setup lang="ts">
/**
 * One field of a form generated from an integration's JSON Schema, at any
 * depth: text, URL, number, yes/no, a choice, a nested object (a fieldset),
 * a list, or a secret.
 *
 * Secrets (`writeOnly`) are write-only: the input starts empty whatever the
 * hub holds, a "set" chip says one is stored, blank keeps it, and what you
 * type lives in the form's secrets map, never in the config values.
 *
 * Writes go straight into the parent object (`container[k]`), which is the
 * editor's reactive config, so nested fields need no event plumbing.
 */
import { computed, ref } from 'vue';
import Glyph from '../components/marks/Glyph.vue';
import AuthorizeButton from './AuthorizeButton.vue';
import { choices, effective, fieldId, fieldLabel, kindOf, newItem, type JsonSchema } from '../lib/schema';
import { useForm } from './form';

const {
  schema,
  container,
  k,
  path,
  required = false,
} = defineProps<{
  schema: JsonSchema;
  container: Record<string, unknown> | unknown[];
  k: string | number;
  path: string;
  required?: boolean;
}>();

const form = useForm();
const kind = computed(() => kindOf(schema));
const label = computed(() => (typeof k === 'number' ? (schema.title ?? `Item ${k + 1}`) : fieldLabel(k, schema)));
const id = computed(() => fieldId(path));
const value = computed<unknown>(() => (container as Record<string | number, unknown>)[k]);
const error = computed(() => (form.showAll.value || form.touched.has(path) ? form.errors.value[path] : undefined));
const describedBy = computed(() => (error.value ? `${id.value}-err` : schema.description ? `${id.value}-help` : undefined));
const options = computed(() => choices(schema) ?? []);
const placeholder = computed(() => (schema.examples?.length ? String(schema.examples[0]) : ''));
const isSet = computed(() => form.secretsSet.value.has(path));
const reveal = ref(false);
const focused = computed(() => form.focus.value === path);

/** For objects: the properties in force (an `if`/`then` branch may add some). */
const objectSchema = computed(() => effective(schema, value.value));
const objectRequired = computed(() => new Set(objectSchema.value.required ?? []));
const join = (key: string | number) => `${path}.${key}`;

function set(v: unknown): void {
  (container as Record<string | number, unknown>)[k] = v;
}

function onText(e: Event): void {
  set((e.target as HTMLInputElement | HTMLSelectElement).value);
}

function onNumber(e: Event): void {
  const raw = (e.target as HTMLInputElement).value;
  if (raw === '') return set(undefined);
  const n = Number(raw);
  set(Number.isFinite(n) ? n : raw);
}

function onSecret(e: Event): void {
  form.secrets[path] = (e.target as HTMLInputElement).value;
}

function onChoice(e: Event): void {
  const raw = (e.target as HTMLInputElement | HTMLSelectElement).value;
  const hit = options.value.find((o) => String(o.value) === raw);
  set(hit ? hit.value : raw || undefined);
}

function blur(): void {
  form.touched.add(path);
}

// Lists
const list = computed<unknown[]>(() => (Array.isArray(value.value) ? (value.value as unknown[]) : []));
const singular = computed(() => {
  const l = label.value;
  return l.endsWith('ies') ? `${l.slice(0, -3)}y` : l.endsWith('s') ? l.slice(0, -1) : l;
});
const itemIsObject = computed(() => schema.items !== undefined && kindOf(schema.items) === 'object');

function addItem(): void {
  if (!Array.isArray(value.value)) set([]);
  (value.value as unknown[]).push(newItem(schema));
}

function removeItem(i: number): void {
  (value.value as unknown[]).splice(i, 1);
  form.touched.add(path);
}
</script>

<template>
  <!-- A nested object: a fieldset of its own fields -->
  <fieldset v-if="kind === 'object'" class="st-fieldset" :disabled="form.disabled.value">
    <legend>{{ label }}</legend>
    <p v-if="schema.description" class="help">{{ schema.description }}</p>
    <SchemaField
      v-for="(sub, key) in objectSchema.properties"
      :key="key"
      :schema="sub"
      :container="value as Record<string, unknown>"
      :k="key"
      :path="join(key)"
      :required="objectRequired.has(String(key))"
    />
  </fieldset>

  <!-- A list -->
  <div v-else-if="kind === 'array'" :class="['field', { 'st-focus': focused }]" role="group" :aria-labelledby="`${id}-label`">
    <div class="label-row">
      <span :id="`${id}-label`" class="lbl">{{ label }}</span>
      <span class="opt-word">{{ list.length }}</span>
    </div>
    <p v-if="schema.description" :id="`${id}-help`" class="help">{{ schema.description }}</p>
    <div class="st-array">
      <template v-for="(_, i) in list" :key="i">
        <fieldset v-if="itemIsObject" class="st-fieldset st-array-item" :disabled="form.disabled.value">
          <legend>{{ singular }} {{ i + 1 }}</legend>
          <SchemaField
            v-for="(sub, key) in effective(schema.items!, list[i]).properties"
            :key="key"
            :schema="sub"
            :container="list[i] as Record<string, unknown>"
            :k="key"
            :path="`${path}.${i}.${key}`"
            :required="(effective(schema.items!, list[i]).required ?? []).includes(String(key))"
          />
          <button type="button" class="btn sm ghost" :disabled="form.disabled.value" @click="removeItem(i)">Remove {{ singular.toLowerCase() }} {{ i + 1 }}</button>
        </fieldset>
        <div v-else class="secret-row">
          <SchemaField :schema="schema.items ?? { type: 'string' }" :container="list" :k="i" :path="`${path}.${i}`" :required="true" />
          <button type="button" class="btn sm ghost" :aria-label="`Remove ${singular} ${i + 1}`" :disabled="form.disabled.value" @click="removeItem(i)">Remove</button>
        </div>
      </template>
      <button type="button" class="btn sm" :disabled="form.disabled.value || (schema.maxItems !== undefined && list.length >= schema.maxItems)" @click="addItem">
        Add {{ singular.toLowerCase() }}
      </button>
    </div>
    <span v-if="error" :id="`${id}-err`" class="help bad">{{ error }}</span>
  </div>

  <!-- Yes / no -->
  <div v-else-if="kind === 'boolean'" :class="['field', { 'st-focus': focused }]">
    <label class="check-row" :for="id">
      <input :id="id" type="checkbox" role="switch" :checked="value === true" :disabled="form.disabled.value" :aria-describedby="describedBy" @change="set(($event.target as HTMLInputElement).checked)" />
      <span>{{ label }}</span>
    </label>
    <span v-if="error" :id="`${id}-err`" class="help bad">{{ error }}</span>
    <span v-else-if="schema.description" :id="`${id}-help`" class="help">{{ schema.description }}</span>
  </div>

  <!-- Everything with a single input -->
  <div v-else :class="['field', { 'st-focus': focused }]">
    <div class="label-row">
      <label v-if="kind !== 'enum' || options.length > 4" :for="id">{{ label }}</label>
      <span v-else :id="`${id}-label`" class="lbl">{{ label }}</span>
      <span v-if="kind === 'secret' && isSet" class="set-chip"><Glyph kind="lock" />set</span>
      <span v-if="!required" class="opt-word">optional</span>
    </div>

    <div v-if="kind === 'secret'" class="secret-row">
      <input
        :id="id"
        :type="reveal ? 'text' : 'password'"
        :value="form.secrets[path] ?? ''"
        :placeholder="isSet ? 'Stored in the vault · type to replace' : placeholder"
        :aria-invalid="error ? 'true' : undefined"
        :aria-describedby="describedBy"
        :aria-required="required && !isSet ? 'true' : undefined"
        :disabled="form.disabled.value"
        class="mono-input"
        autocomplete="new-password"
        autocapitalize="off"
        spellcheck="false"
        data-secret="true"
        @input="onSecret"
        @blur="blur"
      />
      <button
        type="button"
        class="btn sm ghost"
        :aria-pressed="reveal"
        :aria-label="reveal ? `Hide ${label}` : `Show what you typed for ${label}`"
        :disabled="!form.secrets[path]"
        @click="reveal = !reveal"
      >
        {{ reveal ? 'Hide' : 'Show' }}
      </button>
    </div>

    <input
      v-else-if="kind === 'number' || kind === 'integer'"
      :id="id"
      type="number"
      :inputmode="kind === 'integer' ? 'numeric' : 'decimal'"
      :step="kind === 'integer' ? 1 : 'any'"
      :min="schema.minimum"
      :max="schema.maximum"
      :value="typeof value === 'number' ? value : ''"
      :placeholder="placeholder || (schema.default !== undefined ? String(schema.default) : '')"
      :aria-invalid="error ? 'true' : undefined"
      :aria-describedby="describedBy"
      :disabled="form.disabled.value"
      @input="onNumber"
      @blur="blur"
    />

    <div v-else-if="kind === 'enum' && options.length <= 4" class="choice-row" role="radiogroup" :aria-labelledby="`${id}-label`" :aria-describedby="describedBy">
      <label v-for="o in options" :key="String(o.value)">
        <input
          type="radio"
          :name="id"
          :value="String(o.value)"
          :checked="value === o.value"
          :disabled="form.disabled.value"
          @change="onChoice"
          @blur="blur"
        />{{ o.label }}
      </label>
    </div>

    <select
      v-else-if="kind === 'enum'"
      :id="id"
      :value="value === undefined ? '' : String(value)"
      :aria-invalid="error ? 'true' : undefined"
      :aria-describedby="describedBy"
      :disabled="form.disabled.value"
      @change="onChoice"
      @blur="blur"
    >
      <option v-if="!required" value="">—</option>
      <option v-for="o in options" :key="String(o.value)" :value="String(o.value)">{{ o.label }}</option>
    </select>

    <input
      v-else
      :id="id"
      :type="schema.format === 'uri' || schema.format === 'url' ? 'url' : 'text'"
      :inputmode="schema.format === 'uri' || schema.format === 'url' ? 'url' : undefined"
      :value="value ?? ''"
      :placeholder="placeholder"
      :aria-invalid="error ? 'true' : undefined"
      :aria-describedby="describedBy"
      :aria-required="required ? 'true' : undefined"
      :disabled="form.disabled.value"
      autocomplete="off"
      autocapitalize="off"
      spellcheck="false"
      @input="onText"
      @blur="blur"
    />
    <!-- Outside the v-if chain above: inside it, a secret without this button fell through to the text input. -->
    <AuthorizeButton v-if="kind === 'secret' && schema['x-chadash-authorize'] === true && typeof k === 'string'" :label="label" />

    <span v-if="error" :id="`${id}-err`" class="help bad">{{ error }}</span>
    <span v-else-if="schema.description" :id="`${id}-help`" class="help">{{ schema.description }}</span>
  </div>
</template>

<style>
.field .lbl {
  font: var(--weight-medium) var(--type-small) / 1.2 var(--font-ui);
  color: var(--text-2);
}
.st-array > .secret-row > .field {
  flex: 1;
  min-width: 0;
}
</style>
