<script setup lang="ts">
/**
 * Uptime checks: the built-in HTTP/TCP checks and Uptime Kuma monitors (both
 * kind `check`). The target picks the scope: one check
 * (`check@check:<instance id>/<slug>`), an Uptime Kuma instance's monitors
 * (`check@uptimekuma:<instance id>/app`, or `app:uptimekuma`), a checks
 * instance (`check@integration:<instance id>`), or with none, every check.
 *
 * Glyph: up/total, or one check's status. Strip: name · 24 hourly bars ·
 * uptime. Small: up/total, the bars, uptime. Wide: the checks as rows (worst
 * first) with latency sparklines; one check gets its latency over 24 hours.
 */
import { computed } from 'vue';
import type { SizeId } from '@chadash/layout';
import Glyph from '../components/marks/Glyph.vue';
import Sparkline from '../components/marks/Sparkline.vue';
import type { TileStatus } from '../components/Tile.vue';
import { fmtInt, plural, since } from '../lib/format';
import { appsOfType, label, resolve } from '../model/graph';
import { glyphFor } from '../model/status';
import { resourceLinks } from '../model/types';
import { hostOf } from '../model/service';
import { useLive } from '../stores/live';
import Frame from './parts/Frame.vue';
import StateNote from './parts/StateNote.vue';
import StatusBars from './parts/StatusBars.vue';
import { cells, checkScope, counts, type CheckRow } from './checks';
import { fmtUptime } from './read';
import { subjectOf } from './subject';
import { useSubject } from './use';

const { size, target } = defineProps<{ size: SizeId; target?: string | undefined }>();
const live = useLive();

const scopeRes = computed(() => (target ? resolve(live.graph.value, target) : undefined));
const s = useSubject(() => scopeRes.value?.id);
const scope = computed(() => checkScope(live.graph.value, live.metrics.value, scopeRes.value));
const single = computed(() => (scopeRes.value?.kind === 'check' ? scope.value.rows[0] : undefined));
const c = computed(() => counts(scope.value));
const hours = computed(() => cells(scope.value, 24));
const empty = computed(() => !scope.value.rows.length && !scope.value.kuma);

const RANK: Partial<Record<TileStatus, number>> = { down: 0, attention: 1, maintenance: 2, unknown: 3, ok: 4, off: 5 };
const rows = computed(() => [...scope.value.rows].sort((a, b) => (RANK[a.status] ?? 9) - (RANK[b.status] ?? 9) || a.name.localeCompare(b.name)));
const worst = computed<CheckRow | undefined>(() => rows.value.find((r) => r.status === 'down') ?? rows.value.find((r) => r.status === 'attention'));

/** Uptime Kuma instances whose key was rejected: their monitors are last-known; built-in checks keep running. */
const kumaRepair = computed(() => {
  if (scopeRes.value) return undefined;
  return appsOfType(live.graph.value, 'uptimekuma')
    .map((a) => subjectOf(live.graph.value, live.problems.value, live.repairs.value, a.id))
    .find((x) => x.needsRepair);
});

const status = computed<TileStatus>(() => {
  if (single.value) return single.value.status;
  if (s.value.status === 'repair' || s.value.status === 'maintenance') return s.value.status;
  if (scope.value.kuma) return c.value.down > 0 ? 'attention' : s.value.status === 'unknown' ? 'ok' : s.value.status;
  if (worst.value) return worst.value.status;
  if (rows.value.length && rows.value.every((r) => r.status === 'maintenance')) return 'maintenance';
  return rows.value.length && rows.value.every((r) => r.status === 'off') ? 'off' : 'ok';
});
const stale = computed(() =>
  Boolean(scope.value.kuma?.up?.stale) || (rows.value.length > 0 && rows.value.filter((r) => r.latency).every((r) => r.latency?.stale) && rows.value.some((r) => r.latency)),
);
const asOf = computed(() => scope.value.kuma?.up?.as_of ?? rows.value.find((r) => r.latency?.as_of)?.latency?.as_of);

