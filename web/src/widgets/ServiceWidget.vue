<script setup lang="ts">
/**
 * The service tile: any service, with a live stat from its integration
 * (serviceStats.ts, by `attrs.type`) and context from the Home Graph that
 * needs no configuration ("on magpie · update available").
 *
 * What it shows, first match wins:
 * - its target, when the graph has it: an app by ID (`sonarr:<instance id>/app`)
 *   or type (`app:sonarr`), a host or cluster an integration monitors without
 *   an app (a Proxmox node: CPU, memory, guests; a cluster is as well as its
 *   worst node), or a check (`check:<instance id>/<slug>`, an
 *   Uptime Kuma monitor), which is a plain URL: HTTP status, latency, uptime.
 *   `bind.resource` in the tile config is the target (WidgetHost);
 * - the app its tile config's `bind.instance` publishes, else its
 *   `bind.check`;
 * - otherwise the tile config alone (an imported Homepage entry, a link added
 *   by hand, a container found by its labels): name, description and link,
 *   with the status of what it binds (model/service.ts).
 *
 * Glyph: name and the stat (a link glyph for a plain URL). Small: the stat
 * with its unit, one fact, and the graph context. Wide: the stat with a
 * sparkline and its normal band, and two facts. Name and link come from the
 * tile config when it has them (Tile.vue applies them).
 *
 * Views (widgets/views.ts), where there's something to page to: a small tile
 * with history has its 24 hours as a sparkline, and an app with a calendar
 * (Sonarr, Radarr, Lidarr) has what it expects next, at small and wide.
 *
 * Icons are monograms for now: the CSP forbids hot-linking a logo from
 * another origin, and the hub doesn't cache icons yet.
 */
import { computed } from 'vue';
import { hhmm, shortDate } from '../lib/format';
import type { SizeId } from '@chadash/layout';
import Glyph from '../components/marks/Glyph.vue';
import MiniGraph from '../components/marks/MiniGraph.vue';
import Frame from './parts/Frame.vue';
import StateNote from './parts/StateNote.vue';
import { glyphFor, worst } from '../model/status';
import { attr, calendar, label, outgoing, placeOf, resolve } from '../model/graph';
import { findCheck, hostOf, serviceStatus } from '../model/service';
import { resourceLinks, safeUrl } from '../model/types';
import { useBoard } from '../stores/board';
import { useLive } from '../stores/live';
import { originLabel, useTile } from '../stores/tiles';
import { checkFor, checkWords } from './read';
import { clusterNodes, readService, TYPE_NAMES } from './serviceStats';
import { isWithheld, statusOf } from './subject';
import { useSubject } from './use';
import { useViews } from './views';

const { size, target } = defineProps<{ size: SizeId; target?: string | undefined }>();
const live = useLive();
const board = useBoard();
const tile = useTile();

const meta = computed(() => tile.config.value);
/** The resource with the stat: the target, else what the config binds. */
const subjectId = computed(() => {
  const g = live.graph.value;
  const t = target ? resolve(g, target) : undefined;
  if (t) return t.id;
  const bind = meta.value?.bind;
  if (bind?.instance) {
    const watched = outgoing(g, `integration:${bind.instance}`, 'monitors');
    const app = watched.find((r) => r.kind === 'app') ?? watched[0];
    if (app) return app.id;
  }
  return bind?.check ? findCheck(g, bind.check)?.id : undefined;
});
const s = useSubject(() => subjectId.value);
const r = computed(() => s.value.resource);
/** Apps and checks have a stat; anything else (a container, a VM) shows its config and status. */
const hasStat = computed(() => ['app', 'check', 'host', 'cluster'].includes(r.value?.kind ?? ''));
/** A cluster is as well as its worst node. */
const clusterStatus = computed(() => (r.value?.kind === 'cluster' ? worst([s.value.status, ...clusterNodes(live.graph.value, r.value.id).map(statusOf)]) : undefined));
const reading = computed(() => (r.value && hasStat.value ? readService(live.graph.value, live.metrics.value, r.value, live.now.value) : undefined));
const isCheck = computed(() => r.value?.kind === 'check');
const check = computed(() => checkFor(live.graph.value, live.metrics.value, r.value));

