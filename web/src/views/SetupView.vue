<script setup lang="ts">
/**
 * First run: with no account on the hub yet, whoever opens it creates the
 * owner, named admin unless they change it. Validation mirrors the hub
 * (username pattern, 6+ character password) so mistakes show before the
 * request.
 */
import { computed, ref } from 'vue';
import Glyph from '../components/marks/Glyph.vue';
import { DevLogin } from '../dev';
import { authMessage, MIN_PASSWORD, passwordLength, USERNAME_RE, useSession } from '../stores/session';

const session = useSession();
const username = ref('admin');
const password = ref('');
const confirm = ref('');
const busy = ref(false);
const error = ref('');
const touched = ref(false);

const usernameOk = computed(() => USERNAME_RE.test(username.value));
const passwordOk = computed(() => passwordLength(password.value) >= MIN_PASSWORD);
const confirmOk = computed(() => confirm.value === password.value);
const valid = computed(() => usernameOk.value && passwordOk.value && confirmOk.value);
const remaining = computed(() => Math.max(0, MIN_PASSWORD - passwordLength(password.value)));

async function submit() {
  touched.value = true;
  error.value = '';
  if (!valid.value || busy.value) return;
  busy.value = true;
  try {
    await session.setup(username.value, password.value);
  } catch (err) {
    error.value = authMessage(err, 'setup');
  } finally {
    busy.value = false;
  }
}
</script>

<template>
  <main class="door">
    <form class="door-card" novalidate aria-labelledby="setup-h" @submit.prevent="submit">
      <div class="wordmark-row">
        <span class="brand">Cha Dash</span>
        <span class="caps">First run · step 1 of 2</span>
      </div>
      <div>
        <h1 id="setup-h">Make this hub yours.</h1>
        <p class="lede">Create the owner account. Pick a password; the username starts as admin.</p>
      </div>

      <div v-if="error" class="form-error" role="alert"><Glyph kind="attn" /><span>{{ error }}</span></div>

      <div class="field">
        <label for="setup-user">Username</label>
        <input
          id="setup-user"
          v-model="username"
          autocomplete="username"
          autocapitalize="off"
          spellcheck="false"
          :aria-invalid="touched && !usernameOk"
          aria-describedby="setup-user-help"
          required
        />
        <span id="setup-user-help" :class="['help', { bad: touched && !usernameOk }]">Letters, digits and . _ @ -, starting with a letter or digit.</span>
      </div>

      <div class="field">
        <label for="setup-pass">Password</label>
        <input
          id="setup-pass"
          v-model="password"
          type="password"
          autocomplete="new-password"
          autofocus
          :aria-invalid="touched && !passwordOk"
          aria-describedby="setup-pass-help"
          required
        />
        <span id="setup-pass-help" :class="['help', { bad: touched && !passwordOk }]">
          {{ remaining ? `At least ${MIN_PASSWORD} characters · ${remaining} to go` : `At least ${MIN_PASSWORD} characters ✓` }}. A passphrase works well.
        </span>
      </div>

      <div class="field">
        <label for="setup-confirm">Password again</label>
        <input
          id="setup-confirm"
          v-model="confirm"
          type="password"
          autocomplete="new-password"
          :aria-invalid="touched && !confirmOk"
          aria-describedby="setup-confirm-help"
          required
        />
        <span v-if="touched && !confirmOk" id="setup-confirm-help" class="help bad">The two passwords don't match.</span>
      </div>

      <button type="submit" class="btn primary" :disabled="busy" :aria-busy="busy">{{ busy ? 'Creating…' : 'Create owner account' }}</button>
      <p class="door-foot">Cha Dash runs on your hardware. The account lives on this hub only; nothing is sent anywhere else.</p>
      <component :is="DevLogin" v-if="DevLogin && session.state.devLogin" />
    </form>
  </main>
</template>
