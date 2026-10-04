<script setup lang="ts">
/** Backups: the PBS datastore as the one number, a bar with its threshold, and last night's runs. */
import { computed } from 'vue';
import type { SizeId } from '@chadash/layout';
import Tile from '../components/Tile.vue';
import Meter from '../components/marks/Meter.vue';
import { fmtInt, fmtPct, hhmm } from '../lib/format';
import { attr, byKind, incoming, lastBackup, series, value } from '../model/graph';
import { glyphFor, tileStatus } from '../model/status';
import { normalizeHealth, resourceLinks } from '../model/types';
import { useLive } from '../stores/live';
import { useUi } from '../stores/ui';

const { size } = defineProps<{ size: SizeId; target?: string | undefined }>();
const live = useLive();
const ui = useUi();

const store = computed(() => byKind(live.graph.value, 'backup_target')[0]);
const id = computed(() => store.value?.id ?? '');
const used = computed(() => series(live.metrics.value, id.value, 'used.pct'));
const health = computed(() => normalizeHealth(store.value));
const jobsHealth = computed(() => byKind(live.graph.value, 'backup_job').map((j) => normalizeHealth(j)));
const failed = computed(() => value(live.metrics.value, 'app:pbs', 'tasks.failed_24h'));
const last = computed(() => lastBackup(live.graph.value));
const covered = computed(() => {
  const g = live.graph.value;
  return byKind(g, 'backup_job').reduce((n, j) => n + incoming(g, j.id, 'backed_up_by').length, 0);
});
const status = computed(() => {
  const s = tileStatus(health.value, store.value);
  if (s !== 'ok') return s;
  return jobsHealth.value.some((h) => h.status === 'attention' || h.status === 'down') ? 'attention' : 'ok';
});
const incident = computed(() =>
  live.problems.value.find((i) => i.target === id.value || byKind(live.graph.value, 'backup_job').some((j) => j.id === i.target) || i.scenario === 'backup-failed'),
);
const app = computed(() => live.graph.value?.resources['app:pbs']);
const tag = computed(() => (status.value === 'ok' ? '' : (health.value.reason ?? 'needs you')));
</script>

<template>
  <Tile
    :size="size"
    name="Backups"
    mono="PB"
    :context="store ? `PBS · ${store.name}` : 'PBS'"
    :href="resourceLinks(app)[0]?.url"
    :status="status"
    :stale="Boolean(used?.stale)"
    :as-of="used?.as_of"
    :tag="tag"
    :tag-glyph="status === 'ok' ? undefined : glyphFor(status)"
    :loading="live.loading.value"
    :clickable="Boolean(incident)"
    @open="incident && ui.openInbox(incident.id)"
  >
    <template v-if="!store">
      <div class="v sm word">No backup server</div>
      <p class="lab">Connect Proxmox Backup Server to see coverage here.</p>
    </template>
    <template v-else-if="size === 'small'">
      <div class="v">{{ fmtPct(used?.value) }}<span class="u">%</span></div>
      <Meter :value="used?.value" :warn="used?.warn ?? 90" :crit="used?.crit ?? 96" :tick="used?.warn ?? 90" label="PBS datastore used" />
      <div class="tf">
        <span v-if="failed"><span class="n">{{ failed }}</span> task failed</span>
        <span v-else-if="last">last run <span class="n">{{ hhmm(last.at) }}</span></span>
        <span v-else>no runs yet</span>
      </div>
    </template>
    <div v-else class="split">
      <div class="col">
        <div class="v">{{ fmtPct(used?.value) }}<span class="u">% of {{ attr(store, 'capacity_tb') ?? '—' }} TB</span></div>
        <Meter :value="used?.value" :warn="used?.warn ?? 90" :crit="used?.crit ?? 96" :tick="used?.warn ?? 90" label="PBS datastore used" />
      </div>
      <dl class="kv">
        <dt>Last run</dt>
        <dd>{{ last ? hhmm(last.at) : '—' }}</dd>
        <dt>Failed (24 h)</dt>
        <dd>{{ fmtInt(failed) }}</dd>
        <dt>Guests covered</dt>
        <dd>{{ covered }}</dd>
        <dt>Prune job</dt>
        <dd>{{ attr(store, 'prune_job') ?? '—' }}</dd>
      </dl>
    </div>
  </Tile>
</template>
