<script setup lang="ts">
/**
 * First run, step 2 of 2: offer a passkey right after the owner account is
 * made. Skippable, and it says plainly when this connection can't do
 * passkeys (they need HTTPS or localhost). Loaded on demand with the
 * WebAuthn code, so the doors stay small.
 */
import { onMounted, ref } from 'vue';
import Glyph from '../components/marks/Glyph.vue';
import { registerPasskey } from '../lib/passkeys';
import { INSECURE_MESSAGE, passkeySupport, UNSUPPORTED_MESSAGE } from '../lib/webauthn';
import { useSession } from '../stores/session';

const session = useSession();
const support = passkeySupport();
const busy = ref(false);
const error = ref('');
const done = ref('');
const primary = ref<HTMLButtonElement | null>(null);

onMounted(() => primary.value?.focus());

async function add(): Promise<void> {
  if (busy.value) return;
  busy.value = true;
  error.value = '';
  try {
    const p = await registerPasskey();
    done.value = p?.name ? `Added “${p.name}”.` : 'Passkey added.';
  } catch (err) {
    error.value = (err as Error).message || 'The passkey wasn’t added.';
  } finally {
    busy.value = false;
  }
}

function next(): void {
  session.state.offerPasskey = false;
}
</script>

<template>
  <main class="door">
    <section class="door-card" aria-labelledby="pk-offer-h">
      <div class="wordmark-row">
        <span class="brand">Cha Dash</span>
        <span class="caps">First run · step 2 of 2</span>
      </div>
      <div>
        <h1 id="pk-offer-h">{{ done ? 'You’re set.' : 'Add a passkey?' }}</h1>
        <p class="lede">
          <template v-if="done">{{ done }} Next time, choose “Sign in with a passkey”. Add more for other devices in Settings → Security.</template>
          <template v-else>Sign in with Touch ID, Face ID, Windows Hello or a security key instead of typing your password. The password keeps working.</template>
        </p>
      </div>

      <div v-if="!done && support !== 'ok'" class="form-error door-note" role="note">
        <Glyph kind="lock" />
        <span>{{ support === 'insecure' ? INSECURE_MESSAGE : UNSUPPORTED_MESSAGE }} You can add one later in Settings → Security.</span>
      </div>
      <div v-if="error" class="form-error" role="alert"><Glyph kind="attn" /><span>{{ error }}</span></div>

      <template v-if="done">
        <button ref="primary" type="button" class="btn primary" @click="next">Open the front page</button>
      </template>
      <template v-else-if="support === 'ok'">
        <button ref="primary" type="button" class="btn primary" :disabled="busy" :aria-busy="busy" @click="add">{{ busy ? 'Waiting for your device…' : 'Add a passkey' }}</button>
        <button type="button" class="btn ghost" :disabled="busy" @click="next">Skip for now</button>
      </template>
      <button v-else ref="primary" type="button" class="btn primary" @click="next">Continue</button>
      <p class="door-foot">The passkey stays on your device; the hub keeps only its public half.</p>
    </section>
  </main>
</template>

<style>
.door-note {
  border-style: dashed;
  background: repeating-linear-gradient(135deg, var(--hatch) 0 1.5px, transparent 1.5px 7px);
  border-color: var(--line-2);
}
</style>
