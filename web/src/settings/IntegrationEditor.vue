<script setup lang="ts">
/**
 * Add or edit one integration instance. The form is generated from the
 * integration's `config_schema`; secrets are write-only (blank keeps what the
 * vault holds, and nothing the hub holds is ever put back in an input).
 *
 * - Test connection: unsaved settings go to POST /api/integrations/test; an
 *   unchanged saved instance is tested as stored. The result shows version,
 *   latency, grants (write grants on a read-only connection flagged),
 *   warnings and errors.
 * - Save: POST (new) or PUT with only what changed, secrets only when typed.
 * - Delete: confirmed in place.
 * - A Repair's Reconnect lands here with `?field=…` (or `reason=auth`), which
 *   focuses the field that failed.
 * - The integration's least-privilege recipe sits beside the form
 *   (RecipePanel), open while adding; what discovery found pre-fills a new
 *   one (onboarding/draft.ts).
 * - The control credential (or mode) is its own folded section, "Let Cha
 *   Dash act" (ControlSection), with the recipe's control variant beside the
 *   form. `?section=control` (an action refused as unavailable) opens it.
 */
import { computed, nextTick, onMounted, provide, reactive, ref, shallowRef } from 'vue';
import Glyph from '../components/marks/Glyph.vue';
import ControlSection from './ControlSection.vue';
import RecipePanel from './RecipePanel.vue';
import SchemaField from './SchemaField.vue';
import TestResultView from './TestResultView.vue';
import { FORM } from './form';
import { ApiError } from '../lib/api';
import { ago } from '../lib/format';
import { navigate, settingsPath } from '../lib/router';
import { takeDraft } from '../onboarding/draft';
import { clean, fieldId, fieldLabel, fieldPaths, initialValue, typedSecrets, validate, type Errors, type JsonSchema } from '../lib/schema';
import {
  findInstance,
  focusField,
  isReadOnly,
  secretsSet as setOf,
  STATUS_WORDS,
  statusGlyph,
  type InstanceDetail,
  type TestResult,
} from '../model/integrations';
import {
  catalogEntry,
  createInstance,
  deleteInstance,
  errorWords,
  integrations,
  loadCatalog,
  loadInstance,
  loadInstances,
  testConnection,
  updateInstance,
} from '../stores/integrations';
import { useUi } from '../stores/ui';
import { actStatus, controlKeys, hasControl } from '../model/control';

const { instanceRef, integrationId, focus, reason, section } = defineProps<{
  /** An instance id (or, from a Repair, a name or integration id to resolve). */
  instanceRef?: string | undefined;
  /** Adding a new instance of this integration. */
  integrationId?: string | undefined;
  focus?: string | undefined;
  reason?: string | undefined;
  /** "control": open and scroll to "Let Cha Dash act". */
  section?: string | undefined;
}>();

const ui = useUi();
const status = ref<'loading' | 'ready' | 'missing' | 'error'>('loading');
const loadError = ref('');
const detail = shallowRef<InstanceDetail | undefined>(undefined);
const isNew = computed(() => !detail.value);
const entry = computed(() => catalogEntry(detail.value?.integration ?? integrationId ?? ''));
const EMPTY: JsonSchema = { type: 'object', properties: {} };
const schema = computed<JsonSchema>(() => entry.value?.config_schema ?? EMPTY);
const kindName = computed(() => entry.value?.name ?? detail.value?.integration ?? integrationId ?? 'integration');
const recipe = computed(() => entry.value?.recipe);
const fieldLabels = computed(() => labelsFor(recipe.value?.fills));
function labelsFor(fills: Record<string, string> | undefined): Record<string, string> {
  const props = schema.value.properties ?? {};
  return Object.fromEntries(Object.keys(fills ?? {}).map((k) => [k, fieldLabel(k, props[k] ?? {})]));
}

