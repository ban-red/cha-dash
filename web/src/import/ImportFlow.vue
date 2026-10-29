<script setup lang="ts">
/**
 * Import: drop or choose files (or paste them), let the hub name their
 * source, then the Review sheet (docs/plan/11-import-and-discovery.md):
 * every connection and tile as a row to tick, where the tiles go, keys the
 * files didn't hold, a picture of the board, and Apply, which applies
 * exactly the plan on screen or says what changed.
 *
 * Used by Settings → Bring in → Import and by the first-run wizard, which
 * listens for `applied`. Files are never parsed here; the hub does it. A
 * key typed here goes to the hub over this same-origin request and comes
 * back only as a field name.
 */
import { computed, onBeforeUnmount, onMounted, reactive, ref, shallowRef, watch } from 'vue';
import Glyph from '../components/marks/Glyph.vue';
import BoardPreview from './BoardPreview.vue';
import { ApiError, api } from '../lib/api';
import { plural } from '../lib/format';
import { navigate, settingsPath } from '../lib/router';
import { useBoard } from '../stores/board';
import { catalogEntry, errorWords, loadInstances } from '../stores/integrations';
import { useUi } from '../stores/ui';
import { markFresh, pending, takePending } from './fresh';
import {
  choicesBody,
  counts,
  emptyChoices,
  groupState,
  headOf,
  holdFile,
  included,
  reviewOf,
  setIncluded,
  sizeWords,
  sourceInfo,
  SOURCES,
  type Choices,
  type Detected,
  type HeldFile,
  type Review,
  type SourceId,
} from './model';

const { initialSource, preset } = defineProps<{
  initialSource?: SourceId | undefined;
  /** Files to review at once, by role (the Discover tab's leads): no file step. */
  preset?: { files: Record<string, string> } | undefined;
}>();
const emit = defineEmits<{ applied: [boardId: string]; cancel: [] }>();

const ui = useUi();
const board = useBoard();

const source = ref<SourceId>(initialSource ?? 'homepage');
const info = computed(() => sourceInfo(source.value)!);
/** The files by role: dropped or chosen (named), or pasted. */
const held = reactive<Record<string, HeldFile | undefined>>({});
const step = ref<'choose' | 'detecting' | 'previewing' | 'review' | 'applying'>('choose');
const error = ref('');
const detected = ref<Detected | undefined>();
const dragging = ref(false);
const picker = ref<HTMLInputElement | null>(null);

const base = shallowRef<Review | undefined>();
const chosen = shallowRef<Review | undefined>();
const choices = reactive<Choices>(emptyChoices());
const checking = ref(false);
const changed = ref(false);
const said = ref('');
const highlight = ref('');
const keysOpen = reactive(new Set<string>());

const ready = computed(() => info.value.roles.every((r) => !r.required || held[r.key]));

// ── Files ───────────────────────────────────────────────────────────────────

function reset(): void {
  for (const k of Object.keys(held)) held[k] = undefined;
  base.value = chosen.value = undefined;
  Object.assign(choices, emptyChoices());
  detected.value = undefined;
  changed.value = false;
  error.value = '';
  step.value = 'choose';
}

async function takeFiles(list: File[]): Promise<void> {
  if (!list.length) return;
  error.value = '';
  const limit = Math.max(...SOURCES.map((s) => s.maxBytes));
  const tooBig = list.find((f) => f.size > limit);
  if (tooBig) {
    error.value = `${tooBig.name} is larger than ${sizeWords(limit)}; that’s not a config Cha Dash can import.`;
    return;
  }
  step.value = 'detecting';
  try {
    const files = await Promise.all(list.map(holdFile));
    const d = await api<Detected>('/api/import/detect', { method: 'POST', body: { files: files.map(headOf) } });
    detected.value = d;
    if (d.source === 'chadash') {
      step.value = 'choose';
      error.value = '';
      ui.toast('That’s a Cha Dash export. Restore it in Backup.');
      navigate(settingsPath('backup'), { replace: true });
      return;
    }
    if (!sourceInfo(d.source)) {
      step.value = 'choose';
      error.value = d.message ?? 'These don’t look like a config Cha Dash can import.';
      return;
    }
    source.value = d.source as SourceId;
    for (const k of Object.keys(held)) held[k] = undefined;
    for (const f of files) {
      const role = d.files[f.name];
      if (role) held[role] = f;
    }
    step.value = 'choose';
    if (ready.value) await preview();
  } catch (err) {
    step.value = 'choose';
    error.value = errorWords(err, 'The hub couldn’t read those files.');
  }
}

