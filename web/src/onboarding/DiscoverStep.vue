<script setup lang="ts">
/**
 * The wizard's Discover path. First the socket proxy that Cha Dash's
 * compose.yaml runs beside the hub, asked as soon as the step opens: it's
 * the hub's own sidecar. Then, only when asked, Docker endpoints the owner
 * ticks and machines the owner names. What answers can be added: Docker in
 * one click (it needs no credential), the rest through the add form, its
 * address filled in and its recipe beside it.
 */
import { computed, onMounted, reactive, ref } from 'vue';
import Glyph from '../ui/marks/Glyph.vue';
import { monogram } from '../lib/format';
import { navigate } from '../lib/router';
import { catalogEntry, createInstance, errorWords, integrations, loadInstances } from '../stores/integrations';
import type { InstanceDetail } from '../model/integrations';
import { useUi } from '../stores/ui';
import { setDraft } from './draft';
import { cache, candidateKey, discover, MAX_HOSTS, parseHosts, targetOf, WELL_KNOWN_DOCKER, type Candidate, type DiscoverResult } from './discover';
import { onboarding } from './state';
import UiButton from '../ui/Button.vue';
import UiField from '../ui/Field.vue';

const ui = useUi();

type Probe = { state: 'idle' } | { state: 'checking' } | { state: 'done'; result: DiscoverResult } | { state: 'error'; message: string };

const sidecar = ref<Probe>({ state: 'idle' });
const further = ref<Probe>({ state: 'idle' });
const hostsText = ref('');
const picked = reactive(new Set<string>());
const inputError = ref('');
const adding = ref('');
const added = reactive(new Map<string, string>());

