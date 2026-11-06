<script setup lang="ts">
/**
 * Settings → Displays (admin and owner): wall screens that show one board,
 * read-only, with no account (docs/plan/15-v0.5.md → Wave 33).
 *
 *   GET    /api/displays        → {displays: [{id, name, board: {id, name}, quiet: {start, end} | null,
 *                                   created_at, created_by, last_seen_at, last_seen_ip}]}
 *   POST   /api/displays        {name, board, style, quiet} → 201 {display, secret}: the secret, once
 *   PATCH  /api/displays/{id}   {name?, style?, quiet?} (quiet null clears) → {display}
 *   DELETE /api/displays/{id}   → 204: revoked, and the screen's socket closes
 *   GET    /api/boards          → the board picker
 *
 * The sign-in link, `<origin>/display/<board>#<secret>`, is shown once, right
 * after it's made, with Copy. The secret rides in the fragment, so it's never
 * sent in a URL or logged; the display claims it and drops it. Times are
 * Unix milliseconds. Quiet hours are hub time: the screen goes dark, and only
 * a down incident wakes it.
 *
 * Its own chunk, loaded with the pane.
 */
import SettingsHelp from './SettingsHelp.vue';
import { computed, nextTick, onMounted, reactive, ref, useId } from 'vue';
import Glyph from '../ui/marks/Glyph.vue';
import { ago, absolute, hhmm } from '../lib/format';
import { api } from '../lib/api';
import { hubWords } from '../lib/actions';
import { useUi } from '../stores/ui';
import { copyText } from './recipe';
import { focusFirst } from '../lib/focus';
import UiButton from '../ui/Button.vue';
import UiField from '../ui/Field.vue';

export interface Quiet {
  start: string;
  end: string;
}

/** How a display shows its board (wave 34). */
export type Style = 'wall' | 'tv' | 'ink';
const STYLES: { id: Style; label: string }[] = [
  { id: 'wall', label: 'Wall tablet' },
  { id: 'tv', label: 'TV' },
  { id: 'ink', label: 'E-ink screen' },
];
const styleWords = (st: Style | undefined) => STYLES.find((x) => x.id === (st ?? 'wall'))!.label;

export interface HubDisplay {
  id: string;
  name: string;
  board: { id: string; name: string };
  quiet: Quiet | null;
  style?: Style;
  created_at: number;
  created_by: string;
  last_seen_at: number | null;
  last_seen_ip: string | null;
  /** When its unclaimed link stops working; null once a screen claimed it. */
  link_expires_at?: number | null;
}

interface BoardRef {
  id: string;
  name: string;
}

const ui = useUi();
const uid = useId();
const now = Date.now();

const displays = ref<HubDisplay[]>([]);
const boards = ref<BoardRef[]>([]);
const status = ref<'loading' | 'ready' | 'error'>('loading');
const loadError = ref('');

const sorted = computed(() => [...displays.value].sort((a, b) => a.name.localeCompare(b.name)));

async function load(): Promise<void> {
  try {
    const res = await api<{ displays?: HubDisplay[] }>('/api/displays');
    displays.value = res?.displays ?? [];
    status.value = 'ready';
  } catch (err) {
    status.value = 'error';
    loadError.value = hubWords(err, 'Displays couldn’t be loaded.');
  }
}

async function loadBoards(): Promise<void> {
  if (boards.value.length) return;
  try {
    const res = await api<{ boards?: BoardRef[] }>('/api/boards');
    boards.value = res?.boards ?? [];
  } catch (err) {
    createError.value = hubWords(err, 'The boards couldn’t be loaded.');
  }
}

const quietWords = (q: Quiet | null) => (q ? `quiet ${q.start}–${q.end}` : 'no quiet hours');
const HHMM = /^([01]\d|2[0-3]):[0-5]\d$/;