/** Without a config: the entry's slug ("homepage:servers/proxmoxpluto" → "proxmoxpluto"). */
const fallback = computed(() => (target ?? '').split('/').pop()?.replace(/^[a-z]+:/, '') || 'Service');
const name = computed(() => meta.value?.name ?? (r.value ? label(r.value) : fallback.value));
const href = computed(() => safeUrl(meta.value?.href) ?? resourceLinks(r.value)[0]?.url);
const type = computed(() => attr<string>(r.value, 'type'));
const context = computed(() => {
  if (!hasStat.value) return size === 'wide' ? hostOf(meta.value?.href) : '';
  if (isCheck.value) return hostOf(attr<string>(r.value, 'target') ?? href.value) || 'check';
  const t = type.value ? TYPE_NAMES[type.value] : undefined;
  return t && t !== name.value ? t : '';
});

const limited = computed(() => Boolean(reading.value && isWithheld(r.value, reading.value.key)));
const value = computed(() => (limited.value ? null : (reading.value?.value ?? null)));
const stale = computed(() => Boolean(reading.value?.series?.stale));
const graphContext = computed(() => {
  const parts: string[] = [];
  const place = r.value ? placeOf(live.graph.value, r.value.id) : undefined;
  if (place) parts.push(`on ${place}`);
  const latest = attr<string>(r.value, 'latest_version');
  if (attr(r.value, 'update_available') === true || (latest && latest !== attr(r.value, 'version'))) parts.push('update available');
  return parts.join(' · ');
});
/** While the integration needs repair, the footer keeps what the HTTP check says. */
const footer = computed(() => (s.value.needsRepair || limited.value ? checkWords(check.value) || graphContext.value : graphContext.value));
const tone = computed(() => (s.value.status === 'attention' ? 'attn' : s.value.status === 'down' ? 'down' : 'neutral'));
/** The config-only tile's status: what it binds, or the resource it shows. */
const legacy = computed(() => (hasStat.value ? undefined : serviceStatus(live.graph.value, meta.value?.bind ?? (r.value ? { resource: r.value.id } : undefined))));
const legacyGlyph = computed(() => (legacy.value ? (legacy.value.via === 'none' ? 'repair' : glyphFor(legacy.value.status)) : undefined));
/** What's wrong with what a config-only tile binds, in its body: the header's tag is hidden on small tiles. */
const legacyNote = computed(() => {
  const l = legacy.value;
  if (!l || l.status === 'ok' || l.via === 'none' || !legacyGlyph.value) return undefined;
  return { glyph: legacyGlyph.value, text: l.word.charAt(0).toUpperCase() + l.word.slice(1) };
});
const loading = computed(() => board.state.status === 'loading' && !meta.value);
const facts = computed(() => (limited.value ? [] : (reading.value?.facts ?? [])));
const origin = computed(() => originLabel(meta.value));