const sidecarFound = computed(() => (sidecar.value.state === 'done' ? sidecar.value.result.found[0] : undefined));
const sidecarWhy = computed(() => (sidecar.value.state === 'done' ? sidecar.value.result.checked[0]?.error : undefined));
const sidecarHost = computed(() => onboarding.sidecar.replace(/^tcp:\/\//, ''));
/** compose.yaml's chadash-node (node://local), or the older socket proxy. */
const viaNode = computed(() => onboarding.sidecar.startsWith('node://'));
const sidecarWhat = computed(() => (viaNode.value ? 'chadash-node' : 'the socket proxy'));
const furtherFound = computed(() => (further.value.state === 'done' ? further.value.result.found : []));
const furtherMisses = computed(() => (further.value.state === 'done' ? further.value.result.checked.filter((c) => c.found === 0) : []));

async function checkSidecar(force = false): Promise<void> {
  if (cache.sidecar && !force) {
    sidecar.value = { state: 'done', result: cache.sidecar };
    return;
  }
  sidecar.value = { state: 'checking' };
  try {
    const result = await discover({ sidecar: true });
    cache.sidecar = result;
    sidecar.value = { state: 'done', result };
  } catch (err) {
    sidecar.value = { state: 'error', message: errorWords(err, 'The hub couldn’t look. Try again.') };
  }
}

async function checkFurther(): Promise<void> {
  inputError.value = '';
  const { hosts, bad } = parseHosts(hostsText.value);
  if (bad.length) {
    inputError.value = `${bad.map((b) => `“${b}”`).join(', ')} ${bad.length === 1 ? 'isn’t' : 'aren’t'} one machine each. Use a name or an address, without http://, a port, a range or a subnet.`;
    return;
  }
  if (hosts.length > MAX_HOSTS) {
    inputError.value = `At most ${MAX_HOSTS} machines at a time.`;
    return;
  }
  if (!hosts.length && !picked.size) {
    inputError.value = 'Name a machine, or tick an address to try.';
    return;
  }
  further.value = { state: 'checking' };
  try {
    further.value = { state: 'done', result: await discover({ docker: [...picked], hosts }) };
  } catch (err) {
    further.value = { state: 'error', message: errorWords(err, 'The hub couldn’t look. Try again.') };
  }
}

const same = (a: string, b: string) => a.trim().replace(/\/+$/, '').toLowerCase() === b.trim().replace(/\/+$/, '').toLowerCase();
/** Already read by an instance: the hub said so, it was just added, or the list (which carries configs) shows it. */
const isAdded = (c: Candidate) =>
  Boolean(
    c.added ||
      added.get(candidateKey(c)) ||
      (integrations.instances as InstanceDetail[]).some((x) => x.integration === c.integration && x.config && same(targetOf({ config: x.config }), targetOf(c))),
  );
const kindName = (c: Candidate) => catalogEntry(c.integration)?.name ?? c.integration;

/** What the test warned about: a raw Docker API, the bare socket, a proxy that lets too much through. */
const accessWarnings = (c: Candidate) => c.warnings.filter((w) => w.severity !== 'info');

async function add(c: Candidate): Promise<void> {
  if (!c.ready) {
    setDraft(c.integration, { name: c.name, config: c.config });
    navigate(`/settings/integrations/new/${encodeURIComponent(c.integration)}`);
    return;
  }
  const key = candidateKey(c);
  adding.value = key;
  try {
    const id = await createInstance({ integration: c.integration, name: c.name, config: c.config, secrets: {} });
    added.set(key, id);
    ui.toast(`Added ${c.name}. Cha Dash is reading it now.`);
    void loadInstances(true);
  } catch (err) {
    ui.toast(errorWords(err, `Couldn’t add ${c.name}.`), 'error');
  } finally {
    adding.value = '';
  }
}

onMounted(() => void checkSidecar());
</script>

<template>
  <div class="ob-discover">
    <section class="ob-block" aria-labelledby="ob-side-h">
      <h3 id="ob-side-h" class="caps">Docker on this host</h3>
      <p v-if="sidecar.state === 'checking' || sidecar.state === 'idle'" class="ob-quiet" aria-busy="true">
        Asking {{ sidecarWhat }} that came with Cha Dash, at <code>{{ sidecarHost }}</code>…
      </p>
      <div v-else-if="sidecar.state === 'error'" class="form-error" role="alert">
        <Glyph kind="down" /><span>{{ sidecar.message }}</span>
        <UiButton size="sm" @click="checkSidecar(true)">Try again</UiButton>
      </div>
      <ul v-else-if="sidecarFound" class="ob-cands">
        <li class="ob-cand">
          <span class="ob-badge" aria-hidden="true">{{ monogram(kindName(sidecarFound)) }}</span>
          <span class="ob-cand-main">
            <b>Docker on this host</b>
            <span class="ob-m">
              {{ viaNode ? 'Through chadash-node' : 'Through the read-only socket proxy' }}<template v-if="sidecarFound.version"> · Docker <span class="n">{{ sidecarFound.version }}</span></template>
            </span>
            <span v-for="w in accessWarnings(sidecarFound)" :key="w.code" class="ob-warn"><Glyph kind="attn" /> {{ w.message }}</span>
          </span>
          <span class="ob-cand-end">
            <span v-if="isAdded(sidecarFound)" class="ob-done"><Glyph kind="check" /> Added</span>
            <UiButton v-else variant="primary" size="sm" :disabled="adding === candidateKey(sidecarFound)" :aria-busy="adding === candidateKey(sidecarFound)" @click="add(sidecarFound)">
              {{ adding === candidateKey(sidecarFound) ? 'Adding…' : 'Add' }}
            </UiButton>
          </span>
        </li>
      </ul>
      <p v-else class="ob-quiet" role="status">
        Nothing answered at <code>{{ sidecarHost }}</code><template v-if="sidecarWhy"> ({{ sidecarWhy }})</template>. That’s expected if you
        removed {{ sidecarWhat }} from <code>compose.yaml</code>, or run Cha Dash without Compose.
      </p>
    </section>

    <section class="ob-block" aria-labelledby="ob-more-h">
      <h3 id="ob-more-h" class="caps">Look further</h3>
      <p class="ob-quiet">
        Cha Dash checks only what you list here: a few GET requests to the usual ports of the services it knows, on each machine. It never scans your network, and it sends no
        passwords.
      </p>
      <UiField id="ob-hosts" label="Machines to check" :help="`One per line: a name or an address. Up to ${MAX_HOSTS}.`" v-slot="{ control }">
        <textarea v-bind="control" v-model="hostsText" class="ob-hosts" rows="3" spellcheck="false" autocapitalize="off" :placeholder="'nas.home.example\n192.0.2.20'"></textarea>
      </UiField>
      <fieldset class="ob-wellknown">
        <legend>Also try these Docker addresses</legend>
        <label v-for="w in WELL_KNOWN_DOCKER" :key="w.endpoint" class="ob-check">
          <input type="checkbox" :checked="picked.has(w.endpoint)" @change="picked.has(w.endpoint) ? picked.delete(w.endpoint) : picked.add(w.endpoint)" />
          <span><code>{{ w.endpoint }}</code> <span class="ob-m">{{ w.note }}</span></span>
        </label>
      </fieldset>
      <p v-if="inputError" class="form-error" role="alert"><Glyph kind="attn" /><span>{{ inputError }}</span></p>
      <div class="ob-acts">
        <UiButton :disabled="further.state === 'checking'" :aria-busy="further.state === 'checking'" @click="checkFurther">
          {{ further.state === 'checking' ? 'Checking…' : 'Check these' }}
        </UiButton>
      </div>

      <div v-if="further.state === 'error'" class="form-error" role="alert"><Glyph kind="down" /><span>{{ further.message }}</span></div>
      <template v-else-if="further.state === 'done'">
        <ul v-if="furtherFound.length" class="ob-cands" aria-label="Found">
          <li v-for="c in furtherFound" :key="candidateKey(c)" class="ob-cand">
            <span class="ob-badge" aria-hidden="true">{{ monogram(kindName(c)) }}</span>
            <span class="ob-cand-main">
              <b>{{ c.name }}</b>
              <span class="ob-m">
                {{ kindName(c) }} · <span class="n">{{ targetOf(c) }}</span><template v-if="c.version"> · {{ c.version }}</template>
              </span>
              <span v-for="w in accessWarnings(c)" :key="w.code" class="ob-warn"><Glyph kind="attn" /> {{ w.message }}</span>
            </span>
            <span class="ob-cand-end">
              <span v-if="isAdded(c)" class="ob-done"><Glyph kind="check" /> Added</span>
              <UiButton v-else-if="c.ready" variant="primary" size="sm" :disabled="adding === candidateKey(c)" :aria-busy="adding === candidateKey(c)" @click="add(c)">
                {{ adding === candidateKey(c) ? 'Adding…' : 'Add' }}
              </UiButton>
              <UiButton v-else size="sm" @click="add(c)">Set up…</UiButton>
            </span>
          </li>
        </ul>
        <p v-if="!furtherFound.length" class="ob-quiet" role="status">Nothing Cha Dash knows answered there.</p>
        <ul v-if="furtherMisses.length" class="ob-misses">
          <li v-for="m in furtherMisses" :key="m.target">
            <code>{{ m.target }}</code>: {{ m.error ?? 'nothing Cha Dash knows answered' }}
          </li>
        </ul>
      </template>
    </section>
  </div>
</template>
