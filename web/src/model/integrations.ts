/**
 * Integrations as the hub describes them (07 · v0.1 → Shared contracts):
 *
 *   GET  /api/integrations/catalog    → [{id, name, icon, version, tier, capabilities, config_schema, recipe?}]
 *   GET  /api/integrations/instances  → [{id, integration, name, status, version, last_ok, last_error, warnings}]
 *   GET  /api/integrations/instances/{id} → the same plus {config, secrets: {field: {set: true}}}
 *   POST /api/integrations/test, /instances/{id}/test → {ok, version, latency_ms, grants, warnings, error}
 *
 * Pure helpers: normalising what comes back (arrays may arrive wrapped, grants
 * as strings or objects), status words, and where a Repair's "Reconnect"
 * leads.
 */
import type { JsonSchema } from '../lib/schema';
import { secretPaths } from '../lib/schema';
import type { Recipe } from '../settings/recipe';
import type { ControlSpec } from './control';
import type { TileConfig } from '../stores/tiles';
import { appsOfType, integrationOf, resolve } from './graph';
import type { GraphDoc, Incident } from './types';

export type InstanceStatus = 'ok' | 'degraded' | 'repair' | 'error' | 'disabled';

export interface Warning {
  code: string;
  severity: 'info' | 'warning' | 'attention' | 'error' | string;
  message: string;
}

export interface CatalogEntry {
  id: string;
  name: string;
  icon?: string;
  version?: string;
  tier?: string | number;
  /** Where it sits on the Add list (CATEGORIES); missing means "Other". */
  category?: string;
  /** Other names the same upstream goes by (Seerr: Overseerr, Jellyseerr). */
  aliases?: string[];
  capabilities?: string[];
  config_schema: JsonSchema;
  /** How to make a credential with exactly the access it needs (settings/recipe.ts). */
  recipe?: Recipe;
  /** Where its actions get their credential: a control group or a mode (model/control.ts). */
  control?: ControlSpec;
}

/** The Add list's groups, in order. The hub sends the id; the words live here. */
export const CATEGORIES: { id: string; label: string; note: string }[] = [
  { id: 'media', label: 'Media', note: 'servers and requests' },
  { id: 'automation', label: 'Media automation', note: 'libraries and indexers' },
  { id: 'downloads', label: 'Downloads', note: 'torrent and usenet clients' },
  { id: 'hosts', label: 'Hosts & backups', note: 'servers, containers, backups' },
  { id: 'network', label: 'Network', note: 'DNS, routers, proxies' },
  { id: 'monitoring', label: 'Monitoring', note: 'uptime and checks' },
  { id: 'home', label: 'Home', note: 'automation, cameras, weather' },
  { id: 'files', label: 'Files & notes', note: 'photos, documents, bookmarks' },
];

/** Entries in CATEGORIES order; whatever has no known category goes last, as "Other". */
export function groupCatalog<T extends { category?: string | undefined }>(entries: readonly T[]): { id: string; label: string; note: string; items: T[] }[] {
  const known = new Set(CATEGORIES.map((c) => c.id));
  const groups = [...CATEGORIES, { id: 'other', label: 'Other', note: '' }].map((c) => ({
    ...c,
    items: entries.filter((e) => (c.id === 'other' ? !e.category || !known.has(e.category) : e.category === c.id)),
  }));
  return groups.filter((g) => g.items.length);
}

export interface Instance {
  id: string;
  integration: string;
  name: string;
  status: InstanceStatus | string;
  version?: string | null;
  last_ok?: string | null;
  last_error?: string | null;
  warnings?: Warning[];
}

export interface InstanceDetail extends Instance {
  config?: Record<string, unknown>;
  /** Write-only: the hub only ever says which are set. */
  secrets?: Record<string, { set?: boolean } | undefined>;
}

export interface Grant {
  name: string;
  access: 'read' | 'write' | string;
  detail?: string;
}

export interface TestResult {
  ok: boolean;
  version?: string | null;
  latency_ms?: number | null;
  grants?: (Grant | string | Record<string, unknown>)[] | null;
  warnings?: Warning[] | null;
  error?: string | { code?: string; message?: string } | null;
}

/** Lists may come bare or wrapped (`{instances: [...]}`), as GET /api/boards does. */
export function listOf<T>(res: unknown, key: string): T[] {
  if (Array.isArray(res)) return res as T[];
  const inner = (res as Record<string, unknown> | null)?.[key];
  return Array.isArray(inner) ? (inner as T[]) : [];
}

export const STATUS_WORDS: Record<string, string> = {
  ok: 'Reading',
  degraded: 'Degraded',
  repair: 'Needs repair',
  error: 'Error',
  disabled: 'Disabled',
};

export type StatusGlyph = 'ok' | 'attn' | 'repair' | 'down' | 'off';

export function statusGlyph(status: string): StatusGlyph {
  switch (status) {
    case 'ok':
      return 'ok';
    case 'degraded':
      return 'attn';
    case 'repair':
      return 'repair';
    case 'error':
      return 'down';
    default:
      return 'off';
  }
}

