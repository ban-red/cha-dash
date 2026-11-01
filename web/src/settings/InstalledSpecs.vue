<script setup lang="ts">
/**
 * Settings → Integrations → Installed specs (the owner's; docs/plan/09-v0.3.md,
 * "The spec format v1alpha1"): Tier 1 integrations added from a spec file,
 * beside the bundled ones.
 *
 *   GET    /api/specs/installed                {specs}
 *   POST   /api/specs/installed/preview        {yaml, replace?} → {summary, replacing?}
 *   POST   /api/specs/installed                {yaml, source?, replace?} (step-up)
 *   DELETE /api/specs/installed/{id}           409 in_use while it has instances
 *   PUT    /api/specs/installed/{id}/actions   {on} (step-up to turn them on)
 *
 * Adding one is two steps: Check shows what the spec reads and which
 * actions it adds (each one's method, path and tier), then Install, with a
 * step-up. Its reads start at once; its actions stay off until the owner
 * turns them on here, after seeing each one: turning them on names the
 * version reviewed (its SHA-256), so a replacement nobody looked at stays
 * off. A spec with the ID of one already installed offers Replace, which
 * turns changed actions off again.
 *
 * Its own chunk, loaded with the section.
 */
import { computed, onMounted, ref, useId } from 'vue';
import { ApiError, api } from '../lib/api';
import { ago } from '../lib/format';
import { StepUpCancelled } from '../stores/actions';
import { loadCatalog } from '../stores/integrations';
import { useUi } from '../stores/ui';
import Glyph from '../ui/marks/Glyph.vue';
import UiButton from '../ui/Button.vue';
import UiField from '../ui/Field.vue';

interface Summary {
  id: string;
  name: string;
  version: string;
  secrets: string[];
  requests: { name: string; method: string; path: string; optional?: boolean; secrets?: string[]; body?: string }[];
  actions: { id: string; title: string; tier: string; method: string; path: string; body?: string[]; household?: boolean }[];
}
interface Installed {
  id: string;
  sha256: string;
  source?: string;
  actions_on: boolean;
  installed_by?: string;
  installed_at: string;
  updated_at: string;
  instances: number;
  summary: Summary;
  error?: string;
}
interface Preview {
  summary: Summary;
  replacing?: Installed;
}

const ui = useUi();
const uid = useId();
const specs = ref<Installed[]>([]);
const status = ref<'loading' | 'ready' | 'error'>('loading');
const loadError = ref('');
const now = ref(Date.now());

const adding = ref(false);
const yaml = ref('');
const source = ref('');
const preview = ref<Preview | null>(null);
/** Its GET reads, and the POSTs that wait for its actions: a POST can change things whatever it's called. */
const reads = computed(() => preview.value?.summary.requests.filter((r) => r.method !== 'POST') ?? []);
const posts = computed(() => preview.value?.summary.requests.filter((r) => r.method === 'POST') ?? []);
const exists = ref(false);
const error = ref('');
const errorDetails = ref<string[]>([]);
const busy = ref(false);
const picker = ref<HTMLInputElement | null>(null);

async function load(): Promise<void> {
  try {
    specs.value = (await api<{ specs?: Installed[] }>('/api/specs/installed')).specs ?? [];
    status.value = 'ready';
  } catch (err) {
    loadError.value = err instanceof ApiError ? err.message : 'Couldn’t load the installed specs.';
    status.value = 'error';
  }
  now.value = Date.now();
}
onMounted(load);

function startAdd(): void {
  adding.value = true;
  yaml.value = source.value = error.value = '';
  errorDetails.value = [];
  preview.value = null;
  exists.value = false;
}

function edited(v: string): void {
  yaml.value = v;
  preview.value = null;
  exists.value = false;
  error.value = '';
  errorDetails.value = [];
}

async function onPick(e: Event): Promise<void> {
  const f = (e.target as HTMLInputElement).files?.[0];
  if (!f) return;
  edited(await f.text());
  if (!source.value) source.value = f.name;
}

function refused(err: unknown): void {
  if (err instanceof ApiError) {
    const body = err.body as { error?: { details?: string[] }; preview?: Preview } | undefined;
    error.value = err.message;
    errorDetails.value = (body?.error?.details ?? []).slice(1, 6);
    if (err.code === 'exists' && body?.preview) {
      preview.value = body.preview;
      exists.value = true;
      error.value = '';
    }
    return;
  }
  error.value = 'That didn’t work.';
}

async function check(): Promise<void> {
  busy.value = true;
  error.value = '';
  errorDetails.value = [];
  try {
    preview.value = await api<Preview>('/api/specs/installed/preview', { method: 'POST', body: { yaml: yaml.value } });
    exists.value = false;
  } catch (err) {
    preview.value = null;
    refused(err);
  } finally {
    busy.value = false;
  }
}