// The control credential: its own section, and its recipe beside the form.
const control = computed(() => (hasControl(entry.value?.control) ? entry.value!.control : undefined));
const ctlKeys = computed(() => controlKeys(control.value, schema.value));
const mainKeys = computed(() => Object.keys(schema.value.properties ?? {}).filter((k) => !ctlKeys.value.includes(k)));
const controlRecipe = computed(() => (control.value ? recipe.value?.control : undefined));
const controlLabels = computed(() => labelsFor(controlRecipe.value?.fills));
const controlOpen = ref(false);

const name = ref('');
const values = reactive<Record<string, unknown>>({});
const secrets = reactive<Record<string, string>>({});
const held = ref<ReadonlySet<string>>(new Set());
const showAll = ref(false);
const touched = reactive(new Set<string>());
const focusPath = ref<string | undefined>(undefined);
const saving = ref(false);
const saveError = ref('');
const confirmDelete = ref(false);
const deleting = ref(false);
let initial = '';

const config = computed(() => clean(schema.value, values) as Record<string, unknown>);
const errors = computed<Errors>(() => {
  const e = validate(schema.value, values, { secretsSet: held.value, secrets });
  if (!name.value.trim()) e['name'] = 'Give it a name.';
  return e;
});
const dirty = computed(() => JSON.stringify({ n: name.value.trim(), c: config.value }) !== initial || Object.values(secrets).some((v) => v !== ''));
const savedAct = computed(() => (control.value && detail.value ? actStatus(control.value, schema.value, detail.value.config ?? {}, held.value) : undefined));
const currentAct = computed(() => (control.value ? actStatus(control.value, schema.value, config.value, held.value, secrets) : undefined));
const readOnly = computed(() => isReadOnly(config.value));

provide(FORM, {
  secrets,
  secretsSet: held,
  errors,
  showAll,
  touched,
  focus: focusPath,
  disabled: computed(() => saving.value || deleting.value),
  integration: computed(() => (entry.value ? { id: entry.value.id, name: entry.value.name } : undefined)),
  testItem,
});

/** One item of a top-level list, tested alone (a check target's Test). Saves nothing. */
async function testItem(path: string, index: number): Promise<TestResult> {
  const list = config.value[path];
  if (!Array.isArray(list) || list[index] === undefined) throw new Error('Fill in this one first.');
  const cfg = { ...config.value, [path]: [list[index]] };
  const typed = typedSecrets(secrets);
  const d = detail.value;
  try {
    return await testConnection(d ? { instance_id: d.id, config: cfg, secrets: typed } : { integration: entry.value?.id ?? integrationId ?? '', config: cfg, secrets: typed });
  } catch (err) {
    throw new Error(errorWords(err, 'The test couldn’t run. Try again.'));
  }
}

type TestState = { state: 'idle' } | { state: 'running' } | { state: 'done'; result: TestResult } | { state: 'error'; message: string };
const test = ref<TestState>({ state: 'idle' });
/** The hub's hint that this connection could act (Test's control_available warning). */
const controlNote = computed(() => {
  const ws = [...(test.value.state === 'done' ? (test.value.result.warnings ?? []) : []), ...(detail.value?.warnings ?? [])];
  return ws.find((w) => w.code === 'control_available')?.message ?? '';
});

function reset(d: InstanceDetail | undefined): void {
  detail.value = d;
  const draft = d ? undefined : takeDraft(entry.value?.id ?? integrationId ?? '');
  name.value = d?.name ?? draft?.name ?? entry.value?.name ?? '';
  for (const k of Object.keys(values)) delete values[k];
  Object.assign(values, initialValue(schema.value, d?.config ?? draft?.config) as Record<string, unknown>);
  for (const k of Object.keys(secrets)) delete secrets[k];
  held.value = setOf(d);
  touched.clear();
  showAll.value = false;
  initial = JSON.stringify({ n: name.value.trim(), c: config.value });
}

