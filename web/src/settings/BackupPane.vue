<script setup lang="ts">
/**
 * Settings → Backup: this hub as one Home file (YAML), and a Home file back
 * into this hub, previewed field by field first (internal/export; the
 * format is in its README).
 *
 *   GET  /api/export                     the file, secrets as placeholders
 *   POST /api/export {passphrase}        the same with the encrypted secrets bundle
 *   POST /api/import/chadash?dry_run=1   {yaml, passphrase?} → {plan}
 *   POST /api/import/chadash             the same → {plan, changed, board_ids, instance_ids}
 *
 * Cha Dash's own YAML lives here rather than in Import's source picker: the
 * importers move another dashboard's config in (one board, secrets pulled out
 * of its files), while a Home file is this hub's own state going out and
 * back, with an encrypted bundle, stale objects and a field-level diff. It's
 * the other half of Export, so it sits next to it.
 *
 * Its own chunk: SettingsSheet loads it when the pane opens. The YAML is never
 * parsed here; the hub does it. Passphrases are held only until the request
 * that needs them, then cleared; secret values never come back from the hub.
 */
import { computed, reactive, ref, useTemplateRef, watch } from 'vue';
import Glyph from '../ui/marks/Glyph.vue';
import { ApiError, api } from '../lib/api';
import { absolute } from '../lib/format';
import { navigate, settingsPath } from '../lib/router';
import {
  ACTION_WORDS,
  backupPreviewOf,
  hasBundle,
  secretWords,
  type BackupApplied,
  type BackupPreview,
  type FieldRow,
} from '../model/backupPlan';
import { useBoard } from '../stores/board';
import { errorWords, loadInstances } from '../stores/integrations';
import { useUi } from '../stores/ui';
import UiButton from '../ui/Button.vue';
import UiField from '../ui/Field.vue';

const ui = useUi();
const board = useBoard();

/** The hub refuses passphrases shorter than this (its password rule). */
const MIN_PASSPHRASE = 12;
/** A Home file is at most 4 MiB. */
const MAX_BYTES = 4 * 1024 * 1024;

// ───────────── Export ─────────────

const out = reactive({ withSecrets: false, pass: '', again: '', busy: false, error: '', saved: '' });
const passShort = computed(() => out.withSecrets && out.pass !== '' && [...out.pass].length < MIN_PASSPHRASE);
const passMismatch = computed(() => out.withSecrets && out.again !== '' && out.again !== out.pass);
// Too short turns the same words red rather than replacing them.
const passHelp = `At least ${MIN_PASSPHRASE} characters. Cha Dash doesn’t keep it: without it, the secrets in this file can’t be read back by anyone.`;
// Unticking drops a passphrase typed so far: it's held only for the request that needs it.
watch(
  () => out.withSecrets,
  (on) => {
    if (!on) out.pass = out.again = '';
  },
);

function filenameOf(res: Response, fallback: string): string {
  const m = /filename="([^"]+)"/.exec(res.headers.get('Content-Disposition') ?? '');
  return m?.[1] ?? fallback;
}