/** This app's own calendar, soonest first (unmonitored entries left out, as the calendar tile does by default). */
const upcoming = computed(() => {
  const app = r.value;
  if (app?.kind !== 'app' || !Array.isArray(attr(app, 'calendar'))) return [];
  return calendar({ resources: { [app.id]: app }, relations: {} }, live.now.value).slice(0, 8);
});
const day = (at: string | number) => {
  const d = shortDate(at);
  return d === shortDate(live.now.value) ? 'Today' : d === shortDate(live.now.value + 86_400_000) ? 'Tomorrow' : d;
};
interface Playing {
  title: string;
  user: string;
  state: string;
  progress: number | null;
  decision: string;
}
interface Play {
  title: string;
  user: string;
  at: number;
  percent: number | null;
  watched: boolean;
}
const str = (v: unknown) => (typeof v === 'string' ? v : '');
const num = (v: unknown) => (typeof v === 'number' ? v : typeof v === 'string' && v.trim() !== '' && Number.isFinite(Number(v)) ? Number(v) : null);
const rows = (key: string): Record<string, unknown>[] => {
  const v = limited.value ? undefined : attr<unknown>(r.value, key);
  return Array.isArray(v) ? v.filter((x): x is Record<string, unknown> => typeof x === 'object' && x !== null) : [];
};
/** What's playing right now (Tautulli's `now_playing`), as the stat view lists it. */
const playing = computed<Playing[]>(() =>
  rows('now_playing')
    .map((x) => ({ title: str(x['title']), user: str(x['user']), state: str(x['state']), progress: num(x['progress']), decision: str(x['decision']) }))
    .filter((x) => x.title),
);
/** The last plays, newest first (Tautulli's `history`, `at` in Unix seconds). */
const plays = computed<Play[]>(() =>
  rows('history')
    .map((x) => ({ title: str(x['title']), user: str(x['user']), at: (num(x['at']) ?? NaN) * 1000, percent: num(x['percent']), watched: x['watched'] === true }))
    .filter((x) => x.title && Number.isFinite(x.at)),
);
const PLAYING_ROWS = 2;
const HISTORY_ROWS = 4;
const { views, view } = useViews(() => {
  if (!hasStat.value || (size !== 'small' && size !== 'wide')) return [];
  const out = [{ id: 'stat', label: 'Now' }];
  if (plays.value.length) out.push({ id: 'history', label: 'History' });
  if (size === 'small' && !limited.value && (reading.value?.series?.history?.length ?? 0) > 1) out.push({ id: 'trend', label: '24 hours' });
  if (upcoming.value.length) out.push({ id: 'upcoming', label: 'Up next' });
  return out;
});
</script>

