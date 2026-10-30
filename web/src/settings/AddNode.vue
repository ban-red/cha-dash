<script setup lang="ts">
/**
 * Settings → Nodes → Add node: a join token for chadash-node on another
 * Docker host (09 · v0.3 → Remote nodes).
 *
 * The form asks for the host's name (optional), how long the token lives
 * (15 minutes by default, a day at most) and whether approving the node
 * adds its Docker integration (on by default). The hub answers once with
 * the token, its .env block and the compose command; the page shows them
 * with Copy buttons until Done, and never again. The hub address in the
 * block is the hub's suggestion and can be edited before copying.
 *
 * Emits `made` with the new token (for the list) and `close` when the
 * admin cancels or is done.
 */
import { computed, nextTick, onMounted, reactive, ref, useId } from 'vue';
import { api } from '../lib/api';
import { hubWords } from '../lib/actions';
import { absolute } from '../lib/format';
import { useUi } from '../stores/ui';
import UiButton from '../ui/Button.vue';
import UiField from '../ui/Field.vue';
import { copyText } from './recipe';
import { envBlock, TTL_CHOICES, validHubUrl, validNodeName, type CreatedToken, type JoinToken } from './nodes';

const emit = defineEmits<{ made: [token: JoinToken]; close: [] }>();
const ui = useUi();
const uid = useId();

const draft = reactive({ name: '', ttl: 15, docker: true });
const nameError = ref('');
const formError = ref('');
const saving = ref(false);

/** The answer: shown once, until Done. */
const made = ref<CreatedToken | undefined>();
const hubUrl = ref('');
const copied = ref<'' | 'env' | 'command'>('');

onMounted(() => document.getElementById(`${uid}-name`)?.focus());

async function create(): Promise<void> {
  if (saving.value) return;
  const name = draft.name.trim();
  nameError.value = name && !validNodeName(name) ? 'Use 1–63 letters, digits, dots, dashes or underscores, starting with a letter or digit (not “hooks” or “secrets”).' : '';
  formError.value = '';
  if (nameError.value) return;
  saving.value = true;
  try {
    const res = await api<CreatedToken>('/api/nodes/tokens', { method: 'POST', body: { name, ttl_minutes: draft.ttl, add_docker: draft.docker } });
    made.value = res;
    hubUrl.value = res.hub_url;
    copied.value = '';
    emit('made', res.token);
    await nextTick();
    document.getElementById(`${uid}-made-h`)?.focus();
  } catch (err) {
    formError.value = hubWords(err, 'The join token wasn’t made.');
  } finally {
    saving.value = false;
  }
}

const hubOk = computed(() => validHubUrl(hubUrl.value));
const env = computed(() => (made.value ? envBlock(hubUrl.value.trim(), made.value.token.name, made.value.secret) : ''));
const unnamed = computed(() => made.value !== undefined && !made.value.token.name);
const command = computed(() => made.value?.command ?? '');

async function copy(what: 'env' | 'command'): Promise<void> {
  if (!made.value) return;
  const ok = await copyText(what === 'env' ? env.value : command.value);
  copied.value = ok ? what : '';
  if (!ok) ui.toast('Couldn’t copy. Select the text and copy it yourself.', 'error');
}

function done(): void {
  made.value = undefined;
  emit('close');
}
</script>

