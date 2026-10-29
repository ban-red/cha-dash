/**
 * The Review sheet's model (docs/plan/11-import-and-discovery.md): every
 * source's dry run turned into rows to tick, grouped by what happens, and
 * the choices that go back to the hub.
 *
 *   POST /api/import/{source}?dry_run=1 {files, names?, choices?} → importer.Result (+ plan_hash)
 *   POST /api/import/{source} {files, names, choices, plan_hash} → {import_id, board_id, instance_ids, changes}
 *                                                                  409 plan_changed {error, result}
 *   POST /api/import/detect {files: [{name, head, base64?}]} → {source, name, files, unused, missing, roles, message}
 *
 * The sheet shows rows from the dry run without choices (so a row it left
 * out can be ticked again) and counts, the board and the hash from a dry
 * run with them. Values never appear: secrets are field names and counts.
 */
import { previewOf, type ImportPreview } from '../model/importPlan';

export type SourceId = 'homepage' | 'homarr' | 'yaml' | 'discovery';

export interface Role {
  key: string;
  file: string;
  required: boolean;
  binary: boolean;
  what: string;
}

export interface SourceInfo {
  id: SourceId;
  name: string;
  /** The largest request the hub takes for it, in bytes, as sent. */
  maxBytes: number;
  roles: Role[];
  where: string;
}

export const SOURCES: readonly SourceInfo[] = [
  {
    id: 'homepage',
    name: 'Homepage',
    maxBytes: 1 << 20,
    where: 'homepage › config › services.yaml, docker.yaml, settings.yaml',
    roles: [
      { key: 'services_yaml', file: 'services.yaml', required: true, binary: false, what: 'Services, groups and their widgets' },
      { key: 'docker_yaml', file: 'docker.yaml', required: false, binary: false, what: 'Docker hosts the services refer to' },
      { key: 'settings_yaml', file: 'settings.yaml', required: false, binary: false, what: 'Title, layout and provider keys' },
    ],
  },
  {
    id: 'homarr',
    name: 'Homarr',
    maxBytes: 24 << 20,
    where: 'Homarr 2 › Management › Tools › Backup (a .zip)',
    roles: [{ key: 'backup_zip', file: 'homarr-backup.zip', required: true, binary: true, what: 'The backup zip, with its db.sqlite' }],
  },
  {
    id: 'yaml',
    name: 'Plain YAML',
    maxBytes: 1 << 20,
    where: 'A board you write by hand (kind: Board)',
    roles: [{ key: 'board_yaml', file: 'board.yaml', required: true, binary: false, what: 'Sections, tiles and their connections' }],
  },
];

/** Sources no file is dropped for: the Discover tab's leads. */
const HIDDEN: readonly SourceInfo[] = [
  {
    id: 'discovery',
    name: 'Discovery',
    maxBytes: 256 << 10,
    where: 'Settings › Bring in › Discover',
    roles: [{ key: 'leads_json', file: 'leads.json', required: true, binary: false, what: 'The leads you picked' }],
  },
];

export const sourceInfo = (id: string): SourceInfo | undefined => SOURCES.find((s) => s.id === id) ?? HIDDEN.find((s) => s.id === id);

/** One file as the browser holds it: text, or base64 for a binary role. */
export interface HeldFile {
  name: string;
  /** Text, or base64 when binary. */
  data: string;
  binary: boolean;
  bytes: number;
}

export interface Detected {
  source: string;
  name?: string;
  files: Record<string, string>;
  unused: string[];
  missing: string[];
  message?: string;
}

export type Change = 'create' | 'update' | 'unchanged' | 'stale';

export interface ConnectionRow {
  object: string;
  id: string;
  integration: string;
  name: string;
  url?: string;
  change: Change;
  secretFields: string[];
  missing: string[];
  /** An instance the hub has that this resembles: same URL, or the name this one had to give up. */
  existing?: { id: string; match: 'url' | 'name' };
  origin: string;
}

