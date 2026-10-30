<script setup lang="ts">
/**
 * One stack, or a new one (`fresh`): the compose file in a plain textarea,
 * checked by the hub as you type (600 ms after the last key, nothing saved),
 * with its messages, the secrets it references, the plan per service and the
 * line diff against the committed file. Save draft keeps it on the hub
 * (`PUT` with the draft's revision; a 409 says someone else saved first).
 *
 * Deploy plans `stack.deploy` through the safety ladder, so the hold ring
 * shows the plan's steps and blast radius, then this page follows the task on
 * the `tasks` topic: its step, progress and log, and how it ended. History
 * rolls back with `stack.rollback`; a drifted stack accepts the edits made
 * outside Cha Dash with `stack.adopt`.
 *
 * No editor library: a textarea and a line-number gutter, all in the Stacks
 * chunk.
 */
import { computed, nextTick, onBeforeUnmount, onMounted, ref, useId, watch } from 'vue';
import Glyph from '../ui/marks/Glyph.vue';
import { ago, absolute, plural } from '../lib/format';
import { hubWords, isDone } from '../lib/actions';
import { ApiError } from '../lib/api';
import { navigate, stackPath } from '../lib/router';
import type { Task } from '../model/types';
import { useUi } from '../stores/ui';
import {
  KIND_WORDS,
  PLAN_WORDS,
  PROTECTED_WORDS,
  SEVERITY_GLYPH,
  STACK_NAME_RE,
  STARTER,
  STATUS_GLYPH,
  conflictRevision,
  discardDraft,
  getStack,
  outcomeHeadline,
  saveDraft,
  serviceWords,
  statusWords,
  targetOf,
  validateStack,
  type HistoryEntry,
  type StackDetail,
  type StackNode,
  type Validation,
} from './stacks';
import UiButton from '../ui/Button.vue';
import UiField from '../ui/Field.vue';

const props = withDefaults(defineProps<{ node?: string; name?: string; fresh?: boolean; nodes?: StackNode[]; debounceMs?: number }>(), {
  node: '',
  name: '',
  fresh: false,
  nodes: () => [],
  debounceMs: 600,
});

const ui = useUi();
const uid = useId();
const now = ref(Date.now());

// ── loading ──

const detail = ref<StackDetail>();
const status = ref<'loading' | 'ready' | 'error'>(props.fresh ? 'ready' : 'loading');
const loadError = ref('');
const loadCode = ref('');
const content = ref(props.fresh ? STARTER : '');

/** What's saved on the hub: the draft, else the committed file. */
const base = computed(() => detail.value?.draft?.content ?? detail.value?.content ?? '');
const dirty = computed(() => (props.fresh ? false : content.value !== base.value));

async function load(): Promise<void> {
  if (props.fresh) return;
  try {
    const keep = detail.value && dirty.value;
    detail.value = await getStack(props.node, props.name);
    if (!keep) content.value = base.value;
    status.value = 'ready';
    now.value = Date.now();
  } catch (err) {
    status.value = 'error';
    loadCode.value = err instanceof ApiError ? err.code : '';
    loadError.value =
      err instanceof ApiError && err.status === 404 && err.code === 'not_found'
        ? `Cha Dash doesn’t manage a stack called ${props.name} on ${props.node}.`
        : hubWords(err, 'This stack couldn’t be loaded.');
  }
}

const isProtected = computed(() => !!detail.value?.protected);
const protectedWords = computed(() => detail.value?.protected_reason || PROTECTED_WORDS);

// ── a new stack: where and what it's called ──

const onlineNodes = computed(() => props.nodes.filter((n) => n.online && !n.error));
const newNode = ref('');
const newName = ref('');
watch(
  onlineNodes,
  (ns) => {
    if (!ns.some((n) => n.node === newNode.value)) newNode.value = ns[0]?.node ?? '';
  },
  { immediate: true },
);
const nameProblem = computed(() => {
  if (!props.fresh) return '';
  const n = newName.value.trim();
  if (!n) return 'Give it a name.';
  if (!STACK_NAME_RE.test(n)) return 'Lowercase letters, digits, _ and -, starting with a letter or digit.';
  const node = props.nodes.find((x) => x.node === newNode.value);
  if (node?.stacks.some((s) => s.name === n)) return `${n} already exists on ${node.name || node.node}.`;
  if (node?.detected.some((d) => d.name === n)) return `A compose project called ${n} already runs on ${node.name || node.node}: adopt it instead.`;
  return '';
});
const nameTouched = ref(false);