/** The recipe's generator filled in fields (Proxmox's token ID): as if typed. */
function onFill(patch: Record<string, string>): void {
  for (const [k, v] of Object.entries(patch)) {
    values[k] = v;
    touched.add(k);
  }
}

async function focusField_(path: string | undefined): Promise<void> {
  if (path && ctlKeys.value.includes(path.split('.')[0]!)) controlOpen.value = true;
  await nextTick();
  const el = path ? document.getElementById(path === 'name' ? 'f-name' : fieldId(path)) : undefined;
  el?.focus();
  el?.closest('.field')?.scrollIntoView?.({ block: 'center' });
}

onMounted(async () => {
  await Promise.all([loadCatalog(), loadInstances()]);
  if (integrationId !== undefined && !instanceRef) {
    if (!catalogEntry(integrationId)) {
      status.value = 'missing';
      return;
    }
    reset(undefined);
    status.value = 'ready';
    focusPath.value = focusField(schema.value, focus, reason);
    await focusField_(focusPath.value ?? 'name');
    return;
  }
  const ref_ = instanceRef ?? '';
  const found = findInstance(integrations.instances, ref_);
  if (found && found.id !== ref_) {
    // A Repair named it by name or integration: go to its canonical address.
    navigate(settingsPath('integrations', found.id, { field: focus, reason }), { replace: true });
    return;
  }
  try {
    const d = await loadInstance(found?.id ?? ref_);
    if (!catalogEntry(d.integration)) await loadCatalog(true);
    reset(d);
    status.value = 'ready';
    focusPath.value = focusField(schema.value, focus, reason);
    if (focusPath.value) await focusField_(focusPath.value);
    else if (section === 'control') await openControl();
  } catch (err) {
    if (err instanceof ApiError && err.status === 404) status.value = 'missing';
    else {
      status.value = 'error';
      loadError.value = errorWords(err, 'This integration couldn’t be loaded.');
    }
  }
});

/** An unavailable action's recipe link: the control section, open and in view. */
async function openControl(): Promise<void> {
  if (!control.value) return;
  controlOpen.value = true;
  await nextTick();
  const el = document.getElementById('control');
  el?.scrollIntoView?.({ block: 'start' });
  el?.querySelector<HTMLElement>('summary')?.focus();
}

/** Add an instance of this integration, keeping the field to focus. */
function addPath(id: string): string {
  const q = new URLSearchParams();
  if (focus) q.set('field', focus);
  if (reason) q.set('reason', reason);
  const qs = q.toString();
  return `/settings/integrations/new/${encodeURIComponent(id)}${qs ? `?${qs}` : ''}`;
}

/** The catalog entry a missing reference might mean ("plex" from the Demo Home). */
const addInstead = computed(() => (status.value === 'missing' && instanceRef ? catalogEntry(instanceRef) : undefined));

function firstError(): string | undefined {
  const keys = Object.keys(errors.value);
  if (!keys.length) return undefined;
  if (keys.includes('name')) return 'name';
  return fieldPaths(schema.value).find((p) => keys.some((k) => k === p || k.startsWith(`${p}.`))) ?? keys[0];
}

/** A field's label for use mid-sentence: "Token" → "token", "API key" stays. */
function labelOf(path: string): string {
  const key = path.split('.').pop() ?? path;
  const props = schema.value.properties ?? {};
  const l = fieldLabel(key, props[key] ?? {});
  return l.length > 1 && l[1] === l[1]!.toLowerCase() ? l[0]!.toLowerCase() + l.slice(1) : l;
}

/**
 * A control credential half filled in: the hub refuses it as all or nothing,
 * so open its section at the first empty field instead of sending it.
 */
async function partialControl(): Promise<boolean> {
  const a = currentAct.value;
  if (a?.state !== 'partial') return false;
  showAll.value = true;
  await focusField_(a.missing[0]);
  return true;
}