function onPick(e: Event): void {
  const input = e.target as HTMLInputElement;
  void takeFiles([...(input.files ?? [])]);
  input.value = '';
}

function onDrop(e: DragEvent): void {
  dragging.value = false;
  void takeFiles([...(e.dataTransfer?.files ?? [])]);
}

function pasted(role: string, text: string): void {
  held[role] = text ? { name: '', data: text, binary: false, bytes: new Blob([text]).size } : undefined;
  if (step.value === 'review') {
    base.value = chosen.value = undefined;
    step.value = 'choose';
  }
}

function pickSource(id: SourceId): void {
  if (id === source.value) return;
  source.value = id;
  for (const k of Object.keys(held)) held[k] = undefined;
  base.value = chosen.value = undefined;
  step.value = 'choose';
}

// ── Dry runs and apply ──────────────────────────────────────────────────────

function body(extra: Record<string, unknown> = {}): Record<string, unknown> {
  const files: Record<string, string> = {};
  const names: Record<string, string> = {};
  for (const r of info.value.roles) {
    const f = held[r.key];
    if (!f) continue;
    files[r.key] = f.data;
    if (f.name) names[r.key] = f.name;
  }
  return { files, names, ...extra };
}

function importError(err: unknown, fallback: string): string {
  if (err instanceof ApiError) {
    if (err.status === 413) return `Those files are larger than ${sizeWords(info.value.maxBytes)} together; that’s not a ${info.value.name} config.`;
    if (err.status === 415) return 'The hub expected JSON. Reload the page and try again.';
  }
  return errorWords(err, fallback);
}

async function preview(): Promise<void> {
  if (!ready.value) {
    error.value = `Add ${info.value.roles.find((r) => r.required)?.file ?? 'the file'} first.`;
    return;
  }
  error.value = '';
  step.value = 'previewing';
  try {
    const res = reviewOf(await api(`/api/import/${source.value}?dry_run=1`, { method: 'POST', body: body() }));
    Object.assign(choices, emptyChoices());
    keysOpen.clear();
    base.value = chosen.value = res;
    step.value = 'review';
  } catch (err) {
    step.value = 'choose';
    error.value = importError(err, 'The hub couldn’t read those files.');
  }
}

let timer: ReturnType<typeof setTimeout> | undefined;
let seq = 0;
async function recheck(): Promise<void> {
  const mine = ++seq;
  checking.value = true;
  try {
    const res = reviewOf(await api(`/api/import/${source.value}?dry_run=1`, { method: 'POST', body: body({ choices: choicesBody(choices) }) }));
    if (mine === seq) chosen.value = res;
  } catch (err) {
    if (mine === seq) error.value = importError(err, 'The hub couldn’t check those choices.');
  } finally {
    if (mine === seq) checking.value = false;
  }
}
watch(
  () => JSON.stringify(choicesBody(choices)),
  () => {
    if (step.value !== 'review') return;
    error.value = '';
    checking.value = true;
    if (timer) clearTimeout(timer);
    timer = setTimeout(() => void recheck(), 250);
  },
);

