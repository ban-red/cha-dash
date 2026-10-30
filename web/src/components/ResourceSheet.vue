<script setup lang="ts">
/**
 * A resource's sheet (10 · The resource sheet): one container, Docker engine
 * or compose stack, over the front page, opened from the containers tile, ⌘K,
 * the Inbox, another sheet or a deep link
 * (`?r=<resource id>`). A glass panel on the right on desktop, a card a
 * flick dismisses on a phone; the board, the socket and the Inbox stay live
 * underneath. It follows the graph, so a recreated container (keyed by
 * name) keeps its sheet.
 *
 * Tabs: Overview, Actions (what the hub would plan here, by tier, through the
 * usual ladder), Logs (the last 50 lines, kept while the sheet is open),
 * Metrics (CPU and memory, which sources read only while a sheet watches the
 * resource: `live.watchResource`), Related and History (tasks on it; the
 * audit log for admins). A tab with nothing to show isn't drawn.
 * The sheet never repeats the Inbox: an open problem is one line that opens
 * it there.
 *
 * Its own chunk, mounted on first open and kept, so the exit can play.
 */
import { computed, nextTick, onBeforeUnmount, onMounted, ref, useId, watch } from 'vue';
import { AnimatePresence, motion } from 'motion-v';
import Glyph from './marks/Glyph.vue';
import MiniGraph from './marks/MiniGraph.vue';
import { copyText } from '../settings/recipe';
import { api } from '../lib/api';
import { isDone, listActions, type Offer } from '../lib/actions';
import { absolute, duration, hhmm, shortDate } from '../lib/format';
import { transition, useReducedMotion } from '../lib/motion';
import { layerAbove } from '../lib/trap';
import { attr, incoming, label, mergedHealth, outgoing, series, sourcesOf } from '../model/graph';
import { glyphFor } from '../model/status';
import { resourceLinks, safeUrl, type Resource, type Task, type Tier } from '../model/types';
import { useLive } from '../stores/live';
import { useUi } from '../stores/ui';
import type { TileStatus } from './Tile.vue';
import { navigate, settingsPath, stackPath } from '../lib/router';
import { containerRow, imageTag, rowName, updateWord, type ContainerRow } from '../widgets/containers';
import { statusOf } from '../widgets/subject';
import { fmtValue, metricWords, upFor } from '../widgets/read';
import {
  fetchHistory,
  fetchStates,
  keepWords,
  peak,
  pointTime,
  segments,
  SPANS,
  stateWords,
  upWords,
  type HistoryReply,
  type SpanId,
  type StatesReply,
} from '../model/history';

const live = useLive();
const ui = useUi();
const uid = useId();
const reduced = useReducedMotion();
const heading = ref<HTMLElement | null>(null);
const compact = ref(typeof window !== 'undefined' && window.innerWidth < 640);
let lastFocus: Element | null = null;

const id = computed(() => ui.sheet.value);
const open = computed(() => id.value !== undefined);
const current = computed<Resource | undefined>(() => (id.value ? live.graph.value?.resources[id.value] : undefined));
/** The last time the graph had it: a container that goes keeps its name here. */
const lastKnown = ref<Resource | undefined>();
watch(current, (r) => r && (lastKnown.value = r), { immediate: true });
watch(id, (now) => {
  if (now && lastKnown.value?.id !== now) lastKnown.value = current.value;
});
const r = computed(() => current.value ?? (lastKnown.value?.id === id.value ? lastKnown.value : undefined));
const gone = computed(() => open.value && !current.value);

const kind = computed(() => r.value?.kind ?? '');
const KIND_WORDS: Record<string, string> = { container: 'container', engine: 'Docker engine', stack: 'compose stack' };
const kindWord = computed(() => KIND_WORDS[kind.value] ?? kind.value);
const row = computed(() => (current.value?.kind === 'container' ? containerRow(live.graph.value, current.value) : undefined));
const status = computed<TileStatus | undefined>(() => {
  const c = current.value;
  if (!c) return undefined;
  return row.value?.status ?? statusOf({ ...c, health: mergedHealth(live.graph.value, c.id) });
});
const statusWord = computed(() => {
  const c = current.value;
  if (!c) return '';
  const x = row.value;
  if (x) {
    if (x.word) return x.word;
    const state = c.state ?? 'unknown';
    const hc = attr<string>(c, 'health_check');
    return hc && state === 'running' ? `${state} · ${hc}` : state;
  }
  const h = mergedHealth(live.graph.value, c.id);
  return h.status !== 'ok' && h.reason ? h.reason : (c.state ?? h.status);
});
const incident = computed(() => (id.value ? live.problems.value.find((i) => i.target === id.value) : undefined));

