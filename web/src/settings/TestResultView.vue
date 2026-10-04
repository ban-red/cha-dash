<script setup lang="ts">
/**
 * What "Test connection" found: whether Cha Dash could read the service, its
 * version and latency, the grants (what the credential can actually do, read
 * back from the source), warnings and errors. On a read-only connection any
 * write grant is flagged: the credential can do more than Cha Dash needs.
 */
import { computed } from 'vue';
import Glyph from '../components/marks/Glyph.vue';
import { isWrite, normalizeGrant, testError, type TestResult } from '../model/integrations';

const { result, readOnly = true } = defineProps<{ result: TestResult; readOnly?: boolean }>();

const grants = computed(() => (result.grants ?? []).map((g) => normalizeGrant(g)));
const writes = computed(() => grants.value.filter(isWrite));
const flagged = computed(() => (readOnly ? writes.value.length : 0));
const warnings = computed(() => result.warnings ?? []);
const err = computed(() => testError(result.error));
const sevGlyph = (s: string) => (s === 'error' || s === 'down' ? 'down' : s === 'info' ? 'ok' : 'attn');
</script>

<template>
  <section class="test-result" aria-label="Connection test" role="status">
    <div :class="['st-note', result.ok ? (flagged || warnings.length ? 'attn' : '') : 'bad']">
      <Glyph :kind="result.ok ? 'check' : 'down'" :label="result.ok ? 'connected' : 'failed'" />
      <span v-if="result.ok">
        <b>Connected.</b>
        <template v-if="result.version"> Version <span class="n">{{ result.version }}</span>.</template>
        <template v-if="typeof result.latency_ms === 'number'"> Answered in <span class="n">{{ Math.round(result.latency_ms) }}</span> ms.</template>
        <template v-if="flagged"> This credential can do more than read: {{ flagged === 1 ? 'one write grant' : `${flagged} write grants` }} below.</template>
      </span>
      <span v-else><b>Couldn’t connect.</b> {{ err || 'The hub didn’t say why.' }}</span>
    </div>

    <div v-if="grants.length" class="tr-block">
      <h4>What this credential can do</h4>
      <ul class="rel grants">
        <li v-for="g in grants" :key="g.name" :class="{ flagged: readOnly && isWrite(g) }">
          <span>
            <Glyph v-if="readOnly && isWrite(g)" kind="attn" label="more than read-only" />
            <span class="n">{{ g.name }}</span>
            <template v-if="g.detail"> · {{ g.detail }}</template>
          </span>
          <span>{{ isWrite(g) ? (readOnly ? 'write · not needed' : 'write') : 'read' }}</span>
        </li>
      </ul>
      <p v-if="flagged" class="sub">Cha Dash only reads in this version. A token limited to reading can’t be misused to change anything; consider issuing one.</p>
    </div>
    <p v-else-if="result.ok" class="sub">The service doesn’t say what this credential can do, so Cha Dash can’t check it.</p>

    <div v-if="warnings.length" class="tr-block">
      <h4>Worth knowing</h4>
      <ul class="rel">
        <li v-for="w in warnings" :key="w.code + w.message">
          <span><Glyph :kind="sevGlyph(w.severity)" /> {{ w.message }}</span>
          <span>{{ w.code }}</span>
        </li>
      </ul>
    </div>
    <p v-if="!result.ok && err && result.error && typeof result.error === 'object' && result.error.code" class="sub">Code: <span class="n">{{ result.error.code }}</span></p>
  </section>
</template>

<style>
.test-result {
  display: flex;
  flex-direction: column;
  gap: var(--space-4);
}
.tr-block h4 {
  margin: 0 0 var(--space-2);
  font: var(--weight-medium) var(--type-micro) / 1 var(--font-num);
  letter-spacing: var(--tracking-caps);
  text-transform: uppercase;
  color: var(--text-3);
}
.grants li.flagged > span:first-child {
  color: var(--text);
}
.grants li .g {
  margin-right: 4px;
}
.test-result .sub {
  margin-top: var(--space-2);
}
</style>