/** The node and name the hub is asked about: the route's, or the new form's once it's valid. */
const where = computed(() => (props.fresh ? (newNode.value && !nameProblem.value ? { node: newNode.value, name: newName.value.trim() } : undefined) : { node: props.node, name: props.name }));
const target = computed(() => (where.value ? targetOf(where.value.node, where.value.name) : ''));

// ── live validation ──

const validation = ref<Validation>();
const validating = ref(false);
const validateError = ref('');
/** The content the shown validation is for. */
const validatedFor = ref<string | undefined>();
let timer: ReturnType<typeof setTimeout> | undefined;
let inflight: AbortController | undefined;
let seq = 0;
/** What the last check asked about, so an unchanged file isn't sent twice. */
let asked = '';
const key = () => (where.value ? `${where.value.node}/${where.value.name}\n${content.value}` : '');

async function validate(): Promise<void> {
  const w = where.value;
  if (!w) {
    validation.value = undefined;
    validatedFor.value = undefined;
    return;
  }
  inflight?.abort();
  const ctl = new AbortController();
  inflight = ctl;
  const mine = ++seq;
  const text = content.value;
  asked = key();
  validating.value = true;
  try {
    const v = await validateStack(w.node, w.name, text, ctl.signal);
    if (mine !== seq) return;
    validation.value = v;
    validatedFor.value = text;
    validateError.value = '';
  } catch (err) {
    if ((err as Error).name === 'AbortError' || mine !== seq) return;
    asked = '';
    validation.value = undefined;
    validatedFor.value = undefined;
    validateError.value = hubWords(err, 'The file couldn’t be checked. Try again.');
  } finally {
    if (mine === seq) validating.value = false;
  }
}

function schedule(): void {
  clearTimeout(timer);
  if (key() === asked) return;
  timer = setTimeout(() => void validate(), props.debounceMs);
}

watch(content, schedule);
watch(where, (w, before) => {
  if (w?.node !== before?.node || w?.name !== before?.name) schedule();
});

const pending = computed(() => validating.value || validatedFor.value !== content.value);
const counts = computed(() => {
  const m = validation.value?.messages ?? [];
  return { error: m.filter((x) => x.severity === 'error').length, warning: m.filter((x) => x.severity === 'warning').length };
});
const summary = computed(() => {
  if (!where.value) return '';
  if (validateError.value) return validateError.value;
  if (!validation.value) return 'Checking the file…';
  const { error, warning } = counts.value;
  const parts = [error ? plural(error, 'error') : '', warning ? plural(warning, 'warning') : ''].filter(Boolean);
  if (parts.length) return parts.join(', ');
  return validation.value.ok ? 'The file is valid.' : 'The file isn’t valid yet.';
});
const secrets = computed(() => validation.value?.secrets ?? detail.value?.secrets ?? []);
const unset = computed(() => secrets.value.filter((s) => !s.set).map((s) => s.name));
const changes = computed(() => (validation.value?.diff ?? []).some((d) => d.op === '+' || d.op === '-'));
const downtime = computed(() => {
  const s = validation.value?.plan?.downtime_s ?? 0;
  return s > 0 ? `About ${Math.round(s)} s of downtime while services are recreated.` : 'No downtime expected.';
});

// ── the editor ──

const area = ref<HTMLTextAreaElement | null>(null);
const gutter = ref<HTMLElement | null>(null);
const lineCount = computed(() => content.value.split('\n').length);
const lineMarks = computed(() => {
  const marks: Record<number, 'e' | 'w'> = {};
  for (const m of validation.value?.messages ?? []) {
    if (!m.line) continue;
    if (m.severity === 'error') marks[m.line] = 'e';
    else if (m.severity === 'warning' && !marks[m.line]) marks[m.line] = 'w';
  }
  return marks;
});
const escArmed = ref(false);

function syncScroll(): void {
  if (gutter.value && area.value) gutter.value.scrollTop = area.value.scrollTop;
}

/** Put the caret on a line the hub named, and scroll it into view. */
function gotoLine(line: number): void {
  const el = area.value;
  if (!el) return;
  const lines = content.value.split('\n');
  const n = Math.min(Math.max(1, line), lines.length);
  let start = 0;
  for (let i = 0; i < n - 1; i++) start += (lines[i]?.length ?? 0) + 1;
  el.focus();
  el.setSelectionRange(start, start + (lines[n - 1]?.length ?? 0));
  const lh = parseFloat(getComputedStyle(el).lineHeight) || 20;
  el.scrollTop = Math.max(0, (n - 3) * lh);
  syncScroll();
}

/** Esc in the editor with unsaved edits asks once before Settings closes. */
function onEsc(e: KeyboardEvent): void {
  if (!dirty.value || escArmed.value) return;
  e.preventDefault();
  escArmed.value = true;
}
watch(content, () => (escArmed.value = false));

