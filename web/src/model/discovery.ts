/**
 * Label discovery: containers that carry Homepage's `homepage.group` and
 * `homepage.name` labels become suggested `service` tiles in the section
 * whose title matches the group (integrations/docker/README.md → "Label
 * discovery"). Suggested, never added silently: the Inbox asks, and adding
 * them is one action. Pure functions of the graph, the board and its tiles.
 *
 * Containers with the same group and name (one service seen by two sources,
 * until the correlator merges them) are one suggestion. It's offered unless
 * the board already shows one of them (a tile bound to it), a tile of the
 * same name sits in that section already (the import brought it from
 * services.yaml), or one of them was dismissed.
 *
 * A container that is a known integration (catalogMatch.ts: its Homepage
 * widget label or its image) is suggested as that integration's catalog
 * widget, bound to its app (`arr@sonarr:<instance>/app`, `plex`, or the
 * service tile on the app for the per-integration stat), when the graph has
 * that app: the one that `runs_on` the container, else the only one. It's
 * not offered when the board already shows that app. Without the app, it's a
 * service tile on the container, and `known` says which integration to
 * connect for the widget.
 */
import { addSection, smartInsert, type Board } from '@chadash/layout';
import { WIDGETS, itemId, parseItemId } from './widgets';
import { knownIntegrationOf } from './catalogMatch';
import { appsOfType, outgoing } from './graph';
import type { GraphDoc, Resource } from './types';
import type { BoardState } from './edit';
import { hrefOk, withTile, type TileConfig, type TilesDoc } from '../stores/tiles';

export interface LabelService {
  /** The container's resource id (the first, by id, of those with this group and name). */
  resource: string;
  /** The other containers with the same group and name: dismissed along with it. */
  duplicates: string[];
  name: string;
  group: string;
  href?: string;
  icon?: string;
  description?: string;
  /** `homepage.weight`: the order within the section. */
  weight: number;
  /** The section it goes in: an existing one's id, or the one to create. */
  sectionId: string;
  sectionTitle: string;
  newSection: boolean;
  /** The integration the container is, when it's a known one. */
  known?: { id: string; name: string };
  /** The catalog widget to add instead of a service tile: its type, item id and the app it shows. */
  widget?: { type: string; id: string; app: string };
}

/** Same as the importer's section slugs: `Archive/Backup` → `archive-backup`. */
export function slug(s: string): string {
  return s
    .toLowerCase()
    .replace(/[^a-z0-9]+/g, '-')
    .replace(/^-+|-+$/g, '')
    .slice(0, 48)
    .replace(/-+$/, '');
}

const SPAN = 4; // a Homepage group without layout hints, as the importer makes it

const labelOf = (r: Resource, key: string): string => (r.labels?.[`homepage.${key}`] ?? '').trim();

/** The containers whose labels name this group and service (to dismiss them with a tile that shows their app). */
export function labelContainers(g: GraphDoc | undefined, group: string, name: string): string[] {
  if (!g || !group || !name) return [];
  return Object.values(g.resources)
    .filter((r) => r.kind === 'container' && labelOf(r, 'group').toLowerCase() === group.toLowerCase() && labelOf(r, 'name').toLowerCase() === name.toLowerCase())
    .map((r) => r.id)
    .sort();
}

/** The app a known container is: the one that runs_on one of the containers, else the only one of its type. */
function appFor(g: GraphDoc, type: string, containers: readonly Resource[]): Resource | undefined {
  const apps = appsOfType(g, type);
  const ids = new Set(containers.map((c) => c.id));
  return apps.find((a) => outgoing(g, a.id, 'runs_on').some((c) => ids.has(c.id))) ?? (apps.length === 1 ? apps[0] : undefined);
}

/** The catalog widget for a known integration's app. */
function widgetFor(tile: string, app: string): { type: string; id: string; app: string } {
  const spec = WIDGETS[tile];
  if (!spec) return { type: 'service', id: itemId('service', app), app };
  return { type: tile, id: tile === 'service' || spec.bound ? itemId(tile, app) : itemId(tile), app };
}

export const discoveredItemId = (resource: string): string => itemId('service', resource);

