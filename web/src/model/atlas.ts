/**
 * The Atlas: a map of the home, as nodes on rows and the relations between
 * them (docs/plan/09-v0.3.md, "The Atlas"). Pure: the same graph, layers
 * and folds always give the same picture, so a snapshot test pins it.
 *
 * - **Only primaries.** A node is a resource without an outgoing `same_as`;
 *   its health is the worst of its members', as tiles merge it. Relations
 *   between aliases are drawn between their primaries.
 * - **Rows.** The router and the WAN at the top, then hosts, guests and
 *   storage, engines, containers, apps, and routes at the edge. Empty rows
 *   close up.
 * - **Columns.** A tidy tree: each node's parent is what it runs on (or is
 *   part of, stores on, or routes to) on a row above it. A parent sits over
 *   its children; roots go left to right, those with children first.
 * - **Folds.** An engine with more than FOLD healthy containers draws them
 *   as one group node ("18 more containers"), with the apps in them. A
 *   container or app that isn't ok is never folded away.
 * - **The list** is the same forest in the same order (`order`), so the
 *   list view and the canvas always hold the same nodes.
 */
import { aliases, label, mergedHealth } from './graph';
import type { CauseStep, GraphDoc, HealthStatus } from './types';

export type Layer = 'compute' | 'network' | 'storage' | 'backups' | 'dependencies';

export const LAYERS: readonly { id: Layer; name: string; about: string }[] = [
  { id: 'compute', name: 'Compute', about: 'Hosts, guests, engines, containers and apps' },
  { id: 'network', name: 'Network', about: 'The router and the routes in' },
  { id: 'storage', name: 'Storage', about: 'Arrays, pools, shares and datastores' },
  { id: 'backups', name: 'Backups', about: 'Backup jobs and what they protect' },
  { id: 'dependencies', name: 'Dependencies', about: 'What depends on what' },
];

export const DEFAULT_LAYERS: readonly Layer[] = ['compute', 'network', 'dependencies'];

/** Healthy containers on one engine above this fold into a group. */
export const FOLD = 12;

/** Geometry, in CSS pixels. */
export const SLOT_W = 136;
export const ROW_H = 96;
export const NODE_W = 120;
export const NODE_H = 40;
export const PAD = 24;

/** Each kind's layer and row. Kinds not here (integrations, checks, cameras) aren't drawn. */
const KINDS: Record<string, { layer: Layer; row: number }> = {
  router: { layer: 'network', row: 0 },
  wan: { layer: 'network', row: 1 },
  host: { layer: 'compute', row: 1 },
  vm: { layer: 'compute', row: 2 },
  lxc: { layer: 'compute', row: 2 },
  array: { layer: 'storage', row: 2 },
  pool: { layer: 'storage', row: 2 },
  storage: { layer: 'storage', row: 2 },
  engine: { layer: 'compute', row: 3 },
  share: { layer: 'storage', row: 3 },
  disk: { layer: 'storage', row: 3 },
  backup_job: { layer: 'backups', row: 3 },
  container: { layer: 'compute', row: 4 },
  backup_target: { layer: 'storage', row: 4 },
  group: { layer: 'compute', row: 4 },
  app: { layer: 'compute', row: 5 },
  route: { layer: 'network', row: 6 },
};

/** The relations a parent comes from, most telling first: a route hangs under where it goes, not its proxy. */
const PARENT_TYPES: readonly string[] = ['runs_on', 'part_of', 'stores_on'];
const ROUTE_PARENT_TYPES: readonly string[] = ['routes_to', 'part_of'];

export interface AtlasNode {
  id: string;
  kind: string;
  label: string;
  layer: Layer;
  /** Centre, in px. */
  x: number;
  y: number;
  row: number;
  health: HealthStatus;
  reason?: string;
  parent?: string;
  children: string[];
  /** A fold: the containers (and their apps) it stands for. */
  folded?: string[];
}

export interface AtlasEdge {
  key: string;
  from: string;
  to: string;
  type: string;
  layer: Layer;
}

export interface Atlas {
  nodes: Map<string, AtlasNode>;
  /** Depth first, roots left to right: the list's order. */
  order: string[];
  roots: string[];
  edges: AtlasEdge[];
  width: number;
  height: number;
  /** Where each folded resource is drawn: its group's id. */
  foldedInto: Map<string, string>;
}

export interface AtlasOptions {
  layers: readonly Layer[];
  /** Engines whose fold is open. */
  expanded?: ReadonlySet<string>;
  fold?: number;
}

const edgeLayer = (type: string, from: string, to: string): Layer | undefined => {
  const lf = KINDS[from]?.layer;
  const lt = KINDS[to]?.layer;
  switch (type) {
    case 'depends_on':
      return 'dependencies';
    case 'provides_network_for':
    case 'routes_to':
      return 'network';
    case 'backed_up_by':
      return 'backups';
    case 'stores_on':
      return lf === 'backups' || lt === 'backups' ? 'backups' : 'storage';
    case 'runs_on':
    case 'part_of':
      if (lf === 'storage' || lt === 'storage') return 'storage';
      if (lf === 'network' && lt === 'network') return 'network';
      return 'compute';
  }
  return undefined;
};

