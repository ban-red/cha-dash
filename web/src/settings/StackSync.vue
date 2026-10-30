<script setup lang="ts">
/**
 * Follow git: one stack follows a branch and path on a git remote (ADR-0021).
 * The node fetches; the hub deploys what it finds through the usual pipeline,
 * either straight away (deploy on push) or as a draft it asks about (draft
 * and ask, the default). While a stack follows git, its editor is read-only.
 *
 * The remote's credential is write-only: the hub only says whether one is
 * saved, and an empty box keeps it. An SSH remote's host key is pinned from
 * a Test, so a fetch never talks to a host the admin didn't confirm.
 *
 * Making deploy on push, changing the URL, and taking over someone else's
 * sync need a step-up: `ui.withStepUp` asks and tries once more.
 */
import { computed, onMounted, ref, useId } from 'vue';
import Glyph from '../ui/marks/Glyph.vue';
import { ago, absolute } from '../lib/format';
import { hubWords } from '../lib/actions';
import { focusFirst } from '../lib/focus';
import { StepUpCancelled } from '../stores/actions';
import { useUi } from '../stores/ui';
import {
  DEFAULT_POLL_MINUTES,
  DEPLOY_ON_PUSH_WORDS,
  MODE_WORDS,
  STATE_WORDS,
  checkSync,
  deleteSync,
  getSync,
  hookURL,
  putSync,
  resumeSync,
  senderLabel,
  short,
  syncNow,
  type AuthKind,
  type CheckResult,
  type SyncMode,
  type SyncRequest,
  type SyncView,
} from './sync';
import UiButton from '../ui/Button.vue';
import UiField from '../ui/Field.vue';

const props = withDefaults(defineProps<{ node: string; stack: string; protected?: boolean; protectedReason?: string; secretNames?: string[] }>(), {
  protected: false,
  protectedReason: '',
  secretNames: () => [],
});
const emit = defineEmits<{ changed: [sync: SyncView | null] }>();

const ui = useUi();
const uid = useId();
const now = ref(Date.now());

const sync = ref<SyncView | null>(null);
const status = ref<'loading' | 'ready' | 'error'>('loading');
const loadError = ref('');

async function load(): Promise<void> {
  try {
    sync.value = await getSync(props.node, props.stack);
    status.value = 'ready';
    now.value = Date.now();
  } catch (err) {
    status.value = 'error';
    loadError.value = hubWords(err, 'The git sync couldn’t be loaded.');
  }
}

// ── the form ──

const editing = ref(false);
const url = ref('');
const branch = ref('main');
const path = ref('');
const authKind = ref<AuthKind>('none');
const username = ref('');
const secret = ref('');
const hostKey = ref('');
const mode = ref<SyncMode>('ask');
const pollMinutes = ref(DEFAULT_POLL_MINUTES);
const allow = ref<string[]>([]);
const errors = ref<Record<string, string>>({});
const formError = ref('');
const saving = ref(false);

const defaultPath = computed(() => `${props.stack}/compose.yaml`);
const allowChoices = computed(() => [...new Set([...props.secretNames, ...allow.value])].sort());

function startEdit(): void {
  const s = sync.value;
  url.value = s?.url ?? '';
  branch.value = s?.branch ?? 'main';
  path.value = s?.path ?? defaultPath.value;
  authKind.value = s?.auth.kind ?? 'none';
  username.value = s?.auth.username ?? '';
  secret.value = '';
  hostKey.value = s?.host_key ?? '';
  mode.value = s?.mode ?? 'ask';
  pollMinutes.value = s ? Math.max(1, Math.round(s.poll_seconds / 60)) : DEFAULT_POLL_MINUTES;
  allow.value = [...(s?.secrets_allow ?? [])];
  errors.value = {};
  formError.value = '';
  tested.value = undefined;
  testError.value = '';
  editing.value = true;
  void focusFirst(`${uid}-url`);
}