export interface TileRow {
  object: string;
  id: string;
  name: string;
  widget: string;
  section: string;
  /** Connections it reads: leaving one out leaves the tile out. */
  needs: string[];
  check?: string;
}

export interface Review {
  source: string;
  boardName: string;
  connections: ConnectionRow[];
  tiles: TileRow[];
  /** The rest, read-only, from the shared preview model. */
  preview: ImportPreview;
  planHash: string;
}

type O = Record<string, unknown>;
const obj = (v: unknown): O => (typeof v === 'object' && v !== null && !Array.isArray(v) ? (v as O) : {});
const arr = (v: unknown): unknown[] => (Array.isArray(v) ? v : []);
const str = (v: unknown): string => (typeof v === 'string' ? v : '');
const strs = (v: unknown): string[] => arr(v).filter((x): x is string => typeof x === 'string');

export function reviewOf(res: unknown): Review {
  const r = obj(res);
  const plan = obj(r['plan']);
  const changes = new Map<string, O>();
  for (const c0 of arr(plan['changes'])) {
    const c = obj(c0);
    if (c['kind'] === 'instance') changes.set(str(c['id']), c);
  }
  const connections: ConnectionRow[] = arr(plan['instances']).map((x0) => {
    const x = obj(x0);
    const cfg = obj(x['config']);
    const id = str(x['id']);
    const ch = changes.get(id) ?? {};
    const origin = obj(x['origin']);
    const row: ConnectionRow = {
      object: `instance:${id}`,
      id,
      integration: str(x['integration']),
      name: str(ch['name']) || str(x['name']),
      change: (str(ch['action']) || 'create') as Change,
      secretFields: strs(x['secret_fields']),
      missing: strs(x['missing_secrets']),
      origin: [str(origin['group']), str(origin['entry'])].filter(Boolean).join(' › ') || str(origin['file']),
    };
    const url = str(cfg['url']) || str(cfg['endpoint']);
    if (url && !url.startsWith('${')) row.url = url;
    if (ch['existing']) row.existing = { id: str(ch['existing']), match: ch['match'] === 'name' ? 'name' : 'url' };
    return row;
  });
  const tiles: TileRow[] = arr(plan['tiles']).map((t0) => {
    const t = obj(t0);
    const needs = [str(t['instance']), ...arr(t['sources']).map((s) => str(obj(s)['instance']))].filter(Boolean).map((i) => `instance:${i}`);
    const row: TileRow = {
      object: `tile:${str(t['item_id'])}`,
      id: str(t['item_id']),
      name: str(t['name']) || str(t['item_id']),
      widget: str(t['widget']),
      section: str(t['section']),
      needs: [...new Set(needs)],
    };
    if (str(t['check'])) row.check = str(t['check']);
    return row;
  });
  const preview = previewOf(res);
  return { source: str(plan['source']), boardName: preview.boardName, connections, tiles, preview, planHash: str(r['plan_hash']) };
}

/** What the person decided, sent back as the hub's Choices. */
export interface Choices {
  skip: Set<string>;
  target: string;
  useExisting: Map<string, string>;
  /** Typed values by instance and field. Kept in memory only, never logged. */
  secrets: Map<string, Record<string, string>>;
}

export const emptyChoices = (): Choices => ({ skip: new Set(), target: 'new', useExisting: new Map(), secrets: new Map() });

export function choicesBody(c: Choices): Record<string, unknown> {
  const out: Record<string, unknown> = {};
  if (c.skip.size) out['skip'] = [...c.skip].sort();
  if (c.target && c.target !== 'new') out['target'] = c.target;
  if (c.useExisting.size) out['use_existing'] = Object.fromEntries(c.useExisting);
  const secrets: Record<string, Record<string, string>> = {};
  for (const [id, fields] of c.secrets) {
    const set = Object.fromEntries(Object.entries(fields).filter(([, v]) => v !== ''));
    if (Object.keys(set).length && !c.skip.has(`instance:${id}`)) secrets[id] = set;
  }
  if (Object.keys(secrets).length) out['secrets'] = secrets;
  return out;
}

