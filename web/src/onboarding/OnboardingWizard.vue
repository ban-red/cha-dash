<script setup lang="ts">
/**
 * "Connect your home": the first-run wizard, after setup and the passkey
 * offer. A layer over the front page, so the page fills in underneath as
 * things are connected, and it ends on that page. Three ways in:
 *
 * - Import a config: the same Import flow as Settings → Bring in, inside the
 *   wizard's layer; applying it lands on the board and finishes here.
 * - Discover: the compose sidecar's socket proxy, then what the owner names.
 * - One by one: the catalog, each add form with its recipe beside it.
 *
 * Skippable (Skip setup), resumable (closing keeps the step, on the hub), and
 * shown once per hub. Settings opens above it; while it's open the wizard is
 * inert. Its own chunk, loaded only when it shows.
 */
import { computed, defineAsyncComponent, nextTick, onBeforeUnmount, onMounted, ref, watch } from 'vue';
import AddStep from './AddStep.vue';
import ConnectedList from './ConnectedList.vue';
import DiscoverStep from './DiscoverStep.vue';

const ImportFlow = defineAsyncComponent(() => import('../import/ImportFlow.vue'));
import { navigate, view } from '../lib/router';
import { defaultBoard } from '../model/defaultLayout';
import { resources } from '../model/graph';
import { plural } from '../lib/format';
import { integrations, loadCatalog, loadInstances } from '../stores/integrations';
import { useBoard } from '../stores/board';
import { useLive } from '../stores/live';
import { closeWizard, goTo, onboarding, type WizardStep } from './state';
import UiButton from '../ui/Button.vue';
import UiCloseButton from '../ui/CloseButton.vue';

const live = useLive();
const board = useBoard();
const heading = ref<HTMLElement | null>(null);
const finishing = ref(false);

const settingsOpen = computed(() => view.value.name === 'settings');
const step = computed(() => onboarding.step);
const instances = computed(() => integrations.instances);
/** What the page can show so far: everything in the graph but the hub itself. */
const seen = computed(() => resources(live.graph.value).filter((r) => r.id !== 'chadash:hub' && r.kind !== 'integration').length);

const TITLES: Record<WizardStep, string> = {
  start: 'Connect your home.',
  import: 'Import a config',
  discover: 'Discover what’s here',
  add: 'Add what you run',
  done: 'Your page is filling in.',
};

async function focusHeading(): Promise<void> {
  await nextTick();
  heading.value?.focus({ preventScroll: true });
}

function to(s: WizardStep): void {
  goTo(s);
  void focusHeading();
}

function startImport(): void {
  to('import');
}

/** An applied import lands on its board (ImportFlow navigates); the path is done. */
function imported(boardId: string): void {
  if (!boardId) navigate('/');
  to('done');
}

/**
 * The board was laid out when the hub was empty. If nobody has changed it
 * since (everything on it is also in the default for what's connected now),
 * lay it out again from what the graph holds, so the page ends populated.
 * A board someone arranged, or an imported one, is left alone.
 */
async function relayout(): Promise<void> {
  const g = live.graph.value;
  const b = board.board.value;
  if (!g || !b || board.state.status !== 'ready' || board.state.lastKnown || !board.state.boardId) return;
  const next = defaultBoard(g, live.now.value);
  const ids = (x: typeof b) => new Set(x.sections.flatMap((s) => s.items.map((i) => i.id)));
  const have = ids(b);
  const want = ids(next);
  if (want.size <= have.size || ![...have].every((id) => want.has(id))) return;
  try {
    await board.save(next);
  } catch {
    /* the old layout stays; edit mode can add the rest */
  }
}

async function finish(): Promise<void> {
  if (finishing.value) return;
  finishing.value = true;
  try {
    await relayout();
  } finally {
    finishing.value = false;
    await closeWizard('done');
    if (view.value.name !== 'front') navigate('/');
  }
}

function later(): void {
  void closeWizard('later');
}

function skip(): void {
  void closeWizard('skipped');
}

// Back from Settings: refresh what's connected; an applied import lands on
// its board, which finishes the Import path.
watch(settingsOpen, (open, was) => {
  if (!open && was) {
    void loadInstances(true);
    if (onboarding.step === 'import' && view.value.name === 'front' && view.value.board) to('done');
    else void focusHeading();
  }
});

function onKey(e: KeyboardEvent): void {
  if (e.key !== 'Escape' || e.defaultPrevented || settingsOpen.value) return;
  if (document.querySelector('.layer-palette, .layer-top:not(.inert), .layer:not(.inert) .inbox')) return;
  e.preventDefault();
  later();
}

