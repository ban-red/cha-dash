<script setup lang="ts">
/**
 * A plan waiting for a person, in the Inbox: an API token planned an action
 * above T1, or a git sync in "draft and ask" found a commit, and it runs only
 * if an admin or the owner approves it here (`class: "approval"`; the hub
 * sends these to approvers only).
 *
 * Built from the hub's plan, never from the requester's words: who asked
 * (the token or sync and its user), the hub's note (a sync's commit), what
 * it will do (the plan's steps), what it touches (the plan's impact), and
 * when the request expires.
 *
 *   POST /api/approvals/{id}/approve  {hash}  → 202 {task}; needs a step-up made after the request
 *   POST /api/approvals/{id}/deny     {reason?} → 200 {approval}
 *
 * Approve is the tier's confirmation (the hold ring for T2), then a fresh
 * step-up every time, then the approval bound to the plan's hash. Deny
 * needs neither. The item resolves live on the `inbox` topic either way.
 *
 * Its own chunk, loaded when the Inbox first shows an approval.
 */
import { computed, nextTick, ref, useId, useTemplateRef } from 'vue';
import Glyph from '../ui/marks/Glyph.vue';
import HoldToConfirm from './HoldToConfirm.vue';
import { api } from '../lib/api';
import { hubWords, needsStepUp } from '../lib/actions';
import { hhmm } from '../lib/format';
import { impactName, relationWords } from '../model/words';
import type { Incident, Task } from '../model/types';
import { useLive } from '../stores/live';
import { useUi } from '../stores/ui';
import UiButton from '../ui/Button.vue';
import UiField from '../ui/Field.vue';

const { incident } = defineProps<{ incident: Incident }>();
const live = useLive();
const ui = useUi();
const uid = useId();

const apv = computed(() => incident.approval);
const plan = computed(() => apv.value?.plan);
const steps = computed(() => plan.value?.steps ?? []);
const impact = computed(() => (plan.value?.impact ?? incident.blast_radius ?? []).slice(0, 10));
const who = computed(() => apv.value?.originator);
/** How the request came, after "Asked by <name>". */
const via = computed(() => {
  const o = who.value;
  switch (o?.kind) {
    case 'sync':
      return o.user_name ? `, a git sync acting for ${o.user_name}` : ', a git sync';
    case 'agent':
      return o.user_name ? `, an agent of ${o.user_name}’s, through the API` : ', an agent, through the API';
    case 'user':
      return '';
  }
  return o?.user_name ? `, a token of ${o.user_name}’s, through the API` : ', through the API';
});
const tier = computed(() => plan.value?.tier ?? 'T2');
/** T2 holds; anything higher would be typed (tokens never get there). */
const holds = computed(() => tier.value === 'T2');
const busy = ref<'' | 'approve' | 'deny'>('');
const error = ref('');
const progress = ref(0);
/** Bumped after each try, so the ring can be held again. */
const attempt = ref(0);
const denying = ref(false);
const reason = ref('');
const root = useTemplateRef<HTMLElement>('root');

/** Put focus on a control in this item, after Vue has drawn it. */
async function focusIn(selector: string): Promise<void> {
  await nextTick();
  root.value?.querySelector<HTMLElement>(selector)?.focus();
}

function startDeny(): void {
  denying.value = true;
  void focusIn('.apv-deny input');
}

function backFromDeny(): void {
  denying.value = false;
  void focusIn('.apv-deny-btn');
}

async function approve(): Promise<void> {
  const a = apv.value;
  if (!a || busy.value) return;
  busy.value = 'approve';
  error.value = '';
  try {
    // Every approval asks again: the hub wants a step-up made after the request.
    if (!(await ui.stepUp(`Approve “${a.plan.label}” for ${a.originator.name}`, true))) return;
    await api<{ task?: Task }>(`/api/approvals/${encodeURIComponent(a.id)}/approve`, { method: 'POST', body: { hash: a.plan.hash } });
    ui.toast(`Approved: ${a.plan.label}. It runs as ${a.originator.name}.`);
  } catch (err) {
    error.value = needsStepUp(err) ? 'The confirmation didn’t count for this request. Approve again to confirm it’s you.' : hubWords(err, 'It wasn’t approved.');
  } finally {
    busy.value = '';
    progress.value = 0;
    attempt.value++;
    // The ring is rebuilt for another try; keep focus on it rather than the page.
    const a = document.activeElement;
    if (!a || a === document.body || !a.isConnected || root.value?.contains(a)) void focusIn('.hold, .apv-approve');
  }
}

async function deny(): Promise<void> {
  const a = apv.value;
  if (!a || busy.value) return;
  busy.value = 'deny';
  error.value = '';
  try {
    await api(`/api/approvals/${encodeURIComponent(a.id)}/deny`, { method: 'POST', body: reason.value.trim() ? { reason: reason.value.trim() } : {} });
    ui.toast(`Denied: ${a.plan.label}.`);
    denying.value = false;
  } catch (err) {
    error.value = hubWords(err, 'It wasn’t denied.');
  } finally {
    busy.value = '';
  }
}

