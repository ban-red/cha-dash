<script setup lang="ts">
/**
 * Settings → Import from Homepage. Paste or upload services.yaml (and,
 * optionally, docker.yaml and settings.yaml), preview the plan the hub makes
 * from them (a dry run: nothing changes), then apply it in one step and land
 * on the new board.
 *
 * The YAML is never parsed here; the hub does it. Secrets in those files go
 * to the hub once, over this same-origin request, and come back only as a
 * count and field names.
 */
import { computed, reactive, ref } from 'vue';
import Glyph from '../components/marks/Glyph.vue';
import { api } from '../lib/api';
import { navigate } from '../lib/router';
import { previewOf, type ApplyResult, type ImportFiles, type ImportPreview } from '../model/importPlan';
import { ApiError } from '../lib/api';
import { errorWords, loadInstances } from '../stores/integrations';
import { ensureTileMeta } from '../stores/tiles';
import { useUi } from '../stores/ui';

const ui = useUi();

type Slot = keyof ImportFiles;
const SLOTS: { key: Slot; file: string; what: string; required: boolean }[] = [
  { key: 'services_yaml', file: 'services.yaml', what: 'Services, groups and their widgets', required: true },
  { key: 'docker_yaml', file: 'docker.yaml', what: 'Docker hosts the services refer to', required: false },
  { key: 'settings_yaml', file: 'settings.yaml', what: 'Title, layout and provider keys', required: false },
];

const files = reactive<ImportFiles>({ services_yaml: '', docker_yaml: '', settings_yaml: '' });
const names = reactive<Record<Slot, string>>({ services_yaml: '', docker_yaml: '', settings_yaml: '' });
const step = ref<'edit' | 'previewing' | 'preview' | 'applying'>('edit');
const preview = ref<ImportPreview | undefined>(undefined);
const error = ref('');
const dragging = ref<Slot | ''>('');
const pickers = ref<Partial<Record<Slot, HTMLInputElement>>>({});

/** The hub takes at most 1 MiB for the three files together. */
const MAX_BYTES = 1024 * 1024;
const ready = computed(() => files.services_yaml.trim() !== '');
const size = (s: string) => {
  const b = new Blob([s]).size;
  return b < 1024 ? `${b} B` : `${(b / 1024).toFixed(b < 10_240 ? 1 : 0)} kB`;
};
const lines = (s: string) => (s ? s.split('\n').length : 0);

function edited(key: Slot, text: string): void {
  files[key] = text;
  if (step.value === 'preview') step.value = 'edit';
  error.value = '';
}

async function readFile(key: Slot, file: File | undefined): Promise<void> {
  if (!file) return;
  if (file.size > MAX_BYTES) {
    error.value = `${file.name} is larger than 1 MB; that’s not a Homepage config.`;
    return;
  }
  try {
    edited(key, await file.text());
    names[key] = file.name;
  } catch {
    error.value = `Couldn’t read ${file.name}.`;
  }
}

function onPick(key: Slot, e: Event): void {
  const input = e.target as HTMLInputElement;
  void readFile(key, input.files?.[0]);
  input.value = '';
}

function onDrop(key: Slot, e: DragEvent): void {
  dragging.value = '';
  void readFile(key, e.dataTransfer?.files?.[0]);
}

function clear(key: Slot): void {
  edited(key, '');
  names[key] = '';
}

async function runPreview(): Promise<void> {
  if (!ready.value) {
    error.value = 'Paste or upload services.yaml first. The other two are optional.';
    return;
  }
  error.value = '';
  step.value = 'previewing';
  try {
    preview.value = previewOf(await api('/api/import/homepage?dry_run=1', { method: 'POST', body: { ...files } }));
    step.value = 'preview';
  } catch (err) {
    step.value = 'edit';
    error.value = importError(err, 'The hub couldn’t read those files.');
  }
}

/** 422 names the file and line (never the text there): show it as is. */
function importError(err: unknown, fallback: string): string {
  if (err instanceof ApiError) {
    if (err.status === 413) return 'Those files are larger than 1 MB together; that’s not a Homepage config.';
    if (err.status === 415) return 'The hub expected JSON. Reload the page and try again.';
  }
  return errorWords(err, fallback);
}

