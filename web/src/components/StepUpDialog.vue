<script setup lang="ts">
/**
 * Step-up: confirm it's you before a T3+ action or a change to the hub's
 * security. It asks the hub how (`POST /api/auth/step-up/begin`): a passkey
 * (the browser's prompt), or the password when the account has no passkey
 * and the hub allows the fallback, with a nudge to add one. Under "Passkey
 * only" with no passkey it explains and links to Settings → Security.
 *
 * A UiDialog: focus moves in and stays, Esc cancels it and nothing behind
 * it (Settings and the Inbox stay open). Its own chunk with the WebAuthn code, loaded the first time something needs it.
 */
import { computed, nextTick, onMounted, ref, useId } from 'vue';
import Glyph from '../ui/marks/Glyph.vue';
import '../styles/act-dialog.css';
import { beginStepUp, stepUpWithPasskey, stepUpWithPassword } from '../lib/stepup';
import { INSECURE_MESSAGE, passkeySupport, PasskeyError } from '../lib/webauthn';
import { useUi } from '../stores/ui';
import UiButton from '../ui/Button.vue';
import UiDialog from '../ui/Dialog.vue';
import UiField from '../ui/Field.vue';

const ui = useUi();
const uid = useId();

type Phase = 'loading' | 'passkey' | 'password' | 'none' | 'error';
const phase = ref<Phase>('loading');
const options = ref<unknown>(undefined);
const message = ref('');
const addPasskey = ref(false);
const error = ref('');
const busy = ref(false);
const password = ref('');
const pwInput = ref<HTMLInputElement | null>(null);
const firstButton = ref<InstanceType<typeof UiButton> | null>(null);
const support = passkeySupport();

const reason = computed(() => ui.state.stepUp?.reason ?? '');
const fresh = computed(() => ui.state.stepUp?.fresh === true);
const canSettings = computed(() => ui.can.value.settings);

async function begin(): Promise<void> {
  phase.value = 'loading';
  error.value = '';
  try {
    const b = await beginStepUp();
    if (b.method === 'password') phase.value = 'password';
    else if (b.method === 'none') {
      message.value = b.message;
      addPasskey.value = b.addPasskey;
      phase.value = 'none';
    } else {
      options.value = b.options;
      phase.value = 'passkey';
    }
  } catch (err) {
    message.value = (err as Error).message || 'The hub couldn’t start that. Try again.';
    phase.value = 'error';
  }
  await nextTick();
  if (phase.value === 'password') pwInput.value?.focus();
  else firstButton.value?.focus();
}

async function usePasskey(): Promise<void> {
  busy.value = true;
  error.value = '';
  try {
    await stepUpWithPasskey(options.value);
    ui.stepUpDone(true);
  } catch (err) {
    if (err instanceof PasskeyError && err.kind === 'cancelled') error.value = '';
    else error.value = (err as Error).message || 'That didn’t work. Try again.';
    // A used or lapsed challenge can't be tried again: ask the hub for a new one.
    if (!(err instanceof PasskeyError)) void begin();
  } finally {
    busy.value = false;
  }
}

async function usePassword(): Promise<void> {
  if (!password.value) {
    error.value = 'Enter your password.';
    return;
  }
  busy.value = true;
  error.value = '';
  try {
    await stepUpWithPassword(password.value);
    password.value = '';
    ui.stepUpDone(true);
  } catch (err) {
    error.value = (err as Error).message || 'That didn’t work. Try again.';
    await nextTick();
    pwInput.value?.select();
  } finally {
    busy.value = false;
  }
}

function cancel(): void {
  password.value = '';
  ui.stepUpDone(false);
}

function openSecurity(): void {
  cancel();
  ui.cancelConfirm();
  ui.openSettings('security');
}

onMounted(begin);
</script>

<template>
  <UiDialog as="form" class="stepup" title="Confirm it’s you" close-label="Cancel" :aria-describedby="`${uid}-d`" @close="cancel" @submit="phase === 'password' ? usePassword() : usePasskey()" @keydown.esc.stop>
    <template #lead><Glyph kind="lock" /></template>
    <p :id="`${uid}-d`" class="act-lede">
      <template v-if="reason">Before “{{ reason }}”, </template>
      <template v-else>First, </template>Cha Dash checks it’s really you.
      {{ fresh ? 'This one is critical, so it asks every time.' : 'It won’t ask again for 5 minutes.' }}
    </p>

    <p v-if="phase === 'loading'" class="act-hint" aria-busy="true">Asking the hub how…</p>

    <template v-else-if="phase === 'passkey'">
      <p v-if="support === 'insecure'" class="act-note" role="note"><Glyph kind="lock" /><span>{{ INSECURE_MESSAGE }}</span></p>
      <p v-if="error" class="err" role="alert">{{ error }}</p>
    </template>

    <template v-else-if="phase === 'password'">
      <UiField label="Your password" :error="error" alert v-slot="{ control }">
        <input v-bind="control" ref="pwInput" v-model="password" type="password" autocomplete="current-password" />
      </UiField>
      <p class="act-hint">Your account has no passkey yet, so the password does. <template v-if="canSettings">Add a passkey in Settings → Security and this takes a touch instead.</template></p>
    </template>

    <p v-else-if="phase === 'none'" class="act-note" role="alert"><Glyph kind="lock" /><span>{{ message }}</span></p>

    <p v-else class="err" role="alert">{{ message }}</p>

    <template v-if="phase !== 'loading'" #foot>
      <template v-if="phase === 'passkey'">
        <UiButton variant="ghost" @click="cancel">Cancel</UiButton>
        <UiButton ref="firstButton" variant="primary" type="submit" :disabled="busy || support === 'insecure'" :aria-busy="busy || undefined">
          {{ busy ? 'Waiting for you…' : 'Use your passkey' }}
        </UiButton>
      </template>
      <template v-else-if="phase === 'password'">
        <UiButton variant="ghost" @click="cancel">Cancel</UiButton>
        <UiButton variant="primary" type="submit" :disabled="busy" :aria-busy="busy || undefined">{{ busy ? 'Checking…' : 'Confirm' }}</UiButton>
      </template>
      <template v-else-if="phase === 'none'">
        <UiButton ref="firstButton" variant="ghost" @click="cancel">Close</UiButton>
        <UiButton v-if="addPasskey && canSettings" variant="primary" @click="openSecurity">Open Settings → Security</UiButton>
      </template>
      <template v-else>
        <UiButton variant="ghost" @click="cancel">Cancel</UiButton>
        <UiButton ref="firstButton" variant="primary" @click="begin">Try again</UiButton>
      </template>
    </template>
  </UiDialog>
</template>

<style>
.stepup .dlg-head .g {
  width: 14px;
  height: 14px;
}
</style>