async function apply(): Promise<void> {
  const plan = chosen.value;
  if (!plan || checking.value) return;
  step.value = 'applying';
  error.value = '';
  try {
    const res = await api<{ import_id?: string; board_id?: string }>(`/api/import/${source.value}`, {
      method: 'POST',
      body: body({ choices: choicesBody(choices), plan_hash: plan.planHash }),
    });
    const c = counts(base.value!, choices);
    const keys = c.keysNeeded ? ` ${plural(c.keysNeeded, 'connection')} still ${c.keysNeeded === 1 ? 'needs' : 'need'} a key.` : '';
    ui.toast(`Imported from ${info.value.name}: ${plural(c.tiles, 'tile')}, ${plural(c.connections, 'connection')}.${c.secrets ? ` ${plural(c.secrets, 'secret')} in the vault.` : ''}${keys}`, 'ok', 8000, {
      label: 'History',
      run: () => navigate(settingsPath('import') + '/history', { replace: true }),
    });
    void loadInstances(true);
    markFresh(plan.tiles.map((t) => t.id));
    const boardId = res?.board_id ?? '';
    reset();
    emit('applied', boardId);
    if (boardId) {
      navigate(`/boards/${encodeURIComponent(boardId)}`);
      if (boardId === board.state.boardId) void board.load(boardId);
    }
  } catch (err) {
    step.value = 'review';
    if (err instanceof ApiError && err.code === 'plan_changed') {
      changed.value = true;
      base.value = reviewOf((await api(`/api/import/${source.value}?dry_run=1`, { method: 'POST', body: body() }).catch(() => undefined)) ?? {});
      await recheck();
      return;
    }
    error.value = importError(err, 'The import didn’t apply. Nothing changed.');
  }
}

// ── Rows ────────────────────────────────────────────────────────────────────

const r = computed(() => base.value);
const n = computed(() => (r.value ? counts(r.value, choices) : undefined));
const conns = computed(() => r.value?.connections.filter((x) => x.change !== 'unchanged') ?? []);
const sameConns = computed(() => r.value?.connections.filter((x) => x.change === 'unchanged') ?? []);
const tileRows = computed(() => r.value?.tiles ?? []);
const sectionTitle = (id: string) => r.value?.preview.sections.find((s) => s.id === id)?.title ?? id;
const ours = computed(() => new Set(chosen.value?.tiles.map((t) => t.id) ?? []));
const kindName = (id: string) => catalogEntry(id)?.name ?? id;

function toggle(object: string, on: boolean): void {
  if (!r.value) return;
  const moved = setIncluded(r.value, choices, object, on);
  said.value = moved.length ? `${on ? 'Also brings in' : 'Also leaves out'} ${moved.join(', ')}.` : '';
}

function toggleGroup(objects: string[], on: boolean): void {
  for (const o of objects) toggle(o, on);
  said.value = '';
}

function typed(id: string, field: string, value: string): void {
  const cur = { ...(choices.secrets.get(id) ?? {}) };
  cur[field] = value;
  choices.secrets.set(id, cur);
}

function reuse(id: string, existing: string, on: boolean): void {
  if (on) choices.useExisting.set(id, existing);
  else choices.useExisting.delete(id);
}

function focusRow(itemId: string): void {
  highlight.value = itemId;
  document.getElementById(`row-tile:${itemId}`)?.focus();
}

const targetBoard = computed({
  get: () => (choices.target.startsWith('board:') ? choices.target.slice(6) : ''),
  set: (id: string) => (choices.target = `board:${id}`),
});

const sevGlyph = (s: string) => (s === 'error' ? 'down' : s === 'info' ? 'ok' : 'attn');

onMounted(() => {
  if (preset) {
    for (const [role, data] of Object.entries(preset.files)) held[role] = { name: '', data, binary: false, bytes: data.length };
    void preview();
    return;
  }
  void takeFiles(takePending());
});
watch(
  () => pending.n,
  () => {
    if (preset) return;
    reset();
    void takeFiles(takePending());
  },
);
onBeforeUnmount(() => timer && clearTimeout(timer));
</script>

