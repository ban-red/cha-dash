<script setup lang="ts">
/**
 * Settings → Nodes (owner, admin): chadash-node on each Docker host, as the
 * hub knows it (10 · The resource sheet, wave 3).
 *
 *   GET  /api/nodes                      → {nodes, hub_pin, pairing}
 *   POST /api/nodes/{id}/revoke          → the node, revoked (audited)
 *   POST /api/nodes/pairing/regenerate   → 204, a fresh pairing secret (audited)
 *
 * Each node: connected or last seen, version, address, its key, how it was
 * enrolled, and its policy in plain words. What the policy refuses is the
 * node's decision, and the page says the hub can't change it. Revoke asks
 * for the node's name typed out. The pairing token itself never reaches the
 * page (the hub never returns it): the pane shows how to read it on the
 * hub's host.
 *
 * Its own chunk, loaded with the pane.
 */
import SettingsHelp from './SettingsHelp.vue';
import { computed, onMounted, ref, useId } from 'vue';
import Glyph from '../components/marks/Glyph.vue';
import HoldToConfirm from '../components/HoldToConfirm.vue';
import { api } from '../lib/api';
import { focusFirst } from '../lib/focus';
import { hubWords } from '../lib/actions';
import { ago } from '../lib/format';
import { navigate, settingsPath } from '../lib/router';
import { attr, byKind } from '../model/graph';
import { useLive } from '../stores/live';
import { useUi } from '../stores/ui';

interface Verb {
  name: string;
  policy: string;
  allowed: boolean;
}
export interface HubNode {
  id: string;
  name: string;
  fingerprint: string;
  state: string;
  enrolled_via?: string;
  approved_by?: string;
  approved_at?: string;
  revoked_by?: string;
  revoked_at?: string;
  last_seen_at?: string;
  address?: string;
  version?: string;
  verbs?: Verb[];
  connected?: boolean;
  hub_key_current?: boolean;
}

const live = useLive();
const ui = useUi();
const uid = useId();

const nodes = ref<HubNode[]>([]);
const hubPin = ref('');
const pairing = ref(false);
const status = ref<'loading' | 'ready' | 'error'>('loading');
const loadError = ref('');

async function load(): Promise<void> {
  try {
    const res = await api<{ nodes?: HubNode[]; hub_pin?: string; pairing?: boolean }>('/api/nodes');
    nodes.value = (res.nodes ?? []).slice().sort((a, b) => Number(a.state === 'revoked') - Number(b.state === 'revoked') || a.name.localeCompare(b.name));
    hubPin.value = res.hub_pin ?? '';
    pairing.value = res.pairing === true;
    status.value = 'ready';
  } catch (err) {
    status.value = 'error';
    loadError.value = hubWords(err, 'Nodes couldn’t be loaded.');
  }
}
onMounted(() => void load());

/** What each policy grant lets Cha Dash do, in words (internal/node/policy.go). */
const GRANTS: Record<string, string> = {
  'docker.read': 'Read containers and the engine',
  'docker.logs': 'Read container logs',
  'docker.lifecycle': 'Start, stop and restart containers',
  'docker.update': 'Pull images and recreate containers',
  'docker.remove': 'Remove containers',
  'docker.exec': 'Open a shell in containers',
  'stacks.read': 'Read stacks and their history',
  'stacks.deploy': 'Deploy and roll back stacks',
  'stacks.adopt': 'Adopt running compose projects',
  'stacks.sync': 'Follow stacks from git',
  'host.metrics': 'Read host metrics',
  'host.smart': 'Read disk health (SMART)',
};
/** A node's verbs as grants: each policy once, allowed when the node allows its verbs. */
function grantsOf(n: HubNode): { policy: string; words: string; allowed: boolean }[] {
  const by = new Map<string, boolean>();
  for (const v of n.verbs ?? []) by.set(v.policy, (by.get(v.policy) ?? false) || v.allowed);
  return [...by.entries()].map(([policy, allowed]) => ({ policy, words: GRANTS[policy] ?? policy, allowed })).sort((a, b) => a.policy.localeCompare(b.policy));
}

