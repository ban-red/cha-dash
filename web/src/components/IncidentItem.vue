<script setup lang="ts">
/**
 * One incident in the Inbox: symptom first, then (expanded) the facts, the
 * likely causes, the blast radius and the action.
 *
 * - A Repair (an integration Cha Dash can't read) offers Reconnect: the
 *   integration's settings, with the field that failed focused. In demo mode
 *   its suggested action can also run, so the Demo Home can still settle it.
 * - A problem offers its suggested action and the alternatives through the
 *   safety ladder (stores/actions.ts): the hub's engine plans each one, and
 *   says why when one can't run here. Buttons the viewer's role can't use
 *   aren't shown. `link.open` actions open their link.
 *
 * Progress comes from the task working on it (the `tasks` topic): its current
 * step and progress, and Cancel while it runs when its action allows it. It
 * isn't a live region: TaskPresence announces step changes once, for every view. The
 * resolution arrives live from the `inbox` topic.
 */
import { computed } from 'vue';
import Glyph from '../ui/marks/Glyph.vue';
import { hhmm, since } from '../lib/format';
import { label } from '../model/graph';
import { glyphFor } from '../model/status';
import { factLabel, factValue, impactName, relationWords } from '../model/words';
import type { Action, Incident } from '../model/types';
import { useLive } from '../stores/live';
import { canMute, isMarked, mute, unmute } from '../stores/muted';
import { useUi } from '../stores/ui';
import UiButton from '../ui/Button.vue';

const { incident, expanded = false } = defineProps<{ incident: Incident; expanded?: boolean }>();
const emit = defineEmits<{ toggle: [] }>();
const live = useLive();
const ui = useUi();
/** The target's name when its kind has a sheet (10 · The resource sheet): the item links to it. */
const sheetName = computed(() => (incident.target && ui.hasSheet(incident.target) ? label(live.graph.value?.resources[incident.target]) || incident.target : ''));

/** A warning can be hidden from Needs attention until it turns critical (stores/muted.ts). */
const hideable = computed(() => !isResolved.value && canMute(incident));
const marked = computed(() => isMarked(incident));
function toggleHidden() {
  if (marked.value) {
    unmute(incident);
    ui.toast('Showing it in Needs attention again.');
  } else {
    mute(incident);
    ui.toast(`Hidden until it turns critical: ${incident.title}.`, 'ok', 8000, { label: 'Undo', run: () => unmute(incident) });
  }
}

const isResolved = computed(() => incident.status === 'resolved');
const isRepair = computed(() => incident.class === 'repair');
const fix = computed(() => live.fixes[incident.id]);
const task = computed(() => ui.taskFor(incident));
const taskRunning = computed(() => !!task.value && !['succeeded', 'failed', 'cancelled'].includes(task.value.state));
const working = computed(() => !isResolved.value && (fix.value?.state === 'accepted' || taskRunning.value) && fix.value?.state !== 'failed');
const pct = computed(() => (typeof task.value?.progress === 'number' ? Math.round(Math.min(1, Math.max(0, task.value.progress)) * 100) : undefined));
/** The progress ring: the task's progress when it has one, else a quarter (started) or half (the phase moved on). */
const ringOffset = computed(() => {
  if (pct.value !== undefined) return 31.4 * (1 - pct.value / 100);
  return incident.phase === fix.value?.phase ? 23 : 12;
});
const canCancel = computed(() => !!task.value?.cancellable && taskRunning.value && !live.stale.value);
const workingWords = computed(() => {
  const t = task.value;
  if (t && taskRunning.value) {
    const step = t.state === 'queued' ? 'waiting its turn' : t.state === 'verifying' ? 'checking it worked' : t.step || 'working';
    return `${t.label}: ${step}${pct.value !== undefined ? ` · ${pct.value}%` : ''}. This updates by itself.`;
  }
  return `${action.value?.label ?? t?.label ?? 'Fix'}: working${incident.phase ? ` · ${incident.phase}` : ''}. This updates by itself.`;
});
/** The instance whose recipe would make the action available. */
const recipe = computed(() => fix.value?.recipe ?? (action.value?.unavailable ? action.value.recipe : undefined));
const glyph = computed(() => (isResolved.value ? 'check' : isRepair.value ? 'repair' : glyphFor(incident.severity)));
const glyphLabel = computed(() => (isResolved.value ? 'resolved' : isRepair.value ? 'repair' : incident.severity === 'down' ? 'down' : 'needs attention'));
// A value too long for the right-hand column (an error, a fingerprint) goes
// under its label and wraps.
const facts = computed(() =>
  Object.entries(incident.facts ?? {})
    .slice(0, 6)
    .map(([k, v]) => {
      const value = factValue(v, hhmm);
      return { key: k, label: factLabel(k), value, long: value.length > 28 };
    }),
);
const blast = computed(() => (incident.blast_radius ?? []).slice(0, 8));
const action = computed(() => incident.suggested);
const primary = computed(() => ui.canRun(incident));
/** For a repair, the Demo Home can also run its suggested action, which settles it without settings. */
const demoRepairAction = computed(() => (isRepair.value && live.demo.value && action.value ? action.value : undefined));
const runnable = (a: Action) => ui.canRun(incident, a);
const whenText = computed(() => (isResolved.value ? hhmm(incident.resolved_at ?? incident.updated_at) : hhmm(incident.opened_at)));
const id = computed(() => `ib-${incident.id}`);
</script>

