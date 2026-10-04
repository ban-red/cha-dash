/**
 * The generic service tile's status, from its tile binding: the check that
 * watches it (is the service up?), else the integration instance it shows
 * (can Cha Dash read it?), else the resource it's bound to (a container found
 * by its labels: is it running?). All arrive in the Home Graph; a tile bound
 * to nothing has no status at all, and one whose watcher hasn't reported yet
 * says so instead of guessing.
 *
 * Lookups are deliberately forgiving about ids until the framework's
 * resource ids settle: a check is a `check` resource named after the target
 * (or `check:<name>`, or `…/<name>`); an instance is `integration:<id>`.
 */
import type { TileStatus } from '../components/Tile.vue';
import { tileStatus } from './status';
import { normalizeHealth, type GraphDoc, type Resource } from './types';

export interface ServiceStatus {
  status: TileStatus;
  word: string;
  /** Where it came from. */
  via: 'check' | 'integration' | 'resource' | 'none';
}

const WORDS: Record<TileStatus, string> = {
  ok: 'up',
  attention: 'needs attention',
  down: 'down',
  off: 'off',
  maintenance: 'maintenance',
  repair: 'needs repair',
  unknown: 'unknown',
};

export function findCheck(g: GraphDoc | undefined, check: string): Resource | undefined {
  if (!g) return undefined;
  const direct = g.resources[`check:${check}`];
  if (direct) return direct;
  return Object.values(g.resources).find((r) => r.kind === 'check' && (r.name === check || r.id.endsWith(`/${check}`)));
}

export function serviceStatus(
  g: GraphDoc | undefined,
  bind: { instance?: string; check?: string; resource?: string } | undefined,
): ServiceStatus | undefined {
  if (!bind?.check && !bind?.instance && !bind?.resource) return undefined;
  const check = bind.check ? findCheck(g, bind.check) : undefined;
  if (check) {
    const h = normalizeHealth(check);
    const s = tileStatus(h, check);
    return { status: s, word: s === 'ok' ? 'up' : (h.reason ?? WORDS[s]), via: 'check' };
  }
  const inst = bind.instance ? g?.resources[`integration:${bind.instance}`] : undefined;
  if (inst) {
    const h = normalizeHealth(inst);
    const s = tileStatus(h, inst);
    return { status: s, word: s === 'ok' ? 'reading' : (h.reason ?? WORDS[s]), via: 'integration' };
  }
  const r = bind.resource ? g?.resources[bind.resource] : undefined;
  if (r) {
    const h = normalizeHealth(r);
    const s = tileStatus(h, r);
    return { status: s, word: s === 'ok' ? (r.state ?? 'up') : (h.reason ?? WORDS[s]), via: 'resource' };
  }
  return { status: 'unknown', word: 'no reading yet', via: 'none' };
}

/** "pluto.lan:8006" from a link, for the tile's context line. */
export function hostOf(href: string | undefined): string {
  if (!href) return '';
  try {
    return new URL(href).host;
  } catch {
    return '';
  }
}