/** The containers the board could show, in board order (then weight, then name). */
export function labelServices(g: GraphDoc | undefined, board: Board | undefined, tiles: TilesDoc, dismissed: ReadonlySet<string>): LabelService[] {
  if (!g || !board) return [];
  const onBoard = new Set<string>();
  const itemIds = new Set<string>();
  const namesIn = new Map<string, Set<string>>();
  for (const s of board.sections) {
    const names = new Set<string>();
    for (const i of s.items) {
      itemIds.add(i.id);
      const t = parseItemId(i.id).target;
      if (t) onBoard.add(t);
      const c = tiles[i.id];
      const bound = c?.bind?.resource;
      if (bound) onBoard.add(bound);
      if (i.type === 'service' && i.id.startsWith('service@')) onBoard.add(i.id.slice('service@'.length));
      if (c?.name) names.add(c.name.trim().toLowerCase());
    }
    namesIn.set(s.id, names);
  }
  const byTitle = new Map<string, { id: string; title: string }>();
  for (const s of board.sections) {
    const t = (s.title ?? '').trim().toLowerCase();
    if (t && !byTitle.has(t)) byTitle.set(t, { id: s.id, title: s.title! });
  }
  const sectionIds = new Set(board.sections.map((s) => s.id));
  const created = new Map<string, string>(); // group (lower) → new section id
  const out: LabelService[] = [];
  // One entry per group and name, its containers in id order.
  const services = new Map<string, Resource[]>();
  const containers = Object.values(g.resources)
    .filter((r) => r.kind === 'container' && labelOf(r, 'group') && labelOf(r, 'name'))
    .sort((a, b) => a.id.localeCompare(b.id));
  for (const r of containers) {
    const key = `${labelOf(r, 'group').toLowerCase()}\u0000${labelOf(r, 'name').toLowerCase()}`;
    services.set(key, [...(services.get(key) ?? []), r]);
  }
  for (const [first, ...rest] of services.values()) {
    const r = first!;
    if ([r, ...rest].some((c) => onBoard.has(c.id) || dismissed.has(c.id))) continue;
    const group = labelOf(r, 'group');
    const name = labelOf(r, 'name');
    const known = [r, ...rest].map(knownIntegrationOf).find(Boolean);
    const app = known ? appFor(g, known.id, [r, ...rest]) : undefined;
    const widget = known && app ? widgetFor(known.tile, app.id) : undefined;
    if (widget && (onBoard.has(widget.app) || itemIds.has(widget.id))) continue; // the board shows it already

    const g0 = group.toLowerCase();
    const existing = byTitle.get(g0) ?? (sectionIds.has(slug(group)) ? { id: slug(group), title: group } : undefined);
    let sectionId: string;
    let newSection = false;
    if (existing) {
      sectionId = existing.id;
      if (namesIn.get(sectionId)?.has(name.toLowerCase())) continue;
    } else {
      newSection = true;
      sectionId = created.get(g0) ?? '';
      if (!sectionId) {
        const base = slug(group) || 'section';
        sectionId = base;
        for (let n = 2; sectionIds.has(sectionId) || [...created.values()].includes(sectionId); n++) sectionId = `${base}-${n}`;
        created.set(g0, sectionId);
      }
    }
    const s: LabelService = {
      resource: r.id,
      duplicates: rest.map((c) => c.id),
      name,
      group,
      weight: Number.parseInt(labelOf(r, 'weight'), 10),
      sectionId,
      sectionTitle: existing?.title ?? group,
      newSection,
    };
    if (!Number.isFinite(s.weight)) s.weight = 0;
    if (known) s.known = { id: known.id, name: known.name };
    if (widget) s.widget = widget;
    const href = labelOf(r, 'href');
    if (hrefOk(href)) s.href = href;
    const icon = labelOf(r, 'icon');
    if (icon) s.icon = icon;
    const description = labelOf(r, 'description');
    if (description) s.description = description;
    out.push(s);
  }
  const order = new Map(board.sections.map((s, i) => [s.id, i]));
  return out.sort(
    (a, b) =>
      (order.get(a.sectionId) ?? board.sections.length) - (order.get(b.sectionId) ?? board.sections.length) ||
      a.sectionId.localeCompare(b.sectionId) ||
      a.weight - b.weight ||
      a.name.localeCompare(b.name),
  );
}

/** The tile config a discovered service starts with. */
export function labelTileConfig(s: LabelService): TileConfig {
  const origin = { source: 'labels', group: s.group, entry: s.name };
  // A catalog widget's item id names the app; a dedicated widget keeps its own name.
  if (s.widget && s.widget.type !== 'service') return s.href ? { href: s.href, origin } : { origin };
  const c: TileConfig = s.widget ? { name: s.name, origin } : { name: s.name, bind: { resource: s.resource }, origin };
  if (s.href) c.href = s.href;
  if (s.icon) c.icon = s.icon;
  if (s.description) c.description = s.description;
  return c;
}

/**
 * Add discovered services to the board: each into its section (a new one at
 * the end for a group no section matches), in the first gap where it fits.
 * Services that fit nowhere are left out of `added`.
 */
export function addLabelServices(state: BoardState, services: readonly LabelService[]): BoardState & { added: LabelService[] } {
  let board = state.board;
  let tiles = state.tiles;
  const added: LabelService[] = [];
  for (const s of services) {
    if (!board.sections.some((sec) => sec.id === s.sectionId)) {
      board = addSection(board, { id: s.sectionId, title: s.sectionTitle, span: SPAN, items: [] });
    }
    const spec = WIDGETS[s.widget?.type ?? 'service']!;
    const id = s.widget?.id ?? discoveredItemId(s.resource);
    if (board.sections.some((sec) => sec.items.some((i) => i.id === id))) continue;
    // Its default size, else the smallest that fits.
    let placed = false;
    for (const size of [spec.size, ...spec.sizes.filter((x) => x !== spec.size)]) {
      try {
        board = smartInsert(board, { id, type: spec.type, size, supportedSizes: spec.sizes }, { sectionId: s.sectionId });
        placed = true;
        break;
      } catch {
        /* a section too narrow for this size */
      }
    }
    if (!placed) continue;
    tiles = withTile(tiles, id, labelTileConfig(s));
    added.push(s);
  }
  return { board, tiles, added };
}

const list = (names: string[]): string =>
  names.length <= 1 ? (names[0] ?? '') : `${names.slice(0, -1).join(', ')} and ${names[names.length - 1]}`;

/** "Immich and Mealie in Apps; Proxmox Backup Server in a new Backup section." */
export function placementSentence(services: readonly LabelService[]): string {
  const groups = new Map<string, { title: string; isNew: boolean; names: string[] }>();
  for (const s of services) {
    const g = groups.get(s.sectionId) ?? { title: s.sectionTitle, isNew: s.newSection, names: [] };
    g.names.push(s.name);
    groups.set(s.sectionId, g);
  }
  return `${[...groups.values()].map((g) => `${list(g.names)} in ${g.isNew ? `a new ${g.title} section` : g.title}`).join('; ')}.`;
}