// ── Overview ──
const dash = (v: unknown): string => (v === undefined || v === null || v === '' ? '—' : String(v));
const list = (v: unknown): string => (Array.isArray(v) && v.length ? v.join(', ') : '—');
const engine = computed(() => (r.value ? outgoing(live.graph.value, r.value.id, 'runs_on').find((x) => x.kind === 'engine') : undefined));
interface Fact {
  term: string;
  value: string;
}
const facts = computed<Fact[]>(() => {
  const c = r.value;
  if (!c) return [];
  if (c.kind === 'engine') return engineFacts(c);
  if (c.kind === 'stack') return stackFacts(c);
  const running = c.state === 'running';
  const out: Fact[] = [
    { term: 'Image', value: dash(attr(c, 'image')) },
    { term: 'Tag', value: attr(c, 'image') ? imageTag(c) + (updateWord(c) === 'update' ? ' · update available' : '') : '—' },
  ];
  if (running) out.push({ term: 'Up', value: upFor(attr(c, 'started_at'), live.now.value) });
  else {
    const at = attr<string>(c, 'finished_at');
    const exit = attr(c, 'exit_code');
    out.push({ term: 'Stopped', value: at ? `${hhmm(at)}${exit !== undefined ? ` · exit ${String(exit)}` : ''}${attr(c, 'oom_killed') === true ? ' · OOM-killed' : ''}` : '—' });
  }
  const streak = Number(attr(c, 'failing_streak') ?? 0);
  out.push(
    { term: 'Health check', value: running ? dash(attr(c, 'health_check')) + (streak > 0 ? ` · failed ${streak} in a row` : '') : '—' },
    { term: 'Restarts', value: `${dash(attr(c, 'restart_policy'))} · ${dash(attr(c, 'restart_count'))}` },
    { term: 'Memory limit', value: attr(c, 'mem_limit_mib') ? `${String(attr(c, 'mem_limit_mib'))} MiB` : '—' },
    { term: 'Published', value: list(attr(c, 'published')) },
    { term: 'Ports', value: list(attr(c, 'ports')) },
    { term: 'Networks', value: list(attr(c, 'networks')) + (attr(c, 'network_mode') ? ` · ${String(attr(c, 'network_mode'))}` : '') },
    { term: 'Addresses', value: list(attr(c, 'ips')) },
  );
  const project = c.labels?.['com.docker.compose.project'];
  if (project) out.push({ term: 'Compose', value: `${project}${c.labels?.['com.docker.compose.service'] ? ` · ${c.labels['com.docker.compose.service']}` : ''}` });
  const e = engine.value;
  if (e) out.push({ term: 'Engine', value: `${label(e)}${attr(e, 'node') ? ` · node ${String(attr(e, 'node'))}` : ''}` });
  const seen = sourcesOf(live.graph.value, c.id);
  if (seen.length > 1) out.push({ term: 'Seen by', value: seen.join(' · ') });
  return out;
});
/** "3 running of 4": the source's counts, else the containers the graph links to it. */
function countWords(x: Resource): string {
  if (attr(x, 'containers') !== undefined) return `${dash(attr(x, 'running'))} running of ${dash(attr(x, 'containers'))}`;
  const m = members.value;
  return m.length ? `${m.filter((c) => c.r.state === 'running').length} running of ${m.length}` : '—';
}
function engineFacts(e: Resource): Fact[] {
  const out: Fact[] = [
    { term: 'Endpoint', value: dash(attr(e, 'endpoint')) },
    { term: 'Reads through', value: dash(attr(e, 'via')) + (attr(e, 'access') ? ` · ${String(attr(e, 'access'))}` : '') },
    { term: 'Containers', value: countWords(e) },
    { term: 'Docker', value: dash(attr(e, 'version')) + (attr(e, 'api_version') ? ` · API ${String(attr(e, 'api_version'))}` : '') },
    { term: 'System', value: [attr(e, 'os_name') ?? attr(e, 'os'), attr(e, 'arch'), attr(e, 'kernel')].filter(Boolean).join(' · ') || '—' },
    { term: 'Host', value: dash(attr(e, 'hostname') ?? attr(e, 'host')) },
    { term: 'Size', value: attr(e, 'cores') !== undefined ? `${String(attr(e, 'cores'))} cores${attr(e, 'mem_gb') !== undefined ? ` · ${String(attr(e, 'mem_gb'))} GB` : ''}` : '—' },
    { term: 'Managed by', value: dash(attr(e, 'manager')) },
  ];
  if (attr(e, 'node')) out.push({ term: 'Node', value: String(attr(e, 'node')) });
  return out;
}
function stackFacts(st: Resource): Fact[] {
  const out: Fact[] = [
    { term: 'Project', value: dash(attr(st, 'project') ?? st.name) },
    { term: 'Containers', value: countWords(st) },
    { term: 'Folder', value: dash(attr(st, 'working_dir')) },
    { term: 'Compose files', value: list(attr(st, 'config_files')) },
  ];
  if (attr(st, 'managed_by')) out.push({ term: 'Managed by', value: attr(st, 'managed_by') === 'composeman' ? 'Unraid’s Compose Manager' : String(attr(st, 'managed_by')) });
  const e = engine.value;
  if (e) out.push({ term: 'Engine', value: `${label(e)}${attr(e, 'node') ? ` · node ${String(attr(e, 'node'))}` : ''}` });
  return out;
}
/** An engine's or a stack's containers, problems first, each opening its own sheet. */
const RANK: Partial<Record<TileStatus, number>> = { down: 0, attention: 1, maintenance: 2, unknown: 3, ok: 4, off: 5 };
const members = computed<ContainerRow[]>(() => {
  const c = current.value;
  const g = live.graph.value;
  if (!c) return [];
  const list = c.kind === 'engine' ? incoming(g, c.id, 'runs_on') : c.kind === 'stack' ? incoming(g, c.id, 'part_of') : [];
  return list
    .filter((x) => x.kind === 'container')
    .map((x) => containerRow(g, x))
    .sort((a, b) => (RANK[a.status] ?? 9) - (RANK[b.status] ?? 9) || label(a.r).localeCompare(label(b.r)));
});
/** A stack on a node-backed engine: its editor in Settings → Stacks (owned, or detected and adoptable there). */
const stackLink = computed(() => {
  const st = current.value;
  const node = engine.value ? attr<string>(engine.value, 'node') : undefined;
  if (st?.kind !== 'stack' || !node || !ui.can.value.panes.includes('stacks')) return undefined;
  return stackPath(node, attr<string>(st, 'project') ?? st.name ?? '');
});
const nodeLink = computed(() => current.value?.kind === 'engine' && !!attr(current.value, 'node') && ui.can.value.panes.includes('nodes'));
const links = computed(() => resourceLinks(r.value).map((l) => ({ title: l.title || l.url, url: safeUrl(l.url)! })));
const labels = computed(() => Object.entries(r.value?.labels ?? {}).sort(([a], [b]) => a.localeCompare(b)));

// ── Related ──
interface Group {
  name: string;
  items: Resource[];
}
const related = computed<Group[]>(() => {
  const g = live.graph.value;
  const at = id.value;
  if (!at || !current.value) return [];
  const uniq = (xs: Resource[]) => [...new Map(xs.filter((x) => x.id !== at).map((x) => [x.id, x])).values()];
  const groups: Group[] = [
    { name: 'Depends on', items: uniq(outgoing(g, at, 'depends_on')) },
    { name: 'Used by', items: uniq([...incoming(g, at, 'depends_on'), ...incoming(g, at, 'runs_on')]).sort((a, b) => Number(b.kind === 'app') - Number(a.kind === 'app')) },
    { name: 'Routed by', items: uniq(incoming(g, at, 'routes_to')) },
    { name: 'Part of', items: uniq(outgoing(g, at, 'part_of')) },
    { name: 'Runs on', items: uniq(outgoing(g, at, 'runs_on')) },
    { name: 'The same as', items: uniq([...outgoing(g, at, 'same_as'), ...incoming(g, at, 'same_as')]) },
  ];
  const listed = new Set(members.value.map((x) => x.r.id));
  return groups.map((x) => ({ ...x, items: x.items.filter((i) => !listed.has(i.id)) })).filter((x) => x.items.length);
});
/** Another resource: its sheet when its kind has one, else its incident, tile or link. */
function go(x: Resource): void {
  if (ui.hasSheet(x.id)) return ui.openSheet(x.id);
  ui.closeSheet();
  ui.reveal(x.id);
}

