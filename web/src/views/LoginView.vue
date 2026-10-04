<script setup lang="ts">
import { onMounted, ref } from 'vue';
import Glyph from '../components/marks/Glyph.vue';
import { authMessage, useSession } from '../stores/session';

const session = useSession();
const username = ref('');
const password = ref('');
const busy = ref(false);
const error = ref('');
const userInput = ref<HTMLInputElement | null>(null);

onMounted(() => userInput.value?.focus());

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

      <div class="field">
        <label for="login-user">Username</label>
        <input id="login-user" ref="userInput" v-model="username" autocomplete="username" autocapitalize="off" spellcheck="false" required />
      </div>
      <div class="field">
        <label for="login-pass">Password</label>
        <input id="login-pass" v-model="password" type="password" autocomplete="current-password" required />
      </div>
      <button type="submit" class="btn primary" :disabled="busy" :aria-busy="busy">{{ busy ? 'Signing in…' : 'Sign in' }}</button>
      <p class="door-foot">Forgot it? The owner can reset passwords from the hub's command line.</p>
    </form>
  </main>
</template>