<template>
  <div class="imp">
    <p v-if="preset && step === 'previewing'" class="imp-quiet" aria-busy="true">Building the preview…</p>
    <p v-if="preset && error && step === 'choose'" class="err" role="alert">{{ error }}</p>
    <section v-if="!preset && step !== 'review' && step !== 'applying'" class="st-section" aria-labelledby="imp-files-h">
      <header><h3 id="imp-files-h">Files</h3></header>
      <div
        :class="['imp-drop', { drag: dragging }]"
        @dragover.prevent="dragging = true"
        @dragleave="dragging = false"
        @drop.prevent="onDrop"
      >
        <p>
          Drop your config files, or
          <button type="button" class="btn sm" :disabled="step === 'detecting' || step === 'previewing'" @click="picker?.click()">Choose…</button>
        </p>
        <p class="imp-quiet">Homepage · Homarr · plain YAML · a Cha Dash export</p>
        <input ref="picker" class="sr" type="file" multiple accept=".yaml,.yml,.zip,.json,text/yaml,application/yaml,application/zip" tabindex="-1" aria-hidden="true" @change="onPick" />
        <p v-if="step === 'detecting'" class="imp-quiet" role="status" aria-busy="true">Reading the files…</p>
      </div>

      <div v-if="detected && sourceInfo(detected.source)" class="st-note" role="status">
        <Glyph kind="check" />
        <span>
          These look like a {{ detected.name }} config: {{ Object.keys(detected.files).join(', ') }}.
          <template v-if="detected.unused.length"> Not used: {{ detected.unused.join(', ') }}.</template>
        </span>
      </div>

      <div class="seg imp-src" role="group" aria-label="Source">
        <button v-for="s in SOURCES" :key="s.id" type="button" :aria-pressed="source === s.id" @click="pickSource(s.id)">{{ s.name }}</button>
      </div>
      <p class="st-meta">Where to find it: {{ info.where }}</p>

      <div class="imp-roles">
        <div v-for="role in info.roles" :key="role.key" class="imp-role">
          <label :for="`imp-${role.key}`" class="if-name">{{ role.file }}</label>
          <span class="if-what">{{ role.what }}{{ role.required ? '' : ' · optional' }}</span>
          <template v-if="role.binary">
            <p :id="`imp-${role.key}`" class="imp-quiet">
              <template v-if="held[role.key]">{{ held[role.key]!.name }} · {{ sizeWords(held[role.key]!.bytes) }}</template>
              <template v-else>Drop the zip above, or choose it.</template>
            </p>
          </template>
          <textarea
            v-else
            :id="`imp-${role.key}`"
            :value="held[role.key]?.data ?? ''"
            spellcheck="false"
            autocapitalize="off"
            autocomplete="off"
            :placeholder="role.required ? 'Paste it here, or drop the file above' : 'Leave empty if you don’t use it'"
            :disabled="step === 'previewing' || step === 'detecting'"
            @input="pasted(role.key, ($event.target as HTMLTextAreaElement).value)"
          ></textarea>
          <span v-if="held[role.key]?.name && !role.binary" class="help">{{ held[role.key]!.name }} · {{ sizeWords(held[role.key]!.bytes) }}</span>
        </div>
      </div>
      <p v-if="error" class="err" role="alert">{{ error }}</p>
      <div class="st-acts">
        <button type="button" class="btn primary" :disabled="step === 'previewing' || step === 'detecting'" :aria-busy="step === 'previewing'" @click="preview">
          {{ step === 'previewing' ? 'Building the preview…' : 'Preview import' }}
        </button>
        <span class="st-meta">A dry run: nothing changes until you apply.</span>
      </div>
    </section>

    <section v-else-if="r && n" class="st-section imp-review" aria-labelledby="imp-review-h">
      <header>
        <h3 id="imp-review-h">Review: {{ info.name }} → {{ choices.target === 'none' ? 'connections only' : choices.target === 'new' ? `a new board “${r.boardName}”` : 'an existing board' }}</h3>
      </header>
      <div v-if="changed" class="st-note warn" role="alert">
        <Glyph kind="attn" /><span>Something changed since the preview. This is the plan as it is now; check it and apply again.</span>
      </div>
      <p class="imp-summary" aria-live="polite">
        <b>{{ n.total }} of {{ n.of }} selected</b> · {{ plural(n.secrets, 'secret') }} move into the vault
        <template v-if="n.keysNeeded"> · {{ plural(n.keysNeeded, 'connection') }} {{ n.keysNeeded === 1 ? 'needs' : 'need' }} a key</template>
        <template v-if="said"> · {{ said }}</template>
      </p>
      <div v-if="r.preview.nothingNew && !changed" class="st-note" role="status">
        <Glyph kind="check" /><span>Nothing to import: everything in these files is already here.</span>
      </div>

      <fieldset class="imp-target">
        <legend>Where tiles go</legend>
        <label><input v-model="choices.target" type="radio" value="new" /> A new board “{{ r.boardName }}”</label>
        <label v-if="board.state.boards.length">
          <input type="radio" :checked="choices.target.startsWith('board:')" @change="targetBoard = targetBoard || board.state.boards[0]!.id" /> Into board
          <select v-model="targetBoard" aria-label="Board" @focus="targetBoard = targetBoard || board.state.boards[0]!.id">
            <option v-for="b in board.state.boards" :key="b.id" :value="b.id">{{ b.name }}</option>
          </select>
        </label>
        <label><input v-model="choices.target" type="radio" value="none" /> Connections only, no tiles</label>
      </fieldset>

      <div class="imp-grid">
        <div class="imp-rows">
          <fieldset v-if="conns.length" class="imp-group">
            <legend>
              <label>
                <input
                  type="checkbox"
                  :checked="groupState(r, choices, conns.map((x) => x.object)) === 'all'"
                  :indeterminate="groupState(r, choices, conns.map((x) => x.object)) === 'some'"
                  @change="toggleGroup(conns.map((x) => x.object), ($event.target as HTMLInputElement).checked)"
                />
                Connections <span class="n">{{ conns.length }}</span>
              </label>
            </legend>
            <ul>
              <li v-for="x in conns" :key="x.object" class="imp-row">
                <label :for="`row-${x.object}`">
                  <input :id="`row-${x.object}`" type="checkbox" :checked="included(r, choices, x.object)" @change="toggle(x.object, ($event.target as HTMLInputElement).checked)" />
                  <span class="imp-name"><b>{{ x.name }}</b> · {{ kindName(x.integration) }}<template v-if="x.url"> · <span class="n">{{ x.url }}</span></template></span>
                  <span :class="['imp-badge', x.change]">{{ x.change === 'create' ? 'new' : x.change }}</span>
                </label>
                <span v-if="x.existing" class="imp-sub">
                  <label>
                    <input type="checkbox" :checked="choices.useExisting.get(x.id) === x.existing.id" :disabled="!included(r, choices, x.object)" @change="reuse(x.id, x.existing!.id, ($event.target as HTMLInputElement).checked)" />
                    {{ x.existing.match === 'url' ? 'Already connected at this address.' : 'You have one with this name; this one becomes “' + x.name + '”.' }} Use my existing one instead
                  </label>
                </span>
                <span v-if="x.missing.length && included(r, choices, x.object) && !choices.useExisting.has(x.id)" class="imp-sub">
                  <Glyph kind="attn" /> Needs {{ x.missing.join(' and ') }}.
                  <button type="button" class="btn sm ghost" :aria-expanded="keysOpen.has(x.id)" @click="keysOpen.has(x.id) ? keysOpen.delete(x.id) : keysOpen.add(x.id)">
                    {{ keysOpen.has(x.id) ? 'Later' : 'Add key' }}
                  </button>
                </span>
                <div v-if="keysOpen.has(x.id) && included(r, choices, x.object)" class="imp-keys">
                  <label v-for="f in x.missing" :key="f" class="field">
                    <span>{{ f }}</span>
                    <input type="password" autocomplete="off" :value="choices.secrets.get(x.id)?.[f] ?? ''" @input="typed(x.id, f, ($event.target as HTMLInputElement).value)" />
                  </label>
                  <p class="imp-quiet">
                    It goes into the encrypted vault and is never shown again. Leave it empty to add it later: the connection shows a repair in the Inbox until then.
                    <a v-if="catalogEntry(x.integration)?.recipe" :href="settingsPath('integrations') + '/new/' + encodeURIComponent(x.integration)" target="_blank" rel="noopener">How to make one</a>
                  </p>
                </div>
              </li>
            </ul>
            <p v-if="sameConns.length" class="imp-quiet">{{ plural(sameConns.length, 'connection') }} already here, unchanged.</p>
          </fieldset>

          <fieldset v-if="tileRows.length && choices.target !== 'none'" class="imp-group">
            <legend>
              <label>
                <input
                  type="checkbox"
                  :checked="groupState(r, choices, tileRows.map((t) => t.object)) === 'all'"
                  :indeterminate="groupState(r, choices, tileRows.map((t) => t.object)) === 'some'"
                  @change="toggleGroup(tileRows.map((t) => t.object), ($event.target as HTMLInputElement).checked)"
                />
                Tiles <span class="n">{{ tileRows.length }}</span>
              </label>
            </legend>
            <ul>
              <li v-for="t in tileRows" :key="t.object" class="imp-row" @mouseenter="highlight = t.id" @focusin="highlight = t.id">
                <label :for="`row-${t.object}`">
                  <input :id="`row-${t.object}`" type="checkbox" :checked="included(r, choices, t.object)" @change="toggle(t.object, ($event.target as HTMLInputElement).checked)" />
                  <span class="imp-name"><b>{{ t.name }}</b> · {{ sectionTitle(t.section) }}<template v-if="t.check"> · checked</template></span>
                  <span class="imp-badge">{{ t.widget }}</span>
                </label>
              </li>
            </ul>
          </fieldset>

          <div v-if="r.preview.hints.length || r.preview.notes.length" class="imp-group">
            <h4>Worth knowing</h4>
            <ul class="rel">
              <li v-for="(h, i) in r.preview.hints" :key="'h' + i"><span><Glyph :kind="sevGlyph(h.severity)" /> {{ h.message }}</span><span>{{ h.inbox ? 'to the Inbox' : 'hint' }}</span></li>
              <li v-for="(t, i) in r.preview.notes" :key="'n' + i"><span><Glyph kind="ok" /> {{ t }}</span><span>note</span></li>
            </ul>
          </div>
          <div v-if="r.preview.stale.length" class="imp-group">
            <h4>No longer in the files</h4>
            <p class="imp-quiet">Kept, not deleted: {{ r.preview.stale.join(', ') }}. Remove them in Integrations if you don’t need them.</p>
          </div>
          <details v-if="r.preview.unmapped.length || r.preview.ignored.length" class="imp-group">
            <summary>Won’t come across · {{ r.preview.unmapped.length + r.preview.ignored.length }}</summary>
            <ul class="rel">
              <li v-for="(u, i) in r.preview.unmapped" :key="'u' + i">
                <span><b>{{ u.name }}</b><template v-if="u.where"> · {{ u.where }}</template> · {{ u.reason ?? 'no mapping yet' }}</span><span>{{ u.kind }}</span>
              </li>
              <li v-for="(g, i) in r.preview.ignored" :key="'g' + i"><span><span class="n">{{ g.what }}</span> · {{ g.reason }}</span><span>setting</span></li>
            </ul>
          </details>
          <div v-if="r.preview.discovery.length" class="imp-group">
            <h4>Fills in later</h4>
            <ul class="rel">
              <li v-for="(d, i) in r.preview.discovery" :key="i"><span>{{ d }}</span><span>discovery</span></li>
            </ul>
          </div>
        </div>

        <div v-if="choices.target !== 'none'" class="imp-board">
          <details class="imp-board-toggle" open>
            <summary>The board</summary>
            <BoardPreview :sections="chosen?.preview.sections ?? r.preview.sections" :ours="ours" :highlight="highlight" @pick="focusRow" />
          </details>
        </div>
      </div>

      <p v-if="error" class="err" role="alert">{{ error }}</p>
      <div class="st-acts imp-apply">
        <button type="button" class="btn primary" :disabled="step === 'applying' || checking || n.total === 0" :aria-busy="step === 'applying' || checking" @click="apply">
          {{ step === 'applying' ? 'Importing…' : checking ? 'Checking…' : `Apply ${n.total}` }}<span class="tier">T1</span>
        </button>
        <button type="button" class="btn" :disabled="step === 'applying'" @click="preset ? emit('cancel') : reset()">Cancel</button>
        <span class="st-meta">One step; nothing is deleted. Importing the same files again changes only what changed.</span>
      </div>
    </section>
  </div>
