<script setup lang="ts">
/**
 * Update Cha Dash (Settings → Stacks, owner and admin): the node beside the
 * hub pulls the hub's image at a tag, pins its digest, recreates the hub's
 * container and holds it to its healthcheck, putting the previous image back
 * on its own if the check fails. It's `chadash.update` on `node:<id>` through
 * the safety ladder: T3, so typed ("Cha Dash") and a step-up.
 *
 * The hub restarts during the update, so this page loses it for a moment;
 * the task is resumed when the hub is back, and its result shows here once
 * the connection returns. The last update's result comes from
 * `GET /api/nodes/{id}/self-update`, asked of each online node; the section
 * shows for the node that can update the hub, and says why not when a node
 * may but can't. Nothing shows when no node may.
 */
import { computed, onMounted, ref, useId, watch } from 'vue';
import Glyph from '../ui/marks/Glyph.vue';
import UiButton from '../ui/Button.vue';
import UiField from '../ui/Field.vue';
import { ago, absolute } from '../lib/format';
import { isDone } from '../lib/actions';
import { useLive } from '../stores/live';
import { useUi } from '../stores/ui';
import type { Task } from '../model/types';
import type { StackNode } from './stacks';
import { HUB_IMAGE, OUTCOME_WORDS, SELF_TARGET, selfUpdateStatus, shortImage, tagProblem, type SelfUpdateStatus } from './fleet';

const props = defineProps<{ nodes: StackNode[] }>();

const ui = useUi();
const live = useLive();
const uid = useId();
const now = ref(Date.now());

const statuses = ref<SelfUpdateStatus[]>([]);
const state = ref<'loading' | 'ready'>('loading');

const online = computed(() =>
  props.nodes
    .filter((n) => n.online && !n.error)
    .map((n) => n.node)
    .sort()
    .join('|'),
);

async function load(): Promise<void> {
  const ids = online.value ? online.value.split('|') : [];
  const got = await Promise.all(ids.map((id) => selfUpdateStatus(id).catch(() => undefined)));
  statuses.value = got.filter((s): s is SelfUpdateStatus => !!s);
  state.value = 'ready';
  now.value = Date.now();
}
watch(online, () => void load());
onMounted(load);

/** The node that updates the hub: the one that can, else one that may but can't (to say why). */
const here = computed(() => statuses.value.find((s) => s.available) ?? statuses.value.find((s) => s.allowed));
const nodeName = computed(() => {
  const n = props.nodes.find((x) => x.node === here.value?.node);
  return n?.name || n?.node || here.value?.node || '';
});
const canRun = computed(() => ui.can.value.act('T3'));

// ── the update task ──

const task = computed<Task | undefined>(() => {
  const target = here.value ? SELF_TARGET(here.value.node) : '';
  return Object.values(ui.tasks.value)
    .filter((t) => t.action === 'chadash.update' && t.target === target)
    .sort((a, b) => (b.started_at ?? '').localeCompare(a.started_at ?? ''))[0];
});
const running = computed(() => !!task.value && !isDone(task.value));
const reconnecting = computed(() => running.value && live.conn.status !== 'live');
const pct = computed(() => (typeof task.value?.progress === 'number' ? Math.round(Math.min(1, Math.max(0, task.value.progress)) * 100) : undefined));
// When the task ends, the last result is the hub's to tell: ask again.
watch(
  () => (task.value && isDone(task.value) ? task.value.id : ''),
  (id, was) => {
    if (id && id !== was) void load();
  },
);

const outcome = computed(() => {
  const t = task.value;
  if (!t || !isDone(t)) return undefined;
  if (t.state === 'succeeded') return { tone: 'ok', headline: 'Cha Dash is updated and healthy.', message: '' };
  switch (t.error?.code) {
    case 'rolled_back':
      return { tone: 'attn', headline: 'The new version failed its health check, so the previous one is back.', message: t.error.message };
    case 'stuck':
      return { tone: 'bad', headline: 'The update failed, and so did putting the previous version back.', message: t.error.message };
    case 'blocked':
      return { tone: 'attn', headline: 'Nothing was changed.', message: t.error.message };
    case 'interrupted':
      return { tone: 'attn', headline: 'Cha Dash restarted before the update started.', message: t.error.message };
    default:
      return { tone: 'bad', headline: 'The update didn’t run.', message: t.error?.message ?? '' };
  }
});

// ── the form ──

