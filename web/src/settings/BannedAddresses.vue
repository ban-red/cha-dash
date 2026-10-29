<script setup lang="ts">
/**
 * Settings → Users, under People (admin+): client addresses the hub banned
 * after five wrong passwords in a row.
 *
 *   GET    /api/bans        → [{ip, banned_at, failures}], newest first
 *   DELETE /api/bans/{ip}   lifts one ban
 *
 * A ban stays until someone clears it here, or with `chadash bans clear` on
 * the hub's host when nobody can sign in.
 */
import { onMounted, ref } from 'vue';
import Glyph from '../components/marks/Glyph.vue';
import { ago, absolute } from '../lib/format';
import { api } from '../lib/api';
import { hubWords } from '../lib/actions';
import { focusFirst } from '../lib/focus';
import { useUi } from '../stores/ui';

export interface IpBan {
  ip: string;
  banned_at: string;
  failures: number;
}

const ui = useUi();
const now = Date.now();
const bans = ref<IpBan[]>([]);
const status = ref<'loading' | 'ready' | 'error'>('loading');
const loadError = ref('');
const busy = ref('');
const rowError = ref<Record<string, string>>({});

async function load(): Promise<void> {
  try {
    bans.value = (await api<IpBan[]>('/api/bans')) ?? [];
    status.value = 'ready';
  } catch (err) {
    status.value = 'error';
    loadError.value = hubWords(err, 'Banned addresses couldn’t be loaded.');
  }
}

async function unban(b: IpBan): Promise<void> {
  if (busy.value) return;
  busy.value = b.ip;
  rowError.value = { ...rowError.value, [b.ip]: '' };
  try {
    await api(`/api/bans/${encodeURIComponent(b.ip)}`, { method: 'DELETE' });
    bans.value = bans.value.filter((x) => x.ip !== b.ip);
    ui.toast(`${b.ip} can sign in again.`);
    // Its row is gone: back to the heading.
    void focusFirst('ban-h');
  } catch (err) {
    rowError.value = { ...rowError.value, [b.ip]: hubWords(err, 'The ban wasn’t cleared.') };
  } finally {
    busy.value = '';
  }
}

onMounted(load);
</script>

<template>
  <section class="st-section" aria-labelledby="ban-h">
    <header>
      <h3 id="ban-h" tabindex="-1">Banned addresses</h3>
    </header>
    <p class="st-lede">
      Five wrong passwords in a row from one address ban it from signing in, until you clear it here. If nobody can sign in, run
      <code>chadash bans clear &lt;address&gt;</code> on the hub’s host.
    </p>

    <div v-if="status === 'loading'" class="st-skel" aria-busy="true"><span class="skel"></span></div>
    <div v-else-if="status === 'error'" class="st-note bad" role="alert"><Glyph kind="down" /><span>{{ loadError }}</span></div>
    <div v-else-if="!bans.length" class="st-empty">
      <div class="t">No banned addresses</div>
      <div class="m">An address that gets five passwords wrong in a row shows up here.</div>
    </div>
    <ul v-else class="st-list">
      <li v-for="b in bans" :key="b.ip">
        <div class="st-row">
          <span>
            <span class="t st-mono">{{ b.ip }}</span>
            <span class="m">
              Banned <span :title="absolute(b.banned_at)">{{ ago(b.banned_at, now) }}</span> after {{ b.failures }} wrong passwords
            </span>
            <span v-if="rowError[b.ip]" class="err" role="alert">{{ rowError[b.ip] }}</span>
          </span>
          <span class="end">
            <button
              type="button"
              class="btn sm"
              :aria-label="`Clear the ban on ${b.ip}`"
              :aria-disabled="busy === b.ip ? 'true' : undefined"
              :aria-busy="busy === b.ip || undefined"
              @click="unban(b)"
            >
              Clear ban
            </button>
          </span>
        </div>
      </li>
    </ul>
  </section>
</template>
