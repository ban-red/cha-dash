<script setup lang="ts">
/**
 * Settings → Nodes (owner, admin): chadash-node on each Docker host, as the
 * hub knows it (10 · The resource sheet, wave 3; 09 · v0.3, remote nodes).
 *
 *   GET  /api/nodes                      → {nodes, hub_pin, pairing}
 *   POST /api/nodes/{id}/revoke          → the node, revoked (audited)
 *   POST /api/nodes/pairing/regenerate   → 204, a fresh pairing secret (audited)
 *   GET  /api/nodes/tokens               → {tokens}: join tokens, never a secret
 *
 * Nodes waiting for approval come first, each as a PendingNode card with
 * Approve (step-up) and Reject. Add node (AddNode) makes a join token for a
 * node on another host; JoinTokens lists them. Each other node: connected
 * or last seen, version, address, its key, how it was enrolled, and its
 * policy in plain words. What the policy refuses is the node's decision,
 * and the page says the hub can't change it. Revoke asks for the node's
 * name typed out. The pairing token itself never reaches the page (the hub
 * never returns it): the pane shows how to read it on the hub's host.
 *
 * Its own chunk, loaded with the pane.
 */
import SettingsHelp from './SettingsHelp.vue';
import { computed, defineAsyncComponent, onMounted, ref, useId } from 'vue';
import Glyph from '../ui/marks/Glyph.vue';
import HoldToConfirm from '../components/HoldToConfirm.vue';
import { api } from '../lib/api';
import { focusFirst } from '../lib/focus';
import { hubWords } from '../lib/actions';
import { ago } from '../lib/format';
import { navigate, settingsPath } from '../lib/router';
import { attr, byKind } from '../model/graph';
import { useLive } from '../stores/live';
import { useUi } from '../stores/ui';
import UiButton from '../ui/Button.vue';
import JoinTokens from './JoinTokens.vue';
import PendingNode from './PendingNode.vue';
import { grantsOf, type HubNode, type JoinToken } from './nodes';

const AddNode = defineAsyncComponent(() => import('./AddNode.vue'));

const live = useLive();
const ui = useUi();
const uid = useId();

const nodes = ref<HubNode[]>([]);
const tokens = ref<JoinToken[]>([]);
const hubPin = ref('');
const pairing = ref(false);
const status = ref<'loading' | 'ready' | 'error'>('loading');
const loadError = ref('');

async function load(): Promise<void> {
  try {
    const [res, toks] = await Promise.all([
      api<{ nodes?: HubNode[]; hub_pin?: string; pairing?: boolean }>('/api/nodes'),
      api<{ tokens?: JoinToken[] }>('/api/nodes/tokens').catch(() => ({ tokens: [] as JoinToken[] })),
    ]);
    nodes.value = (res.nodes ?? []).slice().sort((a, b) => Number(a.state === 'revoked') - Number(b.state === 'revoked') || a.name.localeCompare(b.name));
    tokens.value = toks.tokens ?? [];
    hubPin.value = res.hub_pin ?? '';
    pairing.value = res.pairing === true;
    status.value = 'ready';
  } catch (err) {
    status.value = 'error';
    loadError.value = hubWords(err, 'Nodes couldn’t be loaded.');
  }
}
onMounted(() => void load());

const pending = computed(() => nodes.value.filter((n) => n.state === 'pending'));
const listed = computed(() => nodes.value.filter((n) => n.state !== 'pending'));

// ── Add node ──
const adding = ref(false);
function startAdd(): void {
  adding.value = true;
}
function closeAdd(): void {
  adding.value = false;
  void focusFirst(`${uid}-add`, 'nd-h');
}
function tokenMade(t: JoinToken): void {
  tokens.value = [t, ...tokens.value.filter((x) => x.id !== t.id)];
}
/** A pending card or a token changed on the hub: say so and read again. */
async function changed(message: string): Promise<void> {
  ui.toast(message);
  await load();
  void focusFirst('nd-h');
}

