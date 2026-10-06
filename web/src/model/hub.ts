/**
 * Cha Dash's own place in the Home Graph (03 · Architecture → "What the graph
 * enables": the hub knows its own container → host → router chain).
 *
 * The hub publishes itself as `chadash:hub` (kind app, attrs.self); the
 * Demo Home's stand-in is `app:chadash` (attrs.self). Integrations that can
 * tell where it runs relate it with runs_on: Docker (the node names the hub's
 * container, or the hub knows its own container ID), Unraid (its container
 * of that ID), Proxmox (the hub_vmid hint, or the hub's addresses). The
 * correlator's same_as and runs_on carry the chain further.
 *
 * Pure helpers: is a resource the hub, and the chain it runs on.
 */
import { label, outgoing } from './graph';
import type { GraphDoc, Resource } from './types';

export const HUB = 'chadash:hub';

/** Is this resource Cha Dash itself? */
export function isHub(g: GraphDoc | undefined, id: string): boolean {
  if (id === HUB) return true;
  const r = g?.resources[id];
  return r?.kind === 'app' && r.attrs?.['self'] === true;
}

/** The hub's resource in this graph, if it has one. */
export function hubOf(g: GraphDoc | undefined): Resource | undefined {
  if (!g) return undefined;
  return g.resources[HUB] ?? Object.values(g.resources).find((r) => isHub(g, r.id));
}

const MAX_HOPS = 8;

/** Where r runs: its runs_on (else part_of) target, or one of the same thing's other sources'. */
function nextHop(g: GraphDoc, id: string, seen: Set<string>): Resource | undefined {
  const pick = (from: string) =>
    [...outgoing(g, from, 'runs_on'), ...outgoing(g, from, 'part_of').filter((r) => r.kind !== 'stack')]
      .filter((r) => !seen.has(r.id))
      .sort((a, b) => a.id.localeCompare(b.id))[0];
  const direct = pick(id);
  if (direct) return direct;
  // The same container as another source sees it (Docker and Unraid, joined
  // by the correlator): continue from there.
  for (const rel of Object.values(g.relations)) {
    if (rel.type !== 'same_as' || (rel.from !== id && rel.to !== id)) continue;
    const other = rel.from === id ? rel.to : rel.from;
    if (seen.has(other)) continue;
    const via = pick(other);
    if (via) return via;
  }
  return undefined;
}

export interface HubPlace {
  /** What the hub runs on, nearest first: container, engine, host, … */
  chain: Resource[];
  /** The router, when the graph has one: everything reaches the hub through it. */
  router?: Resource;
}

/** The chain Cha Dash runs on, and the home's router. */
export function hubPlace(g: GraphDoc | undefined): HubPlace {
  const hub = hubOf(g);
  if (!g || !hub) return { chain: [] };
  const chain: Resource[] = [];
  const seen = new Set([hub.id]);
  let cur = hub.id;
  for (let i = 0; i < MAX_HOPS; i++) {
    const next = nextHop(g, cur, seen);
    if (!next) break;
    seen.add(next.id);
    chain.push(next);
    cur = next.id;
  }
  const routers = Object.values(g.resources)
    .filter((r) => r.kind === 'router')
    .sort((a, b) => a.id.localeCompare(b.id));
  const out: HubPlace = { chain };
  const router = routers.find((r) => chain.some((c) => c.id === r.id)) ?? routers[0];
  if (router && !chain.some((c) => c.id === router.id)) out.router = router;
  return out;
}

const KIND_WORDS: Record<string, string> = {
  container: 'container',
  engine: 'Docker on',
  vm: 'VM',
  lxc: 'LXC',
  host: 'host',
  node: 'host',
  router: 'router',
  nas: 'NAS',
  stack: 'stack',
};

/** "container chadash", "VM pvedocker", "host io". */
export function placeWords(r: Resource): string {
  const k = KIND_WORDS[r.kind] ?? r.kind;
  return `${k} ${label(r)}`;
}
