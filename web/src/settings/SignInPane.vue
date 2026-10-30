<script setup lang="ts">
/**
 * Settings → Security → Sign-in (owner only; ADR-0022): one identity
 * provider over OIDC, forward-auth headers from a trusted proxy, the
 * group → role mappings both use, creating accounts on a first sign-in,
 * and hiding the password form.
 *
 * Nothing saves until Save, and Save needs a step-up (the hub answers
 * `step_up_required`; ui.withStepUp runs the dialog and tries again). The
 * client secret is write-only: the hub says whether one is set, never what
 * it is. Test asks the hub to read the provider's discovery document and,
 * with OIDC on and saved, sends you through a test sign-in that signs
 * nobody in; its result shows here when you come back.
 *
 * A hub without these routes (404) shows nothing.
 */
import { computed, onMounted, ref, useId } from 'vue';
import Glyph from '../ui/marks/Glyph.vue';
import { api, ApiError } from '../lib/api';
import { hubWords } from '../lib/actions';
import { absolute } from '../lib/format';
import { ROLE_WORDS } from '../model/roles';
import { StepUpCancelled } from '../stores/actions';
import { useUi } from '../stores/ui';
import UiButton from '../ui/Button.vue';
import UiField from '../ui/Field.vue';
import { asSettings, browser, GROUP_ROLES, type SignInSettings, type SignInTest, type SignInView } from './signin';

const ui = useUi();
const uid = useId();

const status = ref<'loading' | 'ready' | 'error' | 'absent'>('loading');
const loadError = ref('');
const view = ref<SignInView | undefined>(undefined);
const draft = ref<SignInSettings>(asSettings(undefined));
/** The scopes as typed: space-separated. */
const scopes = ref('');
const secret = ref('');
const clearSecret = ref(false);
const saving = ref(false);
const saveError = ref('');
const testing = ref(false);
const test = ref<SignInTest | undefined>(undefined);
const testError = ref('');
const copied = ref(false);

function fill(v: SignInView): void {
  view.value = v;
  draft.value = asSettings(v);
  scopes.value = draft.value.oidc.scopes.join(' ');
  secret.value = '';
  clearSecret.value = false;
}

async function load(): Promise<void> {
  try {
    fill(await api<SignInView>('/api/settings/sign-in'));
    status.value = 'ready';
  } catch (err) {
    if (err instanceof ApiError && err.status === 404) {
      status.value = 'absent';
      return;
    }
    status.value = 'error';
    loadError.value = hubWords(err, 'The sign-in settings couldn’t be loaded.');
  }
}

function body(): Record<string, unknown> {
  const d = draft.value;
  const out: Record<string, unknown> = {
    ...d,
    oidc: { ...d.oidc, scopes: scopes.value.split(/[\s,]+/).filter(Boolean) },
    mappings: d.mappings.filter((m) => m.group.trim() !== ''),
  };
  if (clearSecret.value) out.client_secret = '';
  else if (secret.value) out.client_secret = secret.value;
  return out;
}

const dirty = computed(() => {
  const v = view.value;
  if (!v) return false;
  if (secret.value || clearSecret.value) return true;
  const saved = asSettings(v);
  return JSON.stringify({ ...draft.value, oidc: { ...draft.value.oidc, scopes: scopes.value.split(/[\s,]+/).filter(Boolean) } }) !== JSON.stringify(saved);
});

async function save(): Promise<void> {
  if (saving.value || !dirty.value) return;
  saving.value = true;
  saveError.value = '';
  try {
    const res = await ui.withStepUp('Change how people sign in', () => api<SignInView>('/api/settings/sign-in', { method: 'PUT', body: body() }));
    fill(res);
    ui.toast('Sign-in settings saved.');
  } catch (err) {
    if (!(err instanceof StepUpCancelled)) saveError.value = hubWords(err, 'The sign-in settings weren’t saved.');
  } finally {
    saving.value = false;
  }
}

async function runTest(): Promise<void> {
  if (testing.value) return;
  testing.value = true;
  testError.value = '';
  test.value = undefined;
  try {
    test.value = await api<SignInTest>('/api/settings/sign-in/test', { method: 'POST', body: {} });
  } catch (err) {
    testError.value = hubWords(err, 'The test couldn’t run.');
  } finally {
    testing.value = false;
  }
}

function tryLogin(): void {
  if (test.value?.login_url) browser.go(test.value.login_url);
}