</template>

<style>
.imp {
  display: flex;
  flex-direction: column;
  gap: var(--space-4);
}
.imp-drop {
  display: flex;
  flex-direction: column;
  align-items: center;
  gap: var(--space-2);
  padding: var(--space-6) var(--space-4);
  border: var(--hairline) dashed var(--line-2);
  border-radius: var(--radius-md);
  text-align: center;
}
.imp-drop p {
  margin: 0;
}
.imp-drop.drag {
  border-color: var(--text-2);
  background: color-mix(in oklch, var(--text) 4%, transparent);
}
.imp-quiet {
  margin: 0;
  font-size: var(--type-small);
  color: var(--text-3);
}
.imp-src {
  max-width: 520px;
}
.imp-roles {
  display: flex;
  flex-direction: column;
  gap: var(--space-3);
}
.imp-role {
  display: flex;
  flex-direction: column;
  gap: 4px;
}
.imp-role textarea {
  min-height: 6rem;
}
.imp-summary {
  margin: 0;
  font-size: var(--type-small);
  color: var(--text-2);
}
.imp-target {
  display: flex;
  flex-wrap: wrap;
  gap: var(--space-2) var(--space-5);
  margin: 0;
  padding: 0;
  border: 0;
  font-size: var(--type-small);
}
.imp-target legend,
.imp-group h4,
.imp-group legend {
  margin-bottom: var(--space-2);
  font: var(--weight-medium) var(--type-micro) / 1 var(--font-num);
  letter-spacing: var(--tracking-caps);
  text-transform: uppercase;
  color: var(--text-3);
}
.imp-grid {
  display: grid;
  grid-template-columns: minmax(0, 3fr) minmax(0, 2fr);
  gap: var(--space-5);
  align-items: start;
}
.imp-rows {
  display: flex;
  flex-direction: column;
  gap: var(--space-4);
}
.imp-group {
  margin: 0;
  padding: 0;
  border: 0;
}
.imp-group ul {
  list-style: none;
  margin: 0;
  padding: 0;
}
.imp-row {
  display: flex;
  flex-direction: column;
  gap: 4px;
  padding: 6px 0;
  border-top: var(--hairline) solid var(--line);
  font-size: var(--type-small);
}
.imp-row > label {
  display: flex;
  align-items: baseline;
  gap: var(--space-2);
}
.imp-name {
  flex: 1;
  min-width: 0;
  overflow-wrap: anywhere;
}
.imp-badge {
  flex: none;
  font: var(--type-micro) / 1 var(--font-num);
  color: var(--text-3);
}
.imp-badge.create,
.imp-badge.update {
  color: var(--text);
}
.imp-sub {
  display: flex;
  flex-wrap: wrap;
  align-items: center;
  gap: 4px var(--space-2);
  padding-left: 1.6rem;
  color: var(--text-2);
}
.imp-keys {
  display: flex;
  flex-direction: column;
  gap: var(--space-2);
  margin-left: 1.6rem;
  padding: var(--space-3);
  border: var(--hairline) solid var(--line);
  border-radius: var(--radius-sm);
}
.imp-board {
  position: sticky;
  top: 0;
}
.imp-board-toggle summary {
  margin-bottom: var(--space-2);
  font-size: var(--type-small);
  color: var(--text-3);
  cursor: pointer;
}
.imp-apply {
  padding-top: var(--space-4);
  border-top: var(--hairline) solid var(--line);
}
.st-note.warn {
  color: var(--text);
}
@media (max-width: 767px) {
  .imp-grid {
    grid-template-columns: minmax(0, 1fr);
  }
  .imp-board {
    position: static;
    order: -1;
  }
}
</style>
