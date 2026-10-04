/**
 * The Homepage importer's dry run, as something to show (internal/importer,
 * 07 · v0.1 → Import):
 *
 *   POST /api/import/homepage?dry_run=1 {services_yaml, docker_yaml, settings_yaml}
 *     → {plan, hints, unmapped}
 *   POST /api/import/homepage (same body) → {board_id, instance_ids, changes}
 *
 * plan: {source, board {id, name, layout}, tiles [{item_id, section, widget,
 * name, href, instance, check, origin}], instances [{id, integration, name,
 * config, secret_fields, missing_secrets, origin}], secrets [{name, instance,
 * field}] (names only), secrets_moved, links, discovery [{kind, message}],
 * ignored [{file, key, entries, reason}], summary {counts…, notes},
 * changes [{kind, id, name, action: create|update|unchanged|stale}]}.
 * hints: [{code, severity: info|attention, inbox, message, subjects}].
 * unmapped: [{kind, origin {file, group, entry}, type, fields, reason}].
 *
 * Values never appear: secrets are field names and a count.
 */
import { parseItemId, WIDGETS } from './widgets';

export interface PreviewTile {
  id: string;
  label: string;
  widget: string;
  /** Watched by a check or read by an integration. */
  live: boolean;
}

export interface PreviewSection {
  id: string;
  title: string;
  tiles: PreviewTile[];
}

export interface PreviewInstance {
  id: string;
  integration: string;
  name: string;
  url?: string;
  secretFields: string[];
  /** Referenced but not in the files ({{HOMEPAGE_VAR_…}}): set them in Cha Dash. */
  missingSecrets: string[];
}

export interface PreviewNote {
  message: string;
  severity: string;
  code?: string;
  /** Becomes a quiet Inbox item after applying. */
  inbox: boolean;
}

export interface PreviewUnmapped {
  name: string;
  kind: string;
  where?: string;
  reason?: string;
  fields: string[];
}

export interface PreviewIgnored {
  what: string;
  reason: string;
}

export interface ChangeCounts {
  create: number;
  update: number;
  unchanged: number;
  stale: number;
}

export interface ImportPreview {
  boardName: string;
  sections: PreviewSection[];
  tileCount: number;
  instances: PreviewInstance[];
  secretsMoved: number;
  checks: number;
  links: number;
  hints: PreviewNote[];
  unmapped: PreviewUnmapped[];
  ignored: PreviewIgnored[];
  /** What discovery will fill in later. */
  discovery: string[];
  notes: string[];
  changes: ChangeCounts;
  /** Stale instances: a previous import made them, the files no longer have them. Reported, never deleted. */
  stale: string[];
  /** Applying changes nothing (a re-import of the same files). */
  nothingNew: boolean;
}

export interface ImportFiles {
  services_yaml: string;
  docker_yaml: string;
  settings_yaml: string;
}

export interface ApplyResult {
  board_id: string;
  instance_ids?: string[];
  changes?: { kind: string; id: string; name?: string; action: string }[];
}

type O = Record<string, unknown>;
const obj = (v: unknown): O => (typeof v === 'object' && v !== null && !Array.isArray(v) ? (v as O) : {});
const arr = (v: unknown): unknown[] => (Array.isArray(v) ? v : []);
const str = (...vs: unknown[]): string | undefined => vs.find((v): v is string => typeof v === 'string' && v !== '');
const strs = (v: unknown): string[] => arr(v).filter((x): x is string => typeof x === 'string');
const num = (...vs: unknown[]): number | undefined => vs.find((v): v is number => typeof v === 'number');

/** "service@homepage:servers/proxmoxpluto" → "proxmoxpluto"; "clock" → "Clock". */
function idLabel(id: string): string {
  const { type, target } = parseItemId(id);
  if (target) return target.replace(/^[a-z_]+:/, '').split('/').pop() || target;
  return WIDGETS[type]?.name ?? type;
}

