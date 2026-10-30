<script setup lang="ts">
/**
 * Sign in: username and password, or a passkey (usernameless). Where the
 * browser supports it, passkeys are also offered in the username field's
 * autofill (conditional mediation). The WebAuthn code loads on demand;
 * whether passkeys can work here at all (they need HTTPS or localhost) is
 * checked inline, and said in words when they can't.
 */
import { onBeforeUnmount, onMounted, ref } from 'vue';
import Glyph from '../ui/marks/Glyph.vue';
import { DevLogin } from '../dev';
import { authMessage, useSession } from '../stores/session';
import UiButton from '../ui/Button.vue';
import UiField from '../ui/Field.vue';

const session = useSession();
const username = ref('');
const password = ref('');
const busy = ref(false);
const error = ref('');
const userInput = ref<HTMLInputElement | null>(null);
const pkBusy = ref(false);
const pkSupport: 'ok' | 'insecure' | 'unsupported' =
  typeof window === 'undefined' || window.isSecureContext === false
    ? 'insecure'
    : typeof window.PublicKeyCredential === 'undefined' || !navigator.credentials
      ? 'unsupported'
      : 'ok';

/** The autofill request, waiting in the background until a passkey is picked. */
let conditional: AbortController | undefined;

async function autofill(retried = false): Promise<void> {
  if (pkSupport !== 'ok') return;
  try {
    const { conditionalMediationAvailable } = await import('../lib/webauthn');
    if (!(await conditionalMediationAvailable())) return;
    const { signInWithPasskey } = await import('../lib/passkeys');
    conditional = new AbortController();
    const res = await signInWithPasskey({ mediation: 'conditional', signal: conditional.signal });
    await session.signedIn(res.user);
  } catch (err) {
    const kind = (err as { kind?: string }).kind;
    if (kind === 'cancelled' || conditional?.signal.aborted) return;
    // The ceremony lapses after 5 minutes on an idle page: start a fresh one, once.
    if (kind === 'expired' && !retried) return autofill(true);
    if (kind !== 'unavailable') error.value = (err as Error).message;
  }
}

async function passkey() {
  if (pkBusy.value || busy.value) return;
  error.value = '';
  pkBusy.value = true;
  // One WebAuthn request at a time: the button replaces the autofill one.
  conditional?.abort();
  try {
    const { signInWithPasskey } = await import('../lib/passkeys');
    const res = await signInWithPasskey();
    await session.signedIn(res.user);
  } catch (err) {
    const kind = (err as { kind?: string }).kind;
    if (kind !== 'cancelled') error.value = (err as Error).message || 'That passkey didn’t work. Try again, or use your password.';
  } finally {
    pkBusy.value = false;
  }
}

onMounted(() => {
  userInput.value?.focus();
  void autofill();
});
onBeforeUnmount(() => conditional?.abort());

async function submit() {
  error.value = '';
  if (!username.value || !password.value) {
    error.value = 'Enter your username and password.';
    return;
  }
  if (busy.value) return;
  busy.value = true;
  try {
    await session.login(username.value, password.value);
  } catch (err) {
    error.value = authMessage(err, 'login');
    password.value = '';
  } finally {
    busy.value = false;
  }
}
</script>

<template>
  <main class="door">
    <form class="door-card" novalidate aria-labelledby="login-h" @submit.prevent="submit">
      <div class="wordmark-row">
        <span class="brand">Cha Dash</span>
        <span class="caps">Sign in</span>
      </div>
      <h1 id="login-h">Welcome home.</h1>

      <div v-if="error" class="form-error" role="alert"><Glyph kind="attn" /><span>{{ error }}</span></div>

      <UiField id="login-user" label="Username" v-slot="{ control }">
        <input v-bind="control" ref="userInput" v-model="username" autocomplete="username webauthn" autocapitalize="off" spellcheck="false" required />
      </UiField>
      <UiField id="login-pass" label="Password" v-slot="{ control }">
        <input v-bind="control" v-model="password" type="password" autocomplete="current-password" required />
      </UiField>
      <UiButton variant="primary" type="submit" :disabled="busy || pkBusy" :aria-busy="busy">{{ busy ? 'Signing in…' : 'Sign in' }}</UiButton>
      <div v-if="pkSupport !== 'unsupported'" class="door-or" aria-hidden="true"><span class="caps">or</span></div>
      <UiButton v-if="pkSupport === 'ok'" class="pk-btn" :disabled="busy || pkBusy" :aria-busy="pkBusy" @click="passkey">
        <svg viewBox="0 0 16 16" aria-hidden="true"><circle cx="6" cy="5.5" r="2.6" /><path d="M1.8 13.5c.5-2.4 2.2-3.7 4.2-3.7 1 0 1.9.3 2.6.9M11.5 8.2a1.9 1.9 0 1 1 0 3.8 1.9 1.9 0 0 1 0-3.8Zm0 3.8v2.6m0-1.3h1.2" /></svg>
        {{ pkBusy ? 'Waiting for your passkey…' : 'Sign in with a passkey' }}
      </UiButton>
      <p v-else-if="pkSupport === 'insecure'" class="door-foot pk-off" role="note">
        Passkeys need a secure connection: open Cha Dash at its https:// address, or on localhost, to sign in with one.
      </p>
      <p class="door-foot">Forgot it? The owner can reset passwords from the hub's command line.</p>
      <component :is="DevLogin" v-if="DevLogin && session.state.devLogin" />
    </form>
  </main>
</template>
