/**
 * The generic service tile's status: from the check that watches it (is the
 * service up?), else from the integration instance it shows (can Cha Dash
 * read it?). Both arrive in the Home Graph; a tile with neither has no status
 * at all, and one whose watcher hasn't reported yet says so instead of
 * guessing.
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
  via: 'check' | 'integration' | 'none';
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

export function serviceStatus(g: GraphDoc | undefined, meta: { instance?: string; check?: string } | undefined): ServiceStatus | undefined {
  if (!meta?.check && !meta?.instance) return undefined;
  const check = meta.check ? findCheck(g, meta.check) : undefined;
  if (check) {
    const h = normalizeHealth(check);
    const s = tileStatus(h, check);
    return { status: s, word: s === 'ok' ? 'up' : (h.reason ?? WORDS[s]), via: 'check' };
  }
  const inst = meta.instance ? g?.resources[`integration:${meta.instance}`] : undefined;
  if (inst) {
    const h = normalizeHealth(inst);
    const s = tileStatus(h, inst);
    return { status: s, word: s === 'ok' ? 'reading' : (h.reason ?? WORDS[s]), via: 'integration' };
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
