<script setup lang="ts">
/**
 * Settings → Security → Linked sign-ins (every role): the identity
 * provider accounts and proxy usernames that sign in as you.
 *
 * Link: confirm it's you (a step-up), then the browser goes to the
 * provider and comes back here with ?oidc=linked (or ?oidc_error=…). When
 * you came through a trusted proxy that names you, "Link" adds that name
 * in place. Unlink asks first, and needs a step-up; the hub refuses to
 * remove your last way in.
 *
 * Nothing shows on a hub without these routes (404), or when there's
 * nothing linked and nothing to link.
 */
import { computed, onMounted, ref } from 'vue';
import Glyph from '../ui/marks/Glyph.vue';
import { api, ApiError } from '../lib/api';
import { hubWords } from '../lib/actions';
import { ago, absolute } from '../lib/format';
import { StepUpCancelled } from '../stores/actions';
import { useUi } from '../stores/ui';
import UiButton from '../ui/Button.vue';
import { browser, identityPath, oidcErrorWords, oidcStartURL, takeOidcReturn, type IdentitiesView, type LinkedIdentity } from './signin';

const ui = useUi();
const status = ref<'loading' | 'ready' | 'error' | 'absent'>('loading');
const loadError = ref('');
const data = ref<IdentitiesView>({ identities: [] });
const busy = ref('');
const confirming = ref('');
const rowError = ref<Record<string, string>>({});
const linkError = ref('');
const now = Date.now();

const keyOf = (i: LinkedIdentity) => `${i.issuer} ${i.subject}`;

async function load(): Promise<void> {
  try {
    const v = await api<IdentitiesView>('/api/auth/identities');
    data.value = { ...v, identities: Array.isArray(v?.identities) ? v.identities : [] };
    status.value = 'ready';
  } catch (err) {
    if (err instanceof ApiError && err.status === 404) {
      status.value = 'absent';
      return;
    }
    status.value = 'error';
    loadError.value = hubWords(err, 'Your linked sign-ins couldn’t be loaded.');
  }
}

const shown = computed(
  () => status.value === 'loading' || status.value === 'error' || data.value.identities.length > 0 || !!data.value.provider || !!data.value.forward_auth_username,
);

function label(i: LinkedIdentity): string {
  if (i.kind === 'forward_auth') return `Proxy username ${i.subject}`;
  return i.email || i.subject;
}

function where(i: LinkedIdentity): string {
  if (i.kind === 'forward_auth') return 'through your proxy';
  try {
    return `at ${new URL(i.issuer).host}`;
  } catch {
    return `at ${i.issuer}`;
  }
}

async function linkProvider(): Promise<void> {
  linkError.value = '';
  // Linking needs a fresh step-up; the hub checks it when the browser arrives.
  if (!(await ui.stepUp('Link a sign-in to your account'))) return;
  browser.go(oidcStartURL({ link: true, returnTo: '/settings/security' }));
}

async function linkProxy(): Promise<void> {
  linkError.value = '';
  busy.value = 'proxy';
  try {
    await ui.withStepUp('Link a sign-in to your account', () => api('/api/auth/identities/forward-auth', { method: 'POST' }));
    ui.toast(`Linked the proxy username ${data.value.forward_auth_username}.`);
    await load();
  } catch (err) {
    if (!(err instanceof StepUpCancelled)) linkError.value = hubWords(err, 'That wasn’t linked.');
  } finally {
    busy.value = '';
  }
}

async function unlink(i: LinkedIdentity): Promise<void> {
  const k = keyOf(i);
  busy.value = k;
  rowError.value = { ...rowError.value, [k]: '' };
  try {
    await ui.withStepUp('Unlink a sign-in', () => api(identityPath(i), { method: 'DELETE' }));
    data.value = { ...data.value, identities: data.value.identities.filter((x) => keyOf(x) !== k) };
    confirming.value = '';
    ui.toast(`Unlinked ${label(i)}.`);
    void load();
  } catch (err) {
    if (!(err instanceof StepUpCancelled)) rowError.value = { ...rowError.value, [k]: hubWords(err, 'That wasn’t unlinked.') };
    confirming.value = '';
  } finally {
    busy.value = '';
  }
}

