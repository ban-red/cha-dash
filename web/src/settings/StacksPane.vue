<script setup lang="ts">
/**
 * Settings → Stacks (owner, admin): the compose stacks Cha Dash owns on each
 * node, the running projects it could adopt, and the secrets stacks reference.
 * The route picks the view:
 *
 *   /settings/stacks                 the list per node, detected projects, stack secrets
 *   /settings/stacks/new             a new stack, saved as a draft
 *   /settings/stacks/{node}/{name}   one stack (StackEditor.vue)
 *
 * Adopting runs `stack.adopt` through the safety ladder (T1: it runs, with
 * Undo for 10 s). The list reloads when a stack task finishes.
 *
 * Its own chunk, with the editor and the secrets, loaded with the pane.
 */
import SettingsHelp from './SettingsHelp.vue';
import { computed, onMounted, ref, watch } from 'vue';
import Glyph from '../ui/marks/Glyph.vue';
import StackEditor from './StackEditor.vue';
import StackSecrets from './StackSecrets.vue';
import StackHooks from './StackHooks.vue';
import StackMove from './StackMove.vue';
import SelfUpdate from './SelfUpdate.vue';
import { ago } from '../lib/format';
import { hubWords, isDone } from '../lib/actions';
import { navigate, stackPath, view } from '../lib/router';
import { useUi } from '../stores/ui';
import { PROTECTED_WORDS, STATUS_GLYPH, listStacks, statusWords, targetOf, type Detected, type StackNode } from './stacks';
import UiButton from '../ui/Button.vue';

const ui = useUi();
const v = computed(() => (view.value.name === 'settings' && view.value.pane === 'stacks' ? view.value : undefined));
const detail = computed(() => {
  const id = v.value?.id;
  if (!id) return undefined;
  const [node, name] = id.split('/');
  return node && name ? { node, name } : undefined;
});
const creating = computed(() => v.value?.add === '');

const nodes = ref<StackNode[]>([]);
const status = ref<'loading' | 'ready' | 'error'>('loading');
const loadError = ref('');
const now = ref(Date.now());

async function load(): Promise<void> {
  try {
    nodes.value = await listStacks();
    status.value = 'ready';
    now.value = Date.now();
  } catch (err) {
    status.value = 'error';
    loadError.value = hubWords(err, 'Stacks couldn’t be loaded.');
  }
}

const anyOnline = computed(() => nodes.value.some((n) => n.online && !n.error));

function adopt(n: StackNode, d: Detected): void {
  void ui.runAction({ action: 'stack.adopt', target: targetOf(n.node, d.name), label: `Adopt ${d.name}`, tier: 'T1' });
}
const canAdopt = computed(() => ui.can.value.act('T1'));

// A stack task finishing (deploy, rollback, adopt, undo) changes the list.
const doneStackTasks = computed(() =>
  Object.values(ui.tasks.value)
    .filter((t) => t.target.startsWith('stack:') && isDone(t))
    .map((t) => t.id)
    .sort()
    .join('|'),
);
watch(doneStackTasks, (next, before) => {
  if (next !== before && !detail.value) void load();
});
// Back from a stack or the new form: read the list again.
watch(
  () => !!detail.value || creating.value,
  (inside, was) => {
    if (!inside && was) void load();
  },
);

onMounted(load);
</script>