async function apply(): Promise<void> {
  step.value = 'applying';
  error.value = '';
  try {
    const res = await api<ApplyResult>('/api/import/homepage', { method: 'POST', body: { ...files } });
    const p = preview.value;
    ui.toast(
      `Imported from Homepage: ${p ? `${p.tileCount} tiles and ${p.instances.length} connections` : 'a new board'}.${p?.secretsMoved ? ` ${p.secretsMoved} secrets are in the vault.` : ''}`,
    );
    for (const k of Object.keys(files) as Slot[]) files[k] = '';
    void loadInstances(true);
    void ensureTileMeta(true);
    navigate(res?.board_id ? `/boards/${encodeURIComponent(res.board_id)}` : '/');
  } catch (err) {
    step.value = 'preview';
    error.value = importError(err, 'The import didn’t apply. Nothing changed.');
  }
}

const sevGlyph = (s: string) => (s === 'error' ? 'down' : s === 'info' ? 'ok' : 'attn');
</script>

<template>
  <section class="st-section" aria-labelledby="src-h">
    <header><h3 id="src-h">Source</h3></header>
    <div class="seg import-src" role="group" aria-label="Import source">
      <button type="button" aria-pressed="true">Homepage</button>
      <button type="button" aria-pressed="false" disabled title="Arrives in v0.2">Homarr<span class="n">v0.2</span></button>
      <button type="button" aria-pressed="false" disabled title="Arrives in v0.2">Plain YAML<span class="n">v0.2</span></button>
    </div>
    <p class="st-lede">Each importer is named after what it reads, and every one previews the plan before anything changes. Homepage’s secrets move into Cha Dash’s encrypted vault; their values are never shown again.</p>
  </section>

  <section class="st-section" aria-labelledby="files-h">
    <header>
      <h3 id="files-h">Files</h3>
      <span class="st-meta">homepage › config › services.yaml · docker.yaml · settings.yaml</span>
    </header>
    <div class="import-files">
      <div
        v-for="s in SLOTS"
        :key="s.key"
        :class="['import-file', { drag: dragging === s.key, filled: files[s.key] }]"
        @dragover.prevent="dragging = s.key"
        @dragleave="dragging = ''"
        @drop.prevent="onDrop(s.key, $event)"
      >
        <div class="if-head">
          <label :for="`yaml-${s.key}`" class="if-name">{{ s.file }}</label>
          <span class="if-what">{{ s.what }}{{ s.required ? '' : ' · optional' }}</span>
          <span class="if-acts">
            <button type="button" class="btn sm" :disabled="step === 'previewing' || step === 'applying'" @click="pickers[s.key]?.click()">Upload…</button>
            <button v-if="files[s.key]" type="button" class="btn sm ghost" :disabled="step === 'applying'" @click="clear(s.key)">Clear</button>
          </span>
          <input
            :ref="(el) => (pickers[s.key] = el as HTMLInputElement)"
            class="sr"
            type="file"
            accept=".yaml,.yml,text/yaml,application/yaml,text/plain"
            tabindex="-1"
            aria-hidden="true"
            @change="onPick(s.key, $event)"
          />
        </div>
        <div class="field">
          <textarea
            :id="`yaml-${s.key}`"
            :value="files[s.key]"
            :placeholder="s.key === 'services_yaml' ? '- Media:\n    - Plex:\n        href: https://plex.home.arpa\n        widget:\n          type: plex' : 'Paste it here, or drop the file on this box'"
            spellcheck="false"
            autocapitalize="off"
            autocomplete="off"
            :disabled="step === 'previewing' || step === 'applying'"
            :aria-describedby="`yaml-${s.key}-meta`"
            @input="edited(s.key, ($event.target as HTMLTextAreaElement).value)"
          ></textarea>
          <span :id="`yaml-${s.key}-meta`" class="help">
            <template v-if="files[s.key]">{{ names[s.key] || 'Pasted' }} · <span class="n">{{ lines(files[s.key]) }}</span> lines · <span class="n">{{ size(files[s.key]) }}</span></template>
            <template v-else>{{ s.required ? 'Required.' : 'Leave empty if you don’t use it.' }}</template>
          </span>
        </div>
      </div>
    </div>
    <p v-if="error" class="err" role="alert">{{ error }}</p>
    <div class="st-acts">
      <button type="button" class="btn primary" :disabled="step === 'previewing' || step === 'applying'" :aria-busy="step === 'previewing'" @click="runPreview">
        {{ step === 'previewing' ? 'Reading…' : step === 'preview' ? 'Preview again' : 'Preview import' }}
      </button>
      <span class="st-meta">A dry run: nothing changes until you apply.</span>
    </div>
  </section>

  <section v-if="preview && (step === 'preview' || step === 'applying')" class="st-section import-preview" aria-labelledby="plan-h">
    <header><h3 id="plan-h">The plan</h3><span class="st-meta">{{ preview.boardName }}</span></header>
    <div v-if="preview.nothingNew" class="st-note" role="status">
      <Glyph kind="check" /><span>These files were imported already: applying changes nothing ({{ preview.changes.unchanged }} unchanged).</span>
    </div>
    <div class="plan-grid">
      <div>
        <h4>What comes in</h4>
        <dl class="kv">
          <dt>Sections</dt>
          <dd>{{ preview.sections.length }}</dd>
          <dt>Services → tiles</dt>
          <dd>{{ preview.tileCount }}</dd>
          <dt>Connections</dt>
          <dd>{{ preview.instances.length }}</dd>
          <dt>Checks</dt>
          <dd>{{ preview.checks }}</dd>
          <dt>Secrets → vault</dt>
          <dd>{{ preview.secretsMoved }}</dd>
          <dt>Not imported</dt>
          <dd>{{ preview.unmapped.length + preview.ignored.length }}</dd>
        </dl>
        <template v-if="preview.changes.update || preview.changes.unchanged || preview.changes.stale">
          <h4>Against what’s here</h4>
          <dl class="kv">
            <dt>New</dt>
            <dd>{{ preview.changes.create }}</dd>
            <dt>Updated</dt>
            <dd>{{ preview.changes.update }}</dd>
            <dt>Unchanged</dt>
            <dd>{{ preview.changes.unchanged }}</dd>
            <template v-if="preview.changes.stale">
              <dt>No longer in the files</dt>
              <dd>{{ preview.changes.stale }}</dd>
            </template>
          </dl>
          <p v-if="preview.stale.length" class="sub">Kept, not deleted: {{ preview.stale.join(', ') }}. Remove them in Integrations if you don’t need them.</p>
        </template>
        <h4>Secrets</h4>
        <p class="sub">
          <template v-if="preview.secretsMoved">{{ preview.secretsMoved }} credential{{ preview.secretsMoved === 1 ? '' : 's' }} move into the encrypted vault and become references. Their values are never shown, logged or exported.</template>
          <template v-else>No credentials in these files.</template>
        </p>
      </div>
      <div>
        <h4>Worth knowing</h4>
        <ul v-if="preview.hints.length || preview.notes.length" class="rel">
          <li v-for="(h, i) in preview.hints" :key="'h' + i">
            <span><Glyph :kind="sevGlyph(h.severity)" /> {{ h.message }}</span><span>{{ h.inbox ? 'to the Inbox' : 'hint' }}</span>
          </li>
          <li v-for="(n, i) in preview.notes" :key="'n' + i">
            <span><Glyph kind="ok" /> {{ n }}</span><span>note</span>
          </li>
        </ul>
        <p v-else class="sub">Nothing to flag.</p>
      </div>
    </div>

    <div v-if="preview.sections.length" class="plan-block">
      <h4>Sections and tiles</h4>
      <ul class="plan-sections">
        <li v-for="s in preview.sections" :key="s.id">
          <span class="ps-title">{{ s.title }} <span class="n">{{ s.tiles.length }}</span></span>
          <span class="ps-tiles">
            <span v-for="t in s.tiles" :key="t.id" class="chip" :title="t.widget"><Glyph v-if="t.live" kind="ok" />{{ t.label }}</span>
            <span v-if="!s.tiles.length" class="sub">Empty for now; fills in through discovery.</span>
          </span>
        </li>
      </ul>
    </div>

    <div v-if="preview.instances.length" class="plan-block">
      <h4>Connections</h4>
      <ul class="rel">
        <li v-for="x in preview.instances" :key="x.id || x.integration + x.name">
          <span>
            <b>{{ x.name }}</b> · {{ x.integration }}<template v-if="x.url"> · <span class="n">{{ x.url }}</span></template>
            <span v-if="x.missingSecrets.length" class="missing"><Glyph kind="attn" /> Set {{ x.missingSecrets.join(' and ') }} in Cha Dash after importing: the files only name a variable.</span>
          </span>
          <span>{{ x.secretFields.length ? `${x.secretFields.join(', ')} → vault` : 'no secrets' }}</span>
        </li>
      </ul>
    </div>

    <div v-if="preview.discovery.length" class="plan-block">
      <h4>Fills in later</h4>
      <ul class="rel">
        <li v-for="(d, i) in preview.discovery" :key="i"><span>{{ d }}</span><span>discovery</span></li>
      </ul>
    </div>

    <div v-if="preview.unmapped.length || preview.ignored.length" class="plan-block">
      <h4>Not imported · {{ preview.unmapped.length + preview.ignored.length }}</h4>
      <ul class="rel">
        <li v-for="(u, i) in preview.unmapped" :key="'u' + i">
          <span>
            <b>{{ u.name }}</b><template v-if="u.where"> · {{ u.where }}</template> · {{ u.reason ?? 'no mapping yet' }}
            <template v-if="u.fields.length"> (<span class="n">{{ u.fields.join(', ') }}</span>)</template>
          </span>
          <span>{{ u.kind }}</span>
        </li>
        <li v-for="(g, i) in preview.ignored" :key="'g' + i">
          <span><span class="n">{{ g.what }}</span> · {{ g.reason }}</span><span>setting</span>
        </li>
      </ul>
    </div>

    <div class="plan-apply">
      <p class="sub">Creates the board and its connections in one step; your current board stays as it is. Importing the same files again updates what changed and never duplicates.</p>
      <div class="st-acts">
        <button type="button" class="btn primary" :disabled="step === 'applying'" :aria-busy="step === 'applying'" @click="apply">
          {{ step === 'applying' ? 'Importing…' : preview.nothingNew ? 'Import anyway' : `Import ${preview.tileCount} tiles` }}<span class="tier">T1</span>
        </button>
        <button type="button" class="btn" :disabled="step === 'applying'" @click="step = 'edit'">Edit files</button>
      </div>
    </div>
  </section>
