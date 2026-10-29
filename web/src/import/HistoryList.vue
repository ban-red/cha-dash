<script setup lang="ts">
/**
 * History: every applied import, newest first (GET /api/imports). Open goes
 * to its board; Import again opens Import with its source; Revert plans
 * first (a dry run listing what goes and what stays, and why), then runs,
 * asking for a step-up when it removes connections.
 */
import { onMounted, ref, shallowRef } from 'vue';
import Glyph from '../components/marks/Glyph.vue';
import { ApiError, api } from '../lib/api';
import { absolute, plural } from '../lib/format';
import { navigate } from '../lib/router';
import { errorWords, loadInstances } from '../stores/integrations';
import { useUi } from '../stores/ui';
import { sourceInfo, type SourceId } from './model';

const emit = defineEmits<{ again: [source: SourceId] }>();
const ui = useUi();

interface Entry {
  id: string;
  source: string;
  actor?: string;
  created_at: number;
  files: string[];
  target: string;
  board_id?: string;
  created: { board?: string; instances: string[]; secrets: string[]; items: string[] };
  counts: { created: number; updated: number; tiles: number; instances: number; secrets: number };
  reverted_at?: number;
}
interface RevertItem {
  kind: string;
  id: string;
  name?: string;
  why?: string;
}
interface RevertPlan {
  remove: RevertItem[];
  keep: RevertItem[];
  connections: boolean;
}

const list = shallowRef<Entry[]>([]);
const state = ref<'loading' | 'ready' | 'error'>('loading');
const error = ref('');
const planning = ref('');
const plan = shallowRef<{ id: string; plan: RevertPlan } | undefined>();
const reverting = ref(false);

async function load(): Promise<void> {
  try {
    list.value = (await api<{ imports: Entry[] }>('/api/imports')).imports ?? [];
    state.value = 'ready';
  } catch (err) {
    state.value = 'error';
    error.value = errorWords(err, 'Couldn’t load the history.');
  }
}

const name = (s: string) => sourceInfo(s)?.name ?? s;
const itemWords = (i: RevertItem) => (i.kind === 'tile' ? `the tile ${i.name ?? i.id}` : `${i.kind === 'board' ? 'the board' : 'the connection'} ${i.name ?? i.id}`);

async function planRevert(e: Entry): Promise<void> {
  planning.value = e.id;
  try {
    plan.value = { id: e.id, plan: await api<RevertPlan>(`/api/imports/${encodeURIComponent(e.id)}/revert?dry_run=1`, { method: 'POST' }) };
  } catch (err) {
    ui.toast(errorWords(err, 'Couldn’t plan the revert.'), 'error');
  } finally {
    planning.value = '';
  }
}

async function revert(): Promise<void> {
  const p = plan.value;
  if (!p) return;
  reverting.value = true;
  const run = () => api<RevertPlan>(`/api/imports/${encodeURIComponent(p.id)}/revert`, { method: 'POST' });
  try {
    let done: RevertPlan;
    try {
      done = await run();
    } catch (err) {
      if (!(err instanceof ApiError && err.code === 'step_up_required')) throw err;
      if (!(await ui.stepUp('Revert an import that removes connections'))) return;
      done = await run();
    }
    ui.toast(`Reverted: ${plural(done.remove.length, 'thing')} removed${done.keep.length ? `, ${done.keep.length} kept` : ''}.`);
    plan.value = undefined;
    void loadInstances(true);
    await load();
  } catch (err) {
    ui.toast(errorWords(err, 'The revert didn’t run. Nothing changed.'), 'error');
  } finally {
    reverting.value = false;
  }
}

onMounted(load);
</script>

<template>
  <section class="st-section" aria-labelledby="hist-h">
    <header><h3 id="hist-h">Imports</h3></header>
    <p v-if="state === 'loading'" class="imp-quiet" aria-busy="true">Loading…</p>
    <p v-else-if="state === 'error'" class="err" role="alert">{{ error }}</p>
    <p v-else-if="!list.length" class="imp-quiet">Nothing imported yet.</p>
    <ul v-else class="rel hist">
      <li v-for="e in list" :key="e.id">
        <span>
          <b>{{ name(e.source) }}</b> · {{ absolute(e.created_at) }}
          <template v-if="e.files.length"> · <span class="n">{{ e.files.join(', ') }}</span></template>
          <br />
          <span class="imp-quiet">
            {{ plural(e.counts.created, 'thing') }} created, {{ e.counts.updated }} updated · {{ plural(e.counts.tiles, 'tile') }} · {{ plural(e.created.instances.length, 'new connection') }}
            <template v-if="e.target === 'none'"> · connections only</template>
            <template v-if="e.reverted_at"> · reverted {{ absolute(e.reverted_at) }}</template>
          </span>
        </span>
        <span class="hist-acts">
          <button v-if="e.board_id && !e.reverted_at" type="button" class="btn sm" @click="navigate(`/boards/${encodeURIComponent(e.board_id)}`)">Open</button>
          <button v-if="sourceInfo(e.source)" type="button" class="btn sm" @click="emit('again', e.source as SourceId)">Import again</button>
          <button v-if="!e.reverted_at" type="button" class="btn sm ghost" :disabled="planning === e.id" :aria-busy="planning === e.id" @click="planRevert(e)">Revert…</button>
        </span>
      </li>
    </ul>

    <div v-if="plan" class="hist-plan" role="region" aria-labelledby="hist-plan-h">
      <h4 id="hist-plan-h">Revert this import?</h4>
      <p v-if="!plan.plan.remove.length" class="imp-quiet">Nothing it made is left unchanged, so there’s nothing to remove.</p>
      <ul v-else class="rel">
        <li v-for="i in plan.plan.remove" :key="i.kind + i.id"><span><Glyph kind="down" /> Removes {{ itemWords(i) }}</span><span>{{ i.kind }}</span></li>
      </ul>
      <ul v-if="plan.plan.keep.length" class="rel">
        <li v-for="i in plan.plan.keep" :key="'k' + i.kind + i.id"><span><Glyph kind="ok" /> Keeps {{ itemWords(i) }}: {{ i.why }}</span><span>{{ i.kind }}</span></li>
      </ul>
      <p class="imp-quiet">What the import updated rather than created stays as it is now.<template v-if="plan.plan.connections"> Removing connections asks you to confirm it’s you.</template></p>
      <div class="st-acts">
        <button type="button" class="btn primary" :disabled="reverting || !plan.plan.remove.length" :aria-busy="reverting" @click="revert">Revert<span class="tier">T3</span></button>
        <button type="button" class="btn" :disabled="reverting" @click="plan = undefined">Keep it</button>
      </div>
    </div>
  </section>
</template>

<style>
.hist li {
  align-items: flex-start;
}
.hist-acts {
  display: inline-flex;
  flex-wrap: wrap;
  gap: var(--space-2);
}
.hist-plan {
  display: flex;
  flex-direction: column;
  gap: var(--space-2);
  margin-top: var(--space-4);
  padding: var(--space-4);
  border: var(--hairline) solid var(--line-2);
  border-radius: var(--radius-md);
}
.hist-plan h4 {
  margin: 0;
}
</style>
