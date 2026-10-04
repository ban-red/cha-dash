/**
 * The state of what a widget shows, in the vocabulary of the Workbench list
 * (docs/design/workbench.md → The eight states): healthy, attention, down,
 * stale, needs repair, permission-limited, plus maintenance, off (expected)
 * and in progress. Loading and empty belong to each widget, since only it
 * knows what "nothing to show" looks like.
 *
 * Pure functions of the topic documents; `useSubject` (use.ts) wraps them.
 *
 * - **Needs repair** comes from the integration that reads the resource: an
 *   open repair targeting `integration:<instance id>`, or that resource's
 *   health `repair`. It isn't an outage: the frame stays quiet, values are
 *   last-known, and the note says how to fix it.
 * - **Permission-limited** values are ones the hub didn't send. Until the hub
 *   has field-level permissions, a resource lists them in `attrs.withheld`
 *   (metric keys or attr names); widgets show "—" with a lock and "Ask an
 *   admin" for those, never a blurred value.
 * - **Off (expected)** is a healthy resource in a stopped state: quiet,
 *   dimmed, "stopped (expected)". Not a problem.
 */
import type { GlyphKind } from '../components/marks/Glyph.vue';
import type { TileStatus } from '../components/Tile.vue';
import { since } from '../lib/format';
import { attr, integrationOf, label, outgoing } from '../model/graph';
import { tileStatus } from '../model/status';
import { normalizeHealth, type GraphDoc, type Health, type Incident, type Resource } from '../model/types';

export interface Subject {
  resource: Resource | undefined;
  health: Health;
  /** The frame treatment for <Tile>: down and attention colour it, repair stays quiet. */
  status: TileStatus;
  /** The integration instance that reads it (`integration:<instance id>`). */
  integration: Resource | undefined;
  /** The open repair for that integration. */
  repair: Incident | undefined;
  needsRepair: boolean;
  /** What's broken, in words: the repair's title, else the integration's reason. */
  repairReason: string | undefined;
  /** The open problem on it, or (while it's in trouble) on what it runs on or depends on. */
  problem: Incident | undefined;
  /** The likely cause of a down or attention state, from that problem or the graph. */
  cause: string | undefined;
}

/** States that are quiet when the resource is healthy: it was stopped on purpose. */
const OFF_STATES = new Set(['stopped', 'off', 'offline', 'exited', 'created', 'disabled', 'paused']);

/** The words for a resource that's off on purpose. */
export const offWords = (r: Resource | undefined, reason?: string): string => reason ?? (r?.state === 'paused' ? 'paused (by you)' : 'stopped (expected)');

/** The tile treatment for one resource's own health and state. */
export function statusOf(r: Resource | undefined): TileStatus {
  if (!r) return 'unknown';
  const h = normalizeHealth(r);
  if (h.status === 'ok' && r.state && OFF_STATES.has(r.state)) return 'off';
  return tileStatus(h, r);
}

/** The integration that reads a resource: whoever `monitors` it, or the thing it's part of. */
export function integrationFor(g: GraphDoc | undefined, id: string | undefined): Resource | undefined {
  let cur = id;
  const seen = new Set<string>();
  for (let depth = 0; cur && depth < 5 && !seen.has(cur); depth++) {
    seen.add(cur);
    const found = integrationOf(g, cur);
    if (found) return found;
    cur = outgoing(g, cur, 'part_of')[0]?.id;
  }
  return undefined;
}

/** `id`, then what it runs on, transitively: app → container → engine → host. */
export function runsOnChain(g: GraphDoc | undefined, id: string): string[] {
  const out = [id];
  let cur = id;
  for (let i = 0; i < 6; i++) {
    const next = outgoing(g, cur, 'runs_on')[0];
    if (!next || out.includes(next.id)) break;
    out.push(next.id);
    cur = next.id;
  }
  return out;
}

const CAUSAL = new Set(['runs_on', 'depends_on', 'part_of']);

function problemFor(g: GraphDoc | undefined, problems: readonly Incident[], id: string, troubled: boolean): Incident | undefined {
  const direct = problems.find((i) => i.target === id);
  if (direct || !troubled) return direct;
  const chain = runsOnChain(g, id);
  return (
    problems.find((i) => chain.includes(i.target)) ??
    problems.find((i) => i.blast_radius?.some((b) => b.resource === id && CAUSAL.has(b.via)))
  );
}

const lowerFirst = (s: string) => (s && /^[A-Z][a-z]/.test(s) ? s[0]!.toLowerCase() + s.slice(1) : s);
const stripDot = (s: string) => s.replace(/\.\s*$/, '');