/** Fetch the file and hand it to the browser as a download (a blob URL: no data: URIs under the CSP). */
async function download(): Promise<void> {
  out.error = '';
  out.saved = '';
  if (out.withSecrets) {
    if ([...out.pass].length < MIN_PASSPHRASE) {
      out.error = `The passphrase needs at least ${MIN_PASSPHRASE} characters.`;
      return;
    }
    if (out.again !== out.pass) {
      out.error = 'The two passphrases differ.';
      return;
    }
  }
  if (typeof URL.createObjectURL !== 'function') {
    out.error = 'This browser can’t save files from the page. Use chadash export on the hub instead.';
    return;
  }
  out.busy = true;
  try {
    const init: RequestInit = { credentials: 'same-origin', headers: { Accept: 'application/yaml, application/json' } };
    if (out.withSecrets) {
      init.method = 'POST';
      init.headers = { Accept: 'application/yaml, application/json', 'Content-Type': 'application/json' };
      init.body = JSON.stringify({ passphrase: out.pass });
    }
    let res: Response;
    try {
      res = await fetch('/api/export', init);
    } catch {
      throw new ApiError(0, 'network', "Can't reach the Cha Dash hub.");
    }
    if (!res.ok) {
      let body: { error?: { code?: string; message?: string } } | undefined;
      try {
        body = (await res.json()) as typeof body;
      } catch {
        body = undefined;
      }
      throw new ApiError(res.status, body?.error?.code ?? `http_${res.status}`, body?.error?.message ?? res.statusText);
    }
    const name = filenameOf(res, 'chadash-home.yaml');
    const url = URL.createObjectURL(await res.blob());
    const a = document.createElement('a');
    a.href = url;
    a.download = name;
    a.rel = 'noopener';
    document.body.append(a);
    a.click();
    a.remove();
    setTimeout(() => URL.revokeObjectURL(url), 1000);
    out.saved = name;
    out.pass = '';
    out.again = '';
  } catch (err) {
    out.error =
      err instanceof ApiError && err.code === 'secret_in_plain_text'
        ? `${errorWords(err)} Nothing was saved.`
        : errorWords(err, 'The hub couldn’t write the export.');
  } finally {
    out.busy = false;
  }
}

// ───────────── Import ─────────────

const yaml = ref('');
const fileName = ref('');
const pass = ref('');
const step = ref<'edit' | 'previewing' | 'preview' | 'applying'>('edit');
const preview = ref<BackupPreview | undefined>(undefined);
const error = ref('');
const passError = ref('');
const dragging = ref(false);
const done = ref<BackupPreview | undefined>(undefined);
const picker = useTemplateRef<HTMLInputElement>('picker');

const bundled = computed(() => hasBundle(yaml.value));
const busy = computed(() => step.value === 'previewing' || step.value === 'applying');
const sizeText = computed(() => {
  const b = new Blob([yaml.value]).size;
  return b < 1024 ? `${b} B` : b < 1024 * 1024 ? `${(b / 1024).toFixed(b < 10_240 ? 1 : 0)} kB` : `${(b / 1024 / 1024).toFixed(1)} MB`;
});
const changes = computed(() => (preview.value ? preview.value.summary.create + preview.value.summary.update : 0));

function edited(text: string): void {
  yaml.value = text;
  if (step.value === 'preview') step.value = 'edit';
  error.value = '';
  passError.value = '';
  done.value = undefined;
}

async function readFile(file: File | undefined): Promise<void> {
  if (!file) return;
  if (file.size > MAX_BYTES) {
    error.value = `${file.name} is larger than 4 MB; that isn’t a Home file.`;
    return;
  }
  try {
    edited(await file.text());
    fileName.value = file.name;
  } catch {
    error.value = `Couldn’t read ${file.name}.`;
  }
}

function onPick(e: Event): void {
  const input = e.target as HTMLInputElement;
  void readFile(input.files?.[0]);
  input.value = '';
}

function onDrop(e: DragEvent): void {
  dragging.value = false;
  void readFile(e.dataTransfer?.files?.[0]);
}

function clearFile(): void {
  edited('');
  fileName.value = '';
  pass.value = '';
}

function body(): { yaml: string; passphrase?: string } {
  return pass.value && bundled.value ? { yaml: yaml.value, passphrase: pass.value } : { yaml: yaml.value };
}

/** Errors in words; a wrong passphrase is shown at its field. */
function importError(err: unknown, fallback: string): string {
  if (err instanceof ApiError) {
    if (err.code === 'wrong_passphrase') {
      passError.value = 'That passphrase didn’t open the secrets. Check it, or clear it to import without them.';
      return '';
    }
    if (err.status === 413) return 'That file is larger than the hub accepts; it isn’t a Home file.';
    if (err.status === 415) return 'The hub expected JSON. Reload the page and try again.';
  }
  return errorWords(err, fallback);
}

async function runPreview(): Promise<void> {
  if (!yaml.value.trim()) {
    error.value = 'Choose a Home file (from Export, or chadash export) first.';
    return;
  }
  error.value = '';
  passError.value = '';
  done.value = undefined;
  step.value = 'previewing';
  try {
    preview.value = backupPreviewOf(await api('/api/import/chadash?dry_run=1', { method: 'POST', body: body() }));
    step.value = 'preview';
  } catch (err) {
    step.value = 'edit';
    error.value = importError(err, 'The hub couldn’t read that file.');
  }
}

