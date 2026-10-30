<script setup lang="ts">
/**
 * One field of a form generated from an integration's JSON Schema, at any
 * depth: text, URL, number, yes/no, a choice, a nested object (a fieldset),
 * a list, or a secret.
 *
 * Secrets (`writeOnly`) are write-only: the input starts empty whatever the
 * hub holds, a "set" chip says one is stored, blank keeps it, and what you
 * type lives in the form's secrets map, never in the config values. An
 * optional one that's stored can be removed: Remove marks it, Save sends
 * null, and the same button keeps it again until then.
 *
 * Writes go straight into the parent object (`container[k]`), which is the
 * editor's reactive config, so nested fields need no event plumbing.
 *
 * A list marked `x-chadash-item-test` gives each item a Test button that
 * tests the instance with only that item (a check target), via the form.
 *
 * An optional nested object (a target's TLS) is folded: its header says what
 * it holds ("verify", "pin · 3f2a…"), and it opens on an error inside or a
 * repair's focus.
 */
import { computed, reactive, ref, watch } from 'vue';
import Glyph from '../ui/marks/Glyph.vue';
import AuthorizeButton from './AuthorizeButton.vue';
import { choices, effective, fieldId, fieldLabel, kindOf, newItem, type JsonSchema } from '../lib/schema';
import { useForm } from './form';
import UiButton from '../ui/Button.vue';

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
const removing = computed(() => form.cleared.has(path));
const canRemove = computed(() => kind.value === 'secret' && isSet.value && !required);
const reveal = ref(false);
const focused = computed(() => form.focus.value === path);

/** For objects: the properties in force (an `if`/`then` branch may add some). */
const objectSchema = computed(() => effective(schema, value.value));
const objectRequired = computed(() => new Set(objectSchema.value.required ?? []));
const join = (key: string | number) => `${path}.${key}`;

// An optional object, folded
const foldable = computed(() => kind.value === 'object' && !required);
const inside = (p: string | undefined) => p !== undefined && (p === path || p.startsWith(`${path}.`));
const errorInside = computed(() => Object.keys(form.errors.value).some((p) => inside(p) && (form.showAll.value || form.touched.has(p))));
const folded = ref(true);
watch(
  () => errorInside.value || inside(form.focus.value),
  (open) => {
    if (open) folded.value = false;
  },
  { immediate: true },
);
/** The folded header's line: each filled field, briefly. */
const foldSummary = computed(() => {
  const obj = (value.value ?? {}) as Record<string, unknown>;
  const parts: string[] = [];
  for (const [key, sub] of Object.entries(objectSchema.value.properties ?? {})) {
    const v = obj[key];
    if (v === undefined || v === null || v === '' || sub.writeOnly) continue;
    const opt = choices(sub)?.find((o) => o.value === v);
    if (opt) parts.push(opt.label);
    else if (typeof v === 'boolean') parts.push(v ? fieldLabel(key, sub) : '');
    else if (typeof v === 'string' && v.length > 12) parts.push(`${v.slice(0, 8)}…`);
    else if (typeof v !== 'object') parts.push(`${fieldLabel(key, sub)} ${String(v)}`);
  }
  return parts.filter(Boolean).join(' · ') || 'Not set';
});

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

function toggleRemove(): void {
  if (form.cleared.has(path)) {
    form.cleared.delete(path);
  } else {
    form.cleared.add(path);
    delete form.secrets[path];
    reveal.value = false;
  }
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
  for (const key of Object.keys(itemTests)) delete itemTests[Number(key)];
}

// Per-item Test
type ItemTest = { state: 'running' } | { state: 'ok'; text: string; warn: string } | { state: 'bad'; text: string };
const itemTests = reactive<Record<number, ItemTest>>({});
const canTestItems = computed(() => schema['x-chadash-item-test'] === true && itemIsObject.value && !!form.testItem && typeof k === 'string');

async function testItem(i: number): Promise<void> {
  itemTests[i] = { state: 'running' };
  try {
    const r = await form.testItem!(path, i);
    // Warnings are clauses ("answering, but …"): sentences here.
    const warn = (r.warnings ?? []).map((w) => w.message.charAt(0).toUpperCase() + w.message.slice(1)).join(' ');
    if (r.ok) itemTests[i] = { state: 'ok', text: typeof r.latency_ms === 'number' ? `Answered in ${Math.round(r.latency_ms)} ms.` : 'Answered.', warn };
    else itemTests[i] = { state: 'bad', text: (typeof r.error === 'string' ? r.error : r.error?.message) || 'It didn’t answer.' };
  } catch (err) {
    itemTests[i] = { state: 'bad', text: err instanceof Error ? err.message : 'The test couldn’t run. Try again.' };
  }
}
</script>

