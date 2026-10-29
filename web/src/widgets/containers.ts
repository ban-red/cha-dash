/**
 * What a container's state means, in words: shared by the containers tile
 * and the container's sheet, so both say the same thing ("exited (137) at
 * 03:12 · OOM-killed · sonarr depends on it").
 */
import type { TileStatus } from '../components/Tile.vue';
import { hhmm } from '../lib/format';
import { attr, incoming, label, mergedHealth, sourcesOf } from '../model/graph';
import type { GraphDoc, Resource } from '../model/types';
import { statusOf } from './subject';

export interface ContainerRow {
  r: Resource;
  status: TileStatus;
  /** Why it isn't fine, or "" when it is. */
  word: string;
  /** When it last started or finished, in ms (0 when unknown). */
  changed: number;
  /** "Docker · Unraid" when more than one source reports it. */
  seenBy: string;
}

const DOWN_STATES = new Set(['exited', 'dead']);

export function containerRow(g: GraphDoc | undefined, r: Resource): ContainerRow {
  const h = mergedHealth(g, r.id);
  const status = statusOf({ ...r, health: h });
  const sources = sourcesOf(g, r.id);
  let word = '';
  if (status === 'down') {
    const exit = attr<string | number>(r, 'last_exit') ?? attr<number>(r, 'exit_code');
    const at = attr<string>(r, 'finished_at');
    const parts = [DOWN_STATES.has(r.state ?? '') ? `exited${exit !== undefined ? ` (${String(exit).replace(/ .*/, '')})` : ''}${at ? ` at ${hhmm(at)}` : ''}` : (r.state ?? 'down')];
    if (attr(r, 'oom_killed') === true || /oom/i.test(String(exit ?? '') + (h.reason ?? ''))) parts.push('OOM-killed');
    else if (h.reason) parts.push(h.reason);
    const deps = [...incoming(g, r.id, 'runs_on'), ...incoming(g, r.id, 'depends_on')].map((d) => label(d));
    if (deps.length) parts.push(`${deps.slice(0, 2).join(', ')} depend${deps.length === 1 ? 's' : ''} on it`);
    word = parts.join(' · ');
  } else if (status === 'attention') {
    word = h.reason ?? (Number(attr(r, 'restart_count') ?? 0) > 3 ? 'restarting repeatedly' : 'unhealthy');
  } else if (status === 'off') {
    word = 'stopped (expected)';
  } else if (status === 'maintenance') {
    word = h.reason ?? (r.state === 'paused' ? 'paused' : 'maintenance');
  }
  const changed = Math.max(Date.parse(attr<string>(r, 'started_at') ?? '') || 0, Date.parse(attr<string>(r, 'finished_at') ?? '') || 0);
  return { r, status, word, changed, seenBy: sources.length > 1 ? sources.join(' · ') : '' };
}

/** The image's tag: what follows the last ':' after the last '/', else "latest". */
export function imageTag(r: Resource): string {
  const img = attr<string>(r, 'image') ?? '';
  const at = img.lastIndexOf(':');
  return at > img.lastIndexOf('/') ? img.slice(at + 1) : 'latest';
}

/** "update" when a source says a newer image exists, "—" when it says none, "" when nobody knows. */
export function updateWord(r: Resource): string {
  const u = attr(r, 'update_available');
  return u === true ? 'update' : u === false ? '—' : '';
}

/** A row's accessible name when it opens the sheet: "sonarr, exited (137) at 03:12". */
export function rowName(x: ContainerRow): string {
  return x.word ? `${label(x.r)}, ${x.word}` : label(x.r);
}