<template>
  <div :class="['ib', { open: expanded, repair: isRepair }]">
    <Glyph :kind="glyph" :label="glyphLabel" />
    <div>
      <button type="button" class="ib-title" :aria-expanded="expanded" :aria-controls="id" @click="emit('toggle')">{{ incident.title }}</button>
      <div class="m">{{ isResolved && incident.resolution ? incident.resolution : incident.summary }}</div>
    </div>
    <span class="when" :title="isResolved ? 'resolved' : 'opened'"><span v-if="isRepair && !isResolved" class="ib-kind">Repair · </span>{{ whenText }}</span>

    <div v-if="!expanded && isRepair && !isResolved && ui.can.value.integrations" class="ib-quick">
      <UiButton size="sm" :disabled="live.stale.value" @click="ui.reconnect(incident)">Reconnect</UiButton>
    </div>

    <div v-if="expanded && !isResolved" :id="id" class="flow">
      <div v-if="working" class="working">
        <svg class="prog" viewBox="0 0 12 12" aria-hidden="true">
          <circle class="track" cx="6" cy="6" r="5" />
          <circle class="arc" cx="6" cy="6" r="5" stroke-dasharray="31.4" :stroke-dashoffset="ringOffset" />
        </svg>
        <span>{{ workingWords }}</span>
        <UiButton v-if="canCancel && task" variant="ghost" size="sm" class="working-cancel" :disabled="ui.cancelling[task.id]" :aria-label="`Cancel ${task.label}`" @click="ui.cancelTask(task)">
          {{ ui.cancelling[task.id] ? 'Cancelling…' : 'Cancel' }}
        </UiButton>
      </div>

      <div v-else-if="isRepair" class="col">
        <h4>Repair · T0</h4>
        <p v-if="ui.can.value.integrations" class="action-detail">
          Cha Dash can’t read this integration, but the service itself may be fine. Reconnect opens its settings with the field that failed, so you can paste a new
          credential and test it before saving.
        </p>
        <p v-else class="action-detail">Cha Dash can’t read this integration, but the service itself may be fine. Ask an admin or the owner to reconnect it.</p>
        <div class="acts">
          <UiButton v-if="ui.can.value.integrations" variant="primary" :disabled="live.stale.value" @click="ui.reconnect(incident)">Reconnect<span class="tier">T0</span></UiButton>
          <UiButton v-if="demoRepairAction" :disabled="!runnable(demoRepairAction).ok" :title="runnable(demoRepairAction).why ?? 'Runs in the Demo Home'" @click="ui.act(incident, demoRepairAction)">
            {{ demoRepairAction.label }}<span class="tier">{{ demoRepairAction.tier }}</span>
          </UiButton>
          <template v-for="alt in incident.alternatives ?? []" :key="alt.id + alt.label">
            <UiButton v-if="alt.id === 'link.open'" :disabled="!runnable(alt).ok" :title="runnable(alt).why" @click="ui.act(incident, alt)">
              {{ alt.label }}<span class="tier">{{ alt.tier }}</span>
            </UiButton>
          </template>
        </div>
        <p v-if="demoRepairAction" class="sub">In the Demo Home, “{{ demoRepairAction.label }}” settles this one without opening settings.</p>
        <p v-if="fix?.state === 'failed'" class="err" role="alert">{{ fix.error }}</p>
      </div>

      <div v-else-if="action" class="col">
        <h4>Suggested · {{ action.tier }}</h4>
        <p v-if="action.detail" class="action-detail">{{ action.detail }}</p>
        <div class="acts">
          <UiButton v-if="!primary.hidden" variant="primary" :disabled="!primary.ok" :title="primary.why" @click="ui.act(incident)">
            {{ action.label }}<span class="tier">{{ action.tier }}</span>
          </UiButton>
          <template v-for="alt in incident.alternatives ?? []" :key="alt.id + alt.label">
            <UiButton v-if="!runnable(alt).hidden" :disabled="!runnable(alt).ok" :title="runnable(alt).why" @click="ui.act(incident, alt)">
              {{ alt.label }}<span class="tier">{{ alt.tier }}</span>
            </UiButton>
          </template>
        </div>
        <p v-if="primary.hidden" class="sub">Your role can see this but not run “{{ action.label }}”. Ask an admin or the owner.</p>
        <p v-else-if="!primary.ok && primary.why && !live.stale.value && fix?.state !== 'failed'" class="sub">{{ primary.why }}</p>
        <p v-if="fix?.state === 'failed'" class="err" role="alert">{{ fix.error }}</p>
        <UiButton v-if="recipe && ui.can.value.integrations" size="sm" @click="ui.openSettings('integrations', recipe, { section: 'control' })">Open its settings</UiButton>
      </div>

      <p v-if="sheetName || hideable" class="sheet-link">
        <UiButton v-if="sheetName" size="sm" @click="ui.openSheet(incident.target)">Open {{ sheetName }}</UiButton>
        <UiButton v-if="hideable" variant="ghost" size="sm" :aria-pressed="marked" @click="toggleHidden">
          {{ marked ? 'Hidden until critical · Show again' : 'Hide until critical' }}
        </UiButton>
      </p>

      <div v-if="facts.length">
        <h4>What Cha Dash saw</h4>
        <dl class="kv">
          <template v-for="f in facts" :key="f.key">
            <dt :class="{ long: f.long }">{{ f.label }}</dt>
            <dd :class="{ long: f.long }">{{ f.value }}</dd>
          </template>
        </dl>
      </div>

      <div v-if="incident.causes?.length">
        <h4>Likely causes</h4>
        <ul class="rel causes">
          <li v-for="(c, i) in incident.causes" :key="i">
            <span>{{ c.text }}</span>
            <span v-if="c.resource || c.at">{{ c.resource ? label(live.graph.value?.resources[c.resource]) || c.resource : '' }}{{ c.at ? ` · ${since(c.at, live.now.value)}` : '' }}</span>
          </li>
        </ul>
      </div>

      <div v-if="blast.length">
        <h4>Blast radius · {{ incident.blast_radius?.length }}</h4>
        <ul class="rel">
          <li v-for="b in blast" :key="b.resource">
            <span>{{ impactName(live.graph.value, b.resource) }}</span>
            <span>{{ relationWords(b.via) }}</span>
          </li>
        </ul>
      </div>
      <p v-if="incident.resolves_when" class="sub">{{ incident.resolves_when }}</p>
    </div>

    <div v-else-if="expanded && isResolved" :id="id" class="flow">
      <div class="done-row" role="status">
        <svg viewBox="0 0 16 16" aria-hidden="true"><path d="m3 8.5 3 3 7-7" /></svg>
        <span>{{ incident.resolution ?? 'Resolved.' }}{{ incident.resolved_by === 'auto' ? ' It cleared by itself.' : '' }}</span>
      </div>
    </div>
  </div>
</template>