<template>
  <!-- An optional nested object: folded, its header saying what it holds -->
  <details v-if="foldable" class="st-fold" :open="!folded" @toggle="folded = !($event.target as HTMLDetailsElement).open">
    <summary>
      <span class="st-fold-title">{{ label }}</span>
      <span class="st-fold-sum">{{ foldSummary }}</span>
      <svg class="st-fold-chev" viewBox="0 0 10 10" aria-hidden="true"><path d="m3 2 3 3-3 3" /></svg>
    </summary>
    <fieldset class="st-fieldset st-fold-body" :disabled="form.disabled.value">
      <legend class="sr">{{ label }}</legend>
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
  </details>

  <!-- A nested object: a fieldset of its own fields -->
  <fieldset v-else-if="kind === 'object'" class="st-fieldset" :disabled="form.disabled.value">
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
          <p v-if="itemTests[i] && itemTests[i].state !== 'running'" :class="['st-note', 'item-test', itemTests[i].state === 'bad' ? 'bad' : itemTests[i].warn ? 'attn' : '']" role="status">
            <Glyph :kind="itemTests[i].state === 'bad' ? 'down' : itemTests[i].warn ? 'attn' : 'check'" />
            <span>{{ itemTests[i].state === 'ok' && itemTests[i].warn ? `${itemTests[i].text} ${itemTests[i].warn}` : itemTests[i].text }}</span>
          </p>
          <div class="item-acts">
            <UiButton v-if="canTestItems" size="sm" :disabled="form.disabled.value || itemTests[i]?.state === 'running'" :aria-busy="itemTests[i]?.state === 'running'" @click="testItem(i)">
              {{ itemTests[i]?.state === 'running' ? 'Testing…' : `Test ${singular.toLowerCase()} ${i + 1}` }}
            </UiButton>
            <UiButton variant="ghost" size="sm" :disabled="form.disabled.value" @click="removeItem(i)">Remove {{ singular.toLowerCase() }} {{ i + 1 }}</UiButton>
          </div>
        </fieldset>
        <div v-else class="secret-row">
          <SchemaField :schema="schema.items ?? { type: 'string' }" :container="list" :k="i" :path="`${path}.${i}`" :required="true" />
          <UiButton variant="ghost" size="sm" :aria-label="`Remove ${singular} ${i + 1}`" :disabled="form.disabled.value" @click="removeItem(i)">Remove</UiButton>
        </div>
      </template>
      <UiButton size="sm" :disabled="form.disabled.value || (schema.maxItems !== undefined && list.length >= schema.maxItems)" @click="addItem">
        Add {{ singular.toLowerCase() }}
      </UiButton>
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
  <div v-else :class="['field', { 'st-focus': focused, half: kind === 'number' || kind === 'integer' }]">
    <div class="label-row">
      <label v-if="kind !== 'enum' || options.length > 4" :for="id">{{ label }}</label>
      <span v-else :id="`${id}-label`" class="lbl">{{ label }}</span>
      <span v-if="kind === 'secret' && isSet && removing" class="set-chip off"><Glyph kind="cross" />removed when you save</span>
      <span v-else-if="kind === 'secret' && isSet" class="set-chip"><Glyph kind="lock" />set</span>
      <span v-if="!required" class="opt-word">optional</span>
    </div>

    <div v-if="kind === 'secret'" class="secret-row">
      <input
        :id="id"
        :type="reveal ? 'text' : 'password'"
        :value="form.secrets[path] ?? ''"
        :placeholder="removing ? 'Removed from the vault when you save' : isSet ? 'Stored in the vault · type to replace' : placeholder"
        :aria-invalid="error ? 'true' : undefined"
        :aria-describedby="describedBy"
        :aria-required="required && !isSet ? 'true' : undefined"
        :disabled="form.disabled.value || removing"
        class="mono-input"
        autocomplete="new-password"
        autocapitalize="off"
        spellcheck="false"
        data-secret="true"
        @input="onSecret"
        @blur="blur"
      />
      <UiButton variant="ghost" size="sm" :aria-pressed="reveal" :aria-label="reveal ? `Hide ${label}` : `Show what you typed for ${label}`" :disabled="!form.secrets[path]" @click="reveal = !reveal">
        {{ reveal ? 'Hide' : 'Show' }}
      </UiButton>
      <!-- One button for both, so focus stays put when it flips. -->
      <UiButton v-if="canRemove" variant="ghost" size="sm" :aria-pressed="removing" :aria-label="removing ? `Keep the saved ${label}` : `Remove the saved ${label}`" :disabled="form.disabled.value" @click="toggleRemove">
        {{ removing ? 'Keep it' : 'Remove' }}
      </UiButton>
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
.st-fold {
  min-width: 0;
  border: var(--hairline) solid var(--line);
  border-radius: var(--radius-md);
}
.st-fold > summary {
  display: flex;
  align-items: center;
  gap: var(--space-2);
  min-height: 36px;
  padding: 6px var(--space-3);
  border-radius: var(--radius-md);
  cursor: pointer;
  list-style: none;
}
.st-fold > summary::-webkit-details-marker {
  display: none;
}
.st-fold > summary:hover {
  background: color-mix(in oklch, var(--text) 4%, transparent);
}
.st-fold > summary:focus-visible {
  outline: 2px solid var(--focus);
  outline-offset: 1px;
}
.st-fold-title {
  font: var(--weight-medium) var(--type-micro) / 1 var(--font-num);
  letter-spacing: var(--tracking-caps);
  text-transform: uppercase;
  color: var(--text-3);
}
.st-fold-sum {
  min-width: 0;
  overflow: hidden;
  font: var(--weight-regular) var(--type-small) / 1.2 var(--font-num);
  color: var(--text-2);
  white-space: nowrap;
  text-overflow: ellipsis;
}
.st-fold-chev {
  flex: none;
  width: 10px;
  height: 10px;
  margin-left: auto;
  fill: none;
  stroke: var(--text-3);
  stroke-width: 1.4;
  transition: transform var(--motion-press-duration) var(--motion-press-easing);
}
.st-fold[open] > summary .st-fold-chev {
  transform: rotate(90deg);
}
.st-fold-body.st-fieldset {
  padding-top: 2px;
  border: 0;
}
.field .lbl {
  font: var(--weight-medium) var(--type-small) / 1.2 var(--font-ui);
  color: var(--text-2);
}
.st-array > .secret-row > .field {
  flex: 1;
  min-width: 0;
}
</style>