const OK = (h: HealthStatus) => h === 'ok' || h === 'unknown';
const byLabel = (a: { label: string; id: string }, b: { label: string; id: string }) => a.label.localeCompare(b.label) || a.id.localeCompare(b.id);

/** The Atlas of g with these layers on. */
export function buildAtlas(g: GraphDoc | undefined, opts: AtlasOptions): Atlas {
  const on = new Set(opts.layers);
  const fold = opts.fold ?? FOLD;
  const nodes = new Map<string, AtlasNode>();
  const foldedInto = new Map<string, string>();
  const empty: Atlas = { nodes, order: [], roots: [], edges: [], width: PAD * 2, height: PAD * 2, foldedInto };
  if (!g) return empty;

  // Primaries, and every resource's primary.
  const alias = aliases(g);
  const primary = (id: string): string => {
    if (!alias.has(id)) return id;
    for (const r of Object.values(g.relations)) if (r.type === 'same_as' && r.from === id) return r.to;
    return id;
  };
  const kindOf = (id: string) => g.resources[id]?.kind ?? '';

  // Relations between primaries, once each, sorted.
  const rels: { from: string; to: string; type: string }[] = [];
  const seen = new Set<string>();
  for (const r of Object.values(g.relations)) {
    if (r.type === 'same_as') continue;
    const from = primary(r.from);
    const to = primary(r.to);
    const key = `${from} ${r.type} ${to}`;
    if (from === to || seen.has(key) || !g.resources[from] || !g.resources[to]) continue;
    seen.add(key);
    rels.push({ from, to, type: r.type });
  }
  rels.sort((a, b) => a.from.localeCompare(b.from) || a.type.localeCompare(b.type) || a.to.localeCompare(b.to));

  // Which primaries show: their layer is on, or a shown depends_on touches them.
  const show = new Set<string>();
  for (const r of Object.values(g.resources)) {
    const k = KINDS[r.kind];
    if (k && !alias.has(r.id) && on.has(k.layer)) show.add(r.id);
  }
  if (on.has('dependencies')) {
    for (const r of rels) {
      if (r.type === 'depends_on' && KINDS[kindOf(r.from)] && KINDS[kindOf(r.to)]) {
        show.add(r.from);
        show.add(r.to);
      }
    }
  }

  const make = (id: string): AtlasNode => {
    const r = g.resources[id]!;
    const h = mergedHealth(g, id);
    const n: AtlasNode = { id, kind: r.kind, label: label(r), layer: KINDS[r.kind]!.layer, x: 0, y: 0, row: KINDS[r.kind]!.row, health: h.status, children: [] };
    if (h.reason) n.reason = h.reason;
    return n;
  };
  for (const id of show) nodes.set(id, make(id));

  // Parents: on a row above, by the most telling relation, then by id.
  const parentOf = new Map<string, string>();
  for (const step of [0, 1, 2]) {
    for (const r of rels) {
      const c = nodes.get(r.from);
      const p = nodes.get(r.to);
      if (!c || !p || parentOf.has(r.from) || p.row >= c.row) continue;
      if ((c.kind === 'route' ? ROUTE_PARENT_TYPES : PARENT_TYPES)[step] === r.type) parentOf.set(r.from, r.to);
    }
  }
  // A backup job hangs under the first guest it backs up.
  for (const r of rels) {
    if (r.type !== 'backed_up_by' || parentOf.has(r.to)) continue;
    const c = nodes.get(r.to);
    const p = nodes.get(r.from);
    if (c && p && p.row < c.row) parentOf.set(r.to, r.from);
  }

  // Folds: an engine's containers whose whole subtree (apps, routes) is healthy.
  const containersOn = new Map<string, string[]>();
  for (const [child, parent] of parentOf) {
    if (nodes.get(child)?.kind === 'container' && nodes.get(parent)?.kind === 'engine') {
      containersOn.set(parent, [...(containersOn.get(parent) ?? []), child]);
    }
  }
  for (const [engine, cts] of [...containersOn].sort(([a], [b]) => a.localeCompare(b))) {
    if (opts.expanded?.has(engine)) continue;
    const subtree = (id: string): string[] => [id, ...[...parentOf].filter(([, p]) => p === id).flatMap(([c]) => subtree(c))];
    const quiet = cts.map(subtree).filter((ids) => ids.every((id) => OK(nodes.get(id)!.health)));
    if (cts.length <= fold || quiet.length < 2) continue;
    const gid = `group:${engine}`;
    const members = quiet.flat().sort();
    for (const m of members) {
      nodes.delete(m);
      parentOf.delete(m);
      foldedInto.set(m, gid);
    }
    nodes.set(gid, { id: gid, kind: 'group', label: `${quiet.length} more containers`, layer: 'compute', x: 0, y: 0, row: KINDS['group']!.row, health: 'ok', children: [], folded: members });
    parentOf.set(gid, engine);
  }

  // The forest.
  for (const [c, p] of parentOf) if (nodes.has(c) && nodes.has(p)) nodes.get(c)!.parent = p;
  for (const n of nodes.values()) if (n.parent) nodes.get(n.parent)!.children.push(n.id);
  const sortIds = (ids: string[]) => ids.sort((a, b) => nodes.get(a)!.row - nodes.get(b)!.row || byLabel(nodes.get(a)!, nodes.get(b)!));
  for (const n of nodes.values()) sortIds(n.children);
  const roots = sortIds([...nodes.values()].filter((n) => !n.parent).map((n) => n.id));
  roots.sort((a, b) => Number(nodes.get(b)!.children.length > 0) - Number(nodes.get(a)!.children.length > 0));

  // Rows close up.
  const rows = [...new Set([...nodes.values()].map((n) => n.row))].sort((a, b) => a - b);
  const rowIndex = new Map(rows.map((r, i) => [r, i]));

  // Tidy columns: a subtree is as wide as its leaves; a parent sits over its children.
  const order: string[] = [];
  const width = new Map<string, number>();
  const measure = (id: string): number => {
    const kids = nodes.get(id)!.children;
    const w = kids.length ? kids.reduce((s, k) => s + measure(k), 0) : 1;
    width.set(id, w);
    return w;
  };
  const place = (id: string, left: number) => {
    const n = nodes.get(id)!;
    order.push(id);
    n.x = PAD + (left + width.get(id)! / 2) * SLOT_W;
    n.y = PAD + (rowIndex.get(n.row)! + 0.5) * ROW_H;
    let at = left;
    for (const k of n.children) {
      place(k, at);
      at += width.get(k)!;
    }
  };
  let left = 0;
  for (const r of roots) {
    measure(r);
    place(r, left);
    left += width.get(r)!;
  }

  // Edges whose layer is on, between drawn nodes (a folded end draws at its group).
  const drawnAt = (id: string) => (nodes.has(id) ? id : foldedInto.get(id));
  const edges: AtlasEdge[] = [];
  const drawn = new Set<string>();
  for (const r of rels) {
    const layer = edgeLayer(r.type, kindOf(r.from), kindOf(r.to));
    const from = drawnAt(r.from);
    const to = drawnAt(r.to);
    if (!layer || !on.has(layer) || !from || !to || from === to) continue;
    const key = `${from} ${r.type} ${to}`;
    if (drawn.has(key)) continue;
    drawn.add(key);
    edges.push({ key, from, to, type: r.type, layer });
  }

  return { nodes, order, roots, edges, width: PAD * 2 + Math.max(1, left) * SLOT_W, height: PAD * 2 + Math.max(1, rows.length) * ROW_H, foldedInto };
}

