<script setup lang="ts">
/**
 * Settings → Updates → Rollouts: what "Update all" and scheduled rollouts
 * did, step by step, and the ones still to run. A running one's progress is
 * on the task sheet too; a scheduled or waiting one can be run now or
 * cancelled, and a running one cancelled before its next step (operator
 * and up): the step in progress finishes, or rolls back on its own.
 *
 *   GET  /api/updates/rollouts               → {rollouts}
 *   POST /api/updates/rollouts/{id}/cancel   → the rollout
 *   updates.rollout {rollout}                runs a scheduled or waiting one now
 */
import { computed, ref } from 'vue';
import { api } from '../lib/api';
import { hubWords } from '../lib/actions';
import { absolute, ago } from '../lib/format';
import { useLive } from '../stores/live';
import { useUi } from '../stores/ui';
import UiButton from '../ui/Button.vue';
import Glyph from '../ui/marks/Glyph.vue';
import type { GlyphKind } from '../ui/marks/Glyph.vue';
import type { Rollout, RolloutStep } from '../model/types';
import { ROLLOUT_ACTION, ROLLOUT_TARGET, ROLLOUT_WORDS, STEP_WORDS, cancellable, type WindowView } from './updates';

const { rollouts, windows, status, error } = defineProps<{ rollouts: Rollout[]; windows: WindowView[]; status: 'loading' | 'ready' | 'error'; error: string }>();
const emit = defineEmits<{ changed: []; retry: [] }>();
const ui = useUi();
const live = useLive();

const canRun = computed(() => ui.can.value.act('T2'));
const busy = ref('');

const GLYPH: Record<Rollout['state'], GlyphKind> = {
  scheduled: 'prog',
  waiting: 'attn',
  running: 'prog',
  succeeded: 'check',
  failed: 'down',
  cancelled: 'off',
};
const STEP_GLYPH: Record<RolloutStep['state'], GlyphKind> = {
  pending: 'prog',
  running: 'prog',
  succeeded: 'check',
  rolled_back: 'down',
  not_started: 'off',
  skipped: 'off',
};

function windowName(id?: string): string {
  return windows.find((w) => w.id === id)?.name ?? 'a maintenance window';
}

function counts(r: Rollout): string {
  const n = (s: RolloutStep['state']) => r.steps.filter((x) => x.state === s).length;
  const parts = [];
  if (n('succeeded')) parts.push(`${n('succeeded')} updated`);
  if (n('rolled_back')) parts.push(`${n('rolled_back')} rolled back`);
  if (n('not_started')) parts.push(`${n('not_started')} didn’t start`);
  if (n('skipped')) parts.push(`${n('skipped')} left out`);
  const waiting = n('pending') + n('running');
  if (waiting) parts.push(`${waiting} to go`);
  return parts.join(' · ');
}

function when(r: Rollout): string {
  const now = live.now.value;
  switch (r.state) {
    case 'scheduled':
      return `in ${windowName(r.window_id)}, from ${absolute(r.scheduled_for)}`;
    case 'waiting':
      return `was for ${windowName(r.window_id)}`;
    case 'running':
      return `started ${ago(r.started_at, now)}`;
    default:
      return ago(r.finished_at || r.created_at, now);
  }
}

async function cancel(r: Rollout): Promise<void> {
  if (busy.value) return;
  busy.value = r.id;
  try {
    await api(`/api/updates/rollouts/${encodeURIComponent(r.id)}/cancel`, { method: 'POST' });
    ui.toast(r.state === 'running' ? 'The rollout stops after the update that’s running.' : 'Cancelled the rollout.');
    emit('changed');
  } catch (err) {
    ui.toast(hubWords(err, 'The rollout wasn’t cancelled.'), 'error');
  } finally {
    busy.value = '';
  }
}

function runNow(r: Rollout): void {
  void ui.runAction({ action: ROLLOUT_ACTION, target: ROLLOUT_TARGET, params: { rollout: r.id }, label: 'Run the scheduled rollout now', tier: 'T2' });
}
</script>

<template>
  <section class="st-section" aria-labelledby="up-ro">
    <header>
      <h3 id="up-ro">Rollouts</h3>
    </header>
    <div v-if="status === 'loading'" class="st-skel" aria-busy="true"><span class="skel"></span></div>
    <div v-else-if="status === 'error'" class="st-note bad" role="alert">
      <Glyph kind="down" /><span>{{ error }}</span>
      <UiButton size="sm" @click="emit('retry')">Try again</UiButton>
    </div>
    <p v-else-if="!rollouts.length" class="st-meta">No rollouts in the last 30 days. Update all, or a selection, runs one.</p>
    <ul v-else class="st-list" aria-label="Rollouts">
      <li v-for="r in rollouts" :key="r.id">
        <details class="up-ro">
          <summary class="st-row up-row">
            <Glyph :kind="GLYPH[r.state]" :label="ROLLOUT_WORDS[r.state]" />
            <span>
              <span class="t">{{ r.cancelling ? 'Stopping after this step' : ROLLOUT_WORDS[r.state] }} · {{ when(r) }}</span>
              <span class="m">{{ counts(r) }}<template v-if="r.created_by"> · by {{ r.created_by }}</template></span>
            </span>
          </summary>
          <ol class="up-steps" :aria-label="`Steps of the rollout ${when(r)}`">
            <li v-for="s in r.steps" :key="s.target">
              <Glyph :kind="STEP_GLYPH[s.state]" />
              <span>
                <b>{{ s.name || s.target }}</b><template v-if="s.host"> on {{ s.host }}</template> · {{ STEP_WORDS[s.state] }}
                <template v-if="s.snapshot === 'pve'"> · snapshot first</template>
                <template v-else-if="s.snapshot === 'git'"> · stack committed</template>
                <span v-if="s.reason" class="m"> · {{ s.reason }}</span>
              </span>
            </li>
          </ol>
        </details>
        <div v-if="canRun && cancellable(r)" class="up-acts up-ro-acts">
          <UiButton v-if="r.state !== 'running'" size="sm" variant="primary" :aria-label="`Run the rollout ${when(r)} now`" @click="runNow(r)">
            Run now<span class="tier">T2</span>
          </UiButton>
          <UiButton
            size="sm"
            :disabled="busy === r.id"
            :aria-label="r.state === 'running' ? `Cancel the rollout ${when(r)} after the update that’s running` : `Cancel the rollout ${when(r)}`"
            @click="cancel(r)"
          >
            Cancel
          </UiButton>
        </div>
      </li>
    </ul>
  </section>
</template>

<style>
.up-ro > summary {
  cursor: pointer;
  list-style: none;
}
.up-ro > summary::-webkit-details-marker {
  display: none;
}
.up-steps {
  margin: 4px 0 8px;
  padding-left: 28px;
  display: grid;
  gap: 4px;
  list-style: none;
}
.up-steps li {
  display: flex;
  gap: 6px;
  align-items: baseline;
}
.up-steps .m {
  color: var(--text-2);
}
.up-ro-acts {
  justify-content: start;
  padding: 0 0 8px 28px;
}
</style>