/** Whether a row is in: not left out, and nothing it needs is left out (or reused instead). */
export function included(r: Review, c: Choices, object: string): boolean {
  if (c.skip.has(object)) return false;
  const t = r.tiles.find((x) => x.object === object);
  if (!t) return true;
  return t.needs.every((n) => !c.skip.has(n));
}

/**
 * Ticks or unticks a row. Leaving a connection out leaves out what needs it
 * (the hub does the same); ticking a tile ticks the connections it needs.
 * Returns the names of the other rows that moved with it, for the live
 * region.
 */
export function setIncluded(r: Review, c: Choices, object: string, on: boolean): string[] {
  const moved: string[] = [];
  if (on) {
    c.skip.delete(object);
    const t = r.tiles.find((x) => x.object === object);
    for (const n of t?.needs ?? []) {
      if (c.skip.delete(n)) moved.push(r.connections.find((x) => x.object === n)?.name ?? n);
    }
    if (object.startsWith('instance:')) {
      for (const tile of r.tiles) if (tile.needs.includes(object) && c.skip.has(tile.object) === false) moved.push(tile.name);
    }
  } else {
    c.skip.add(object);
    if (object.startsWith('instance:')) {
      for (const tile of r.tiles) if (tile.needs.length === 1 && tile.needs[0] === object) moved.push(tile.name);
    }
  }
  return moved;
}

/** Group checkbox state: all, none or some of the rows in. */
export function groupState(r: Review, c: Choices, objects: string[]): 'all' | 'none' | 'some' {
  const n = objects.filter((o) => included(r, c, o)).length;
  return n === 0 ? 'none' : n === objects.length ? 'all' : 'some';
}

/** Counts for the summary line, from the rows and the choices. */
export function counts(r: Review, c: Choices): { connections: number; tiles: number; secrets: number; keysNeeded: number; total: number; of: number } {
  const conns = r.connections.filter((x) => included(r, c, x.object) && !c.useExisting.has(x.id));
  const tiles = c.target === 'none' ? [] : r.tiles.filter((t) => included(r, c, t.object));
  let secrets = 0;
  let keysNeeded = 0;
  for (const x of conns) {
    const typed = c.secrets.get(x.id) ?? {};
    secrets += x.secretFields.length + x.missing.filter((f) => typed[f]).length;
    if (x.missing.some((f) => !typed[f])) keysNeeded++;
  }
  const selectable = r.connections.length + r.tiles.length;
  return { connections: conns.length, tiles: tiles.length, secrets, keysNeeded, total: conns.length + tiles.length, of: selectable };
}

/** The first bytes the hub needs to name a file's source. */
export const HEAD_BYTES = 64 * 1024;

export async function holdFile(file: File): Promise<HeldFile> {
  const binary = /\.zip$/i.test(file.name);
  if (binary) {
    const buf = new Uint8Array(await file.arrayBuffer());
    return { name: file.name, data: base64(buf), binary, bytes: file.size };
  }
  return { name: file.name, data: await file.text(), binary, bytes: file.size };
}

/** The detect request's view of a held file. */
export function headOf(f: HeldFile): { name: string; head: string; base64?: boolean } {
  if (f.binary) return { name: f.name, head: f.data.slice(0, 5464), base64: true }; // 4 KiB of bytes
  return { name: f.name, head: f.data.slice(0, HEAD_BYTES) };
}

export function base64(bytes: Uint8Array): string {
  let s = '';
  const step = 0x8000;
  for (let i = 0; i < bytes.length; i += step) s += String.fromCharCode(...bytes.subarray(i, i + step));
  return btoa(s);
}

export function sizeWords(b: number): string {
  if (b < 1024) return `${b} B`;
  if (b < 1024 * 1024) return `${(b / 1024).toFixed(b < 10_240 ? 1 : 0)} kB`;
  return `${(b / 1024 / 1024).toFixed(1)} MB`;
}