/** The quiet hours a form adds up to, or an error sentence. */
function quietOf(f: { quietOn: boolean; start: string; end: string }): { quiet: Quiet | null } | { error: string } {
  if (!f.quietOn) return { quiet: null };
  if (!HHMM.test(f.start) || !HHMM.test(f.end)) return { error: 'Give both times as HH:MM, such as 22:00 and 07:00.' };
  if (f.start === f.end) return { error: 'Quiet hours need a different start and end.' };
  return { quiet: { start: f.start, end: f.end } };
}

// ── create ──
const creating = ref(false);
const saving = ref(false);
const createError = ref('');
const nameError = ref('');
const draft = reactive({ name: '', board: '', style: 'wall' as Style, quietOn: false, start: '22:00', end: '07:00' });
/** The link just made: shown once, until Done. */
const fresh = ref<{ display: HubDisplay; link: string } | undefined>();
const copied = ref(false);

async function startCreate(): Promise<void> {
  creating.value = true;
  Object.assign(draft, { name: '', board: '', style: 'wall', quietOn: false, start: '22:00', end: '07:00' });
  nameError.value = '';
  createError.value = '';
  await loadBoards();
  draft.board = boards.value[0]?.id ?? '';
  await nextTick();
  document.getElementById(`${uid}-name`)?.focus();
}

async function create(): Promise<void> {
  if (saving.value) return;
  nameError.value = draft.name.trim() ? '' : 'Name it after where it hangs, such as “Hall tablet”.';
  createError.value = '';
  if (nameError.value) return;
  if (!draft.board) {
    createError.value = 'Pick the board it shows.';
    return;
  }
  const q = quietOf(draft);
  if ('error' in q) {
    createError.value = q.error;
    return;
  }
  saving.value = true;
  try {
    const res = await api<{ display: HubDisplay; secret: string }>('/api/displays', { method: 'POST', body: { name: draft.name.trim(), board: draft.board, style: draft.style, quiet: q.quiet } });
    fresh.value = { display: res.display, link: `${location.origin}/display/${encodeURIComponent(res.display.board.id)}#${res.secret}` };
    copied.value = false;
    displays.value = [res.display, ...displays.value];
    creating.value = false;
    await nextTick();
    document.getElementById(`${uid}-link`)?.focus();
  } catch (err) {
    createError.value = hubWords(err, 'The display wasn’t made.');
  } finally {
    saving.value = false;
  }
}

async function copy(): Promise<void> {
  if (!fresh.value) return;
  copied.value = await copyText(fresh.value.link);
  if (!copied.value) ui.toast('Couldn’t copy. Select the link and copy it yourself.', 'error');
}

/** A new link for a display: shown once, like a new display's; the screen on the old link keeps working until it's claimed. */
async function newLink(d: HubDisplay): Promise<void> {
  busy.value = d.id;
  rowError[d.id] = '';
  try {
    const res = await api<{ display: HubDisplay; secret: string }>(`/api/displays/${encodeURIComponent(d.id)}/link`, { method: 'POST' });
    if (!res) return;
    displays.value = displays.value.map((x) => (x.id === d.id ? { ...x, ...res.display } : x));
    fresh.value = { display: res.display, link: `${location.origin}/display/${encodeURIComponent(res.display.board.id)}#${res.secret}` };
  } catch (err) {
    rowError[d.id] = hubWords(err, 'Couldn’t make a new link.');
  } finally {
    busy.value = '';
  }
}

function done(): void {
  fresh.value = undefined;
  copied.value = false;
  void focusFirst(`${uid}-new`, 'dpl-h');
}

function cancelCreate(): void {
  creating.value = false;
  void focusFirst(`${uid}-new`, 'dpl-h');
}

// ── edit ──
const editing = ref('');
const edit = reactive({ name: '', style: 'wall' as Style, quietOn: false, start: '22:00', end: '07:00' });
const editError = ref('');
const busy = ref('');
const rowError = reactive<Record<string, string>>({});

function startEdit(d: HubDisplay): void {
  editing.value = d.id;
  revoking.value = '';
  editError.value = '';
  Object.assign(edit, { name: d.name, style: d.style ?? 'wall', quietOn: !!d.quiet, start: d.quiet?.start ?? '22:00', end: d.quiet?.end ?? '07:00' });
  void focusFirst(`${uid}-en-${d.id}`);
}