let poll: ReturnType<typeof setInterval> | undefined;
let lastFocus: Element | null = null;

onMounted(() => {
  lastFocus = document.activeElement;
  void loadCatalog();
  void loadInstances(true);
  // Statuses turn "Reading" after an instance's first sync.
  poll = setInterval(() => {
    if (!settingsOpen.value && document.visibilityState !== 'hidden') void loadInstances(true);
  }, 5000);
  document.addEventListener('keydown', onKey);
  void focusHeading();
});
onBeforeUnmount(() => {
  clearInterval(poll);
  document.removeEventListener('keydown', onKey);
  if (lastFocus instanceof HTMLElement && lastFocus.isConnected) lastFocus.focus({ preventScroll: true });
});
</script>

<template>
  <Teleport to="body">
    <div :class="['layer', 'layer-onboarding', { inert: settingsOpen }]" :inert="settingsOpen || undefined">
      <div class="scrim ob-scrim"></div>
      <section class="onboarding" role="dialog" aria-modal="true" aria-labelledby="ob-h">
        <header class="ob-head">
          <span class="caps">First run · Connect your home</span>
          <UiCloseButton label="Close, and finish later" title="Finish later" @click="later" />
        </header>

        <div class="ob-body">
          <h2 id="ob-h" ref="heading" tabindex="-1">{{ TITLES[step] }}</h2>

          <!-- Start: three ways in -->
          <template v-if="step === 'start'">
            <p class="ob-lede">Cha Dash reads what you already run. Pick a way in; you can mix them, come back later, and none of it needs YAML.</p>
            <div class="ob-paths">
              <button type="button" class="ob-path" @click="startImport">
                <span class="ob-path-t">Import a config</span>
                <span class="ob-path-m">Drop your Homepage files, a Homarr backup or a board in YAML. You see the board before anything changes; secrets go into the encrypted vault.</span>
              </button>
              <button type="button" class="ob-path" @click="to('discover')">
                <span class="ob-path-t">Discover</span>
                <span class="ob-path-m">Find Docker on this host through the socket proxy Cha Dash ships with, then check machines you name.</span>
              </button>
              <button type="button" class="ob-path" @click="to('add')">
                <span class="ob-path-t">Add one by one</span>
                <span class="ob-path-m">Pick what you run. Each comes with a recipe for a credential that can only read, wherever the service has one.</span>
              </button>
            </div>
            <ConnectedList />
          </template>

          <!-- Import: the same flow as Settings → Bring in -->
          <template v-else-if="step === 'import'">
            <ImportFlow @applied="imported" />
            <ConnectedList />
          </template>

          <template v-else-if="step === 'discover'">
            <DiscoverStep />
            <ConnectedList />
          </template>

          <template v-else-if="step === 'add'">
            <p class="ob-lede">Each opens its form in Settings, with how to make the credential beside it. Close Settings to come back here.</p>
            <AddStep />
            <ConnectedList />
          </template>

          <!-- Done -->
          <template v-else>
            <p class="ob-lede">
              <template v-if="instances.length">
                {{ plural(instances.length, 'connection') }}, and Cha Dash sees {{ plural(seen, 'thing') }} so far. Tiles show what each one reads, and the Inbox says when
                something needs you.
              </template>
              <template v-else>Nothing is connected yet, and that’s fine: Settings → Integrations has all of this whenever you want it.</template>
            </p>
            <ConnectedList />
            <p class="ob-quiet ob-posture">
              The security level starts at <b>Relaxed</b>, for a home network nobody reaches from outside: security findings wait in the Inbox as hints.
              Change it in Settings → Security.
            </p>
          </template>
        </div>

        <footer class="ob-foot">
          <UiButton v-if="step === 'start'" variant="ghost" @click="skip">Skip setup</UiButton>
          <UiButton v-else variant="ghost" @click="to('start')">‹ Back</UiButton>
          <span class="ob-foot-gap"></span>
          <UiButton v-if="step === 'discover'" @click="to('add')">Add more by hand</UiButton>
          <UiButton v-if="step !== 'done' && (step !== 'start' || instances.length)" variant="primary" @click="to('done')">
            {{ instances.length ? 'Done' : 'Finish' }}
          </UiButton>
          <UiButton v-if="step === 'done'" variant="primary" :disabled="finishing" :aria-busy="finishing" @click="finish">Open the front page</UiButton>
        </footer>
      </section>
    </div>
  </Teleport>
</template>

