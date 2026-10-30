<script setup lang="ts">
/**
 * Settings → Nodes → Join tokens: the tokens made for nodes on other hosts,
 * unspent ones and those used, expired or revoked in the last 7 days (the
 * hub deletes older ones). Never a secret: the hub shows that once, when
 * the token is made. An unspent token can be revoked.
 *
 *   DELETE /api/nodes/tokens/{id}   → the token, revoked; 409 used, expired or revoked
 *
 * Emits `changed` with a sentence for the toast after a revoke.
 */
import { ref, useId } from 'vue';
import { api } from '../lib/api';
import { hubWords } from '../lib/actions';
import { focusFirst } from '../lib/focus';
import { absolute, ago, duration } from '../lib/format';
import { useLive } from '../stores/live';
import UiButton from '../ui/Button.vue';
import Glyph from '../ui/marks/Glyph.vue';
import type { GlyphKind } from '../ui/marks/Glyph.vue';
import type { JoinToken, TokenState } from './nodes';

const { tokens } = defineProps<{ tokens: JoinToken[] }>();
const emit = defineEmits<{ changed: [message: string] }>();
const live = useLive();
const uid = useId();

const GLYPH: Record<TokenState, GlyphKind> = { active: 'prog', used: 'check', expired: 'off', revoked: 'cross' };
const label = (t: JoinToken) => t.name || 'any name';

function stateWords(t: JoinToken): string {
  const now = live.now.value;
  switch (t.state) {
    case 'active':
      return `unused · expires in ${duration(Math.max(0, Date.parse(t.expires_at) - now))}`;
    case 'used':
      return `used by ${t.node_name ?? 'a node since removed'} ${ago(t.used_at, now)}`;
    case 'revoked':
      return `revoked ${ago(t.revoked_at, now)}`;
    default:
      return `expired unused ${ago(t.expires_at, now)}`;
  }
}

const revoking = ref('');
const busy = ref('');
const rowError = ref<Record<string, string>>({});
function ask(t: JoinToken): void {
  revoking.value = t.id;
  void focusFirst(`${uid}-c-${t.id}`);
}
function cancel(t: JoinToken): void {
  revoking.value = '';
  void focusFirst(`${uid}-r-${t.id}`);
}
async function revoke(t: JoinToken): Promise<void> {
  if (busy.value) return;
  busy.value = t.id;
  rowError.value = { ...rowError.value, [t.id]: '' };
  try {
    await api(`/api/nodes/tokens/${encodeURIComponent(t.id)}`, { method: 'DELETE' });
    revoking.value = '';
    emit('changed', `Revoked the join token for ${label(t)}. It won’t enroll a node.`);
    void focusFirst('nd-t');
  } catch (err) {
    rowError.value = { ...rowError.value, [t.id]: hubWords(err, 'The join token wasn’t revoked.') };
  } finally {
    busy.value = '';
  }
}
</script>

<template>
  <section class="st-section" aria-labelledby="nd-t">
    <header>
      <h3 id="nd-t" tabindex="-1">Join tokens</h3>
      <span class="st-meta">{{ tokens.filter((t) => t.state === 'active').length }} unused</span>
    </header>
    <p v-if="!tokens.length" class="st-lede">No join tokens in the last 7 days. Add node makes one for a host.</p>
    <ul v-else class="st-list nd-tokens" aria-label="Join tokens">
      <li v-for="t in tokens" :key="t.id">
        <div :class="['st-row', 'nd-row', { off: t.state !== 'active' }]">
          <Glyph :kind="GLYPH[t.state] ?? 'off'" :label="t.state" />
          <span>
            <span class="t">For {{ label(t) }}</span>
            <span class="m">
              {{ stateWords(t) }} · made by {{ t.created_by_name || 'a removed account' }}
              <span :title="absolute(t.created_at)">{{ ago(t.created_at, live.now.value) }}</span>
              <template v-if="t.add_docker"> · adds Docker</template>
            </span>
            <span v-if="rowError[t.id]" class="err" role="alert">{{ rowError[t.id] }}</span>
          </span>
          <span v-if="t.state === 'active' && revoking !== t.id" class="end">
            <UiButton variant="ghost" size="sm" :id="`${uid}-r-${t.id}`" :aria-label="`Revoke the join token for ${label(t)}`" @click="ask(t)">Revoke</UiButton>
          </span>
        </div>
        <div v-if="revoking === t.id" class="st-confirm nd-confirm" role="group" :aria-labelledby="`${uid}-q-${t.id}`">
          <span :id="`${uid}-q-${t.id}`">Revoke the join token for <b>{{ label(t) }}</b>? A node that presents it is refused.</span>
          <UiButton variant="danger-solid" size="sm" :aria-disabled="busy === t.id || undefined" :aria-busy="busy === t.id || undefined" @click="revoke(t)">Revoke token</UiButton>
          <UiButton size="sm" :id="`${uid}-c-${t.id}`" @click="cancel(t)">Cancel</UiButton>
        </div>
      </li>
    </ul>
  </section>
</template>

<style>
.nd-tokens .st-row.off .t,
.nd-tokens .st-row.off .m {
  color: var(--text-3);
}
</style>