// ── Actions ──
/** Actions whose result is lines to read: they live in the Logs tab, not the Actions tab. */
const LOG_ACTIONS: ReadonlySet<string> = new Set(['docker.container.logs']);
/** Remove asks about anonymous volumes first; named ones always stay. */
const REMOVE = 'docker.container.remove';
const TIERS: readonly Tier[] = ['T0', 'T1', 'T2', 'T3', 'T4'];
const offers = ref<{ target: string; list: Offer[] }>({ target: '', list: [] });
let asking: AbortController | undefined;
async function ask(target: string | undefined): Promise<void> {
  asking?.abort();
  if (!target || live.stale.value) return;
  const ctrl = new AbortController();
  asking = ctrl;
  const list = await listActions(target, ctrl.signal);
  if (!ctrl.signal.aborted && id.value === target) offers.value = { target, list };
}
const mineOffers = computed(() => (offers.value.target === id.value ? offers.value.list : []).filter((o) => ui.can.value.act(o.tier, o.household)));
const actionOffers = computed(() => mineOffers.value.filter((o) => !LOG_ACTIONS.has(o.id)));
const logOffer = computed(() => (ui.can.value.readData ? mineOffers.value.find((o) => LOG_ACTIONS.has(o.id)) : undefined));
const byTier = computed(() => TIERS.map((tier) => ({ tier, list: actionOffers.value.filter((o) => o.tier === tier) })).filter((g) => g.list.length));
/** Tasks on this resource: the topic's, plus the ones this tab started. */
const tasksHere = computed(() =>
  Object.values(ui.tasks.value)
    .filter((t) => t.target === id.value)
    .sort((a, b) => (b.started_at ?? '').localeCompare(a.started_at ?? '')),
);
const working = computed(() => tasksHere.value.find((t) => !isDone(t)));
const deleteVolumes = ref(false);
function run(o: Offer): void {
  if (!id.value || !o.available || live.stale.value) return;
  const params = o.id === REMOVE && deleteVolumes.value ? { params: { volumes: true } } : {};
  void ui.runAction({ action: o.id, target: id.value, label: o.label, tier: o.tier, ...params });
}
const pct = (t: Task) => (typeof t.progress === 'number' ? ` · ${Math.round(Math.min(1, Math.max(0, t.progress)) * 100)}%` : '');
const stepOf = (t: Task) => (t.state === 'queued' ? 'waiting its turn' : t.state === 'verifying' ? 'checking it worked' : t.step || 'working');
// Ask again for a new resource, when its state changes (preconditions read it), and when a task on it ends.
watch(id, (now) => {
  deleteVolumes.value = false;
  void ask(now);
}, { immediate: true });
watch(() => current.value?.state, (now, before) => before !== undefined && now !== before && void ask(id.value));
watch(() => working.value?.id, (now, before) => before && !now && void ask(id.value));
watch(live.stale, (stale) => !stale && void ask(id.value));

// ── Logs ──
const logLines = ref<{ target: string; lines: string[] } | undefined>();
const logBusy = computed(() => !!working.value && LOG_ACTIONS.has(working.value.action));
const logCopied = ref(false);
watch(id, () => {
  logLines.value = undefined;
  logCopied.value = false;
});
function showLogs(): void {
  const o = logOffer.value;
  const target = id.value;
  if (!o || !o.available || !target || live.stale.value) return;
  logCopied.value = false;
  void ui.runAction({
    action: o.id,
    target,
    label: o.label,
    tier: o.tier,
    onLog: (log) => {
      if (id.value === target) logLines.value = { target, lines: log.lines };
    },
  });
}
async function copyLogs(): Promise<void> {
  logCopied.value = await copyText((logLines.value?.lines ?? []).join('\n'));
}

// ── History ──
interface AuditRow {
  id: string;
  at: string;
  action: string;
  result: string;
  actor?: { name?: string; kind?: string };
  error?: { message?: string } | null;
}
const audit = ref<{ target: string; events: AuditRow[]; next?: string; state: 'idle' | 'loading' | 'error' }>({ target: '', events: [], state: 'idle' });
async function loadAudit(more = false): Promise<void> {
  const target = id.value;
  if (!target || !ui.can.value.audit) return;
  const cursor = more ? audit.value.next : undefined;
  audit.value = { ...(more ? audit.value : { events: [] }), target, state: 'loading' };
  try {
    const q = new URLSearchParams({ target, limit: '20' });
    if (cursor) q.set('cursor', cursor);
    const res = await api<{ events?: AuditRow[]; next?: string | null }>(`/api/audit?${q.toString()}`);
    if (id.value !== target) return;
    audit.value = { target, events: [...(more ? audit.value.events : []), ...(res.events ?? [])], ...(res.next ? { next: res.next } : {}), state: 'idle' };
  } catch {
    if (id.value === target) audit.value = { ...audit.value, state: 'error' };
  }
}
const taskWord = (t: Task) =>
  t.state === 'succeeded' ? 'done' : t.state === 'failed' ? (t.error?.message ?? 'failed') : t.state === 'cancelled' ? 'cancelled' : stepOf(t);
const resultWord = (e: AuditRow) => (e.result === 'ok' ? 'done' : e.result === 'refused' ? `refused${e.error?.message ? `: ${e.error.message}` : ''}` : (e.error?.message ?? e.result));
function openAudit(): void {
  if (id.value) ui.openSettings('audit', undefined, { target: id.value });
}

// ── Metrics ──
// While the sheet is open the hub knows someone watches this resource, so a
// source can read its costly series (the Docker integration: a container's
// stats every 10 s). The readings seen meanwhile draw the sparkline; the
// series' own history is the fallback.
watch(
  id,
  (now, _, onCleanup) => {
    if (now) onCleanup(live.watchResource(now));
  },
  { immediate: true },
);
const METRICS = ['cpu.pct', 'mem.mib'] as const;
const SEEN_MAX = 60;
const seen = ref<Record<string, (number | null)[]>>({});
watch(id, () => (seen.value = {}));
// Health over the last week (GET /api/history/states), for the History tab.
const HEALTH_SPAN = 7 * 86_400_000;
const health = ref<{ target?: string; reply?: StatesReply; from: number; to: number; state: 'idle' | 'loading' | 'error' }>({ from: 0, to: 0, state: 'idle' });
async function loadHealth(): Promise<void> {
  const target = id.value;
  if (!target) return;
  const to = Date.now();
  health.value = { target, from: to - HEALTH_SPAN, to, state: 'loading' };
  try {
    const reply = await fetchStates(target, HEALTH_SPAN, to);
    if (id.value === target) health.value = { ...health.value, reply, state: 'idle' };
  } catch {
    if (id.value === target) health.value = { ...health.value, state: 'error' };
  }
}
const healthShown = computed(() => {
  const h = health.value;
  return h.target === id.value && h.reply && h.reply.level !== 'off' ? h.reply : undefined;
});
const healthBar = computed(() => (healthShown.value ? segments(healthShown.value.states, health.value.from, health.value.to) : []));
const healthSummary = computed(() => {
  const h = healthShown.value;
  if (!h) return '';
  if (h.up === null) return 'Cha Dash hasn’t watched it in the last 7 days.';
  const last = h.states.at(-1);
  const now = last && last.status !== 'ok' && last.status !== 'stopped' && last.status !== 'gone' ? ` ${stateWords(last.status)} since ${shortDate(last.at)} ${hhmm(last.at)}.` : '';
  return `Up ${upWords(h.up)} of the time Cha Dash was watching.${now}`;
});
/** The newest changes first, each with how long it lasted. */
const healthChanges = computed(() =>
  healthBar.value
    .map((s) => ({ ...s, length: duration(s.to - s.from) }))
    .reverse()
    .slice(0, 8),
);

