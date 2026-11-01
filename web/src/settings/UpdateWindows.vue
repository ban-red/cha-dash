<script setup lang="ts">
/**
 * Settings → Updates → Maintenance windows: a time range, once or weekly,
 * for a set of hosts. A rollout can be scheduled into one, and while one is
 * open, incidents on its hosts don't notify (one still open when it ends
 * notifies then). Everyone who can open Updates sees them; admins change
 * them.
 *
 *   GET    /api/windows        → {windows}
 *   POST   /api/windows        WindowInput → the window
 *   PUT    /api/windows/{id}   WindowInput → the window
 *   DELETE /api/windows/{id}   (its scheduled rollouts are cancelled)
 *
 * Emits `changed` with the windows after any change, so the pane can offer
 * them for scheduling.
 */
import { computed, ref, useId } from 'vue';
import { api } from '../lib/api';
import { hubWords } from '../lib/actions';
import { focusFirst } from '../lib/focus';
import { absolute } from '../lib/format';
import { useLive } from '../stores/live';
import { useUi } from '../stores/ui';
import UiButton from '../ui/Button.vue';
import UiDialog from '../ui/Dialog.vue';
import UiField from '../ui/Field.vue';
import Glyph from '../ui/marks/Glyph.vue';
import { fromLocalInput, localInput, windowProblem, type WindowInput, type WindowView } from './updates';

const { windows, status, error } = defineProps<{ windows: WindowView[]; status: 'loading' | 'ready' | 'error'; error: string }>();
const emit = defineEmits<{ changed: []; retry: [] }>();
const ui = useUi();
const live = useLive();
const uid = useId();

const canEdit = computed(() => ui.can.value.role === 'owner' || ui.can.value.role === 'admin');

/** Hosts a window can be for: hosts, guests and Docker engines in the graph. */
const hostChoices = computed(() => {
  const res = live.graph.value?.resources ?? {};
  return Object.values(res)
    .filter((r) => ['host', 'vm', 'lxc', 'engine'].includes(r.kind))
    .map((r) => ({ id: r.id, name: r.name || r.id, kind: r.kind }))
    .sort((a, b) => a.name.localeCompare(b.name) || a.id.localeCompare(b.id));
});
const KIND_WORDS: Record<string, string> = { host: 'host', vm: 'VM', lxc: 'container (LXC)', engine: 'Docker' };
function hostName(id: string): string {
  return hostChoices.value.find((h) => h.id === id)?.name ?? id;
}

// ── the form ─────────────────────────────────────────────────────────────

const editing = ref<WindowView | 'new'>();
const form = ref({ name: '', hosts: [] as string[], start: '', end: '', weekly: true });
const formError = ref('');
const saving = ref(false);

function openForm(w?: WindowView): void {
  editing.value = w ?? 'new';
  formError.value = '';
  if (w) {
    form.value = { name: w.name, hosts: [...w.hosts], start: localInput(w.starts_at), end: localInput(w.ends_at), weekly: w.weekly };
  } else {
    // Sunday 02:00–05:00 next week, in the browser's zone: a sensible start.
    const d = new Date();
    d.setDate(d.getDate() + ((7 - d.getDay()) % 7 || 7));
    d.setHours(2, 0, 0, 0);
    const end = new Date(d.getTime() + 3 * 3600_000);
    form.value = { name: '', hosts: [], start: localInput(d.toISOString()), end: localInput(end.toISOString()), weekly: true };
  }
}
function closeForm(): void {
  editing.value = undefined;
  void focusFirst(`${uid}-add`);
}

const input = computed<WindowInput>(() => ({
  name: form.value.name.trim(),
  hosts: form.value.hosts,
  starts_at: fromLocalInput(form.value.start),
  ends_at: fromLocalInput(form.value.end),
  weekly: form.value.weekly,
}));

async function save(): Promise<void> {
  if (saving.value) return;
  const problem = windowProblem(input.value);
  if (problem) {
    formError.value = problem;
    return;
  }
  saving.value = true;
  formError.value = '';
  try {
    const w = editing.value;
    if (w && w !== 'new') await api(`/api/windows/${encodeURIComponent(w.id)}`, { method: 'PUT', body: input.value });
    else await api('/api/windows', { method: 'POST', body: input.value });
    ui.toast(w && w !== 'new' ? `Saved ${input.value.name}.` : `Added ${input.value.name}.`);
    editing.value = undefined;
    emit('changed');
  } catch (err) {
    formError.value = hubWords(err, 'The window wasn’t saved.');
  } finally {
    saving.value = false;
  }
}

// ── delete ───────────────────────────────────────────────────────────────

const deleting = ref('');
const busy = ref('');
const rowError = ref<Record<string, string>>({});
function ask(w: WindowView): void {
  deleting.value = w.id;
  void focusFirst(`${uid}-c-${w.id}`);
}
function cancelDelete(w: WindowView): void {
  deleting.value = '';
  void focusFirst(`${uid}-d-${w.id}`);
}
async function remove(w: WindowView): Promise<void> {
  if (busy.value) return;
  busy.value = w.id;
  try {
    await api(`/api/windows/${encodeURIComponent(w.id)}`, { method: 'DELETE' });
    deleting.value = '';
    ui.toast(`Deleted ${w.name}.`);
    emit('changed');
    void focusFirst(`${uid}-h`);
  } catch (err) {
    rowError.value = { ...rowError.value, [w.id]: hubWords(err, 'The window wasn’t deleted.') };
  } finally {
    busy.value = '';
  }
}