function addMapping(): void {
  draft.value.mappings.push({ group: '', role: 'viewer' });
}

function removeMapping(i: number): void {
  draft.value.mappings.splice(i, 1);
}

async function copyRedirect(): Promise<void> {
  try {
    await navigator.clipboard.writeText(view.value?.redirect_uri ?? '');
    copied.value = true;
    setTimeout(() => (copied.value = false), 2000);
  } catch {
    copied.value = false;
  }
}

const authTimeWords = computed(() => {
  switch (test.value?.auth_time) {
    case 'listed':
      return 'The provider lists auth_time, so people without a passkey can confirm it’s them there.';
    case 'not_listed':
      return 'The provider doesn’t list auth_time. People without a passkey or password may not be able to confirm destructive actions. A test sign-in says for sure.';
    default:
      return 'The provider doesn’t say which claims it sends. A test sign-in shows whether it sends auth_time.';
  }
});

const last = computed(() => view.value?.last_test ?? null);
const groupsSeen = computed(() => view.value?.forward_groups ?? null);
/** Mappings are saved and forward-auth is on, but no proxy has sent a groups header yet. */
const groupsMissing = computed(() => !!view.value && view.value.forward_auth.enabled && view.value.mappings.length > 0 && !groupsSeen.value);

onMounted(load);
</script>