async function install(): Promise<void> {
  busy.value = true;
  const replace = exists.value;
  try {
    const v = await ui.withStepUp(replace ? 'Replace an installed spec' : 'Install a spec', () =>
      api<Installed>('/api/specs/installed', { method: 'POST', body: { yaml: yaml.value, source: source.value.trim(), replace } }),
    );
    ui.toast(`${replace ? 'Replaced' : 'Installed'} ${v.summary.name}. ${v.summary.actions.length && !v.actions_on ? 'Its actions are off until you turn them on.' : ''}`.trim());
    adding.value = false;
    await Promise.all([load(), loadCatalog(true)]);
  } catch (err) {
    if (!(err instanceof StepUpCancelled)) refused(err);
  } finally {
    busy.value = false;
  }
}

async function setActions(s: Installed, on: boolean): Promise<void> {
  try {
    const path = `/api/specs/installed/${encodeURIComponent(s.id)}/actions`;
    const v = await ui.withStepUp(`Turn ${on ? 'on' : 'off'} ${s.summary.name}’s actions`, () =>
      api<Installed>(path, { method: 'PUT', body: { on, sha256: s.sha256 } }),
    );
    specs.value = specs.value.map((x) => (x.id === v.id ? v : x));
  } catch (err) {
    if (!(err instanceof StepUpCancelled)) ui.toast(err instanceof ApiError ? err.message : 'That didn’t work.', 'error');
    // Replaced since this page loaded: show what's installed now.
    if (err instanceof ApiError && err.code === 'changed') await load();
  }
}

async function remove(s: Installed): Promise<void> {
  try {
    await api(`/api/specs/installed/${encodeURIComponent(s.id)}`, { method: 'DELETE' });
    ui.toast(`Removed ${s.summary.name || s.id}.`);
    await Promise.all([load(), loadCatalog(true)]);
  } catch (err) {
    ui.toast(err instanceof ApiError ? err.message : 'That didn’t work.', 'error');
  }
}
</script>