// ── drafts ──

const saving = ref(false);
const saveError = ref('');
const conflict = ref<number>();
const confirmDiscard = ref(false);

async function save(revision?: number): Promise<void> {
  if (props.fresh) return saveNew();
  saving.value = true;
  saveError.value = '';
  try {
    const d = await saveDraft(props.node, props.name, content.value, revision ?? detail.value?.draft?.revision ?? 0);
    if (detail.value) detail.value = { ...detail.value, draft: d };
    conflict.value = undefined;
    ui.toast('Draft saved. Deploy it when you’re ready.');
  } catch (err) {
    const rev = conflictRevision(err);
    if (rev !== undefined) conflict.value = rev;
    else saveError.value = hubWords(err, 'The draft wasn’t saved.');
  } finally {
    saving.value = false;
  }
}

async function loadTheirs(): Promise<void> {
  conflict.value = undefined;
  detail.value = undefined;
  await load();
}

async function saveNew(): Promise<void> {
  nameTouched.value = true;
  const w = where.value;
  if (!w) return;
  saving.value = true;
  saveError.value = '';
  try {
    await saveDraft(w.node, w.name, content.value, 0);
    ui.toast(`Draft saved for ${w.name}. Deploy it when you’re ready.`);
    navigate(stackPath(w.node, w.name), { replace: true });
  } catch (err) {
    saveError.value = hubWords(err, 'The draft wasn’t saved.');
  } finally {
    saving.value = false;
  }
}

async function discard(): Promise<void> {
  saving.value = true;
  saveError.value = '';
  try {
    await discardDraft(props.node, props.name);
    confirmDiscard.value = false;
    if (detail.value) detail.value = { ...detail.value, draft: null };
    content.value = detail.value?.content ?? '';
    ui.toast('Draft discarded.');
    if (!detail.value?.content && !detail.value?.history.length) navigate(stackPath(), { replace: true });
  } catch (err) {
    saveError.value = hubWords(err, 'The draft wasn’t discarded.');
  } finally {
    saving.value = false;
  }
}

// ── tasks on this stack ──

/** Tasks on this stack seen running while the page is open; only those show how they ended. */
const seen = ref(new Set<string>());
const dismissed = ref(new Set<string>());
const onTarget = computed(() => Object.values(ui.tasks.value).filter((t) => !!target.value && t.target === target.value && t.action.startsWith('stack.')));
watch(
  onTarget,
  (ts) => {
    const add = ts.filter((t) => !isDone(t) && !seen.value.has(t.id));
    if (add.length) seen.value = new Set([...seen.value, ...add.map((t) => t.id)]);
  },
  { immediate: true },
);
const task = computed<Task | undefined>(() =>
  onTarget.value
    .filter((t) => (!isDone(t) || seen.value.has(t.id)) && !dismissed.value.has(t.id))
    .sort((a, b) => (b.started_at ?? '').localeCompare(a.started_at ?? ''))[0],
);
const running = computed(() => !!task.value && !isDone(task.value));
const pct = computed(() => (typeof task.value?.progress === 'number' ? Math.round(Math.min(1, Math.max(0, task.value.progress)) * 100) : undefined));
const taskStep = computed(() => {
  const t = task.value;
  if (!t) return '';
  return t.state === 'queued' ? 'Waiting its turn' : t.state === 'verifying' ? 'Checking it worked' : t.step || 'Working';
});
const outcome = computed(() => {
  const t = task.value;
  if (!t || !isDone(t)) return undefined;
  if (t.state === 'succeeded') {
    const words =
      t.action === 'stack.deploy'
        ? 'Deployed. The new version passed its health check.'
        : t.action === 'stack.rollback'
          ? 'Rolled back. That version is running again.'
          : t.action === 'stack.adopt'
            ? 'Done. Cha Dash keeps the file’s history from here.'
            : `Done: ${t.label}.`;
    return { tone: 'ok' as const, headline: words, message: '' };
  }
  if (t.state === 'cancelled') return { tone: 'attn' as const, headline: `Cancelled: ${t.label}.`, message: '' };
  const code = t.error?.code;
  return { tone: code === 'rolled_back' || code === 'blocked' ? ('attn' as const) : ('bad' as const), headline: outcomeHeadline(code), message: t.error?.message ?? '' };
});

watch(
  () => (task.value && isDone(task.value) ? task.value.id : ''),
  (id) => {
    if (id) void load();
  },
);

function dismiss(): void {
  if (task.value) dismissed.value = new Set([...dismissed.value, task.value.id]);
}

// ── actions ──

const canHold = computed(() => ui.can.value.act('T2'));
const canUndoable = computed(() => ui.can.value.act('T1'));