/** The nodes and edges a cause's path lights, as drawn (a folded end lights its group). */
export function lit(atlas: Atlas, symptom: string, path: readonly CauseStep[] | undefined): { nodes: Set<string>; edges: Set<string> } {
  const at = (id: string) => (atlas.nodes.has(id) ? id : atlas.foldedInto.get(id));
  const nodes = new Set<string>();
  const edges = new Set<string>();
  const s = at(symptom);
  if (s) nodes.add(s);
  for (const st of path ?? []) {
    const from = at(st.from);
    const to = at(st.to);
    if (from) nodes.add(from);
    if (to) nodes.add(to);
    if (from && to) edges.add(`${from} ${st.type} ${to}`);
  }
  return { nodes, edges };
}

export type Direction = 'up' | 'down' | 'left' | 'right';

/**
 * Where an arrow key goes from id: up to its parent, down to its middle
 * child, left and right along its row. Without a parent or children, the
 * nearest node on the row above or below.
 */
export function neighbour(atlas: Atlas, id: string, dir: Direction): string | undefined {
  const n = atlas.nodes.get(id);
  if (!n) return atlas.order[0];
  const all = [...atlas.nodes.values()];
  const nearest = (cands: AtlasNode[]) => cands.sort((a, b) => Math.abs(a.x - n.x) - Math.abs(b.x - n.x) || a.x - b.x)[0]?.id;
  switch (dir) {
    case 'up':
      if (n.parent) return n.parent;
      return nearest(all.filter((m) => m.y < n.y && m.y === Math.max(...all.filter((o) => o.y < n.y).map((o) => o.y))));
    case 'down':
      if (n.children.length) return n.children[Math.floor((n.children.length - 1) / 2)];
      return nearest(all.filter((m) => m.y > n.y && m.y === Math.min(...all.filter((o) => o.y > n.y).map((o) => o.y))));
    case 'left':
    case 'right': {
      const row = all.filter((m) => m.y === n.y).sort((a, b) => a.x - b.x);
      const i = row.findIndex((m) => m.id === id);
      return row[i + (dir === 'left' ? -1 : 1)]?.id;
    }
  }
}

/** A node's kind in words, for the list and labels. */
export const KIND_WORDS: Record<string, string> = {
  router: 'router', wan: 'WAN', host: 'host', vm: 'VM', lxc: 'LXC', engine: 'Docker engine', container: 'container',
  app: 'service', route: 'route', array: 'array', pool: 'pool', storage: 'storage', share: 'share', disk: 'disk',
  backup_job: 'backup job', backup_target: 'backups', group: 'group',
};