function cancelEdit(): void {
  editing.value = false;
  secret.value = '';
  void focusFirst(`${uid}-edit`, `${uid}-follow`, `${uid}-h`);
}

/** Whether a secret is already saved for this auth kind (an empty box keeps it). */
const secretSaved = computed(() => !!sync.value?.auth.secret_set && sync.value.auth.kind === authKind.value);

function authBody(): SyncRequest['auth'] {
  const a: SyncRequest['auth'] = { kind: authKind.value };
  if (authKind.value !== 'none') {
    if (username.value.trim()) a.username = username.value.trim();
    if (secret.value) a.secret = secret.value;
  }
  return a;
}

function validate(): boolean {
  const e: Record<string, string> = {};
  const u = url.value.trim();
  if (!u) e.url = 'Give the remote’s address.';
  else if (!/^(https?|ssh):\/\//i.test(u)) e.url = 'Start with https://, http:// or ssh://.';
  else if (/^[a-z]+:\/\/[^/]*@/i.test(u)) e.url = 'Leave the user and password out of the address; put them under Sign-in.';
  if (!branch.value.trim()) e.branch = 'Name the branch to follow.';
  const p = path.value.trim();
  if (!p) e.path = 'Name the compose file in the repository.';
  else if (p.startsWith('/') || p.split('/').includes('..')) e.path = 'A path inside the repository, such as media/compose.yaml.';
  if (authKind.value === 'https' && !username.value.trim()) e.username = 'Give the user the token belongs to.';
  if (authKind.value !== 'none' && !secret.value && !secretSaved.value)
    e.secret = authKind.value === 'ssh' ? 'Paste the private key Cha Dash signs in with.' : 'Paste the token or password.';
  if (authKind.value === 'ssh' && !hostKey.value) e.hostKey = 'Test the remote, then pin the host key it shows.';
  if (!Number.isFinite(pollMinutes.value) || pollMinutes.value < 1 || pollMinutes.value > 1440) e.poll = 'Between 1 and 1440 minutes.';
  errors.value = e;
  return !Object.keys(e).length;
}

async function save(): Promise<void> {
  if (saving.value) return;
  formError.value = '';
  if (!validate()) {
    const first = Object.keys(errors.value)[0];
    void focusFirst(`${uid}-${first}`);
    return;
  }
  const req: SyncRequest = {
    url: url.value.trim(),
    branch: branch.value.trim(),
    path: path.value.trim(),
    auth: authBody(),
    mode: mode.value,
    poll_seconds: Math.round(pollMinutes.value) * 60,
    secrets_allow: [...allow.value],
  };
  if (authKind.value === 'ssh' && hostKey.value) req.host_key = hostKey.value;
  saving.value = true;
  // The typed secret lives only in this request.
  secret.value = '';
  const creating = !sync.value;
  try {
    const s = await ui.withStepUp(creating ? `Make ${props.stack} follow git` : `Change how ${props.stack} follows git`, () => putSync(props.node, props.stack, req));
    sync.value = s;
    editing.value = false;
    emit('changed', s);
    ui.toast(creating ? `${props.stack} follows ${s.branch} now. Its editor is read-only.` : 'Saved.');
    void focusFirst(`${uid}-edit`, `${uid}-h`);
  } catch (err) {
    if (!(err instanceof StepUpCancelled)) formError.value = hubWords(err, 'The sync wasn’t saved.');
  } finally {
    saving.value = false;
  }
}

// ── test ──

const testing = ref(false);
const tested = ref<CheckResult>();
const testError = ref('');

async function test(): Promise<void> {
  if (testing.value) return;
  testError.value = '';
  tested.value = undefined;
  if (!url.value.trim()) {
    errors.value = { ...errors.value, url: 'Give the remote’s address.' };
    void focusFirst(`${uid}-url`);
    return;
  }
  testing.value = true;
  try {
    tested.value = await checkSync(props.node, props.stack, { url: url.value.trim(), branch: branch.value.trim(), auth: authBody() });
  } catch (err) {
    testError.value = hubWords(err, 'The remote couldn’t be reached.');
  } finally {
    testing.value = false;
  }
}

const keyDiffers = computed(() => !!tested.value?.host_key && tested.value.host_key !== hostKey.value);

function pin(): void {
  if (!tested.value?.host_key) return;
  hostKey.value = tested.value.host_key;
  errors.value = { ...errors.value, hostKey: '' };
}

// ── what a following sync can do ──

const busy = ref('');
const actError = ref('');
const confirmUnfollow = ref(false);

async function checkNow(): Promise<void> {
  if (busy.value || !sync.value) return;
  busy.value = 'now';
  actError.value = '';
  try {
    await syncNow(props.node, props.stack);
    ui.toast(`Checking ${sync.value.branch} now.`);
  } catch (err) {
    actError.value = hubWords(err, 'Cha Dash couldn’t check now.');
  } finally {
    busy.value = '';
  }
}

async function resume(): Promise<void> {
  if (busy.value || !sync.value) return;
  busy.value = 'resume';
  actError.value = '';
  try {
    const s = await ui.withStepUp(`Resume the git sync of ${props.stack}`, () => resumeSync(props.node, props.stack));
    sync.value = s;
    emit('changed', s);
    ui.toast(`${props.stack} follows ${s.branch} again.`);
  } catch (err) {
    if (!(err instanceof StepUpCancelled)) actError.value = hubWords(err, 'The sync wasn’t resumed.');
  } finally {
    busy.value = '';
  }
}

function askUnfollow(): void {
  confirmUnfollow.value = true;
  void focusFirst(`${uid}-uc`);
}

function keepFollowing(): void {
  confirmUnfollow.value = false;
  void focusFirst(`${uid}-unfollow`);
}

async function unfollow(): Promise<void> {
  if (busy.value) return;
  busy.value = 'unfollow';
  actError.value = '';
  try {
    await ui.withStepUp(`Stop ${props.stack} following git`, () => deleteSync(props.node, props.stack));
    sync.value = null;
    confirmUnfollow.value = false;
    emit('changed', null);
    ui.toast(`${props.stack} no longer follows git. Its files and history stay.`);
    void focusFirst(`${uid}-follow`, `${uid}-h`);
  } catch (err) {
    if (!(err instanceof StepUpCancelled)) actError.value = hubWords(err, 'The sync wasn’t removed.');
  } finally {
    busy.value = '';
  }
}

const state = computed(() => (sync.value ? STATE_WORDS[sync.value.state] : undefined));
const minutes = (s: number) => {
  const m = Math.max(1, Math.round(s / 60));
  return m === 1 ? 'every minute' : `every ${m} minutes`;
};

onMounted(load);
</script>

<template>
  <section class="sk-block sy" :aria-labelledby="`${uid}-h`">
    <h4 :id="`${uid}-h`" class="sk-sub" tabindex="-1">Follow git</h4>

    <div v-if="status === 'loading'" class="st-skel" aria-busy="true"><span class="skel"></span><span class="skel"></span></div>
    <div v-else-if="status === 'error'" class="st-note bad" role="alert">
      <Glyph kind="down" /><span>{{ loadError }}</span>
      <UiButton size="sm" @click="load">Try again</UiButton>
    </div>

    <div v-else-if="props.protected && !sync" class="st-note sk-prot" role="note">
      <Glyph kind="lock" /><span>{{ protectedReason ? `${protectedReason} ` : '' }}A protected stack can’t follow git.</span>
    </div>

    <template v-else>
      <!-- not following -->
      <div v-if="!sync && !editing" class="st-empty sy-empty">
        <div class="m">Keep this stack’s compose file in your own git. Cha Dash checks the branch every few minutes, or when a webhook says it changed, and deploys through the same health gate.</div>
        <div class="st-acts">
          <UiButton :id="`${uid}-follow`" @click="startEdit">Follow git</UiButton>
        </div>
      </div>

      <!-- following -->
      <div v-if="sync" class="sy-now">
        <p class="sy-state">
          <Glyph :kind="state!.glyph" /><b>{{ state!.label }}</b>
          <span class="st-mono sy-where">{{ sync.path }}</span> on <span class="st-mono">{{ sync.branch }}</span> in <span class="st-mono sy-where">{{ sync.url }}</span>
        </p>
        <p v-if="sync.state !== 'active' && sync.state_reason" class="st-note attn" role="note"><Glyph :kind="state!.glyph" /><span>{{ sync.state_reason }}</span></p>
        <dl class="sy-facts">
          <div><dt>Mode</dt><dd>{{ MODE_WORDS[sync.mode] }}</dd></div>
          <div><dt>Checks</dt><dd>{{ minutes(sync.poll_seconds) }}<template v-if="sync.hook"> and on a {{ senderLabel(sync.hook.sender) }} webhook</template></dd></div>
          <div><dt>Acts as</dt><dd>{{ sync.owner ? sync.owner.username : 'nobody: its owner is gone' }}</dd></div>
          <div v-if="sync.last_seen_sha"><dt>Last seen</dt><dd class="st-mono">{{ short(sync.last_seen_sha) }}</dd></div>
          <div v-if="sync.last_deployed_sha"><dt>Deployed</dt><dd class="st-mono">{{ short(sync.last_deployed_sha) }}</dd></div>
          <div v-if="sync.rejected_sha"><dt>Rejected</dt><dd><span class="st-mono">{{ short(sync.rejected_sha) }}</span> · waits for a newer commit</dd></div>
          <div v-if="sync.hook"><dt>Webhook</dt><dd class="st-mono sy-where">{{ hookURL(sync.hook.path) }}<template v-if="sync.hook.last_used_at"> · used <span :title="absolute(sync.hook.last_used_at)">{{ ago(sync.hook.last_used_at, now) }}</span></template></dd></div>
        </dl>
        <p v-if="sync.last_error" class="st-note bad" role="note"><Glyph kind="down" /><span>{{ sync.last_error }}</span></p>
        <p v-if="sync.running" class="st-meta" role="status"><Glyph kind="prog" /> Checking now…</p>

        <p v-if="actError" class="err" role="alert">{{ actError }}</p>
        <div v-if="confirmUnfollow" class="st-confirm" role="group" :aria-label="`Stop ${stack} following git?`">
          <span>Stop following git? The files and the history stay, and {{ stack }} becomes a normal stack you edit here.</span>
          <UiButton variant="danger-solid" size="sm" :aria-disabled="busy === 'unfollow' ? 'true' : undefined" :aria-busy="busy === 'unfollow' || undefined" @click="unfollow">Unfollow</UiButton>
          <UiButton size="sm" :id="`${uid}-uc`" @click="keepFollowing">Keep following</UiButton>
        </div>
        <div v-if="!editing && !confirmUnfollow" class="st-acts">
          <UiButton v-if="sync.state === 'active'" size="sm" :aria-disabled="busy ? 'true' : undefined" :aria-busy="busy === 'now' || undefined" @click="checkNow">Check now</UiButton>
          <UiButton v-else variant="primary" size="sm" :aria-disabled="busy ? 'true' : undefined" :aria-busy="busy === 'resume' || undefined" @click="resume">Resume</UiButton>
          <UiButton :id="`${uid}-edit`" variant="ghost" size="sm" @click="startEdit">Change</UiButton>
          <UiButton :id="`${uid}-unfollow`" variant="ghost" size="sm" @click="askUnfollow">Unfollow</UiButton>
        </div>
      </div>

      <!-- the form -->
      <form v-if="editing" class="st-form sy-form" novalidate :aria-labelledby="`${uid}-fh`" @submit.prevent="save">
        <h5 :id="`${uid}-fh`" class="sr">{{ sync ? 'Change the git sync' : 'Follow a git remote' }}</h5>
        <UiField :id="`${uid}-url`" label="Remote" help="https://, http:// or ssh://, without a user or password." :error="errors.url" v-slot="{ control }">
          <input v-bind="control" v-model="url" type="url" autocomplete="off" autocapitalize="off" spellcheck="false" placeholder="https://git.example/ops/stacks.git" />
        </UiField>
        <div class="sy-row">
          <UiField :id="`${uid}-branch`" label="Branch" :error="errors.branch" v-slot="{ control }">
            <input v-bind="control" v-model="branch" type="text" autocomplete="off" autocapitalize="off" spellcheck="false" />
          </UiField>
          <UiField :id="`${uid}-path`" label="Path" help="The compose file in the repository." :error="errors.path" v-slot="{ control }">
            <input v-bind="control" v-model="path" type="text" autocomplete="off" autocapitalize="off" spellcheck="false" :placeholder="defaultPath" />
          </UiField>
        </div>

        <fieldset class="st-fieldset">
          <legend>Sign-in</legend>
          <div class="choice-row" role="radiogroup" aria-label="How Cha Dash signs in to the remote">
            <label><input v-model="authKind" type="radio" :name="`${uid}-auth`" value="none" />None (a public remote)</label>
            <label><input v-model="authKind" type="radio" :name="`${uid}-auth`" value="https" />HTTPS user and token</label>
            <label><input v-model="authKind" type="radio" :name="`${uid}-auth`" value="ssh" />SSH key</label>
          </div>
          <div v-if="authKind !== 'none'" class="sy-row">
            <UiField :id="`${uid}-username`" label="User" :optional="authKind === 'ssh'" :help="authKind === 'ssh' ? 'git when left empty.' : undefined" :error="errors.username" v-slot="{ control }">
              <input v-bind="control" v-model="username" type="text" autocomplete="off" autocapitalize="off" spellcheck="false" :placeholder="authKind === 'ssh' ? 'git' : ''" />
            </UiField>
            <UiField
              :id="`${uid}-secret`"
              :label="authKind === 'ssh' ? 'Private key' : 'Token'"
              :help="secretSaved ? 'Saved. Leave it empty to keep it; typing replaces it.' : 'Write-only: Cha Dash keeps it in its vault and never shows it again.'"
              :error="errors.secret"
              v-slot="{ control }"
            >
              <textarea v-if="authKind === 'ssh'" v-bind="control" v-model="secret" class="st-mono sy-key" rows="3" spellcheck="false" autocomplete="off" :placeholder="secretSaved ? 'saved' : '-----BEGIN OPENSSH PRIVATE KEY-----'"></textarea>
              <input v-else v-bind="control" v-model="secret" type="password" autocomplete="new-password" spellcheck="false" :placeholder="secretSaved ? 'saved' : ''" />
            </UiField>
          </div>
        </fieldset>

        <!-- test, and the SSH host key -->
        <div class="sy-test">
          <UiButton size="sm" :aria-disabled="testing ? 'true' : undefined" :aria-busy="testing || undefined" @click="test">{{ testing ? 'Testing…' : 'Test' }}</UiButton>
          <div aria-live="polite" class="sy-test-out">
            <p v-if="tested" class="st-meta"><Glyph kind="check" /> Reached it. {{ branch }} is at <span class="st-mono">{{ short(tested.sha) }}</span>.</p>
          </div>
          <p v-if="testError" class="err" role="alert">{{ testError }}</p>
        </div>
        <div v-if="authKind === 'ssh'" class="sy-hostkey">
          <UiField :id="`${uid}-hostKey`" label="Pinned host key" :error="errors.hostKey" :help="hostKey ? 'A fetch refuses any other key.' : 'Test the remote to see its key, then pin it.'" v-slot="{ control }">
            <input v-bind="control" :value="hostKey || 'none yet'" type="text" class="st-mono" readonly />
          </UiField>
          <div v-if="keyDiffers" class="st-note attn sy-newkey" role="group" :aria-labelledby="`${uid}-nk`">
            <span :id="`${uid}-nk`">The remote presents this key. Compare it with the one your git server shows before you pin it.</span>
            <code class="st-mono sy-where">{{ tested!.host_key }}</code>
            <UiButton size="sm" @click="pin">Pin this host key</UiButton>
          </div>
        </div>

        <fieldset class="st-fieldset">
          <legend>When a new commit arrives</legend>
          <div class="choice-row" role="radiogroup" aria-label="Mode">
            <label><input v-model="mode" type="radio" :name="`${uid}-mode`" value="ask" />Draft and ask</label>
            <label><input v-model="mode" type="radio" :name="`${uid}-mode`" value="deploy" />Deploy on push</label>
          </div>
          <p class="help">{{ mode === 'deploy' ? DEPLOY_ON_PUSH_WORDS + ' Turning it on asks you to confirm it’s you.' : 'The commit becomes the draft, and the Inbox asks an admin to deploy it.' }}</p>
        </fieldset>

        <UiField :id="`${uid}-poll`" label="Check every" help="Minutes. A webhook makes it follow a push at once." :error="errors.poll" v-slot="{ control }">
          <input v-bind="control" v-model.number="pollMinutes" type="number" min="1" max="1440" step="1" inputmode="numeric" class="sy-num" />
        </UiField>

        <fieldset class="st-fieldset">
          <legend>Secrets it may deploy without asking</legend>
          <p v-if="!allowChoices.length" class="help">This stack references no secrets. A commit that adds one always asks first.</p>
          <template v-else>
            <label v-for="n in allowChoices" :key="n" class="check-row">
              <input v-model="allow" type="checkbox" :value="n" />
              <span class="st-mono">{{ n }}</span>
            </label>
            <p class="help">A commit that references any other stack secret always asks first, whatever the mode.</p>
          </template>
        </fieldset>

        <p v-if="formError" class="err" role="alert">{{ formError }}</p>
        <div class="st-acts">
          <UiButton type="submit" variant="primary" :aria-disabled="saving ? 'true' : undefined" :aria-busy="saving || undefined">{{ saving ? 'Saving…' : sync ? 'Save' : 'Follow' }}</UiButton>
          <UiButton variant="ghost" @click="cancelEdit">Cancel</UiButton>
        </div>
      </form>
    </template>
  </section>
</template>

<style>
.sy-state {
  display: flex;
  flex-wrap: wrap;
  align-items: center;
  gap: 6px;
  margin: 0 0 var(--space-2);
}
.sy-where {
  overflow-wrap: anywhere;
}
.sy-facts {
  display: grid;
  grid-template-columns: repeat(auto-fit, minmax(180px, 1fr));
  gap: var(--space-2) var(--space-3);
  margin: 0 0 var(--space-3);
}
.sy-facts dt {
  font-size: var(--type-caption);
  color: var(--text-3);
}
.sy-facts dd {
  margin: 0;
}
.sy-form {
  display: grid;
  gap: var(--space-3);
}
.sy-row {
  display: grid;
  grid-template-columns: repeat(auto-fit, minmax(200px, 1fr));
  gap: var(--space-3);
}
.sy-key {
  min-height: 4.5rem;
}
.sy-num {
  max-width: 8rem;
}
.sy-test {
  display: flex;
  flex-wrap: wrap;
  align-items: center;
  gap: var(--space-2);
}
.sy-newkey {
  display: grid;
  gap: var(--space-2);
  margin-top: var(--space-2);
}
</style>