const deployBlock = computed(() => {
  const d = detail.value;
  if (!d) return 'Loading…';
  if (isProtected.value) return `${protectedWords.value}.`;
  if (running.value) return 'A task is working on this stack.';
  if (d.drift) return 'The file on disk was edited outside Cha Dash. Accept those edits first, or deploying would overwrite them.';
  if (dirty.value) return 'Save the draft first: Deploy runs the saved draft.';
  if (!d.draft) return 'Nothing to deploy: edit the file and save a draft.';
  if (pending.value) return 'Checking the file…';
  if (validateError.value) return validateError.value;
  if (!validation.value?.ok) return 'Fix the errors in the file first.';
  if (unset.value.length) return `Set ${unset.value.join(', ')} in Stack secrets first.`;
  if (!validation.value.plan) return 'The hub has no plan for this file yet.';
  if (!changes.value && validation.value.plan.services.every((s) => s.action === 'unchanged')) return 'Nothing changed against what’s deployed.';
  return '';
});

function deploy(): void {
  if (deployBlock.value) return;
  void ui.runAction({ action: 'stack.deploy', target: target.value, label: `Deploy ${props.name}`, tier: 'T2' });
}

function rollback(h: HistoryEntry): void {
  void ui.runAction({ action: 'stack.rollback', target: target.value, params: { commit: h.commit }, label: `Roll back ${props.name} to ${h.short}`, tier: 'T2' });
}

function acceptDrift(): void {
  void ui.runAction({ action: 'stack.adopt', target: target.value, label: `Accept the edits made outside Cha Dash to ${props.name}`, tier: 'T1' });
}

const metaLine = computed(() =>
  Object.entries(detail.value?.meta ?? {})
    .filter(([, v]) => ['string', 'number', 'boolean'].includes(typeof v))
    .map(([k, v]) => `${k} ${String(v)}`)
    .join(' · '),
);

onMounted(async () => {
  await load();
  if (status.value === 'ready') {
    clearTimeout(timer);
    void validate();
  }
  if (props.fresh) {
    await nextTick();
    document.getElementById(`${uid}-name`)?.focus();
  }
});
onBeforeUnmount(() => {
  clearTimeout(timer);
  inflight?.abort();
});
</script>