function when(w: WindowView): string {
  const start = w.open ? w.starts_at : (w.next_open ?? w.starts_at);
  const end = w.open ? w.ends_at : (w.next_end ?? w.ends_at);
  if (w.open) return `Open now, until ${absolute(w.next_end ?? end)}`;
  if (!w.next_open) return `Over: it was ${absolute(w.starts_at)} to ${absolute(w.ends_at)}`;
  return `${w.weekly ? 'Weekly, next' : 'Once,'} ${absolute(start)} to ${absolute(end)}`;
}
</script>

<template>
  <section class="st-section" :aria-labelledby="`${uid}-h`">
    <header>
      <h3 :id="`${uid}-h`" tabindex="-1">Maintenance windows</h3>
      <UiButton v-if="canEdit" :id="`${uid}-add`" size="sm" @click="openForm()">Add window</UiButton>
    </header>
    <p class="st-lede">
      When updates may run, and when trouble is expected. While a window is open, incidents on its hosts don’t notify; one still open when it ends notifies
      then. A rollout can be scheduled into a window.
    </p>
    <div v-if="status === 'loading'" class="st-skel" aria-busy="true"><span class="skel"></span></div>
    <div v-else-if="status === 'error'" class="st-note bad" role="alert">
      <Glyph kind="down" /><span>{{ error }}</span>
      <UiButton size="sm" @click="emit('retry')">Try again</UiButton>
    </div>
    <p v-else-if="!windows.length" class="st-meta">No maintenance windows yet.</p>
    <ul v-else class="st-list" aria-label="Maintenance windows">
      <li v-for="w in windows" :key="w.id">
        <div class="st-row up-row">
          <Glyph :kind="w.open ? 'maint' : w.next_open ? 'prog' : 'off'" :label="w.open ? 'open' : w.next_open ? 'planned' : 'over'" />
          <span>
            <span class="t">{{ w.name }}</span>
            <span class="m">{{ when(w) }}</span>
            <span class="m">For {{ w.hosts.map(hostName).join(', ') }}</span>
            <span v-if="rowError[w.id]" class="err" role="alert">{{ rowError[w.id] }}</span>
          </span>
          <span v-if="canEdit && deleting !== w.id" class="end up-acts">
            <UiButton size="sm" :aria-label="`Edit ${w.name}`" @click="openForm(w)">Edit</UiButton>
            <UiButton variant="ghost" size="sm" :id="`${uid}-d-${w.id}`" :aria-label="`Delete ${w.name}`" @click="ask(w)">Delete</UiButton>
          </span>
        </div>
        <div v-if="deleting === w.id" class="st-confirm" role="group" :aria-labelledby="`${uid}-q-${w.id}`">
          <span :id="`${uid}-q-${w.id}`">Delete <b>{{ w.name }}</b>? Rollouts scheduled into it are cancelled.</span>
          <UiButton variant="danger-solid" size="sm" :aria-busy="busy === w.id || undefined" @click="remove(w)">Delete window</UiButton>
          <UiButton size="sm" :id="`${uid}-c-${w.id}`" @click="cancelDelete(w)">Cancel</UiButton>
        </div>
      </li>
    </ul>
  </section>

  <UiDialog
    v-if="editing"
    as="form"
    size="md"
    :title="editing === 'new' ? 'Add a maintenance window' : `Edit ${editing.name}`"
    close-label="Cancel"
    @close="closeForm"
    @submit="save"
  >
    <UiField label="Name" v-slot="{ control }">
      <input v-bind="control" v-model="form.name" maxlength="80" autocomplete="off" />
    </UiField>
    <UiField label="Starts" help="In this browser’s time zone." v-slot="{ control }">
      <input v-bind="control" v-model="form.start" type="datetime-local" />
    </UiField>
    <UiField label="Ends" v-slot="{ control }">
      <input v-bind="control" v-model="form.end" type="datetime-local" />
    </UiField>
    <UiField label="Every week" check v-slot="{ control }">
      <input v-bind="control" v-model="form.weekly" type="checkbox" />
    </UiField>
    <fieldset class="up-hosts">
      <legend>Hosts</legend>
      <p v-if="!hostChoices.length" class="m">Cha Dash doesn’t see any hosts yet.</p>
      <UiField v-for="h in hostChoices" :key="h.id" :label="`${h.name} (${KIND_WORDS[h.kind] ?? h.kind})`" check v-slot="{ control }">
        <input v-bind="control" v-model="form.hosts" type="checkbox" :value="h.id" />
      </UiField>
    </fieldset>
    <p v-if="formError" class="err" role="alert">{{ formError }}</p>
    <template #foot>
      <UiButton @click="closeForm">Cancel</UiButton>
      <UiButton variant="primary" type="submit" :aria-busy="saving || undefined">{{ editing === 'new' ? 'Add window' : 'Save' }}</UiButton>
    </template>
  </UiDialog>
</template>

<style>
.up-hosts {
  border: 0;
  padding: 0;
  margin: 8px 0 0;
  display: grid;
  gap: 2px;
  max-height: 14em;
  overflow: auto;
}
.up-hosts legend {
  font-weight: 600;
  padding: 0;
  margin-bottom: 4px;
}
</style>