<template>
  <!-- Nothing with a stat: the tile config, and the status of what it binds. -->
  <Frame
    v-if="!hasStat"
    :size="size"
    :name="name"
    :context="context"
    :href="href"
    :status="legacy?.status ?? 'ok'"
    :tag="legacyNote ? undefined : legacy?.word"
    :tag-glyph="legacyGlyph"
    :tag-note="legacyNote?.text"
    :loading="loading"
    :incident="r ? (s.problem ?? s.repair) : undefined"
  >
    <template v-if="size === 'glyph'">
      <div class="gv"><Glyph kind="link" /></div>
      <div class="gl">{{ hostOf(href) || 'no link' }}</div>
    </template>
    <template v-else>
      <p v-if="meta?.description" :class="['lab', 'svc-desc', { full: size === 'wide' }]">{{ meta.description }}</p>
      <p v-else-if="!meta && !r" class="lab">Not set up yet. Edit the layout to name it and give it a link.</p>
      <StateNote v-if="legacyNote" :line="legacyNote" :title="name" />
      <div class="tf">
        <a v-if="href" class="svc-link" :href="href" target="_blank" rel="noopener noreferrer" @click.stop>
          <span>{{ hostOf(href) || 'Open' }}</span><span aria-hidden="true"> ↗</span>
        </a>
        <span v-else>No link</span>
        <span v-if="size === 'wide' && origin" class="svc-origin">{{ origin }}</span>
      </div>
    </template>
  </Frame>

  <Frame v-else :size="size" :name="name" :context="context" :href="href" :subject="s" :status="clusterStatus" :stale="stale" :as-of="reading?.series?.as_of" :loading="loading" :views="views">
    <template v-if="size === 'glyph'">
      <div v-if="isCheck" class="gv"><Glyph kind="link" /></div>
      <div v-else :class="['gv', { unknown: value === null }]">{{ limited ? '—' : reading?.text }}</div>
      <div class="gl">{{ isCheck ? checkWords(check) || reading?.unit : (reading?.empty ?? reading?.unit) }}</div>
    </template>

    <template v-else-if="view === 'trend'">
      <div class="lab">
        <span class="n">{{ reading?.text }}</span> {{ reading?.unit }} now
      </div>
      <MiniGraph
        :values="reading?.series?.history"
        :band="reading?.series?.band"
        :tone="tone"
        :height="40"
        :stale="stale || s.needsRepair"
        :label="`${name}, ${reading?.unit ?? 'value'}, 24 hours`"
      />
    </template>

    <div v-else-if="view === 'history'" class="list" role="list" :aria-label="`Recently watched on ${name}`">
      <div v-for="p in plays.slice(0, HISTORY_ROWS)" :key="p.at + p.title + p.user" class="ag-row" role="listitem">
        <span class="n">{{ hhmm(p.at) }}</span>
        <span>
          {{ p.title }}
          <small>{{ p.user }} · {{ day(p.at) }}<template v-if="!p.watched && p.percent !== null"> · stopped at {{ Math.round(p.percent) }}%</template></small>
        </span>
      </div>
    </div>

    <div v-else-if="view === 'upcoming'" class="list" role="list" :aria-label="`Up next on ${name}`">
      <div v-for="e in upcoming" :key="e.at + e.title" class="ag-row" role="listitem">
        <span class="n">{{ hhmm(e.at) }}</span>
        <span>{{ e.title }}<small>{{ day(e.at) }}{{ e.detail ? ` · ${e.detail}` : '' }}</small></span>
      </div>
    </div>

    <template v-else>
      <div :class="size === 'wide' ? 'split' : 'col svc-main'">
        <div class="col">
          <template v-if="reading?.empty && !limited">
            <div class="v sm word">{{ reading.empty }}</div>
            <div class="lab">{{ reading.context ?? (facts[0] ? `${facts[0].value} ${facts[0].label}` : '') }}</div>
          </template>
          <template v-else>
            <div :class="['v', { unknown: value === null, sm: size !== 'wide' && (reading?.text.length ?? 0) > 5 }]">
              {{ limited ? '—' : reading?.text }}<span class="u">{{ reading?.unit }}</span>
            </div>
            <div v-if="size === 'small' && facts[0] && !limited" class="lab">
              <span class="n">{{ facts[0].value }}</span> {{ facts[0].label }}
            </div>
            <div v-if="size === 'wide' && facts.length" class="sub">
              <template v-for="(f, i) in facts.slice(0, 2)" :key="f.label">{{ i ? ' · ' : '' }}<b class="n">{{ f.value }}</b> {{ f.label }}</template>
            </div>
          </template>
          <StateNote :subject="s" :limited="limited" :last-known="value !== null" />
          <div v-if="s.status === 'ok' && !limited && reading?.context && !reading.empty" class="lab svc-ctx">{{ reading.context }}</div>
        </div>
        <MiniGraph
          v-if="size === 'wide'"
          :values="limited ? [] : reading?.series?.history"
          :band="reading?.series?.band"
          :tone="tone"
          :height="48"
          :stale="stale || s.needsRepair"
          :label="`${name}, ${reading?.unit ?? 'value'}, 24 hours`"
        />
      </div>
      <div v-if="playing.length" class="list svc-playing" role="list" :aria-label="`Playing on ${name}`">
        <div v-for="p in playing.slice(0, PLAYING_ROWS)" :key="p.title + p.user" class="ag-row" role="listitem">
          <span class="n">{{ p.progress === null ? '—' : `${p.progress}%` }}</span>
          <span>{{ p.title }}<small>{{ p.user }} · {{ p.state === 'playing' ? p.decision : p.state }}</small></span>
        </div>
        <small v-if="playing.length > PLAYING_ROWS" class="svc-more">+{{ playing.length - PLAYING_ROWS }} more</small>
      </div>
      <div class="tf">
        <span v-if="footer">{{ footer }}</span>
        <a v-else-if="href" class="svc-link" :href="href" target="_blank" rel="noopener noreferrer" @click.stop>
          <span>{{ hostOf(href) || 'Open' }}</span><span aria-hidden="true"> ↗</span>
        </a>
      </div>
    </template>
  </Frame>
</template>

<style>
.svc-desc {
  display: -webkit-box;
  -webkit-line-clamp: 2;
  -webkit-box-orient: vertical;
  overflow: hidden;
}
.svc-desc.full {
  -webkit-line-clamp: 3;
}
.svc-link {
  min-width: 0;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
  color: var(--text-2);
  text-decoration: none;
  font-family: var(--font-num);
}
.svc-link:hover {
  color: var(--text);
  text-decoration: underline;
}
.svc-origin {
  flex: none;
  color: var(--text-3);
}
.svc-main {
  gap: var(--space-1);
}
.svc-playing {
  margin-top: auto;
}
.svc-more {
  color: var(--text-3);
  font-size: var(--type-caption);
}
.svc-ctx {
  white-space: nowrap;
  overflow: hidden;
  text-overflow: ellipsis;
}
</style>
