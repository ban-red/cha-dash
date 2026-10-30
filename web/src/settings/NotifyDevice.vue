<script setup lang="ts">
/**
 * "Notify me on this device": every role, for their own devices. Turning it
 * on asks the browser for permission (only ever from this click), subscribes
 * with the hub's key and registers the device; turning it off forgets it on
 * both sides. Below, the account's other devices, each removable.
 *
 * What a device hears follows the role: viewers and up the problems the
 * Inbox shows, household only what its boards show, admins and the owner
 * also every critical action (internal/notify).
 */
import { computed, onMounted, ref } from 'vue';
import Glyph from '../ui/marks/Glyph.vue';
import { ago } from '../lib/format';
import { hubWords } from '../lib/actions';
import { currentSubscription, disablePush, enablePush, fingerprint, listDevices, permission, pushSupport, removeDevice, type PushDevice } from '../lib/push';
import { useUi } from '../stores/ui';
import UiButton from '../ui/Button.vue';

const ui = useUi();
const support = pushSupport();
const on = ref(false);
const mine = ref('');
const busy = ref(false);
const error = ref('');
const devices = ref<PushDevice[]>([]);
const status = ref<'loading' | 'ready' | 'error'>('loading');
const loadError = ref('');
const removing = ref('');
const now = ref(Date.now());
const blocked = ref(false);

const audience = computed(() => {
  switch (ui.can.value.role) {
    case 'owner':
    case 'admin':
      return 'You’ll hear about what needs you: something down, something that needs attention for more than a few minutes, an integration to reconnect, a stuck stack, and every critical action anyone runs.';
    case 'household':
      return 'You’ll hear when something on your boards is down or needs attention, by its name.';
    default:
      return 'You’ll hear about what needs someone: something down, something that needs attention for more than a few minutes, an integration to reconnect, a stuck stack.';
  }
});

async function refresh(): Promise<void> {
  try {
    devices.value = await listDevices();
    status.value = 'ready';
    now.value = Date.now();
  } catch (err) {
    status.value = 'error';
    loadError.value = hubWords(err, 'Your devices couldn’t be loaded.');
  }
}

async function findSelf(): Promise<void> {
  const sub = await currentSubscription();
  mine.value = sub ? await fingerprint(sub.endpoint).catch(() => '') : '';
  // On only when the browser has a subscription and the hub knows it.
  on.value = !!sub && devices.value.some((d) => d.fingerprint === mine.value);
  blocked.value = permission() === 'denied';
}

async function toggle(): Promise<void> {
  busy.value = true;
  error.value = '';
  try {
    if (on.value) {
      await disablePush();
      ui.toast('This device won’t get notifications any more.');
    } else {
      await enablePush();
      ui.toast('This device will get notifications.');
    }
  } catch (err) {
    error.value = err instanceof Error && !('status' in err) ? err.message : hubWords(err, 'That didn’t work. Try again.');
  } finally {
    busy.value = false;
    await refresh();
    await findSelf();
  }
}

async function remove(d: PushDevice): Promise<void> {
  removing.value = d.id;
  try {
    if (d.fingerprint === mine.value) await disablePush();
    else await removeDevice(d.id);
    await refresh();
    await findSelf();
  } catch (err) {
    error.value = hubWords(err, `${d.label || 'That device'} wasn’t removed.`);
  } finally {
    removing.value = '';
  }
}

onMounted(async () => {
  await refresh();
  await findSelf();
});
</script>

<template>
  <section class="st-section" aria-labelledby="nd-h">
    <header><h3 id="nd-h">This device</h3></header>
    <p class="st-lede">{{ audience }} Hints and problems that clear by themselves never notify.</p>

    <div v-if="!support.ok" class="st-note hatched"><Glyph kind="off" /><span>{{ support.reason }}</span></div>
    <template v-else>
      <div class="nd-toggle">
        <UiButton class="nd-switch" :aria-pressed="on" :disabled="busy" :aria-busy="busy || undefined" @click="toggle">
          <span class="nd-track" aria-hidden="true"><span class="nd-knob"></span></span>
          Notify me on this device
        </UiButton>
        <span class="st-meta" role="status">{{ busy ? 'Working…' : on ? 'On' : 'Off' }}</span>
      </div>
      <div v-if="blocked && !on" class="st-note attn">
        <Glyph kind="attn" /><span>Notifications are blocked for Cha Dash in this browser. Allow them in the site’s settings (the icon beside the address), then turn this on.</span>
      </div>
    </template>
    <p v-if="error" class="err" role="alert">{{ error }}</p>

    <div v-if="status === 'error'" class="st-note bad" role="alert">
      <Glyph kind="down" /><span>{{ loadError }}</span>
      <UiButton size="sm" @click="refresh">Try again</UiButton>
    </div>
    <ul v-else-if="devices.length" class="st-list" aria-label="Your devices">
      <li v-for="d in devices" :key="d.id">
        <div class="st-row">
          <Glyph :kind="d.last_error ? 'attn' : 'ok'" />
          <span>
            <span class="t">{{ d.label || 'A browser' }}<span v-if="d.fingerprint === mine" class="set-chip nd-this">this device</span></span>
            <span class="m">
              via {{ d.host }} · added {{ ago(d.created_at, now) }}<template v-if="d.last_ok_at"> · last delivered {{ ago(d.last_ok_at, now) }}</template>
            </span>
            <span v-if="d.last_error" class="m">{{ d.last_error }}</span>
          </span>
          <span class="end">
            <UiButton variant="ghost" size="sm" :disabled="removing === d.id" :aria-label="`Remove ${d.label || 'this browser'}`" @click="remove(d)">Remove</UiButton>
          </span>
        </div>
      </li>
    </ul>
  </section>
</template>

<style>
.nd-toggle {
  display: flex;
  flex-wrap: wrap;
  align-items: center;
  gap: var(--space-3);
}
.nd-switch {
  display: inline-flex;
  align-items: center;
  gap: 10px;
  min-height: var(--target-min);
}
.nd-track {
  position: relative;
  display: inline-block;
  width: 30px;
  height: 18px;
  flex: none;
  border-radius: var(--radius-full);
  border: var(--hairline) solid var(--line-2);
  background: color-mix(in oklch, var(--text) 10%, transparent);
  transition: background-color var(--motion-press-duration) var(--motion-press-easing);
}
.nd-knob {
  position: absolute;
  top: 2px;
  left: 2px;
  width: 12px;
  height: 12px;
  border-radius: 50%;
  background: var(--text-2);
  transition: transform var(--motion-press-duration) var(--motion-press-easing);
}
.nd-switch[aria-pressed='true'] .nd-track {
  background: var(--text);
}
.nd-switch[aria-pressed='true'] .nd-knob {
  transform: translateX(12px);
  background: var(--bg);
}
.nd-this {
  margin-left: var(--space-2);
  vertical-align: 1px;
}
</style>