const glyph = (n: HubNode) => (n.state === 'revoked' ? 'cross' : n.connected ? 'ok' : 'off');
function stateWords(n: HubNode): string {
  if (n.state === 'revoked') return `revoked${n.revoked_by ? ` by ${n.revoked_by}` : ''}${n.revoked_at ? ` ${ago(n.revoked_at, live.now.value)}` : ''}`;
  if (n.connected) return 'connected';
  return n.last_seen_at ? `offline · last seen ${ago(n.last_seen_at, live.now.value)}` : 'never connected';
}
function enrolledWords(n: HubNode): string {
  const via = n.enrolled_via === 'local' ? 'paired with the hub’s pairing token' : (n.enrolled_via ?? '');
  return [via, n.approved_by ? `approved by ${n.approved_by}` : '', n.approved_at ? ago(n.approved_at, live.now.value) : ''].filter(Boolean).join(' · ');
}
/** The Docker integration that reads through this node (`node://<name>`), when there is one. */
function dockerOf(n: HubNode): string | undefined {
  const engine = byKind(live.graph.value, 'engine').find((e) => attr(e, 'node') === n.name);
  return engine?.source?.startsWith('integration:') ? engine.source.slice('integration:'.length) : undefined;
}

const open = ref<string | undefined>();
const toggle = (id: string) => (open.value = open.value === id ? undefined : id);

// ── Revoke ──
const revoking = ref<string | undefined>();
const typed = ref('');
const busy = ref(false);
const rowError = ref<Record<string, string>>({});
function askRevoke(n: HubNode): void {
  revoking.value = n.id;
  typed.value = '';
  void focusFirst(`${uid}-rt-${n.id}`);
}
function cancelRevoke(n: HubNode): void {
  revoking.value = undefined;
  void focusFirst(`${uid}-rv-${n.id}`);
}
async function revoke(n: HubNode): Promise<void> {
  if (typed.value !== n.name || busy.value) return;
  busy.value = true;
  try {
    await api(`/api/nodes/${encodeURIComponent(n.id)}/revoke`, { method: 'POST' });
    revoking.value = undefined;
    ui.toast(`Revoked ${n.name}. Its key won’t work again.`);
    await load();
    void focusFirst(`${uid}-dt-${n.id}`, 'nd-h');
  } catch (err) {
    rowError.value = { ...rowError.value, [n.id]: hubWords(err, `${n.name} wasn’t revoked.`) };
  } finally {
    busy.value = false;
  }
}

// ── Pairing ──
const regenError = ref('');
const regenerated = ref(false);
/** A fresh hold button after each regenerate, so it can be done again. */
const regenCount = ref(0);
async function regenerate(): Promise<void> {
  regenError.value = '';
  try {
    await api('/api/nodes/pairing/regenerate', { method: 'POST' });
    regenerated.value = true;
    regenCount.value++;
    ui.toast('A fresh pairing token is in the pairing file. The old one no longer works.');
  } catch (err) {
    regenError.value = hubWords(err, 'The pairing token wasn’t regenerated.');
  }
}
const READ_TOKEN = 'docker run --rm -v chadash_pairing:/p:ro busybox cat /p/token';
const GUIDE = 'https://github.com/ban-red/cha-dash/blob/main/deploy/README.md#nodes-on-other-hosts';
const active = computed(() => nodes.value.filter((n) => n.state !== 'revoked'));
</script>