const tag = ref('');
const tagError = ref('');
function update(): void {
  const h = here.value;
  if (!h?.available || running.value) return;
  tagError.value = tagProblem(tag.value);
  if (tagError.value) return;
  const image = tag.value.trim();
  const short = image.startsWith(`${HUB_IMAGE}:`) ? image.slice(HUB_IMAGE.length + 1) : image;
  void ui.runAction({ action: 'chadash.update', target: SELF_TARGET(h.node), params: { image: short }, label: `Update Cha Dash to ${short}`, tier: 'T3' });
}

const last = computed(() => here.value?.last ?? null);
</script>

<template>
  <section v-if="state === 'ready' && here" class="st-section sku" :aria-labelledby="`${uid}-h`">
    <header>
      <h3 :id="`${uid}-h`">Cha Dash itself</h3>
      <span class="st-meta">on {{ nodeName }}</span>
    </header>

    <div v-if="!here.available" class="st-note hatched" role="note">
      <Glyph kind="off" /><span>{{ here.reason || `${nodeName} can’t update Cha Dash.` }}</span>
    </div>

    <template v-else>
      <p class="st-lede">
        The hub runs <span class="st-mono">{{ shortImage(here.running) }}</span>. Updating pulls a tag of <span class="st-mono">{{ HUB_IMAGE }}</span>, pins its digest
        and recreates the hub alone; the node and your stacks stay as they are. If the new version doesn’t pass its health check within 2 minutes, the previous one
        comes back on its own.
      </p>

      <!-- the update running, and how it ended -->
      <div v-if="task && (running || outcome)" :class="['sk-task', running ? 'live' : outcome?.tone]">
        <template v-if="running">
          <div class="sk-task-h">
            <Glyph kind="prog" />
            <b>{{ task.label }}</b>
            <span v-if="!reconnecting">· {{ task.step || 'Starting' }}<template v-if="pct !== undefined"> · {{ pct }}%</template></span>
          </div>
          <p class="sk-task-m" role="status">
            <template v-if="reconnecting">Cha Dash is restarting. This page reconnects on its own and shows the result when the hub is back.</template>
            <template v-else>Cha Dash restarts during the update, so this page loses the hub for a moment.</template>
          </p>
        </template>
        <template v-else-if="outcome">
          <div class="sk-task-h">
            <Glyph :kind="outcome.tone === 'ok' ? 'check' : outcome.tone === 'attn' ? 'attn' : 'down'" />
            <b>{{ outcome.headline }}</b>
          </div>
          <p v-if="outcome.message" class="sk-task-m">{{ outcome.message }}</p>
        </template>
      </div>

      <form v-if="canRun" class="sku-form" novalidate @submit.prevent="update">
        <UiField label="Image tag" :error="tagError" alert :help="`A tag of ${HUB_IMAGE}, such as 0.3.0. The node pins the digest it points at.`" v-slot="{ control }">
          <input v-bind="control" v-model="tag" type="text" autocomplete="off" spellcheck="false" placeholder="0.3.0" :disabled="running" @input="tagError = ''" />
        </UiField>
        <UiButton type="submit" variant="primary" :disabled="running">Update Cha Dash<span class="tier">T3</span></UiButton>
      </form>
      <p v-else class="st-meta">Only admins and the owner can update Cha Dash.</p>
    </template>

    <p v-if="last && last.outcome !== 'running'" class="st-meta sku-last">
      <Glyph :kind="OUTCOME_WORDS[last.outcome]?.glyph ?? 'off'" />
      Last update: {{ OUTCOME_WORDS[last.outcome]?.words ?? last.outcome }} · <span class="st-mono">{{ shortImage(last.image || last.current) }}</span>
      <template v-if="last.finished_at"> · <span :title="absolute(last.finished_at)">{{ ago(last.finished_at, now) }}</span></template>
      <template v-if="last.reason && last.outcome !== 'deployed'">. {{ last.reason }}</template>
    </p>
  </section>
</template>

<style>
.sku-form {
  display: flex;
  flex-wrap: wrap;
  align-items: flex-end;
  gap: 8px 12px;
}
.sku-form .field {
  flex: 1 1 14rem;
  margin: 0;
}
.sku-last {
  display: flex;
  flex-wrap: wrap;
  align-items: center;
  gap: 4px;
}
.sku-last .st-mono {
  overflow-wrap: anywhere;
}
</style>
