/**
 * The default board, built from what the Home Graph contains. The hub stores
 * `layout: null` for a new board; the SPA builds this with @chadash/layout
 * and PUTs it back (see stores/board.ts).
 *
 * Desktop, 12 columns:
 *
 *   Home (4)            │ Infrastructure (8)
 *   cameras    large    │ fleet large · OPNsense wide · backups · containers
 *   Home Asst. wide     │ host wide · Traefik · Uptime Kuma
 *   ────────────────────┼─────────────────────────────
 *   Media (8)                                │ Activity (4)
 *   Now playing wide · Sonarr · Radarr       │ incidents large
 *   downloads wide · up next wide            │
 *
 * Without a graph (realtime not up yet) it falls back to the singleton
 * widgets, so the first paint is never empty.
 */
import { createBoard, type Board, type ItemInput, type SectionInput } from '@chadash/layout';
import { WIDGETS, itemId } from './widgets';
import { appsOfType, byKind, calendar, resolve, resources } from './graph';
import type { GraphDoc } from './types';

function item(type: string, target?: string): ItemInput {
  const w = WIDGETS[type];
  if (!w) throw new Error(`unknown widget type ${type}`);
  return { id: itemId(type, target), type, size: w.size, supportedSizes: w.sizes };
}

export function defaultBoard(g: GraphDoc | undefined, nowMs = Date.now()): Board {
  const has = (id: string) => Boolean(resolve(g, id));
  const kinds = new Set(resources(g).map((r) => r.kind));
  const any = !g; // no graph: show every singleton
  const sections: SectionInput[] = [];

  const home: ItemInput[] = [];
  if (any || kinds.has('camera')) home.push(item('cameras'));
  if (any || has('app:homeassistant')) home.push(item('homeassistant'));
  if (home.length) sections.push({ id: 'home', title: 'Home', span: 4, items: home });

  const infra: ItemInput[] = [];
  if (any || kinds.has('host')) infra.push(item('fleet'));
  if (any || kinds.has('router')) infra.push(item('opnsense'));
  if (any || kinds.has('backup_target')) infra.push(item('backups'));
  if (any || kinds.has('engine')) infra.push(item('containers'));
  const hosts = byKind(g, 'host');
  const featured = hosts.find((h) => h.attrs?.['platform'] === 'unraid') ?? hosts[0];
  if (featured) infra.push(item('host', featured.id));
  if (any || has('app:traefik')) infra.push(item('traefik'));
  if (any || has('app:uptimekuma')) infra.push(item('uptimekuma'));
  if (infra.length) sections.push({ id: 'infra', title: 'Infrastructure', span: 8, items: infra });

  const media: ItemInput[] = [];
  if (any || has('app:plex')) media.push(item('plex'));
  for (const type of ['sonarr', 'radarr']) for (const app of appsOfType(g, type)) media.push(item('arr', app.id));
  if (any || has('app:qbittorrent')) media.push(item('downloads'));
  if (any || calendar(g, nowMs - 7 * 86_400_000).length > 0) media.push(item('calendar'));
  if (media.length) sections.push({ id: 'media', title: 'Media', span: 8, items: media });

  sections.push({ id: 'activity', title: 'Activity', span: 4, items: [item('incidents')] });

  return createBoard({ sections });
}
