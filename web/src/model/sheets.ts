/**
 * Which resources have a sheet (10 · The resource sheet). Each kind joins
 * when its Overview is designed: containers, then Docker engines and
 * compose stacks, then cameras. Everything else keeps what opening it did before (its incident,
 * its tile, or its link).
 */
import type { GraphDoc } from './types';

const KINDS: ReadonlySet<string> = new Set(['container', 'engine', 'stack', 'camera']);

/** The resource is in the graph and its kind has a sheet. */
export function hasSheet(g: GraphDoc | undefined, id: string): boolean {
  const kind = g?.resources[id]?.kind;
  return kind !== undefined && KINDS.has(kind);
}