<template>
  <div v-if="made" class="nd-made st-note attn" role="region" :aria-labelledby="`${uid}-made-h`">
    <div class="nd-made-body">
      <h4 :id="`${uid}-made-h`" tabindex="-1">Set up {{ made.token.name || 'the node' }} now</h4>
      <p>
        This is the only time Cha Dash shows the token. It works once, until {{ absolute(made.token.expires_at) }}. The node it enrolls waits in the Inbox until
        an admin approves it{{ made.token.add_docker ? ', and approving adds its Docker integration' : '' }}.
      </p>

      <UiField
        label="Hub address"
        :error="hubOk ? '' : 'Use ws://host:port/node, or wss://host/node through a reverse proxy.'"
        help="Where the node dials the hub. Change it if the node reaches the hub by another name or through a proxy."
        v-slot="{ control }"
      >
        <input v-bind="control" v-model="hubUrl" class="st-mono" type="text" autocomplete="off" spellcheck="false" />
      </UiField>

      <div class="nd-step">
        <span :id="`${uid}-env-l`" class="nd-step-l">1. Put this in <span class="st-mono">.env</span> next to <span class="st-mono">compose.node.yaml</span> on that host</span>
        <pre class="nd-cmd st-mono" tabindex="0" :aria-labelledby="`${uid}-env-l`">{{ env }}</pre>
        <p v-if="unnamed" class="help">Replace <span class="st-mono">&lt;this host&gt;</span> with the name the hub should call it.</p>
        <UiButton size="sm" :aria-describedby="`${uid}-env-l`" @click="copy('env')">{{ copied === 'env' ? 'Copied' : 'Copy .env' }}</UiButton>
      </div>
      <div class="nd-step">
        <span :id="`${uid}-cmd-l`" class="nd-step-l">2. Start the node</span>
        <pre class="nd-cmd st-mono" tabindex="0" :aria-labelledby="`${uid}-cmd-l`">{{ command }}</pre>
        <UiButton size="sm" :aria-describedby="`${uid}-cmd-l`" @click="copy('command')">{{ copied === 'command' ? 'Copied' : 'Copy command' }}</UiButton>
      </div>
      <p>3. When it asks to join, compare the fingerprint in its log with the one shown here, then approve it.</p>
      <p class="sr" aria-live="polite">{{ copied ? 'Copied to the clipboard.' : '' }}</p>
      <div class="st-acts"><UiButton variant="primary" size="sm" @click="done">Done</UiButton></div>
    </div>
  </div>

  <form v-else class="st-form nd-add" novalidate :aria-labelledby="`${uid}-add-h`" @submit.prevent="create">
    <h4 :id="`${uid}-add-h`">Add a node on another host</h4>
    <UiField
      :id="`${uid}-name`"
      label="Name"
      optional
      help="What the hub calls the host. Leave it empty to set CHADASH_NODE_NAME on the host instead."
      :error="nameError"
      v-slot="{ control }"
    >
      <input v-bind="control" v-model="draft.name" type="text" maxlength="63" autocomplete="off" spellcheck="false" />
    </UiField>
    <UiField label="The token works for" help="Once, within this time. A late or spent token can’t do more than ask to join." v-slot="{ control }">
      <select v-bind="control" v-model.number="draft.ttl">
        <option v-for="c in TTL_CHOICES" :key="c.minutes" :value="c.minutes">{{ c.words }}{{ c.minutes === 15 ? ' (default)' : '' }}</option>
      </select>
    </UiField>
    <UiField check label="Add its Docker integration when I approve it" v-slot="{ control }">
      <input v-bind="control" v-model="draft.docker" type="checkbox" />
    </UiField>
    <p v-if="formError" class="err" role="alert">{{ formError }}</p>
    <div class="st-acts">
      <UiButton variant="primary" type="submit" :aria-disabled="saving || undefined" :aria-busy="saving || undefined">{{ saving ? 'Making…' : 'Make join token' }}</UiButton>
      <UiButton variant="ghost" @click="emit('close')">Cancel</UiButton>
    </div>
  </form>
</template>

<style>
.nd-add {
  padding: var(--space-4);
  border: var(--hairline) solid var(--line);
  border-radius: var(--radius-lg);
}
.nd-add h4,
.nd-made h4 {
  margin: 0;
  font: var(--weight-medium) var(--type-body) / 1.3 var(--font-ui);
}
.nd-made {
  flex-direction: column;
}
.nd-made-body {
  display: flex;
  flex-direction: column;
  gap: var(--space-3);
  width: 100%;
  min-width: 0;
}
.nd-made-body p {
  margin: 0;
}
.nd-step {
  display: flex;
  flex-direction: column;
  align-items: flex-start;
  gap: var(--space-2);
}
.nd-step .nd-cmd {
  align-self: stretch;
}
.nd-step-l {
  font-size: var(--type-small);
}
</style>
