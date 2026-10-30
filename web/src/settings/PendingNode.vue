<script setup lang="ts">
/**
 * A node waiting for approval (Settings → Nodes): it enrolled with a join
 * token and the hub refuses its sessions until an admin approves it.
 *
 * The card shows what the admin is approving: the address it dialled from,
 * its key's fingerprint (to compare with the node's own log), its version,
 * what its policy file allows, and who made the token and when. Approve
 * needs a step-up: an approved node gets stack secrets when Cha Dash
 * deploys there. Reject is the revoke, with a confirm.
 *
 *   POST /api/nodes/{id}/approve   step-up; 409 when it isn't pending any more
 *   POST /api/nodes/{id}/revoke    Reject
 *
 * Emits `changed` with a sentence for the toast after either.
 */
import { computed, ref, useId } from 'vue';
import { api } from '../lib/api';
import { hubWords } from '../lib/actions';
import { focusFirst } from '../lib/focus';
import { absolute, ago } from '../lib/format';
import { StepUpCancelled } from '../stores/actions';
import { useLive } from '../stores/live';
import { useUi } from '../stores/ui';
import UiButton from '../ui/Button.vue';
import Glyph from '../ui/marks/Glyph.vue';
import { grantsOf, type HubNode } from './nodes';

const { node } = defineProps<{ node: HubNode }>();
const emit = defineEmits<{ changed: [message: string] }>();
const live = useLive();
const ui = useUi();
const uid = useId();

const grants = computed(() => grantsOf(node));
const tok = computed(() => node.join_token);
const busy = ref<'' | 'approve' | 'reject'>('');
const error = ref('');
const rejecting = ref(false);

async function approve(): Promise<void> {
  if (busy.value) return;
  busy.value = 'approve';
  error.value = '';
  try {
    await ui.withStepUp(`Approve the node ${node.name}`, () => api(`/api/nodes/${encodeURIComponent(node.id)}/approve`, { method: 'POST' }));
    emit('changed', `Approved ${node.name}. It connects on its next try, within 30 seconds.`);
  } catch (err) {
    if (!(err instanceof StepUpCancelled)) error.value = hubWords(err, `${node.name} wasn’t approved.`);
  } finally {
    busy.value = '';
  }
}

function askReject(): void {
  rejecting.value = true;
  void focusFirst(`${uid}-rjc`);
}
function cancelReject(): void {
  rejecting.value = false;
  void focusFirst(`${uid}-rj`);
}
async function reject(): Promise<void> {
  if (busy.value) return;
  busy.value = 'reject';
  error.value = '';
  try {
    await api(`/api/nodes/${encodeURIComponent(node.id)}/revoke`, { method: 'POST' });
    rejecting.value = false;
    emit('changed', `Rejected ${node.name}. Its key won’t work again.`);
  } catch (err) {
    error.value = hubWords(err, `${node.name} wasn’t rejected.`);
  } finally {
    busy.value = '';
  }
}
</script>

<template>
  <article class="nd-pending" :aria-labelledby="`${uid}-h`">
    <header class="nd-pending-head">
      <Glyph kind="attn" />
      <h4 :id="`${uid}-h`">{{ node.name }} wants to join</h4>
      <span class="st-meta">asked <span :title="absolute(node.created_at)">{{ ago(node.created_at, live.now.value) }}</span></span>
    </header>
    <dl class="nd-kv">
      <dt>Fingerprint</dt>
      <dd>
        <span class="st-mono nd-fp">{{ node.fingerprint }}</span>
        <small class="nd-fp-help">Compare it with the node’s log: it prints “waiting for approval in Cha Dash; fingerprint …”.</small>
      </dd>
      <dt>Address</dt>
      <dd>{{ node.address || 'not seen yet' }}</dd>
      <dt>Version</dt>
      <dd>{{ node.version || 'unknown' }}</dd>
      <dt>Token</dt>
      <dd v-if="tok">
        made by {{ tok.created_by_name || 'a removed account' }} <span :title="absolute(tok.created_at)">{{ ago(tok.created_at, live.now.value) }}</span>
      </dd>
      <dd v-else>—</dd>
    </dl>
    <template v-if="grants.length">
      <h5 class="nd-h4">What its policy allows</h5>
      <ul class="nd-grants">
        <li v-for="g in grants" :key="g.policy">
          <Glyph :kind="g.allowed ? 'check' : 'cross'" :label="g.allowed ? 'allowed' : 'refused'" />
          <span>{{ g.words }}</span>
          <small class="st-mono">{{ g.policy }}</small>
        </li>
      </ul>
    </template>
    <p class="st-lede">
      Approving doesn’t widen its policy; {{ node.name }}’s own policy file still decides what it does. It does let the hub send it stack secrets.
      <template v-if="tok?.add_docker"> Its Docker integration is added when you approve it.</template>
    </p>
    <div v-if="!rejecting" class="st-acts">
      <UiButton variant="primary" size="sm" :aria-disabled="busy !== '' || undefined" :aria-busy="busy === 'approve' || undefined" @click="approve">
        Approve {{ node.name }} <span class="tier">T3</span>
      </UiButton>
      <UiButton variant="danger" size="sm" :id="`${uid}-rj`" :aria-disabled="busy !== '' || undefined" @click="askReject">Reject</UiButton>
    </div>
    <div v-else class="st-confirm" role="group" :aria-labelledby="`${uid}-rjq`">
      <span :id="`${uid}-rjq`">Reject {{ node.name }}? Its key never works on this hub again; to add the host later, make a new join token.</span>
      <UiButton variant="danger-solid" size="sm" :aria-disabled="busy !== '' || undefined" :aria-busy="busy === 'reject' || undefined" @click="reject">Reject {{ node.name }}</UiButton>
      <UiButton size="sm" :id="`${uid}-rjc`" @click="cancelReject">Cancel</UiButton>
    </div>
    <p v-if="error" class="err" role="alert">{{ error }}</p>
  </article>
</template>

<style>
.nd-pending {
  display: flex;
  flex-direction: column;
  gap: var(--space-3);
  padding: var(--space-4);
  border: var(--hairline) solid var(--line);
  border-left: 3px solid var(--attention);
  border-radius: var(--radius-lg);
}
.nd-pending-head {
  display: flex;
  align-items: baseline;
  gap: var(--space-2);
  flex-wrap: wrap;
}
.nd-pending-head h4 {
  margin: 0;
  font: var(--weight-medium) var(--type-body) / 1.3 var(--font-ui);
}
.nd-fp {
  display: block;
  overflow-wrap: anywhere;
}
.nd-fp-help {
  display: block;
  margin-top: 2px;
  color: var(--text-3);
}
.nd-pending .st-lede {
  margin: 0;
}
</style>