<template>
  <section class="st-section sk-editor" aria-labelledby="ske-h">
    <a class="st-crumb" :href="stackPath()" @click.prevent="navigate(stackPath(), { replace: true })">‹ Stacks</a>

    <!-- the header -->
    <header v-if="fresh"><h3 id="ske-h">New stack</h3></header>
    <header v-else>
      <h3 id="ske-h">{{ name }} <span class="sk-on">on {{ node }}</span></h3>
      <span v-if="detail" class="st-meta st-status">
        <Glyph :kind="STATUS_GLYPH[detail.status] ?? 'repair'" />{{ statusWords({ status: detail.status, services: detail.services.length, running: detail.services.filter((s) => s.state === 'running').length }) }}
      </span>
    </header>

    <div v-if="status === 'loading'" class="st-skel" aria-busy="true"><span class="skel"></span><span class="skel"></span><span class="skel"></span></div>
    <div v-else-if="status === 'error'" :class="['st-note', loadCode === 'node_unavailable' ? 'hatched' : 'bad']" role="alert">
      <Glyph :kind="loadCode === 'node_unavailable' ? 'off' : 'down'" /><span>{{ loadError }}</span>
      <UiButton v-if="loadCode !== 'not_found'" size="sm" @click="load">Try again</UiButton>
    </div>

    <template v-else>
      <!-- what this stack is -->
      <p v-if="detail" class="st-lede sk-path">
        <span class="st-mono">{{ detail.path }}/{{ detail.file }}</span>
        <template v-if="detail.location === 'external'"> · adopted in place</template>
        <template v-if="detail.env.present"> · .env with {{ detail.env.keys.length ? detail.env.keys.join(', ') : 'no keys' }}</template>
        <template v-if="metaLine"> · x-chadash: {{ metaLine }}</template>
      </p>
      <div v-if="isProtected" class="st-note sk-prot" role="note"><Glyph kind="lock" /><span>{{ protectedWords }}. You can read it and see what would change; deploying it from here comes with self-update.</span></div>
      <div v-if="detail?.drift && !isProtected" class="st-note attn" role="note">
        <Glyph kind="attn" />
        <span>The file on disk was edited outside Cha Dash since its last commit. Accept those edits to make them the stack’s history, or deploying will refuse.</span>
        <UiButton v-if="canUndoable" size="sm" :disabled="running" @click="acceptDrift">Accept edits made outside Cha Dash<span class="tier">T1</span></UiButton>
      </div>

      <!-- a new stack's node and name -->
      <div v-if="fresh" class="sk-new">
        <div v-if="!onlineNodes.length" class="st-note hatched" role="status"><Glyph kind="off" /><span>No node is online, so there’s nowhere to put a new stack right now.</span></div>
        <template v-else>
          <UiField label="Node" v-slot="{ control }">
            <select v-bind="control" v-model="newNode">
              <option v-for="n in onlineNodes" :key="n.node" :value="n.node">{{ n.name || n.node }} · {{ n.root }}</option>
            </select>
          </UiField>
          <UiField
            :id="`${uid}-name`"
            label="Name"
            help="The compose project name. Deploying creates its folder and compose.yaml in the node’s stacks root."
            :error="nameTouched && nameProblem"
            v-slot="{ control }"
          >
            <input
              v-bind="control"
              v-model="newName"
              type="text"
              autocomplete="off"
              autocapitalize="off"
              spellcheck="false"
              maxlength="64"
              placeholder="media"
              @blur="nameTouched = true"
            />
          </UiField>
        </template>
      </div>

      <!-- the editor -->
      <div class="sk-ed-head">
        <label :for="`${uid}-yaml`" class="sk-label">{{ fresh ? 'compose.yaml' : detail?.file || 'compose.yaml' }}</label>
        <span v-if="detail?.draft && !fresh" class="st-meta">
          draft · revision {{ detail.draft.revision }} · saved by {{ detail.draft.updated_by }} <span :title="absolute(detail.draft.updated_at)">{{ ago(detail.draft.updated_at, now) }}</span>
        </span>
        <span v-else-if="!fresh" class="st-meta">no draft · showing the committed file</span>
        <span v-if="dirty" class="chip">unsaved edits</span>
      </div>
      <div class="sk-ed">
        <div ref="gutter" class="sk-gutter" aria-hidden="true">
          <span v-for="i in lineCount" :key="i" :class="lineMarks[i]">{{ i }}</span>
        </div>
        <textarea
          :id="`${uid}-yaml`"
          ref="area"
          v-model="content"
          class="sk-area"
          spellcheck="false"
          autocapitalize="off"
          autocomplete="off"
          wrap="off"
          :readonly="isProtected"
          :aria-invalid="counts.error ? true : undefined"
          :aria-describedby="`${uid}-sum`"
          @scroll="syncScroll"
          @keydown.esc="onEsc"
        ></textarea>
      </div>
      <p v-if="escArmed" class="st-meta" role="status">Unsaved edits. Press Esc again to close Settings and lose them.</p>

      <!-- what the hub says about it -->
      <div class="sk-check">
        <p :id="`${uid}-sum`" :class="['sk-sum', { bad: counts.error || validateError, busy: pending && !validateError }]" role="status" aria-live="polite">
          <Glyph :kind="validateError || counts.error ? 'down' : pending ? 'prog' : counts.warning ? 'attn' : 'check'" />
          <span>{{ where ? summary : fresh ? 'Name the stack and Cha Dash checks the file as you type.' : '' }}</span>
        </p>
        <ul v-if="validation?.messages.length" class="sk-msgs" aria-label="What the hub found in the file">
          <li v-for="(m, i) in validation.messages" :key="i" :class="['sk-msg', m.severity]">
            <Glyph :kind="SEVERITY_GLYPH[m.severity] ?? 'off'" :label="m.severity" />
            <button v-if="m.line" type="button" class="sk-line" :aria-label="`Go to line ${m.line}`" @click="gotoLine(m.line)">line {{ m.line }}</button>
            <span v-if="m.service" class="st-mono">{{ m.service }}</span>
            <span class="sk-msg-t">{{ m.message }}</span>
          </li>
        </ul>
      </div>

      <div v-if="secrets.length" class="sk-secrets">
        <h4 class="sk-sub">Secrets it references</h4>
        <ul class="sk-chips" aria-label="Secrets the file references">
          <li v-for="s in secrets" :key="s.name" :class="['chip', { unset: !s.set }]">
            <Glyph :kind="s.set ? 'check' : 'attn'" /><span class="st-mono">{{ s.name }}</span> · {{ s.set ? 'set' : 'not set' }}
          </li>
        </ul>
        <p v-if="unset.length" class="st-meta">
          Set {{ unset.join(', ') }} in <a :href="stackPath()" @click.prevent="navigate(stackPath(), { replace: true })">Stack secrets</a> before deploying.
        </p>
      </div>

      <div v-if="validation?.plan" class="sk-plan">
        <h4 class="sk-sub">What deploying does</h4>
        <table class="sk-table">
          <caption class="sr">The plan per service</caption>
          <thead>
            <tr><th scope="col">Service</th><th scope="col">Change</th><th scope="col">Image</th><th scope="col">Why</th></tr>
          </thead>
          <tbody>
            <tr v-for="s in validation.plan.services" :key="s.name" :class="s.action">
              <th scope="row" class="st-mono">{{ s.name }}</th>
              <td>{{ PLAN_WORDS[s.action] ?? s.action }}<template v-if="s.pull"> · pull</template></td>
              <td class="st-mono">{{ s.image ?? '' }}</td>
              <td>{{ s.reason ?? '' }}</td>
            </tr>
          </tbody>
        </table>
        <p class="st-meta">{{ downtime }}</p>
      </div>

      <div v-if="changes" class="sk-diffwrap">
        <h4 class="sk-sub">{{ fresh || !detail?.content ? 'The new file' : 'Changes against the committed file' }}</h4>
        <pre class="sk-diff" tabindex="0"><span v-for="(d, i) in validation!.diff" :key="i" :class="['dl', d.op === '+' ? 'add' : d.op === '-' ? 'del' : d.op === '…' ? 'fold' : 'eq']"><span v-if="d.op === '+'" class="sr">added: </span><span v-else-if="d.op === '-'" class="sr">removed: </span><span aria-hidden="true">{{ d.op === '…' ? '⋯' : d.op }} </span>{{ d.text }}</span></pre>
      </div>

      <!-- save, discard, deploy -->
      <div v-if="conflict !== undefined" class="st-note attn" role="alert">
        <Glyph kind="attn" />
        <span>Someone saved this draft since you opened it (now revision {{ conflict }}). Load theirs, or save yours over it.</span>
        <UiButton size="sm" @click="loadTheirs">Load theirs</UiButton>
        <UiButton size="sm" :disabled="saving" @click="save(conflict)">Save mine anyway</UiButton>
      </div>
      <p v-if="saveError" class="err" role="alert">{{ saveError }}</p>
      <div v-if="confirmDiscard" class="st-confirm" role="group" aria-label="Discard the draft?">
        <span>Discard the draft? The saved edits go; the committed file stays as it is.</span>
        <UiButton variant="danger-solid" size="sm" :disabled="saving" @click="discard">Discard draft</UiButton>
        <UiButton size="sm" @click="confirmDiscard = false">Keep it</UiButton>
      </div>

      <div v-if="!isProtected" class="st-acts sk-acts">
        <UiButton :disabled="saving || (fresh ? !where || !onlineNodes.length : !dirty)" :aria-busy="saving || undefined" @click="save()">
          {{ saving ? 'Saving…' : 'Save draft' }}
        </UiButton>
        <UiButton v-if="!fresh && detail?.draft && !confirmDiscard" variant="ghost" :disabled="saving" @click="confirmDiscard = true">Discard draft</UiButton>
        <template v-if="!fresh && canHold">
          <UiButton variant="primary" class="sk-deploy" :aria-disabled="deployBlock ? 'true' : undefined" :aria-describedby="deployBlock ? `${uid}-why` : undefined" @click="deploy">
            Deploy<span class="tier">T2</span>
          </UiButton>
          <span v-if="deployBlock" :id="`${uid}-why`" class="st-meta sk-why">{{ deployBlock }}</span>
        </template>
      </div>
      <p v-else-if="!fresh && canHold" class="st-acts sk-acts">
        <UiButton variant="primary" class="sk-deploy" aria-disabled="true" :aria-describedby="`${uid}-why`">Deploy<span class="tier">T2</span></UiButton>
        <span :id="`${uid}-why`" class="st-meta sk-why">{{ deployBlock }}</span>
      </p>

      <!-- the task working on it, and how it ended (TaskPresence announces it; not a live region here) -->
      <div v-if="task" :class="['sk-task', running ? 'live' : outcome?.tone]">
        <template v-if="running">
          <div class="sk-task-h">
            <Glyph kind="prog" />
            <b>{{ task.label }}</b>
            <span>· {{ taskStep }}<template v-if="pct !== undefined"> · {{ pct }}%</template></span>
          </div>
          <div v-if="pct !== undefined" class="sk-bar" aria-hidden="true"><span :style="{ width: `${pct}%` }"></span></div>
        </template>
        <template v-else-if="outcome">
          <div class="sk-task-h">
            <Glyph :kind="outcome.tone === 'ok' ? 'check' : outcome.tone === 'attn' ? 'attn' : 'down'" />
            <b>{{ outcome.headline }}</b>
          </div>
          <p v-if="outcome.message" class="sk-task-m">{{ outcome.message }}</p>
        </template>
        <pre v-if="task.log?.length" class="sk-log" tabindex="0">{{ task.log.slice(-12).join('\n') }}</pre>
        <UiButton v-if="!running" variant="ghost" size="sm" @click="dismiss">Dismiss</UiButton>
      </div>

      <!-- services -->
      <section v-if="detail && !fresh" class="sk-block" :aria-labelledby="`${uid}-svc`">
        <h4 :id="`${uid}-svc`" class="sk-sub">Services</h4>
        <p v-if="!detail.services.length" class="st-meta">No containers for this stack right now.</p>
        <ul v-else class="st-list">
          <li v-for="s in detail.services" :key="s.name" class="st-row sk-svc">
            <Glyph :kind="serviceWords(s).glyph" />
            <span>
              <span class="t st-mono">{{ s.name }}</span>
              <span class="m st-mono">{{ s.image }}</span>
            </span>
            <span class="end">{{ serviceWords(s).words }}<template v-if="s.restarts"> · restarted {{ s.restarts }}×</template></span>
          </li>
        </ul>
      </section>

      <!-- history -->
      <section v-if="detail && !fresh" class="sk-block" :aria-labelledby="`${uid}-hist`">
        <h4 :id="`${uid}-hist`" class="sk-sub">History</h4>
        <p v-if="!detail.history.length" class="st-meta">Nothing deployed yet. The first deploy starts the history.</p>
        <ol v-else class="st-list sk-hist">
          <li v-for="(h, i) in detail.history" :key="h.commit" :class="['st-row', 'sk-h', h.kind]">
            <span :class="['sk-kind', h.kind]"><Glyph :kind="(KIND_WORDS[h.kind] ?? KIND_WORDS.other).glyph" />{{ (KIND_WORDS[h.kind] ?? KIND_WORDS.other).label }}</span>
            <span>
              <span class="t">{{ h.subject }}</span>
              <span class="m"><span class="st-mono">{{ h.short }}</span> · {{ h.author }} · <span :title="absolute(h.at)">{{ ago(h.at, now) }}</span><template v-if="i === 0"> · current</template></span>
            </span>
            <span class="end">
              <UiButton v-if="i > 0 && canHold && !isProtected" variant="ghost" size="sm" :disabled="running || detail.drift" :aria-label="`Roll back to ${h.short}: ${h.subject}`" @click="rollback(h)">
                Roll back to this<span class="tier">T2</span>
              </UiButton>
            </span>
          </li>
        </ol>
      </section>
    </template>
  </section>