// What the hub kept (GET /api/history): 'now' is what this sheet read while open.
const span = ref<'now' | SpanId>('now');
const past = ref<{ target?: string; span?: SpanId; reply?: HistoryReply; state: 'idle' | 'loading' | 'error' }>({ state: 'idle' });
watch(id, () => {
  span.value = 'now';
  past.value = { state: 'idle' };
});
async function loadPast(want: SpanId): Promise<void> {
  const target = id.value;
  if (!target) return;
  past.value = { ...past.value, target, span: want, state: 'loading' };
  try {
    const reply = await fetchHistory(
      METRICS.map((k) => `${target}#${k}`),
      want,
      Date.now(),
    );
    if (id.value === target && past.value.span === want) past.value = { target, span: want, reply, state: 'idle' };
  } catch {
    if (id.value === target && past.value.span === want) past.value = { ...past.value, state: 'error' };
  }
}
const historyLevel = computed(() => (past.value.target === id.value ? past.value.reply?.level : undefined));

const metricRows = computed(() =>
  METRICS.flatMap((key) => {
    const sr = id.value ? series(live.metrics.value, id.value, key) : undefined;
    if (!sr) return [];
    const unit = sr.unit ?? (key === 'cpu.pct' ? '%' : 'MiB');
    const fmt = (v: number) => `${fmtValue(v, unit)}${unit === '%' ? '%' : ` ${unit}`}`;
    const local = seen.value[key] ?? [];
    const h = span.value === 'now' || past.value.span !== span.value ? undefined : past.value.reply?.series[`${id.value}#${key}`];
    const top = h ? peak(h) : undefined;
    const spanName = SPANS.find((x) => x.id === span.value)?.name;
    return [
      {
        key,
        name: metricWords(key),
        sr,
        shown: typeof sr.value === 'number' ? fmt(sr.value) : '—',
        of: key === 'mem.mib' && sr.max ? `of ${fmt(sr.max)}` : '',
        values: span.value === 'now' ? (local.length >= 2 ? local : sr.history) : (h?.avg ?? []),
        at: h ? (i: number) => pointTime(h, i) : undefined,
        peak: top === undefined ? '' : `peak ${fmt(top)}`,
        graphLabel: span.value === 'now' ? `${metricWords(key)} while this is open` : `${metricWords(key)}, average over the last ${spanName}`,
        fmt,
      },
    ];
  }),
);
watch(
  () => METRICS.map((key) => (id.value ? series(live.metrics.value, id.value, key) : undefined)),
  (now, before) => {
    const next = { ...seen.value };
    now.forEach((sr, i) => {
      if (!sr) return;
      const prev = before?.[i];
      if (prev && prev.value === sr.value && prev.as_of === sr.as_of) return;
      next[METRICS[i]!] = [...(next[METRICS[i]!] ?? []), sr.value].slice(-SEEN_MAX);
    });
    seen.value = next;
  },
);

// ── Tabs ──
type Tab = 'overview' | 'actions' | 'logs' | 'metrics' | 'related' | 'history';
const tabs = computed(() => {
  const out: { id: Tab; name: string }[] = [{ id: 'overview', name: 'Overview' }];
  if (actionOffers.value.length || working.value) out.push({ id: 'actions', name: 'Actions' });
  if (logOffer.value) out.push({ id: 'logs', name: 'Logs' });
  if (metricRows.value.length) out.push({ id: 'metrics', name: 'Metrics' });
  if (related.value.length) out.push({ id: 'related', name: 'Related' });
  out.push({ id: 'history', name: 'History' });
  return out;
});
const tab = ref<Tab>('overview');
watch(id, () => (tab.value = 'overview'));
watch(tabs, (t) => {
  if (!t.some((x) => x.id === tab.value)) tab.value = 'overview';
});
// The audit log is read when History is first shown for a resource.
watch([tab, id], ([t, now]) => t === 'history' && now && audit.value.target !== now && void loadAudit());
watch([tab, id], ([t, now]) => t === 'history' && now && health.value.target !== now && void loadHealth());
// Opening the tab asks for a day, which also says whether the hub keeps history.
watch([tab, id, span], ([t, now, sp]) => {
  if (t !== 'metrics' || !now) return;
  const want = sp === 'now' ? '24h' : sp;
  if (past.value.target !== now || past.value.span !== want) void loadPast(want);
});
const tabEls = ref<HTMLElement[]>([]);
function onTabKey(e: KeyboardEvent, i: number): void {
  const n = tabs.value.length;
  const to = e.key === 'ArrowRight' ? (i + 1) % n : e.key === 'ArrowLeft' ? (i - 1 + n) % n : e.key === 'Home' ? 0 : e.key === 'End' ? n - 1 : -1;
  if (to < 0) return;
  e.preventDefault();
  tab.value = tabs.value[to]!.id;
  void nextTick(() => tabEls.value[to]?.focus());
}

// ── Layer ──
function close(): void {
  ui.closeSheet();
}
function onKey(e: KeyboardEvent): void {
  // A confirm, the step-up or a modal <dialog> above the sheet takes its own Esc.
  if (e.key === 'Escape' && open.value && !e.defaultPrevented && !layerAbove(ui.state)) close();
}
function onResize(): void {
  compact.value = window.innerWidth < 640;
}
function onDragEnd(_e: PointerEvent, info: { offset: { y: number }; velocity: { y: number } }): void {
  if (info.offset.y + info.velocity.y * 0.2 > 140) close();
}
const enter = computed(() => (reduced.value ? { opacity: 0 } : compact.value ? { y: '100%' } : { opacity: 0, x: 24 }));
const exit = computed(() => (reduced.value ? { opacity: 0 } : compact.value ? { y: '100%' } : { opacity: 0, x: 16 }));