async function runTest(): Promise<void> {
  const problems = Object.keys(errors.value).filter((k) => k !== 'name');
  if (problems.length) {
    showAll.value = true;
    await focusField_(firstError());
    return;
  }
  if (await partialControl()) return;
  const d = detail.value;
  const typed = typedSecrets(secrets);
  let target: Parameters<typeof testConnection>[0];
  if (d && !dirty.value) {
    target = { id: d.id };
  } else if (d) {
    // Unsaved edits to a saved instance: the hub fills in the stored secrets
    // that weren't retyped, and saves nothing.
    target = { instance_id: d.id, config: config.value, secrets: typed };
  } else {
    target = { integration: entry.value?.id ?? integrationId ?? '', config: config.value, secrets: typed };
  }
  test.value = { state: 'running' };
  try {
    test.value = { state: 'done', result: await testConnection(target) };
  } catch (err) {
    test.value = { state: 'error', message: errorWords(err, 'The test couldn’t run. Try again.') };
  }
}

async function save(): Promise<void> {
  showAll.value = true;
  saveError.value = '';
  if (Object.keys(errors.value).length) {
    await focusField_(firstError());
    return;
  }
  if (await partialControl()) return;
  saving.value = true;
  const typed = typedSecrets(secrets);
  try {
    const d = detail.value;
    if (!d) {
      const id = await createInstance({ integration: entry.value?.id ?? integrationId ?? '', name: name.value.trim(), config: config.value, secrets: typed });
      for (const k of Object.keys(secrets)) delete secrets[k];
      ui.toast(`Saved ${name.value.trim()}. Cha Dash starts reading it now.`);
      navigate(settingsPath('integrations', id), { replace: true });
      return;
    }
    const was = JSON.parse(initial) as { n: string; c: unknown };
    const body: { name?: string; config?: unknown; secrets?: Record<string, string> } = {};
    if (was.n !== name.value.trim()) body.name = name.value.trim();
    if (JSON.stringify(was.c) !== JSON.stringify(config.value)) body.config = config.value;
    if (Object.keys(typed).length) body.secrets = typed;
    await updateInstance(d.id, body);
    for (const k of Object.keys(secrets)) delete secrets[k];
    let fresh: InstanceDetail = { ...d, name: body.name ?? d.name, config: config.value, secrets: { ...d.secrets, ...Object.fromEntries(Object.keys(typed).map((k) => [k, { set: true }])) } };
    try {
      fresh = await loadInstance(d.id);
    } catch {
      /* keep the local picture */
    }
    reset(fresh);
    ui.toast(`Saved ${fresh.name}.`);
  } catch (err) {
    saveError.value = errorWords(err, 'Couldn’t save. Nothing changed.');
  } finally {
    saving.value = false;
  }
}

async function remove(): Promise<void> {
  const d = detail.value;
  if (!d) return;
  deleting.value = true;
  try {
    await deleteInstance(d.id);
    ui.toast(`Deleted ${d.name}. Its secrets are gone from the vault.`);
    navigate(settingsPath('integrations'), { replace: true });
  } catch (err) {
    saveError.value = errorWords(err, 'Couldn’t delete it. Try again.');
    confirmDelete.value = false;
  } finally {
    deleting.value = false;
  }
}

const nowMs = Date.now();
const nameError = computed(() => (showAll.value || touched.has('name') ? errors.value['name'] : undefined));
</script>

