<script setup lang="ts">
/**
 * "Sign in with …" beside a secret the integration's sign-in flow can fill
 * in (`x-chadash-authorize` in its schema; Plex's PIN). The hub starts the
 * flow (POST /api/integrations/authorize/{integration}), a new tab opens its
 * link, and the button polls the hub until the user has approved Cha Dash
 * there. What comes back goes into this form's secrets only, exactly as if
 * it had been typed: nothing is saved until the user saves.
 */
import { computed, onBeforeUnmount, ref } from 'vue';
import Glyph from '../components/marks/Glyph.vue';
import { ApiError } from '../lib/api';
import { safeUrl } from '../model/types';
import { beginAuthorize, errorWords, pollAuthorize } from '../stores/integrations';
import { useForm } from './form';

const { label } = defineProps<{ label: string }>();
const form = useForm();
const who = computed(() => form.integration?.value?.name ?? 'the service');
const what = computed(() => (label.length > 1 && label[1] === label[1]!.toLowerCase() ? label[0]!.toLowerCase() + label.slice(1) : label));

const state = ref<'idle' | 'starting' | 'waiting' | 'done' | 'error'>('idle');
const link = ref('');
const message = ref('');
let timer: ReturnType<typeof setTimeout> | undefined;
let run = 0; // bumps on start, cancel and unmount, so a late answer is dropped

function stop(): void {
  run++;
  if (timer !== undefined) clearTimeout(timer);
  timer = undefined;
}

function fail(text: string): void {
  state.value = 'error';
  message.value = text;
}

async function start(): Promise<void> {
  const integration = form.integration?.value?.id;
  if (!integration) return;
  stop();
  const mine = run;
  // Open the tab inside the click, so popup blockers allow it, and point it
  // at the link once the hub has one. Cut its opener first: the page it
  // ends up on is someone else's.
  const tab = window.open('', '_blank');
  if (tab) tab.opener = null;
  state.value = 'starting';
  message.value = '';
  link.value = '';
  try {
    const a = await beginAuthorize(integration);
    if (mine !== run) {
      tab?.close();
      return;
    }
    link.value = safeUrl(a.url) ?? '';
    if (tab && link.value) tab.location.href = link.value;
    else tab?.close(); // not an http(s) address: nothing to open
    state.value = 'waiting';
    schedule(integration, a.id, a.interval_s, mine);
  } catch (err) {
    tab?.close();
    if (mine === run) fail(errorWords(err, `Couldn’t start signing in with ${who.value}. Try again, or paste the ${what.value}.`));
  }
}

function schedule(integration: string, id: string, intervalS: number, mine: number): void {
  timer = setTimeout(() => void poll(integration, id, intervalS, mine), Math.max(1, intervalS || 2) * 1000);
}

async function poll(integration: string, id: string, intervalS: number, mine: number): Promise<void> {
  try {
    const s = await pollAuthorize(integration, id);
    if (mine !== run) return;
    if (s.status === 'pending') {
      schedule(integration, id, intervalS, mine);
      return;
    }
    if (s.status === 'done') {
      for (const [field, value] of Object.entries(s.secrets ?? {})) {
        form.secrets[field] = value;
        form.touched.add(field);
      }
      state.value = 'done';
      message.value = `Signed in with ${who.value}. The ${what.value} is filled in: test the connection, then save.`;
      return;
    }
    if (s.status === 'expired') fail(`The sign-in expired before ${who.value} approved it. Try again.`);
    else if (s.error?.message) fail(`${who.value} didn’t sign in: ${s.error.message.replace(/[.!?]$/, '')}.`);
    else fail(`The sign-in didn’t finish. Try again, or paste the ${what.value}.`);
  } catch (err) {
    if (mine !== run) return;
    if (err instanceof ApiError && err.status === 0) {
      schedule(integration, id, intervalS, mine); // the hub blinked: keep waiting
      return;
    }
    if (err instanceof ApiError && err.status === 404) fail('The hub lost track of this sign-in (did it restart?). Try again.');
    else fail(errorWords(err, `The sign-in didn’t finish. Try again, or paste the ${what.value}.`));
  }
}

function cancel(): void {
  stop();
  state.value = 'idle';
  link.value = '';
}

onBeforeUnmount(stop);
</script>

<template>
  <div class="auth-row" aria-live="polite">
    <template v-if="state === 'starting' || state === 'waiting'">
      <span class="auth-wait"><Glyph kind="prog" />Waiting for {{ who }}: approve Cha Dash in the tab that opened.</span>
      <a v-if="link" class="btn sm ghost" :href="link" target="_blank" rel="noopener noreferrer">Open {{ who }} again <Glyph kind="link" /></a>
      <button type="button" class="btn sm ghost" @click="cancel">Cancel</button>
    </template>
    <button v-else type="button" class="btn sm" :disabled="form.disabled.value || !form.integration?.value" @click="start">
      Sign in with {{ who }}
    </button>
    <span v-if="state === 'done'" class="help auth-done"><Glyph kind="check" /> {{ message }}</span>
    <span v-else-if="state === 'error'" class="help bad" role="alert">{{ message }}</span>
  </div>
</template>

<style>
.auth-row {
  display: flex;
  flex-wrap: wrap;
  align-items: center;
  gap: var(--space-2);
}
.auth-row .help {
  flex-basis: 100%;
}
.auth-wait {
  display: inline-flex;
  align-items: center;
  gap: var(--space-2);
  font-size: var(--type-small);
  color: var(--text-2);
}
.auth-done {
  color: var(--text-2);
}
</style>
