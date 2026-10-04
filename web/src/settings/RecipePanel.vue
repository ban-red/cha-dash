<script setup lang="ts">
/**
 * The least-privilege recipe beside an integration's form: how to make a
 * credential with exactly the access Cha Dash needs, as steps with commands
 * to copy. A small generator fills the commands with the names you choose
 * (and the form's URL where a command needs it); a value its pattern refuses
 * never reaches a command, and a command with a missing value can't be
 * copied. "Use in the form" carries what the recipe determines (Proxmox's
 * token ID) into the form. After Test connection, it says whether the
 * credential holds more than the recipe gives it.
 */
import { computed, reactive, ref, useId, watch } from 'vue';
import Glyph from '../components/marks/Glyph.vue';
import RecipeText from './RecipeText.vue';
import { normalizeGrant, type TestResult } from '../model/integrations';
import { accessOf, boundField, copyText, extraGrants, fills, gaps, paramValues, render, segments, type Recipe, type RecipeParam } from './recipe';

const {
  recipe,
  form,
  name = '',
  labels = {},
  test,
  open = true,
} = defineProps<{
  recipe: Recipe;
  /** The form's current config values, for parameters bound to a field. */
  form: Record<string, unknown>;
  /** The instance's name in the form, for a parameter bound to it. */
  name?: string;
  /** Field labels, for "Goes into the form". */
  labels?: Record<string, string>;
  /** The last Test connection result, to compare grants with. */
  test?: TestResult | undefined;
  /** Start expanded (adding), or folded (editing). */
  open?: boolean;
}>();
const emit = defineEmits<{ fill: [patch: Record<string, string>] }>();

const uid = useId();
const inputs = reactive<Record<string, string>>({});
watch(
  () => recipe,
  (r) => {
    for (const k of Object.keys(inputs)) delete inputs[k];
    for (const p of r.params) if (!boundField(p)) inputs[p.name] = p.default;
  },
  { immediate: true },
);

const access = computed(() => accessOf(recipe)!);
const values = computed(() => paramValues(recipe, inputs, form, name));
const editable = computed(() => recipe.params.filter((p) => !boundField(p)));
const waiting = computed(() => recipe.params.filter((p) => boundField(p) && values.value[p.name] === undefined));
const invalid = (p: RecipeParam) => values.value[p.name] === undefined;

const filled = computed(() => fills(recipe, values.value));
const fillRows = computed(() => Object.keys(recipe.fills ?? {}).map((k) => ({ field: k, label: labels[k] ?? k, value: filled.value[k], inForm: form[k] === filled.value[k] })));
const pendingFills = computed(() => fillRows.value.some((r) => r.value !== undefined && !r.inForm));

const testGrants = computed(() => (test?.ok ? (test.grants ?? []).map((g) => normalizeGrant(g)) : []));
const extra = computed(() => extraGrants(recipe, testGrants.value));

const copied = ref('');
const copyFailed = ref('');
async function copy(key: string, tpl: string): Promise<void> {
  const text = render(tpl, recipe, values.value);
  if (text === undefined) return;
  copyFailed.value = '';
  if (await copyText(text)) {
    copied.value = key;
    setTimeout(() => {
      if (copied.value === key) copied.value = '';
    }, 2000);
  } else {
    copyFailed.value = key;
  }
}

/** A consent page or similar: open it, but only an http(s) address. */
function openable(tpl: string): string | undefined {
  const u = render(tpl, recipe, values.value);
  return u && /^https?:\/\/[^\s]+$/i.test(u) ? u : undefined;
}

function patternHint(p: RecipeParam): string {
  return p.help ? `${p.help} ` : '';
}
</script>