<style>
/* ───────────── Onboarding: "Connect your home" ───────────── */
.layer-onboarding {
  z-index: 30; /* below Settings (35), which opens over it */
  display: grid;
  place-items: center;
  padding: var(--space-6) 16px;
}
.ob-scrim {
  animation: ob-fade var(--motion-sheet-duration) var(--motion-sheet-easing);
}
.onboarding {
  position: relative;
  display: flex;
  flex-direction: column;
  width: min(760px, 100%);
  max-height: calc(100dvh - 2 * var(--space-6));
  border: var(--hairline) solid var(--line-2);
  border-radius: var(--radius-xl);
  background: var(--bg);
  background-image: var(--backdrop);
  box-shadow: var(--shadow-overlay);
  animation: ob-in var(--motion-sheet-duration) var(--motion-sheet-easing);
}
@keyframes ob-in {
  from {
    transform: translateY(12px);
    opacity: 0;
  }
}
@keyframes ob-fade {
  from {
    opacity: 0;
  }
}
@media (prefers-reduced-motion: reduce) {
  html:not([data-motion='full']) .onboarding {
    animation-name: ob-fade;
  }
}
.ob-head {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: var(--space-3);
  padding: var(--space-4) var(--space-6);
  border-bottom: var(--hairline) solid var(--line);
}
.ob-body {
  flex: 1;
  min-height: 0;
  overflow: auto;
  overscroll-behavior: contain;
  display: flex;
  flex-direction: column;
  gap: var(--space-5);
  padding: var(--space-6);
}
.ob-body h2 {
  font: var(--weight-medium) var(--type-headline) / var(--leading-snug) var(--font-ui);
  letter-spacing: -0.016em;
  outline: none;
}
:root[data-briefing='headline'] .ob-body h2 {
  font: var(--weight-regular) 34px/1.06 var(--font-head);
}
.ob-lede,
.ob-quiet {
  max-width: 62ch;
  font-size: var(--type-callout);
  line-height: var(--leading-relaxed);
  color: var(--text-2);
  text-wrap: pretty;
}
.ob-quiet {
  font-size: var(--type-small);
  color: var(--text-3);
}
.ob-body code {
  font-family: var(--font-num);
  font-size: 0.92em;
  color: var(--text);
}
.ob-paths {
  display: grid;
  grid-template-columns: repeat(auto-fit, minmax(200px, 1fr));
  gap: var(--space-3);
}
.ob-path {
  display: flex;
  flex-direction: column;
  align-items: flex-start;
  gap: var(--space-2);
  padding: var(--space-4);
  border: var(--hairline) solid var(--line-2);
  border-radius: var(--radius-lg);
  background: var(--surface);
  text-align: left;
  color: var(--text);
  transition:
    background-color var(--motion-press-duration) var(--motion-press-easing),
    transform var(--motion-press-duration) var(--motion-press-easing);
}
.ob-path:hover {
  background: var(--surface-hover);
}
.ob-path:active {
  transform: scale(0.985);
}
.ob-path:focus-visible,
.ob-item:focus-visible {
  outline: 2px solid var(--focus);
  outline-offset: 2px;
}
.ob-path-t {
  font: var(--weight-medium) var(--type-callout) / 1.25 var(--font-ui);
}
.ob-path-m {
  font-size: var(--type-small);
  line-height: 1.45;
  color: var(--text-2);
}
.ob-block {
  display: flex;
  flex-direction: column;
  gap: var(--space-3);
}
.ob-acts {
  display: flex;
  flex-wrap: wrap;
  gap: var(--space-2);
}
.ob-cands,
.ob-grid,
.ob-connected ul,
.ob-misses {
  margin: 0;
  padding: 0;
  list-style: none;
}
.ob-cands {
  display: flex;
  flex-direction: column;
  gap: var(--space-2);
}
.ob-cand {
  display: grid;
  grid-template-columns: auto minmax(0, 1fr) auto;
  gap: var(--space-3);
  align-items: center;
  padding: var(--space-3);
  border: var(--hairline) solid var(--line-2);
  border-radius: var(--radius-md);
  background: var(--surface);
}
.ob-cand-main,
.ob-item-main {
  display: flex;
  flex-direction: column;
  gap: 2px;
  min-width: 0;
}
.ob-cand-main b,
.ob-item-main b {
  font: var(--weight-medium) var(--type-body) / 1.3 var(--font-ui);
}
.ob-m {
  font-size: var(--type-small);
  line-height: 1.4;
  color: var(--text-3);
  overflow-wrap: anywhere;
}
.ob-warn {
  display: flex;
  gap: 6px;
  align-items: flex-start;
  font-size: var(--type-small);
  color: var(--text-2);
}
.ob-warn .g {
  margin-top: 3px;
  color: var(--attention);
}
.ob-done {
  display: inline-flex;
  align-items: center;
  gap: 5px;
  font: var(--weight-medium) var(--type-caption) / 1 var(--font-ui);
  color: var(--text-2);
}
.ob-badge {
  display: inline-grid;
  place-items: center;
  width: 30px;
  height: 30px;
  flex: none;
  border: var(--hairline) solid var(--line-2);
  border-radius: var(--radius-sm);
  font: 600 11px/1 var(--font-num);
  letter-spacing: 0.02em;
  color: var(--text-2);
}
.ob-hosts {
  width: 100%;
  min-height: 5.5rem;
  padding: 10px 12px;
  border: var(--hairline) solid var(--line-2);
  border-radius: var(--radius-md);
  background: var(--surface);
  font: var(--weight-regular) var(--type-small) / 1.5 var(--font-num);
  color: var(--text);
  resize: vertical;
}
.ob-hosts:focus {
  outline: 2px solid var(--focus);
  outline-offset: 1px;
}
.ob-wellknown {
  display: flex;
  flex-direction: column;
  gap: var(--space-2);
  margin: 0;
  padding: 0;
  border: 0;
}
.ob-wellknown > legend {
  margin-bottom: var(--space-2);
  font: var(--weight-medium) var(--type-small) / 1.2 var(--font-ui);
  color: var(--text-2);
}
.ob-check {
  display: flex;
  gap: var(--space-2);
  align-items: flex-start;
  font-size: var(--type-small);
  line-height: 1.4;
}
.ob-check input {
  width: 16px;
  height: 16px;
  margin-top: 1px;
  accent-color: var(--text);
}
.ob-misses {
  display: flex;
  flex-direction: column;
  gap: 4px;
  font-size: var(--type-small);
  color: var(--text-3);
}
.ob-grid {
  display: grid;
  grid-template-columns: repeat(auto-fill, minmax(210px, 1fr));
  gap: var(--space-2);
}
.ob-item {
  display: grid;
  grid-template-columns: auto minmax(0, 1fr) auto;
  gap: var(--space-3);
  align-items: center;
  min-height: var(--target-min);
  padding: var(--space-2) var(--space-3);
  border: var(--hairline) solid var(--line-2);
  border-radius: var(--radius-md);
  background: var(--surface);
  color: inherit;
  text-decoration: none;
}
.ob-item:hover {
  background: var(--surface-hover);
}
.ob-chev {
  color: var(--text-3);
}
.ob-skel {
  display: flex;
  flex-direction: column;
  gap: var(--space-3);
}
.ob-connected {
  display: flex;
  flex-direction: column;
  gap: var(--space-2);
  padding-top: var(--space-4);
  border-top: var(--hairline) solid var(--line);
}
.ob-connected li {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: var(--space-3);
  padding: 6px 0;
  border-bottom: var(--hairline) solid var(--line);
}
.ob-c-name {
  display: flex;
  align-items: baseline;
  gap: var(--space-2);
  min-width: 0;
  font-size: var(--type-body);
}
.ob-c-name b {
  font-weight: var(--weight-medium);
}
.ob-c-kind {
  font-size: var(--type-small);
  color: var(--text-3);
}
.ob-c-status {
  display: inline-flex;
  align-items: center;
  gap: 6px;
  font: var(--weight-medium) var(--type-caption) / 1 var(--font-ui);
  color: var(--text-2);
  white-space: nowrap;
}
.ob-foot {
  display: flex;
  flex-wrap: wrap;
  align-items: center;
  gap: var(--space-2);
  padding: var(--space-4) var(--space-6);
  border-top: var(--hairline) solid var(--line);
}
.ob-foot-gap {
  flex: 1;
}
@media (max-width: 719px) {
  .layer-onboarding {
    padding: 0;
  }
  .onboarding {
    width: 100%;
    max-height: none;
    height: 100%;
    border-radius: 0;
    border: 0;
  }
  .ob-head,
  .ob-body,
  .ob-foot {
    padding-left: 16px;
    padding-right: 16px;
  }
  .ob-head {
    padding-top: calc(var(--space-4) + env(safe-area-inset-top));
  }
  .ob-foot {
    padding-bottom: calc(var(--space-4) + env(safe-area-inset-bottom));
  }
}
</style>