function cancelEdit(d: HubDisplay): void {
  editing.value = '';
  void focusFirst(`${uid}-eb-${d.id}`, 'dpl-h');
}

async function saveEdit(d: HubDisplay): Promise<void> {
  if (busy.value) return;
  editError.value = '';
  const name = edit.name.trim();
  if (!name) {
    editError.value = 'A display needs a name.';
    return;
  }
  const q = quietOf(edit);
  if ('error' in q) {
    editError.value = q.error;
    return;
  }
  const body: { name?: string; style?: Style; quiet?: Quiet | null } = {};
  if (name !== d.name) body.name = name;
  if (edit.style !== (d.style ?? 'wall')) body.style = edit.style;
  if (JSON.stringify(q.quiet) !== JSON.stringify(d.quiet)) body.quiet = q.quiet;
  if (!Object.keys(body).length) {
    cancelEdit(d);
    return;
  }
  busy.value = d.id;
  try {
    const res = await api<{ display: HubDisplay }>(`/api/displays/${encodeURIComponent(d.id)}`, { method: 'PATCH', body });
    displays.value = displays.value.map((x) => (x.id === d.id ? { ...x, ...(res?.display ?? body) } : x));
    editing.value = '';
    void focusFirst(`${uid}-eb-${d.id}`, 'dpl-h');
  } catch (err) {
    editError.value = hubWords(err, 'The display wasn’t changed.');
  } finally {
    busy.value = '';
  }
}

// ── revoke ──
const revoking = ref('');

/** Open the confirm group and put focus on its Cancel, the safe choice. */
function askRevoke(d: HubDisplay): void {
  revoking.value = d.id;
  editing.value = '';
  void focusFirst(`${uid}-rvc-${d.id}`);
}

function cancelRevoke(d: HubDisplay): void {
  revoking.value = '';
  void focusFirst(`${uid}-rvb-${d.id}`, 'dpl-h');
}

async function revoke(d: HubDisplay): Promise<void> {
  if (busy.value) return;
  busy.value = d.id;
  rowError[d.id] = '';
  try {
    await api(`/api/displays/${encodeURIComponent(d.id)}`, { method: 'DELETE' });
    displays.value = displays.value.filter((x) => x.id !== d.id);
    revoking.value = '';
    void focusFirst(`${uid}-new`, 'dpl-h');
    ui.toast(`Revoked “${d.name}”. The screen stops showing the board now.`);
  } catch (err) {
    rowError[d.id] = hubWords(err, 'The display wasn’t revoked.');
  } finally {
    busy.value = '';
  }
}

onMounted(load);
</script>