async function apply(): Promise<void> {
  step.value = 'applying';
  error.value = '';
  passError.value = '';
  try {
    const res = await api<BackupApplied>('/api/import/chadash', { method: 'POST', body: body() });
    const result = backupPreviewOf(res);
    const s = result.summary;
    const parts = [`${s.create} new`, `${s.update} updated`];
    let text = `Imported: ${parts.join(', ')}.`;
    if (s.secretsRestored) text += ` ${s.secretsRestored} secret${s.secretsRestored === 1 ? '' : 's'} restored.`;
    if (s.secretsNeeded) text += ` ${s.secretsNeeded} still needed.`;
    ui.toast(text);
    done.value = result;
    preview.value = undefined;
    yaml.value = '';
    fileName.value = '';
    pass.value = '';
    step.value = 'edit';
    void loadInstances(true);
    // Boards and tiles may have changed under the page: reload the one on screen.
    if (board.state.boardId) void board.load(board.state.boardId);
  } catch (err) {
    step.value = 'preview';
    error.value = importError(err, 'The import didn’t apply. Nothing changed.');
  }
}

const OP_MARK: Record<FieldRow['op'], string> = { add: '+', replace: '~', remove: '−', reorder: '⇅' };
const OP_WORD: Record<FieldRow['op'], string> = { add: 'added', replace: 'changed', remove: 'removed', reorder: 'reordered' };

</script>