<template>
  <div :class="['st-section', 'editor', { 'with-recipe': recipe && status === 'ready' }]">
    <a class="st-crumb" :href="settingsPath('integrations')" @click.prevent="navigate(settingsPath('integrations'), { replace: true })">‹ Integrations</a>

    <div v-if="status === 'loading'" class="st-skel" aria-busy="true"><span class="skel"></span><span class="skel"></span><span class="skel"></span></div>

    <div v-else-if="status === 'missing'" class="st-empty" role="alert">
      <div class="t">{{ addInstead ? `${addInstead.name} isn’t connected on this hub` : 'Not found' }}</div>
      <div class="m">
        <template v-if="addInstead">Add it to read {{ addInstead.name }}: paste its URL and credential, test the connection, then save.</template>
        <template v-else-if="integrationId">This hub doesn’t have an integration called “{{ integrationId }}”.</template>
        <template v-else>There’s no integration “{{ instanceRef }}” on this hub. It may have been deleted.</template>
      </div>
      <div class="st-acts">
        <a
          v-if="addInstead"
          class="btn primary"
          :href="`/settings/integrations/new/${encodeURIComponent(addInstead.id)}`"
          @click.prevent="navigate(addPath(addInstead.id), { replace: true })"
          >Add {{ addInstead.name }}</a
        >
        <a class="btn" :href="settingsPath('integrations')" @click.prevent="navigate(settingsPath('integrations'), { replace: true })">All integrations</a>
      </div>
    </div>

    <div v-else-if="status === 'error'" class="st-note bad" role="alert"><Glyph kind="down" /><span>{{ loadError }}</span></div>

    <template v-else>
      <div class="ed-head">
        <h3 class="ed-title">{{ isNew ? `Add ${kindName}` : detail?.name }}</h3>
        <span v-if="detail" class="st-status">
          <Glyph :kind="statusGlyph(detail.status)" :label="STATUS_WORDS[detail.status] ?? detail.status" />
          <span aria-hidden="true">{{ STATUS_WORDS[detail.status] ?? detail.status }}</span>
        </span>
      </div>
      <p class="st-meta">
        {{ kindName }}<template v-if="entry?.version"> · integration v{{ entry.version }}</template
        ><template v-if="detail?.version"> · reads {{ detail.version }}</template
        ><template v-if="detail?.last_ok"> · last read {{ ago(detail.last_ok, nowMs) }}</template>
      </p>

      <div v-if="detail && (detail.status === 'repair' || detail.status === 'error') && detail.last_error" class="st-note bad" role="alert">
        <Glyph :kind="detail.status === 'repair' ? 'repair' : 'down'" />
        <span>
          <b>{{ detail.last_error }}</b>
          <template v-if="focusPath"> Paste a new {{ labelOf(focusPath) }} below, test it, then save.</template>
        </span>
      </div>
      <ul v-if="detail?.warnings?.length" class="rel">
        <li v-for="w in detail.warnings" :key="w.code + w.message">
          <span><Glyph kind="attn" /> {{ w.message }}</span><span>{{ w.code }}</span>
        </li>
      </ul>

      <div :class="['ed-grid', { 'has-recipe': recipe }]">
        <div class="ed-main">
          <form class="st-form" novalidate :aria-label="isNew ? `Add ${kindName}` : `Edit ${detail?.name}`" @submit.prevent="save">
            <div :class="['field', { 'st-focus': focusPath === 'name' }]">
              <div class="label-row"><label for="f-name">Name</label></div>
              <input
                id="f-name"
                v-model="name"
                autocomplete="off"
                spellcheck="false"
                :aria-invalid="nameError ? 'true' : undefined"
                :aria-describedby="nameError ? 'f-name-err' : 'f-name-help'"
                :disabled="saving || deleting"
                @blur="touched.add('name')"
              />
              <span v-if="nameError" id="f-name-err" class="help bad">{{ nameError }}</span>
              <span v-else id="f-name-help" class="help">How it’s called on your page and in the Inbox.</span>
            </div>

            <SchemaField
              v-for="key in mainKeys"
              :key="key"
              :schema="schema.properties![key]!"
              :container="values"
              :k="key"
              :path="key"
              :required="(schema.required ?? []).includes(key)"
            />

            <ControlSection
              v-if="control && ctlKeys.length && currentAct"
              v-model:open="controlOpen"
              :control="control"
              :schema="schema"
              :keys="ctlKeys"
              :values="values"
              :saved="savedAct"
              :current="currentAct"
              :kind="isNew ? kindName : (detail?.name ?? kindName)"
              :note="controlNote"
            />

            <p v-if="saveError" class="err" role="alert">{{ saveError }}</p>

            <div class="st-acts ed-acts">
              <button type="button" class="btn" :disabled="test.state === 'running' || saving" :aria-busy="test.state === 'running'" @click="runTest">
                {{ test.state === 'running' ? 'Testing…' : 'Test connection' }}
              </button>
              <button type="submit" class="btn primary" :disabled="saving || (!isNew && !dirty)" :aria-busy="saving">
                {{ saving ? 'Saving…' : isNew ? 'Save' : 'Save changes' }}
              </button>
              <button v-if="!isNew && !confirmDelete" type="button" class="btn ghost ed-del" :disabled="saving" @click="confirmDelete = true">Delete…</button>
            </div>

            <div v-if="confirmDelete" class="st-confirm" role="alertdialog" aria-labelledby="del-q">
              <span id="del-q">Delete <b>{{ detail?.name }}</b>? Its tiles stop updating, and its stored secrets are erased from the vault.</span>
              <button type="button" class="btn danger solid" :disabled="deleting" :aria-busy="deleting" @click="remove">
                {{ deleting ? 'Deleting…' : 'Delete' }}<span class="tier">T1</span>
              </button>
              <button type="button" class="btn" :disabled="deleting" @click="confirmDelete = false">Cancel</button>
            </div>
          </form>

          <div class="ed-test" aria-live="polite">
            <TestResultView v-if="test.state === 'done'" :result="test.result" :read-only="readOnly" />
            <div v-else-if="test.state === 'error'" class="st-note bad" role="alert"><Glyph kind="down" /><span>{{ test.message }}</span></div>
          </div>
        </div>
        <div v-if="recipe" class="ed-aside">
          <RecipePanel
            :recipe="recipe"
            :form="config"
            :name="name"
            :labels="fieldLabels"
            :test="test.state === 'done' ? test.result : undefined"
            :open="isNew"
            @fill="onFill"
          />
          <div v-if="controlRecipe" class="ed-control-recipe">
            <h4 class="caps">To let Cha Dash act</h4>
            <RecipePanel
              :recipe="controlRecipe"
              :form="config"
              :name="name"
              :labels="controlLabels"
              :open="controlOpen"
              @fill="onFill"
            />
          </div>
        </div>
      </div>
    </template>
  </div>