<template>
  <section class="st-section" aria-labelledby="dpl-h">
    <header>
      <h3 id="dpl-h" tabindex="-1">Displays</h3>
      <span v-if="status === 'ready' && displays.length" class="st-meta">{{ displays.length }} signed in</span>
      <UiButton v-if="!creating && !fresh" size="sm" :id="`${uid}-new`" @click="startCreate">New display</UiButton>
    </header>
    <SettingsHelp summary="A wall tablet or a TV that shows one board, with no account.">
      A display shows one board, read-only, with no account and no menus. It dims when nothing changes, goes dark in its quiet hours, and wakes when an
      incident needs you. Make a link here and open it once on the screen; revoke it here when the screen goes.
    </SettingsHelp>

    <div v-if="fresh" class="dpl-fresh st-note attn" role="region" :aria-labelledby="`${uid}-fresh-h`">
      <div class="dpl-fresh-body">
        <h4 :id="`${uid}-fresh-h`">Open this link on “{{ fresh.display.name }}”</h4>
        <p>This is the only time Cha Dash shows it. It works once, within a day: the screen signs in when it opens the link, and then the link is used up. Lost it? Make a new link from the display’s row.</p>
        <div class="secret-row">
          <label :for="`${uid}-link`" class="sr">The display’s link</label>
          <input :id="`${uid}-link`" class="st-mono" type="text" readonly :value="fresh.link" spellcheck="false" autocomplete="off" @focus="($event.target as HTMLInputElement).select()" />
          <UiButton @click="copy">{{ copied ? 'Copied' : 'Copy' }}</UiButton>
        </div>
        <p class="sr" aria-live="polite">{{ copied ? 'Copied to the clipboard.' : '' }}</p>
        <div class="st-acts"><UiButton variant="primary" size="sm" @click="done">Done</UiButton></div>
      </div>
    </div>

    <form v-if="creating" class="st-form dpl-add" novalidate aria-labelledby="dpl-add-h" @submit.prevent="create">
      <h4 id="dpl-add-h" class="sr">New display</h4>
      <UiField :id="`${uid}-name`" label="Name" help="Where it hangs, so you can tell displays apart." :error="nameError" v-slot="{ control }">
        <input v-bind="control" v-model="draft.name" type="text" maxlength="64" autocomplete="off" />
      </UiField>
      <UiField label="Board" help="The one board it shows." v-slot="{ control }">
        <select v-bind="control" v-model="draft.board">
          <option v-for="b in boards" :key="b.id" :value="b.id">{{ b.name }}</option>
        </select>
      </UiField>
      <UiField label="Kind of screen" help="A wall tablet dims and wakes; a TV is larger, moved with the remote's arrows; an e-ink screen is one colour and never dims." v-slot="{ control }">
        <select v-bind="control" v-model="draft.style">
          <option v-for="x in STYLES" :key="x.id" :value="x.id">{{ x.label }}</option>
        </select>
      </UiField>
      <UiField label="Quiet hours" check help="Dark at night; only a down incident wakes it. Hub time." v-slot="{ control }">
        <input v-bind="control" v-model="draft.quietOn" type="checkbox" />
      </UiField>
      <div v-if="draft.quietOn" class="dpl-quiet">
        <UiField label="From" v-slot="{ control }"><input v-bind="control" v-model="draft.start" type="time" /></UiField>
        <UiField label="To" v-slot="{ control }"><input v-bind="control" v-model="draft.end" type="time" /></UiField>
      </div>
      <p v-if="createError" class="err" role="alert">{{ createError }}</p>
      <div class="st-acts">
        <UiButton variant="primary" type="submit" :aria-disabled="saving ? 'true' : undefined" :aria-busy="saving || undefined">{{ saving ? 'Making…' : 'Make link' }}</UiButton>
        <UiButton variant="ghost" @click="cancelCreate">Cancel</UiButton>
      </div>
    </form>

    <div v-if="status === 'loading'" class="st-skel" aria-busy="true"><span class="skel"></span><span class="skel"></span></div>
    <div v-else-if="status === 'error'" class="st-note bad" role="alert"><Glyph kind="down" /><span>{{ loadError }}</span></div>
    <div v-else-if="!displays.length && !creating" class="st-empty">
      <span class="t">No displays yet</span>
      <span class="m">Put a board on a wall tablet or a TV: make a link here and open it on the screen.</span>
    </div>
    <ul v-else class="st-list dpl-list">
      <li v-for="d in sorted" :key="d.id">
        <div class="st-row dpl-row">
          <span class="mono-badge" aria-hidden="true">{{ d.name.slice(0, 2).toUpperCase() }}</span>
          <span>
            <span class="t">{{ d.name }} <span class="chip">{{ d.board.name }}</span></span>
            <span class="m">{{ styleWords(d.style) }} · {{ quietWords(d.quiet) }}</span>
            <span class="m">
              made <span :title="absolute(d.created_at)">{{ ago(d.created_at, now) }}</span> by {{ d.created_by }} ·
              <template v-if="d.last_seen_at">last seen <span :title="absolute(d.last_seen_at)">{{ ago(d.last_seen_at, now) }}</span><template v-if="d.last_seen_ip"> from {{ d.last_seen_ip }}</template></template>
              <template v-else-if="d.link_expires_at">link unused, works until <span :title="absolute(d.link_expires_at)">{{ hhmm(d.link_expires_at) }}</span></template>
              <template v-else>never opened</template>
            </span>
            <span v-if="rowError[d.id]" class="err" role="alert">{{ rowError[d.id] }}</span>
          </span>
          <span v-if="revoking !== d.id && editing !== d.id" class="end">
            <UiButton variant="ghost" size="sm" :aria-label="`New link for ${d.name}`" :aria-busy="busy === d.id || undefined" @click="newLink(d)">New link</UiButton>
            <UiButton variant="ghost" size="sm" :id="`${uid}-eb-${d.id}`" :aria-label="`Edit ${d.name}`" @click="startEdit(d)">Edit</UiButton>
            <UiButton variant="ghost" size="sm" :id="`${uid}-rvb-${d.id}`" :aria-label="`Revoke ${d.name}`" @click="askRevoke(d)">Revoke</UiButton>
          </span>
        </div>
        <form v-if="editing === d.id" class="st-form dpl-edit" novalidate :aria-label="`Edit ${d.name}`" @submit.prevent="saveEdit(d)">
          <UiField :id="`${uid}-en-${d.id}`" label="Name" v-slot="{ control }">
            <input v-bind="control" v-model="edit.name" type="text" maxlength="64" autocomplete="off" />
          </UiField>
          <UiField label="Kind of screen" help="A wall tablet dims and wakes; a TV is larger, moved with the remote's arrows; an e-ink screen is one colour and never dims." v-slot="{ control }">
            <select v-bind="control" v-model="edit.style">
              <option v-for="x in STYLES" :key="x.id" :value="x.id">{{ x.label }}</option>
            </select>
          </UiField>
          <UiField label="Quiet hours" check help="Hub time." v-slot="{ control }">
            <input v-bind="control" v-model="edit.quietOn" type="checkbox" />
          </UiField>
          <div v-if="edit.quietOn" class="dpl-quiet">
            <UiField label="From" v-slot="{ control }"><input v-bind="control" v-model="edit.start" type="time" /></UiField>
            <UiField label="To" v-slot="{ control }"><input v-bind="control" v-model="edit.end" type="time" /></UiField>
          </div>
          <p v-if="editError" class="err" role="alert">{{ editError }}</p>
          <div class="st-acts">
            <UiButton variant="primary" type="submit" :aria-disabled="busy === d.id ? 'true' : undefined" :aria-busy="busy === d.id || undefined">Save</UiButton>
            <UiButton variant="ghost" @click="cancelEdit(d)">Cancel</UiButton>
          </div>
        </form>
        <div v-if="revoking === d.id" class="st-confirm dpl-revoke" role="group" :aria-labelledby="`${uid}-rv-${d.id}`">
          <span :id="`${uid}-rv-${d.id}`">Revoke <b>{{ d.name }}</b>? The screen stops showing the board at once, and its link never works again.</span>
          <UiButton variant="danger-solid" :aria-disabled="busy === d.id ? 'true' : undefined" :aria-busy="busy === d.id || undefined" @click="revoke(d)">Revoke {{ d.name }}</UiButton>
          <UiButton :id="`${uid}-rvc-${d.id}`" @click="cancelRevoke(d)">Cancel</UiButton>
        </div>
      </li>
    </ul>
  </section>
</template>

<style>
.dpl-add,
.dpl-edit {
  padding: var(--space-4);
  border: var(--hairline) solid var(--line);
  border-radius: var(--radius-lg);
}
.dpl-edit {
  margin: 0 var(--space-2) var(--space-3);
}
.dpl-quiet {
  display: flex;
  flex-wrap: wrap;
  gap: var(--space-3);
}
.dpl-fresh {
  flex-direction: column;
}
.dpl-fresh-body {
  display: flex;
  flex-direction: column;
  gap: var(--space-2);
  width: 100%;
}
.dpl-fresh h4 {
  font: var(--weight-medium) var(--type-body) / 1.3 var(--font-ui);
}
.dpl-revoke {
  margin: 0 var(--space-2) var(--space-3);
}
</style>
