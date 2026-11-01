<script setup lang="ts">
/**
 * Settings → Backups (14 · v0.4, "The coverage map"): every guest,
 * container with volumes, Unraid share and flash drive, what backs it up,
 * and how fresh its newest good backup is, grouped by state, what needs you
 * first. Read from the `backups` topic, as this viewer may see it.
 *
 *   PUT    /api/backups/objectives/{resource}  {max_age_ms} → the objective (admin+, audited)
 *   DELETE /api/backups/objectives/{resource}  → the objective it had (admin+, audited)
 *
 * Each row offers Run now (Proxmox's backup of the guest, or the guest a
 * container inherits from) and Verify now (PBS checks the guest's snapshots
 * on the datastore its job writes to), both T1 through the usual ladder;
 * the hub refuses them with its own sentence when they can't run. Admins
 * can hold a resource to its own objective instead of its schedule's.
 *
 * Its own chunk, loaded with the pane.
 */
import { computed, ref } from 'vue';
import { ApiError, api } from '../lib/api';
import { BACKUP_STATES, OBJECTIVE_CHOICES, STATE_GLYPH, STATE_WORDS, counts, coverWords, groups, kindWord, lastWords, objectiveSource, objectiveWords, overBy, runNow, verifyNow, type BackupAction } from '../model/backups';
import type { BackupItem } from '../model/types';
import { useLive } from '../stores/live';
import { useUi } from '../stores/ui';
import Glyph from '../ui/marks/Glyph.vue';
import UiButton from '../ui/Button.vue';
import SettingsHelp from './SettingsHelp.vue';

const live = useLive();
const ui = useUi();
const busy = ref<Record<string, boolean>>({});

const doc = computed(() => live.backups.value);
const loading = computed(() => !doc.value && (live.loading.value || live.conn.status !== 'live'));
const list = computed(() => groups(doc.value));
const n = computed(() => counts(doc.value));
const total = computed(() => BACKUP_STATES.reduce((s, k) => s + n.value[k], 0));
const summary = computed(() => {
  const parts = BACKUP_STATES.filter((k) => n.value[k]).map((k) => `${n.value[k]} ${STATE_WORDS[k].short}`);
  return parts.join(' · ');
});

const canRun = computed(() => ui.can.value.act('T1'));
const canSet = computed(() => ui.can.value.objectives);
const now = computed(() => live.now.value);

function run(a: BackupAction): void {
  void ui.runAction({ action: a.action, target: a.target, label: a.label, tier: 'T1', ...(a.params ? { params: a.params } : {}) });
}

/** The select's value: the override in hours, or "" for the schedule's. */
function objectiveValue(i: BackupItem): string {
  return i.objective === 'override' ? String(Math.round(i.max_age_ms / 3_600_000)) : '';
}

async function setObjective(i: BackupItem, value: string): Promise<void> {
  busy.value[i.resource] = true;
  const path = `/api/backups/objectives/${encodeURIComponent(i.resource)}`;
  try {
    if (value === '') await api(path, { method: 'DELETE' });
    else await api(path, { method: 'PUT', body: { max_age_ms: Number(value) * 3_600_000 } });
    ui.toast(value === '' ? `${i.name} follows its schedule again.` : `${i.name} is held to ${objectiveWords(Number(value) * 3_600_000)}.`);
  } catch (err) {
    ui.toast(err instanceof ApiError ? err.message : 'That didn’t work.', 'error');
  } finally {
    busy.value[i.resource] = false;
  }
}

function choices(i: BackupItem): { value: string; label: string }[] {
  const out = OBJECTIVE_CHOICES.map((c) => ({ value: String(c.hours), label: c.label }));
  const cur = objectiveValue(i);
  if (cur && !out.some((c) => c.value === cur)) out.push({ value: cur, label: objectiveWords(i.max_age_ms) });
  return out;
}
</script>