</template>

<style>
.sk-on {
  font-weight: var(--weight-regular);
  color: var(--text-3);
  text-transform: none;
  letter-spacing: 0;
}
.sk-path {
  overflow-wrap: anywhere;
}
.sk-new {
  display: grid;
  grid-template-columns: repeat(auto-fit, minmax(200px, 1fr));
  gap: var(--space-3);
}
.sk-ed-head {
  display: flex;
  flex-wrap: wrap;
  align-items: center;
  gap: var(--space-2);
}
.sk-label {
  font: var(--weight-medium) var(--type-small) / 1.2 var(--font-num);
  color: var(--text-2);
}
.sk-ed {
  display: grid;
  grid-template-columns: auto minmax(0, 1fr);
  height: clamp(16rem, 48vh, 34rem);
  border: var(--hairline) solid var(--line-2);
  border-radius: var(--radius-md);
  background: var(--surface);
  overflow: hidden;
}
.sk-ed:focus-within {
  outline: 2px solid var(--focus);
  outline-offset: 1px;
}
.sk-gutter,
.sk-area {
  font: var(--weight-regular) var(--type-small) / 1.6 var(--font-num);
  padding: 10px 0;
}
.sk-gutter {
  display: flex;
  flex-direction: column;
  min-width: 3.2em;
  overflow: hidden;
  padding-inline: 8px 6px;
  border-right: var(--hairline) solid var(--line);
  text-align: right;
  color: var(--text-3);
  user-select: none;
}
.sk-gutter .e {
  color: var(--down-text);
  font-weight: var(--weight-medium);
}
.sk-gutter .w {
  color: var(--attention-text);
}
.sk-area {
  width: 100%;
  height: 100%;
  padding-inline: 10px;
  border: 0;
  outline: none;
  resize: none;
  background: transparent;
  color: var(--text);
  white-space: pre;
  overflow: auto;
  tab-size: 2;
}
.sk-area[readonly] {
  color: var(--text-2);
}
.sk-check {
  display: flex;
  flex-direction: column;
  gap: var(--space-2);
}
.sk-sum {
  display: flex;
  align-items: center;
  gap: var(--space-2);
  font-size: var(--type-small);
  color: var(--text-2);
}
.sk-sum.bad {
  color: var(--down-text);
}
.sk-msgs {
  list-style: none;
  margin: 0;
  padding: 0;
  display: flex;
  flex-direction: column;
  gap: 4px;
}
.sk-msg {
  display: flex;
  flex-wrap: wrap;
  align-items: baseline;
  gap: 6px;
  font-size: var(--type-small);
  line-height: 1.45;
  color: var(--text);
}
.sk-line {
  padding: 0 4px;
  border: var(--hairline) solid var(--line-2);
  border-radius: var(--radius-xs);
  background: none;
  font: var(--weight-medium) var(--type-caption) / 1.4 var(--font-num);
  color: var(--text-2);
}
.sk-line:hover {
  color: var(--text);
}
.sk-chips {
  list-style: none;
  margin: 0;
  padding: 0;
  display: flex;
  flex-wrap: wrap;
  gap: 6px;
}
.sk-chips .chip.unset {
  border-color: color-mix(in oklch, var(--attention) 55%, var(--line-2));
}
.sk-table {
  width: 100%;
  border-collapse: collapse;
  font-size: var(--type-small);
}
.sk-table th,
.sk-table td {
  padding: 6px 8px;
  border-bottom: var(--hairline) solid var(--line);
  text-align: left;
  vertical-align: top;
  overflow-wrap: anywhere;
}
.sk-table thead th {
  font: var(--weight-medium) var(--type-micro) / 1 var(--font-num);
  letter-spacing: var(--tracking-caps);
  text-transform: uppercase;
  color: var(--text-3);
}
.sk-table tr.unchanged td,
.sk-table tr.unchanged th {
  color: var(--text-3);
}
.sk-table tr.remove td:nth-child(2) {
  color: var(--down-text);
}
.sk-diff,
.sk-log {
  margin: 0;
  max-height: 22rem;
  overflow: auto;
  padding: 8px 0;
  border: var(--hairline) solid var(--line);
  border-radius: var(--radius-md);
  font: var(--weight-regular) var(--type-caption) / 1.55 var(--font-num);
  color: var(--text-2);
}
.sk-diff .dl {
  display: block;
  padding-inline: 10px;
  white-space: pre;
}
.sk-diff .add {
  background: color-mix(in oklch, var(--progress) 14%, transparent);
  color: var(--text);
}
.sk-diff .del {
  background: var(--down-bg);
  color: var(--text);
}
.sk-diff .fold {
  color: var(--text-3);
  font-style: italic;
}
.sk-log {
  padding-inline: 10px;
  white-space: pre-wrap;
  max-height: 12rem;
}
.sk-acts {
  margin-top: var(--space-2);
}
.sk-why {
  flex: 1 1 16rem;
}
.sk-task {
  display: flex;
  flex-direction: column;
  align-items: flex-start;
  gap: var(--space-2);
  padding: 12px 14px;
  border: var(--hairline) solid var(--line-2);
  border-radius: var(--radius-md);
  font-size: var(--type-small);
  color: var(--text);
}
.sk-task > pre {
  align-self: stretch;
}
.sk-task.attn {
  border-color: color-mix(in oklch, var(--attention) 55%, var(--line));
  background: var(--attention-bg);
}
.sk-task.bad {
  border-color: color-mix(in oklch, var(--down) 55%, var(--line));
  background: var(--down-bg);
}
.sk-task-h {
  display: flex;
  flex-wrap: wrap;
  align-items: center;
  gap: 6px;
}
.sk-task-m {
  color: var(--text-2);
  line-height: 1.45;
}
.sk-bar {
  align-self: stretch;
  height: 3px;
  border-radius: var(--radius-full);
  background: color-mix(in oklch, var(--text) 9%, transparent);
  overflow: hidden;
}
.sk-bar > span {
  display: block;
  height: 100%;
  background: var(--progress);
  transition: width var(--motion-fade-duration) var(--motion-fade-easing);
}
.sk-block {
  display: flex;
  flex-direction: column;
  gap: var(--space-2);
  margin-top: var(--space-4);
}
.sk-svc,
.sk-h {
  grid-template-columns: auto minmax(0, 1fr) auto;
}
.sk-kind {
  display: inline-flex;
  align-items: center;
  gap: 5px;
  min-width: 7.5em;
  font: var(--weight-medium) var(--type-caption) / 1.2 var(--font-num);
  color: var(--text-2);
}
.sk-kind.rollback {
  color: var(--attention-text);
}
.sk-hist {
  padding: 0;
}
@media (max-width: 719px) {
  .sk-h {
    grid-template-columns: minmax(0, 1fr);
  }
  .sk-h .end {
    justify-content: flex-start;
  }
}
</style>
