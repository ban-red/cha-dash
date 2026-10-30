<script setup lang="ts">
/**
 * Backups (PBS): the datastore as the one number, a bar with its threshold,
 * and how the nightly runs went. Target: a datastore
 * (`backups@pbs:<instance id>/datastore/main`), else the first one.
 *
 * Strip: "Backups · 92% · 1 failed". Small: datastore %, the bar, the last
 * result. Wide: plus "full in ~3 weeks" and last night per job. Large: plus
 * the failed runs with their times, guests covered and the prune policy.
 *
 * Reads the datastore's `used.pct`, `capacity_tb`, `full_in_days`,
 * `prune_job` and `retention`; the app's `tasks.failed_24h`; and the PVE
 * backup jobs' `last_run.*` (PBS's own per-guest snapshots aren't polled).
 */
import { computed } from 'vue';
import type { SizeId } from '@chadash/layout';
import Glyph from '../ui/marks/Glyph.vue';
import Meter from '../ui/marks/Meter.vue';
import type { TileStatus } from '../components/Tile.vue';
import { fmtInt, fmtPct, hhmm } from '../lib/format';
import { attr, byKind, incoming, label, lastBackup, outgoing, resolve, series, value } from '../model/graph';
import { glyphFor } from '../model/status';
import { normalizeHealth, resourceLinks } from '../model/types';
import { useLive } from '../stores/live';
import Frame from './parts/Frame.vue';
import StateNote from './parts/StateNote.vue';
import { fullIn } from './read';
import { useSubject } from './use';

const { size, target } = defineProps<{ size: SizeId; target?: string | undefined }>();
const live = useLive();

const store = computed(() => {
  const t = target ? resolve(live.graph.value, target) : undefined;
  return t?.kind === 'backup_target' ? t : byKind(live.graph.value, 'backup_target')[0];
});
const s = useSubject(() => store.value?.id);
const id = computed(() => store.value?.id ?? '');
const used = computed(() => series(live.metrics.value, id.value, 'used.pct'));
const jobs = computed(() => byKind(live.graph.value, 'backup_job'));
const app = computed(() => outgoing(live.graph.value, id.value, 'part_of').find((r) => r.kind === 'app') ?? resolve(live.graph.value, 'app:pbs'));
const failed = computed(() => value(live.metrics.value, app.value?.id ?? 'app:pbs', 'tasks.failed_24h'));
const last = computed(() => lastBackup(live.graph.value));
const covered = computed(() => jobs.value.reduce((n, j) => n + incoming(live.graph.value, j.id, 'backed_up_by').length, 0));
const failedJobs = computed(() => jobs.value.filter((j) => attr(j, 'last_run.status') === 'failed' || normalizeHealth(j).status === 'attention'));
const full = computed(() => fullIn(store.value, used.value, live.metrics.value?.history));
const status = computed<TileStatus>(() => {
  const own = s.value.status;
  if (own !== 'ok' && own !== 'unknown') return own;
  return failedJobs.value.length || (failed.value ?? 0) > 0 ? 'attention' : 'ok';
});
const tag = computed(() => {
  if (status.value === s.value.status && status.value !== 'ok') return undefined;
  if (status.value === 'attention') return failedJobs.value.length === 1 ? 'a backup failed' : `${failedJobs.value.length || failed.value} failed`;
  return '';
});
const lastWords = computed(() => {
  if (failed.value) return `${failed.value} task${failed.value === 1 ? '' : 's'} failed`;
  if (last.value) return `verified ${hhmm(last.value.at)} · ${last.value.ok} guests`;
  return jobs.value.length ? 'no runs yet' : 'no backup jobs yet';
});
const jobName = (id: string) => label(outgoing(live.graph.value, id, 'runs_on')[0]) || id.split('/')[0]?.replace(/^[a-z]+:/, '') || id;
const incident = computed(() => s.value.problem ?? live.problems.value.find((i) => jobs.value.some((j) => j.id === i.target) || i.scenario === 'backup-failed'));
const nights = computed(() =>
  jobs.value.map((j) => ({
    id: j.id,
    name: jobName(j.id),
    ok: attr(j, 'last_run.status') !== 'failed' && normalizeHealth(j).status !== 'attention',
    known: Boolean(attr(j, 'last_run.status')),
    at: attr<string>(j, 'last_run.end') ?? attr<string>(j, 'last_run.start'),
  })),
);
const capacity = computed(() => attr<number>(store.value, 'capacity_tb'));
</script>