function causeOf(g: GraphDoc | undefined, problem: Incident | undefined, r: Resource): string | undefined {
  if (problem) {
    if (problem.target !== r.id) return stripDot(problem.title);
    const first = problem.causes?.[0]?.text;
    if (first) return stripDot(first);
  }
  // From the graph: the first thing it runs on that's down, or a container's last exit.
  for (const id of runsOnChain(g, r.id).slice(1)) {
    const on = g?.resources[id];
    if (on && normalizeHealth(on).status === 'down') return `${label(on)} is down`;
    const exit = attr<string>(on, 'last_exit');
    if (exit) return `exit ${exit}`;
  }
  return undefined;
}

export function subjectOf(
  g: GraphDoc | undefined,
  problems: readonly Incident[],
  repairs: readonly Incident[],
  id: string | undefined,
): Subject {
  const resource = id ? g?.resources[id] : undefined;
  const health = normalizeHealth(resource);
  const integration = resource ? integrationFor(g, resource.id) : undefined;
  const ih = normalizeHealth(integration);
  const repair = repairs.find((i) => (integration && i.target === integration.id) || (id && i.target === id));
  const needsRepair = Boolean(repair) || ih.status === 'repair' || health.status === 'repair';
  const repairReason = repair?.title ?? (ih.status === 'repair' ? ih.reason : undefined) ?? (health.status === 'repair' ? health.reason : undefined);
  let status = statusOf(resource);
  if (resource && needsRepair && (status === 'ok' || status === 'unknown')) status = 'repair';
  const troubled = status === 'down' || status === 'attention';
  const problem = resource ? problemFor(g, problems, resource.id, troubled) : undefined;
  const cause = resource && troubled ? causeOf(g, problem, resource) : undefined;
  return { resource, health, status, integration, repair, needsRepair, repairReason, problem, cause };
}

/** The incident a click on the tile opens: the problem first, else the repair. */
export const incidentOf = (s: Subject): Incident | undefined => s.problem ?? s.repair;

/** The header's state word and glyph. Healthy is quiet unless the widget names a word. */
export function tagOf(s: Pick<Subject, 'status' | 'health' | 'resource'>, now: number, okWord = ''): { tag: string; glyph: GlyphKind | undefined } {
  const h = s.health;
  switch (s.status) {
    case 'down':
      return { tag: h.since ? `down ${since(h.since, now)}` : 'down', glyph: 'down' };
    case 'attention':
      return { tag: h.reason ?? 'needs attention', glyph: 'attn' };
    case 'maintenance':
      return { tag: h.reason ?? 'maintenance', glyph: 'maint' };
    case 'off':
      return { tag: offWords(s.resource, h.reason), glyph: 'off' };
    case 'repair':
      return { tag: 'needs repair', glyph: 'repair' };
    case 'unknown':
      return { tag: s.resource ? 'no reading yet' : '', glyph: undefined };
    default:
      return { tag: okWord, glyph: okWord ? 'ok' : undefined };
  }
}

export interface StateLine {
  glyph: GlyphKind;
  text: string;
  /** Offer the repair (Reconnect) after the text. */
  reconnect?: boolean;
}

/**
 * The one sentence a widget prints for a state that isn't healthy, symptom
 * first and with its time: "Down since 14:02 · exit 137 · likely: qBittorrent
 * ran out of memory", "Plex rejected its token. Values are last-known.",
 * "Stopped (as intended)". Undefined when healthy.
 */
export function stateLine(s: Subject, now: number): StateLine | undefined {
  const h = s.health;
  switch (s.status) {
    case 'down': {
      const parts = [h.since ? `Down ${since(h.since, now)}` : 'Down'];
      if (h.reason && !/^down\b/i.test(h.reason)) parts.push(h.reason);
      if (s.cause) parts.push(`likely: ${lowerFirst(s.cause)}`);
      return { glyph: 'down', text: parts.join(' · ') };
    }
    case 'attention': {
      const parts = [sentence(h.reason ?? 'Needs attention')];
      if (s.cause && !h.reason?.toLowerCase().includes(s.cause.toLowerCase())) parts.push(`likely: ${lowerFirst(s.cause)}`);
      return { glyph: 'attn', text: parts.join(' · ') };
    }
    case 'maintenance':
      return { glyph: 'maint', text: h.reason ? `Paused · ${h.reason}` : 'Paused · maintenance' };
    case 'off':
      return { glyph: 'off', text: h.reason ? sentence(h.reason) : s.resource?.state === 'paused' ? 'Paused (by you)' : 'Stopped (as intended)' };
    case 'repair':
      return { glyph: 'repair', text: `${stripDot(sentence(s.repairReason ?? "Cha Dash can't read it"))}.`, reconnect: true };
    default:
      return undefined;
  }
}

export const sentence = (s: string) => (s ? s[0]!.toUpperCase() + s.slice(1) : s);

/** The hub didn't send this value to this viewer (or couldn't read it with its credential). */
export function isWithheld(r: Resource | undefined, key: string): boolean {
  const w = r?.attrs?.['withheld'];
  return Array.isArray(w) && w.includes(key);
}

/** Roles that can repair integrations (and see their settings). */
export const canRepair = (role: string | undefined) => role === 'owner' || role === 'admin';
