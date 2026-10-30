<script setup lang="ts">
/**
 * Move a running project into the node's stacks root: a button, then a review
 * before anything happens. The review asks the hub for a dry run
 * (`POST …/move/preview`, which saves nothing) and shows each relative path
 * the move makes absolute, line by line, and what the first deploy will do to
 * the running containers. Move runs `stack.move` (T1: it runs, with Undo for
 * 10 s, which un-adopts and leaves both copies).
 *
 * Every state has its own words: loading, the review, a refusal (the node's
 * sentence), and a hub that couldn't be asked.
 */
import { computed, onBeforeUnmount, ref } from 'vue';
import Glyph from '../ui/marks/Glyph.vue';
import UiButton from '../ui/Button.vue';
import UiDialog from '../ui/Dialog.vue';
import { hubWords } from '../lib/actions';
import { useUi } from '../stores/ui';
import { PLAN_WORDS, targetOf } from './stacks';
import { previewMove, type MovePreview } from './fleet';

const props = defineProps<{ node: string; name: string; /** The compose file the project runs from, on the node. */ fromPath: string }>();

const ui = useUi();
const open = ref(false);
const state = ref<'loading' | 'ready' | 'error'>('loading');
const preview = ref<MovePreview>();
const error = ref('');
let inflight: AbortController | undefined;

async function review(): Promise<void> {
  open.value = true;
  state.value = 'loading';
  inflight?.abort();
  const ctl = new AbortController();
  inflight = ctl;
  try {
    preview.value = await previewMove(props.node, props.name, props.fromPath, ctl.signal);
    state.value = 'ready';
  } catch (err) {
    if (ctl.signal.aborted) return;
    error.value = hubWords(err, 'The hub couldn’t ask the node what moving would do.');
    state.value = 'error';
  }
}

function close(): void {
  inflight?.abort();
  open.value = false;
}

const recreated = computed(() => (preview.value?.plan?.services ?? []).filter((s) => s.action === 'recreate').map((s) => s.name));
const created = computed(() => (preview.value?.plan?.services ?? []).filter((s) => s.action === 'create').map((s) => s.name));

function move(): void {
  if (!preview.value?.ok) return;
  open.value = false;
  void ui.runAction({ action: 'stack.move', target: targetOf(props.node, props.name), params: { from_path: props.fromPath }, label: `Move ${props.name} into the stacks root`, tier: 'T1' });
}

onBeforeUnmount(() => inflight?.abort());
</script>

<template>
  <UiButton size="sm" :aria-label="`Move ${name} into the stacks root`" @click="review">Move<span class="tier">T1</span></UiButton>

  <UiDialog v-if="open" size="lg" class="skm" :title="`Move ${name} into the stacks root`" close-label="Cancel" @close="close">
    <p class="st-lede">
      The file <span class="st-mono">{{ fromPath }}</span> and the <span class="st-mono">.env</span> beside it are copied into the root and committed. The project keeps its
      name, so its volumes and networks stay. Nothing restarts now.
    </p>

    <div v-if="state === 'loading'" class="st-skel" aria-busy="true" aria-label="Asking the node what moving would do">
      <span class="skel"></span><span class="skel"></span>
    </div>
    <div v-else-if="state === 'error'" class="st-note bad" role="alert">
      <Glyph kind="down" /><span>{{ error }}</span>
      <UiButton size="sm" @click="review">Try again</UiButton>
    </div>
    <div v-else-if="preview && !preview.ok" class="st-note attn skm-refusal" role="alert">
      <Glyph kind="attn" />
      <span><b>It can’t be moved.</b> {{ preview.refusal?.message || 'The node refused it.' }}</span>
    </div>
    <template v-else-if="preview">
      <section class="skm-part" aria-labelledby="skm-rw">
        <h3 id="skm-rw" class="sk-sub">Paths made absolute</h3>
        <p v-if="!preview.rewrites.length" class="st-meta">No relative paths: the file moves as it is.</p>
        <template v-else>
          <p class="st-meta">These named something next to the old file. They’re rewritten so they still name the same data.</p>
          <ol class="skm-rewrites">
            <li v-for="r in preview.rewrites" :key="`${r.line}-${r.from}`">
              <span class="skm-line">Line {{ r.line }}</span>
              <span class="st-mono skm-from"><span class="sr"> was </span>{{ r.from }}</span>
              <span class="skm-arrow" aria-hidden="true">→</span>
              <span class="st-mono skm-to"><span class="sr"> becomes </span>{{ r.to }}</span>
            </li>
          </ol>
        </template>
      </section>

      <section v-if="preview.plan" class="skm-part" aria-labelledby="skm-plan">
        <h3 id="skm-plan" class="sk-sub">What the first deploy will do</h3>
        <p v-if="recreated.length" class="st-note attn" role="note">
          <Glyph kind="attn" />
          <span>The next deploy recreates {{ recreated.join(', ') }}: the file now lives somewhere else, so Compose sees new containers. Moving itself restarts nothing.</span>
        </p>
        <p v-else-if="created.length" class="st-meta">Nothing of it runs now, so the first deploy creates {{ created.join(', ') }}.</p>
        <p v-else class="st-meta">The first deploy leaves the running containers as they are.</p>
        <table class="sk-table">
          <caption class="sr">The first deploy, per service</caption>
          <thead>
            <tr><th scope="col">Service</th><th scope="col">Change</th><th scope="col">Why</th></tr>
          </thead>
          <tbody>
            <tr v-for="s in preview.plan.services" :key="s.name" :class="s.action">
              <th scope="row" class="st-mono">{{ s.name }}</th>
              <td>{{ PLAN_WORDS[s.action] ?? s.action }}</td>
              <td>{{ s.reason ?? '' }}</td>
            </tr>
          </tbody>
        </table>
      </section>

      <p class="st-meta">
        It will live at <span class="st-mono">{{ preview.path }}</span>. A copy stays at <span class="st-mono">{{ fromPath }}</span>; the tool that wrote it may still deploy
        it, so stop that tool from managing it.
      </p>
    </template>

    <template #foot>
      <UiButton variant="ghost" @click="close">Cancel</UiButton>
      <UiButton variant="primary" :disabled="!preview?.ok || state !== 'ready'" @click="move">Move {{ name }}<span class="tier">T1</span></UiButton>
    </template>
  </UiDialog>
</template>

<style>
.skm .skm-part {
  display: grid;
  gap: 8px;
  margin-top: var(--space-2);
}
.skm-rewrites {
  margin: 0;
  padding: 0;
  list-style: none;
  display: grid;
  gap: 6px;
}
.skm-rewrites li {
  display: grid;
  grid-template-columns: auto minmax(0, 1fr) auto minmax(0, 1fr);
  gap: 4px 8px;
  align-items: baseline;
  padding: 6px 8px;
  border: var(--hairline) solid var(--line);
  border-radius: var(--radius-md);
  font-size: var(--type-small);
}
.skm-line {
  color: var(--text-3);
  font: var(--weight-medium) var(--type-caption) / 1.3 var(--font-num);
}
.skm-from,
.skm-to {
  overflow-wrap: anywhere;
}
.skm-from {
  color: var(--text-2);
  text-decoration: line-through;
  text-decoration-color: var(--text-3);
}
.skm-arrow {
  color: var(--text-3);
}
@media (max-width: 560px) {
  .skm-rewrites li {
    grid-template-columns: auto minmax(0, 1fr);
  }
  .skm-arrow {
    display: none;
  }
  .skm-to {
    grid-column: 2;
  }
}
</style>
