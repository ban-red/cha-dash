<script setup lang="ts">
/**
 * "Login as Local Dev": one click past setup and sign-in, for working on the
 * frontend. Shown only by the Vite dev server, against a hub built with
 * -tags devlogin (`bun run dev`). The hub signs in as the owner, creating a
 * passwordless `dev` owner on first run.
 */
import { ref } from 'vue';
import Glyph from '../components/marks/Glyph.vue';
import { api } from '../lib/api';
import { authMessage, useSession, type User } from '../stores/session';

const session = useSession();
const busy = ref(false);
const error = ref('');

async function enter() {
  if (busy.value) return;
  busy.value = true;
  error.value = '';
  try {
    const res = await api<{ user: User }>('/api/auth/dev-login', { method: 'POST' });
    session.state.user = res.user;
    session.state.phase = 'ready';
  } catch (err) {
    error.value = authMessage(err, 'dev');
  } finally {
    busy.value = false;
  }
}
</script>

<template>
  <section class="dev-login" aria-label="Local development">
    <div class="dev-rule" aria-hidden="true"><span class="caps">Dev build</span></div>
    <div v-if="error" class="form-error" role="alert"><Glyph kind="attn" /><span>{{ error }}</span></div>
    <button type="button" class="btn dev-btn" :disabled="busy" :aria-busy="busy" @click="enter">
      {{ busy ? 'Signing in…' : 'Login as Local Dev' }}
    </button>
    <p class="door-foot">Skips the token and passwords: signs in as the owner, or creates a passwordless <code>dev</code> owner on first run. Only in <code>bun run dev</code>.</p>
  </section>
</template>

<style scoped>
.dev-login {
  display: flex;
  flex-direction: column;
  gap: var(--space-3);
}
.dev-rule {
  display: flex;
  align-items: center;
  gap: var(--space-3);
}
.dev-rule::before,
.dev-rule::after {
  content: '';
  flex: 1;
  border-top: var(--hairline) dashed var(--line-2);
}
.dev-btn {
  min-height: var(--target-min);
  font-size: var(--type-body);
  border-style: dashed;
  border-color: var(--attention-line);
}
.dev-btn:hover {
  background: var(--attention-bg);
}
</style>