</template>

<style>
.editor {
  max-width: 680px;
}
.ed-head {
  display: flex;
  align-items: baseline;
  gap: var(--space-3);
}
.ed-title {
  font: var(--weight-medium) var(--type-title) / 1.2 var(--font-ui);
  letter-spacing: -0.006em;
  color: var(--text);
}
.ed-acts {
  padding-top: var(--space-2);
}
.ed-del {
  margin-left: auto;
}
.ed-test:empty {
  display: none;
}
.ed-control-recipe {
  margin-top: var(--space-5);
}
.ed-control-recipe h4 {
  margin-bottom: var(--space-2);
  font: var(--weight-medium) var(--type-caption) / 1.3 var(--font-ui);
  letter-spacing: var(--tracking-caps);
  color: var(--text-2);
}
.ed-grid,
.ed-main {
  display: flex;
  flex-direction: column;
  gap: var(--space-5);
}
.ed-main .st-form {
  max-width: none;
}
/* Wide enough for the recipe beside the form: the sheet widens to make room. */
@media (min-width: 1180px) {
  .settings:has(.ed-grid.has-recipe) {
    width: min(1320px, 100%);
  }
  .editor.with-recipe {
    max-width: 1120px;
  }
  .ed-grid.has-recipe {
    display: grid;
    grid-template-columns: minmax(0, 1fr) minmax(0, 1fr);
    gap: var(--space-6);
    align-items: start;
  }
  .ed-aside {
    position: sticky;
    top: 0;
  }
}
</style>