<template>
  <section class="st-section" :aria-labelledby="`${uid}-h`">
    <header>
      <h3 :id="`${uid}-h`">Installed specs</h3>
      <UiButton v-if="!adding" size="sm" @click="startAdd">Add from a spec file</UiButton>
    </header>

    <div v-if="adding" class="is-add">
      <p class="m">
        A spec is a YAML file in the <span class="st-mono">chadash/v1alpha1</span> format, from the community repository or your own. Check it first: Cha Dash
        shows what it reads and what it could change.
      </p>
      <div class="st-acts">
        <UiButton size="sm" :disabled="busy" @click="picker?.click()">Choose a file</UiButton>
        <input ref="picker" class="sr" type="file" accept=".yaml,.yml,text/yaml,application/yaml,text/plain" tabindex="-1" aria-hidden="true" @change="onPick" />
      </div>
      <UiField :id="`${uid}-yaml`" label="Spec YAML" v-slot="{ control }">
        <textarea
          v-bind="control"
          :value="yaml"
          rows="10"
          placeholder="apiVersion: chadash/v1alpha1&#10;kind: Integration&#10;…"
          spellcheck="false"
          autocapitalize="off"
          autocomplete="off"
          :disabled="busy"
          @input="edited(($event.target as HTMLTextAreaElement).value)"
        ></textarea>
      </UiField>
      <UiField :id="`${uid}-src`" label="Where it came from" help="Optional: a link or a note, kept with the spec." v-slot="{ control }">
        <input v-bind="control" v-model="source" type="text" maxlength="512" autocomplete="off" spellcheck="false" :disabled="busy" />
      </UiField>
      <div v-if="error" class="st-note bad" role="alert">
        <Glyph kind="down" />
        <span>
          {{ error }}
          <ul v-if="errorDetails.length" class="is-details">
            <li v-for="d in errorDetails" :key="d">{{ d }}</li>
          </ul>
        </span>
      </div>

      <div v-if="preview" class="is-preview" role="region" :aria-label="`What ${preview.summary.name} reads and adds`">
        <p v-if="exists" class="st-note" role="status">
          <Glyph kind="attn" /><span>{{ preview.summary.name }} is installed already ({{ preview.replacing?.summary.version }}). Replace it with {{ preview.summary.version }}: its instances restart, and changed actions turn off again.</span>
        </p>
        <h4>{{ preview.summary.name }} {{ preview.summary.version }} reads</h4>
        <ul class="is-list">
          <li v-for="r in reads" :key="r.name">
            <span class="st-mono">{{ r.method }} {{ r.path }}</span><template v-if="r.optional"> · optional</template>
            <template v-if="r.secrets?.length"> · sends <span class="st-mono">{{ r.secrets.join(', ') }}</span></template>
          </li>
        </ul>
        <template v-if="posts.length">
          <h4>and sends {{ posts.length }} POST request{{ posts.length === 1 ? '' : 's' }} that could change things, which wait{{ posts.length === 1 ? 's' : '' }} for its actions to be on</h4>
          <ul class="is-list">
            <li v-for="r in posts" :key="r.name">
              <span class="st-mono">{{ r.method }} {{ r.path }}</span><template v-if="r.optional"> · optional</template>
              <template v-if="r.secrets?.length"> · sends <span class="st-mono">{{ r.secrets.join(', ') }}</span></template>
              <template v-if="r.body"> · body <code class="st-mono">{{ r.body }}</code></template>
            </li>
          </ul>
        </template>
        <template v-if="preview.summary.actions.length">
          <h4>and adds {{ preview.summary.actions.length }} action{{ preview.summary.actions.length === 1 ? '' : 's' }}, off until you turn them on</h4>
          <ul class="is-list">
            <li v-for="a in preview.summary.actions" :key="a.id">
              {{ a.title }} <span class="chip">{{ a.tier }}</span> · <span class="st-mono">{{ a.method }} {{ a.path }}</span>
              <template v-if="a.body?.length"> · sends <span class="st-mono">{{ a.body.join(', ') }}</span></template>
              <template v-if="a.household"> · <strong>household members can run it</strong></template>
            </li>
          </ul>
        </template>
        <p v-else class="m">It adds no actions: it only reads.</p>
      </div>

      <div class="st-acts">
        <UiButton v-if="!preview" variant="primary" size="sm" :disabled="busy || !yaml.trim()" @click="check">Check</UiButton>
        <UiButton v-else variant="primary" size="sm" :disabled="busy" @click="install">{{ exists ? 'Replace' : 'Install' }}</UiButton>
        <UiButton variant="ghost" size="sm" :disabled="busy" @click="adding = false">Cancel</UiButton>
      </div>
    </div>

    <div v-if="status === 'loading'" class="st-skel" aria-busy="true"><span class="skel"></span></div>
    <div v-else-if="status === 'error'" class="st-note bad" role="alert">
      <Glyph kind="down" /><span>{{ loadError }}</span>
      <UiButton size="sm" @click="load">Try again</UiButton>
    </div>
    <p v-else-if="!specs.length && !adding" class="m is-empty">None yet. Specs you add show here and on the Add list, marked installed.</p>
    <ul v-else-if="specs.length" class="st-list">
      <li v-for="s in specs" :key="s.id">
        <div class="st-row is-row">
          <Glyph :kind="s.error ? 'down' : 'ok'" />
          <span>
            <span class="t">{{ s.summary.name || s.id }} <span class="m">{{ s.summary.version }}</span></span>
            <span class="m">
              {{ s.instances }} instance{{ s.instances === 1 ? '' : 's' }} · installed {{ ago(s.updated_at, now) }}<template v-if="s.installed_by"> by {{ s.installed_by }}</template>
              <template v-if="s.source"> · {{ s.source }}</template>
            </span>
            <span v-if="s.error" class="m is-why">{{ s.error }}</span>
            <span v-if="s.summary.actions.length" class="m">
              {{ s.actions_on ? 'Actions on' : 'Actions off' }}:
              <template v-for="(a, i) in s.summary.actions" :key="a.id">{{ i ? ', ' : '' }}{{ a.title }} ({{ a.tier }}, <span class="st-mono">{{ a.method }} {{ a.path }}</span><template v-if="a.household">, household too</template>)</template>
            </span>
          </span>
          <span class="end is-acts">
            <template v-if="s.summary.actions.length">
              <UiButton v-if="s.actions_on" size="sm" :aria-label="`Turn ${s.summary.name}'s actions off`" @click="setActions(s, false)">Turn actions off</UiButton>
              <UiButton v-else size="sm" :aria-label="`Turn ${s.summary.name}'s actions on`" @click="setActions(s, true)">Turn actions on</UiButton>
            </template>
            <UiButton
              size="sm"
              variant="ghost"
              :disabled="s.instances > 0"
              :title="s.instances > 0 ? 'Delete its instances first.' : undefined"
              :aria-label="`Remove ${s.summary.name || s.id}`"
              @click="remove(s)"
              >Remove</UiButton
            >
          </span>
        </div>
      </li>
    </ul>
  </section>
</template>

<style>
.is-add {
  display: grid;
  gap: var(--space-2);
  margin-bottom: var(--space-3);
}
.is-add textarea {
  font-family: var(--font-num);
  min-height: 12rem;
}
.is-preview h4 {
  margin: var(--space-2) 0 var(--space-1);
}
.is-list,
.is-details {
  margin: 0;
  padding-left: 1.2em;
}
.is-row {
  grid-template-columns: auto minmax(0, 1fr) auto;
}
.is-row .m {
  display: block;
}
.is-acts {
  display: flex;
  flex-wrap: wrap;
  gap: 6px;
  justify-content: end;
}
.is-why {
  color: var(--text-2);
}
.is-empty {
  margin: 0;
}
</style>