watch(
  id,
  async (now, before) => {
    if (!now) {
      if (lastFocus instanceof HTMLElement && lastFocus.isConnected) lastFocus.focus();
      lastFocus = null;
      return;
    }
    // Moving between sheets keeps where the first one was opened from.
    if (!before) lastFocus = document.activeElement;
    onResize();
    await nextTick();
    heading.value?.focus();
  },
  { immediate: true },
);
onMounted(() => {
  document.addEventListener('keydown', onKey);
  window.addEventListener('resize', onResize);
});
onBeforeUnmount(() => {
  document.removeEventListener('keydown', onKey);
  window.removeEventListener('resize', onResize);
});
</script>

<template>
  <Teleport to="body">
    <div :class="['layer', { inert: !open }]">
      <AnimatePresence>
        <motion.div
          v-if="open"
          key="scrim"
          :class="['scrim', { clear: !compact }]"
          :initial="{ opacity: 0 }"
          :animate="{ opacity: 1 }"
          :exit="{ opacity: 0 }"
          :transition="transition('sheet', reduced)"
          @click="close"
        />
      </AnimatePresence>
      <AnimatePresence>
        <motion.div
          v-if="open"
          key="panel"
          :class="['rsheet', 'glass', { compact }]"
          role="dialog"
          aria-modal="true"
          :aria-labelledby="`${uid}-h`"
          :initial="enter"
          :animate="{ opacity: 1, x: 0, y: 0 }"
          :exit="exit"
          :transition="transition('sheet', reduced)"
          :drag="compact ? 'y' : false"
          :drag-constraints="{ top: 0, bottom: 0 }"
          :drag-elastic="{ top: 0, bottom: 0.9 }"
          @drag-end="onDragEnd"
        >
          <div class="grab" aria-hidden="true"></div>
          <header class="rs-head">
            <Glyph v-if="status" :kind="glyphFor(status)" :label="status" />
            <div class="rs-id">
              <h2 :id="`${uid}-h`" ref="heading" tabindex="-1">{{ label(r) || id }}</h2>
              <p class="rs-sub">
                <span>{{ kindWord || 'resource' }}</span><template v-if="engine"> · {{ label(engine) }}</template>
                <template v-if="kind === 'engine' && attr(r, 'node')"> · node {{ attr(r, 'node') }}</template>
                <template v-if="status"> · <span :class="['rs-word', status]">{{ statusWord }}</span></template>
              </p>
            </div>
            <button type="button" class="x" :aria-label="`Close ${label(r) || 'the sheet'}`" @click="close">
              <svg viewBox="0 0 10 10" aria-hidden="true"><path d="m2.5 2.5 5 5m0-5-5 5" fill="none" stroke="currentColor" stroke-width="1.5" stroke-linecap="round" /></svg>
            </button>
          </header>

          <p v-if="live.stale.value" class="rs-note"><Glyph kind="down" /><span>The hub is unreachable. This is how it was at {{ hhmm(live.offlineAt.value) }}.</span></p>
          <p v-if="gone" class="rs-note"><Glyph kind="off" /><span>This {{ kindWord || 'resource' }} isn’t in the graph any more.</span></p>
          <button v-if="incident && ui.can.value.inbox" type="button" class="rs-incident" @click="ui.openInbox(incident.id)">
            <Glyph :kind="glyphFor(incident.severity)" />
            <span>{{ incident.title }}</span>
            <span class="go">Open in the Inbox</span>
          </button>

          <template v-if="r && !gone">
            <div v-if="tabs.length > 1" class="rs-tabs" role="tablist" :aria-label="`${label(r)}`">
              <button
                v-for="(t, i) in tabs"
                :id="`${uid}-tab-${t.id}`"
                :key="t.id"
                ref="tabEls"
                type="button"
                role="tab"
                :aria-selected="tab === t.id"
                :aria-controls="`${uid}-panel-${t.id}`"
                :tabindex="tab === t.id ? 0 : -1"
                :class="{ on: tab === t.id }"
                @click="tab = t.id"
                @keydown="onTabKey($event, i)"
              >
                {{ t.name }}
              </button>
            </div>

            <section
              v-show="tab === 'overview'"
              :id="`${uid}-panel-overview`"
              class="rs-panel"
              :role="tabs.length > 1 ? 'tabpanel' : undefined"
              :aria-labelledby="tabs.length > 1 ? `${uid}-tab-overview` : `${uid}-h`"
            >
              <dl class="rs-facts">
                <template v-for="f in facts" :key="f.term">
                  <dt>{{ f.term }}</dt>
                  <dd>{{ f.value }}</dd>
                </template>
              </dl>
              <template v-if="members.length">
                <h3 class="rs-h3">Containers · {{ members.length }}</h3>
                <ul class="rs-members" :aria-label="`Containers of ${label(r)}`">
                  <li v-for="x in members" :key="x.r.id">
                    <Glyph v-if="x.status !== 'ok'" :kind="glyphFor(x.status)" :label="x.word" />
                    <span v-else class="rs-dot" aria-hidden="true"></span>
                    <button type="button" class="lk" :aria-label="rowName(x)" @click="ui.openSheet(x.r.id)">{{ label(x.r) }}</button>
                    <small v-if="x.word">{{ x.word }}</small>
                  </li>
                </ul>
              </template>
              <div v-if="stackLink || nodeLink" class="rs-acts">
                <a v-if="stackLink" class="btn sm" :href="stackLink" @click.prevent="navigate(stackLink)">Open in Settings → Stacks</a>
                <a v-if="nodeLink" class="btn sm" :href="settingsPath('nodes')" @click.prevent="ui.openSettings('nodes')">Its node in Settings → Nodes</a>
              </div>
              <ul v-if="links.length" class="rs-links">
                <li v-for="l in links" :key="l.url"><a :href="l.url" target="_blank" rel="noopener noreferrer">{{ l.title }}</a></li>
              </ul>
              <details v-if="labels.length" class="rs-labels">
                <summary>Labels · {{ labels.length }}</summary>
                <dl class="rs-facts mono">
                  <template v-for="[k, v] in labels" :key="k">
                    <dt>{{ k }}</dt>
                    <dd>{{ v }}</dd>
                  </template>
                </dl>
              </details>
              <p v-if="live.demo.value" class="rs-hint">In the Demo Home, Docker is read-only: nothing here has actions or logs.</p>
            </section>

            <section
              v-if="tabs.some((t) => t.id === 'actions')"
              v-show="tab === 'actions'"
              :id="`${uid}-panel-actions`"
              class="rs-panel"
              role="tabpanel"
              :aria-labelledby="`${uid}-tab-actions`"
            >
              <div v-if="working" class="rs-working">
                <span>{{ working.label }} · {{ stepOf(working) }}{{ pct(working) }}</span>
                <button
                  v-if="working.cancellable"
                  type="button"
                  class="btn sm ghost"
                  :disabled="live.stale.value || ui.cancelling[working.id]"
                  :aria-label="`Cancel ${working.label}`"
                  @click="ui.cancelTask(working)"
                >
                  Cancel
                </button>
              </div>
              <div v-for="g in byTier" :key="g.tier" class="rs-tier">
                <h3>{{ g.tier }}</h3>
                <ul>
                  <li v-for="o in g.list" :key="o.id">
                    <button
                      type="button"
                      class="btn"
                      :aria-disabled="!o.available || live.stale.value || !!working || undefined"
                      :aria-describedby="!o.available ? `${uid}-why-${o.id}` : undefined"
                      @click="!working && run(o)"
                    >
                      {{ o.label }}<span class="tier">{{ o.tier }}</span>
                    </button>
                    <label v-if="o.id === REMOVE && o.available" class="rs-check">
                      <input v-model="deleteVolumes" type="checkbox" />
                      Also delete its anonymous volumes (named volumes always stay)
                    </label>
                    <p v-if="!o.available" :id="`${uid}-why-${o.id}`" class="rs-why">
                      {{ o.unavailable ?? 'Cha Dash can’t run this here yet.' }}
                      <button
                        v-if="o.recipe && ui.can.value.integrations"
                        type="button"
                        class="lk under"
                        @click="ui.openSettings('integrations', o.recipe, { section: 'control' })"
                      >
                        Open its settings
                      </button>
                    </p>
                  </li>
                </ul>
              </div>
              <p v-if="live.stale.value" class="rs-hint">Actions come back when the hub does.</p>
            </section>

            <section
              v-if="logOffer"
              v-show="tab === 'logs'"
              :id="`${uid}-panel-logs`"
              class="rs-panel"
              role="tabpanel"
              :aria-labelledby="`${uid}-tab-logs`"
            >
              <p class="rs-hint top">The last 50 lines when you ask, newest at the bottom. Cha Dash doesn’t keep them: they go when you close this. Live logs come with v0.3.</p>
              <p v-if="!logOffer.available" class="rs-why">{{ logOffer.unavailable ?? 'Cha Dash can’t read these logs here yet.' }}</p>
              <div class="rs-acts">
                <button
                  type="button"
                  class="btn"
                  :aria-disabled="!logOffer.available || live.stale.value || logBusy || undefined"
                  :aria-busy="logBusy || undefined"
                  @click="!logBusy && showLogs()"
                >
                  {{ logBusy ? 'Reading…' : logLines ? 'Show again' : 'Show the last 50 lines' }}
                </button>
                <button v-if="logLines?.lines.length" type="button" class="btn ghost" @click="copyLogs">{{ logCopied ? 'Copied' : 'Copy' }}</button>
              </div>
              <p class="sr" aria-live="polite">{{ logCopied ? 'Copied to the clipboard.' : logLines ? `${logLines.lines.length} lines.` : '' }}</p>
              <pre v-if="logLines?.lines.length" class="rs-log" tabindex="0" :aria-label="`Last lines of ${label(r)}’s log`">{{ logLines.lines.join('\n') }}</pre>
              <p v-else-if="logLines" class="rs-hint">No lines came back.</p>
            </section>

            <section
              v-if="metricRows.length"
              v-show="tab === 'metrics'"
              :id="`${uid}-panel-metrics`"
              class="rs-panel"
              role="tabpanel"
              :aria-labelledby="`${uid}-tab-metrics`"
            >
              <p class="rs-hint top">
                Read while this is open, every few seconds.<template v-if="historyLevel"> {{ keepWords(historyLevel) }}</template>
              </p>
              <div v-if="historyLevel && historyLevel !== 'off'" class="rs-span" role="group" aria-label="Time range">
                <button type="button" :aria-pressed="span === 'now'" @click="span = 'now'">Now</button>
                <button v-for="s in SPANS" :key="s.id" type="button" :aria-pressed="span === s.id" @click="span = s.id">{{ s.name }}</button>
              </div>
              <p v-if="span !== 'now' && past.state === 'error'" class="rs-hint">Couldn’t load the history. Try again in a moment.</p>
              <ul class="rs-metrics" :aria-busy="span !== 'now' && past.state === 'loading'">
                <li v-for="m in metricRows" :key="m.key">
                  <span class="rs-mt">{{ m.name }}</span>
                  <span class="rs-mv">
                    <b>{{ m.shown }}</b>{{ m.of ? ' ' : '' }}<small v-if="m.of">{{ m.of }}</small>{{ span !== 'now' && m.peak ? ' ' : '' }}<small v-if="span !== 'now' && m.peak">· {{ m.peak }}</small>
                  </span>
                  <MiniGraph
                    :values="m.values"
                    :band="m.sr.band"
                    :height="span === 'now' ? 28 : 56"
                    :stale="span === 'now' && (m.sr.stale || live.stale.value)"
                    :label="m.graphLabel"
                    :format="m.fmt"
                    :at="m.at"
                  />
                </li>
              </ul>
              <p v-if="span !== 'now' && past.state === 'idle'" class="rs-hint">Containers are read only while their sheet is open, so their history has gaps.</p>
            </section>

            <section
              v-show="tab === 'history'"
              :id="`${uid}-panel-history`"
              class="rs-panel"
              role="tabpanel"
              :aria-labelledby="`${uid}-tab-history`"
            >
              <template v-if="healthShown">
                <h3 class="rs-h3">Health · the last 7 days</h3>
                <p class="rs-up">{{ healthSummary }}</p>
                <template v-if="healthBar.length">
                  <div class="rs-health" role="img" :aria-label="`Health over the last 7 days. ${healthSummary}`">
                    <span
                      v-for="s in healthBar"
                      :key="s.from"
                      :class="['seg', `st-${s.status}`]"
                      :style="{ left: `${s.left}%`, width: `${s.width}%` }"
                      :title="`${stateWords(s.status)} · ${absolute(s.from)}`"
                    ></span>
                  </div>
                  <div class="rs-health-axis" aria-hidden="true"><span>7 days ago</span><span>Now</span></div>
                  <ul class="rs-hist" aria-label="Health changes, newest first">
                    <li v-for="c in healthChanges" :key="c.from">
                      <span class="t">{{ stateWords(c.status) }}</span>
                      <small>{{ shortDate(c.from) }} {{ hhmm(c.from) }} · {{ c.length }}</small>
                    </li>
                  </ul>
                </template>
              </template>
              <p v-else-if="health.state === 'error' && health.target === id" class="rs-why">
                Its health history couldn’t be read. <button type="button" class="lk under" @click="loadHealth()">Try again</button>
              </p>
              <h3 class="rs-h3">Tasks · the last 24 hours</h3>
              <ul v-if="tasksHere.length" class="rs-hist">
                <li v-for="t in tasksHere" :key="t.id">
                  <span class="t">{{ t.label }}</span>
                  <small>{{ t.actor?.name ?? t.actor?.kind ?? '' }}{{ t.started_at ? ` · ${hhmm(t.started_at)}` : '' }} · <span :class="['w', t.state]">{{ taskWord(t) }}</span></small>
                </li>
              </ul>
              <p v-else class="rs-hint">No tasks on it in the last 24 hours.</p>
              <template v-if="ui.can.value.audit">
                <h3 class="rs-h3">Audit log</h3>
                <p v-if="audit.state === 'error'" class="rs-why">The audit log couldn’t be read. <button type="button" class="lk under" @click="loadAudit()">Try again</button></p>
                <ul v-else-if="audit.events.length" class="rs-hist">
                  <li v-for="e in audit.events" :key="e.id">
                    <span class="t mono">{{ e.action }}</span>
                    <small>{{ e.actor?.name ?? e.actor?.kind ?? '' }} · {{ hhmm(e.at) }} · <span :class="['w', e.result]">{{ resultWord(e) }}</span></small>
                  </li>
                </ul>
                <p v-else-if="audit.state === 'idle' && audit.target === id" class="rs-hint">Nothing in the audit log for it yet.</p>
                <div class="rs-acts">
                  <button v-if="audit.next" type="button" class="btn sm" :aria-busy="audit.state === 'loading' || undefined" @click="loadAudit(true)">Older</button>
                  <button type="button" class="btn sm ghost" @click="openAudit">Open in Settings → Audit</button>
                </div>
              </template>
              <p v-else class="rs-hint">Older history is in the audit log, for admins and the owner.</p>
            </section>

            <section
              v-if="related.length"
              v-show="tab === 'related'"
              :id="`${uid}-panel-related`"
              class="rs-panel"
              role="tabpanel"
              :aria-labelledby="`${uid}-tab-related`"
            >
              <div v-for="g in related" :key="g.name" class="rs-group">
                <h3>{{ g.name }}</h3>
                <ul>
                  <li v-for="x in g.items" :key="x.id">
                    <button type="button" class="lk" @click="go(x)">{{ label(x) }}</button>
                    <small>{{ x.kind }}</small>
                  </li>
                </ul>
              </div>
            </section>
          </template>
        </motion.div>
      </AnimatePresence>
    </div>
  </Teleport>
