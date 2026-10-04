<script setup lang="ts">
/**
 * One incident in the Inbox: symptom first, then (expanded) the facts, the
 * likely causes, the blast radius and the suggested action with its tier.
 * Fix runs through the safety ladder; progress and the resolution arrive
 * live from the `inbox` topic.
 */
import { computed } from 'vue';
import Glyph from './marks/Glyph.vue';
import { hhmm, since } from '../lib/format';
import { label } from '../model/graph';
import { glyphFor } from '../model/status';
import { factLabel, factValue, impactName, relationWords } from '../model/words';
import type { Incident } from '../model/types';
import { useLive } from '../stores/live';
import { useUi } from '../stores/ui';

const { incident, expanded = false } = defineProps<{ incident: Incident; expanded?: boolean }>();
const emit = defineEmits<{ toggle: [] }>();
const live = useLive();
const ui = useUi();

const isResolved = computed(() => incident.status === 'resolved');
const fix = computed(() => live.fixes[incident.id]);
const working = computed(() => !isResolved.value && fix.value?.state === 'accepted');
const glyph = computed(() => (isResolved.value ? 'check' : incident.class === 'repair' ? 'repair' : glyphFor(incident.severity)));
const glyphLabel = computed(() => (isResolved.value ? 'resolved' : incident.class === 'repair' ? 'repair' : incident.severity === 'down' ? 'down' : 'needs attention'));
const facts = computed(() => Object.entries(incident.facts ?? {}).slice(0, 6));
const blast = computed(() => (incident.blast_radius ?? []).slice(0, 8));
const action = computed(() => incident.suggested);
const disabled = computed(
  () => live.stale.value || fix.value?.state === 'sending' || !action.value || action.value.tier === 'T3' || action.value.tier === 'T4',
);
const whenText = computed(() => (isResolved.value ? hhmm(incident.resolved_at ?? incident.updated_at) : hhmm(incident.opened_at)));
const id = computed(() => `ib-${incident.id}`);
</script>

<template>
  <div :class="['ib', { open: expanded }]">
    <Glyph :kind="glyph" :label="glyphLabel" />
    <div>
      <button type="button" class="ib-title" :aria-expanded="expanded" :aria-controls="id" @click="emit('toggle')">{{ incident.title }}</button>
      <div class="m">{{ isResolved && incident.resolution ? incident.resolution : incident.summary }}</div>
    </div>
    <span class="when" :title="isResolved ? 'resolved' : 'opened'">{{ whenText }}</span>

    <div v-if="expanded && !isResolved" :id="id" class="flow">
      <div v-if="working" class="working" role="status">
        <svg class="prog" viewBox="0 0 12 12" aria-hidden="true">
          <circle class="track" cx="6" cy="6" r="5" />
          <circle class="arc" cx="6" cy="6" r="5" stroke-dasharray="31.4" :stroke-dashoffset="incident.phase === fix?.phase ? 23 : 12" />
        </svg>
        <span>{{ action?.label }}: working · {{ incident.phase }}. This updates by itself.</span>
      </div>
      <div v-else-if="action" class="col">
        <h4>Suggested · {{ action.tier }}</h4>
        <p v-if="action.detail" class="action-detail">{{ action.detail }}</p>
        <div class="acts">
          <button type="button" class="btn primary" :disabled="disabled" @click="ui.act(incident)">
            {{ action.label }}<span class="tier">{{ action.tier }}</span>
          </button>
          <button
            v-for="alt in incident.alternatives ?? []"
            :key="alt.id + alt.label"
            type="button"
            class="btn"
            disabled
            :title="'Only the suggested action runs in this version'"
          >
            {{ alt.label }}<span class="tier">{{ alt.tier }}</span>
          </button>
        </div>
        <p v-if="fix?.state === 'failed'" class="err" role="alert">{{ fix.error }}</p>
      </div>

      <div v-if="facts.length">
        <h4>What Cha Dash saw</h4>
        <dl class="kv">
          <template v-for="[k, v] in facts" :key="k">
            <dt>{{ factLabel(k) }}</dt>
            <dd>{{ factValue(v, hhmm) }}</dd>
          </template>
        </dl>
      </div>

      <div v-if="incident.causes?.length">
        <h4>Likely causes</h4>
        <ul class="rel causes">
          <li v-for="(c, i) in incident.causes" :key="i">
            <span>{{ c.text }}</span>
            <span v-if="c.resource || c.at">{{ c.resource ? label(live.graph.value?.resources[c.resource]) || c.resource : '' }}{{ c.at ? ` · ${since(c.at, live.now.value)}` : '' }}</span>
          </li>
        </ul>
      </div>

      <div v-if="blast.length">
        <h4>Blast radius · {{ incident.blast_radius?.length }}</h4>
        <ul class="rel">
          <li v-for="b in blast" :key="b.resource">
            <span>{{ impactName(live.graph.value, b.resource) }}</span>
            <span>{{ relationWords(b.via) }}</span>
          </li>
        </ul>
      </div>
      <p v-if="incident.resolves_when" class="sub">{{ incident.resolves_when }}</p>
    </div>

    <div v-else-if="expanded && isResolved" :id="id" class="flow">
      <div class="done-row" role="status">
        <svg viewBox="0 0 16 16" aria-hidden="true"><path d="m3 8.5 3 3 7-7" /></svg>
        <span>{{ incident.resolution ?? 'Resolved.' }}{{ incident.resolved_by === 'auto' ? ' It cleared by itself.' : '' }}</span>
      </div>
    </div>
  </div>
</template>
