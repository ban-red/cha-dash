/**
 * A Home file import's dry run, as something to show (internal/export):
 *
 *   POST /api/import/chadash?dry_run=1 {yaml, passphrase?} → {plan}
 *   POST /api/import/chadash (same body) → {plan, changed, board_ids, instance_ids}
 *
 * plan: {format, exported_at, hub_version, changes [{kind: board|layout|tiles|
 * instance|setting, id, name, file_id?, integration?, action: create|update|
 * unchanged|stale, detail?, fields [{path, op: add|replace|remove|reorder,
 * from?, to?}], truncated?, secrets [{field, secret, action: create|update|
 * unchanged|needed, source?: bundle|vault, in_bundle?}]}], secrets_needed
 * [{instance, name, integration, field, secret, in_bundle?}], bundle {present,
 * unlocked, secrets}, summary {create, update, unchanged, stale,
 * secrets_restored, secrets_needed}, warnings [{code, message, kind?, id?}]}.
 *
 * Values in field changes are never secrets: the hub reports secrets by
 * field and vault name only.
 */

export type Action = 'create' | 'update' | 'unchanged' | 'stale';
export type Kind = 'board' | 'layout' | 'tiles' | 'instance' | 'setting';

export interface FieldRow {
  path: string;
  op: 'add' | 'replace' | 'remove' | 'reorder';
  /** One-line JSON, cut short. */
  from?: string;
  to?: string;
}

export interface SecretRow {
  field: string;
  action: 'create' | 'update' | 'unchanged' | 'needed';
  source?: 'bundle' | 'vault';
  inBundle: boolean;
}

export interface ObjectRow {
  key: string;
  kind: Kind;
  id: string;
  name: string;
  /** The board's ID in the file, when it matched one of this hub's by slug. */
  fileId?: string;
  integration?: string;
  action: Action;
  detail?: string;
  fields: FieldRow[];
  truncated: boolean;
  secrets: SecretRow[];
}

export interface KindSection {
  kind: Kind;
  title: string;
  /** Created, updated and stale objects, in the plan's order. */
  changed: ObjectRow[];
  /** Objects that stay as they are: named, not detailed. */
  unchanged: ObjectRow[];
}

export interface NeededRow {
  instance: string;
  name: string;
  integration: string;
  field: string;
  inBundle: boolean;
}

export interface BackupPreview {
  exportedAt?: string;
  hubVersion?: string;
  sections: KindSection[];
  needed: NeededRow[];
  bundle: { present: boolean; unlocked: boolean; secrets: number };
  summary: { create: number; update: number; unchanged: number; stale: number; secretsRestored: number; secretsNeeded: number };
  warnings: { code: string; message: string }[];
  /** Applying writes nothing: an export of this same hub, or a re-import. */
  nothingNew: boolean;
}

export interface BackupApplied {
  plan?: unknown;
  changed?: boolean;
  board_ids?: string[];
  instance_ids?: string[];
}

type O = Record<string, unknown>;
const obj = (v: unknown): O => (typeof v === 'object' && v !== null && !Array.isArray(v) ? (v as O) : {});
const arr = (v: unknown): unknown[] => (Array.isArray(v) ? v : []);
const str = (v: unknown): string | undefined => (typeof v === 'string' && v !== '' ? v : undefined);
const num = (v: unknown): number => (typeof v === 'number' && Number.isFinite(v) ? v : 0);

const KINDS: { kind: Kind; title: string }[] = [
  { kind: 'board', title: 'Boards' },
  { kind: 'layout', title: 'Layouts' },
  { kind: 'tiles', title: 'Tile settings' },
  { kind: 'instance', title: 'Connections' },
  { kind: 'setting', title: 'Settings' },
];
const ACTIONS: Action[] = ['create', 'update', 'unchanged', 'stale'];
const OPS: FieldRow['op'][] = ['add', 'replace', 'remove', 'reorder'];

/** A value as one line of JSON, cut to `max` characters. */
export function valueText(v: unknown, max = 72): string {
  let s: string;
  try {
    s = JSON.stringify(v) ?? 'null';
  } catch {
    s = String(v);
  }
  return s.length > max ? `${s.slice(0, max - 1)}…` : s;
}

/**
 * A field path for people: JSON Pointer tokens unescaped and joined with
 * " › ", the object's own prefix dropped. "/document/sections/media/items/
 * service@homepage:media~1sonarr/size" → "sections › media › items ›
 * service@homepage:media/sonarr › size".
 */
export function pathLabel(path: string): string {
  const parts = path
    .split('/')
    .slice(1)
    .map((p) => p.replace(/~1/g, '/').replace(/~0/g, '~'));
  if (parts[0] === 'document' && parts.length > 1) parts.shift();
  return parts.join(' › ') || '(whole)';
}

