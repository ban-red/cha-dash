<script setup lang="ts">
/**
 * First run: with no account on the hub yet, whoever opens it creates the
 * owner, named admin unless they change it. Validation mirrors the hub
 * (username pattern, 6+ character password) so mistakes show before the
 * request.
 */
import { computed, ref } from 'vue';
import Glyph from '../ui/marks/Glyph.vue';
import { DevLogin } from '../dev';
import { authMessage, MIN_PASSWORD, passwordLength, USERNAME_RE, useSession } from '../stores/session';
import UiButton from '../ui/Button.vue';
import UiField from '../ui/Field.vue';

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

      <UiField id="setup-user" label="Username" help="Letters, digits and . _ @ -, starting with a letter or digit." :invalid="touched && !usernameOk" v-slot="{ control }">
        <input v-bind="control" v-model="username" autocomplete="username" autocapitalize="off" spellcheck="false" required />
      </UiField>

      <UiField id="setup-pass" label="Password" :help="`${remaining ? `At least ${MIN_PASSWORD} characters · ${remaining} to go` : `At least ${MIN_PASSWORD} characters ✓`}. A passphrase works well.`" :invalid="touched && !passwordOk" v-slot="{ control }">
        <input v-bind="control" v-model="password" type="password" autocomplete="new-password" autofocus required />
      </UiField>

      <UiField id="setup-confirm" label="Password again" :error="touched && !confirmOk && 'The two passwords don\'t match.'" v-slot="{ control }">
        <input v-bind="control" v-model="confirm" type="password" autocomplete="new-password" required />
      </UiField>

      <UiButton variant="primary" type="submit" :disabled="busy" :aria-busy="busy">{{ busy ? 'Creating…' : 'Create owner account' }}</UiButton>
      <p class="door-foot">Cha Dash runs on your hardware. The account lives on this hub only; nothing is sent anywhere else.</p>
      <component :is="DevLogin" v-if="DevLogin && session.state.devLogin" />
    </form>
  </main>
</template>