function rowWords(r: CheckRow): string {
  switch (r.status) {
    case 'down':
      return `down ${since(r.since, live.now.value)}${r.code ? ` (HTTP ${r.code})` : ''}`.trim();
    case 'attention':
      return r.reason ?? (r.latency?.value != null ? `slow · ${fmtInt(r.latency.value)} ms` : 'slow');
    case 'maintenance':
      return r.reason ?? 'silenced';
    case 'off':
      return 'paused';
    case 'unknown':
      return 'no reading yet';
    default:
      return r.latency?.value != null ? `${fmtInt(r.latency.value)} ms` : 'up';
  }
}

const tag = computed(() => {
  if (single.value) return rowWords(single.value);
  if (s.value.status === 'repair') return 'needs repair';
  if (worst.value) return worst.value.status === 'down' ? `${worst.value.name} down ${since(worst.value.since, live.now.value)}`.trim() : `${worst.value.name} ${rowWords(worst.value)}`;
  if (scope.value.kuma && c.value.down > 0) return `${plural(c.value.down, 'monitor')} down`;
  if (status.value === 'maintenance') return 'silenced';
  return '';
});
const name = computed(() => (scopeRes.value ? label(scopeRes.value) : 'Checks'));
const context = computed(() => {
  if (single.value) return hostOf(single.value.target) || single.value.target || '';
  if (scope.value.kuma) return 'Uptime Kuma';
  const n = scope.value.rows.length;
  return n ? plural(n, 'check') : '';
});
const ratio = computed(() => `${fmtInt(c.value.up)}`);
</script>

<template>
  <Frame
    :size="size"
    :name="name"
    :mono="scopeRes ? undefined : 'HC'"
    :context="context"
    :href="resourceLinks(scopeRes)[0]?.url"
    :subject="scopeRes ? s : kumaRepair"
    :status="status"
    :tag="tag"
    :tag-glyph="tag ? glyphFor(status) : undefined"
    :stale="stale"
    :as-of="asOf"
    :incident="scopeRes ? undefined : (worst ? live.problems.value.find((i) => i.target === worst?.id) : undefined)"
  >
    <template v-if="empty">
      <div :class="size === 'glyph' ? 'gv unknown' : 'v sm word'">{{ size === 'glyph' ? '—' : 'No checks yet' }}</div>
      <p v-if="size !== 'glyph'" class="lab">Every discovered URL gets one automatically.</p>
    </template>

    <template v-else-if="size === 'glyph'">
      <div v-if="single" class="gv"><Glyph :kind="glyphFor(single.status)" :label="rowWords(single)" /></div>
      <div v-else :class="['gv', { unknown: c.up === null }]">{{ ratio }}<span class="u">/{{ fmtInt(c.total) }}</span></div>
      <div class="gl">{{ single ? rowWords(single) : 'up' }}</div>
    </template>

    <template v-else-if="size === 'strip' || size === 'strip-long'">
      <StatusBars :cells="hours" :label="`${name}, last 24 hours`" />
      <span class="sv n">{{ fmtUptime(single ? single.uptime : c.uptime) }}<small>%</small></span>
    </template>

    <template v-else-if="size === 'small' || (single && size !== 'wide')">
      <div v-if="single" :class="['v', { unknown: single.latency?.value == null }]">
        {{ fmtInt(single.latency?.value) }}<span class="u">ms</span>
      </div>
      <div v-else :class="['v', { unknown: c.up === null }]">{{ ratio }}<span class="u">/{{ fmtInt(c.total) }} up</span></div>
      <StatusBars :cells="hours" :label="`${name}, last 24 hours`" />
      <StateNote v-if="kumaRepair && !scopeRes" :line="{ glyph: 'repair', text: 'Uptime Kuma: key rejected; built-in checks keep running.' }" />
      <StateNote v-else :subject="scopeRes && !single ? s : undefined" />
      <div class="tf">
        <span><span class="n">{{ fmtUptime(single ? single.uptime : c.uptime) }}%</span> up · 24 h</span>
      </div>
    </template>

    <template v-else-if="single">
      <div class="split">
        <div class="col">
          <div :class="['v', { unknown: single.latency?.value == null }]">{{ fmtInt(single.latency?.value) }}<span class="u">ms</span></div>
          <div class="sub">
            <template v-if="single.code">HTTP <b class="n">{{ single.code }}</b> · </template><b class="n">{{ fmtUptime(single.uptime) }}%</b> up · 24 h
          </div>
          <div v-if="single.status !== 'ok'" :class="['note', 'w-note', `n-${glyphFor(single.status)}`]">
            <Glyph :kind="glyphFor(single.status)" /><span>{{ rowWords(single) }}{{ single.reason && single.status === 'down' ? ` · ${single.reason}` : '' }}</span>
          </div>
        </div>
        <Sparkline
          :values="single.latency?.history"
          :band="single.latency?.band"
          :tone="single.status === 'down' ? 'down' : single.status === 'attention' ? 'attn' : 'neutral'"
          :threshold="single.latency?.warn"
          :stale="single.latency?.stale"
          :height="52"
          :label="`${single.name} latency, 24 hours`"
          :format="(v: number) => `${fmtInt(v)} ms`"
        />
      </div>
      <div class="tf"><StatusBars :cells="hours" :label="`${single.name}, last 24 hours`" /></div>
    </template>

    <template v-else>
      <div class="row">
        <div :class="['v', 'sm', { unknown: c.up === null }]">{{ ratio }}<span class="u">/{{ fmtInt(c.total) }} up</span></div>
        <span class="lab"><span class="n">{{ fmtUptime(c.uptime) }}%</span> · 24 h</span>
      </div>
      <ul v-if="rows.length" class="list chk-rows" :aria-label="`${name}: checks`">
        <li v-for="r in rows.slice(0, 4)" :key="r.id" :class="['chk', { dim: r.status === 'off' }]">
          <Glyph :kind="glyphFor(r.status)" :label="rowWords(r)" />
          <span class="t">{{ r.name }}<small v-if="r.target">{{ hostOf(r.target) || r.target }}</small></span>
          <Sparkline
            :values="r.latency?.history"
            :band="r.latency?.band"
            :tone="r.status === 'down' ? 'down' : r.status === 'attention' ? 'attn' : 'neutral'"
            :height="16"
            :label="`${r.name} latency`"
            :format="(v: number) => `${fmtInt(v)} ms`"
          />
          <span class="n">{{ rowWords(r) }}</span>
        </li>
      </ul>
      <StatusBars v-else :cells="hours" :label="`${name}, last 24 hours`" />
      <StateNote v-if="kumaRepair && !scopeRes" :line="{ glyph: 'repair', text: 'Uptime Kuma: key rejected; built-in checks keep running.' }" />
      <StateNote v-else :subject="scopeRes ? s : undefined" />
      <div v-if="rows.length > 4" class="tf"><span>+{{ rows.length - 4 }} more</span></div>
    </template>
  </Frame>