<template>
  <Frame
    :size="size"
    name="Backups"
    mono="PB"
    :context="store ? `PBS · ${label(store)}` : 'PBS'"
    :href="resourceLinks(store)[0]?.url ?? resourceLinks(app)[0]?.url"
    :subject="s"
    :status="store ? status : 'ok'"
    :tag="tag"
    :tag-glyph="tag ? glyphFor(status) : undefined"
    :stale="Boolean(used?.stale)"
    :as-of="used?.as_of"
    :incident="incident"
  >
    <template v-if="!store">
      <div class="v sm word">No backup server</div>
      <p v-if="size !== 'strip'" class="lab">Connect Proxmox Backup Server to see coverage here.</p>
    </template>

    <template v-else-if="size === 'strip' || size === 'strip-long' || size === 'glyph'">
      <Meter v-if="size !== 'glyph'" class="sm-meter" :value="used?.value" :warn="used?.warn ?? 90" :crit="used?.crit ?? 96" :tick="used?.warn ?? 90" label="PBS datastore used" />
      <span class="sv n">{{ fmtPct(used?.value) }}<small>%</small></span>
      <span class="sx">{{ failed ? `${failed} failed` : last ? `ok ${hhmm(last.at)}` : '' }}</span>
    </template>

    <template v-else-if="size === 'small'">
      <div :class="['v', { unknown: used?.value == null }]">{{ fmtPct(used?.value) }}<span class="u">%</span></div>
      <Meter :value="used?.value" :warn="used?.warn ?? 90" :crit="used?.crit ?? 96" :tick="used?.warn ?? 90" label="PBS datastore used" />
      <StateNote v-if="s.status !== 'ok' && s.status !== 'attention'" :subject="s" />
      <div class="tf">
        <span v-if="failed"><span class="n">{{ failed }}</span> task failed</span>
        <span v-else-if="last">last run <span class="n">{{ hhmm(last.at) }}</span></span>
        <span v-else>{{ jobs.length ? 'no runs yet' : 'No backup jobs yet' }}</span>
      </div>
    </template>

    <template v-else>
      <div class="split">
        <div class="col">
          <div :class="['v', { unknown: used?.value == null }]">{{ fmtPct(used?.value) }}<span class="u">% of {{ capacity ?? '—' }} TB</span></div>
          <Meter :value="used?.value" :warn="used?.warn ?? 90" :crit="used?.crit ?? 96" :tick="used?.warn ?? 90" label="PBS datastore used" />
          <div class="lab">{{ full ?? lastWords }}</div>
          <StateNote v-if="s.status !== 'ok' && s.status !== 'attention'" :subject="s" />
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
      <div v-if="nights.length" class="nights" role="list" aria-label="Last night, per job">
        <span v-for="n in nights" :key="n.id" :class="['night', { bad: !n.ok }]" role="listitem" :aria-label="`${n.name}: ${n.known ? (n.ok ? 'ok' : 'failed') : 'no run'}`">
          <Glyph :kind="!n.known ? 'off' : n.ok ? 'check' : 'attn'" />{{ n.name }}
        </span>
      </div>
      <template v-if="size === 'large'">
        <ul v-if="failedJobs.length" class="list" aria-label="Failed runs">
          <li v-for="j in failedJobs" :key="j.id" class="li">
            <Glyph kind="attn" />
            <span><span class="t">{{ jobName(j.id) }} nightly</span><small>{{ attr(j, 'last_run.error') ?? normalizeHealth(j).reason ?? 'failed' }}</small></span>
            <span class="n">{{ hhmm(attr<string>(j, 'last_run.end') ?? attr<string>(j, 'last_run.start')) }}</span>
          </li>
        </ul>
        <p v-else class="lab">No failed runs in the last day.</p>
        <div class="tf"><span>Prune: {{ attr(store, 'retention') ?? '—' }}</span></div>
      </template>
    </template>
  </Frame>
</template>