<template>
  <section class="st-section" aria-labelledby="bk-out-h">
    <header><h3 id="bk-out-h">Export</h3></header>
    <p class="st-lede">
      Everything Cha Dash needs to rebuild this home, as one YAML file: boards, layouts and tiles, connections and their settings. Secrets stay out: each one is a
      <code>!secret</code> placeholder naming what it needs. Users, sessions, passkeys and the audit log are never included.
    </p>
    <form class="st-form bk-out" @submit.prevent="download">
      <label class="check-row">
        <input v-model="out.withSecrets" type="checkbox" :disabled="out.busy" />
        <span>Include secrets, encrypted with a passphrase</span>
      </label>
      <template v-if="out.withSecrets">
        <UiField id="bk-pass" label="Passphrase" :help="passHelp" :error="passShort && passHelp" v-slot="{ control }">
          <input v-bind="control" v-model="out.pass" type="password" autocomplete="new-password" spellcheck="false" :disabled="out.busy" />
        </UiField>
        <UiField id="bk-pass2" label="The same passphrase again" :error="passMismatch && 'The two passphrases differ.'" v-slot="{ control }">
          <input v-bind="control" v-model="out.again" type="password" autocomplete="new-password" spellcheck="false" :disabled="out.busy" />
        </UiField>
      </template>
      <p v-if="out.error" class="err" role="alert">{{ out.error }}</p>
      <p v-else-if="out.saved" class="st-note" role="status"><Glyph kind="check" /><span>Saved {{ out.saved }}. Keep it somewhere safe; it describes your whole home.</span></p>
      <div class="st-acts">
        <UiButton variant="primary" type="submit" :disabled="out.busy" :aria-busy="out.busy">
          {{ out.busy ? 'Exporting…' : out.withSecrets ? 'Download with secrets' : 'Download export' }}
        </UiButton>
        <span class="st-meta">Also on the hub: <code>chadash export</code></span>
      </div>
    </form>
  </section>

  <section class="st-section" aria-labelledby="bk-in-h">
    <header><h3 id="bk-in-h">Restore or import</h3></header>
    <p class="st-lede">
      Bring in a Home file from this hub or another. You’ll see every change first. Nothing is deleted, and importing the same file again changes nothing.
    </p>
    <div
      :class="['import-file', 'bk-file', { drag: dragging, filled: yaml }]"
      @dragover.prevent="dragging = true"
      @dragleave="dragging = false"
      @drop.prevent="onDrop"
    >
      <div class="if-head">
        <span class="if-name">{{ fileName || (yaml ? 'Pasted' : 'Home file') }}</span>
        <span class="if-what">
          <template v-if="yaml"><span class="n">{{ sizeText }}</span><template v-if="bundled"> · has encrypted secrets</template></template>
          <template v-else>Drop it here, or choose it</template>
        </span>
        <span class="if-acts">
          <UiButton size="sm" :disabled="busy" @click="picker?.click()">Choose file…</UiButton>
          <UiButton v-if="yaml" variant="ghost" size="sm" :disabled="busy" @click="clearFile">Clear</UiButton>
        </span>
        <input ref="picker" class="sr" type="file" accept=".yaml,.yml,text/yaml,application/yaml,text/plain" tabindex="-1" aria-hidden="true" @change="onPick" />
      </div>
      <details class="bk-paste" :open="!fileName && yaml !== ''">
        <summary>Or paste the YAML</summary>
        <UiField id="bk-yaml" label="Home file YAML" hide-label v-slot="{ control }">
          <textarea
            v-bind="control"
            :value="yaml"
            placeholder="apiVersion: chadash/v1alpha1&#10;kind: Home&#10;…"
            spellcheck="false"
            autocapitalize="off"
            autocomplete="off"
            :disabled="busy"
            @input="edited(($event.target as HTMLTextAreaElement).value)"
          ></textarea>
        </UiField>
      </details>
    </div>
    <UiField v-if="bundled" id="bk-unlock" class="bk-unlock" label="Passphrase for the secrets" help="This file carries its secrets, encrypted. Enter its passphrase to restore them, or leave it empty to import without them." :error="passError" alert v-slot="{ control }">
      <input v-bind="control" v-model="pass" type="password" autocomplete="off" spellcheck="false" :disabled="busy" @input="passError = ''" />
    </UiField>
    <p v-if="error" class="err" role="alert">{{ error }}</p>
    <div class="st-acts">
      <UiButton variant="primary" :disabled="busy" :aria-busy="step === 'previewing'" @click="runPreview">
        {{ step === 'previewing' ? 'Reading…' : step === 'preview' ? 'Preview again' : 'Preview import' }}
      </UiButton>
      <span class="st-meta">A dry run: nothing changes until you apply.</span>
    </div>
    <div v-if="done" class="st-note" role="status">
      <Glyph kind="check" />
      <span>
        Imported.
        <template v-if="done.needed.length">
          {{ done.needed.length }} secret{{ done.needed.length === 1 ? ' is' : 's are' }} still needed: each connection waits with a Repair until you enter it in
          <a :href="settingsPath('integrations')" @click.prevent="navigate(settingsPath('integrations'), { replace: true })">Integrations</a>.
        </template>
      </span>
    </div>
  </section>

  <section v-if="preview && (step === 'preview' || step === 'applying')" class="st-section bk-preview" aria-labelledby="bk-plan-h">
    <header>
      <h3 id="bk-plan-h">The changes</h3>
      <span v-if="preview.exportedAt || preview.hubVersion" class="st-meta">
        <template v-if="preview.exportedAt">Exported {{ absolute(preview.exportedAt) }}</template><template v-if="preview.hubVersion"> by Cha Dash {{ preview.hubVersion }}</template>
      </span>
    </header>
    <div v-if="preview.nothingNew" class="st-note" role="status">
      <Glyph kind="check" /><span>This hub already matches the file: importing it changes nothing ({{ preview.summary.unchanged }} unchanged).</span>
    </div>
    <dl class="kv bk-summary">
      <dt>New</dt>
      <dd>{{ preview.summary.create }}</dd>
      <dt>Updated</dt>
      <dd>{{ preview.summary.update }}</dd>
      <dt>Unchanged</dt>
      <dd>{{ preview.summary.unchanged }}</dd>
      <dt>Not in the file</dt>
      <dd>{{ preview.summary.stale }}</dd>
      <template v-if="preview.bundle.present">
        <dt>Secrets restored</dt>
        <dd>{{ preview.summary.secretsRestored }}</dd>
      </template>
      <dt>Secrets needed</dt>
      <dd>{{ preview.summary.secretsNeeded }}</dd>
    </dl>
    <p v-if="preview.bundle.present && !preview.bundle.unlocked" class="st-note attn">
      <Glyph kind="lock" /><span>The file’s {{ preview.bundle.secrets }} secrets are encrypted. Enter the passphrase above and preview again to restore them.</span>
    </p>
    <p v-else-if="preview.bundle.unlocked" class="st-note"><Glyph kind="lock" /><span>The passphrase opened the file’s {{ preview.bundle.secrets }} encrypted secrets. Their values are never shown.</span></p>

    <div v-if="preview.warnings.length" class="plan-block">
      <h4>Worth knowing</h4>
      <ul class="rel">
        <li v-for="(w, i) in preview.warnings" :key="i"><span><Glyph kind="attn" /> {{ w.message }}</span><span>{{ w.code.replace(/_/g, ' ') }}</span></li>
      </ul>
    </div>

    <div v-if="preview.needed.length" class="plan-block">
      <h4>Secrets needed · {{ preview.needed.length }}</h4>
      <p class="sub">These connections arrive without them and wait with a Repair until you enter them in Integrations.</p>
      <ul class="rel">
        <li v-for="n in preview.needed" :key="n.instance + n.field">
          <span><b>{{ n.name }}</b> · {{ n.integration }} · <span class="n">{{ n.field }}</span></span>
          <span>{{ n.inBundle ? 'in the encrypted bundle' : 'enter after importing' }}</span>
        </li>
      </ul>
    </div>

    <div v-for="sec in preview.sections" :key="sec.kind" class="plan-block bk-kind">
      <h4>{{ sec.title }} · {{ sec.changed.length + sec.unchanged.length }}</h4>
      <ul v-if="sec.changed.length" class="bk-objs">
        <li v-for="o in sec.changed" :key="o.key" class="bk-obj">
          <div class="bk-obj-head">
            <span class="bk-act" :data-action="o.action">{{ ACTION_WORDS[o.action] }}</span>
            <b>{{ o.name }}</b>
            <span class="bk-tags n">{{ [o.integration, o.id !== o.name ? o.id : ''].filter(Boolean).join(' · ') }}</span>
          </div>
          <p v-if="o.action === 'stale'" class="sub">On this hub but not in the file. Kept as it is.</p>
          <p v-else-if="o.fileId" class="sub">Matches this hub’s board by its slug (the file calls it <span class="n">{{ o.fileId }}</span>).</p>
          <p v-if="o.detail" class="sub">{{ o.detail }}</p>
          <ul v-if="o.fields.length" class="bk-fields">
            <li v-for="(f, i) in o.fields" :key="i" :data-op="f.op">
              <span class="bk-op" aria-hidden="true">{{ OP_MARK[f.op] }}</span>
              <span class="bk-path">{{ f.path }}</span>
              <span class="sr">{{ OP_WORD[f.op] }}</span>
              <span v-if="f.op === 'replace' || f.op === 'reorder'" class="bk-val"><s>{{ f.from }}</s> → {{ f.to }}</span>
              <span v-else-if="f.op === 'add'" class="bk-val">{{ f.to }}</span>
              <span v-else class="bk-val"><s>{{ f.from }}</s></span>
            </li>
            <li v-if="o.truncated" class="bk-more">… and more changes than listed</li>
          </ul>
          <ul v-if="o.secrets.length" class="bk-secrets">
            <li v-for="s in o.secrets" :key="s.field" :data-action="s.action">
              <Glyph :kind="s.action === 'needed' ? 'attn' : 'lock'" />
              <span class="n">{{ s.field }}</span>
              <span>{{ secretWords(s) }}</span>
            </li>
          </ul>
        </li>
      </ul>
      <details v-if="sec.unchanged.length" class="bk-same">
        <summary>{{ sec.unchanged.length }} unchanged</summary>
        <p class="sub">{{ sec.unchanged.map((o) => o.name).join(', ') }}</p>
      </details>
    </div>

    <div class="plan-apply">
      <p class="sub">Applies everything in one step, or nothing. A changed board keeps its history: its layout gains a revision you can undo past.</p>
      <div class="st-acts">
        <UiButton variant="primary" :disabled="step === 'applying' || preview.nothingNew" :aria-busy="step === 'applying'" @click="apply">
          {{ step === 'applying' ? 'Importing…' : preview.nothingNew ? 'Nothing to import' : `Import ${changes} change${changes === 1 ? '' : 's'}` }}<span class="tier">T1</span>
        </UiButton>
        <UiButton :disabled="step === 'applying'" @click="step = 'edit'">Back</UiButton>
      </div>
    </div>
  </section>