<template>
  <details class="recipe" :open="open">
    <summary>
      <span class="rc-kicker caps">How to connect</span>
      <span class="rc-title">{{ recipe.title }}</span>
      <span :class="['rc-access', `tone-${access.tone}`]">
        <Glyph :kind="access.tone === 'ok' ? 'lock' : access.tone === 'attn' ? 'attn' : 'ok'" />{{ access.label }}
      </span>
    </summary>

    <div class="rc-body">
      <p class="rc-summary">{{ recipe.summary }}</p>

      <fieldset v-if="editable.length" class="rc-gen">
        <legend>Names to use</legend>
        <div v-for="p in editable" :key="p.name" class="field">
          <label :for="`${uid}-${p.name}`">{{ p.label }}</label>
          <select v-if="p.options?.length" :id="`${uid}-${p.name}`" v-model="inputs[p.name]" :aria-describedby="p.help ? `${uid}-${p.name}-h` : undefined">
            <option v-for="o in p.options" :key="o.value" :value="o.value">{{ o.label }}</option>
          </select>
          <input
            v-else
            :id="`${uid}-${p.name}`"
            v-model.trim="inputs[p.name]"
            class="mono-input"
            autocomplete="off"
            autocapitalize="off"
            spellcheck="false"
            :aria-invalid="invalid(p) ? 'true' : undefined"
            :aria-describedby="`${uid}-${p.name}-h`"
          />
          <span v-if="invalid(p) && !p.options?.length" :id="`${uid}-${p.name}-h`" class="help bad">{{ patternHint(p) }}That can’t go into a command as typed.</span>
          <span v-else-if="p.help" :id="`${uid}-${p.name}-h`" class="help">{{ p.help }}</span>
        </div>
      </fieldset>
      <p v-if="waiting.length" class="rc-wait"><Glyph kind="ok" /> Fill in {{ waiting.map((p) => p.label).join(' and ') }} in the form to complete the commands.</p>

      <ol class="rc-steps">
        <li v-for="(s, i) in recipe.steps" :key="i">
          <h5><RecipeText :text="s.title" :recipe="recipe" :values="values" /></h5>
          <template v-for="(b, j) in s.blocks" :key="j">
            <p v-if="b.kind === 'text'"><RecipeText :text="b.text ?? ''" :recipe="recipe" :values="values" /></p>
            <component :is="b.ordered ? 'ol' : 'ul'" v-else-if="b.kind === 'list'" class="rc-list">
              <li v-for="(it, k) in b.items" :key="k"><RecipeText :text="it" :recipe="recipe" :values="values" /></li>
            </component>
            <div v-else class="rc-code">
              <pre :aria-label="`${b.lang === 'url' ? 'Address' : 'Commands'} for step ${i + 1}`"><code><template v-for="(seg, k) in segments(b.code ?? '', recipe, values)" :key="k"><span v-if="seg.t === 'val'" class="rc-val">{{ seg.v }}</span><span v-else-if="seg.t === 'gap'" class="rc-gap">‹{{ seg.label }}›</span><template v-else>{{ seg.v }}</template></template></code></pre>
              <div class="rc-code-acts">
                <button
                  type="button"
                  class="btn sm"
                  :disabled="gaps(b.code ?? '', recipe, values).length > 0"
                  :title="gaps(b.code ?? '', recipe, values).length ? `Needs ${gaps(b.code ?? '', recipe, values).join(', ')}` : undefined"
                  @click="copy(`${i}.${j}`, b.code ?? '')"
                >
                  {{ copied === `${i}.${j}` ? 'Copied' : 'Copy' }}
                </button>
                <a v-if="b.lang === 'url' && openable(b.code ?? '')" class="btn sm" :href="openable(b.code ?? '')" target="_blank" rel="noopener noreferrer">Open <Glyph kind="link" /></a>
                <span v-if="copyFailed === `${i}.${j}`" class="rc-copy-fail" role="status">Select the text and copy it by hand.</span>
              </div>
            </div>
          </template>
        </li>
      </ol>

      <div v-if="fillRows.length" class="rc-block">
        <h5>Goes into the form</h5>
        <ul class="rel">
          <li v-for="r in fillRows" :key="r.field">
            <span>{{ r.label }}: <span class="n">{{ r.value ?? '—' }}</span></span>
            <span>{{ r.inForm ? 'in the form' : '' }}</span>
          </li>
        </ul>
        <div class="st-acts">
          <button type="button" class="btn sm" :disabled="!pendingFills" @click="emit('fill', filled)">Use in the form</button>
        </div>
      </div>

      <div v-if="recipe.grants?.length" class="rc-block">
        <h5>Test connection should show</h5>
        <p class="rc-chips">
          <span v-for="g in recipe.grants" :key="g" class="chip n">{{ g }}</span>
        </p>
        <p v-if="test?.ok && extra.length" class="st-note attn" role="status"><Glyph kind="attn" /><span>This credential holds more than the recipe gives it: {{ extra.join(', ') }}.</span></p>
        <p v-else-if="test?.ok" class="rc-match" role="status"><Glyph kind="check" /> Your test shows nothing beyond these.</p>
      </div>

      <div v-if="recipe.notes?.length" class="rc-block">
        <h5>Good to know</h5>
        <ul class="rc-notes">
          <template v-for="(b, j) in recipe.notes" :key="j">
            <li v-if="b.kind === 'text'"><RecipeText :text="b.text ?? ''" :recipe="recipe" :values="values" /></li>
            <template v-else-if="b.kind === 'list'">
              <li v-for="(it, k) in b.items" :key="k"><RecipeText :text="it" :recipe="recipe" :values="values" /></li>
            </template>
          </template>
        </ul>
      </div>
    </div>
  </details>
</template>