<template>
  <section class="st-section" aria-labelledby="bk-h">
    <header>
      <h3 id="bk-h">Backup coverage</h3>
    </header>
    <SettingsHelp summary="What in this house is backed up, and what’s stale.">
      Cha Dash reads Proxmox VE’s backup jobs, Proxmox Backup Server’s datastores and backup tools that report what they cover. A container with volumes on a
      Docker host that is a Proxmox guest is covered by the guest’s backup, and says so: a restore brings back the whole guest. Something is stale when its
      newest good backup is older than its objective: 26 hours for a daily job, 8 days for a weekly one, unless you set one here.
    </SettingsHelp>

    <div v-if="loading" class="st-skel" aria-busy="true"><span class="skel"></span><span class="skel"></span></div>
    <div v-else-if="!doc" class="st-note" role="status">
      <Glyph kind="off" /><span>The hub hasn’t sent the coverage map yet.</span>
    </div>
    <div v-else-if="!total" class="st-empty">
      <div class="t">Nothing to back up yet</div>
      <div class="m">Guests from Proxmox VE, containers with volumes, and Unraid’s shares and flash drive show here once their integrations are connected.</div>
    </div>
    <p v-else class="st-meta bk-sum" role="status">{{ summary }}</p>
  </section>

  <template v-if="doc">
    <section v-for="g in list" :key="g.state" class="st-section" :aria-labelledby="`bk-${g.state}`">
      <header>
        <h3 :id="`bk-${g.state}`">{{ g.title }} · {{ g.items.length }}</h3>
      </header>
      <ul class="st-list" :aria-label="g.title">
        <li v-for="i in g.items" :key="i.resource">
          <div class="st-row bk-row">
            <Glyph :kind="STATE_GLYPH[i.state]" :label="STATE_WORDS[i.state].short" />
            <span>
              <span class="t">
                {{ i.name }}
                <span class="chip">{{ kindWord(i.kind) }}</span>
                <span v-if="i.objective === 'override'" class="chip">own objective</span>
              </span>
              <span class="m">{{ i.text }}</span>
              <span v-if="coverWords(i)" class="m">{{ coverWords(i) }}</span>
              <span class="m">
                {{ lastWords(i, now) }}<template v-if="overBy(i, now)"> · {{ overBy(i, now) }}</template>
                <template v-if="i.state !== 'uncovered'"> · objective {{ objectiveWords(i.max_age_ms) }}, {{ objectiveSource(i) }}</template>
              </span>
              <button v-if="i.cause" type="button" class="m bk-cause" @click="ui.openSheet(i.cause.resource)">{{ i.cause.text }}</button>
            </span>
            <span class="end bk-acts">
              <template v-if="canRun">
                <UiButton v-if="runNow(i, live.graph.value)" size="sm" :aria-label="runNow(i, live.graph.value)!.label" @click="run(runNow(i, live.graph.value)!)">
                  Run now<span class="tier">T1</span>
                </UiButton>
                <UiButton v-if="verifyNow(i, live.graph.value)" size="sm" :aria-label="verifyNow(i, live.graph.value)!.label" @click="run(verifyNow(i, live.graph.value)!)">
                  Verify now<span class="tier">T1</span>
                </UiButton>
              </template>
              <select
                v-if="canSet && i.state !== 'uncovered'"
                :value="objectiveValue(i)"
                :disabled="busy[i.resource]"
                :aria-label="`Objective for ${i.name}`"
                @change="setObjective(i, ($event.target as HTMLSelectElement).value)"
              >
                <option value="">Schedule’s ({{ i.objective === 'override' ? 'default' : objectiveWords(i.max_age_ms) }})</option>
                <option v-for="c in choices(i)" :key="c.value" :value="c.value">{{ c.label }}</option>
              </select>
            </span>
          </div>
        </li>
      </ul>
    </section>
  </template>
</template>

<style>
.bk-row {
  grid-template-columns: auto minmax(0, 1fr) auto;
}
.bk-row .t {
  display: flex;
  flex-wrap: wrap;
  align-items: center;
  gap: 6px;
}
.bk-row .m {
  display: block;
}
.bk-acts {
  display: flex;
  flex-wrap: wrap;
  gap: 6px;
  justify-content: end;
  align-items: center;
}
.bk-cause {
  all: unset;
  cursor: pointer;
  color: var(--text-2);
  text-decoration: underline;
  text-underline-offset: 2px;
}
.bk-cause:focus-visible {
  outline: 2px solid var(--focus);
  outline-offset: 2px;
}
.bk-sum {
  margin: 0;
}
@media (max-width: 640px) {
  .bk-row {
    grid-template-columns: auto minmax(0, 1fr);
  }
  .bk-acts {
    grid-column: 2;
    justify-content: start;
  }
}
</style>
