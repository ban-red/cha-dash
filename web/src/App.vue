<script setup lang="ts">
/**
 * The doors: first-run setup (then the optional passkey offer), sign-in, or
 * the front page. The doors are small and load with the app; the front page
 * and the passkey offer are their own chunks. When the hub can't be reached
 * but this device kept a last-known snapshot, the front page opens on it
 * (session.state.offline) instead of the unreachable door.
 */
import { defineAsyncComponent, onMounted } from 'vue';
import Glyph from './components/marks/Glyph.vue';
import LoginView from './views/LoginView.vue';
import SetupView from './views/SetupView.vue';
import { takeInlineSnapshot } from './lib/inline';
import { startRouter } from './lib/router';
import { createSession, provideSession } from './stores/session';

const FrontPage = defineAsyncComponent(() => import('./views/FrontPage.vue'));
const PasskeyOffer = defineAsyncComponent(() => import('./views/PasskeyOffer.vue'));

// Signed in, the hub inlines the first board's snapshot: the front page renders from it at once.
const session = createSession({ inline: takeInlineSnapshot() });
provideSession(session);
startRouter();
onMounted(() => void session.refresh());
</script>

<template>
  <main v-if="session.state.phase === 'loading'" class="door" aria-busy="true"><span class="caps">Cha Dash</span></main>
  <SetupView v-else-if="session.state.phase === 'setup'" />
  <LoginView v-else-if="session.state.phase === 'login'" />
  <PasskeyOffer v-else-if="session.state.phase === 'ready' && session.state.offerPasskey" />
  <FrontPage v-else-if="session.state.phase === 'ready'" />
  <main v-else class="door">
    <div class="door-card" role="alert">
      <div class="wordmark-row"><span class="brand">Cha Dash</span><span class="caps">Hub unreachable</span></div>
      <h1><Glyph kind="down" label="unreachable" />{{ session.state.error }}</h1>
      <p class="lede">Check that the hub is running, then try again.</p>
      <button type="button" class="btn primary" @click="session.refresh()">Try again</button>
    </div>
  </main>
</template>