<style>
/* ───────────── The recipe beside an integration's form ───────────── */
.recipe {
  border: var(--hairline) solid var(--line-2);
  border-radius: var(--radius-lg);
  background: var(--surface);
}
.recipe > summary {
  display: grid;
  grid-template-columns: minmax(0, 1fr) auto;
  gap: 4px var(--space-3);
  align-items: center;
  padding: var(--space-3) var(--space-4);
  cursor: pointer;
  list-style: none;
}
.recipe > summary::-webkit-details-marker {
  display: none;
}
.recipe > summary:focus-visible {
  outline: 2px solid var(--focus);
  outline-offset: 2px;
  border-radius: var(--radius-lg);
}
.rc-kicker {
  grid-column: 1 / -1;
}
.rc-title {
  font: var(--weight-medium) var(--type-callout) / 1.3 var(--font-ui);
  color: var(--text);
}
.rc-access {
  display: inline-flex;
  align-items: center;
  gap: 5px;
  padding: 3px 8px;
  border: var(--hairline) solid var(--line-2);
  border-radius: var(--radius-full);
  font: var(--weight-medium) var(--type-caption) / 1 var(--font-ui);
  color: var(--text-2);
  white-space: nowrap;
}
.rc-access.tone-attn {
  border-color: color-mix(in oklch, var(--attention) 50%, var(--line-2));
  color: var(--attention-text);
}
.rc-body {
  display: flex;
  flex-direction: column;
  gap: var(--space-4);
  padding: 0 var(--space-4) var(--space-4);
  font-size: var(--type-small);
  line-height: var(--leading-relaxed);
  color: var(--text-2);
}
.rc-summary {
  text-wrap: pretty;
}
.rc-gen {
  display: grid;
  grid-template-columns: repeat(auto-fit, minmax(150px, 1fr));
  gap: var(--space-3);
  margin: 0;
  padding: var(--space-3);
  border: var(--hairline) solid var(--line);
  border-radius: var(--radius-md);
}
.rc-gen > legend {
  padding: 0 6px;
  font: var(--weight-medium) var(--type-micro) / 1 var(--font-num);
  letter-spacing: var(--tracking-caps);
  text-transform: uppercase;
  color: var(--text-3);
}
.rc-gen .field input {
  font-size: var(--type-small);
}
.rc-wait,
.rc-match {
  display: flex;
  align-items: center;
  gap: var(--space-2);
  color: var(--text-3);
}
.rc-steps {
  display: flex;
  flex-direction: column;
  gap: var(--space-4);
  margin: 0;
  padding: 0;
  list-style: none;
  counter-reset: rc-step;
}
.rc-steps > li {
  display: flex;
  flex-direction: column;
  gap: var(--space-2);
  padding-left: 28px;
  position: relative;
  counter-increment: rc-step;
}
.rc-steps > li::before {
  content: counter(rc-step);
  position: absolute;
  left: 0;
  top: 1px;
  width: 20px;
  height: 20px;
  display: grid;
  place-items: center;
  border: var(--hairline) solid var(--line-2);
  border-radius: 50%;
  font: 600 10.5px/1 var(--font-num);
  color: var(--text-3);
}
.rc-steps h5,
.rc-block h5 {
  font: var(--weight-medium) var(--type-body) / 1.35 var(--font-ui);
  color: var(--text);
}
.rc-block h5 {
  margin-bottom: var(--space-2);
  font: var(--weight-medium) var(--type-micro) / 1 var(--font-num);
  letter-spacing: var(--tracking-caps);
  text-transform: uppercase;
  color: var(--text-3);
}
.rc-body code,
.rc-steps h5 code {
  font-family: var(--font-num);
  font-size: 0.92em;
  color: var(--text);
}
.rc-body b {
  font-weight: var(--weight-medium);
  color: var(--text);
}
.rc-list {
  margin: 0;
  padding-left: 1.3em;
}
.rc-code {
  position: relative;
  border: var(--hairline) solid var(--line);
  border-radius: var(--radius-md);
  background: var(--bg);
}
.rc-code pre {
  margin: 0;
  padding: 10px 12px 6px;
  overflow-x: auto;
  font: var(--weight-regular) 12px / 1.55 var(--font-num);
  color: var(--text);
  white-space: pre;
  tab-size: 2;
}
.rc-val {
  border-radius: 2px;
  background: var(--accent-bg);
  color: var(--accent-text);
}
.rc-gap {
  font-style: italic;
  color: var(--attention-text);
}
.rc-code-acts {
  display: flex;
  flex-wrap: wrap;
  align-items: center;
  gap: var(--space-2);
  padding: 4px 8px 8px;
}
.rc-copy-fail {
  font-size: var(--type-caption);
  color: var(--text-3);
}
.rc-chips {
  display: flex;
  flex-wrap: wrap;
  gap: 6px;
}
.rc-notes {
  margin: 0;
  padding-left: 1.2em;
  display: flex;
  flex-direction: column;
  gap: var(--space-2);
}
</style>