</template>

<style>
.rsheet {
  position: absolute;
  right: max(16px, calc((100% - var(--board-max)) / 2));
  top: 84px;
  width: min(520px, calc(100% - 32px));
  max-height: min(84vh, 880px);
  overflow: auto;
  overscroll-behavior: contain;
  padding: var(--space-2) var(--space-4) var(--space-4);
  border-radius: var(--radius-lg);
  transform-origin: top right;
}
.rsheet.compact {
  left: 0;
  right: 0;
  top: auto;
  bottom: 0;
  width: auto;
  max-height: 92%;
  border-radius: var(--radius-xl) var(--radius-xl) 0 0;
  padding-bottom: calc(var(--space-6) + env(safe-area-inset-bottom));
  border-bottom: 0;
  transform-origin: bottom center;
}
.rsheet.compact .grab {
  display: block;
  height: 18px;
  position: relative;
}
.rs-head {
  display: flex;
  align-items: flex-start;
  gap: var(--space-3);
  padding: var(--space-3) 0 var(--space-2);
}
.rs-head > .g {
  margin-top: 6px;
}
.rs-id {
  min-width: 0;
  flex: 1;
}
/* Focused on open so screen readers start at the name; it isn't a control, so no ring. */
.rs-id h2:focus-visible {
  outline: none;
}
.rs-id h2 {
  margin: 0;
  font: var(--weight-semibold) var(--type-title) / 1.25 var(--font-ui);
  overflow-wrap: anywhere;
}
.rs-sub {
  margin: 2px 0 0;
  font-size: var(--type-small);
  color: var(--text-3);
}
.rs-word.down {
  color: var(--down-text);
}
.rs-word.attention {
  color: var(--attention-text);
}
.rs-head .x {
  flex: none;
  display: grid;
  place-items: center;
  width: var(--target-min);
  height: var(--target-min);
  padding: 0;
  border: 0;
  border-radius: var(--radius-md);
  background: none;
  color: var(--text-2);
}
.rs-head .x svg {
  width: 12px;
  height: 12px;
}
.rs-note,
.rs-hint {
  display: flex;
  gap: var(--space-2);
  align-items: flex-start;
  margin: 0 0 var(--space-3);
  font-size: var(--type-small);
  line-height: var(--leading-relaxed);
  color: var(--text-2);
}
.rs-hint {
  margin: var(--space-4) 0 0;
  color: var(--text-3);
}
.rs-incident {
  display: flex;
  align-items: center;
  gap: var(--space-2);
  width: 100%;
  margin: 0 0 var(--space-3);
  padding: 10px 12px;
  border: var(--hairline) solid var(--line-2);
  border-radius: var(--radius-md);
  background: var(--surface);
  color: var(--text);
  font: inherit;
  font-size: var(--type-small);
  text-align: start;
  cursor: pointer;
}
.rs-incident > span:nth-child(2) {
  flex: 1;
  min-width: 0;
}
.rs-incident .go {
  flex: none;
  color: var(--text-3);
}
.rs-tabs {
  display: flex;
  gap: var(--space-1);
  margin-bottom: var(--space-3);
  border-bottom: var(--hairline) solid var(--line);
}
.rs-tabs [role='tab'] {
  min-height: var(--target-min);
  padding: 0 var(--space-3);
  border: 0;
  border-bottom: 2px solid transparent;
  background: none;
  color: var(--text-2);
  font: var(--weight-medium) var(--type-small) / 1 var(--font-ui);
  cursor: pointer;
}
.rs-tabs [role='tab'].on {
  border-bottom-color: var(--text);
  color: var(--text);
}
.rs-up {
  margin: 0 0 var(--space-2);
  font-size: var(--type-small);
  color: var(--text-2);
}
.rs-health {
  position: relative;
  height: 14px;
  border-radius: 3px;
  overflow: hidden;
  background: repeating-linear-gradient(135deg, transparent 0 3px, color-mix(in oklch, var(--text) 9%, transparent) 3px 4px);
  box-shadow: inset 0 0 0 var(--hairline) var(--line-2);
}
.rs-health .seg {
  position: absolute;
  top: 0;
  bottom: 0;
  min-width: 1px;
}
/* Healthy is quiet; only trouble takes a colour. Unwatched time stays hatched. */
.rs-health .st-ok {
  background: color-mix(in oklch, var(--text) 22%, transparent);
}
.rs-health .st-attention {
  background: var(--attention);
}
.rs-health .st-down,
.rs-health .st-repair {
  background: var(--down);
}
.rs-health .st-maintenance {
  background: var(--maint);
}
.rs-health-axis {
  display: flex;
  justify-content: space-between;
  margin: 4px 0 var(--space-3);
  font-size: var(--type-micro);
  color: var(--text-3);
}
.rs-span {
  display: inline-flex;
  margin-bottom: var(--space-4);
  padding: 2px;
  border-radius: calc(var(--radius-md) + 2px);
  background: color-mix(in oklch, var(--text) 7%, transparent);
}
.rs-span button {
  padding: 5px 10px;
  border: 0;
  border-radius: var(--radius-md);
  background: transparent;
  font: var(--weight-medium) var(--type-small) / 1 var(--font-ui);
  color: var(--text-2);
  white-space: nowrap;
  cursor: pointer;
}
.rs-span button:hover {
  color: var(--text);
}
.rs-span button[aria-pressed='true'] {
  background: var(--surface);
  color: var(--text);
  box-shadow:
    0 0 0 var(--hairline) var(--line-2),
    0 1px 2px color-mix(in oklch, var(--bg) 60%, transparent);
}
.rs-span button:focus-visible {
  outline: 2px solid var(--focus);
  outline-offset: 1px;
}
.rs-metrics {
  display: grid;
  gap: var(--space-4);
  margin: 0;
  padding: 0;
  list-style: none;
}
.rs-metrics li {
  display: grid;
  grid-template-columns: minmax(7.5em, auto) minmax(0, 1fr);
  gap: var(--space-1) var(--space-4);
  align-items: baseline;
  font-size: var(--type-small);
}
.rs-mt {
  color: var(--text-3);
}
.rs-mv {
  display: inline-flex;
  gap: 0.4em;
  align-items: baseline;
}
.rs-mv b {
  font: inherit;
  font-weight: 600;
  font-variant-numeric: tabular-nums;
  color: var(--text);
}
.rs-mv small {
  font-size: inherit;
  color: var(--text-3);
}
.rs-metrics li > :last-child {
  grid-column: 1 / -1;
}
.rs-facts {
  display: grid;
  grid-template-columns: minmax(7.5em, auto) minmax(0, 1fr);
  gap: 6px var(--space-4);
  margin: 0;
  font-size: var(--type-small);
}
.rs-facts dt {
  color: var(--text-3);
}
.rs-facts dd {
  margin: 0;
  color: var(--text);
  font-family: var(--font-num);
  overflow-wrap: anywhere;
}
.rs-facts.mono dt {
  font-family: var(--font-num);
  overflow-wrap: anywhere;
}
.rs-links {
  display: flex;
  flex-wrap: wrap;
  gap: var(--space-3);
  margin: var(--space-4) 0 0;
  padding: 0;
  list-style: none;
  font-size: var(--type-small);
}
.rs-labels {
  margin-top: var(--space-4);
  font-size: var(--type-small);
}
.rs-labels summary {
  cursor: pointer;
  color: var(--text-2);
  margin-bottom: var(--space-2);
}
.rs-tier h3,
.rs-h3 {
  margin: var(--space-3) 0 var(--space-2);
  font: var(--weight-medium) var(--type-caption) / 1.2 var(--font-ui);
  color: var(--text-3);
}
.rs-tier ul,
.rs-hist {
  display: flex;
  flex-direction: column;
  gap: var(--space-2);
  margin: 0;
  padding: 0;
  list-style: none;
}
.rs-tier li {
  display: flex;
  flex-direction: column;
  align-items: flex-start;
  gap: var(--space-1);
}
.rs-tier .btn[aria-disabled='true'] {
  opacity: 0.55;
  cursor: not-allowed;
}
.rs-check {
  display: flex;
  gap: var(--space-2);
  align-items: center;
  font-size: var(--type-small);
  color: var(--text-2);
}
.rs-why {
  margin: 0;
  font-size: var(--type-small);
  color: var(--text-2);
}
.lk.under {
  text-decoration: underline;
  text-underline-offset: 2px;
}
.rs-working {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: var(--space-2);
  margin-bottom: var(--space-2);
  padding: 8px 10px;
  border-radius: var(--radius-md);
  background: var(--surface);
  font-size: var(--type-small);
}
.rs-acts {
  display: flex;
  flex-wrap: wrap;
  gap: var(--space-2);
  margin: var(--space-3) 0;
}
.rs-hint.top {
  margin: 0 0 var(--space-2);
}
.rs-log {
  margin: 0;
  max-height: min(50vh, 28rem);
  overflow: auto;
  padding: 8px 10px;
  border: var(--hairline) solid var(--line);
  border-radius: var(--radius-md);
  font: var(--weight-regular) var(--type-caption) / 1.55 var(--font-num);
  color: var(--text);
  white-space: pre-wrap;
  overflow-wrap: anywhere;
}
.rs-hist li {
  display: flex;
  flex-direction: column;
  font-size: var(--type-small);
}
.rs-hist .mono {
  font-family: var(--font-num);
}
.rs-hist small {
  color: var(--text-3);
}
.rs-hist .w.failed,
.rs-hist .w.refused {
  color: var(--down-text);
}
.rs-members {
  display: flex;
  flex-direction: column;
  gap: 4px;
  margin: 0;
  padding: 0;
  list-style: none;
  font-size: var(--type-small);
}
.rs-members li {
  display: grid;
  grid-template-columns: 10px auto minmax(0, 1fr);
  align-items: baseline;
  gap: var(--space-2);
}
.rs-members small {
  color: var(--text-3);
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}
.rs-group h3 {
  margin: var(--space-3) 0 var(--space-1);
  font: var(--weight-medium) var(--type-caption) / 1.2 var(--font-ui);
  color: var(--text-3);
}
.rs-group ul {
  margin: 0;
  padding: 0;
  list-style: none;
}
.rs-group li {
  display: flex;
  align-items: baseline;
  gap: var(--space-2);
  padding: 4px 0;
  font-size: var(--type-small);
}
.rs-group small {
  color: var(--text-3);
}
</style>