export function backupPreviewOf(res: unknown): BackupPreview {
  const plan = obj(obj(res)['plan'] ?? res);
  const sections = new Map<Kind, KindSection>(KINDS.map((k) => [k.kind, { ...k, changed: [], unchanged: [] }]));

  arr(plan['changes']).forEach((c0, i) => {
    const c = obj(c0);
    const kind = str(c['kind']) as Kind | undefined;
    const action = str(c['action']) as Action | undefined;
    const section = kind ? sections.get(kind) : undefined;
    if (!section || !action || !ACTIONS.includes(action)) return;
    const id = String(c['id'] ?? '');
    const row: ObjectRow = {
      key: `${kind}:${id}:${i}`,
      kind: section.kind,
      id,
      name: str(c['name']) ?? id,
      action,
      fields: arr(c['fields']).flatMap((f0): FieldRow[] => {
        const f = obj(f0);
        const op = str(f['op']) as FieldRow['op'] | undefined;
        if (!op || !OPS.includes(op)) return [];
        const out: FieldRow = { path: pathLabel(String(f['path'] ?? '')), op };
        if (op === 'reorder') {
          out.from = arr(f['from']).map(String).join(', ');
          out.to = arr(f['to']).map(String).join(', ');
        } else {
          if ('from' in f) out.from = valueText(f['from']);
          if ('to' in f) out.to = valueText(f['to']);
        }
        return [out];
      }),
      truncated: c['truncated'] === true,
      secrets: arr(c['secrets']).flatMap((s0): SecretRow[] => {
        const s = obj(s0);
        const field = str(s['field']);
        const a = str(s['action']) as SecretRow['action'] | undefined;
        if (!field || !a) return [];
        const out: SecretRow = { field, action: a, inBundle: s['in_bundle'] === true };
        const source = str(s['source']);
        if (source === 'bundle' || source === 'vault') out.source = source;
        return [out];
      }),
    };
    const fileId = str(c['file_id']);
    if (fileId) row.fileId = fileId;
    const integration = str(c['integration']);
    if (integration) row.integration = integration;
    const detail = str(c['detail']);
    if (detail) row.detail = detail;
    (action === 'unchanged' ? section.unchanged : section.changed).push(row);
  });

  const summary = obj(plan['summary']);
  const bundle = obj(plan['bundle']);
  const out: BackupPreview = {
    sections: [...sections.values()].filter((s) => s.changed.length || s.unchanged.length),
    needed: arr(plan['secrets_needed']).map((n0) => {
      const n = obj(n0);
      return {
        instance: String(n['instance'] ?? ''),
        name: str(n['name']) ?? String(n['instance'] ?? '?'),
        integration: String(n['integration'] ?? ''),
        field: String(n['field'] ?? ''),
        inBundle: n['in_bundle'] === true,
      };
    }),
    bundle: { present: bundle['present'] === true, unlocked: bundle['unlocked'] === true, secrets: num(bundle['secrets']) },
    summary: {
      create: num(summary['create']),
      update: num(summary['update']),
      unchanged: num(summary['unchanged']),
      stale: num(summary['stale']),
      secretsRestored: num(summary['secrets_restored']),
      secretsNeeded: num(summary['secrets_needed']),
    },
    warnings: arr(plan['warnings']).map((w0) => {
      const w = obj(w0);
      return { code: String(w['code'] ?? ''), message: str(w['message']) ?? String(w['code'] ?? '') };
    }),
    nothingNew: false,
  };
  out.nothingNew = out.summary.create === 0 && out.summary.update === 0;
  const exportedAt = str(plan['exported_at']);
  if (exportedAt) out.exportedAt = exportedAt;
  const hubVersion = str(plan['hub_version']);
  if (hubVersion) out.hubVersion = hubVersion;
  return out;
}

/** Does this text carry an encrypted secrets bundle? (A quick look; the hub decides.) */
export function hasBundle(yaml: string): boolean {
  return /^secrets_bundle:/m.test(yaml);
}

export const ACTION_WORDS: Record<Action, string> = { create: 'New', update: 'Updated', unchanged: 'Unchanged', stale: 'Not in the file' };

export function secretWords(s: SecretRow): string {
  switch (s.action) {
    case 'needed':
      return s.inBundle ? 'needed: in the file’s encrypted bundle' : 'needed: enter it after importing';
    case 'unchanged':
      return 'kept';
    default:
      return s.source === 'bundle' ? 'restored from the bundle' : s.action === 'create' ? 'set from this hub’s vault' : 'switched to this hub’s vault';
  }
}