</template>

<style>
.import-src {
  max-width: 520px;
}
.import-files {
  display: flex;
  flex-direction: column;
  gap: var(--space-3);
}
.import-file {
  display: flex;
  flex-direction: column;
  gap: var(--space-2);
  padding: var(--space-3);
  border: var(--hairline) dashed var(--line-2);
  border-radius: var(--radius-md);
  transition:
    border-color var(--motion-press-duration) var(--motion-press-easing),
    background-color var(--motion-press-duration) var(--motion-press-easing);
}
.import-file.filled {
  border-style: solid;
}
.import-file.drag {
  border-color: var(--text-2);
  background: color-mix(in oklch, var(--text) 4%, transparent);
}
.if-head {
  display: flex;
  flex-wrap: wrap;
  align-items: baseline;
  gap: 4px var(--space-3);
}
.if-name {
  font: var(--weight-medium) var(--type-body) / 1.2 var(--font-num);
  color: var(--text);
}
.if-what {
  font-size: var(--type-small);
  color: var(--text-3);
}
.if-acts {
  margin-left: auto;
  display: inline-flex;
  gap: var(--space-2);
}
.import-file textarea {
  min-height: 7.5rem;
}
.plan-grid {
  display: grid;
  grid-template-columns: repeat(auto-fit, minmax(240px, 1fr));
  gap: var(--space-6);
}
.import-preview h4 {
  margin: 0 0 var(--space-2);
  font: var(--weight-medium) var(--type-micro) / 1 var(--font-num);
  letter-spacing: var(--tracking-caps);
  text-transform: uppercase;
  color: var(--text-3);
}
.import-preview .kv + h4 {
  margin-top: var(--space-5);
}
.plan-block {
  margin-top: var(--space-2);
}
.plan-sections {
  list-style: none;
  margin: 0;
  padding: 0;
}
.plan-sections li {
  display: grid;
  grid-template-columns: minmax(8rem, 12rem) minmax(0, 1fr);
  gap: var(--space-3);
  padding: 8px 0;
  border-top: var(--hairline) solid var(--line);
  font-size: var(--type-small);
}
.ps-title {
  color: var(--text);
  font-weight: var(--weight-medium);
}
.ps-title .n {
  margin-left: 4px;
  color: var(--text-3);
  font-weight: var(--weight-regular);
}
.ps-tiles {
  display: flex;
  flex-wrap: wrap;
  gap: 4px;
}
.import-preview .missing {
  display: block;
  margin-top: 2px;
  color: var(--text);
}
.ps-tiles .chip .g {
  width: 8px;
  height: 8px;
}
.plan-apply {
  display: flex;
  flex-direction: column;
  gap: var(--space-3);
  padding-top: var(--space-4);
  border-top: var(--hairline) solid var(--line);
}
@media (max-width: 539px) {
  .plan-sections li {
    grid-template-columns: minmax(0, 1fr);
  }
}
</style>