const lit = (i: number) => progress.value >= ((i + 0.5) / Math.max(1, impact.value.length)) * 0.92;
</script>

<template>
  <div ref="root" class="ib open apv" role="group" :aria-labelledby="`${uid}-t`">
    <Glyph kind="attn" label="waiting for approval" />
    <div>
      <div :id="`${uid}-t`" class="ib-title">{{ incident.title }}</div>
      <div class="m">
        Asked by <b>{{ who?.name ?? 'an API token' }}</b>{{ via }}.
        It runs only if you approve it.
      </div>
      <div v-if="plan?.note" class="m apv-note">{{ plan.note }}</div>
      <dl class="apv-meta">
        <div><dt>Action</dt><dd>{{ plan?.label }} <span class="chip">{{ tier }}</span></dd></div>
        <div><dt>Asked</dt><dd>{{ hhmm(apv?.requested_at ?? incident.opened_at) }}</dd></div>
        <div><dt>Expires</dt><dd>{{ hhmm(apv?.expires_at) }}</dd></div>
      </dl>
      <h4 class="apv-h">What Cha Dash will do</h4>
      <ol v-if="steps.length" class="apv-steps">
        <li v-for="(s, i) in steps" :key="i">{{ s.label }}</li>
      </ol>
      <h4 class="apv-h">What this affects</h4>
      <ul class="blast apv-blast">
        <li v-for="(x, i) in impact" :key="x.resource" :class="{ on: lit(i) }">
          <Glyph :kind="x.depth <= 1 ? 'attn' : 'ok'" />
          <span>{{ impactName(live.graph.value, x.resource) }}</span>
          <span class="via">{{ relationWords(x.via) }}</span>
        </li>
        <li v-if="!impact.length"><Glyph kind="ok" /><span>Nothing else, as far as the Home Graph knows</span></li>
      </ul>

      <p v-if="!ui.can.value.approve" class="sub">An admin or the owner can approve this.</p>
      <template v-else>
        <div v-if="!denying" class="apv-acts">
          <template v-if="holds">
            <HoldToConfirm :key="attempt" label="approve" :hint="`${uid}-hint`" @progress="progress = $event" @confirm="approve" />
            <span :id="`${uid}-hint`" class="hint">hold to approve · then confirm it’s you</span>
          </template>
          <UiButton v-else variant="primary" class="apv-approve" :disabled="!!busy || live.stale.value" @click="approve">Approve<span class="tier">{{ tier }}</span></UiButton>
          <UiButton variant="ghost" size="sm" class="apv-deny-btn" :disabled="!!busy || live.stale.value" @click="startDeny">Deny</UiButton>
        </div>
        <form v-else class="apv-deny" novalidate @submit.prevent="deny">
          <UiField>
            <template #label>Why not? <span class="opt-word">optional, shown to whoever asked</span></template>
            <template #default="{ control }">
              <input v-bind="control" v-model="reason" type="text" maxlength="300" autocomplete="off" />
            </template>
          </UiField>
          <div class="st-acts">
            <UiButton size="sm" type="submit" :disabled="!!busy">Deny it</UiButton>
            <UiButton variant="ghost" size="sm" @click="backFromDeny">Back</UiButton>
          </div>
        </form>
        <p v-if="busy === 'approve'" class="sub" role="status">Approving…</p>
      </template>
      <p v-if="error" class="err" role="alert">{{ error }}</p>
    </div>
    <span class="when"><span class="ib-kind">Approval</span></span>
  </div>
</template>

<style>
/* The approver reads every row before holding: no resting dim here. */
.apv-blast li {
  color: var(--text);
}
.apv-meta {
  display: flex;
  flex-wrap: wrap;
  gap: var(--space-2) var(--space-5);
  margin: var(--space-3) 0 0;
  font-size: var(--type-small);
}
.apv-meta dt {
  font: var(--weight-medium) var(--type-micro) / 1.2 var(--font-num);
  letter-spacing: var(--tracking-caps);
  text-transform: uppercase;
  color: var(--text-3);
}
.apv-meta dd {
  margin: 2px 0 0;
  color: var(--text);
}
.apv-h {
  margin: var(--space-3) 0 var(--space-1);
  font: var(--weight-medium) var(--type-micro) / 1.2 var(--font-num);
  letter-spacing: var(--tracking-caps);
  text-transform: uppercase;
  color: var(--text-3);
}
.apv-steps {
  margin: 0;
  padding-left: 1.2em;
  font-size: var(--type-small);
  line-height: 1.5;
  color: var(--text-2);
}
.apv-acts {
  display: flex;
  flex-wrap: wrap;
  align-items: center;
  gap: var(--space-3);
  margin-top: var(--space-3);
}
.apv-deny {
  display: flex;
  flex-direction: column;
  gap: var(--space-2);
  margin-top: var(--space-3);
}
</style>