</template>

<style>
.bk-out .check-row {
  align-self: flex-start;
}
.bk-file .bk-paste {
  font-size: var(--type-small);
  color: var(--text-3);
}
.bk-paste > summary {
  cursor: pointer;
  width: fit-content;
}
.bk-paste .field {
  margin-top: var(--space-2);
}
.bk-paste textarea {
  min-height: 9rem;
}
.bk-unlock {
  max-width: 420px;
}
.bk-summary {
  max-width: 360px;
}
.bk-preview h4 {
  margin: 0 0 var(--space-2);
  font: var(--weight-medium) var(--type-micro) / 1 var(--font-num);
  letter-spacing: var(--tracking-caps);
  text-transform: uppercase;
  color: var(--text-3);
}
.bk-objs {
  list-style: none;
  margin: 0;
  padding: 0;
}
.bk-obj {
  display: flex;
  flex-direction: column;
  gap: var(--space-1);
  padding: var(--space-2) 0;
  border-top: var(--hairline) solid var(--line);
}
.bk-obj-head {
  display: flex;
  flex-wrap: wrap;
  align-items: baseline;
  gap: 4px var(--space-2);
  font-size: var(--type-small);
  color: var(--text);
}
.bk-tags {
  color: var(--text-3);
  font-size: var(--type-caption);
}
.bk-act {
  flex: none;
  padding: 2px 6px;
  border: var(--hairline) solid var(--line-2);
  border-radius: var(--radius-full);
  font: var(--weight-medium) 10.5px/1.2 var(--font-num);
  color: var(--text-2);
}
.bk-act[data-action='create'] {
  border-color: var(--accent-line);
  background: var(--accent-bg);
  color: var(--accent-text);
}
.bk-act[data-action='update'] {
  border-color: var(--attention-line);
  background: var(--attention-bg);
  color: var(--attention-text);
}
.bk-act[data-action='stale'] {
  border-style: dashed;
  color: var(--text-3);
}
.bk-fields,
.bk-secrets {
  list-style: none;
  margin: 0;
  padding: 0 0 0 var(--space-4);
  font-size: var(--type-caption);
  line-height: 1.5;
  color: var(--text-2);
}
.bk-fields li {
  display: grid;
  grid-template-columns: 1.2em minmax(0, max-content) minmax(0, 1fr);
  gap: var(--space-2);
  align-items: baseline;
}
.bk-op {
  font-family: var(--font-num);
  color: var(--text-3);
  text-align: center;
}
.bk-fields li[data-op='add'] .bk-op {
  color: var(--accent-text);
}
.bk-fields li[data-op='remove'] .bk-op {
  color: var(--down-text);
}
.bk-path {
  font-family: var(--font-num);
  color: var(--text);
  overflow-wrap: anywhere;
}
.bk-val {
  font-family: var(--font-num);
  overflow-wrap: anywhere;
}
.bk-val s {
  color: var(--text-3);
}
.bk-fields .bk-more {
  display: block;
  color: var(--text-3);
}
.bk-secrets li {
  display: flex;
  align-items: center;
  gap: var(--space-2);
}
.bk-secrets li[data-action='needed'] {
  color: var(--attention-text);
}
.bk-same {
  margin-top: var(--space-1);
  font-size: var(--type-small);
  color: var(--text-3);
}
.bk-same > summary {
  cursor: pointer;
  width: fit-content;
}
@media (max-width: 539px) {
  .bk-fields li {
    grid-template-columns: 1.2em minmax(0, 1fr);
  }
  .bk-fields .bk-val {
    grid-column: 2;
  }
}
</style>