<template>
  <section v-if="status !== 'absent'" class="st-section si" aria-labelledby="si-h">
    <header><h3 id="si-h">Sign-in</h3></header>
    <p class="st-lede">
      Let people sign in with your identity provider (Pocket ID, Authelia, authentik, Keycloak, Kanidm) or through a proxy that has already signed them in.
      Accounts are linked by the provider’s ID, never by email. You can always sign in here with your password or passkey.
    </p>

    <div v-if="status === 'loading'" class="st-skel" aria-busy="true"><span class="skel"></span><span class="skel"></span><span class="skel"></span></div>
    <div v-else-if="status === 'error'" class="st-note bad" role="alert"><Glyph kind="down" /><span>{{ loadError }}</span></div>

    <form v-else class="si-form" novalidate :aria-busy="saving || undefined" @submit.prevent="save">
      <fieldset class="si-group">
        <legend>Identity provider (OIDC)</legend>
        <UiField check label="Offer “Sign in with” the provider on the sign-in page" v-slot="{ control }">
          <input v-bind="control" v-model="draft.oidc.enabled" type="checkbox" />
        </UiField>
        <div class="si-grid">
          <UiField label="Button name" optional help="Shown as “Sign in with …”." v-slot="{ control }">
            <input v-bind="control" v-model="draft.oidc.name" maxlength="40" placeholder="Pocket ID" autocomplete="off" />
          </UiField>
          <UiField label="Issuer URL" help="Exactly as the provider’s discovery document names it." v-slot="{ control }">
            <input v-bind="control" v-model="draft.oidc.issuer" type="url" inputmode="url" placeholder="https://id.home.example" autocomplete="off" spellcheck="false" />
          </UiField>
          <UiField label="Client ID" v-slot="{ control }">
            <input v-bind="control" v-model="draft.oidc.client_id" autocomplete="off" spellcheck="false" />
          </UiField>
          <UiField label="Client secret" optional :help="view?.client_secret_set ? 'A secret is saved in the vault. Type a new one to replace it.' : 'Kept in the vault and never shown again. Leave it empty for a public client.'" v-slot="{ control }">
            <input v-bind="control" v-model="secret" type="password" autocomplete="new-password" :placeholder="view?.client_secret_set ? 'saved' : ''" :disabled="clearSecret" />
          </UiField>
          <UiField label="Scopes" help="Space-separated. openid is always asked for." v-slot="{ control }">
            <input v-bind="control" v-model="scopes" autocomplete="off" spellcheck="false" />
          </UiField>
          <UiField label="Group claim" help="The ID token claim that lists someone’s groups." v-slot="{ control }">
            <input v-bind="control" v-model="draft.oidc.group_claim" autocomplete="off" spellcheck="false" />
          </UiField>
        </div>
        <UiField v-if="view?.client_secret_set" check label="Remove the saved client secret" v-slot="{ control }">
          <input v-bind="control" v-model="clearSecret" type="checkbox" />
        </UiField>
        <div class="si-redirect">
          <span class="si-k" :id="`${uid}-ru`">Redirect URI to register with the provider</span>
          <code class="st-mono" :aria-labelledby="`${uid}-ru`">{{ view?.redirect_uri }}</code>
          <UiButton size="sm" variant="ghost" @click="copyRedirect">{{ copied ? 'Copied' : 'Copy' }}</UiButton>
        </div>
        <div class="st-acts">
          <UiButton size="sm" :disabled="!view?.oidc.issuer" :aria-busy="testing || undefined" @click="runTest">{{ testing ? 'Testing…' : 'Test' }}</UiButton>
          <span v-if="dirty" class="si-hint">Test uses the saved settings. Save first to test changes.</span>
        </div>
        <p v-if="testError" class="err" role="alert">{{ testError }}</p>
        <div v-if="test" class="si-test" role="status">
          <p v-if="!test.ok" class="st-note bad"><Glyph kind="down" /><span>{{ test.error }}</span></p>
          <template v-else>
            <p class="st-note"><Glyph kind="ok" /><span>Found <span class="st-mono">{{ test.issuer }}</span>. {{ authTimeWords }}</span></p>
            <p v-if="test.login_url" class="si-hint">
              <UiButton size="sm" @click="tryLogin">Try a test sign-in</UiButton>
              You’ll sign in at the provider and come back here. It signs nobody in and links nothing.
            </p>
            <p v-else class="si-hint">Turn OIDC on and save to try a test sign-in.</p>
          </template>
        </div>
        <div v-if="last" class="si-test" aria-label="Last test sign-in">
          <p v-if="!last.ok" class="st-note bad"><Glyph kind="down" /><span>Last test sign-in <span :title="absolute(last.at)">failed</span>: {{ last.error }}</span></p>
          <dl v-else class="si-facts">
            <dt>Last test sign-in</dt>
            <dd>{{ last.username || last.email || 'someone' }}<template v-if="last.linked">, linked to {{ last.linked }}</template></dd>
            <dt>auth_time</dt>
            <dd>
              <template v-if="last.auth_time_fresh">sent and fresh: step-up through the provider works</template>
              <template v-else-if="last.auth_time">sent, but not fresh: check the provider honours max_age=0</template>
              <template v-else>not sent: people without a passkey or password can’t confirm destructive actions</template>
            </dd>
            <dt>Groups</dt>
            <dd>{{ last.groups.length ? last.groups.join(', ') : 'none' }}</dd>
            <dt>Role from groups</dt>
            <dd>{{ last.mapped_role ? ROLE_WORDS[last.mapped_role as keyof typeof ROLE_WORDS]?.label ?? last.mapped_role : 'none' }}</dd>
          </dl>
        </div>
      </fieldset>

      <fieldset class="si-group">
        <legend>Forward-auth from a proxy</legend>
        <UiField check label="Trust the user headers a proxy in front of Cha Dash sends" v-slot="{ control }">
          <input v-bind="control" v-model="draft.forward_auth.enabled" type="checkbox" />
        </UiField>
        <div v-if="draft.forward_auth.enabled && view && !view.trusted_proxies" class="st-note attn" role="note">
          <Glyph kind="attn" />
          <span>CHADASH_TRUSTED_PROXIES lists no proxy, so these headers never count. Add your proxy’s address to it and restart Cha Dash.</span>
        </div>
        <p class="si-hint">
          Headers count only from addresses in CHADASH_TRUSTED_PROXIES; from anywhere else they’re removed. Your proxy must set or clear every one of these headers
          on every request, or a header a browser sent could pass as the proxy’s. A header never confirms it’s you for a destructive action.
        </p>
        <p v-if="groupsSeen" class="si-hint">
          Groups header last received: <span class="st-mono">{{ groupsSeen.header }}: {{ groupsSeen.value || '(empty)' }}</span>, {{ absolute(groupsSeen.at) }}.
        </p>
        <div v-if="groupsMissing" class="st-note attn" role="note">
          <Glyph kind="attn" />
          <span>Roles come from groups, but no proxy has sent a groups header since Cha Dash started. Check the groups header name and your proxy’s settings.</span>
        </div>
        <div class="si-grid">
          <UiField label="User header" v-slot="{ control }">
            <input v-bind="control" v-model="draft.forward_auth.user_header" autocomplete="off" spellcheck="false" />
          </UiField>
          <UiField label="Groups header" v-slot="{ control }">
            <input v-bind="control" v-model="draft.forward_auth.groups_header" autocomplete="off" spellcheck="false" />
          </UiField>
          <UiField label="Email header" v-slot="{ control }">
            <input v-bind="control" v-model="draft.forward_auth.email_header" autocomplete="off" spellcheck="false" />
          </UiField>
        </div>
      </fieldset>

      <fieldset class="si-group">
        <legend>Roles from groups</legend>
        <p class="si-hint">
          With a mapping here, everyone who signs in through the provider or the proxy gets the highest role their groups map to, at every sign-in. Someone in
          no mapped group is turned away. The owner never changes. With no mappings, roles are set in Settings → Users.
        </p>
        <p v-if="!draft.mappings.length" class="st-empty si-empty">No mappings: roles are managed here, in Cha Dash.</p>
        <ul v-else class="si-maps">
          <li v-for="(m, i) in draft.mappings" :key="i" class="si-map">
            <UiField :label="`Group ${i + 1}`" hide-label v-slot="{ control }">
              <input v-bind="control" v-model="m.group" placeholder="Group name" autocomplete="off" spellcheck="false" />
            </UiField>
            <UiField :label="`Role for group ${i + 1}`" hide-label v-slot="{ control }">
              <select v-bind="control" v-model="m.role">
                <option v-for="r in GROUP_ROLES" :key="r" :value="r">{{ ROLE_WORDS[r].label }}</option>
              </select>
            </UiField>
            <UiButton size="sm" variant="ghost" :aria-label="`Remove the mapping for ${m.group || `group ${i + 1}`}`" @click="removeMapping(i)">Remove</UiButton>
          </li>
        </ul>
        <div class="st-acts"><UiButton size="sm" @click="addMapping">Add a mapping</UiButton></div>
        <UiField check label="Create an account on a first sign-in, for people whose groups map to a role" v-slot="{ control }">
          <input v-bind="control" v-model="draft.provision" type="checkbox" />
        </UiField>
        <UiField check label="Hide the password form on the sign-in page behind a link" v-slot="{ control }">
          <input v-bind="control" v-model="draft.hide_password_form" type="checkbox" />
        </UiField>
      </fieldset>

      <div class="st-acts">
        <UiButton variant="primary" size="sm" type="submit" :aria-disabled="!dirty || saving ? 'true' : undefined" :aria-busy="saving || undefined">
          {{ saving ? 'Saving…' : 'Save sign-in settings' }}<span class="tier">T3</span>
        </UiButton>
      </div>
      <p v-if="saveError" class="err" role="alert">{{ saveError }}</p>
    </form>
  </section>