/** Which secret fields the hub holds, as paths. Anything but `{set: true}` counts as not set. */
export function secretsSet(detail: InstanceDetail | undefined): Set<string> {
  const out = new Set<string>();
  for (const [k, v] of Object.entries(detail?.secrets ?? {})) if (v && typeof v === 'object' && v.set === true) out.add(k);
  return out;
}

const WRITE_WORDS = /^(write|admin|manage|modify|control|delete|power|exec)\b|[:._-](write|admin|manage|modify|control|delete|power(mgmt)?|exec)$|\b(PowerMgmt|Allocate|Modify|Snapshot|Console)\b/i;

/** A grant, whatever shape it came in: "read:vms", "VM.PowerMgmt", {name, access}. */
export function normalizeGrant(g: Grant | string | Record<string, unknown>): Grant {
  if (typeof g === 'string') return { name: g, access: WRITE_WORDS.test(g) ? 'write' : 'read' };
  const o = g as Record<string, unknown>;
  const name = String(o['name'] ?? o['scope'] ?? o['permission'] ?? o['id'] ?? 'grant');
  let access = typeof o['access'] === 'string' ? (o['access'] as string) : o['write'] === true ? 'write' : WRITE_WORDS.test(name) ? 'write' : 'read';
  if (access === 'rw' || access === 'read-write' || access === 'readwrite' || access === 'admin') access = 'write';
  const detail = typeof o['detail'] === 'string' ? o['detail'] : typeof o['description'] === 'string' ? (o['description'] as string) : undefined;
  return detail ? { name, access, detail } : { name, access };
}

export const isWrite = (g: Grant) => g.access !== 'read' && g.access !== 'none';

/**
 * Is this connection read-only? v0.1 is a read-only release, so every
 * connection is unless its config says `read_only: false` explicitly.
 */
export const isReadOnly = (config: Record<string, unknown> | undefined) => config?.['read_only'] !== false;

export function testError(e: TestResult['error']): string {
  if (!e) return '';
  if (typeof e === 'string') return e;
  return e.message ?? e.code ?? 'The test failed.';
}

/**
 * Where a Repair's "Reconnect" goes: the instance's settings with the field
 * that failed focused. The incident targets `integration:<ref>`, where ref is
 * the instance id in real mode (the Demo Home uses names); `facts.instance`
 * and `facts.field` win when the hub sends them. Without a named field, an
 * auth failure (401/403, phase "rejected") focuses the first secret.
 */
export function repairTarget(i: Incident): { ref: string; field?: string; reason?: 'auth' } {
  const facts = i.facts ?? {};
  const fromFacts = (k: string) => (typeof facts[k] === 'string' && facts[k] ? (facts[k] as string) : undefined);
  const ref = fromFacts('instance') ?? fromFacts('instance_id') ?? i.target.replace(/^integration:/, '');
  const field = fromFacts('field') ?? fromFacts('secret');
  const status = Number(facts['status']);
  const auth = status === 401 || status === 403 || i.phase === 'rejected' || i.phase === 'unauthorized';
  const out: { ref: string; field?: string; reason?: 'auth' } = { ref };
  if (field) out.field = field;
  if (auth) out.reason = 'auth';
  return out;
}

/** Find the instance a reference names: id, then name, then the only instance of that integration. */
export function findInstance(instances: readonly Instance[], ref: string): Instance | undefined {
  return (
    instances.find((x) => x.id === ref) ??
    instances.find((x) => x.name === ref) ??
    instances.find((x) => x.name.toLowerCase() === ref.toLowerCase()) ??
    (instances.filter((x) => x.integration === ref).length === 1 ? instances.find((x) => x.integration === ref) : undefined)
  );
}

/**
 * The integration instance a tile shows, for its settings cog: the bound
 * instance, else the only one its sources read, else the one that reports
 * what it shows. That's its target (a series key counts as its resource,
 * `app:sonarr` the first Sonarr), or for a widget without one (`plex`) the
 * only app of its type; then `integration:<id>` itself, the resource's
 * `source`, or whoever `monitors` it. Undefined for what the hub reports.
 */
export function tileInstance(g: GraphDoc | undefined, type: string, config: TileConfig | undefined, target: string | undefined): string | undefined {
  const bind = config?.bind;
  if (bind?.instance) return bind.instance;
  const sources = [...new Set((bind?.sources ?? []).map((x) => x.instance))];
  if (sources.length === 1) return sources[0];
  const id = target?.split('#')[0];
  const apps = id ? [] : appsOfType(g, type);
  const r = id ? resolve(g, id) : apps.length === 1 ? apps[0] : undefined;
  const ref = id?.startsWith('integration:') ? id : r?.source?.startsWith('integration:') ? r.source : integrationOf(g, r?.id)?.id;
  return ref?.startsWith('integration:') ? ref.slice('integration:'.length) || undefined : undefined;
}

/** The field to focus when the editor opens: the named one, else the first secret for auth trouble. */
export function focusField(schema: JsonSchema | undefined, field: string | undefined, reason: string | undefined): string | undefined {
  if (field) return field;
  if (reason === 'auth' && schema) return secretPaths(schema)[0];
  return undefined;
}