onMounted(() => {
  const back = takeOidcReturn();
  if (back.result === 'linked') ui.toast('Linked. You can sign in with it now.');
  else if (back.result === 'tested') ui.toast('Test sign-in done. The result is under Sign-in.');
  if (back.error) linkError.value = oidcErrorWords(back.error);
  void load();
});
</script>

<template>
  <section v-if="status !== 'absent' && (shown || linkError)" class="st-section" aria-labelledby="li-h">
    <header>
      <h3 id="li-h">Linked sign-ins</h3>
      <UiButton v-if="status === 'ready' && data.provider" size="sm" @click="linkProvider">Link {{ data.provider }}</UiButton>
    </header>
    <p class="st-lede">Accounts at your identity provider, or names your proxy sends, that sign in as you. Unlinking one doesn’t touch the account there.</p>
    <p v-if="linkError" class="err" role="alert">{{ linkError }}</p>

    <div v-if="status === 'loading'" class="st-skel" aria-busy="true"><span class="skel"></span></div>
    <div v-else-if="status === 'error'" class="st-note bad" role="alert"><Glyph kind="down" /><span>{{ loadError }}</span></div>
    <template v-else>
      <div v-if="data.forward_auth_username" class="st-note" role="note">
        <Glyph kind="link" />
        <span v-if="data.forward_auth_linked_elsewhere">Your proxy says you’re <b>{{ data.forward_auth_username }}</b>, which is linked to another Cha Dash user.</span>
        <span v-else>
          Your proxy says you’re <b>{{ data.forward_auth_username }}</b>. Link it, and the proxy signs you in from now on.
          <UiButton size="sm" :aria-busy="busy === 'proxy' || undefined" :aria-disabled="busy === 'proxy' ? 'true' : undefined" @click="linkProxy">Link {{ data.forward_auth_username }}</UiButton>
        </span>
      </div>
      <div v-if="!data.identities.length" class="st-empty">
        <div class="t">Nothing linked</div>
        <div class="m">{{ data.provider ? `Link your ${data.provider} account to sign in with it.` : 'Your password and passkeys are how you sign in.' }}</div>
      </div>
      <ul v-else class="st-list">
        <li v-for="i in data.identities" :key="keyOf(i)">
          <div class="st-row li-row">
            <span class="mono-badge" aria-hidden="true"><Glyph kind="link" /></span>
            <span>
              <span class="t">{{ label(i) }}</span>
              <span class="m">
                {{ where(i) }} · linked <span :title="absolute(i.created_at)">{{ ago(i.created_at, now) }}</span>
                <template v-if="i.last_used_at"> · last used <span :title="absolute(i.last_used_at)">{{ ago(i.last_used_at, now) }}</span></template>
              </span>
              <span v-if="rowError[keyOf(i)]" class="err" role="alert">{{ rowError[keyOf(i)] }}</span>
            </span>
            <span v-if="confirming !== keyOf(i)" class="end">
              <UiButton variant="ghost" size="sm" :aria-label="`Unlink ${label(i)}`" @click="confirming = keyOf(i)">Unlink…</UiButton>
            </span>
          </div>
          <div v-if="confirming === keyOf(i)" class="st-confirm li-confirm" role="alertdialog" :aria-label="`Unlink ${label(i)}?`">
            <span>Unlink <b>{{ label(i) }}</b>? It won’t sign in as you any more. You can link it again later.</span>
            <UiButton variant="danger-solid" :aria-disabled="busy === keyOf(i) ? 'true' : undefined" :aria-busy="busy === keyOf(i) || undefined" @click="unlink(i)">Unlink<span class="tier">T3</span></UiButton>
            <UiButton @click="confirming = ''">Cancel</UiButton>
          </div>
        </li>
      </ul>
    </template>
  </section>
</template>

<style>
.li-row {
  grid-template-columns: auto minmax(0, 1fr) auto;
}
.li-row .mono-badge .g {
  width: 12px;
  height: 12px;
}
.li-confirm {
  margin: 0 var(--space-2) var(--space-3);
}
</style>