</template>

<style>
/* ───────────── Hourly status bars: shape carries the state ───────────── */
.bars {
  display: flex;
  gap: 2px;
  height: 16px;
  align-items: flex-end;
}
.bars i {
  flex: 1;
  min-width: 2px;
  height: 45%;
  border-radius: 1px;
  background: color-mix(in oklch, var(--text-3) 48%, transparent);
}
.bars i.b-attn {
  height: 72%;
  background: var(--attention);
}
.bars i.b-down {
  height: 100%;
  background: var(--down);
}
.bars i.b-maint {
  height: 100%;
  background: linear-gradient(90deg, var(--maint) 0 30%, transparent 30% 70%, var(--maint) 70%);
}
.bars i.b-unknown {
  height: 100%;
  background: repeating-linear-gradient(135deg, var(--line-2) 0 1.5px, transparent 1.5px 4px);
}
.chk {
  display: grid;
  grid-template-columns: auto minmax(0, 1fr) minmax(2.5em, 5em) auto;
  gap: var(--space-2);
  align-items: center;
  padding: 5px 0;
  border-top: var(--hairline) solid var(--line);
  font-size: var(--type-small);
  color: var(--text);
}
.chk > .g {
  width: 8px;
  height: 8px;
}
.chk .t {
  min-width: 0;
  white-space: nowrap;
  overflow: hidden;
  text-overflow: ellipsis;
}
.chk small {
  margin-left: 6px;
  font-size: var(--type-caption);
  color: var(--text-3);
}
.chk .n {
  font-size: var(--type-caption);
  color: var(--text-3);
  white-space: nowrap;
}
</style>