<template>
  <section class="st-section" aria-labelledby="nd-h">
    <header>
      <h3 id="nd-h" tabindex="-1">Nodes</h3>
      <span v-if="status === 'ready'" class="st-meta">{{ active.length }} {{ active.length === 1 ? 'node' : 'nodes' }}</span>
    </header>
    <SettingsHelp summary="chadash-node is Cha Dash’s hands on a Docker host, limited by its own policy file.">
      chadash-node is Cha Dash’s hands on a Docker host. Each one dials the hub and does only what its own policy file allows. The hub can’t widen that policy.
    </SettingsHelp>

    <div v-if="status === 'loading'" class="st-skel" aria-busy="true"><span class="skel"></span><span class="skel"></span></div>
    <div v-else-if="status === 'error'" class="st-note bad" role="alert">
      <Glyph kind="down" /><span>{{ loadError }}</span>
      <button type="button" class="btn sm" @click="load">Try again</button>
    </div>
    <div v-else-if="!nodes.length" class="st-empty">
      <div class="t">No nodes yet</div>
      <div class="m">
        The shipped <span class="st-mono">compose.yaml</span> runs one beside the hub, and it pairs on its own. For another Docker host, use
        <span class="st-mono">compose.node.yaml</span> and the pairing token below.
      </div>
    </div>

    <ul v-else class="st-list nd-list" aria-label="Nodes">
      <li v-for="n in nodes" :key="n.id" :class="{ dim: n.state === 'revoked' }">
        <div class="st-row nd-row">
          <Glyph :kind="glyph(n)" />
          <span>
            <span class="t">{{ n.name }}</span>
            <span class="m">{{ stateWords(n) }}<template v-if="n.version"> · {{ n.version }}</template><template v-if="n.address"> · {{ n.address }}</template></span>
          </span>
          <button :id="`${uid}-dt-${n.id}`" type="button" class="btn sm ghost" :aria-expanded="open === n.id" :aria-controls="`${uid}-${n.id}`" @click="toggle(n.id)">
            {{ open === n.id ? 'Less' : 'Details' }}<span class="sr"> for {{ n.name }}</span>
          </button>
        </div>

        <div v-if="open === n.id" :id="`${uid}-${n.id}`" class="nd-detail">
          <dl class="nd-kv">
            <dt>Key</dt>
            <dd class="st-mono">{{ n.fingerprint }}</dd>
            <dt>Enrolled</dt>
            <dd>{{ enrolledWords(n) || '—' }}</dd>
          </dl>
          <p v-if="n.state !== 'revoked' && n.hub_key_current === false" class="st-note" role="note">
            <Glyph kind="attn" /><span>{{ n.name }} hasn’t taken the hub’s newest key yet. It will when it next connects.</span>
          </p>
          <template v-if="grantsOf(n).length">
            <h4 class="nd-h4">What its policy allows</h4>
            <ul class="nd-grants">
              <li v-for="g in grantsOf(n)" :key="g.policy">
                <Glyph :kind="g.allowed ? 'check' : 'cross'" :label="g.allowed ? 'allowed' : 'refused'" />
                <span>{{ g.words }}</span>
                <small class="st-mono">{{ g.policy }}</small>
              </li>
            </ul>
            <p class="st-lede">Refused means refused by {{ n.name }}’s own policy file. Change it on that host; the hub can’t.</p>
          </template>
          <div class="st-acts">
            <a v-if="dockerOf(n)" class="btn sm" :href="settingsPath('integrations', dockerOf(n))" @click.prevent="navigate(settingsPath('integrations', dockerOf(n)))">Its Docker integration</a>
            <button v-if="n.state !== 'revoked' && revoking !== n.id" :id="`${uid}-rv-${n.id}`" type="button" class="btn sm danger" @click="askRevoke(n)">Revoke {{ n.name }}</button>
          </div>
          <div v-if="revoking === n.id" class="st-confirm nd-confirm" role="group" :aria-labelledby="`${uid}-rq-${n.id}`">
            <span :id="`${uid}-rq-${n.id}`">
              Revoking ends {{ n.name }}’s session now, and its key never works again. The hub writes a fresh pairing token, and the Docker integration reading
              through it stops updating. Type <b class="st-mono">{{ n.name }}</b> to confirm.
            </span>
            <input
              :id="`${uid}-rt-${n.id}`"
              v-model="typed"
              class="st-mono"
              type="text"
              autocomplete="off"
              spellcheck="false"
              :aria-label="`Type ${n.name} to confirm`"
            />
            <button type="button" class="btn sm ghost" @click="cancelRevoke(n)">Cancel</button>
            <button type="button" class="btn sm danger solid" :aria-disabled="typed !== n.name || busy || undefined" :aria-busy="busy || undefined" @click="revoke(n)">Revoke</button>
          </div>
          <p v-if="rowError[n.id]" class="err" role="alert">{{ rowError[n.id] }}</p>
        </div>
      </li>
    </ul>
  </section>

  <section v-if="status === 'ready'" class="st-section" aria-labelledby="nd-p">
    <header>
      <h3 id="nd-p">Pairing</h3>
      <span class="st-meta">{{ pairing ? 'on' : 'off' }}</span>
    </header>
    <template v-if="pairing">
      <p class="st-lede">
        A new node pairs once with the hub’s pairing token, then uses its own key. The token is single-use: pairing spends it, and the hub writes a fresh one.
        Cha Dash never shows it here. Read it on the hub’s host:
      </p>
      <pre class="nd-cmd st-mono" tabindex="0" aria-label="Command to read the pairing token">{{ READ_TOKEN }}</pre>
      <p class="st-lede">
        Pairing is accepted only from private and loopback addresses (<span class="st-mono">CHADASH_NODE_PAIRING_FROM</span>).
        <a :href="GUIDE" target="_blank" rel="noopener noreferrer">Nodes on other hosts</a> has the steps.
      </p>
      <div class="st-acts">
        <HoldToConfirm :key="regenCount" label="regenerate the pairing token" :hint="`${uid}-regen`" @confirm="regenerate" />
      </div>
      <p :id="`${uid}-regen`" class="st-lede">Hold to replace the token: a copy anyone made stops working.{{ regenerated ? ' Done.' : '' }}</p>
      <p v-if="regenError" class="err" role="alert">{{ regenError }}</p>
    </template>
    <p v-else class="st-lede">
      Pairing is off on this hub (<span class="st-mono">CHADASH_NODE_PAIRING_FILE</span> isn’t set), so no new node can join. Nodes already enrolled keep working.
    </p>
    <dl v-if="hubPin" class="nd-kv">
      <dt>Hub key</dt>
      <dd class="st-mono">{{ hubPin }}</dd>
    </dl>
  </section>