const glyph = (n: HubNode) => (n.state === 'revoked' ? 'cross' : n.connected ? 'ok' : 'off');
function stateWords(n: HubNode): string {
  if (n.state === 'revoked') {
    if (n.revoked_by === 'expired') return `expired unapproved${n.revoked_at ? ` ${ago(n.revoked_at, live.now.value)}` : ''}`;
    return `revoked${n.revoked_by ? ` by ${n.revoked_by}` : ''}${n.revoked_at ? ` ${ago(n.revoked_at, live.now.value)}` : ''}`;
  }
  if (n.connected) return 'connected';
  return n.last_seen_at ? `offline · last seen ${ago(n.last_seen_at, live.now.value)}` : 'never connected';
}
function enrolledWords(n: HubNode): string {
  const via =
    n.enrolled_via === 'local'
      ? 'paired with the hub’s pairing token'
      : n.enrolled_via === 'token'
        ? `joined with a token${n.join_token?.created_by_name ? ` ${n.join_token.created_by_name} made` : ''}`
        : (n.enrolled_via ?? '');
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
const active = computed(() => nodes.value.filter((n) => n.state === 'approved'));
</script>

<template>
  <section class="st-section" aria-labelledby="nd-h">
    <header>
      <h3 id="nd-h" tabindex="-1">Nodes</h3>
      <span v-if="status === 'ready'" class="st-meta">
        {{ active.length }} {{ active.length === 1 ? 'node' : 'nodes' }}<template v-if="pending.length"> · {{ pending.length }} waiting</template>
      </span>
      <UiButton v-if="status === 'ready' && !adding" size="sm" :id="`${uid}-add`" @click="startAdd">Add node</UiButton>
    </header>
    <SettingsHelp summary="chadash-node is Cha Dash’s hands on a Docker host, limited by its own policy file.">
      chadash-node is Cha Dash’s hands on a Docker host. Each one dials the hub and does only what its own policy file allows. The hub can’t widen that policy.
      A node on another host joins with a token from Add node, and waits here until an admin approves it.
    </SettingsHelp>

    <AddNode v-if="adding" @made="tokenMade" @close="closeAdd" />

    <div v-if="status === 'loading'" class="st-skel" aria-busy="true"><span class="skel"></span><span class="skel"></span></div>
    <div v-else-if="status === 'error'" class="st-note bad" role="alert">
      <Glyph kind="down" /><span>{{ loadError }}</span>
      <UiButton size="sm" @click="load">Try again</UiButton>
    </div>
    <template v-else>
      <div v-if="pending.length" class="nd-pendings" role="region" aria-label="Waiting for approval">
        <PendingNode v-for="n in pending" :key="n.id" :node="n" @changed="changed" />
      </div>
      <div v-if="!nodes.length" class="st-empty">
        <div class="t">No nodes yet</div>
        <div class="m">
          The shipped <span class="st-mono">compose.yaml</span> runs one beside the hub, and it pairs on its own. For another Docker host, use Add node and
          <span class="st-mono">compose.node.yaml</span>.
        </div>
      </div>
    </template>

    <ul v-if="status === 'ready' && listed.length" class="st-list nd-list" aria-label="Nodes">
      <li v-for="n in listed" :key="n.id" :class="{ dim: n.state === 'revoked' }">
        <div class="st-row nd-row">
          <Glyph :kind="glyph(n)" />
          <span>
            <span class="t">{{ n.name }}</span>
            <span class="m">{{ stateWords(n) }}<template v-if="n.version"> · {{ n.version }}</template><template v-if="n.address"> · {{ n.address }}</template></span>
          </span>
          <UiButton variant="ghost" size="sm" :id="`${uid}-dt-${n.id}`" :aria-expanded="open === n.id" :aria-controls="`${uid}-${n.id}`" @click="toggle(n.id)">
            {{ open === n.id ? 'Less' : 'Details' }}<span class="sr"> for {{ n.name }}</span>
          </UiButton>
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
            <UiButton v-if="dockerOf(n)" size="sm" :href="settingsPath('integrations', dockerOf(n))" @click.prevent="navigate(settingsPath('integrations', dockerOf(n)))">Its Docker integration</UiButton>
            <UiButton v-if="n.state !== 'revoked' && revoking !== n.id" variant="danger" size="sm" :id="`${uid}-rv-${n.id}`" @click="askRevoke(n)">Revoke {{ n.name }}</UiButton>
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
            <UiButton variant="ghost" size="sm" @click="cancelRevoke(n)">Cancel</UiButton>
            <UiButton variant="danger-solid" size="sm" :aria-disabled="typed !== n.name || busy || undefined" :aria-busy="busy || undefined" @click="revoke(n)">Revoke</UiButton>
          </div>
          <p v-if="rowError[n.id]" class="err" role="alert">{{ rowError[n.id] }}</p>
        </div>
      </li>
    </ul>
  </section>

  <JoinTokens v-if="status === 'ready'" :tokens="tokens" @changed="changed" />

  <section v-if="status === 'ready'" class="st-section" aria-labelledby="nd-p">
    <header>
      <h3 id="nd-p">Pairing</h3>
      <span class="st-meta">{{ pairing ? 'on' : 'off' }}</span>
    </header>
    <template v-if="pairing">
      <p class="st-lede">
        The node beside the hub pairs once with the hub’s pairing token, then uses its own key, and is approved at once. The token is single-use: pairing
        spends it, and the hub writes a fresh one. Cha Dash never shows it here. Read it on the hub’s host:
      </p>
      <pre class="nd-cmd st-mono" tabindex="0" aria-label="Command to read the pairing token">{{ READ_TOKEN }}</pre>
      <p class="st-lede">
        Pairing is accepted only from private and loopback addresses (<span class="st-mono">CHADASH_NODE_PAIRING_FROM</span>). For a node on another host,
        use Add node instead: <a :href="GUIDE" target="_blank" rel="noopener noreferrer">Nodes on other hosts</a> has the steps.
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
.nd-pendings {
  display: flex;
  flex-direction: column;
  gap: var(--space-3);
}
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