<template>
  <StackEditor v-if="detail" :key="`${detail.node}/${detail.name}`" :node="detail.node" :name="detail.name" />
  <StackEditor v-else-if="creating" key="new" :nodes="nodes" fresh />

  <template v-else>
    <section class="st-section" aria-labelledby="sk-h">
      <header>
        <h3 id="sk-h">Stacks</h3>
        <UiButton v-if="anyOnline" size="sm" href="/settings/stacks/new" @click.prevent="navigate('/settings/stacks/new')">New stack</UiButton>
      </header>
      <SettingsHelp summary="Compose stacks Cha Dash owns, kept in git on their node, with rollback on a failed health check.">
        Compose stacks Cha Dash owns. Each lives in git on its node; deploying checks the file, pulls, starts it, holds it to a health check, and rolls back on
        its own if the check fails.
      </SettingsHelp>

      <div v-if="status === 'loading'" class="st-skel" aria-busy="true"><span class="skel"></span><span class="skel"></span><span class="skel"></span></div>
      <div v-else-if="status === 'error'" class="st-note bad" role="alert">
        <Glyph kind="down" /><span>{{ loadError }}</span>
        <UiButton size="sm" @click="load">Try again</UiButton>
      </div>
      <div v-else-if="!nodes.length" class="st-empty">
        <div class="t">No nodes connected</div>
        <div class="m">Stacks run on a node: the hub’s own host, or another machine running the Cha Dash node. Once one connects, its stacks show here.</div>
      </div>
    </section>

    <template v-if="status === 'ready'">
      <section v-for="n in nodes" :key="n.node" class="st-section sk-node" :aria-labelledby="`sk-n-${n.node}`">
        <header>
          <h3 :id="`sk-n-${n.node}`">{{ n.name || n.node }}</h3>
          <span class="st-meta">{{ n.root }}</span>
        </header>

        <div v-if="n.error || !n.online" class="st-note hatched" role="status">
          <Glyph kind="off" />
          <span>{{ n.error?.message || `${n.name || n.node} is offline, so its stacks can’t be read or changed right now.` }}</span>
        </div>

        <div v-else-if="!n.stacks.length && !n.detected.length" class="st-empty">
          <div class="t">No stacks on {{ n.name || n.node }} yet</div>
          <div class="m">Write one here, or start a compose project on this node and adopt it where it is.</div>
          <div class="st-acts">
            <UiButton variant="primary" href="/settings/stacks/new" @click.prevent="navigate('/settings/stacks/new')">New stack</UiButton>
          </div>
        </div>

        <ul v-if="n.stacks.length" class="st-list" :aria-label="`Stacks on ${n.name || n.node}`">
          <li v-for="s in n.stacks" :key="s.name">
            <a class="st-row sk-row" :href="stackPath(n.node, s.name)" @click.prevent="navigate(stackPath(n.node, s.name))">
              <Glyph :kind="STATUS_GLYPH[s.status] ?? 'repair'" />
              <span>
                <span class="t">
                  {{ s.name }}
                  <span v-if="s.protected" class="chip sk-protected"><Glyph kind="lock" />{{ PROTECTED_WORDS }}</span>
                  <span v-if="s.drift" class="chip sk-drift"><Glyph kind="attn" />edited outside Cha Dash</span>
                  <span v-if="s.draft" class="chip">draft</span>
                </span>
                <span class="m">
                  {{ statusWords(s) }}
                  <template v-if="s.location === 'external'"> · adopted in place at <span class="st-mono">{{ s.path }}</span></template>
                  <template v-if="s.head"> · <span class="st-mono">{{ s.head.short }}</span> {{ s.head.subject }} · {{ ago(s.head.at, now) }}</template>
                  <template v-else> · not deployed yet</template>
                </span>
              </span>
              <span class="end" aria-hidden="true">›</span>
            </a>
          </li>
        </ul>

        <template v-if="n.detected.length && n.online && !n.error">
          <h4 class="sk-sub">Running here, not managed yet</h4>
          <ul class="st-list" :aria-label="`Compose projects on ${n.name || n.node} Cha Dash could adopt`">
            <li v-for="d in n.detected" :key="d.name">
              <div class="st-row sk-row">
                <Glyph :kind="STATUS_GLYPH[d.status] ?? 'repair'" />
                <span>
                  <span class="t">{{ d.name }}</span>
                  <span class="m">{{ statusWords(d) }} · <span class="st-mono">{{ d.path }}</span></span>
                  <span v-if="d.protected" class="chip sk-protected"><Glyph kind="lock" />{{ PROTECTED_WORDS }}</span>
                  <span v-else-if="!d.adoptable && d.reason" class="m sk-why">{{ d.reason }}</span>
                </span>
                <span class="end">
                  <UiButton v-if="d.adoptable && canAdopt" size="sm" :aria-label="`Adopt ${d.name} where it is`" @click="adopt(n, d)">
                    Adopt<span class="tier">T1</span>
                  </UiButton>
                  <StackMove v-if="d.moveable && canAdopt" :node="n.node" :name="d.name" :from-path="d.config_files?.[0] ?? d.path" />
                  <span v-else-if="!d.adoptable && !d.protected">can’t adopt</span>
                </span>
              </div>
            </li>
          </ul>
          <p class="st-lede sk-fine">Adopting leaves the files where they are and starts a history for them. Nothing restarts.</p>
        </template>
      </section>
    </template>

    <StackHooks />
    <SelfUpdate :nodes="nodes" />
    <StackSecrets />
  </template>
</template>

<style>
.sk-row {
  grid-template-columns: auto minmax(0, 1fr) auto;
}
.sk-row .t {
  display: flex;
  flex-wrap: wrap;
  align-items: center;
  gap: 6px;
}
.sk-row .m {
  overflow-wrap: anywhere;
}
.sk-why {
  color: var(--text-2);
}
.sk-protected .g,
.sk-drift .g {
  width: 9px;
  height: 9px;
}
.sk-drift {
  border-color: color-mix(in oklch, var(--attention) 55%, var(--line-2));
}
.sk-sub {
  margin-top: var(--space-2);
  font: var(--weight-medium) var(--type-small) / 1.3 var(--font-ui);
  color: var(--text-2);
}
.sk-fine {
  font-size: var(--type-caption);
  color: var(--text-3);
}
</style>