</template>

<style>
.nd-list li.dim {
  opacity: 0.6;
}
.nd-row {
  grid-template-columns: auto minmax(0, 1fr) auto;
}
.nd-detail {
  display: flex;
  flex-direction: column;
  gap: var(--space-3);
  padding: var(--space-2) var(--space-3) var(--space-4) calc(var(--space-3) + 20px);
}
.nd-kv {
  display: grid;
  grid-template-columns: minmax(6em, auto) minmax(0, 1fr);
  gap: 6px var(--space-4);
  margin: 0;
  font-size: var(--type-small);
}
.nd-kv dt {
  color: var(--text-3);
}
.nd-kv dd {
  margin: 0;
  overflow-wrap: anywhere;
}
.nd-h4 {
  margin: 0;
  font: var(--weight-medium) var(--type-caption) / 1.2 var(--font-ui);
  color: var(--text-3);
}
.nd-grants {
  display: flex;
  flex-direction: column;
  gap: 4px;
  margin: 0;
  padding: 0;
  list-style: none;
  font-size: var(--type-small);
}
.nd-grants li {
  display: flex;
  align-items: baseline;
  gap: var(--space-2);
}
.nd-grants small {
  color: var(--text-3);
}
.nd-confirm input {
  flex: 0 1 12rem;
  min-height: var(--target-min);
}
.nd-cmd {
  margin: 0;
  padding: 8px 10px;
  border: var(--hairline) solid var(--line);
  border-radius: var(--radius-md);
  font-size: var(--type-caption);
  white-space: pre-wrap;
  overflow-wrap: anywhere;
}
</style>