</template>

<style>
.si-form {
  display: grid;
  gap: var(--space-4);
}
.si-group {
  display: grid;
  gap: var(--space-3);
  margin: 0;
  padding: var(--space-3) var(--space-4) var(--space-4);
  border: var(--hairline) solid var(--line);
  border-radius: var(--radius-lg);
  min-width: 0;
}
.si-group > legend {
  padding: 0 var(--space-2);
  font-weight: 600;
}
.si-grid {
  display: grid;
  grid-template-columns: repeat(auto-fit, minmax(min(100%, 16rem), 1fr));
  gap: var(--space-3);
}
.si-hint {
  margin: 0;
  color: var(--text-2);
}
.si-redirect {
  display: flex;
  flex-wrap: wrap;
  align-items: center;
  gap: var(--space-2);
}
.si-redirect code {
  overflow-wrap: anywhere;
}
.si-k {
  color: var(--text-2);
}
.si-test {
  display: grid;
  gap: var(--space-2);
}
.si-facts {
  display: grid;
  grid-template-columns: max-content minmax(0, 1fr);
  gap: var(--space-1) var(--space-3);
  margin: 0;
}
.si-facts dt {
  color: var(--text-2);
}
.si-facts dd {
  margin: 0;
  overflow-wrap: anywhere;
}
.si-maps {
  display: grid;
  gap: var(--space-2);
  margin: 0;
  padding: 0;
  list-style: none;
}
.si-map {
  display: grid;
  grid-template-columns: minmax(0, 1fr) minmax(8rem, auto) auto;
  gap: var(--space-2);
  align-items: start;
}
.si-empty {
  margin: 0;
}
@media (max-width: 480px) {
  .si-map {
    grid-template-columns: minmax(0, 1fr) auto;
  }
  .si-map > :first-child {
    grid-column: 1 / -1;
  }
}
</style>