export function previewOf(res: unknown): ImportPreview {
  const r = obj(res);
  const plan = obj(r['plan']);
  const board = obj(plan['board']);
  const layout = obj(board['layout'] ?? plan['layout']);
  const summary = obj(plan['summary']);

  // Tile names come from plan.tiles; the layout only has ids, types and sizes.
  const tileInfo = new Map<string, O>();
  for (const t of arr(plan['tiles'])) {
    const o = obj(t);
    const id = str(o['item_id']);
    if (id) tileInfo.set(id, o);
  }
  const sections: PreviewSection[] = arr(layout['sections']).map((s0, i) => {
    const s = obj(s0);
    const tiles = arr(s['items']).map((t0) => {
      const t = obj(t0);
      const id = String(t['id'] ?? '');
      const info = tileInfo.get(id) ?? {};
      return {
        id,
        label: str(info['name'], t['title'], t['name']) ?? idLabel(id),
        widget: String(info['widget'] ?? t['type'] ?? ''),
        live: Boolean(str(info['check'], info['instance'])),
      };
    });
    return { id: String(s['id'] ?? `section-${i}`), title: str(s['title'], s['name']) ?? 'Untitled section', tiles };
  });

  const instances: PreviewInstance[] = arr(plan['instances']).map((x0) => {
    const x = obj(x0);
    const url = str(obj(x['config'])['url'], obj(x['config'])['endpoint']);
    const out: PreviewInstance = {
      id: String(x['id'] ?? ''),
      integration: String(x['integration'] ?? '?'),
      name: String(x['name'] ?? x['integration'] ?? '?'),
      secretFields: strs(x['secret_fields']),
      missingSecrets: strs(x['missing_secrets']),
    };
    if (url) out.url = url;
    return out;
  });
  const instanceName = new Map(instances.map((x) => [x.id, x.name]));

  const hints: PreviewNote[] = arr(r['hints']).map((h0) => {
    if (typeof h0 === 'string') return { message: h0, severity: 'info', inbox: false };
    const h = obj(h0);
    const note: PreviewNote = { message: str(h['message']) ?? String(h['code'] ?? ''), severity: str(h['severity']) ?? 'info', inbox: h['inbox'] === true };
    const code = str(h['code']);
    if (code) note.code = code;
    return note;
  });

  const unmapped: PreviewUnmapped[] = arr(r['unmapped']).map((u0) => {
    if (typeof u0 === 'string') return { name: u0, kind: 'entry', fields: [] };
    const u = obj(u0);
    const origin = obj(u['origin']);
    const out: PreviewUnmapped = {
      name: str(origin['entry'], origin['group'], u['type'], origin['file']) ?? '?',
      kind: str(u['kind']) ?? 'entry',
      fields: strs(u['fields']),
    };
    const where = [str(origin['file']), str(origin['group'])].filter(Boolean).join(' › ');
    if (where) out.where = where;
    const reason = str(u['reason']);
    if (reason) out.reason = reason;
    return out;
  });

  const ignored: PreviewIgnored[] = arr(plan['ignored']).map((g0) => {
    const g = obj(g0);
    const entries = strs(g['entries']);
    const what = `${str(g['file']) ?? ''}${g['key'] ? ` › ${String(g['key'])}` : ''}${entries.length ? ` (${entries.join(', ')})` : ''}`;
    return { what, reason: str(g['reason']) ?? '' };
  });

  const changes: ChangeCounts = { create: 0, update: 0, unchanged: 0, stale: 0 };
  const stale: string[] = [];
  for (const c0 of arr(plan['changes'])) {
    const c = obj(c0);
    const a = str(c['action']);
    if (a === 'create' || a === 'update' || a === 'unchanged' || a === 'stale') changes[a]++;
    if (a === 'stale') stale.push(str(c['name']) ?? instanceName.get(String(c['id'])) ?? String(c['id'] ?? ''));
  }

  const tileCount = num(summary['tiles']) ?? sections.reduce((n, s) => n + s.tiles.length, 0);
  return {
    boardName: str(board['name']) ?? 'Imported from Homepage',
    sections,
    tileCount,
    instances,
    secretsMoved: num(plan['secrets_moved'], summary['secrets_moved']) ?? arr(plan['secrets']).length,
    checks: num(summary['checks']) ?? 0,
    links: num(summary['links']) ?? arr(plan['links']).length,
    hints,
    unmapped,
    ignored,
    discovery: arr(plan['discovery']).map((d) => str(obj(d)['message'])).filter((m): m is string => Boolean(m)),
    notes: strs(summary['notes']),
    changes,
    stale,
    nothingNew: arr(plan['changes']).length > 0 && changes.create === 0 && changes.update === 0,
  };
}
